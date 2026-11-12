// Package migrations holds the schema migrations of Pandokos, embedded in the binary.
package migrations

import (
	"embed"

	"github.com/pandokos/pandokos/internal/store/migrate"
)

//go:embed sql/*.sql
var files embed.FS

// Load returns the embedded migrations, in order.
func Load() ([]migrate.Migration, error) { return migrate.Load(files, "sql") }
