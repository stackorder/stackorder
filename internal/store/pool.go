package store

import (
	"net/url"
	"strings"
)

const poolMaxConnsKey = "pool_max_conns"

func setsPoolMaxConns(dsn string) bool {
	dsn = strings.TrimSpace(dsn)
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		return err == nil && u.Query().Has(poolMaxConnsKey)
	}
	fields := strings.Fields(dsn)
	for i, f := range fields {
		key, _, hasValue := strings.Cut(f, "=")
		if key != poolMaxConnsKey {
			continue
		}
		if hasValue || i+1 < len(fields) && strings.HasPrefix(fields[i+1], "=") {
			return true
		}
	}
	return false
}
