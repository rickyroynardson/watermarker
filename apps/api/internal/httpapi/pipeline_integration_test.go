package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/batch"
	"github.com/rickyroynardson/watermarker/apps/api/internal/image"
	"github.com/rickyroynardson/watermarker/apps/api/internal/outbox"
	"github.com/rickyroynardson/watermarker/apps/api/internal/queue"
	"github.com/rickyroynardson/watermarker/apps/api/internal/tracing"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func testPipeline(t *testing.T, ctx context.Context, originalDB *pgxpool.Pool, sqsClient *sqs.Client, owner uuid.UUID) {
	cfg := originalDB.Config()
	cfg.MaxConns = 4
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer db.Close()
	// Drain earlier test jobs through a fake successful sender to isolate each scenario.
	for {
		sent, err := outbox.DispatchOne(ctx, db, func(context.Context, string) error { return nil })
		require.NoError(t, err)
		if !sent {
			break
		}
	}
	repo := batch.NewRepository(db)
	newBatch := func() batch.Batch {
		return batch.Batch{ID: uuid.New(), APIKeyID: owner, IdempotencyKey: uuid.NewString(),
			WatermarkKey: "sources/" + owner.String() + "/" + uuid.NewString(),
			Images:       []batch.Image{{ID: uuid.New(), SourceKey: "sources/" + owner.String() + "/" + uuid.NewString()}}}
	}
	countJobs := func() int {
		var count int
		require.NoError(t, db.QueryRow(ctx, "SELECT count(*) FROM outbox_messages").Scan(&count))
		return count
	}

	t.Run("exhausted jobs retry atomically and reject stale messages", func(t *testing.T) {
		b, err := repo.CreateBatch(ctx, newBatch())
		require.NoError(t, err)
		defer db.Exec(ctx, "DELETE FROM batches WHERE id=$1", b.ID)
		var dead string
		require.NoError(t, db.QueryRow(ctx, "SELECT payload::text FROM outbox_messages WHERE image_id=$1", b.Images[0].ID).Scan(&dead))
		_, err = db.Exec(ctx, "DELETE FROM outbox_messages WHERE image_id=$1", b.Images[0].ID)
		require.NoError(t, err)
		handler := image.NewResultHandler(db)
		require.Error(t, handler.HandleDeadJob(ctx, "{}"))
		require.NoError(t, handler.HandleDeadJob(ctx, dead))
		details, err := repo.GetBatch(ctx, owner, b.ID)
		require.NoError(t, err)
		require.True(t, details.Images[0].Retryable)
		failures, countErr := image.UnresolvedFailures(ctx, db)
		require.NoError(t, countErr)
		require.Equal(t, int64(1), failures)
		require.NotNil(t, details.CompletedAt)
		require.ErrorIs(t, repo.RetryImage(ctx, uuid.New(), b.ID, b.Images[0].ID, 0), batch.ErrBatchNotFound)
		// Failed enqueue must preserve the terminal state.
		_, err = db.Exec(ctx, "ALTER TABLE outbox_messages ADD CONSTRAINT reject_retry CHECK(false) NOT VALID")
		require.NoError(t, err)
		retryErr := repo.RetryImage(ctx, owner, b.ID, b.Images[0].ID, 0)
		_, err = db.Exec(ctx, "ALTER TABLE outbox_messages DROP CONSTRAINT reject_retry")
		require.NoError(t, err)
		require.Error(t, retryErr)
		details, err = repo.GetBatch(ctx, owner, b.ID)
		require.NoError(t, err)
		require.True(t, details.Images[0].Retryable)
		require.Zero(t, details.Images[0].Attempt)
		errs := make(chan error, 4)
		for range 4 {
			go func() { errs <- repo.RetryImage(ctx, owner, b.ID, b.Images[0].ID, 0) }()
		}
		for range 4 {
			require.NoError(t, <-errs)
		}
		require.Equal(t, 1, countJobs())
		details, err = repo.GetBatch(ctx, owner, b.ID)
		require.NoError(t, err)
		failures, countErr = image.UnresolvedFailures(ctx, db)
		require.NoError(t, countErr)
		require.Zero(t, failures)
		require.Equal(t, 1, details.Images[0].Attempt)
		require.Equal(t, "pending", details.Images[0].Status)
		require.Nil(t, details.CompletedAt)
		require.NoError(t, handler.HandleDeadJob(ctx, dead))
		result := image.Result{Version: 1, JobType: "composite", BatchID: b.ID, ImageID: b.Images[0].ID, Status: "failed", Error: "invalid image"}
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		require.NoError(t, handler.Handle(ctx, string(raw)))
		details, err = repo.GetBatch(ctx, owner, b.ID)
		require.NoError(t, err)
		require.Equal(t, "pending", details.Images[0].Status)
		result.Attempt = 1
		raw, err = json.Marshal(result)
		require.NoError(t, err)
		require.NoError(t, handler.Handle(ctx, string(raw)))
		failures, countErr = image.UnresolvedFailures(ctx, db)
		require.NoError(t, countErr)
		require.Zero(t, failures, "invalid image failures do not need infrastructure retry")
		require.ErrorIs(t, repo.RetryImage(ctx, owner, b.ID, b.Images[0].ID, 1), batch.ErrRetryConflict)
		require.NoError(t, repo.RetryImage(ctx, owner, b.ID, b.Images[0].ID, 0))
	})

	t.Run("outbox monitor includes delayed retries and preserves age", func(t *testing.T) {
		count, age, err := outbox.Backlog(ctx, db)
		require.NoError(t, err)
		require.Zero(t, count)
		require.Zero(t, age)
		b, err := repo.CreateBatch(ctx, newBatch())
		require.NoError(t, err)
		_, err = db.Exec(ctx, "UPDATE images SET created_at = now() - interval '2 minutes' WHERE id = $1", b.Images[0].ID)
		require.NoError(t, err)
		_, err = db.Exec(ctx, "UPDATE outbox_messages SET next_attempt_at = now() + interval '1 hour' WHERE image_id = $1", b.Images[0].ID)
		require.NoError(t, err)
		count, age, err = outbox.Backlog(ctx, db)
		require.NoError(t, err)
		require.Equal(t, int64(1), count)
		require.GreaterOrEqual(t, age, float64(120))
		_, err = db.Exec(ctx, "DELETE FROM outbox_messages WHERE image_id = $1", b.Images[0].ID)
		require.NoError(t, err)
	})
	t.Run("outbox preserves request trace context", func(t *testing.T) {
		parent := tracing.Propagator.Extract(ctx, propagation.MapCarrier{
			"traceparent": "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
		})
		b := newBatch()
		_, err := repo.CreateBatch(parent, b)
		require.NoError(t, err)
		var body string
		require.NoError(t, db.QueryRow(ctx, "SELECT payload::text FROM outbox_messages WHERE image_id = $1", b.Images[0].ID).Scan(&body))
		restored := trace.SpanContextFromContext(tracing.Extract(ctx, body))
		require.Equal(t, trace.SpanContextFromContext(parent), restored)
		_, err = db.Exec(ctx, "DELETE FROM outbox_messages WHERE image_id = $1", b.Images[0].ID)
		require.NoError(t, err)
	})

	t.Run("batch completion survives concurrent final results and duplicates", func(t *testing.T) {
		b := newBatch()
		b.Images = append(b.Images, batch.Image{ID: uuid.New(), SourceKey: "sources/" + owner.String() + "/" + uuid.NewString()}, batch.Image{ID: uuid.New(), SourceKey: "sources/" + owner.String() + "/" + uuid.NewString()})
		_, err := repo.CreateBatch(ctx, b)
		require.NoError(t, err)
		handler := image.NewResultHandler(db)
		bodies := make([]string, len(b.Images))
		for i, img := range b.Images {
			result := image.Result{Version: 1, JobType: "composite", BatchID: b.ID, ImageID: img.ID, Status: "failed", Error: "invalid image"}
			if i == 0 {
				result.Status = "done"
				result.Error = ""
				result.OutputKey = "processed/" + b.ID.String() + "/" + img.ID.String() + ".png"
			}
			raw, err := json.Marshal(result)
			require.NoError(t, err)
			bodies[i] = string(raw)
		}
		require.NoError(t, handler.Handle(ctx, bodies[0]))
		pending, err := repo.GetBatch(ctx, owner, b.ID)
		require.NoError(t, err)
		require.Nil(t, pending.CompletedAt)
		require.Nil(t, pending.DurationSeconds)
		errs := make(chan error, 2)
		for _, body := range bodies[1:] {
			go func(body string) { errs <- handler.Handle(ctx, body) }(body)
		}
		for range 2 {
			require.NoError(t, <-errs)
		}
		completed, err := repo.GetBatch(ctx, owner, b.ID)
		require.NoError(t, err)
		require.NotNil(t, completed.CompletedAt)
		require.NotNil(t, completed.DurationSeconds)
		require.GreaterOrEqual(t, *completed.DurationSeconds, 0.0)
		for _, body := range bodies {
			require.NoError(t, handler.Handle(ctx, body))
		}
		repeated, err := repo.GetBatch(ctx, owner, b.ID)
		require.NoError(t, err)
		require.Equal(t, completed.CompletedAt, repeated.CompletedAt)
		require.Equal(t, completed.DurationSeconds, repeated.DurationSeconds)
		// Keep the existing outbox scenarios isolated.
		_, err = db.Exec(ctx, "DELETE FROM batches WHERE id = $1", b.ID)
		require.NoError(t, err)
	})

	t.Run("outbox insert failure rolls back images and batch", func(t *testing.T) {
		_, err := db.Exec(ctx, "ALTER TABLE outbox_messages ADD CONSTRAINT test_reject_jobs CHECK (false) NOT VALID")
		require.NoError(t, err)
		defer func() {
			_, err := db.Exec(ctx, "ALTER TABLE outbox_messages DROP CONSTRAINT test_reject_jobs")
			require.NoError(t, err)
		}()
		b := newBatch()
		_, err = repo.CreateBatch(ctx, b)
		require.Error(t, err)
		var count int
		require.NoError(t, db.QueryRow(ctx, "SELECT count(*) FROM batches WHERE id = $1", b.ID).Scan(&count))
		require.Zero(t, count)
		require.NoError(t, db.QueryRow(ctx, "SELECT count(*) FROM images WHERE batch_id = $1", b.ID).Scan(&count))
		require.Zero(t, count)
		require.Zero(t, countJobs())
	})

	t.Run("failed send survives restart and retries original payload", func(t *testing.T) {
		b, err := repo.CreateBatch(ctx, newBatch())
		require.NoError(t, err)
		failure := errors.New("SQS unavailable")
		var first string
		sent, err := outbox.DispatchOne(ctx, db, func(_ context.Context, body string) error {
			first = body
			return failure
		})
		require.True(t, sent)
		require.ErrorIs(t, err, failure)
		require.Equal(t, 1, countJobs())
		sent, err = outbox.DispatchOne(ctx, db, func(context.Context, string) error {
			t.Fatal("failed job must respect retry delay")
			return nil
		})
		require.NoError(t, err)
		require.False(t, sent)
		_, err = db.Exec(ctx, "UPDATE outbox_messages SET next_attempt_at = now()")
		require.NoError(t, err)
		// A new pool represents a restarted dispatcher; no HTTP retry is involved.
		restarted, err := pgxpool.NewWithConfig(ctx, cfg)
		require.NoError(t, err)
		defer restarted.Close()
		sent, err = outbox.DispatchOne(ctx, restarted, func(_ context.Context, body string) error {
			require.JSONEq(t, first, body)
			var job batch.Job
			require.NoError(t, json.Unmarshal([]byte(body), &job))
			require.Equal(t, b.Images[0].ID, job.ImageID)
			return nil
		})
		require.NoError(t, err)
		require.True(t, sent)
		require.Zero(t, countJobs())
	})

	t.Run("concurrent dispatchers skip locked rows", func(t *testing.T) {
		_, err := repo.CreateBatch(ctx, newBatch())
		require.NoError(t, err)
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		var once sync.Once
		defer once.Do(func() { close(release) })
		go func() {
			_, err := outbox.DispatchOne(ctx, db, func(sendCtx context.Context, _ string) error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-sendCtx.Done():
					return sendCtx.Err()
				}
			})
			done <- err
		}()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		secondCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		sent, err := outbox.DispatchOne(secondCtx, db, func(context.Context, string) error {
			t.Fatal("locked job must not be sent twice")
			return nil
		})
		require.NoError(t, err)
		require.False(t, sent)
		once.Do(func() { close(release) })
		require.NoError(t, <-done)
		require.Zero(t, countJobs())
	})

	t.Run("crash after send retains job for redelivery", func(t *testing.T) {
		_, err := repo.CreateBatch(ctx, newBatch())
		require.NoError(t, err)
		crashCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var first string
		_, err = outbox.DispatchOne(crashCtx, db, func(_ context.Context, body string) error {
			first = body
			cancel() // SQS accepted, but the database delete cannot commit.
			return nil
		})
		require.Error(t, err)
		require.Equal(t, 1, countJobs())
		_, err = outbox.DispatchOne(ctx, db, func(_ context.Context, body string) error {
			require.JSONEq(t, first, body)
			return nil
		})
		require.NoError(t, err)
		require.Zero(t, countJobs())
	})

	t.Run("results are committed once before acknowledgement", func(t *testing.T) {
		b, err := repo.CreateBatch(ctx, newBatch())
		require.NoError(t, err)
		result := image.Result{Version: 1, JobType: "composite", BatchID: b.ID, ImageID: b.Images[0].ID,
			Status: "done", OutputKey: "processed/" + b.ID.String() + "/" + b.Images[0].ID.String() + ".png"}
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		body := string(encoded)
		handler := image.NewResultHandler(db)
		resultsQueue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("watermarker-results")})
		require.NoError(t, err)
		results, err := queue.NewSQS(ctx, aws.ToString(resultsQueue.QueueUrl))
		require.NoError(t, err)
		require.NoError(t, results.Send(ctx, body))
		require.NoError(t, results.Poll(ctx, handler.Handle))
		var output, status string
		var updated time.Time
		require.NoError(t, db.QueryRow(ctx, "SELECT status, output_key, updated_at FROM images WHERE id = $1", result.ImageID).Scan(&status, &output, &updated))
		require.Equal(t, "done", status)
		require.Equal(t, result.OutputKey, output)

		// Include invisible messages so an unacknowledged result cannot pass this check.
		require.Eventually(t, func() bool {
			attrs, err := sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
				QueueUrl: resultsQueue.QueueUrl, AttributeNames: []types.QueueAttributeName{
					types.QueueAttributeNameApproximateNumberOfMessages,
					types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
				},
			})
			return err == nil && attrs.Attributes["ApproximateNumberOfMessages"] == "0" &&
				attrs.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
		}, 5*time.Second, 100*time.Millisecond)

		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Go(func() { errs[i] = handler.Handle(ctx, body) })
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}
		result.Status, result.OutputKey, result.Error = "failed", "", "late failure"
		encoded, err = json.Marshal(result)
		require.NoError(t, err)
		require.NoError(t, handler.Handle(ctx, string(encoded)))
		var after time.Time
		require.NoError(t, db.QueryRow(ctx, "SELECT status, output_key, updated_at FROM images WHERE id = $1", result.ImageID).Scan(&status, &output, &after))
		require.Equal(t, "done", status)
		require.Equal(t, updated, after, "duplicates must not mutate terminal rows")
		result.ImageID = uuid.New()
		encoded, err = json.Marshal(result)
		require.NoError(t, err)
		require.Error(t, handler.Handle(ctx, string(encoded)), "unknown image must not be acknowledged")

		failed, err := repo.CreateBatch(ctx, newBatch())
		require.NoError(t, err)
		result.BatchID, result.ImageID = failed.ID, failed.Images[0].ID
		encoded, err = json.Marshal(result)
		require.NoError(t, err)
		require.NoError(t, handler.Handle(ctx, string(encoded)))
		require.NoError(t, handler.Handle(ctx, string(encoded)))
		var failureReason string
		var noOutput bool
		require.NoError(t, db.QueryRow(ctx, "SELECT status, error, output_key IS NULL FROM images WHERE id = $1", result.ImageID).Scan(&status, &failureReason, &noOutput))
		require.Equal(t, "failed", status)
		require.Equal(t, result.Error, failureReason)
		require.True(t, noOutput)
	})
}
