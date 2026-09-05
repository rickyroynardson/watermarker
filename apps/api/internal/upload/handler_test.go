package upload

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestPresign(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "test-session")
	t.Setenv("AWS_ENDPOINT_URL", "http://127.0.0.1:1")
	t.Setenv("AWS_ENDPOINT_URL_S3", "http://127.0.0.1:1")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/credentials")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	s, err := storage.NewS3(t.Context(), "watermarker")
	require.NoError(t, err)
	h := NewHandler(utils.NewValidator(), s)
	owner := uuid.New()
	send := func(handler *UploadHandler, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/uploads/presign", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("api_key_id", owner)
		handler.Presign(c)
		return w
	}

	for _, contentType := range []string{"image/png", "image/jpeg", "image/webp"} {
		t.Run("accepts/"+contentType, func(t *testing.T) {
			w := send(h, `{"content_type":"`+contentType+`"}`)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			var body struct {
				Data storage.S3Upload `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			upload := body.Data
			keyID, ok := strings.CutPrefix(upload.Key, "uploads/"+owner.String()+"/")
			require.True(t, ok)
			_, err := uuid.Parse(keyID)
			require.NoError(t, err)
			require.NotEmpty(t, upload.URL)
			require.Equal(t, upload.Key, upload.Fields["key"])
			require.Equal(t, contentType, upload.Fields["Content-Type"])
			require.Positive(t, upload.ExpiresIn)
		})
	}

	for _, tt := range []struct {
		name string
		body string
	}{
		{"malformed JSON", `{`},
		{"missing content type", `{}`},
		{"non-string content type", `{"content_type":123}`},
		{"unsupported content type", `{"content_type":"image/svg+xml"}`},
		// Otherwise valid JSON ensures this fails only because of the body limit.
		{"oversized body", `{"content_type":"image/png","padding":"` + strings.Repeat("x", 64*1024) + `"}`},
	} {
		t.Run("rejects/"+tt.name, func(t *testing.T) {
			// Invalid requests must return before accessing storage.
			w := send(NewHandler(utils.NewValidator(), nil), tt.body)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			var body utils.ErrorBody
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, utils.CodeInvalidRequest, body.Error.Code)
		})
	}

	t.Run("signing failure", func(t *testing.T) {
		t.Setenv("AWS_REGION", "")
		t.Setenv("AWS_DEFAULT_REGION", "")
		s, err := storage.NewS3(t.Context(), "watermarker")
		require.NoError(t, err)
		w := send(NewHandler(utils.NewValidator(), s), `{"content_type":"image/png"}`)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.JSONEq(t, `{"error":{"code":"internal","message":"something went wrong"}}`, w.Body.String())
	})
}
