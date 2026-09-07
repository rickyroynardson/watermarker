package storage

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/require"
)

func TestIsNotFound(t *testing.T) {
	require.True(t, isNotFound(&types.NotFound{}))
	require.True(t, isNotFound(&types.NoSuchKey{}))
	require.False(t, isNotFound(errors.New("access denied")))
	require.False(t, isNotFound(nil))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPromote(t *testing.T) {
	for _, tt := range []struct {
		name       string
		copyStatus int
		copyCode   string
		headStatus int
		wantErr    string
	}{
		{"new upload", 200, "", 0, ""},
		{"already promoted", 412, "PreconditionFailed", 0, ""},
		{"staging deleted concurrently", 404, "NoSuchKey", 200, ""},
		{"missing upload", 404, "NoSuchKey", 404, "not found"},
		{"copy denied", 403, "AccessDenied", 0, "AccessDenied"},
		{"head denied", 404, "NoSuchKey", 403, "Forbidden"},
		{"missing bucket", 404, "NoSuchBucket", 0, "NoSuchBucket"},
		{"conditional conflict", 409, "ConditionalRequestConflict", 0, "ConditionalRequestConflict"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var methods []string
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				methods = append(methods, r.Method)
				require.Equal(t, "/watermarker/sources/destination", r.URL.Path)
				status, body := tt.copyStatus, `<CopyObjectResult><ETag>"copied"</ETag></CopyObjectResult>`
				if r.Method == http.MethodPut {
					require.Equal(t, "*", r.Header.Get("If-None-Match"))
					require.Equal(t, url.PathEscape("watermarker/uploads/a +?#"), r.Header.Get("X-Amz-Copy-Source"))
					if tt.copyCode != "" {
						body = "<Error><Code>" + tt.copyCode + "</Code></Error>"
					}
				} else {
					require.Equal(t, http.MethodHead, r.Method)
					require.NotZero(t, tt.headStatus)
					status, body = tt.headStatus, ""
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/xml"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			store := &S3{bucket: "watermarker", client: s3.NewFromConfig(aws.Config{
				Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, HTTPClient: client, RetryMaxAttempts: 1,
			}, func(o *s3.Options) { o.UsePathStyle = true })}
			err := store.Promote(t.Context(), "uploads/a +?#", "sources/destination")
			switch tt.wantErr {
			case "":
				require.NoError(t, err)
			case "not found":
				require.ErrorIs(t, err, ErrNotFound)
			default:
				var apiErr smithy.APIError
				require.ErrorAs(t, err, &apiErr)
				require.Equal(t, tt.wantErr, apiErr.ErrorCode())
			}
			wantMethods := []string{http.MethodPut}
			if tt.headStatus != 0 {
				wantMethods = append(wantMethods, http.MethodHead)
			}
			require.Equal(t, wantMethods, methods)
		})
	}
}
