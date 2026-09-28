package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/batch"
	"github.com/rickyroynardson/watermarker/apps/api/internal/live"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"go.uber.org/zap"
)

// Events uses snapshots, so reconnecting recovers missed changes without an event log.
func batchEvents(db *pgxpool.Pool, service *batch.BatchService, events *live.Events) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			utils.RespondError(c, 400, utils.CodeInvalidRequest, "invalid batch ID")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
		defer cancel()
		if events == nil {
			utils.RespondError(c, 503, utils.CodeInternal, "Live updates unavailable. Please retry.")
			return
		}
		// Register before reading the snapshot to avoid missing concurrent changes.
		updates, unsubscribe := events.Subscribe(id)
		defer unsubscribe()
		snapshot, err := service.GetBatch(ctx, auth.UserID(c), id)
		if errors.Is(err, batch.ErrBatchNotFound) {
			utils.RespondError(c, 404, "not_found", "batch not found")
			return
		}
		if err != nil {
			utils.RespondError(c, 500, utils.CodeInternal, "Could not load batch.")
			return
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-store")
		c.Header("X-Accel-Buffering", "no")
		controller := http.NewResponseController(c.Writer)
		send := func(b batch.BatchDetails) error {
			data, err := json.Marshal(b)
			if err != nil {
				return err
			}
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err = fmt.Fprintf(c.Writer, "event: batch\ndata: %s\n\n", data); err != nil {
				return err
			}
			if err := controller.Flush(); err != nil {
				return err
			}
			zap.L().Info("SSE batch event sent", zap.String("batch_id", id.String()),
				zap.String("viewer_id", fmt.Sprintf("%p", updates)), zap.String("status", b.Status))
			return nil
		}
		if send(snapshot) != nil {
			return
		}
		// Bounded connections rotate every two minutes; the next snapshot also renews signed URLs.
		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		for {
			changed := false
			select {
			case <-ctx.Done():
				return
			case <-updates:
				changed = true
			case <-heartbeat.C:
			}
			// Recheck credentials even on a quiet stream, bounding logout/revocation visibility to 15s.
			valid, err := auth.Active(ctx, db, c)
			if err != nil || !valid {
				return
			}
			if changed {
				snapshot, err = service.GetBatch(ctx, auth.UserID(c), id)
				if err != nil || send(snapshot) != nil {
					return
				}
			} else {
				_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if _, err = fmt.Fprint(c.Writer, ": keepalive\n\n"); err != nil {
					return
				}
				if controller.Flush() != nil {
					return
				}
			}
		}
	}
}
