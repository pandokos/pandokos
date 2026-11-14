package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// The bounds of the two caches of the Opener. A full map is emptied, as the principal cache of
// the gateway authenticator is.
const (
	maxCredentialEntries = 10000
	maxDataKeyEntries    = 10000
)

// The bounds of the configurable lifetimes (sprint 2.1, section 5.9).
const (
	minCacheTTL      = time.Second
	maxCredentialTTL = 60 * time.Second
	maxDataKeyTTL    = 15 * time.Minute
)

// The errors of Open. 3.1 maps them to the codes of ADR 0020.
var (
	// ErrDenied is returned when the principal may not have its calls authenticated with a
	// credential of its tenant.
	ErrDenied = errors.New("vault: the principal may not use a credential")
	// ErrNoCredential is returned when no credential resolves: no default, or the pinned
	// credential is not visible in the tenant. The two cases are not told apart.
	ErrNoCredential = errors.New("vault: no credential to use")
)

// NotUsableError is returned when the credential that resolves is not active.
type NotUsableError struct {
	// State is "disabled" or "revoked".
	State string
}

// Error describes the state; it names no credential.
func (e *NotUsableError) Error() string {
	return "vault: the credential is " + e.State
}

// OpenerOptions are the arguments of NewOpener.
type OpenerOptions struct {
	// DB is the gateway pool.
	DB *store.DB
	// Provider unwraps the data keys.
	Provider KeyProvider
	// Logger receives the lines of section 5.10.
	Logger *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// CredentialTTL is cache.credential_ttl: 1 s to 60 s.
	CredentialTTL time.Duration
	// DataKeyTTL is cache.data_key_ttl: 1 s to 15 m.
	DataKeyTTL time.Duration
}

// Use is what a call to Open is made for.
type Use struct {
	// Principal is the authenticated caller.
	Principal authz.Principal
	// RequestID is the ID of the request; required.
	RequestID string
	// Provider is the provider of the call, e.g. "openai"; required.
	Provider string
}

// Opened is a credential ready for use. Secret prints as the redaction marker everywhere.
type Opened struct {
	// ID, Provider, Label and Fingerprint identify the credential.
	ID, Provider, Label, Fingerprint string
	// Pinned reports whether the credential was chosen by the pin of the key in use.
	Pinned bool
	// Secret is the credential to present to the provider.
	Secret secret.Secret
}

// source is where the Opener reads. Production reads in a tenant transaction of the gateway
// role; the unit test injects a fake. A row that does not exist is store.ErrNoRows.
type source interface {
	credentialByID(ctx context.Context, id tenant.ID, credentialID string) (queries.GetCredentialForUseRow, error)
	defaultCredential(ctx context.Context, id tenant.ID, provider string) (queries.GetDefaultCredentialForUseRow, error)
	dataKey(ctx context.Context, id tenant.ID, version int32) (queries.GetDataKeyRow, error)
}

type dbSource struct{ db *store.DB }

func (s dbSource) credentialByID(ctx context.Context, id tenant.ID, credentialID string) (row queries.GetCredentialForUseRow, err error) {
	err = s.db.WithTenant(ctx, id, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetCredentialForUse(ctx, credentialID)
		return err
	})
	return row, err
}

func (s dbSource) defaultCredential(ctx context.Context, id tenant.ID, provider string) (row queries.GetDefaultCredentialForUseRow, err error) {
	err = s.db.WithTenant(ctx, id, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetDefaultCredentialForUse(ctx, provider)
		return err
	})
	return row, err
}

func (s dbSource) dataKey(ctx context.Context, id tenant.ID, version int32) (row queries.GetDataKeyRow, err error) {
	err = s.db.WithTenant(ctx, id, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetDataKey(ctx, version)
		return err
	})
	return row, err
}

// credRow is what both credential queries return.
type credRow struct {
	id, provider, label, state, fingerprint string
	keyVersion                              int32
	sealed                                  []byte
}

type credEntry struct {
	opened  Opened // Pinned is always false here; Open sets it per call
	expires time.Time
	alias   string // the provider this entry is the default of, when it was reached as one
}

type dekEntry struct {
	key        *DataKey
	kekVersion string
	expires    time.Time
}

// tenantCache is everything the Opener caches for one tenant.
type tenantCache struct {
	creds   map[string]credEntry // by credential ID
	aliases map[string]string    // provider -> credential ID of the default
	deks    map[int32]dekEntry
}

// Opener is the gateway side of the vault: it resolves the credential a call is made with,
// reads it in the tenant's session, opens it and keeps the result in two bounded caches.
type Opener struct {
	p               KeyProvider
	src             source
	log             *slog.Logger
	now             func() time.Time
	credTTL, dekTTL time.Duration

	mu      sync.Mutex
	tenants map[tenant.ID]*tenantCache
	nCreds  int
	nDeks   int
	epoch   uint64 // bumped by every invalidation, so a read that raced one is not cached
}

// NewOpener returns an Opener that reads through o.DB. It refuses a nil database, provider or
// logger and lifetimes outside the bounds of section 5.9.
func NewOpener(o OpenerOptions) (*Opener, error) {
	if o.DB == nil {
		return nil, errors.New("vault: an opener needs a database")
	}
	return newOpener(o, dbSource{db: o.DB})
}

func newOpener(o OpenerOptions, src source) (*Opener, error) {
	switch {
	case o.Provider == nil:
		return nil, errors.New("vault: an opener needs a key provider")
	case o.Logger == nil:
		return nil, errors.New("vault: an opener needs a logger")
	case o.CredentialTTL < minCacheTTL || o.CredentialTTL > maxCredentialTTL:
		return nil, errors.New("vault: the credential lifetime must be between 1s and 60s")
	case o.DataKeyTTL < minCacheTTL || o.DataKeyTTL > maxDataKeyTTL:
		return nil, errors.New("vault: the data key lifetime must be between 1s and 15m")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Opener{p: o.Provider, src: src, log: o.Logger, now: now, credTTL: o.CredentialTTL, dekTTL: o.DataKeyTTL,
		tenants: map[tenant.ID]*tenantCache{}}, nil
}

// Open returns the credential a call is made with. It requires a request ID and a provider,
// then credential:use on the principal's own tenant, then resolves the credential: the pin of
// the key when it is of the provider of the call, otherwise the default of the provider.
func (o *Opener) Open(ctx context.Context, u Use) (Opened, error) {
	if u.RequestID == "" || u.Provider == "" {
		return Opened{}, errors.New("vault: a request ID and a provider are required")
	}
	p := u.Principal
	if authz.Decide(p, authz.CredentialUse, authz.Target{TenantID: p.TenantID}) != authz.Allow {
		return Opened{}, ErrDenied
	}
	tid := p.TenantID
	epoch, start := o.epochNow(), o.now()

	if p.CredentialID != "" {
		if ent, ok := o.cachedCredential(tid, p.CredentialID); ok {
			if ent.opened.Provider == u.Provider {
				return o.opened(u, ent.opened, true, true), nil
			}
			// The pin is of another provider: it is not in play for this call.
		} else {
			row, err := o.src.credentialByID(ctx, tid, p.CredentialID)
			switch {
			case errors.Is(err, store.ErrNoRows):
				return Opened{}, ErrNoCredential
			case err != nil:
				return Opened{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
			if row.Provider == u.Provider {
				cr := credRow{id: row.ID, provider: row.Provider, label: row.Label, state: row.State,
					fingerprint: row.Fingerprint, keyVersion: row.KeyVersion, sealed: row.Secret}
				return o.openRow(ctx, u, cr, true, "", epoch, start)
			}
		}
	}

	if ent, ok := o.cachedDefault(tid, u.Provider); ok {
		return o.opened(u, ent.opened, false, true), nil
	}
	row, err := o.src.defaultCredential(ctx, tid, u.Provider)
	switch {
	case errors.Is(err, store.ErrNoRows):
		return Opened{}, ErrNoCredential
	case err != nil:
		return Opened{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	cr := credRow{id: row.ID, provider: row.Provider, label: row.Label, state: row.State,
		fingerprint: row.Fingerprint, keyVersion: row.KeyVersion, sealed: row.Secret}
	return o.openRow(ctx, u, cr, false, u.Provider, epoch, start)
}

// openRow checks the state of a credential read from the database, opens it and caches it.
func (o *Opener) openRow(ctx context.Context, u Use, r credRow, pinned bool, alias string, epoch uint64, start time.Time) (Opened, error) {
	if r.state != "active" {
		return Opened{}, &NotUsableError{State: r.state}
	}
	tid := u.Principal.TenantID
	fail := func(err error, cause, kek string) (Opened, error) {
		o.log.Error("credential cannot be opened", "request_id", u.RequestID, "tenant_id", tid.String(), "credential_id", r.id,
			"data_key_version", r.keyVersion, "kek_version", kek, "cause", cause)
		return Opened{}, err
	}
	dk, kek, cause, err := o.dataKey(ctx, tid, r.keyVersion, epoch, start)
	if err != nil {
		if cause == "" { // the database, not the key material
			return Opened{}, err
		}
		return fail(err, cause, kek)
	}
	plain, err := dk.Open(r.id, r.sealed)
	if err != nil {
		return fail(ErrUndecryptable, "authentication", kek)
	}
	op := Opened{ID: r.id, Provider: r.provider, Label: r.label, Fingerprint: r.fingerprint, Secret: secret.New(string(plain))}
	clear(plain)
	o.putCredential(tid, op, alias, epoch, start)
	return o.opened(u, op, pinned, false), nil
}

// dataKey returns the derived data key of a tenant, from the cache or from the database. cause
// is set, and err is one of the errors of Open, when the key material is at fault; it is empty
// when the database is out of reach.
func (o *Opener) dataKey(ctx context.Context, tid tenant.ID, version int32, epoch uint64, start time.Time) (dk *DataKey, kek, cause string, err error) {
	if e, ok := o.cachedDataKey(tid, version); ok {
		return e.key, e.kekVersion, "", nil
	}
	row, err := o.src.dataKey(ctx, tid, version)
	switch {
	case errors.Is(err, store.ErrNoRows):
		return nil, "", "data_key_missing", fmt.Errorf("%w: the data key of the tenant is missing", ErrUndecryptable)
	case err != nil:
		return nil, "", "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	raw, err := o.p.Unwrap(ctx, row.KekVersion, row.WrappedKey, DataAAD(tid, row.Version))
	switch {
	case errors.Is(err, ErrUnwrap):
		return nil, row.KekVersion, "data_key_unwrap", fmt.Errorf("%w: the data key does not unwrap", ErrUndecryptable)
	case err != nil: // an unknown version or a provider out of reach; the cause text is not kept
		return nil, row.KekVersion, "kek_unavailable", fmt.Errorf("%w: the key-encryption key is not available", ErrUnavailable)
	}
	defer clear(raw)
	dk, err = NewDataKey(raw, tid, row.Version)
	if err != nil {
		return nil, row.KekVersion, "data_key_unwrap", fmt.Errorf("%w: the data key is not usable", ErrUndecryptable)
	}
	o.putDataKey(tid, row.Version, dekEntry{key: dk, kekVersion: row.KekVersion}, epoch, start)
	return dk, row.KekVersion, "", nil
}

// opened logs a successful open and returns the result for this call.
func (o *Opener) opened(u Use, op Opened, pinned, hit bool) Opened {
	op.Pinned = pinned
	cache := "miss"
	if hit {
		cache = "hit"
	}
	o.log.Debug("credential opened", "request_id", u.RequestID, "tenant_id", u.Principal.TenantID.String(),
		"principal_id", u.Principal.ID, "key_id", u.Principal.KeyID, "credential_id", op.ID, "provider", op.Provider,
		"pinned", pinned, "cache", cache)
	return op
}

// Invalidate applies a notification payload of the channel pandokos_cache (section 5.6).
func (o *Opener) Invalidate(payload string) {
	switch {
	case payload == "platform", strings.HasPrefix(payload, "probe:"), payload == "catalog", strings.HasPrefix(payload, "catalog:"):
		return
	case strings.HasPrefix(payload, "vault:"), strings.HasPrefix(payload, "tenant:"):
		_, id, _ := strings.Cut(payload, ":")
		if tid, err := tenant.ParseID(id); err == nil {
			o.dropTenant(tid)
			return
		}
	}
	o.flush("unknown_payload")
}

// Flush empties both caches; the listener calls it each time it (re)connects, because notifications may have been missed.
func (o *Opener) Flush() { o.flush("reset") }

func (o *Opener) epochNow() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.epoch
}

func (o *Opener) dropTenant(tid tenant.ID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.epoch++
	if t, ok := o.tenants[tid]; ok {
		o.nCreds -= len(t.creds)
		o.nDeks -= len(t.deks)
		delete(o.tenants, tid)
	}
}

func (o *Opener) flush(reason string) {
	o.mu.Lock()
	o.epoch++
	o.tenants = map[tenant.ID]*tenantCache{}
	o.nCreds, o.nDeks = 0, 0
	o.mu.Unlock()
	o.log.Debug("vault cache flushed", "reason", reason)
}

func (o *Opener) tenantLocked(tid tenant.ID) *tenantCache {
	t := o.tenants[tid]
	if t == nil {
		t = &tenantCache{creds: map[string]credEntry{}, aliases: map[string]string{}, deks: map[int32]dekEntry{}}
		o.tenants[tid] = t
	}
	return t
}

func (o *Opener) pruneLocked(tid tenant.ID) {
	if t := o.tenants[tid]; t != nil && len(t.creds) == 0 && len(t.deks) == 0 {
		delete(o.tenants, tid)
	}
}

// removeCredLocked removes one credential entry and the alias that points to it.
func (o *Opener) removeCredLocked(tid tenant.ID, id string) {
	t := o.tenants[tid]
	if t == nil {
		return
	}
	e, ok := t.creds[id]
	if !ok {
		return
	}
	delete(t.creds, id)
	o.nCreds--
	if e.alias != "" && t.aliases[e.alias] == id {
		delete(t.aliases, e.alias)
	}
	o.pruneLocked(tid)
}

func (o *Opener) cachedCredential(tid tenant.ID, id string) (credEntry, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	t := o.tenants[tid]
	if t == nil {
		return credEntry{}, false
	}
	e, ok := t.creds[id]
	if !ok {
		return credEntry{}, false
	}
	if !o.now().Before(e.expires) {
		o.removeCredLocked(tid, id)
		return credEntry{}, false
	}
	return e, true
}

func (o *Opener) cachedDefault(tid tenant.ID, provider string) (credEntry, bool) {
	o.mu.Lock()
	t := o.tenants[tid]
	var id string
	if t != nil {
		id = t.aliases[provider]
	}
	o.mu.Unlock()
	if id == "" {
		return credEntry{}, false
	}
	e, ok := o.cachedCredential(tid, id)
	if !ok || e.opened.Provider != provider {
		return credEntry{}, false
	}
	return e, true
}

// putCredential caches a successful open. A read that started before an invalidation is not
// cached: it may carry what the invalidation was sent for.
func (o *Opener) putCredential(tid tenant.ID, op Opened, alias string, epoch uint64, start time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.epoch != epoch {
		return
	}
	o.removeCredLocked(tid, op.ID)
	if o.nCreds >= maxCredentialEntries {
		for _, t := range o.tenants {
			t.creds = map[string]credEntry{}
			t.aliases = map[string]string{}
		}
		o.nCreds = 0
		for id, t := range o.tenants {
			if len(t.deks) == 0 {
				delete(o.tenants, id)
			}
		}
		o.log.Debug("vault cache flushed", "reason", "full")
	}
	t := o.tenantLocked(tid)
	t.creds[op.ID] = credEntry{opened: op, expires: start.Add(o.credTTL), alias: alias}
	if alias != "" {
		t.aliases[alias] = op.ID
	}
	o.nCreds++
}

func (o *Opener) cachedDataKey(tid tenant.ID, version int32) (dekEntry, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	t := o.tenants[tid]
	if t == nil {
		return dekEntry{}, false
	}
	e, ok := t.deks[version]
	if !ok {
		return dekEntry{}, false
	}
	if !o.now().Before(e.expires) {
		delete(t.deks, version)
		o.nDeks--
		o.pruneLocked(tid)
		return dekEntry{}, false
	}
	return e, true
}

func (o *Opener) putDataKey(tid tenant.ID, version int32, e dekEntry, epoch uint64, start time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.epoch != epoch {
		return
	}
	if t := o.tenants[tid]; t != nil {
		if _, exists := t.deks[version]; exists {
			delete(t.deks, version)
			o.nDeks--
		}
	}
	if o.nDeks >= maxDataKeyEntries {
		for _, t := range o.tenants {
			t.deks = map[int32]dekEntry{}
		}
		o.nDeks = 0
		for id, t := range o.tenants {
			if len(t.creds) == 0 {
				delete(o.tenants, id)
			}
		}
		o.log.Debug("vault cache flushed", "reason", "full")
	}
	e.expires = start.Add(o.dekTTL)
	o.tenantLocked(tid).deks[version] = e
	o.nDeks++
}
