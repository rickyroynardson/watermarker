package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/batch"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/rickyroynardson/watermarker/apps/api/internal/upload"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
)

func NewRouter(dbpool *pgxpool.Pool, s3Storage *storage.S3) http.Handler {
	r := gin.Default()

	validator := utils.NewValidator()

	batchRepository := batch.NewRepository(dbpool)
	batchService := batch.NewService(batchRepository, s3Storage)
	batchHandler := batch.NewHandler(validator, batchService)

	batches := r.Group("/batches", auth.RequireAPIKey(dbpool))
	batches.GET("", batchHandler.ListBatches)
	batches.POST("", batchHandler.CreateBatch)

	uploadHandler := upload.NewHandler(validator, s3Storage)
	r.POST("/uploads/presign", auth.RequireAPIKey(dbpool), uploadHandler.Presign)

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	return r
}
