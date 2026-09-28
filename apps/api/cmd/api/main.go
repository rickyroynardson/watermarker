package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/database"
	"github.com/rickyroynardson/watermarker/apps/api/internal/httpapi"
	"github.com/rickyroynardson/watermarker/apps/api/internal/live"
	"github.com/rickyroynardson/watermarker/apps/api/internal/logger"
	"github.com/rickyroynardson/watermarker/apps/api/internal/metrics"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/rickyroynardson/watermarker/apps/api/internal/tracing"
	"go.uber.org/zap"
)

func main() {
	envErr := godotenv.Load()

	logger, shutdownLogs := logger.New("watermarker-api")
	defer shutdownLogs()
	zap.ReplaceGlobals(logger)
	shutdownMetrics := metrics.New("watermarker-api")
	defer shutdownMetrics()
	shutdownTraces := tracing.New("watermarker-api")
	defer shutdownTraces()

	if envErr != nil && !errors.Is(envErr, os.ErrNotExist) {
		logger.Warn("could not load optional .env file", zap.Error(envErr))
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

	login, err := auth.FromEnv(context.Background(), dbpool)
	if err != nil {
		logger.Fatal("configure sign-in", zap.Error(err))
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	events, err := live.FromEnv()
	if err != nil {
		logger.Fatal("configure Redis URL", zap.Error(err))
	}
	defer events.Close()
	stopEvents, err := events.Listen(rootCtx)
	if err != nil {
		logger.Fatal("subscribe to Redis", zap.Error(err))
	}
	defer stopEvents()
	srv := &http.Server{
		BaseContext: func(net.Listener) context.Context { return rootCtx },
		Addr:        ":8080",
		Handler:     httpapi.NewRouter(dbpool, s3Storage, events, login),
	}

	logger.Info("API started", zap.String("address", srv.Addr))
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("listen", zap.Error(err))
		}
	}()

	<-rootCtx.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("server shutdown", zap.Error(err))
	}
	logger.Info("server exiting")
}
