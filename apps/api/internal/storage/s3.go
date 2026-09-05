package storage

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3 struct {
	presigner *s3.PresignClient
	bucket    string
}

type S3Upload struct {
	Key       string            `json:"key"`
	URL       string            `json:"url"`
	Fields    map[string]string `json:"fields"`
	ExpiresIn int               `json:"expires_in"`
}

func NewS3(ctx context.Context, bucket string) (*S3, error) {
	if bucket == "" {
		return nil, errors.New("S3_BUCKET is required")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})
	return &S3{presigner: s3.NewPresignClient(client), bucket: bucket}, nil
}

func (s *S3) PresignUpload(ctx context.Context, key, contentType string) (S3Upload, error) {
	const expiresIn = 900

	post, err := s.presigner.PresignPostObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(o *s3.PresignPostOptions) {
		o.Expires = expiresIn * time.Second
		o.Conditions = []any{
			map[string]string{"Content-Type": contentType},
			[]any{"content-length-range", 1, 10 * 1024 * 1024},
		}
	})
	if err != nil {
		return S3Upload{}, err
	}
	post.Values["Content-Type"] = contentType
	return S3Upload{Key: key, URL: post.URL, Fields: post.Values, ExpiresIn: expiresIn}, nil
}
