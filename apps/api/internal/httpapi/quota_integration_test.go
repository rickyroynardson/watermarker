package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/cleanup"
	"github.com/rickyroynardson/watermarker/apps/api/internal/httpapi"
	"github.com/rickyroynardson/watermarker/apps/api/internal/image"
	"github.com/rickyroynardson/watermarker/apps/api/internal/live"
	"github.com/rickyroynardson/watermarker/apps/api/internal/quota"
	"github.com/stretchr/testify/require"
)

func testQuotas(t *testing.T, db *pgxpool.Pool, redisURL string) {
	t.Helper()
	ctx := t.Context()
	owner := uuid.New()
	_, err := db.Exec(ctx, "INSERT INTO users(id) VALUES($1)", owner)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = db.Exec(ctx, "INSERT INTO api_keys(id,user_id,name,key_hash) VALUES($1,$2,'quota',encode(sha256($3::bytea),'hex'))", uuid.New(), owner, token)
	require.NoError(t, err)
	second, err := pgxpool.New(ctx, db.Config().ConnString())
	require.NoError(t, err)
	defer second.Close()
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pool := db
			if i%2 == 1 {
				pool = second
			}
			results <- quota.Reserve(ctx, pool, owner, "sources/"+owner.String()+"/"+uuid.NewString(), 10485760)
		}(i)
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		} else {
			require.ErrorIs(t, err, quota.ErrExceeded)
		}
	}
	require.Equal(t, 10, admitted)
	events, err := live.New(redisURL)
	require.NoError(t, err)
	defer events.Close()
	router := httpapi.NewRouter(db, nil, events)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", owner.String())
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	t.Setenv("QUOTA_DEMO_ENABLED", "false")
	require.Equal(t, 403, call("POST", "/account/quota/demo", `{"plan":"pro"}`).Code)
	t.Setenv("QUOTA_DEMO_ENABLED", "true")
	require.Equal(t, 200, call("POST", "/account/quota/demo", `{"plan":"pro"}`).Code)
	require.Equal(t, 200, call("POST", "/account/quota/demo", `{"addon":true}`).Code)
	require.Equal(t, 200, call("POST", "/account/quota/demo", `{"addon":true}`).Code)
	require.Contains(t, call("GET", "/account/quota", "").Body.String(), `"included_bytes":1073741824`)
	require.Contains(t, call("GET", "/account/quota", "").Body.String(), `"addon_bytes":104857600`)
	require.Equal(t, 400, call("POST", "/account/quota/demo", `{"plan":"unknown"}`).Code)
	require.Equal(t, 200, call("POST", "/account/quota/demo", `{"plan":"free"}`).Code)
	require.NoError(t, quota.Reserve(ctx, db, owner, "sources/"+owner.String()+"/"+uuid.NewString(), 1))
	_, err = db.Exec(ctx, "UPDATE users SET addon_bytes=0 WHERE id=$1", owner)
	require.NoError(t, err)
	require.ErrorIs(t, quota.Reserve(ctx, db, owner, "sources/"+owner.String()+"/"+uuid.NewString(), 1), quota.ErrExceeded)
	_, err = db.Exec(ctx, "UPDATE upload_reservations SET created_at=now()-interval '25 hours' WHERE user_id=$1", owner)
	require.NoError(t, err)
	n, err := cleanup.Reserve(ctx, db, time.Now(), 1000)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 22)
	_, err = db.Exec(ctx, "UPDATE cleanup_objects SET delete_after=now()-interval '1 second'")
	require.NoError(t, err)
	_, err = cleanup.DeleteOne(ctx, db, func(_ context.Context, _ string) error { return errors.New("S3 unavailable") })
	require.Error(t, err)
	require.ErrorIs(t, quota.Reserve(ctx, db, owner, "sources/"+owner.String()+"/"+uuid.NewString(), 1), quota.ErrExceeded)
	_, err = db.Exec(ctx, "UPDATE cleanup_objects SET next_attempt_at=now()-interval '1 second'")
	require.NoError(t, err)
	for {
		more, err := cleanup.DeleteOne(ctx, db, func(_ context.Context, _ string) error { return nil })
		require.NoError(t, err)
		if !more {
			break
		}
	}
	require.NoError(t, quota.Reserve(ctx, db, owner, "sources/"+owner.String()+"/"+uuid.NewString(), 1))
	// Output bytes commit with the result; duplicate and cancelled results still represent one stored object.
	batchID, imageID, cancelledID := uuid.New(), uuid.New(), uuid.New()
	_, err = db.Exec(ctx, "INSERT INTO batches(id,user_id,watermark_key) VALUES($1,$2,$3)", batchID, owner, "sources/"+owner.String()+"/"+uuid.NewString())
	require.NoError(t, err)
	for _, id := range []uuid.UUID{imageID, cancelledID} {
		_, err = db.Exec(ctx, "INSERT INTO images(id,batch_id,source_key) VALUES($1,$2,$3)", id, batchID, "sources/"+owner.String()+"/"+uuid.NewString())
		require.NoError(t, err)
	}
	_, err = db.Exec(ctx, "UPDATE images SET status='cancelled' WHERE id=$1", cancelledID)
	require.NoError(t, err)
	result := image.Result{Version: 1, JobType: "composite", BatchID: batchID, ImageID: imageID, Status: "done", OutputKey: "processed/" + batchID.String() + "/" + imageID.String() + ".png", OutputBytes: 120 * 1024 * 1024}
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	handler := image.NewResultHandler(db)
	require.NoError(t, handler.Handle(ctx, string(raw)))
	require.NoError(t, handler.Handle(ctx, string(raw)))
	// A legacy replay must not replace the actual measurement with the compatibility estimate.
	result.OutputBytes = 0
	raw, err = json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, handler.Handle(ctx, string(raw)))
	result.ImageID = cancelledID
	result.OutputKey = "processed/" + batchID.String() + "/" + cancelledID.String() + ".png"
	result.OutputBytes = 3
	raw, err = json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, handler.Handle(ctx, string(raw)))
	var status string
	require.NoError(t, db.QueryRow(ctx, "SELECT status FROM images WHERE id=$1", cancelledID).Scan(&status))
	require.Equal(t, "cancelled", status)
	var count int
	var bytes int64
	require.NoError(t, db.QueryRow(ctx, "SELECT count(*),sum(bytes) FROM output_storage WHERE user_id=$1", owner).Scan(&count, &bytes))
	require.Equal(t, 2, count)
	require.Equal(t, int64(120*1024*1024+3), bytes)
	var account struct {
		Data quota.Account `json:"data"`
	}
	require.NoError(t, json.Unmarshal(call("GET", "/account/quota", "").Body.Bytes(), &account))
	require.Equal(t, bytes+1, account.Data.Used)
	require.ErrorIs(t, quota.Reserve(ctx, db, owner, "sources/"+owner.String()+"/"+uuid.NewString(), 1), quota.ErrExceeded)
	_, err = db.Exec(ctx, "UPDATE batches SET completed_at=now()-interval '40 days' WHERE id=$1", batchID)
	require.NoError(t, err)
	_, err = cleanup.Reserve(ctx, db, time.Now().Add(-30*24*time.Hour), 1000)
	require.NoError(t, err)
	// Both current and cancelled output keys must be scheduled, without releasing bytes at scheduling time.
	require.NoError(t, json.Unmarshal(call("GET", "/account/quota", "").Body.Bytes(), &account))
	require.Equal(t, bytes+1, account.Data.Used)
	_, err = db.Exec(ctx, "UPDATE cleanup_objects SET delete_after=now()-interval '1 second'")
	require.NoError(t, err)
	for {
		more, err := cleanup.DeleteOne(ctx, db, func(context.Context, string) error { return nil })
		require.NoError(t, err)
		if !more {
			break
		}
	}
	require.NoError(t, json.Unmarshal(call("GET", "/account/quota", "").Body.Bytes(), &account))
	require.Equal(t, int64(1), account.Data.Used)
	// Late stored-output reports re-arm a completed delete instead of leaving recreated bytes uncharged.
	require.NoError(t, handler.Handle(ctx, string(raw)))
	require.NoError(t, json.Unmarshal(call("GET", "/account/quota", "").Body.Bytes(), &account))
	require.Equal(t, int64(4), account.Data.Used)
	var retired bool
	require.NoError(t, db.QueryRow(ctx, "SELECT deleted_at IS NULL FROM cleanup_objects WHERE key=$1", result.OutputKey).Scan(&retired))
	require.True(t, retired)
	_, err = db.Exec(ctx, "DELETE FROM cleanup_objects WHERE key LIKE $1", "processed/"+batchID.String()+"/%")
	require.NoError(t, err)
	_, err = db.Exec(ctx, "DELETE FROM batches WHERE id=$1", batchID)
	require.NoError(t, err)
	// Remove this helper's data so later pipeline assertions see only their own objects.
	_, err = db.Exec(ctx, "DELETE FROM cleanup_objects WHERE key LIKE $1 OR key LIKE $2", "sources/"+owner.String()+"/%", "uploads/"+owner.String()+"/%")
	require.NoError(t, err)
	_, err = db.Exec(ctx, "DELETE FROM upload_reservations WHERE user_id=$1", owner)
	require.NoError(t, err)
}
