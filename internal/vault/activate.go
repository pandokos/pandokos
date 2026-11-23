package vault

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/store"
)

// ErrKeyReplaced means the configured key-encryption key version was replaced: a newer version
// is active and this one only unwraps (ADR 0068).
var ErrKeyReplaced = errors.New("vault: the configured key-encryption key version was replaced")

// errNotImplemented marks the parts of sprint 6.3 that later steps fill in.
var errNotImplemented = errors.New("vault: not implemented")

// Activate retires the versions that wrap no data key, checks that the provider holds every
// active or retiring version and makes the provider's active version the active one (§5.8). It
// returns the version and what the database did with it.
func Activate(_ context.Context, _, _ *store.DB, _ KeyProvider) (version, result string, err error) {
	return "", "", errNotImplemented
}
