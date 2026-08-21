package utils

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrInvalidCursor = errors.New("invalid cursor")

type cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
}

func EncodeCursor(createdAt time.Time, id string) string {
	b, err := json.Marshal(cursor{CreatedAt: createdAt, ID: id})
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

type CursorPagination struct {
	Limit  int    `form:"limit,default=20" binding:"min=1,max=100"`
	Cursor string `form:"cursor"`
}

// Args returns the keyset bounds, or (nil, nil) for the first page.
// A cursor that doesn't decode is ErrInvalidCursor, never a silent first page.
func (p CursorPagination) Args() (before any, beforeID any, err error) {
	if p.Cursor == "" {
		return nil, nil, nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(p.Cursor)
	if err != nil {
		return nil, nil, ErrInvalidCursor
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, nil, ErrInvalidCursor
	}
	if c.CreatedAt.IsZero() {
		return nil, nil, ErrInvalidCursor
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return nil, nil, ErrInvalidCursor
	}
	return c.CreatedAt, c.ID, nil
}

func (p CursorPagination) QueryLimit() int {
	return p.Limit + 1
}

func Page[T any](rows []T, limit int) (page []T, hasMore bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

func NextCursor(hasMore bool, lastCreatedAt time.Time, lastID string) *string {
	if !hasMore {
		return nil
	}
	s := EncodeCursor(lastCreatedAt, lastID)
	return &s
}

type OffsetPagination struct {
	Page  int `form:"page,default=1" binding:"min=1"`
	Limit int `form:"limit,default=20" binding:"min=1,max=100"`
}

func (p OffsetPagination) Offset() int {
	return (p.Page - 1) * p.Limit
}

type PageMetadata struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

func (p OffsetPagination) Metadata(total int64) PageMetadata {
	totalPages := int((total + int64(p.Limit) - 1) / int64(p.Limit))
	return PageMetadata{Page: p.Page, Limit: p.Limit, Total: total, TotalPages: totalPages}
}
