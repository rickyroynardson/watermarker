package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var queueDepth, _ = meter.Int64Gauge("watermarker.queue.depth")
var outboxPending, _ = meter.Int64Gauge("watermarker.outbox.pending")
var outboxAge, _ = meter.Float64Gauge("watermarker.outbox.oldest.age", metric.WithUnit("s"))
var observationSuccess, _ = meter.Int64Gauge("watermarker.backlog.observation.success")
var observationTime, _ = meter.Int64Gauge("watermarker.backlog.observation.time", metric.WithUnit("s"))

func QueueDepth(ctx context.Context, queue string, depth [3]int64) {
	for i, state := range []string{"visible", "in_flight", "delayed"} {
		queueDepth.Record(ctx, depth[i], metric.WithAttributes(attribute.String("source", queue), attribute.String("state", state)))
	}
}

func OutboxBacklog(ctx context.Context, count int64, age float64) {
	attrs := metric.WithAttributes(attribute.String("source", "outbox"))
	outboxPending.Record(ctx, count, attrs)
	outboxAge.Record(ctx, age, attrs)
}

// Timestamp freshness distinguishes a stopped monitor from an empty queue.
func BacklogObservation(ctx context.Context, source string, err error) {
	success := int64(1)
	if err != nil {
		success = 0
	}
	attrs := metric.WithAttributes(attribute.String("source", source))
	observationSuccess.Record(ctx, success, attrs)
	observationTime.Record(ctx, time.Now().Unix(), attrs)
}

var unresolvedFailures, _ = meter.Int64Gauge("watermarker.images.unresolved")

func UnresolvedFailures(ctx context.Context, count int64) {
	unresolvedFailures.Record(ctx, count, metric.WithAttributes(attribute.String("source", "failed_images")))
}

var cleanupObjects, _ = meter.Int64Gauge("watermarker.cleanup.objects")

func CleanupObjects(ctx context.Context, counts [3]int64) {
	for i, state := range []string{"pending", "failed", "deleted"} {
		cleanupObjects.Record(ctx, counts[i], metric.WithAttributes(attribute.String("source", "cleanup"), attribute.String("state", state)))
	}
}
