package batch

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
)

var ErrIdempotencyConflict = errors.New("idempotency key reused with different request")

const CodeIdempotencyConflict utils.ErrorCode = "idempotency_conflict"

type BatchService struct {
	repository *BatchRepository
}

func NewService(r *BatchRepository) *BatchService {
	return &BatchService{
		repository: r,
	}
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
	// source order has no meaning, so normalize before storing/comparing.
	slices.Sort(req.SourceKeys)

	b := Batch{
		ID:             uuid.New(),
		APIKeyID:       apiKeyID,
		WatermarkKey:   req.WatermarkKey,
		IdempotencyKey: req.IdempotencyKey,
		Images:         make([]Image, len(req.SourceKeys)),
	}
	for i, key := range req.SourceKeys {
		b.Images[i] = Image{ID: uuid.New(), SourceKey: key}
	}

	stored, created, err := s.repository.CreateBatch(ctx, b)
	if err != nil {
		return CreateBatchResponse{}, err
	}

	if !created {
		storedKeys := make([]string, len(stored.Images))
		for i, img := range stored.Images {
			storedKeys[i] = img.SourceKey
		}
		if stored.WatermarkKey != b.WatermarkKey || !slices.Equal(storedKeys, req.SourceKeys) {
			return CreateBatchResponse{}, ErrIdempotencyConflict
		}
	}

	return CreateBatchResponse{ID: stored.ID}, nil
}
