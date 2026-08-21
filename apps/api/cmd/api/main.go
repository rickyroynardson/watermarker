package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/batch"
	"github.com/rickyroynardson/watermarker/apps/api/internal/database"
	"github.com/rickyroynardson/watermarker/apps/api/internal/logger"
	"go.uber.org/zap"
)

func main() {
	logger := logger.New()
	defer logger.Sync()
	zap.ReplaceGlobals(logger)

	if err := godotenv.Load(); err != nil {
		logger.Warn("no .env file loaded", zap.Error(err))
	}

	dbpool, err := database.ConnectPgx(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		logger.Fatal("unable to create connection pool", zap.Error(err))
	}
	defer dbpool.Close()

	if err := dbpool.Ping(context.Background()); err != nil {
		logger.Fatal("unable to ping database", zap.Error(err))
	}

	r := gin.Default()

	batchRepository := batch.NewRepository(dbpool)
	batchService := batch.NewService(batchRepository)
	batchHandler := batch.NewHandler(batchService)

	batches := r.Group("/batches", auth.RequireAPIKey(dbpool))
	batches.GET("", batchHandler.ListBatches)
	batches.POST("", batchHandler.CreateBatch)

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	srv := &http.Server{
		Addr:    ":8080",
		Handler: r.Handler(),
	}

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
