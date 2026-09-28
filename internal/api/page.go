package api

import (
	"cmp"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/stackorder/stackorder/internal/principal"
	"github.com/stackorder/stackorder/internal/store"
)

const (
	defaultPageSize = 50
	maxPageSize     = 500
)

var errBadCursor = &principal.InvalidError{Field: "cursor", Reason: "malformed cursor; pass the next_cursor of the previous page"}

func pageParams(r *http.Request) (int, string, error) {
	q := r.URL.Query()
	limit := defaultPageSize
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return 0, "", &principal.InvalidError{Field: "limit", Reason: "must be a positive integer"}
		}
		limit = min(n, maxPageSize)
	}
	return limit, q.Get("cursor"), nil
}

func storeCursorError(err error) error {
	if errors.Is(err, store.ErrInvalid) {
		return errBadCursor
	}
	return err
}

func encodeKeyCursor(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

func decodeKeyCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) == 0 {
		return "", errBadCursor
	}
	return string(raw), nil
}

func pageByKey[T any](items []T, key func(T) string, limit int, cursor string) ([]T, string, error) {
	after, err := decodeKeyCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
	start := 0
	if after != "" {
		start, _ = slices.BinarySearchFunc(sorted, after, func(item T, k string) int {
			if key(item) <= k {
				return -1
			}
			return 1
		})
	}
	rest := sorted[start:]
	if len(rest) <= limit {
		return append([]T{}, rest...), "", nil
	}
	page := rest[:limit]
	return append([]T{}, page...), encodeKeyCursor(key(page[len(page)-1])), nil
}
