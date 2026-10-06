package metrics

import (
	"context"

	"github.com/rickyroynardson/watermarker/apps/api/internal/database"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var databaseLocks, _ = meter.Int64Gauge("watermarker.db.lock.sessions")
var idleTransactions, _ = meter.Int64Gauge("watermarker.db.transaction.idle")
var oldestTransaction, _ = meter.Float64Gauge("watermarker.db.transaction.oldest.age", metric.WithUnit("s"))

func DatabaseActivity(ctx context.Context, activity database.Activity) {
	attrs := metric.WithAttributes(attribute.String("source", "database"))
	databaseLocks.Record(ctx, activity.Blocked, metric.WithAttributes(attribute.String("source", "database"), attribute.String("state", "blocked")))
	databaseLocks.Record(ctx, activity.Blocking, metric.WithAttributes(attribute.String("source", "database"), attribute.String("state", "blocking")))
	idleTransactions.Record(ctx, activity.IdleTransactions, attrs)
	oldestTransaction.Record(ctx, activity.OldestTransactionSeconds, attrs)
}
