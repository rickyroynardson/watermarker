package metrics

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

// DatabasePool reads in-memory statistics at collection time; it never acquires
// a connection or runs SQL. Call the returned function before closing the pool.
// Each instrumented process has one pool, distinguished by its service resource.
func DatabasePool(db *pgxpool.Pool) func() {
	return databasePool(otel.Meter("watermarker"), db)
}

func databasePool(m metric.Meter, db *pgxpool.Pool) func() {
	connections, e1 := m.Int64ObservableGauge("watermarker.db.pool.connections")
	limit, e2 := m.Int64ObservableGauge("watermarker.db.pool.limit")
	acquires, e3 := m.Int64ObservableCounter("watermarker.db.pool.acquires")
	waits, e4 := m.Int64ObservableCounter("watermarker.db.pool.waits")
	canceled, e5 := m.Int64ObservableCounter("watermarker.db.pool.canceled")
	waitTime, e6 := m.Float64ObservableCounter("watermarker.db.pool.wait.duration", metric.WithUnit("s"))
	if err := errors.Join(e1, e2, e3, e4, e5, e6); err != nil {
		zap.L().Warn("configure database pool metrics", zap.Error(err))
		return func() {}
	}
	registration, err := m.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		stats := db.Stat()
		for state, value := range map[string]int32{
			"acquired": stats.AcquiredConns(), "idle": stats.IdleConns(), "constructing": stats.ConstructingConns(),
		} {
			observer.ObserveInt64(connections, int64(value), metric.WithAttributes(attribute.String("state", state)))
		}
		observer.ObserveInt64(limit, int64(stats.MaxConns()))
		observer.ObserveInt64(acquires, stats.AcquireCount())
		// These wait totals include only successful acquires, including connection
		// construction waits. Canceled acquires are a separate counter.
		observer.ObserveInt64(waits, stats.EmptyAcquireCount())
		observer.ObserveInt64(canceled, stats.CanceledAcquireCount())
		observer.ObserveFloat64(waitTime, stats.EmptyAcquireWaitTime().Seconds())
		return nil
	}, connections, limit, acquires, waits, canceled, waitTime)
	if err != nil {
		zap.L().Warn("register database pool metrics", zap.Error(err))
		return func() {}
	}
	return func() { _ = registration.Unregister() }
}
