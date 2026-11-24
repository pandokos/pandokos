package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// ErrKeyReplaced means the configured key-encryption key version was replaced: a newer version
// is active and this one only unwraps (ADR 0068).
var ErrKeyReplaced = errors.New("vault: the configured key-encryption key version was replaced")

// Activate retires the versions that wrap no data key, checks that the provider holds every
// active or retiring version and makes the provider's active version the active one (§5.8). It
// returns the version and what the database did with it ("activated" or "unchanged").
//
// A version that was replaced never comes back: the database refuses it and Activate answers
// ErrKeyReplaced, naming the configured version and the one that replaced it.
func Activate(ctx context.Context, admin, worker *store.DB, p KeyProvider) (version, result string, err error) {
	// 0. The provider proves itself before anything is written, so a refusal leaves no trace.
	if version, err = Probe(ctx, p); err != nil {
		return "", "", err
	}
	// 1. An unused old version never blocks a start.
	err = worker.Platform(ctx, func(c context.Context, tx store.Tx) error {
		_, err := queries.New(tx).RetireKEKVersions(c)
		return err
	})
	if err != nil {
		return "", "", fmt.Errorf("vault: cannot retire the unused key versions: %w", err)
	}
	// 2. Every version the database still needs is provided.
	version, err = checkVersions(ctx, admin, p, version)
	if err != nil {
		return "", "", err
	}
	// 3. The configured key becomes the active one.
	err = admin.Platform(ctx, func(c context.Context, tx store.Tx) error {
		var err error
		result, err = queries.New(tx).ActivateKEKVersion(c, queries.ActivateKEKVersionParams{ID: version, Provider: p.Name()})
		return err
	})
	if err == nil {
		return version, result, nil
	}
	if store.ErrorConstraint(err) == "kek_version_replaced" {
		return "", "", &replacedError{configured: version, active: activeVersion(ctx, admin)}
	}
	return "", "", fmt.Errorf("vault: cannot activate the key-encryption key version: %w", err)
}

// activeVersion reads the active version, or "" when it cannot be read.
func activeVersion(ctx context.Context, db *store.DB) string {
	var active string
	_ = db.Platform(ctx, func(c context.Context, tx store.Tx) error {
		rows, err := queries.New(tx).ListKEKVersions(c)
		for _, r := range rows {
			if r.State == stateActive {
				active = r.ID
			}
		}
		return err
	})
	return active
}

// replacedError is ErrKeyReplaced with the two versions, which are identifiers and not keys.
type replacedError struct{ configured, active string }

func (e *replacedError) Error() string {
	if strings.HasPrefix(e.configured, "local-") && (e.active == "" || strings.HasPrefix(e.active, "local-")) {
		if e.active == "" {
			return fmt.Sprintf("the configured key-encryption key version %s was replaced; set vault.kek.local.key_file to the key of the active version", e.configured)
		}
		return fmt.Sprintf("the configured key-encryption key version %s was replaced by %s; set vault.kek.local.key_file to the key of %s", e.configured, e.active, e.active)
	}
	if e.active == "" {
		return fmt.Sprintf("the configured key-encryption key version %s was replaced; configure the provider of the active version as vault.kek.provider", e.configured)
	}
	return fmt.Sprintf("the configured key-encryption key version %s was replaced by %s; configure the provider of %s as vault.kek.provider", e.configured, e.active, e.active)
}

func (e *replacedError) Is(target error) bool { return target == ErrKeyReplaced }
