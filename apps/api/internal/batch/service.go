package batch

import (
	"context"

	"github.com/google/uuid"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
)

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
		res.NextCursor = utils.NextCursor(hasMore, last.CreatedAt, last.ID)
	}
	return res, nil
}
