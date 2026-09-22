package image

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/rickyroynardson/watermarker/apps/api/internal/metrics"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Result struct {
	Attempt   int       `json:"attempt"`
	Version   int       `json:"version"`
	JobType   string    `json:"job_type"`
	BatchID   uuid.UUID `json:"batch_id"`
	ImageID   uuid.UUID `json:"image_id"`
	Status    string    `json:"status"`
	OutputKey string    `json:"output_key,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func (r Result) Validate() error {
	if r.Attempt < 0 || r.Version != 1 || r.JobType != "composite" || r.BatchID == uuid.Nil || r.ImageID == uuid.Nil {
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

func (h *ResultHandler) Handle(ctx context.Context, body string) (err error) {
	start := time.Now()
	defer func() { metrics.Message(ctx, "process_result", start, err) }()
	var result Result
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return err
	}
	if err := result.Validate(); err != nil {
		return err
	}
	return h.apply(ctx, result, false)
}

// HandleDeadJob persists exhausted jobs before the queue acknowledges them.
func (h *ResultHandler) HandleDeadJob(ctx context.Context, body string) error {
	var result Result
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return err
	}
	result.Status = "failed"
	result.OutputKey = ""
	result.Error = "Automatic attempts exhausted. Check worker logs and fix the cause before retrying."
	if err := result.Validate(); err != nil {
		return err
	}
	return h.apply(ctx, result, true)
}

func (h *ResultHandler) apply(ctx context.Context, result Result, retryable bool) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize result commits per batch so concurrent final images cannot miss completion.
	// per-batch lock and image scan; use terminal counters if large batches contend.
	var batchID uuid.UUID
	if err := tx.QueryRow(ctx, "SELECT id FROM batches WHERE id = $1 FOR UPDATE", result.BatchID).Scan(&batchID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE images SET status = $3, output_key = NULLIF($4, ''),
			error = NULLIF($5, ''), retryable = $7, updated_at = clock_timestamp()
		WHERE id = $1 AND batch_id = $2 AND status = 'pending' AND attempt = $6;
	`, result.ImageID, result.BatchID, result.Status, result.OutputKey, result.Error, result.Attempt, retryable)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM images WHERE id = $1 AND batch_id = $2)", result.ImageID, result.BatchID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("result references an unknown image or batch")
		}
		return tx.Commit(ctx)
	}
	var seconds float64
	var failed bool
	err = tx.QueryRow(ctx, `
		UPDATE batches b SET completed_at = clock_timestamp()
		WHERE id = $1 AND completed_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM images WHERE batch_id = b.id AND status = 'pending')
		RETURNING EXTRACT(EPOCH FROM (completed_at - created_at))::double precision,
		  EXISTS (SELECT 1 FROM images WHERE batch_id = b.id AND status = 'failed')
	`, result.BatchID).Scan(&seconds, &failed)
	completed := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if completed {
		metrics.BatchCompleted(ctx, seconds, failed)
	}
	return nil
}

// UnresolvedFailures counts exhausted jobs awaiting an explicit retry.
func UnresolvedFailures(ctx context.Context, db *pgxpool.Pool) (count int64, err error) {
	err = db.QueryRow(ctx, "SELECT count(*) FROM images WHERE status = 'failed' AND retryable").Scan(&count)
	return
}
