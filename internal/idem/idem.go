// Package idem makes a create operation safe to retry with an Idempotency-Key (sprint 4.2, section 5.11).
//
// A request that carries the header claims the key before it runs (Begin). The transaction of the
// create then marks the claim, through the audit record every create writes (Mark), so that the
// creation and its memory are one atomic fact. After the handler returned, Finish stores the
// response without the members that are shown once, or releases the claim.
package idem

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
)

// The refusals of Begin: the key was used with another request (422), the first request with the key
// is still running (409), and the first request committed but its response was not kept (409).
var (
	ErrKeyReused    = errors.New("idem: the key was used with another request")
	ErrInProgress   = errors.New("idem: the first request with the key is still running")
	ErrResponseLost = errors.New("idem: the first request committed and its response was not kept")
	// ErrSuperseded is returned by Mark when another attempt took the claim over: the transaction
	// of this attempt must roll back.
	ErrSuperseded = errors.New("idem: the claim was taken over by another attempt")
)

// Scope says where a key lives: a tenant (zero: the platform) and one principal.
type Scope struct {
	TenantID      tenant.ID
	PrincipalKind string // platform, service_account or user
	PrincipalID   string
}

// Replay is the first response of a key, to be written again.
type Replay struct {
	Status int
	Body   []byte
}

// Claim is the hold of one request on a key: the row, the attempt and the tenant.
type Claim struct {
	id      string
	attempt int32
	tenant  tenant.ID
	marked  atomic.Bool
}

type claimKey struct{}

// Options configure a Store.
type Options struct {
	DB        *store.DB
	Now       func() time.Time
	Retention time.Duration
}

// Store keeps the idempotency keys.
type Store struct {
	o       Options
	replays atomic.Int64
}

// Stats is a snapshot of the counters of a Store.
type Stats struct{ Replays int64 }

// Stats returns the counters.
func (s *Store) Stats() Stats { return Stats{Replays: s.replays.Load()} }

// NewStore returns a Store.
func NewStore(o Options) *Store {
	if o.Retention <= 0 {
		o.Retention = 24 * time.Hour
	}
	return &Store{o: o}
}

// run executes fn in the transaction of the scope of the key: the tenant's session, or the platform.
func (s *Store) run(ctx context.Context, tid tenant.ID, fn func(ctx context.Context, q *queries.Queries) error) error {
	f := func(ctx context.Context, tx store.Tx) error { return fn(ctx, queries.New(tx)) }
	if tid.IsZero() {
		return s.o.DB.Platform(ctx, f)
	}
	return s.o.DB.WithTenant(ctx, tid, f)
}

func tenantArg(tid tenant.ID) *string {
	if tid.IsZero() {
		return nil
	}
	v := tid.String()
	return &v
}

func requestHash(salt []byte, operation, path string, body []byte) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(operation))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}

// Begin claims the key for the request. A replay is returned for a key that completed; ctx2
// carries the claim for Mark and Finish.
func (s *Store) Begin(ctx context.Context, scope Scope, operation, path, key string, body []byte) (ctx2 context.Context, replay *Replay, err error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return ctx, nil, err
	}
	hash := requestHash(salt, operation, path, body)
	var claim *Claim
	err = s.run(ctx, scope.TenantID, func(ctx context.Context, q *queries.Queries) error {
		for try := 0; try < 2; try++ {
			row, err := q.ClaimIdempotencyKey(ctx, queries.ClaimIdempotencyKeyParams{
				PrincipalKind: scope.PrincipalKind, PrincipalID: scope.PrincipalID, Key: key, Operation: operation,
				Salt: salt, RequestHash: hash, TenantID: tenantArg(scope.TenantID), Retention: store.Interval(s.o.Retention),
			})
			if err == nil {
				claim = &Claim{id: row.ID, attempt: row.Attempt, tenant: scope.TenantID}
				return nil
			}
			if !errors.Is(err, store.ErrNoRows) {
				return err
			}
			ex, err := q.GetIdempotencyKey(ctx, queries.GetIdempotencyKeyParams{
				PrincipalKind: scope.PrincipalKind, PrincipalID: scope.PrincipalID, Key: key, TenantID: tenantArg(scope.TenantID),
			})
			if errors.Is(err, store.ErrNoRows) {
				continue // removed between the two statements
			}
			if err != nil {
				return err
			}
			if ex.Expired {
				// Past its retention: the key can be used again.
				if _, err := q.PurgeIdempotencyKeys(ctx); err != nil {
					return err
				}
				continue
			}
			if ex.Operation != operation || subtle.ConstantTimeCompare(requestHash(ex.Salt, ex.Operation, path, body), ex.RequestHash) != 1 {
				return ErrKeyReused
			}
			switch {
			case ex.Completed:
				replay = &Replay{Status: int(derefInt32(ex.ResponseStatus)), Body: []byte(ex.ResponseBody)}
				return nil
			case ex.Committed:
				return ErrResponseLost
			case ex.Locked:
				return ErrInProgress
			}
			attempt, err := q.TakeOverIdempotencyKey(ctx, ex.ID)
			if errors.Is(err, store.ErrNoRows) {
				return ErrInProgress
			}
			if err != nil {
				return err
			}
			claim = &Claim{id: ex.ID, attempt: attempt, tenant: scope.TenantID}
			return nil
		}
		return ErrInProgress
	})
	if err != nil {
		return ctx, nil, err
	}
	if replay != nil {
		s.replays.Add(1)
		return ctx, replay, nil
	}
	return context.WithValue(ctx, claimKey{}, claim), nil, nil
}

func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// Mark records, in the transaction of the create, which resource the claim made. A context without
// a claim (a request without a key) is not an error. Only the first mark of a claim is written: an
// operation that audits in several transactions marks the first.
func Mark(ctx context.Context, tx store.Tx, resourceType, resourceID string) error {
	c, _ := ctx.Value(claimKey{}).(*Claim)
	if c == nil || c.marked.Load() {
		return nil
	}
	if len(resourceID) > 64 {
		resourceID = resourceID[:64]
	}
	n, err := queries.New(tx).MarkIdempotencyKeyCommitted(ctx, queries.MarkIdempotencyKeyCommittedParams{
		ID: c.id, Attempt: c.attempt, ResourceType: resourceType, ResourceID: resourceID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSuperseded
	}
	c.marked.Store(true)
	return nil
}

// Finish stores the response of the claim of ctx, without the members that are shown once. A
// response that is not a success releases the claim, unless the create committed.
func (s *Store) Finish(ctx context.Context, status int, body []byte) error {
	c, _ := ctx.Value(claimKey{}).(*Claim)
	if c == nil {
		return nil
	}
	if status < 200 || status > 299 {
		// Not a success: nothing was created that a retry could repeat, so the claim is released
		// unless the create committed.
		return s.run(ctx, c.tenant, func(ctx context.Context, q *queries.Queries) error {
			return q.ReleaseIdempotencyKey(ctx, queries.ReleaseIdempotencyKeyParams{ID: c.id, Attempt: c.attempt})
		})
	}
	stored, err := redact(body)
	if err != nil {
		return err
	}
	st := int32(status) //nolint:gosec // 200 to 299
	return s.run(ctx, c.tenant, func(ctx context.Context, q *queries.Queries) error {
		if !c.marked.Load() {
			// A success that created nothing (a dry run): it commits here, so that the retry gets its response.
			n, err := q.MarkIdempotencyKeyCommitted(ctx, queries.MarkIdempotencyKeyCommittedParams{ID: c.id, Attempt: c.attempt})
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrSuperseded
			}
		}
		return q.CompleteIdempotencyKey(ctx, queries.CompleteIdempotencyKeyParams{ID: c.id, ResponseStatus: &st, ResponseBody: stored})
	})
}

// redact returns body as a JSON object without what must not be kept: the members that are shown
// once (the sensitive members of the audit log, and the secret of a key), at any depth. The request
// body is never stored: the row holds its hash. A body that is not a JSON object is stored as an
// empty one.
func redact(body []byte) (string, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	derr := dec.Decode(&v)
	m, ok := v.(map[string]any)
	if derr != nil || !ok {
		return "{}", nil //nolint:nilerr // a body that is not an object is stored as an empty one
	}
	out, err := json.Marshal(drop(m))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func sensitive(key string) bool {
	k := strings.ToLower(key)
	return telemetry.IsSensitiveKey(k) || k == "secret_hash" || k == "hash"
}

func drop(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			// Only a text value is a secret; an object is searched, and a flag such as
			// must_change_password stays.
			_, isString := e.(string)
			if isString && (sensitive(k) || k == "key") {
				delete(x, k)
				continue
			}
			x[k] = drop(e)
		}
	case []any:
		for i, e := range x {
			x[i] = drop(e)
		}
	}
	return v
}

// Purge removes the keys whose retention has passed: those of the platform and those of each tenant.
func (s *Store) Purge(ctx context.Context) error {
	if err := s.run(ctx, tenant.ID{}, func(ctx context.Context, q *queries.Queries) error {
		_, err := q.PurgeIdempotencyKeys(ctx)
		return err
	}); err != nil {
		return err
	}
	var ids []string
	after := false
	afterID := "00000000-0000-0000-0000-000000000000"
	for {
		var page []queries.PandokosTenant
		err := s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			page, err = queries.New(tx).ListTenants(ctx, queries.ListTenantsParams{Sort: "created_at", After: after, AfterID: afterID, PageSize: 500})
			return err
		})
		if err != nil {
			return err
		}
		for _, t := range page {
			ids = append(ids, t.ID)
		}
		if len(page) < 500 {
			break
		}
		after, afterID = true, page[len(page)-1].ID
	}
	for _, id := range ids {
		tid, err := tenant.ParseID(id)
		if err != nil {
			continue
		}
		if err := s.run(ctx, tid, func(ctx context.Context, q *queries.Queries) error {
			_, err := q.PurgeIdempotencyKeys(ctx)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
