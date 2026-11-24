package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/vault"
)

// startVaultOptions are what the start sequence of the vault (section 5.10 of sprint 12.1) needs.
type startVaultOptions struct {
	Transit       *vault.TransitProvider // started first; nil when no transit provider is configured
	Provider      vault.KeyProvider      // what activates and wraps: the provider or a router
	Admin, Worker *store.DB
	Logger        *slog.Logger
	StartTimeout  time.Duration // vault.kek.transit.start_timeout; 0 waits without limit
	Now           func() time.Time
	// Sleep waits d, or returns the error of ctx when it ends first; the tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// startVault runs the start sequence: start the transit provider, activate the version. A failure
// that is vault.ErrProviderUnavailable is logged and tried again after 1 s, 2 s, 4 s up to 15 s
// until it succeeds, ctx ends or the start timeout has passed; any other failure is returned at once.
//
//nolint:revive,unparam // scaffolding: implemented in step C8
func startVault(_ context.Context, _ startVaultOptions) (version, activation string, err error) {
	return "", "", errors.New("not implemented")
}
