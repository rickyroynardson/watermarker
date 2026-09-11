package httpapi

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/batch"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/rickyroynardson/watermarker/apps/api/internal/upload"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"go.uber.org/zap"
)

func NewRouter(dbpool *pgxpool.Pool, s3Storage *storage.S3) http.Handler {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		start := time.Now()
		c.Next()
		// Route templates avoid collecting query strings, credentials, or arbitrary URLs.
		zap.L().Info("HTTP request", zap.String("http.request.method", c.Request.Method),
			zap.String("http.route", c.FullPath()), zap.Int("http.response.status_code", c.Writer.Status()),
			zap.Duration("duration", time.Since(start)))
	}, gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		zap.L().Error("HTTP panic", zap.String("http.route", c.FullPath()), zap.Stack("stack"))
		c.AbortWithStatus(http.StatusInternalServerError)
	}))

	validator := utils.NewValidator()

	batchRepository := batch.NewRepository(dbpool)
	batchService := batch.NewService(batchRepository, s3Storage)
	batchHandler := batch.NewHandler(validator, batchService)

	batches := r.Group("/batches", auth.RequireAPIKey(dbpool))
	batches.GET("", batchHandler.ListBatches)
	batches.GET("/:id", batchHandler.GetBatch)
	batches.POST("", batchHandler.CreateBatch)

	uploadHandler := upload.NewHandler(validator, s3Storage)
	r.POST("/uploads/presign", auth.RequireAPIKey(dbpool), uploadHandler.Presign)

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	return r
}
