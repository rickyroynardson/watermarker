package queue

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestPollAcknowledgesOnlySuccessfulHandling(t *testing.T) {
	for _, stage := range []string{"success", "handler failure", "delete failure"} {
		t.Run(stage, func(t *testing.T) {
			var handled, deleted atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-amz-json-1.0")
				switch r.Header.Get("X-Amz-Target") {
				case "AmazonSQS.ReceiveMessage":
					_, _ = w.Write([]byte(`{"Messages":[{"MessageId":"id","ReceiptHandle":"receipt","Body":"{\"trace_context\":{\"traceparent\":\"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01\"}}"}]}`))
				case "AmazonSQS.DeleteMessage":
					if !handled.Load() {
						t.Error("acknowledgement happened before successful handling")
					}
					deleted.Store(true)
					if stage == "delete failure" {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = w.Write([]byte(`{"__type":"InternalError","message":"unavailable"}`))
						return
					}
					_, _ = w.Write([]byte(`{}`))
				default:
					t.Errorf("unexpected SQS operation: %s", r.Header.Get("X-Amz-Target"))
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			q := &SQS{queueURL: server.URL + "/queue", client: sqs.NewFromConfig(aws.Config{
				Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
			}, func(o *sqs.Options) { o.BaseEndpoint = aws.String(server.URL); o.RetryMaxAttempts = 1 })}
			failure := errors.New("database unavailable")
			err := q.Poll(t.Context(), func(ctx context.Context, body string) error {
				require.Equal(t, "0123456789abcdef0123456789abcdef", trace.SpanContextFromContext(ctx).TraceID().String())
				if stage == "handler failure" {
					return failure
				}
				handled.Store(true)
				return nil
			})
			switch stage {
			case "success":
				require.NoError(t, err)
				require.True(t, deleted.Load())
			case "handler failure":
				require.ErrorIs(t, err, failure)
				require.False(t, deleted.Load())
			case "delete failure":
				require.Error(t, err)
				require.True(t, handled.Load())
			}
		})
	}
}

func TestDepthIsReadOnlyAndRejectsUnknownCounts(t *testing.T) {
	for _, value := range []string{"7", "", "bad", "-1"} {
		t.Run(value, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "AmazonSQS.GetQueueAttributes", r.Header.Get("X-Amz-Target"))
				w.Header().Set("Content-Type", "application/x-amz-json-1.0")
				json.NewEncoder(w).Encode(map[string]any{"Attributes": map[string]string{
					"ApproximateNumberOfMessages": value, "ApproximateNumberOfMessagesNotVisible": "2", "ApproximateNumberOfMessagesDelayed": "1",
				}})
			}))
			defer server.Close()
			q := &SQS{queueURL: server.URL + "/queue", client: sqs.NewFromConfig(aws.Config{
				Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
			}, func(o *sqs.Options) { o.BaseEndpoint = aws.String(server.URL); o.RetryMaxAttempts = 1 })}
			counts, err := q.Depth(t.Context())
			if value == "7" {
				require.NoError(t, err)
				require.Equal(t, [3]int64{7, 2, 1}, counts)
			} else {
				require.Error(t, err)
			}
		})
	}
}
