package utils

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin/binding"
	"github.com/stretchr/testify/assert"
)

func TestCursorRoundTrip(t *testing.T) {
	ts := time.Date(2026, 8, 21, 4, 9, 56, 123456789, time.UTC)
	id := "0195f0a0-0000-7000-8000-000000000000"

	if got := NextCursor(false, ts, id); got != nil {
		t.Errorf("last page: got %v, want nil", *got)
	}
	next := NextCursor(true, ts, id)
	if next == nil {
		t.Fatal("more rows: got nil, want cursor")
	}

	before, beforeID, err := CursorPagination{Limit: 20, Cursor: *next}.Args()
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	assert.Equal(t, ts, before, "nanosecond precision must survive the round trip")
	assert.Equal(t, id, beforeID)
}

func TestCursorArgs(t *testing.T) {
	before, beforeID, err := CursorPagination{Limit: 20}.Args()
	assert.Nil(t, before)
	assert.Nil(t, beforeID)
	assert.NoError(t, err)

	valid := EncodeCursor(time.Now(), "0195f0a0-0000-7000-8000-000000000000")
	for name, c := range map[string]string{
		"not base64":    "!!!not-base64!!!",
		"not json":      base64.RawURLEncoding.EncodeToString([]byte("nope")),
		"empty json":    base64.RawURLEncoding.EncodeToString([]byte(`{}`)),
		"bad uuid":      base64.RawURLEncoding.EncodeToString([]byte(`{"t":"2026-08-21T04:09:56Z","i":"abc"}`)),
		"missing time":  base64.RawURLEncoding.EncodeToString([]byte(`{"i":"0195f0a0-0000-7000-8000-000000000000"}`)),
		"truncated":     valid[:len(valid)-4],
		"sql injection": base64.RawURLEncoding.EncodeToString([]byte(`{"t":"2026-08-21T04:09:56Z","i":"' OR 1=1--"}`)),
	} {
		if _, _, err := (CursorPagination{Limit: 20, Cursor: c}).Args(); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: got %v, want ErrInvalidCursor", name, err)
		}
	}
}

func TestPage(t *testing.T) {
	p := CursorPagination{Limit: 3}
	if got := p.QueryLimit(); got != 4 {
		t.Errorf("QueryLimit: got %d, want 4", got)
	}

	page, hasMore := Page([]int{1, 2, 3}, p.Limit)
	assert.Equal(t, []int{1, 2, 3}, page)
	assert.False(t, hasMore)

	page, hasMore = Page([]int{1, 2, 3, 4}, p.Limit)
	assert.Equal(t, []int{1, 2, 3}, page)
	assert.True(t, hasMore)

	page, hasMore = Page([]int{1}, p.Limit)
	assert.Equal(t, []int{1}, page)
	assert.False(t, hasMore)
}

func TestCursorBinding(t *testing.T) {
	for _, tc := range []struct {
		query   string
		wantErr bool
	}{
		{"", false},
		{"limit=100", false},
		{"limit=0", true},
		{"limit=101", true},
		{"cursor=garbage", false},
	} {
		req := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
		var p CursorPagination
		err := binding.Query.Bind(req, &p)
		if (err != nil) != tc.wantErr {
			t.Errorf("query %q: err = %v, wantErr %v", tc.query, err, tc.wantErr)
		}
	}
}

func TestOffsetPagination(t *testing.T) {
	got := OffsetPagination{Page: 1, Limit: 20}.Offset()
	assert.Equal(t, 0, got)

	got = OffsetPagination{Page: 3, Limit: 20}.Offset()
	assert.Equal(t, 40, got)

	for _, tc := range []struct{ total, want int }{
		{0, 0}, {1, 1}, {20, 1}, {21, 2}, {40, 2},
	} {
		got := (OffsetPagination{Page: 1, Limit: 20}.Metadata(int64(tc.total))).TotalPages
		if got != tc.want {
			t.Errorf("total %d: got %d pages, want %d", tc.total, got, tc.want)
		}
	}
}
