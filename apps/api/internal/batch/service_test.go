package batch

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// these stubs exercise failures that the HTTP integration suite cannot reliably induce.
type failingRepo struct {
	details              BatchDetails
	detailsErr           error
	stored               Batch
	lookupErr, createErr error
}

func (r *failingRepo) ListBatches(context.Context, uuid.UUID, any, any, int) ([]ListBatchItem, error) {
	panic("not used by these tests")
}

func (r *failingRepo) FindByIdempotencyKey(context.Context, uuid.UUID, string) (Batch, bool, error) {
	return r.stored, r.stored.ID != uuid.Nil, r.lookupErr
}

func (r *failingRepo) CreateBatch(_ context.Context, b Batch) (Batch, error) {
	if r.createErr != nil {
		return Batch{}, r.createErr
	}
	r.stored = b
	return b, nil
}

type failingStore struct {
	signErr               error
	signed                []string
	promoteErr, deleteErr error
	promotions, deletions int
}

func (s *failingStore) Promote(context.Context, string, string) error {
	s.promotions++
	return s.promoteErr
}

func (s *failingStore) Delete(context.Context, string) error {
	s.deletions++
	return s.deleteErr
}

func TestCreateBatchFailures(t *testing.T) {
	owner := uuid.New()
	key := func(id string) string { return "uploads/" + owner.String() + "/" + id }
	req := CreateBatchRequest{
		IdempotencyKey: "retry", WatermarkKey: key(uuid.NewString()),
		SourceKeys: []string{key("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"), key("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")},
	}
	original := slices.Clone(req.SourceKeys)
	for _, stage := range []string{"lookup", "promotion", "commit", "cleanup", "replay during S3 outage"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("unavailable")
			repo, store := &failingRepo{}, &failingStore{}
			service := NewService(repo, store)
			switch stage {
			case "lookup":
				repo.lookupErr = failure
			case "promotion":
				store.promoteErr = failure
			case "commit":
				repo.createErr = failure
			case "cleanup":
				store.deleteErr = failure
			case "replay during S3 outage":
				_, err := service.CreateBatch(t.Context(), owner, req)
				require.NoError(t, err)
				store.promotions, store.deletions = 0, 0
				store.promoteErr = failure
			}
			res, err := service.CreateBatch(t.Context(), owner, req)
			require.Equal(t, original, req.SourceKeys, "must not mutate the caller's slice")
			if stage == "cleanup" || stage == "replay during S3 outage" {
				require.NoError(t, err)
				require.Equal(t, repo.stored.ID, res.ID)
				if stage == "cleanup" {
					require.Equal(t, 3, store.deletions)
				} else {
					require.Zero(t, store.promotions)
					require.Zero(t, store.deletions)
				}
			} else {
				require.ErrorIs(t, err, failure)
				require.Equal(t, uuid.Nil, repo.stored.ID)
				require.Zero(t, store.deletions, "failed creation must preserve uploads")
			}
		})
	}
}

func TestPersistentKeyRequiresIssuedFormat(t *testing.T) {
	owner := uuid.New()
	for _, id := range []string{"", "../other", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "{aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa}", "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", "urn:uuid:aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"} {
		_, err := persistentKey(owner, "uploads/"+owner.String()+"/"+id)
		require.ErrorIs(t, err, ErrInvalidUploadKey)
	}
}

func (r *failingRepo) GetBatch(context.Context, uuid.UUID, uuid.UUID) (BatchDetails, error) {
	return r.details, r.detailsErr
}
func (s *failingStore) PresignOutput(_ context.Context, key string, download bool) (string, error) {
	s.signed = append(s.signed, key)
	return "https://storage.example/output", s.signErr
}

func TestGetBatch(t *testing.T) {
	for _, tt := range []struct {
		statuses []string
		want     string
	}{
		{[]string{"pending", "done", "failed"}, "pending"},
		{[]string{"failed", "pending"}, "pending"},
		{[]string{"done", "failed"}, "failed"},
		{[]string{"done", "done"}, "done"},
	} {
		repo, store := &failingRepo{}, &failingStore{}
		for _, status := range tt.statuses {
			repo.details.Images = append(repo.details.Images, ImageDetails{Status: status, OutputKey: "output"})
		}
		result, err := NewService(repo, store).GetBatch(t.Context(), uuid.New(), uuid.New())
		require.NoError(t, err)
		require.Equal(t, tt.want, result.Status)
		done := 0
		for _, image := range result.Images {
			if image.Status == "done" {
				done++
				require.NotEmpty(t, image.PreviewURL)
				require.NotEmpty(t, image.DownloadURL)
			} else {
				require.Empty(t, image.PreviewURL)
				require.Empty(t, image.DownloadURL)
			}
		}
		require.Len(t, store.signed, done*2)
	}
	repo, store := &failingRepo{detailsErr: ErrBatchNotFound}, &failingStore{}
	_, err := NewService(repo, store).GetBatch(t.Context(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, ErrBatchNotFound)
	require.Empty(t, store.signed)
	repo.detailsErr = nil
	repo.details.Images = []ImageDetails{{Status: "done", OutputKey: "output"}}
	store.signErr = errors.New("signing unavailable")
	_, err = NewService(repo, store).GetBatch(t.Context(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, store.signErr)
}
