package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rickyroynardson/watermarker/apps/api/internal/database"
	"github.com/rickyroynardson/watermarker/apps/api/internal/httpapi"
	"github.com/rickyroynardson/watermarker/apps/api/internal/logger"
	"github.com/rickyroynardson/watermarker/apps/api/internal/metrics"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"go.uber.org/zap"
)

func main() {
	envErr := godotenv.Load()

	logger, shutdownLogs := logger.New("watermarker-api")
	defer shutdownLogs()
	zap.ReplaceGlobals(logger)
	shutdownMetrics := metrics.New("watermarker-api")
	defer shutdownMetrics()

	if envErr != nil {
		logger.Warn("no .env file loaded", zap.Error(envErr))
	}

	dbpool, err := database.ConnectPgx(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		logger.Fatal("unable to create connection pool", zap.Error(err))
	}
	defer dbpool.Close()

	if err := dbpool.Ping(context.Background()); err != nil {
		logger.Fatal("unable to ping database", zap.Error(err))
	}

	s3Storage, err := storage.NewS3(context.Background(), os.Getenv("S3_BUCKET"))
	if err != nil {
		logger.Fatal("unable to configure S3", zap.Error(err))
	}

	srv := &http.Server{
		Addr:    ":8080",
		Handler: httpapi.NewRouter(dbpool, s3Storage),
	}

	logger.Info("API started", zap.String("address", srv.Addr))
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("listen", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("server shutdown", zap.Error(err))
	}
	logger.Info("server exiting")
}
