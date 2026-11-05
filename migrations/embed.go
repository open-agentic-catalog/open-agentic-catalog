// Package migrations owns the immutable, embedded PostgreSQL migration bundle.
package migrations

import "embed"

// Files contains every release migration. Migration checksums make edits to an
// already-applied file a startup error.
//
//go:embed *.sql semantic/*.sql
var Files embed.FS
