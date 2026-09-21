package httpapi

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRequestLogOmitsSecrets(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	request := httptest.NewRequest(http.MethodGet, "/ping?token=secret", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	NewRouter(nil, nil).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d", response.Code)
	}
	entries := logs.FilterMessage("HTTP request").All()
	if len(entries) != 1 {
		t.Fatalf("got %d request logs", len(entries))
	}
	fields := entries[0].ContextMap()
	if len(fields) != 5 || fields["http.route"] != "/ping" || fields["http.response.status_code"] != int64(200) {
		t.Fatalf("unexpected request fields: %v", fields)
	}
}

func TestHTTPTraceUsesRemoteParentAndRouteTemplate(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := trace.NewTracerProvider(trace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(old)
	request := httptest.NewRequest(http.MethodGet, "/ping?token=secret", nil)
	request.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	NewRouter(nil, nil).ServeHTTP(httptest.NewRecorder(), request)
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "GET /ping" || spans[0].SpanContext().TraceID().String() != "0123456789abcdef0123456789abcdef" || spans[0].Parent().SpanID().String() != "0123456789abcdef" {
		t.Fatalf("unexpected HTTP trace: %+v", spans)
	}
}
