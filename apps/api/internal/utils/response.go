package utils

import "github.com/gin-gonic/gin"

type ErrorCode string

const (
	CodeInvalidRequest ErrorCode = "invalid_request"
	CodeInvalidCursor  ErrorCode = "invalid_cursor"
	CodeUnauthorized   ErrorCode = "unauthorized"
	CodeInternal       ErrorCode = "internal"
)

type SuccessBody struct {
	Data any `json:"data"`
}

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func RespondSuccess(c *gin.Context, status int, data any) {
	c.JSON(status, SuccessBody{Data: data})
}

func RespondError(c *gin.Context, status int, code ErrorCode, msg string) {
	c.JSON(status, ErrorBody{Error: ErrorDetail{Code: code, Message: msg}})
}
