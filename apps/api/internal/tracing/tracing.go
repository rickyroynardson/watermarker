// Package tracing carries W3C trace context through durable JSON messages.
package tracing

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var Propagator = propagation.TraceContext{}

func New(service string) func() {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func() {}
	}
	ctx := context.Background()
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", service)), resource.WithFromEnv())
	if err != nil {
		zap.L().Error("configure trace resource", zap.Error(err))
		return func() {}
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithTimeout(5*time.Second))
	if err != nil {
		zap.L().Error("configure trace exporter", zap.Error(err))
		return func() {}
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exporter))
	otel.SetTracerProvider(provider)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			zap.L().Error("flush traces", zap.Error(err))
		}
	}
}

func Carrier(ctx context.Context) propagation.MapCarrier {
	carrier := propagation.MapCarrier{}
	Propagator.Inject(ctx, carrier)
	return carrier
}

// Missing or malformed optional telemetry must not invalidate a job.
func Extract(ctx context.Context, body string) context.Context {
	var envelope struct {
		Trace json.RawMessage `json:"trace_context"`
	}
	var carrier propagation.MapCarrier
	if json.Unmarshal([]byte(body), &envelope) != nil || json.Unmarshal(envelope.Trace, &carrier) != nil {
		return ctx
	}
	return Propagator.Extract(ctx, carrier)
}

func Inject(ctx context.Context, body string) (string, error) {
	carrier := Carrier(ctx)
	if len(carrier) == 0 {
		return body, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return "", err
	}
	if envelope == nil {
		return body, nil
	}
	envelope["trace_context"], _ = json.Marshal(carrier)
	encoded, err := json.Marshal(envelope)
	return string(encoded), err
}

func Start(ctx context.Context, name string, kind trace.SpanKind) (context.Context, trace.Span) {
	return otel.Tracer("watermarker").Start(ctx, name, trace.WithSpanKind(kind))
}

func End(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, "operation failed")
	}
	span.End()
}
