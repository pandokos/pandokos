// Package idem makes a create operation safe to retry with an Idempotency-Key (sprint 4.2).
// Part A gives the package its names and no behaviour.
package idem

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

var errNotImplemented = errors.New("idem: not implemented")

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
type Claim struct{}

// Options configure a Store.
type Options struct {
	DB        *store.DB
	Now       func() time.Time
	Retention time.Duration
}

// Store keeps the idempotency keys.
type Store struct{ o Options }

// NewStore returns a Store.
func NewStore(o Options) *Store { return &Store{o: o} }

// Begin claims the key for the request. A replay is returned for a key that completed; ctx2
// carries the claim for Mark.
func (s *Store) Begin(ctx context.Context, scope Scope, operation, path, key string, body []byte) (ctx2 context.Context, replay *Replay, err error) {
	_, _, _, _, _, _, _ = ctx, scope, operation, path, key, body, s
	return ctx, nil, errNotImplemented
}

// Mark records, in the transaction of the create, which resource the claim made. A context without
// a claim (a request without a key) is not an error.
func Mark(ctx context.Context, tx store.Tx, resourceType, resourceID string) error {
	_, _, _, _ = ctx, tx, resourceType, resourceID
	return nil
}

// Finish stores the response of the claim of ctx, without the members that are shown once.
func (s *Store) Finish(ctx context.Context, status int, body []byte) error {
	_, _, _, _ = ctx, status, body, s
	return errNotImplemented
}
