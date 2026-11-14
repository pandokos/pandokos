package authn

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Failure says why a credential was refused. It wraps authz.ErrUnauthenticated. The reason goes
// to the log and to the audit record, never to the response.
type Failure struct {
	Reason   string    // malformed, kind_not_accepted, unknown_key, wrong_secret, revoked, expired, service_account_disabled, service_account_not_valid
	KeyID    string    // when the key parsed
	TenantID tenant.ID // when the key ID belongs to a tenant
}

// Error returns "authn: " and the reason.
func (f *Failure) Error() string { return "authn: " + f.Reason }

// Unwrap returns authz.ErrUnauthenticated.
func (f *Failure) Unwrap() error { return authz.ErrUnauthenticated }

// Detail returns the reason, the key ID and the tenant ID (empty when unknown), for the log.
func (f *Failure) Detail() (reason, keyID, tenantID string) {
	if !f.TenantID.IsZero() {
		tenantID = f.TenantID.String()
	}
	return f.Reason, f.KeyID, tenantID
}

// Options configure an Authenticator.
type Options struct {
	DB       *store.DB // the pool of the role that authenticates
	Logger   *slog.Logger
	Kinds    []Kind        // kinds this authenticator accepts
	CacheTTL time.Duration // 0: no cache
	Now      func() time.Time
}

// Authenticator turns a presented key into a principal.
type Authenticator struct {
	db    *store.DB
	kinds map[Kind]bool
	now   func() time.Time
	cache *cache
}

// New validates o.
func New(o Options) (*Authenticator, error) {
	switch {
	case o.DB == nil:
		return nil, errors.New("authn: DB is required")
	case o.Logger == nil:
		return nil, errors.New("authn: Logger is required")
	case len(o.Kinds) == 0:
		return nil, errors.New("authn: at least one kind is required")
	}
	a := &Authenticator{db: o.DB, kinds: map[Kind]bool{}, now: o.Now}
	if a.now == nil {
		a.now = time.Now
	}
	for _, k := range o.Kinds {
		if k == KindPlatform && o.DB.Role() != store.RoleAdmin {
			return nil, errors.New("authn: platform keys need the pool of the admin role")
		}
		a.kinds[k] = true
	}
	if o.CacheTTL > 0 {
		a.cache = newCache(a.now, o.CacheTTL, o.Logger)
	}
	return a, nil
}

func unavailable(err error) error {
	return fmt.Errorf("%w: %s", authz.ErrUnavailable, store.SQLState(err))
}

// Authenticate verifies presented and returns the principal. A refusal is a *Failure; a database
// that cannot answer is authz.ErrUnavailable.
func (a *Authenticator) Authenticate(ctx context.Context, presented string) (authz.Principal, error) {
	keyID, kind, hash, err := Parse(presented)
	if err != nil {
		return authz.Principal{}, &Failure{Reason: "malformed"}
	}
	if !a.kinds[kind] {
		return authz.Principal{}, &Failure{Reason: "kind_not_accepted", KeyID: keyID}
	}
	if kind == KindPlatform {
		return a.platform(ctx, keyID, hash)
	}
	return a.virtual(ctx, keyID, hash)
}

func (a *Authenticator) platform(ctx context.Context, keyID string, hash [32]byte) (authz.Principal, error) {
	var row queries.GetPlatformKeyRow
	err := a.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetPlatformKey(ctx, keyID)
		return err
	})
	switch {
	case errors.Is(err, store.ErrNoRows):
		return authz.Principal{}, &Failure{Reason: "unknown_key", KeyID: keyID}
	case err != nil:
		return authz.Principal{}, unavailable(err)
	}
	now := a.now()
	switch {
	case subtle.ConstantTimeCompare(row.SecretHash, hash[:]) != 1:
		return authz.Principal{}, &Failure{Reason: "wrong_secret", KeyID: keyID}
	case row.RevokedAt != nil:
		return authz.Principal{}, &Failure{Reason: "revoked", KeyID: keyID}
	case row.ExpiresAt != nil && !row.ExpiresAt.After(now):
		return authz.Principal{}, &Failure{Reason: "expired", KeyID: keyID}
	}
	return authz.Principal{Kind: authz.KindPlatform, ID: keyID, Name: row.Description, KeyID: keyID, Bootstrap: row.Bootstrap, Roles: []string{row.Role}}, nil
}

func (a *Authenticator) virtual(ctx context.Context, keyID string, hash [32]byte) (authz.Principal, error) {
	var e *entry
	if a.cache != nil {
		if a.cache.negative(keyID) {
			return authz.Principal{}, &Failure{Reason: "unknown_key", KeyID: keyID}
		}
		e, _ = a.cache.get(keyID)
	}
	if e == nil {
		start := a.now() // the lifetime of what is cached counts from here, before the reads
		var tenantText string
		err := a.db.Unscoped(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			tenantText, err = queries.New(tx).LookupVirtualKeyTenant(ctx, keyID)
			return err
		})
		if err != nil {
			return authz.Principal{}, unavailable(err)
		}
		tid, perr := tenant.ParseID(tenantText)
		if tenantText == "" || perr != nil {
			if a.cache != nil {
				a.cache.putNegativeAt(keyID, start)
			}
			return authz.Principal{}, &Failure{Reason: "unknown_key", KeyID: keyID}
		}
		var row queries.GetKeyPrincipalRow
		err = a.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			var err error
			row, err = queries.New(tx).GetKeyPrincipal(ctx, keyID)
			return err
		})
		switch {
		case errors.Is(err, store.ErrNoRows):
			if a.cache != nil {
				a.cache.putNegativeAt(keyID, start)
			}
			return authz.Principal{}, &Failure{Reason: "unknown_key", KeyID: keyID}
		case err != nil:
			return authz.Principal{}, unavailable(err)
		}
		e = &entry{row: row}
		if a.cache != nil {
			a.cache.putAt(keyID, row.TenantID, e, start)
		}
	}
	return a.check(e, keyID, hash)
}

func (a *Authenticator) check(e *entry, keyID string, hash [32]byte) (authz.Principal, error) {
	row := e.row
	tid, err := tenant.ParseID(row.TenantID)
	if err != nil {
		return authz.Principal{}, unavailable(err)
	}
	fail := func(reason string) (authz.Principal, error) {
		return authz.Principal{}, &Failure{Reason: reason, KeyID: keyID, TenantID: tid}
	}
	now := a.now()
	switch {
	case subtle.ConstantTimeCompare(row.SecretHash, hash[:]) != 1:
		return fail("wrong_secret")
	case row.RevokedAt != nil:
		return fail("revoked")
	case row.ExpiresAt != nil && !row.ExpiresAt.After(now):
		return fail("expired")
	case row.ServiceAccountState != "active":
		return fail("service_account_disabled")
	case row.ValidFrom != nil && now.Before(*row.ValidFrom), row.ValidUntil != nil && !now.Before(*row.ValidUntil):
		return fail("service_account_not_valid")
	}
	p := authz.Principal{
		Kind: authz.KindServiceAccount, ID: row.ServiceAccountID, Name: row.ServiceAccountName, KeyID: keyID,
		TenantID: tid, TenantName: row.TenantName, TenantState: tenant.State(row.TenantState),
		Roles: append([]string{}, row.Roles...),
	}
	if row.Scoped {
		p.Scopes = append([]string{}, row.Scopes...)
	}
	if row.ModelsRestricted {
		p.Models = append([]string{}, row.Models...)
	}
	if row.CredentialID != nil {
		p.CredentialID = *row.CredentialID
	}
	return p, nil
}

// Invalidate applies a notification payload to the cache (section 5.8).
func (a *Authenticator) Invalidate(payload string) {
	if a.cache != nil {
		a.cache.invalidate(payload)
	}
}

// Flush empties the cache.
func (a *Authenticator) Flush() {
	if a.cache != nil {
		a.cache.flush("reset")
	}
}
