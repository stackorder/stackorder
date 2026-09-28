// Package migrations embeds the SQL schema of the stackorder server. The
// files follow the golang-migrate naming scheme NNNN_name.up.sql and
// NNNN_name.down.sql and are applied by internal/store.
package migrations

import "embed"

// FS holds every migration file at its root.
//
//go:embed *.sql
var FS embed.FS
