package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/rickyroynardson/watermarker/apps/api/internal/auth"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"go.uber.org/zap"
)

// Count and expiry are one atomic operation, including across API replicas.
// go-redis runs this Lua on Redis; it does not translate Go code into a script.
// Go with WATCH, a transaction, and conflict retries can enforce the same limit,
// but Lua performs the check and update in one server-side execution without
// client-side retries or extra read/transaction round trips. A plain pipeline
// reduces round trips but cannot make the conditional admission decision atomic.
// ponytail: fixed windows can burst at rollover; use a token bucket if smoother admission is needed.
var admission = redis.NewScript(`
local count = tonumber(redis.call('GET', KEYS[1]) or '0')
if count >= tonumber(ARGV[1]) then
  return {0, redis.call('PTTL', KEYS[1])}
end
count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
return {1, redis.call('PTTL', KEYS[1])}
`)

// Must run after RequireUser: sessions and all API keys share the user's budget.
func rateLimit(client *redis.Client, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if client == nil {
			c.Abort()
			utils.RespondError(c, http.StatusServiceUnavailable, utils.CodeUnavailable, "Rate limiting is temporarily unavailable.")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		key := "watermarker:rate:" + auth.UserID(c).String() + ":" + c.FullPath()
		result, err := admission.Run(ctx, client, []string{key}, limit, window.Milliseconds()).Int64Slice()
		if err != nil {
			zap.L().Warn("rate limiter unavailable", zap.Error(err))
			c.Abort()
			utils.RespondError(c, http.StatusServiceUnavailable, utils.CodeUnavailable, "Rate limiting is temporarily unavailable.")
			return
		}
		if result[0] == 0 {
			c.Header("Retry-After", strconv.FormatInt(max(1, (result[1]+999)/1000), 10))
			c.Abort()
			utils.RespondError(c, http.StatusTooManyRequests, utils.CodeRateLimited, "Too many requests. Retry after the indicated delay.")
			return
		}
		c.Next()
	}
}
