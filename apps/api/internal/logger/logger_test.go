package logger

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	collector "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func TestOTLPExportAndShutdown(t *testing.T) {
	var requests []*collector.ExportLogsServiceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		request := new(collector.ExportLogsServiceRequest)
		if err := proto.Unmarshal(body, request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	t.Setenv("APP_ENV", "production")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", server.URL+"/v1/logs")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_COMPRESSION", "none")
	t.Setenv("OTEL_SERVICE_NAME", "test-api")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=test")
	log, shutdown := New("watermarker-api")
	log.Debug("must not export")
	log.Info("export check", zap.String("image_id", "image-123"))
	shutdown()
	server.Close() // Wait for receiver handlers before inspecting their records.
	if len(requests) != 1 {
		t.Fatalf("got %d export requests", len(requests))
	}
	resourceLogs := requests[0].ResourceLogs[0]
	attrs := map[string]string{}
	for _, attr := range resourceLogs.Resource.Attributes {
		attrs[attr.Key] = attr.Value.GetStringValue()
	}
	if attrs["service.name"] != "test-api" || attrs["deployment.environment.name"] != "test" {
		t.Fatalf("resource: %v", attrs)
	}
	records := resourceLogs.ScopeLogs[0].LogRecords
	if len(records) != 1 || records[0].Body.GetStringValue() != "export check" || records[0].SeverityText != "info" {
		t.Fatalf("records: %v", records)
	}
	found := false
	for _, attr := range records[0].Attributes {
		if attr.Key == "image_id" && attr.Value.GetStringValue() == "image-123" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing structured field")
	}
}

func TestConsoleOnly(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	log, shutdown := New("watermarker-api")
	log.Info("console only")
	shutdown()
}
