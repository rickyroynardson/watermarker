package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"go.uber.org/zap"
)

const apiKeyIDKey = "api_key_id"

func RequireAPIKey(dbpool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !ok || key == "" {
			c.Abort()
			utils.RespondError(c, http.StatusUnauthorized, utils.CodeUnauthorized,
				"Missing API key. Send it as: Authorization: Bearer <key>.")
			return
		}

		sum := sha256.Sum256([]byte(key))
		var id uuid.UUID
		err := dbpool.QueryRow(c.Request.Context(),
			"SELECT id FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL",
			hex.EncodeToString(sum[:]),
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			c.Abort()
			utils.RespondError(c, http.StatusUnauthorized, utils.CodeUnauthorized,
				"Invalid or revoked API key.")
			return
		}
		if err != nil {
			zap.L().Error("auth lookup failed", zap.Error(err))
			c.Abort()
			utils.RespondError(c, http.StatusInternalServerError, utils.CodeInternal,
				"Something went wrong.")
			return
		}

		c.Set(apiKeyIDKey, id)
		c.Next()
	}
}

func APIKeyID(c *gin.Context) uuid.UUID {
	return c.MustGet(apiKeyIDKey).(uuid.UUID)
}
