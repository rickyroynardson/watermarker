package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	apitrace "go.opentelemetry.io/otel/trace"
)

func TestDurablePropagation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := trace.NewTracerProvider(trace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(old)
	ctx, root := Start(context.Background(), "request", apitrace.SpanKindServer)
	body, err := Inject(ctx, `{"version":1}`)
	if err != nil {
		t.Fatal(err)
	}
	root.End() // Dispatch happens after the request, using only persisted JSON.
	ctx, publish := Start(Extract(context.Background(), body), "publish", apitrace.SpanKindProducer)
	body, err = Inject(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	End(publish, nil)
	for range 2 { // Redelivery stays in the same trace with distinct attempt spans.
		_, consume := Start(Extract(context.Background(), body), "consume", apitrace.SpanKindConsumer)
		End(consume, nil)
	}
	spans := recorder.Ended()
	for _, span := range spans[1:] {
		if span.SpanContext().TraceID() != root.SpanContext().TraceID() {
			t.Fatal("trace disconnected")
		}
	}
	if spans[1].Parent().SpanID() != root.SpanContext().SpanID() || spans[2].Parent().SpanID() != publish.SpanContext().SpanID() || spans[2].SpanContext().SpanID() == spans[3].SpanContext().SpanID() {
		t.Fatal("incorrect parentage or attempt IDs")
	}
	for _, body := range []string{`{}`, `null`, `invalid`, `{"trace_context":42}`, `{"trace_context":{"traceparent":"bad"}}`} {
		if apitrace.SpanContextFromContext(Extract(context.Background(), body)).IsValid() {
			t.Fatal("invalid carrier accepted")
		}
	}
}
