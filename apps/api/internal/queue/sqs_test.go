package queue

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/require"
)

func TestPollAcknowledgesOnlySuccessfulHandling(t *testing.T) {
	for _, stage := range []string{"success", "handler failure", "delete failure"} {
		t.Run(stage, func(t *testing.T) {
			var handled, deleted atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-amz-json-1.0")
				switch r.Header.Get("X-Amz-Target") {
				case "AmazonSQS.ReceiveMessage":
					_, _ = w.Write([]byte(`{"Messages":[{"MessageId":"id","ReceiptHandle":"receipt","Body":"result"}]}`))
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
			err := q.Poll(t.Context(), func(_ context.Context, body string) error {
				require.Equal(t, "result", body)
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
