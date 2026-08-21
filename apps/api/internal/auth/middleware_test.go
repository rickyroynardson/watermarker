package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireApiKeyRejectsBadHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, h := range []string{"", "Bearer ", "abc123", "Basic xyz", "bearer abc"} {
		r := gin.New()
		r.GET("/x", RequireAPIKey(nil), func(c *gin.Context) { t.Fatalf("handler ran for %q", h) })

		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("header %q: got %d, want 401", h, w.Code)
		}
	}
}
