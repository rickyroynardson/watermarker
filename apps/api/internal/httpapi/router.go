package httpapi

import (
	"github.com/rickyroynardson/watermarker/apps/api/internal/metrics"
	"github.com/rickyroynardson/watermarker/apps/api/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
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
		ctx := tracing.Propagator.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		ctx, span := tracing.Start(ctx, "HTTP request", trace.SpanKindServer)
		defer span.End()
		c.Request = c.Request.WithContext(ctx)
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		span.SetName(c.Request.Method + " " + route)
		span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", c.Writer.Status()))
		if c.Writer.Status() >= 500 {
			span.SetStatus(codes.Error, "HTTP server error")
		}
		metrics.HTTP(c.Request.Context(), c.Request.Method, c.FullPath(), c.Writer.Status(), time.Since(start))
		// Route templates avoid collecting query strings, credentials, or arbitrary URLs.
		zap.L().Info("HTTP request", zap.Any("context", ctx), zap.String("http.request.method", c.Request.Method),
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
