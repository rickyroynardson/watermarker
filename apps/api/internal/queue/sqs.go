package queue

import (
	"context"
	"errors"
	"github.com/rickyroynardson/watermarker/apps/api/internal/metrics"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/zap"
)

type SQS struct {
	client   *sqs.Client
	queueURL string
}

func NewSQS(ctx context.Context, queueURL string) (*SQS, error) {
	if queueURL == "" {
		return nil, errors.New("queue URL is required")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &SQS{client: sqs.NewFromConfig(cfg), queueURL: queueURL}, nil
}

func (q *SQS) Send(ctx context.Context, body string) (err error) {
	start := time.Now()
	defer func() { metrics.Message(ctx, "publish_job", start, err) }()
	_, err = q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(q.queueURL), MessageBody: aws.String(body),
	})
	return err
}

// Poll acknowledges a message only after its handler commits successfully.
func (q *SQS) Poll(ctx context.Context, handle func(context.Context, string) error) error {
	messages, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(q.queueURL), MaxNumberOfMessages: 1,
		WaitTimeSeconds: 20, VisibilityTimeout: 60,
	})
	if err != nil {
		return err
	}
	for _, message := range messages.Messages {
		handleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := handle(handleCtx, aws.ToString(message.Body))
		cancel()
		if err != nil {
			return err // Leave failures for retry and eventual DLQ redrive.
		}
		deleteCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err = q.client.DeleteMessage(deleteCtx, &sqs.DeleteMessageInput{
			QueueUrl: aws.String(q.queueURL), ReceiptHandle: message.ReceiptHandle,
		})
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (q *SQS) Consume(ctx context.Context, handle func(context.Context, string) error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := q.Poll(ctx, handle); err != nil && ctx.Err() == nil {
			zap.L().Error("consume result", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}
}
