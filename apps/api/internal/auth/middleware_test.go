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
		r.GET("/x", RequireUser(nil, ""), func(c *gin.Context) { t.Fatalf("handler ran for %q", h) })

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

func TestLoginConfigurationAndCookie(t *testing.T) {
	for _, cfg := range [][4]string{
		{"", "client", "", ""},
		{"http://provider.example", "client", "", "http://localhost:5173"},
		{"https://provider.example", "client", "", "http://app.example"},
		{"https://provider.example", "client", "", "https://app.example/path"},
		{"https://provider.example", "client", "", "https://app.example?redirect=other"},
	} {
		if _, err := NewLogin(t.Context(), nil, cfg[0], cfg[1], cfg[2], cfg[3]); err == nil {
			t.Fatalf("accepted invalid config: %v", cfg)
		}
	}
	if login, err := NewLogin(t.Context(), nil, "", "", "", ""); login != nil || err != nil {
		t.Fatal("empty config should disable OIDC")
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	(&Login{secure: true}).cookie(c, sessionCookie, "opaque", 60)
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" {
		t.Fatal("unsafe session cookie")
	}
}
