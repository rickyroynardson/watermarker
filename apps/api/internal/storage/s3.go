package storage

import (
	"context"
	"errors"
	"mime"
	"net/url"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

var ErrNotFound = errors.New("object not found")

type S3 struct {
	client    *s3.Client
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
	return &S3{client: client, presigner: s3.NewPresignClient(client), bucket: bucket}, nil
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

// Promote copies src to dst only if dst does not exist, so the first successful copy wins.
func (s *S3) Promote(ctx context.Context, src, dst string) error {
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:      aws.String(s.bucket),
		CopySource:  aws.String(url.PathEscape(s.bucket + "/" + src)),
		Key:         aws.String(dst),
		IfNoneMatch: aws.String("*"),
	})
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
		return nil // Another request already promoted this upload.
	}
	if isNotFound(err) {
		// A concurrent request may have already deleted the staging object.
		_, err = s.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(dst),
		})
		if isNotFound(err) {
			return ErrNotFound
		}
	}
	return err
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var notFound *types.NotFound
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &notFound) || errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey":
			return true
		}
	}
	return false
}

func (s *S3) PresignOutput(ctx context.Context, key string, download bool) (string, error) {
	disposition := "inline"
	if download {
		disposition = "attachment"
	}
	res, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
		ResponseContentDisposition: aws.String(mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(key)})),
	}, func(o *s3.PresignOptions) { o.Expires = 15 * time.Minute })
	if err != nil {
		return "", err
	}
	return res.URL, nil
}
