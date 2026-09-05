package upload

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"go.uber.org/zap"
)

type UploadHandler struct {
	validator *validator.Validate
	s3        *storage.S3
}

func NewHandler(v *validator.Validate, s *storage.S3) *UploadHandler {
	return &UploadHandler{
		validator: v,
		s3:        s,
	}
}

func (h *UploadHandler) Presign(c *gin.Context) {
	var req PresignRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidRequest, "invalid request")
		return
	}

	if err := h.validator.Struct(&req); err != nil {
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidRequest, "content_type must be image/jpeg, image/png, or image/webp")
		return
	}

	key := fmt.Sprintf("uploads/%s/%s", auth.APIKeyID(c).String(), uuid.NewString())

	res, err := h.s3.PresignUpload(c.Request.Context(), key, req.ContentType)
	if err != nil {
		zap.L().Error("presign uppload", zap.Error(err))
		utils.RespondError(c, http.StatusInternalServerError, utils.CodeInternal, "something went wrong")
		return
	}
	c.Header("Cache-Control", "no-store")
	utils.RespondSuccess(c, http.StatusOK, res)
}
