package logger

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New keeps console logging and enables batched OTLP/HTTP export when configured.
// Call shutdown on normal exit; fatal and panic entries flush before terminating.
func New(service string) (*zap.Logger, func()) {
	cfg := zap.NewDevelopmentConfig()
	if os.Getenv("APP_ENV") == "production" {
		cfg = zap.NewProductionConfig()
	}

	log := zap.Must(cfg.Build())
	console := log
	shutdown := func() { _ = log.Sync() }
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") == "" {
		return log, shutdown
	}

	res, err := resource.New(context.Background(), resource.WithAttributes(attribute.String("service.name", service)), resource.WithFromEnv())
	if err != nil {
		log.Error("configure log resource", zap.Error(err))
		return log, shutdown
	}

	exporter, err := otlploghttp.New(context.Background(), otlploghttp.WithTimeout(5*time.Second))
	if err != nil {
		log.Error("configure log exporter", zap.Error(err))
		return log, shutdown
	}

	provider := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)))
	core, err := zapcore.NewIncreaseLevelCore(otelzap.NewCore(service, otelzap.WithLoggerProvider(provider)), cfg.Level)
	if err != nil {
		panic(err)
	}

	log = log.WithOptions(zap.WrapCore(func(console zapcore.Core) zapcore.Core {
		return zapcore.NewTee(console, core)
	}), zap.Hooks(func(entry zapcore.Entry) error {
		if entry.Level >= zapcore.DPanicLevel {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return provider.ForceFlush(ctx)
		}
		return nil
	}))

	return log, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			// Use the console core after the exporter has shut down.
			console.Error("flush logs", zap.Error(err))
		}
		_ = log.Sync()
	}
}
