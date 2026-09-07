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
