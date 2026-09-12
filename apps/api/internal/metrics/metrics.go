package metrics

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.uber.org/zap"
)

var meter = otel.Meter("watermarker")
var httpDuration, _ = meter.Float64Histogram("watermarker.http.request.duration", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10))
var messageDuration, _ = meter.Float64Histogram("watermarker.message.duration", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.01, .05, .1, .5, 1, 5, 10, 30, 60))

func New(service string) func() {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") == "" {
		return func() {}
	}
	ctx := context.Background()
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", service), attribute.String("service.instance.id", uuid.NewString())), resource.WithFromEnv())
	if err != nil {
		zap.L().Error("configure metrics resource", zap.Error(err))
		return func() {}
	}
	exporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithTimeout(5*time.Second))
	if err != nil {
		zap.L().Error("configure metrics exporter", zap.Error(err))
		return func() {}
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)))
	otel.SetMeterProvider(provider)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			zap.L().Error("flush metrics", zap.Error(err))
		}
	}
}

func HTTP(ctx context.Context, method, route string, status int, elapsed time.Duration) {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH":
	default:
		method = "_OTHER"
	}
	if route == "" {
		route = "unmatched"
	}
	httpDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attribute.String("method", method), attribute.String("route", route), attribute.Int("status", status)))
}

func Message(ctx context.Context, operation string, start time.Time, err error) {
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	messageDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("operation", operation), attribute.String("outcome", outcome)))
}
