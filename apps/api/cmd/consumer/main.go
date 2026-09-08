package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/rickyroynardson/watermarker/apps/api/internal/database"
	"github.com/rickyroynardson/watermarker/apps/api/internal/image"
	"github.com/rickyroynardson/watermarker/apps/api/internal/logger"
	"github.com/rickyroynardson/watermarker/apps/api/internal/outbox"
	"github.com/rickyroynardson/watermarker/apps/api/internal/queue"
	"go.uber.org/zap"
)

func main() {
	log := logger.New()
	defer log.Sync()
	zap.ReplaceGlobals(log)
	if err := godotenv.Load(); err != nil {
		log.Warn("no .env file loaded", zap.Error(err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := database.ConnectPgx(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("configure database", zap.Error(err))
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatal("ping database", zap.Error(err))
	}
	jobs, err := queue.NewSQS(ctx, os.Getenv("SQS_JOBS_QUEUE_URL"))
	if err != nil {
		log.Fatal("configure SQS_JOBS_QUEUE_URL", zap.Error(err))
	}
	results, err := queue.NewSQS(ctx, os.Getenv("SQS_RESULTS_QUEUE_URL"))
	if err != nil {
		log.Fatal("configure SQS_RESULTS_QUEUE_URL", zap.Error(err))
	}
	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		outbox.Run(ctx, db, jobs.Send)
	}()
	results.Consume(ctx, image.NewResultHandler(db).Handle)
	<-dispatcherDone
	log.Info("consumer exiting")
}
