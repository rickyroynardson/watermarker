package batch

import (
	"time"

	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
)

type ListBatchItem struct {
	ID           string    `json:"id"`
	WatermarkKey string    `json:"watermark_key"`
	CreatedAt    time.Time `json:"created_at"`
}

type ListBatchesRequest struct {
	utils.CursorPagination
}

type ListBatchesResponse struct {
	Batches    []ListBatchItem `json:"batches"`
	NextCursor *string         `json:"next_cursor"`
}
