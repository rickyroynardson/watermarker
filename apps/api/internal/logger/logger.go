package logger

import (
	"os"

	"go.uber.org/zap"
)

func New() *zap.Logger {
	if os.Getenv("APP_ENV") == "production" {
		return zap.Must(zap.NewProduction())
	}

	return zap.Must(zap.NewDevelopment())
}
