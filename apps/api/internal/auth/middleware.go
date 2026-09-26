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

const sessionCookie = "watermarker_session"
const loginCookie = "watermarker_login"

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Explicit bearer credentials take precedence: invalid keys never fall back to cookies.
func RequireUser(db *pgxpool.Pool, origin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var id uuid.UUID
		var err error
		if header := c.GetHeader("Authorization"); header != "" {
			key, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || key == "" {
				unauthorized(c)
				return
			}
			err = db.QueryRow(c.Request.Context(), "SELECT user_id FROM api_keys WHERE key_hash=$1 AND revoked_at IS NULL", hash(key)).Scan(&id)
		} else {
			if origin == "" {
				unauthorized(c)
				return
			}
			token, cookieErr := c.Cookie(sessionCookie)
			if cookieErr != nil || token == "" {
				unauthorized(c)
				return
			}
			// Cookies are ambient credentials. Require our exact frontend Origin for writes.
			if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
				if origin == "" || c.GetHeader("Origin") != origin {
					c.AbortWithStatus(http.StatusForbidden)
					return
				}
			}
			digest := hash(token)
			err = db.QueryRow(c.Request.Context(), "SELECT user_id FROM auth_sessions WHERE token_hash=$1 AND expires_at>now()", digest).Scan(&id)
			c.Set("session_hash", digest)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			unauthorized(c)
			return
		}
		if err != nil {
			zap.L().Error("auth lookup failed", zap.Error(err))
			c.Abort()
			utils.RespondError(c, 500, utils.CodeInternal, "Something went wrong.")
			return
		}
		c.Set("user_id", id)
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func unauthorized(c *gin.Context) {
	c.Abort()
	utils.RespondError(c, 401, utils.CodeUnauthorized, "Sign in or provide a valid API key.")
}

func UserID(c *gin.Context) uuid.UUID { return c.MustGet("user_id").(uuid.UUID) }

func sessionOnly(c *gin.Context) {
	if _, ok := c.Get("session_hash"); !ok {
		c.AbortWithStatus(403)
		return
	}
	c.Next()
}
