package store

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultPageSize = 50
	maxPageSize     = 500
)

type cursor struct {
	at time.Time
	id uuid.UUID
}

func encodeCursor(at time.Time, id uuid.UUID) string {
	raw := at.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(op, s string) (*cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, invalid(op, "malformed cursor")
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, invalid(op, "malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, invalid(op, "malformed cursor time")
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return nil, invalid(op, "malformed cursor id")
	}
	return &cursor{at: t, id: u}, nil
}

func encodeIDCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func decodeIDCursor(op, s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, invalid(op, "malformed cursor")
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, invalid(op, "malformed cursor")
	}
	return id, nil
}

func pageSize(limit int) int {
	switch {
	case limit <= 0:
		return defaultPageSize
	case limit > maxPageSize:
		return maxPageSize
	}
	return limit
}

func (c *cursor) args() (*time.Time, *uuid.UUID) {
	if c == nil {
		return nil, nil
	}
	return &c.at, &c.id
}
