package upload

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/quota"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"go.uber.org/zap"
)

type UploadHandler struct {
	validator *validator.Validate
	s3        *storage.S3
	db        *pgxpool.Pool
}

func NewHandler(v *validator.Validate, s *storage.S3, db ...*pgxpool.Pool) *UploadHandler {
	h := &UploadHandler{
		validator: v,
		s3:        s,
	}
	if len(db) > 0 {
		h.db = db[0]
	}
	return h
}

func (h *UploadHandler) Presign(c *gin.Context) {
	var req PresignRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidRequest, "invalid request")
		return
	}

	if err := h.validator.Struct(&req); err != nil {
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidRequest, "Use image/jpeg, image/png, or image/webp and size_bytes between 1 and 10485760 (omit to reserve 10 MiB).")
		return
	}

	// Compatibility: older clients reserve the full upload ceiling.
	if req.SizeBytes == 0 {
		req.SizeBytes = 10 * 1024 * 1024
	}
	key := fmt.Sprintf("uploads/%s/%s", auth.UserID(c).String(), uuid.NewString())

	res, err := h.s3.PresignUpload(c.Request.Context(), key, req.ContentType, req.SizeBytes)
	if err != nil {
		zap.L().Error("presign upload", zap.Error(err))
		utils.RespondError(c, http.StatusInternalServerError, utils.CodeInternal, "something went wrong")
		return
	}
	if h.db != nil {
		err = quota.Reserve(c.Request.Context(), h.db, auth.UserID(c), strings.Replace(key, "uploads/", "sources/", 1), req.SizeBytes)
		if errors.Is(err, quota.ErrExceeded) {
			utils.RespondError(c, 409, "quota_exceeded", "Storage quota exceeded. Choose a larger plan or add extra storage.")
			return
		}
		if err != nil {
			utils.RespondError(c, 500, utils.CodeInternal, "Could not reserve upload storage.")
			return
		}
	}
	c.Header("Cache-Control", "no-store")
	utils.RespondSuccess(c, http.StatusOK, res)
}
