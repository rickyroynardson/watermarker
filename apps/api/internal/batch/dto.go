package batch

import (
	"time"

	"github.com/google/uuid"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
)

type ListBatchItem struct {
	ID           uuid.UUID `json:"id"`
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

type CreateBatchRequest struct {
	IdempotencyKey string   `json:"-" validate:"omitempty,max=255"`
	WatermarkKey   string   `json:"watermark_key" validate:"required"`
	SourceKeys     []string `json:"source_keys" validate:"required,min=1,unique,dive,required"`
}

type CreateBatchResponse struct {
	ID uuid.UUID `json:"id"`
}

type Batch struct {
	ID             uuid.UUID `json:"id"`
	APIKeyID       uuid.UUID `json:"api_key_id"`
	WatermarkKey   string    `json:"watermark_key"`
	IdempotencyKey string    `json:"idempotency_key"`
	Images         []Image   `json:"images"`
}

type Job struct {
	Version      int       `json:"version"`
	JobType      string    `json:"job_type"`
	BatchID      uuid.UUID `json:"batch_id"`
	ImageID      uuid.UUID `json:"image_id"`
	SourceKey    string    `json:"source_key"`
	WatermarkKey string    `json:"watermark_key"`
}

type Image struct {
	ID        uuid.UUID `json:"id"`
	SourceKey string    `json:"source_key"`
}

// BatchDetails includes signed output links only for completed images.
type BatchDetails struct {
	ID           uuid.UUID      `json:"id"`
	WatermarkKey string         `json:"watermark_key"`
	CreatedAt    time.Time      `json:"created_at"`
	Status       string         `json:"status"`
	Images       []ImageDetails `json:"images"`
}

type ImageDetails struct {
	ID          uuid.UUID `json:"id"`
	SourceKey   string    `json:"source_key"`
	Status      string    `json:"status"`
	OutputKey   string    `json:"-"`
	Error       string    `json:"error,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	PreviewURL  string    `json:"preview_url,omitempty"`
	DownloadURL string    `json:"download_url,omitempty"`
}
