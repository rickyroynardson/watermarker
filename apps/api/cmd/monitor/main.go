// Monitor runs independently so worker/consumer outages remain observable.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rickyroynardson/watermarker/apps/api/internal/cleanup"
	"github.com/rickyroynardson/watermarker/apps/api/internal/database"
	"github.com/rickyroynardson/watermarker/apps/api/internal/image"
	"github.com/rickyroynardson/watermarker/apps/api/internal/logger"
	"github.com/rickyroynardson/watermarker/apps/api/internal/metrics"
	"github.com/rickyroynardson/watermarker/apps/api/internal/outbox"
	"github.com/rickyroynardson/watermarker/apps/api/internal/queue"
	"go.uber.org/zap"
)

func main() {
	_ = godotenv.Load()
	log, shutdownLogs := logger.New("watermarker-monitor")
	defer shutdownLogs()
	zap.ReplaceGlobals(log)
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") == "" {
		log.Error("monitor requires an OTLP metrics endpoint")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := database.ConnectPgx(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Error("configure monitor database", zap.Error(err))
		return
	}
	defer db.Close()
	queues := make(map[string]*queue.SQS)
	for name, env := range map[string]string{
		"jobs": "SQS_JOBS_QUEUE_URL", "results": "SQS_RESULTS_QUEUE_URL",
		"jobs_dlq": "SQS_JOBS_DLQ_QUEUE_URL", "results_dlq": "SQS_RESULTS_DLQ_QUEUE_URL",
	} {
		q, err := queue.NewSQS(ctx, os.Getenv(env))
		if err != nil {
			log.Error("configure monitor queue", zap.String("variable", env), zap.Error(err))
			return
		}
		queues[name] = q
	}
	// Match export cadence to polling unless explicitly configured.
	if os.Getenv("OTEL_METRIC_EXPORT_INTERVAL") == "" {
		_ = os.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "15000")
	}
	shutdownMetrics := metrics.New("watermarker-monitor")
	defer shutdownMetrics()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	log.Info("backlog monitor started")
	for ctx.Err() == nil {
		for name, q := range queues {
			pollCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			depth, err := q.Depth(pollCtx)
			cancel()
			metrics.BacklogObservation(ctx, name, err)
			if err != nil {
				log.Warn("observe queue", zap.String("queue", name), zap.Error(err))
				continue
			}
			metrics.QueueDepth(ctx, name, depth)
		}
		pollCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		count, age, err := outbox.Backlog(pollCtx, db)
		cancel()
		metrics.BacklogObservation(ctx, "outbox", err)
		if err != nil {
			log.Warn("observe outbox", zap.Error(err))
		} else {
			metrics.OutboxBacklog(ctx, count, age)
		}

		pollCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
		failures, err := image.UnresolvedFailures(pollCtx, db)
		cancel()
		metrics.BacklogObservation(ctx, "failed_images", err)
		if err != nil {
			log.Warn("observe unresolved failures", zap.Error(err))
		} else {
			metrics.UnresolvedFailures(ctx, failures)
		}
		pollCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
		counts, err := cleanup.Counts(pollCtx, db)
		cancel()
		metrics.BacklogObservation(ctx, "cleanup", err)
		if err != nil {
			log.Warn("observe cleanup", zap.Error(err))
		} else {
			metrics.CleanupObjects(ctx, counts)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
