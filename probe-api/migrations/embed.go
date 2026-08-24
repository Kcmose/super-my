// Package migrations exposes the SQL migrations embedded in the probe-api binary.
package migrations

import "embed"

// Files contains every up and down SQL migration shipped with probe-api.
//
//go:embed *.sql
var Files embed.FS
