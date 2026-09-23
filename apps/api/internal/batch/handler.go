package batch

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"go.uber.org/zap"
)

type BatchHandler struct {
	validator *validator.Validate
	service   *BatchService
}

func NewHandler(v *validator.Validate, s *BatchService) *BatchHandler {
	return &BatchHandler{
		validator: v,
		service:   s,
	}
}

func (h *BatchHandler) ListBatches(c *gin.Context) {
	var req ListBatchesRequest

	if err := c.ShouldBindQuery(&req); err != nil {
		zap.L().Debug("list batches: bad query", zap.Error(err))
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidRequest,
			"limit must be an integer between 1 and 100")
		return
	}

	res, err := h.service.ListBatches(c.Request.Context(), auth.APIKeyID(c), req.CursorPagination)
	if errors.Is(err, utils.ErrInvalidCursor) {
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidCursor,
			"cursor is malformed or truncated. omit it to start from the first page")
		return
	}
	if err != nil {
		zap.L().Error("list batches", zap.Error(err))
		utils.RespondError(c, http.StatusInternalServerError, utils.CodeInternal,
			"something went wrong")
		return
	}

	utils.RespondSuccess(c, http.StatusOK, res)
}

func (h *BatchHandler) CreateBatch(c *gin.Context) {
	var req CreateBatchRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Debug("create batch: bad json", zap.Error(err))
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidRequest, "invalid request")
		return
	}

	req.IdempotencyKey = c.GetHeader("Idempotency-Key")

	if err := h.validator.Struct(&req); err != nil {
		zap.L().Debug("create batch: invalid request", zap.Error(err))
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidRequest, utils.ValidationMessage(err))
		return
	}

	res, err := h.service.CreateBatch(c.Request.Context(), auth.APIKeyID(c), req)
	switch {
	case errors.Is(err, ErrIdempotencyConflict):
		utils.RespondError(c, http.StatusConflict, CodeIdempotencyConflict, "idempotency key was already used with a different request")
	case errors.Is(err, ErrInvalidUploadKey):
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidRequest, "watermark_key and source_keys must be uploads issued to this API key")
	case errors.Is(err, ErrUploadNotFound):
		utils.RespondError(c, http.StatusBadRequest, CodeUploadNotFound, "one or more uploads were not found")
	case err != nil:
		zap.L().Error("create batch", zap.Error(err))
		utils.RespondError(c, http.StatusInternalServerError, utils.CodeInternal, "something went wrong")
	default:
		utils.RespondSuccess(c, http.StatusCreated, res)
	}

}

func (h *BatchHandler) GetBatch(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.RespondError(c, http.StatusBadRequest, utils.CodeInvalidRequest, "invalid batch ID")
		return
	}
	res, err := h.service.GetBatch(c.Request.Context(), auth.APIKeyID(c), id)
	switch {
	case errors.Is(err, ErrBatchNotFound):
		utils.RespondError(c, http.StatusNotFound, "not_found", "batch not found")
	case err != nil:
		zap.L().Error("get batch", zap.Error(err))
		utils.RespondError(c, http.StatusInternalServerError, utils.CodeInternal, "something went wrong")
	default:
		utils.RespondSuccess(c, http.StatusOK, res)
	}
}

func (h *BatchHandler) RetryImage(c *gin.Context) {
	batchID, err := uuid.Parse(c.Param("id"))
	imageID, imageErr := uuid.Parse(c.Param("imageID"))
	var req struct {
		Attempt *int `json:"attempt"`
	}
	if err != nil || imageErr != nil || c.ShouldBindJSON(&req) != nil || req.Attempt == nil || *req.Attempt < 0 || *req.Attempt >= 2147483647 {
		utils.RespondError(c, 400, utils.CodeInvalidRequest, "valid IDs and a nonnegative attempt are required")
		return
	}
	err = h.service.repository.RetryImage(c.Request.Context(), auth.APIKeyID(c), batchID, imageID, *req.Attempt)
	switch {
	case errors.Is(err, ErrBatchNotFound):
		utils.RespondError(c, 404, "not_found", "batch or image not found")
	case errors.Is(err, ErrRetryConflict):
		utils.RespondError(c, 409, "retry_conflict", "image is not retryable at this attempt; refresh its state")
	case err != nil:
		zap.L().Error("retry image", zap.Error(err))
		utils.RespondError(c, 500, utils.CodeInternal, "something went wrong")
	default:
		utils.RespondSuccess(c, 202, gin.H{"id": imageID})
	}
}

func (h *BatchHandler) Cancel(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.RespondError(c, 400, utils.CodeInvalidRequest, "invalid batch ID")
		return
	}
	err = h.service.repository.Cancel(c.Request.Context(), auth.APIKeyID(c), id)
	switch {
	case errors.Is(err, ErrBatchNotFound):
		utils.RespondError(c, 404, "not_found", "batch not found")
	case errors.Is(err, ErrCancelConflict):
		utils.RespondError(c, 409, "cancel_conflict", "batch has no remaining work to cancel")
	case err != nil:
		zap.L().Error("cancel batch", zap.Error(err))
		utils.RespondError(c, 500, utils.CodeInternal, "something went wrong")
	default:
		utils.RespondSuccess(c, 200, gin.H{"id": id})
	}
}
