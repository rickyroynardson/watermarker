package utils

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	RespondSuccess(c, http.StatusOK, map[string]any{"batches": []string{}, "next_cursor": nil})
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"data":{"batches":[],"next_cursor":null}}`, w.Body.String())

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	RespondError(c, http.StatusBadRequest, CodeInvalidCursor, "The cursor is malformed.")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.JSONEq(t, `{"error":{"code":"invalid_cursor","message":"The cursor is malformed."}}`, w.Body.String())

	assert.NotContains(t, w.Body.String(), `"data"`)
}
