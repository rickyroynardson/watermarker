package httpapi_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rickyroynardson/watermarker/apps/api/internal/batch"
	"github.com/rickyroynardson/watermarker/apps/api/internal/cleanup"
	"github.com/rickyroynardson/watermarker/apps/api/internal/httpapi"
	"github.com/rickyroynardson/watermarker/apps/api/internal/image"
	"github.com/rickyroynardson/watermarker/apps/api/internal/live"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/stretchr/testify/require"
)

func testBatchEvents(t *testing.T, db *pgxpool.Pool, objects *storage.S3, owner uuid.UUID, otherKey string, events *live.Events, redisURL string) {
	ctx := t.Context()
	repo := batch.NewRepository(db, events)
	b := batch.Batch{ID: uuid.New(), UserID: owner, WatermarkKey: "sources/" + owner.String() + "/" + uuid.NewString(), Images: []batch.Image{{ID: uuid.New(), SourceKey: "sources/" + owner.String() + "/" + uuid.NewString()}, {ID: uuid.New(), SourceKey: "sources/" + owner.String() + "/" + uuid.NewString()}}}
	_, err := repo.CreateBatch(ctx, b)
	require.NoError(t, err)
	defer db.Exec(ctx, "DELETE FROM batches WHERE id=$1", b.ID)
	defer db.Exec(ctx, "DELETE FROM cleanup_objects WHERE key=ANY($1)", []string{
		b.WatermarkKey, b.Images[0].SourceKey, b.Images[1].SourceKey,
		"processed/" + b.ID.String() + "/" + b.Images[0].ID.String() + ".png",
	})
	keyID, token := uuid.New(), uuid.NewString()
	sum := sha256.Sum256([]byte(token))
	_, err = db.Exec(ctx, "INSERT INTO api_keys(id,user_id,name,key_hash) VALUES($1,$2,'SSE test',$3)", keyID, owner, hex.EncodeToString(sum[:]))
	require.NoError(t, err)
	defer db.Exec(ctx, "DELETE FROM api_keys WHERE id=$1", keyID)
	server := httptest.NewServer(httpapi.NewRouter(db, objects, events))
	defer server.Close()
	client := &http.Client{Timeout: 20 * time.Second}
	path := server.URL + "/batches/" + b.ID.String() + "/events"
	open := func(key string) *http.Response {
		req, err := http.NewRequestWithContext(ctx, "GET", path, nil)
		require.NoError(t, err)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res, err := client.Do(req)
		require.NoError(t, err)
		return res
	}
	for key, want := range map[string]int{"": 401, otherKey: 404} {
		res := open(key)
		require.Equal(t, want, res.StatusCode)
		res.Body.Close()
	}
	response := open(token)
	require.Equal(t, 200, response.StatusCode)
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	require.Equal(t, "no", response.Header.Get("X-Accel-Buffering"))
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	until := func(status string) batch.BatchDetails {
		t.Helper()
		for scanner.Scan() {
			if !strings.HasPrefix(scanner.Text(), "data: ") {
				continue
			}
			var snapshot batch.BatchDetails
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &snapshot))
			require.Equal(t, b.ID, snapshot.ID)
			if snapshot.Status == status {
				return snapshot
			}
		}
		t.Fatalf("stream closed before %s snapshot: %v", status, scanner.Err())
		return batch.BatchDetails{}
	}
	// A second API instance gets the same update through its own shared Redis subscriber.
	secondEvents, err := live.New(redisURL)
	require.NoError(t, err)
	defer secondEvents.Close()
	stopSecond, err := secondEvents.Listen(ctx)
	require.NoError(t, err)
	defer stopSecond()
	secondServer := httptest.NewServer(httpapi.NewRouter(db, objects, secondEvents))
	defer secondServer.Close()
	secondRequest, err := http.NewRequestWithContext(ctx, "GET", secondServer.URL+"/batches/"+b.ID.String()+"/events", nil)
	require.NoError(t, err)
	secondRequest.Header.Set("Authorization", "Bearer "+token)
	secondResponse, err := client.Do(secondRequest)
	require.NoError(t, err)
	defer secondResponse.Body.Close()
	require.Equal(t, 200, secondResponse.StatusCode)
	secondScanner := bufio.NewScanner(secondResponse.Body)
	readSecond := func() batch.BatchDetails {
		t.Helper()
		for secondScanner.Scan() {
			if strings.HasPrefix(secondScanner.Text(), "data: ") {
				var snapshot batch.BatchDetails
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(secondScanner.Text(), "data: ")), &snapshot))
				return snapshot
			}
		}
		t.Fatalf("second API stream closed: %v", secondScanner.Err())
		return batch.BatchDetails{}
	}
	require.Equal(t, "pending", readSecond().Status)
	initial := until("pending")
	require.Len(t, initial.Images, 2)
	byID := func(snapshot batch.BatchDetails, id uuid.UUID) batch.ImageDetails {
		for _, image := range snapshot.Images {
			if image.ID == id {
				return image
			}
		}
		t.Fatalf("snapshot missing image %s", id)
		return batch.ImageDetails{}
	}
	completed := image.Result{Version: 1, JobType: "composite", BatchID: b.ID, ImageID: b.Images[0].ID, Status: "done", OutputKey: "processed/" + b.ID.String() + "/" + b.Images[0].ID.String() + ".png"}
	completedPayload, err := json.Marshal(completed)
	require.NoError(t, err)
	require.NoError(t, image.NewResultHandler(db, events).Handle(ctx, string(completedPayload)))
	progress := until("pending")
	require.Equal(t, "done", byID(progress, b.Images[0].ID).Status)
	require.NotEmpty(t, byID(progress, b.Images[0].ID).DownloadURL)
	result := image.Result{Version: 1, JobType: "composite", BatchID: b.ID, ImageID: b.Images[1].ID}
	payload, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, image.NewResultHandler(db, events).HandleDeadJob(ctx, string(payload)))
	// Ignore earlier pending snapshots when checking cross-instance delivery.
	for snapshot := readSecond(); snapshot.Status != "failed"; snapshot = readSecond() {
	}
	failed := until("failed")
	require.True(t, byID(failed, b.Images[1].ID).Retryable)
	// Retry resumes progress even from a terminal state, with no browser polling.
	require.NoError(t, repo.RetryImage(ctx, owner, b.ID, b.Images[1].ID, 0))
	retried := until("pending")
	require.Equal(t, 1, byID(retried, b.Images[1].ID).Attempt)
	// Change state without publishing, then sever Redis subscribers. Re-subscription
	// must refresh both open streams, even though the missed update has no event to replay.
	_, err = db.Exec(ctx, "UPDATE images SET status='failed', error='reconnect marker', retryable=true WHERE id=$1", b.Images[1].ID)
	require.NoError(t, err)
	options, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	admin := redis.NewClient(options)
	defer admin.Close()
	killed, err := admin.ClientKillByFilter(ctx, "TYPE", "pubsub").Result()
	require.NoError(t, err)
	require.EqualValues(t, 2, killed, "one Redis subscriber per API instance")
	for snapshot := until("failed"); byID(snapshot, b.Images[1].ID).Error != "reconnect marker"; snapshot = until("failed") {
	}
	for snapshot := readSecond(); byID(snapshot, b.Images[1].ID).Error != "reconnect marker"; snapshot = readSecond() {
	}
	require.NoError(t, secondResponse.Body.Close())
	// Disconnect, change state, and reconnect: the first snapshot recovers missed changes.
	require.NoError(t, response.Body.Close())
	require.NoError(t, repo.Cancel(ctx, owner, b.ID))
	response = open(token)
	defer response.Body.Close()
	scanner = bufio.NewScanner(response.Body)
	require.Equal(t, "cancelled", until("cancelled").Status)
	_, err = db.Exec(ctx, "UPDATE batches SET completed_at=now()-interval '31 days' WHERE id=$1", b.ID)
	require.NoError(t, err)
	_, err = cleanup.Reserve(ctx, db, time.Now().Add(-30*24*time.Hour), 1000, events)
	require.NoError(t, err)
	expired := until("expired")
	require.Equal(t, "expired", expired.Status)
	require.Empty(t, byID(expired, b.Images[0].ID).DownloadURL)
	// Revoke an open stream's key and trigger a change: it must close without leaking another snapshot.
	_, err = db.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", keyID)
	require.NoError(t, err)
	_, err = db.Exec(ctx, "UPDATE batches SET completed_at=clock_timestamp() WHERE id=$1", b.ID)
	require.NoError(t, err)
	events.Publish(ctx, b.ID)
	for scanner.Scan() {
		require.False(t, strings.HasPrefix(scanner.Text(), "data:"), "revoked stream received data")
	}
	require.NoError(t, scanner.Err())
	denied := open(token)
	require.Equal(t, 401, denied.StatusCode)
	denied.Body.Close()
	unavailable, err := live.New("redis://127.0.0.1:1/0")
	require.NoError(t, err)
	defer unavailable.Close()
	outageBatch := batch.Batch{ID: uuid.New(), UserID: owner, WatermarkKey: "sources/" + owner.String() + "/" + uuid.NewString(), Images: []batch.Image{{ID: uuid.New(), SourceKey: "sources/" + owner.String() + "/" + uuid.NewString()}}}
	_, err = batch.NewRepository(db).CreateBatch(ctx, outageBatch)
	require.NoError(t, err)
	defer db.Exec(ctx, "DELETE FROM batches WHERE id=$1", outageBatch.ID)
	dead := image.Result{Version: 1, JobType: "composite", BatchID: outageBatch.ID, ImageID: outageBatch.Images[0].ID}
	deadPayload, err := json.Marshal(dead)
	require.NoError(t, err)
	require.NoError(t, image.NewResultHandler(db, unavailable).HandleDeadJob(ctx, string(deadPayload)))
	saved, err := repo.GetBatch(ctx, owner, outageBatch.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", saved.Images[0].Status)

	// A canceled request must remove its subscription without blocking server shutdown.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	req, err := http.NewRequestWithContext(cancelCtx, "GET", path, nil)
	require.NoError(t, err)
	_, err = client.Do(req)
	require.Error(t, err)
}
