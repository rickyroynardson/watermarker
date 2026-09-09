package httpapi_test

import (
	"bytes"
	"context"
	stdimage "image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/batch"
	"github.com/rickyroynardson/watermarker/apps/api/internal/image"
	"github.com/rickyroynardson/watermarker/apps/api/internal/outbox"
	"github.com/rickyroynardson/watermarker/apps/api/internal/queue"
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
	"github.com/stretchr/testify/require"
)

func testPythonWorker(t *testing.T, db *pgxpool.Pool, s3Client *s3.Client, sqsClient *sqs.Client, objects *storage.S3, owner uuid.UUID) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	// Earlier cases deliberately leave pending images; isolate their jobs from this worker.
	for {
		sent, err := outbox.DispatchOne(ctx, db, func(context.Context, string) error { return nil })
		require.NoError(t, err)
		if !sent {
			break
		}
	}
	jobsQueue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("python-worker-jobs")})
	require.NoError(t, err)
	resultsQueue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("python-worker-results")})
	require.NoError(t, err)
	t.Setenv("S3_BUCKET", "watermarker")
	t.Setenv("SQS_JOBS_QUEUE_URL", aws.ToString(jobsQueue.QueueUrl))
	t.Setenv("SQS_RESULTS_QUEUE_URL", aws.ToString(resultsQueue.QueueUrl))
	jobs, err := queue.NewSQS(ctx, aws.ToString(jobsQueue.QueueUrl))
	require.NoError(t, err)
	results, err := queue.NewSQS(ctx, aws.ToString(resultsQueue.QueueUrl))
	require.NoError(t, err)
	workerDir, err := filepath.Abs("../../../worker")
	require.NoError(t, err)
	runWorker := func() {
		cmd := exec.CommandContext(ctx, "uv", "run", "--frozen", "--offline", "main.py", "--once")
		cmd.Dir = workerDir
		// Use the test's explicit credentials/endpoint, never a developer's AWS profile.
		cmd.Env = slices.DeleteFunc(os.Environ(), func(s string) bool {
			return strings.HasPrefix(s, "AWS_PROFILE=") || strings.HasPrefix(s, "AWS_DEFAULT_PROFILE=")
		})
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
	}
	pngBytes := func(size int, c color.NRGBA) string {
		img := stdimage.NewNRGBA(stdimage.Rect(0, 0, size, size))
		draw.Draw(img, img.Bounds(), stdimage.NewUniform(c), stdimage.Point{}, draw.Src)
		var data bytes.Buffer
		require.NoError(t, png.Encode(&data, img))
		return data.String()
	}
	upload := func(contents string) string {
		key := "uploads/" + owner.String() + "/" + uuid.NewString()
		presigned, err := objects.PresignUpload(ctx, key, "image/png")
		require.NoError(t, err)
		postUpload(t, ctx, presigned, contents)
		return key
	}
	service := batch.NewService(batch.NewRepository(db), objects)
	res, err := service.CreateBatch(ctx, owner, batch.CreateBatchRequest{
		SourceKeys:   []string{upload(pngBytes(100, color.NRGBA{255, 255, 255, 255}))},
		WatermarkKey: upload(pngBytes(4, color.NRGBA{255, 0, 0, 128})),
	})
	require.NoError(t, err)
	var body string
	sent, err := outbox.DispatchOne(ctx, db, func(ctx context.Context, payload string) error {
		body = payload
		return jobs.Send(ctx, payload)
	})
	require.NoError(t, err)
	require.True(t, sent)
	runWorker()
	handler := image.NewResultHandler(db)
	require.NoError(t, results.Poll(ctx, handler.Handle))
	var status, outputKey string
	var updated time.Time
	require.NoError(t, db.QueryRow(ctx, "SELECT status, output_key, updated_at FROM images WHERE batch_id = $1", res.ID).Scan(&status, &outputKey, &updated))
	require.Equal(t, "done", status)
	object, err := s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("watermarker"), Key: aws.String(outputKey)})
	require.NoError(t, err)
	img, err := png.Decode(object.Body)
	require.NoError(t, object.Body.Close())
	require.NoError(t, err)
	require.Equal(t, stdimage.Rect(0, 0, 100, 100), img.Bounds())
	require.Equal(t, color.NRGBA{255, 178, 178, 255}, color.NRGBAModel.Convert(img.At(95, 95)))

	// Redelivery reuses the S3 object, publishes a result again, and leaves DB state unchanged.
	require.NoError(t, jobs.Send(ctx, body))
	runWorker()
	require.NoError(t, results.Poll(ctx, handler.Handle))
	var after time.Time
	require.NoError(t, db.QueryRow(ctx, "SELECT updated_at FROM images WHERE batch_id = $1", res.ID).Scan(&after))
	require.Equal(t, updated, after)
	head, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("watermarker"), Key: aws.String(outputKey)})
	require.NoError(t, err)
	require.Equal(t, object.VersionId, head.VersionId, "duplicate job must not rewrite the output")

	bad, err := service.CreateBatch(ctx, owner, batch.CreateBatchRequest{
		SourceKeys: []string{upload("invalid image bytes")}, WatermarkKey: upload(pngBytes(4, color.NRGBA{255, 0, 0, 255})),
	})
	require.NoError(t, err)
	sent, err = outbox.DispatchOne(ctx, db, jobs.Send)
	require.NoError(t, err)
	require.True(t, sent)
	runWorker()
	require.NoError(t, results.Poll(ctx, handler.Handle))
	var reason string
	require.NoError(t, db.QueryRow(ctx, "SELECT status, error FROM images WHERE batch_id = $1", bad.ID).Scan(&status, &reason))
	require.Equal(t, "failed", status)
	require.NotEmpty(t, reason)
}
