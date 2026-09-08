package image

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Result struct {
	Version   int       `json:"version"`
	JobType   string    `json:"job_type"`
	BatchID   uuid.UUID `json:"batch_id"`
	ImageID   uuid.UUID `json:"image_id"`
	Status    string    `json:"status"`
	OutputKey string    `json:"output_key,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func (r Result) Validate() error {
	if r.Version != 1 || r.JobType != "composite" || r.BatchID == uuid.Nil || r.ImageID == uuid.Nil {
		return errors.New("invalid result version, job type, or IDs")
	}
	switch r.Status {
	case "done":
		prefix := "processed/" + r.BatchID.String() + "/" + r.ImageID.String()
		ext := strings.TrimPrefix(r.OutputKey, prefix)
		if !strings.HasPrefix(r.OutputKey, prefix) || r.Error != "" || (ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp") {
			return errors.New("done result must contain its image's output key and no error")
		}
	case "failed":
		if r.OutputKey != "" || strings.TrimSpace(r.Error) == "" || len(r.Error) > 4096 {
			return errors.New("failed result must contain an error of 1-4096 bytes and no output key")
		}
	default:
		return errors.New("result status must be done or failed")
	}
	return nil
}

type ResultHandler struct {
	db *pgxpool.Pool
}

func NewResultHandler(db *pgxpool.Pool) *ResultHandler {
	return &ResultHandler{db: db}
}

func (h *ResultHandler) Handle(ctx context.Context, body string) error {
	var result Result
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return err
	}
	if err := result.Validate(); err != nil {
		return err
	}
	// the first terminal result wins, including when consumers race. A repeated
	// result after a failed SQS acknowledgement must not change updated_at either.
	tag, err := h.db.Exec(ctx, `
		UPDATE images SET status = $3, output_key = NULLIF($4, ''),
			error = NULLIF($5, ''), updated_at = now()
		WHERE id = $1 AND batch_id = $2 AND status = 'pending';
	`, result.ImageID, result.BatchID, result.Status, result.OutputKey, result.Error)
	if err != nil || tag.RowsAffected() != 0 {
		return err
	}
	var exists bool
	if err := h.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM images WHERE id = $1 AND batch_id = $2)", result.ImageID, result.BatchID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("result references an unknown image or batch")
	}
	return nil
}
