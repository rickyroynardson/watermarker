package httpapi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/httpapi"
	"github.com/rickyroynardson/watermarker/apps/api/internal/live"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/stretchr/testify/require"
)

func testRateLimits(t *testing.T, db *pgxpool.Pool, objects *storage.S3, redisURL string) {
	ctx := t.Context()
	first, err := live.New(redisURL)
	require.NoError(t, err)
	defer first.Close()
	second, err := live.New(redisURL)
	require.NoError(t, err)
	defer second.Close()
	routers := []http.Handler{httpapi.NewRouter(db, objects, first), httpapi.NewRouter(db, objects, second)}
	owner := uuid.New()
	_, err = db.Exec(ctx, "INSERT INTO users(id,name) VALUES($1,'Rate limit test')", owner)
	require.NoError(t, err)
	tokens := []string{uuid.NewString(), uuid.NewString()}
	for _, token := range tokens {
		digest := sha256.Sum256([]byte(token))
		_, err = db.Exec(ctx, "INSERT INTO api_keys(id,user_id,name,key_hash) VALUES($1,$2,'Rate limit test',$3)", uuid.New(), owner, hex.EncodeToString(digest[:]))
		require.NoError(t, err)
	}
	request := func(instance int, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		routers[instance].ServeHTTP(w, r)
		return w
	}
	// Different credentials and different API processes still share one user budget.
	responses := make([]*httptest.ResponseRecorder, 60)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Go(func() { responses[i] = request(i%2, "/batches", tokens[i%2]) })
	}
	wg.Wait()
	allowed, denied := 0, 0
	for _, response := range responses {
		switch response.Code {
		case 400:
			allowed++ // Invalid payload reached the handler, so admission succeeded.
		case 429:
			denied++
			delay, err := strconv.Atoi(response.Header().Get("Retry-After"))
			require.NoError(t, err)
			require.Positive(t, delay)
			require.Contains(t, response.Body.String(), `"code":"rate_limited"`)
		default:
			t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
		}
	}
	require.Equal(t, 30, allowed)
	require.Equal(t, 30, denied)
	require.Equal(t, 401, request(0, "/batches", "invalid").Code)
	other, otherToken := uuid.New(), uuid.NewString()
	_, err = db.Exec(ctx, "INSERT INTO users(id,name) VALUES($1,'Other rate limit user')", other)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(otherToken))
	_, err = db.Exec(ctx, "INSERT INTO api_keys(id,user_id,name,key_hash) VALUES($1,$2,'Other rate limit user',$3)", uuid.New(), other, hex.EncodeToString(digest[:]))
	require.NoError(t, err)
	require.Equal(t, 400, request(0, "/batches", otherToken).Code)
	key := "watermarker:rate:" + owner.String() + ":/batches"
	ttl, err := first.Client().PTTL(ctx, key).Result()
	require.NoError(t, err)
	require.Positive(t, ttl)
	require.LessOrEqual(t, ttl, time.Minute)

	// Upload signing has its own budget; malformed authenticated requests also count.
	for range 300 {
		require.Equal(t, 400, request(0, "/uploads/presign", tokens[0]).Code)
	}
	require.Equal(t, 429, request(1, "/uploads/presign", tokens[1]).Code)
	_, err = first.Client().PExpire(ctx, key, time.Millisecond).Result()
	require.NoError(t, err)
	require.Eventually(t, func() bool { return first.Client().Exists(ctx, key).Val() == 0 }, time.Second, time.Millisecond)
	require.Equal(t, 400, request(1, "/batches", tokens[1]).Code)

	// Redis failure must not silently remove write admission limits.
	require.NoError(t, second.Close())
	require.Equal(t, 503, request(1, "/batches", tokens[0]).Code)
	w := httptest.NewRecorder()
	routers[1].ServeHTTP(w, httptest.NewRequest("GET", "/ping", nil))
	require.Equal(t, 200, w.Code)
}
