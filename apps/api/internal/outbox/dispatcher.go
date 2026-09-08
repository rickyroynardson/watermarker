package outbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// DispatchOne returns false when no message is currently eligible for delivery.
func DispatchOne(ctx context.Context, db *pgxpool.Pool, send func(context.Context, string) error) (bool, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id uuid.UUID
	var body string
	err = tx.QueryRow(ctx, `
		SELECT image_id, payload::text FROM outbox_messages
		WHERE next_attempt_at <= now()
		ORDER BY next_attempt_at, image_id
		LIMIT 1 FOR UPDATE SKIP LOCKED;
	`).Scan(&id, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// hold one row lock during a bounded send; use leases if connection
	// pressure warrants it. A crash after SQS accepts still permits redelivery.
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	sendErr := send(sendCtx, body)
	cancel()
	if sendErr != nil {
		_, err = tx.Exec(ctx, "UPDATE outbox_messages SET next_attempt_at = clock_timestamp() + interval '5 seconds' WHERE image_id = $1", id)
	} else {
		_, err = tx.Exec(ctx, "DELETE FROM outbox_messages WHERE image_id = $1", id)
	}
	if err != nil {
		return true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return true, err
	}
	if sendErr != nil {
		return true, fmt.Errorf("dispatch image %s: %w", id, sendErr)
	}
	return true, nil
}

func Run(ctx context.Context, db *pgxpool.Pool, send func(context.Context, string) error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		sent, err := DispatchOne(ctx, db, send)
		if err != nil && ctx.Err() == nil {
			zap.L().Error("dispatch outbox", zap.Error(err))
		}
		if sent && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
