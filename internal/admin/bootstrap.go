package admin

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// ErrUsableKeyExists is returned by Bootstrap when a usable platform key exists and Recover is false.
var ErrUsableKeyExists = errors.New(`a usable platform key exists; create further keys with it, or run "pandokos init --recover" if every platform key is lost`)

// BootstrapOptions say how Bootstrap issues the key.
type BootstrapOptions struct {
	TTL         time.Duration
	Description string // default "bootstrap"
	Recover     bool
	Now         func() time.Time
	// BeforeCommit is called inside the transaction after the audit record; an error rolls back.
	BeforeCommit func(key secret.Secret, keyID string, expiresAt time.Time) error
}

// BootstrapResult is the key that Bootstrap issued.
type BootstrapResult struct {
	Key       secret.Secret
	KeyID     string
	ExpiresAt time.Time
}

// Bootstrap issues the bootstrap platform key (sprint 1.3, section 5.14, step 2). It is used by
// the init command and by the test helper, and by nothing else.
func Bootstrap(ctx context.Context, db *store.DB, o BootstrapOptions) (BootstrapResult, error) {
	if o.TTL <= 0 {
		return BootstrapResult{}, errors.New("admin: the bootstrap key needs a lifetime")
	}
	if o.Description == "" {
		o.Description = "bootstrap"
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	var res BootstrapResult
	err := db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if err := q.LockPlatformKeys(ctx); err != nil {
			return err
		}
		n, err := q.CountUsablePlatformKeys(ctx)
		if err != nil {
			return err
		}
		if n.Usable > 0 && !o.Recover {
			return ErrUsableKeyExists
		}
		key, keyID, hash, err := authn.Generate(authn.KindPlatform)
		if err != nil {
			return err
		}
		expires := now().Add(o.TTL).UTC().Truncate(time.Second)
		if _, err := q.CreatePlatformKey(ctx, queries.CreatePlatformKeyParams{
			KeyID: keyID, Description: o.Description, Role: "platform-admin", SecretHash: hash[:], Bootstrap: true, ExpiresAt: &expires,
		}); err != nil {
			return err
		}
		if err := audit.RecordPlatform(ctx, tx, audit.Event{
			Actor:  audit.Actor{Type: audit.ActorHost, Name: "pandokos init"},
			Action: "platform_key.bootstrap", ResourceType: "platform_key", ResourceID: keyID, Outcome: "success",
			Detail: map[string]any{"expires_at": expires.Format(time.RFC3339), "recover": o.Recover},
		}); err != nil {
			return err
		}
		res = BootstrapResult{Key: key, KeyID: keyID, ExpiresAt: expires}
		if o.BeforeCommit != nil {
			return o.BeforeCommit(key, keyID, expires)
		}
		return nil
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	return res, nil
}

// BootstrapKey is a bootstrap key that is still usable.
type BootstrapKey struct {
	KeyID     string
	ExpiresAt time.Time
}

// ActiveBootstrapKeys returns the bootstrap keys that are neither revoked nor expired, for the
// warning of IAM-14.
func ActiveBootstrapKeys(ctx context.Context, db *store.DB) ([]BootstrapKey, error) {
	var out []BootstrapKey
	err := db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		rows, err := queries.New(tx).ListPlatformKeys(ctx, queries.ListPlatformKeysParams{
			State: "active", Sort: "created_at", AfterID: "00000000-0000-0000-0000-000000000000", PageSize: 200,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Bootstrap && r.ExpiresAt != nil {
				out = append(out, BootstrapKey{KeyID: r.KeyID, ExpiresAt: r.ExpiresAt.UTC()})
			}
		}
		return nil
	})
	return out, err
}
