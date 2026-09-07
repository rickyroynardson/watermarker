package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/batch"
	"github.com/rickyroynardson/watermarker/apps/api/internal/httpapi"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/rickyroynardson/watermarker/apps/api/internal/utils"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func useActiveDockerContext(t *testing.T) {
	t.Helper()
	if os.Getenv("DOCKER_HOST") != "" {
		return
	}
	host, err := exec.CommandContext(t.Context(), "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err == nil && strings.TrimSpace(string(host)) != "" {
		t.Setenv("DOCKER_HOST", strings.TrimSpace(string(host)))
	}
}

func postUpload(t *testing.T, ctx context.Context, upload storage.S3Upload, contents string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, value := range upload.Fields {
		require.NoError(t, form.WriteField(key, value))
	}
	file, err := form.CreateFormFile("file", "image.png")
	require.NoError(t, err)
	_, err = io.WriteString(file, contents)
	require.NoError(t, err)
	require.NoError(t, form.Close())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upload.URL, &body)
	require.NoError(t, err)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.StatusCode, string(data))
}

func TestBatchAPIIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires Docker")
	}
	useActiveDockerContext(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	pg, err := postgres.Run(ctx, "postgres:18-alpine", postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, pg)
	require.NoError(t, err)
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	// Concurrent retries must release their transaction before querying the pool again.
	cfg.MaxConns = 1
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer db.Close()
	paths, err := filepath.Glob("../../migrations/*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		sql, err := os.ReadFile(path)
		require.NoError(t, err)
		up, _, _ := strings.Cut(string(sql), "-- +goose Down")
		_, err = db.Exec(ctx, up)
		require.NoError(t, err)
	}

	awsContainer, err := localstack.Run(ctx, "localstack/localstack:4.14.0",
		testcontainers.WithEnv(map[string]string{"SERVICES": "s3"}))
	testcontainers.CleanupContainer(t, awsContainer)
	require.NoError(t, err)
	endpoint, err := awsContainer.PortEndpoint(ctx, "4566/tcp", "http")
	require.NoError(t, err)
	const bucket = "watermarker"
	s3Client := s3.NewFromConfig(aws.Config{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	_, err = s3Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	// ponytail: versioning avoids LocalStack's copy-before-precondition bug;
	// unversioned overwrite protection still needs an AWS check.
	_, err = s3Client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	})
	require.NoError(t, err)
	for key, value := range map[string]string{
		"AWS_REGION": "us-east-1", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
		"AWS_SESSION_TOKEN": "", "AWS_PROFILE": "", "AWS_EC2_METADATA_DISABLED": "true",
		"AWS_ENDPOINT_URL": endpoint, "AWS_ENDPOINT_URL_S3": endpoint,
		"AWS_CONFIG_FILE": t.TempDir() + "/config", "AWS_SHARED_CREDENTIALS_FILE": t.TempDir() + "/credentials",
	} {
		t.Setenv(key, value)
	}
	objects, err := storage.NewS3(ctx, bucket)
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	router := httpapi.NewRouter(db, objects)
	request := func(method, path, token, idempotencyKey, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if idempotencyKey != "" {
			r.Header.Set("Idempotency-Key", idempotencyKey)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	seedKey := func() (uuid.UUID, string) {
		id, token := uuid.New(), uuid.NewString()
		hash := sha256.Sum256([]byte(token))
		_, err := db.Exec(ctx, "INSERT INTO api_keys(id, name, key_hash) VALUES ($1, 'test', $2)", id, hex.EncodeToString(hash[:]))
		require.NoError(t, err)
		return id, token
	}
	owner, token := seedKey()
	_, otherToken := seedKey()
	revokedID, revokedToken := seedKey()
	_, err = db.Exec(ctx, "UPDATE api_keys SET revoked_at = now() WHERE id = $1", revokedID)
	require.NoError(t, err)
	jsonBody := func(t *testing.T, body any) string {
		t.Helper()
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		return string(encoded)
	}
	checkError := func(t *testing.T, w *httptest.ResponseRecorder, status int, code utils.ErrorCode) {
		t.Helper()
		require.Equal(t, status, w.Code, w.Body.String())
		var response utils.ErrorBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Equal(t, code, response.Error.Code)
	}
	upload := func(t *testing.T, token, contents string) storage.S3Upload {
		t.Helper()
		w := request(http.MethodPost, "/uploads/presign", token, "", `{"content_type":"image/png"}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response struct {
			Data storage.S3Upload `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		postUpload(t, ctx, response.Data, contents)
		return response.Data
	}
	batchID := func(t *testing.T, w *httptest.ResponseRecorder) uuid.UUID {
		t.Helper()
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		var response struct {
			Data batch.CreateBatchResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.NotEqual(t, uuid.Nil, response.Data.ID)
		return response.Data.ID
	}
	counts := func(t *testing.T, idem string, batches, images int) {
		t.Helper()
		var gotBatches, gotImages int
		require.NoError(t, db.QueryRow(ctx, "SELECT count(*) FROM batches WHERE api_key_id = $1 AND idempotency_key = $2", owner, idem).Scan(&gotBatches))
		require.NoError(t, db.QueryRow(ctx, "SELECT count(*) FROM images JOIN batches ON batches.id = images.batch_id WHERE api_key_id = $1 AND idempotency_key = $2", owner, idem).Scan(&gotImages))
		require.Equal(t, batches, gotBatches)
		require.Equal(t, images, gotImages)
	}

	t.Run("authentication", func(t *testing.T) {
		for _, key := range []string{"", "unknown", revokedToken} {
			for _, path := range []string{"/uploads/presign", "/batches"} {
				checkError(t, request(http.MethodPost, path, key, "unauthorized", `{}`), http.StatusUnauthorized, utils.CodeUnauthorized)
			}
		}
		counts(t, "unauthorized", 0, 0)
	})

	t.Run("create retry conflict and ownership", func(t *testing.T) {
		watermark := upload(t, token, "watermark")
		first := upload(t, token, "first image")
		second := upload(t, token, "second image")
		req := batch.CreateBatchRequest{WatermarkKey: watermark.Key, SourceKeys: []string{first.Key, second.Key}}
		id := batchID(t, request(http.MethodPost, "/batches", token, "create", jsonBody(t, req)))
		counts(t, "create", 1, 2)
		var storedOwner uuid.UUID
		var storedWatermark string
		require.NoError(t, db.QueryRow(ctx, "SELECT api_key_id, watermark_key FROM batches WHERE id = $1", id).Scan(&storedOwner, &storedWatermark))
		require.Equal(t, owner, storedOwner)
		require.Equal(t, strings.Replace(watermark.Key, "uploads/", "sources/", 1), storedWatermark)
		for key, want := range map[string]string{watermark.Key: "watermark", first.Key: "first image", second.Key: "second image"} {
			persistent := strings.Replace(key, "uploads/", "sources/", 1)
			object, err := s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(persistent)})
			require.NoError(t, err)
			data, err := io.ReadAll(object.Body)
			require.NoError(t, object.Body.Close())
			require.NoError(t, err)
			require.Equal(t, want, string(data))
			_, err = s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			var missing *types.NotFound
			require.ErrorAs(t, err, &missing, "staging object must be deleted")
			if key != watermark.Key {
				var status string
				require.NoError(t, db.QueryRow(ctx, "SELECT status FROM images WHERE batch_id = $1 AND source_key = $2", id, persistent).Scan(&status))
				require.Equal(t, "pending", status)
			}
		}
		slices.Reverse(req.SourceKeys)
		require.Equal(t, id, batchID(t, request(http.MethodPost, "/batches", token, "create", jsonBody(t, req))))
		changed := req
		changed.WatermarkKey = "uploads/" + owner.String() + "/" + uuid.NewString()
		checkError(t, request(http.MethodPost, "/batches", token, "create", jsonBody(t, changed)), http.StatusConflict, batch.CodeIdempotencyConflict)
		checkError(t, request(http.MethodPost, "/batches", otherToken, "create", jsonBody(t, req)), http.StatusBadRequest, utils.CodeInvalidRequest)
		counts(t, "create", 1, 2)
		w := request(http.MethodGet, "/batches", token, "", "")
		require.Equal(t, http.StatusOK, w.Code)
		var list struct {
			Data batch.ListBatchesResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
		require.Len(t, list.Data.Batches, 1)
		require.Equal(t, id, list.Data.Batches[0].ID)
		w = request(http.MethodGet, "/batches", otherToken, "", "")
		require.Equal(t, http.StatusOK, w.Code)
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
		require.Empty(t, list.Data.Batches)

		// Replaying an upload URL cannot change the inputs of another accepted batch.
		postUpload(t, ctx, first, "replacement image")
		batchID(t, request(http.MethodPost, "/batches", token, "reuse", jsonBody(t, req)))
		object, err := s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(strings.Replace(first.Key, "uploads/", "sources/", 1))})
		require.NoError(t, err)
		data, err := io.ReadAll(object.Body)
		require.NoError(t, object.Body.Close())
		require.NoError(t, err)
		require.Equal(t, "first image", string(data))
	})

	t.Run("invalid and missing uploads", func(t *testing.T) {
		for _, body := range []string{`{`, `{}`, `{"watermark_key":"x","source_keys":["x","x"]}`} {
			checkError(t, request(http.MethodPost, "/batches", token, "invalid", body), http.StatusBadRequest, utils.CodeInvalidRequest)
		}
		req := batch.CreateBatchRequest{WatermarkKey: "uploads/" + owner.String() + "/" + uuid.NewString(), SourceKeys: []string{"uploads/" + owner.String() + "/" + uuid.NewString()}}
		checkError(t, request(http.MethodPost, "/batches", token, "missing", jsonBody(t, req)), http.StatusBadRequest, batch.CodeUploadNotFound)
		counts(t, "invalid", 0, 0)
		counts(t, "missing", 0, 0)
	})

	t.Run("concurrent retries", func(t *testing.T) {
		watermark, source := upload(t, token, "watermark"), upload(t, token, "image")
		body := jsonBody(t, batch.CreateBatchRequest{WatermarkKey: watermark.Key, SourceKeys: []string{source.Key}})
		responses := make([]*httptest.ResponseRecorder, 4)
		var wg sync.WaitGroup
		for i := range responses {
			wg.Go(func() { responses[i] = request(http.MethodPost, "/batches", token, "race", body) })
		}
		wg.Wait()
		id := batchID(t, responses[0])
		for _, response := range responses[1:] {
			require.Equal(t, id, batchID(t, response))
		}
		counts(t, "race", 1, 1)
	})

	t.Run("database failure rolls back and preserves uploads", func(t *testing.T) {
		watermark, source := upload(t, token, "watermark"), upload(t, token, "image")
		_, err := db.Exec(ctx, "ALTER TABLE images ADD CONSTRAINT test_reject_images CHECK (false) NOT VALID")
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := db.Exec(ctx, "ALTER TABLE images DROP CONSTRAINT test_reject_images")
			require.NoError(t, err)
		})
		body := jsonBody(t, batch.CreateBatchRequest{WatermarkKey: watermark.Key, SourceKeys: []string{source.Key}})
		checkError(t, request(http.MethodPost, "/batches", token, "rollback", body), http.StatusInternalServerError, utils.CodeInternal)
		counts(t, "rollback", 0, 0)
		for _, key := range []string{watermark.Key, source.Key} {
			_, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			require.NoError(t, err)
		}
	})
}