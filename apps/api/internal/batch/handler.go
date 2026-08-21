package batch

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"go.uber.org/zap"
)

type BatchHandler struct {
	service *BatchService
}

func NewHandler(s *BatchService) *BatchHandler {
	return &BatchHandler{
		service: s,
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
	c.JSON(http.StatusCreated, gin.H{"message": "batch created"})
}
