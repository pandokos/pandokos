package vault

import (
	"context"
	"log/slog"

	"github.com/pandokos/pandokos/internal/store"
)

// RewrapStats is what the re-wrap job counts. Nothing in it names a tenant.
type RewrapStats struct {
	OK, Failed      int64
	Versions        map[string]int64 // key-encryption key versions by state
	ErasuresPending int64
}

// Rewrapper re-wraps the data keys of every tenant under the active version and retires the
// versions that wrap none (§5.9).
type Rewrapper struct {
	DB       *store.DB
	Provider KeyProvider
	Logger   *slog.Logger
}

// Run passes at every tick until ctx ends.
func (r *Rewrapper) Run(_ context.Context) {}

// Pass runs one pass.
func (r *Rewrapper) Pass(_ context.Context) error { return errNotImplemented }

// Stats returns the counters.
func (r *Rewrapper) Stats() RewrapStats { return RewrapStats{} }
