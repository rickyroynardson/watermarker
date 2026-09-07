package batch

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"go.uber.org/zap"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key reused with different request")
	ErrInvalidUploadKey    = errors.New("invalid upload key")
	ErrUploadNotFound      = errors.New("upload not found")
)

const (
	CodeIdempotencyConflict utils.ErrorCode = "idempotency_conflict"
	CodeUploadNotFound      utils.ErrorCode = "upload_not_found"
)

type batchRepository interface {
	ListBatches(ctx context.Context, apiKeyID uuid.UUID, before, beforeID any, limit int) ([]ListBatchItem, error)
	CreateBatch(ctx context.Context, b Batch) (Batch, error)
	FindByIdempotencyKey(ctx context.Context, apiKeyID uuid.UUID, key string) (Batch, bool, error)
}

type objectStore interface {
	Promote(ctx context.Context, src, dst string) error
	Delete(ctx context.Context, key string) error
}

type uploadPromotion struct {
	src, dst string
}

type BatchService struct {
	repository batchRepository
	objects    objectStore
}

func NewService(r batchRepository, objects objectStore) *BatchService {
	return &BatchService{
		repository: r,
		objects:    objects,
	}
}

func persistentKey(apiKeyID uuid.UUID, key string) (string, error) {
	id, ok := strings.CutPrefix(key, "uploads/"+apiKeyID.String()+"/")
	if !ok {
		return "", ErrInvalidUploadKey
	}
	if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
		return "", ErrInvalidUploadKey
	}
	return "sources/" + apiKeyID.String() + "/" + id, nil
}

func prepareBatch(apiKeyID uuid.UUID, req CreateBatchRequest) (Batch, []uploadPromotion, error) {
	// source order has no meaning, so normalize before storing/comparing.
	req.SourceKeys = slices.Clone(req.SourceKeys)
	slices.Sort(req.SourceKeys)

	watermarkKey, err := persistentKey(apiKeyID, req.WatermarkKey)
	if err != nil {
		return Batch{}, nil, err
	}
	images := make([]Image, len(req.SourceKeys))
	uploads := make([]uploadPromotion, 0, 1+len(req.SourceKeys))
	uploads = append(uploads, uploadPromotion{src: req.WatermarkKey, dst: watermarkKey})
	for i, key := range req.SourceKeys {
		p, err := persistentKey(apiKeyID, key)
		if err != nil {
			return Batch{}, nil, err
		}
		images[i] = Image{ID: uuid.New(), SourceKey: p}
		uploads = append(uploads, uploadPromotion{src: key, dst: p})
	}

	b := Batch{
		ID:             uuid.New(),
		APIKeyID:       apiKeyID,
		WatermarkKey:   watermarkKey,
		IdempotencyKey: req.IdempotencyKey,
		Images:         images,
	}

	return b, uploads, nil
}

func idempotentBatchResponse(stored, requested Batch) (CreateBatchResponse, error) {
	if stored.WatermarkKey != requested.WatermarkKey || !slices.EqualFunc(stored.Images, requested.Images, func(a, b Image) bool {
		return a.SourceKey == b.SourceKey
	}) {
		return CreateBatchResponse{}, ErrIdempotencyConflict
	}
	return CreateBatchResponse{ID: stored.ID}, nil
}

func (s *BatchService) ListBatches(ctx context.Context, apiKeyID uuid.UUID, p utils.CursorPagination) (ListBatchesResponse, error) {
	before, beforeID, err := p.Args()
	if err != nil {
		return ListBatchesResponse{}, err
	}

	rows, err := s.repository.ListBatches(ctx, apiKeyID, before, beforeID, p.QueryLimit())
	if err != nil {
		return ListBatchesResponse{}, err
	}

	batches, hasMore := utils.Page(rows, p.Limit)
	if batches == nil {
		batches = []ListBatchItem{} // JSON [], not null
	}

	res := ListBatchesResponse{Batches: batches}
	if n := len(batches); n > 0 {
		last := batches[n-1]
		res.NextCursor = utils.NextCursor(hasMore, last.CreatedAt, last.ID.String())
	}
	return res, nil
}

func (s *BatchService) CreateBatch(ctx context.Context, apiKeyID uuid.UUID, req CreateBatchRequest) (CreateBatchResponse, error) {
	b, uploads, err := prepareBatch(apiKeyID, req)
	if err != nil {
		return CreateBatchResponse{}, err
	}

	// replays and conflicts must not depend on S3 availability or change objects.
	if req.IdempotencyKey != "" {
		stored, found, err := s.repository.FindByIdempotencyKey(ctx, apiKeyID, req.IdempotencyKey)
		if err != nil {
			return CreateBatchResponse{}, err
		}
		if found {
			return idempotentBatchResponse(stored, b)
		}
	}

	// keep partial promotions for retries; add an orphan sweep if storage growth warrants it.
	for _, upload := range uploads {
		if err := s.objects.Promote(ctx, upload.src, upload.dst); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return CreateBatchResponse{}, ErrUploadNotFound
			}
			return CreateBatchResponse{}, err
		}
	}

	// the unique constraint still resolves requests racing the lookup above.
	stored, err := s.repository.CreateBatch(ctx, b)
	if err != nil {
		return CreateBatchResponse{}, err
	}
	res, err := idempotentBatchResponse(stored, b)
	if err != nil {
		return CreateBatchResponse{}, err
	}

	for _, upload := range uploads {
		if err := s.objects.Delete(ctx, upload.src); err != nil {
			zap.L().Warn("delete staging upload", zap.String("key", upload.src), zap.Error(err))
		}
	}
	return res, nil
}
