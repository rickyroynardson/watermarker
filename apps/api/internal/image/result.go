package image

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/rickyroynardson/watermarker/apps/api/internal/live"
	"github.com/rickyroynardson/watermarker/apps/api/internal/metrics"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Result struct {
	Attempt     int       `json:"attempt"`
	Version     int       `json:"version"`
	JobType     string    `json:"job_type"`
	BatchID     uuid.UUID `json:"batch_id"`
	ImageID     uuid.UUID `json:"image_id"`
	Status      string    `json:"status"`
	OutputBytes int64     `json:"output_bytes,omitempty"`
	OutputKey   string    `json:"output_key,omitempty"`
	Error       string    `json:"error,omitempty"`
}

func (r Result) Validate() error {
	if r.OutputBytes < 0 || r.OutputBytes > 128*1024*1024 {
		return errors.New("invalid output byte size")
	}
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
		if r.OutputBytes != 0 || r.OutputKey != "" || strings.TrimSpace(r.Error) == "" || len(r.Error) > 4096 {
			return errors.New("failed result must contain an error of 1-4096 bytes and no output key")
		}
	default:
		return errors.New("result status must be done or failed")
	}
	return nil
}

type ResultHandler struct {
	db     *pgxpool.Pool
	events *live.Events
}

func NewResultHandler(db *pgxpool.Pool, events ...*live.Events) *ResultHandler {
	h := &ResultHandler{db: db}
	if len(events) > 0 {
		h.events = events[0]
	}
	return h
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
	result.OutputBytes = 0
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
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(823091)"); err != nil {
		return err
	}
	var owner uuid.UUID
	if err := tx.QueryRow(ctx, "SELECT user_id FROM batches WHERE id = $1 FOR UPDATE", result.BatchID).Scan(&owner); err != nil {
		return err
	}
	if result.Status == "done" {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM images WHERE id=$1 AND batch_id=$2)", result.ImageID, result.BatchID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("result references an unknown image or batch")
		}
		// Charge an existing object even if cancellation or a stale attempt makes its result inert.
		// Share the account lock with upload admission so concurrent uploads see the new usage.
		if _, err = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", owner); err != nil {
			return err
		}
		// Compatibility with queued results from older workers.
		bytes := result.OutputBytes
		if bytes == 0 {
			bytes = 80 * 1024 * 1024
		}
		_, err = tx.Exec(ctx, `INSERT INTO output_storage(key,batch_id,user_id,bytes) VALUES($1,$2,$3,$4)
          ON CONFLICT(key) DO UPDATE SET bytes=CASE WHEN $5::bigint>0 THEN EXCLUDED.bytes ELSE output_storage.bytes END`, result.OutputKey, result.BatchID, owner, bytes, result.OutputBytes)
		if err != nil {
			return err
		}
		// A suspended worker can report an object after cleanup finished. Conservatively
		// charge it again and repeat deletion; a delayed duplicate is safe to delete again.
		_, err = tx.Exec(ctx, `UPDATE cleanup_objects SET deleted_at=NULL,delete_after=now()+interval '24 hours',
          next_attempt_at=now(),last_error=NULL WHERE key=$1 AND deleted_at IS NOT NULL`, result.OutputKey)
		if err != nil {
			return err
		}
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
	h.events.Publish(ctx, result.BatchID)
	metrics.ImageCompleted(ctx, result.Status)
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
