package httpapi

import (
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
	if len(fields) != 4 || fields["http.route"] != "/ping" || fields["http.response.status_code"] != int64(200) {
		t.Fatalf("unexpected request fields: %v", fields)
	}
}
