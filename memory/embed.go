// Package memory embeds the factory's SQL migrations so binaries can apply
// them without shipping the files separately. It is its own module because
// go:embed only reaches files at or below the package directory, and the
// migrations live in memory/ per the repo layout (SPEC.md §10).
package memory

import "embed"

// Migrations holds the goose-format SQL files under migrations/.
//
//go:embed migrations/*.sql
var Migrations embed.FS
