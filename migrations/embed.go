// Package migrations embeds the SQL migrations so they ship inside the binary.
//
// Embedding rather than reading from disk means the migrations that run are
// exactly the ones that were built and tested — a deployment cannot end up
// applying a different set from a stale directory (BR-VER-07).
package migrations

import "embed"

// FS holds every migration, applied in filename order by goose.
//
//go:embed *.sql
var FS embed.FS
