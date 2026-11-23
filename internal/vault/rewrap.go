package vault

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	// rewrapEvery is the tick of the re-wrap job (§5.9).
	rewrapEvery = time.Minute
	// rewrapWarnEvery limits the line "key rewrap failing".
	rewrapWarnEvery = time.Minute
)

// RewrapStats is what the re-wrap job counts. Nothing in it names a tenant.
type RewrapStats struct {
	OK, Failed      int64
	Versions        map[string]int64 // key-encryption key versions by state
	ErasuresPending int64
}

// Rewrapper re-wraps the data keys of every tenant under the active version and retires the
// versions that wrap none (§5.9). It runs in the worker role; each tenant's keys are handled in
// that tenant's session, in one transaction. It reads and writes no credential: a credential's
// associated data name the data key version, not the key-encryption key (ADR 0033).
type Rewrapper struct {
	DB       *store.DB
	Provider KeyProvider
	Logger   *slog.Logger

	mu         sync.Mutex
	stats      RewrapStats
	rewrapped  map[string]int64 // data keys this process re-wrapped, by the version they had
	failures   int64
	lastWarned time.Time
}

func (r *Rewrapper) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// Run passes at once and then at every tick until ctx ends.
func (r *Rewrapper) Run(ctx context.Context) {
	t := time.NewTicker(rewrapEvery)
	defer t.Stop()
	for {
		_ = r.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pass runs one pass: it reads the versions, and while one is retiring it re-wraps the data keys
// of every tenant that are not under the active version. A tenant that fails is counted and logged
// and the others go on; only a pass without a failure retires versions. The error returned says
// that something failed, never what key was involved.
func (r *Rewrapper) Pass(ctx context.Context) error {
	versions, err := r.versions(ctx)
	if err != nil {
		return r.fail(err, false)
	}
	r.publish(ctx, versions)
	active, retiring := "", false
	for _, v := range versions {
		switch v.State {
		case stateActive:
			active = v.ID
		case stateRetiring:
			retiring = true
		}
	}
	if !retiring {
		r.succeeded()
		return nil
	}
	if active == "" {
		return r.fail(errors.New("vault: no key-encryption key version is active"), false)
	}

	var ids []string
	err = r.DB.Platform(ctx, func(c context.Context, tx store.Tx) error {
		var err error
		ids, err = queries.New(tx).ListTenantsForRewrap(c)
		return err
	})
	if err != nil {
		return r.fail(err, false)
	}
	failed := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		tid, err := tenant.ParseID(id)
		if err != nil {
			continue
		}
		if err := r.rewrapTenant(ctx, tid, active); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var stop *stopError
			if errors.As(err, &stop) { // the provider's key is not the active one: no tenant can be re-wrapped
				return r.fail(stop.err, true)
			}
			failed++
			_ = r.fail(err, true)
		}
	}
	if failed > 0 {
		return fmt.Errorf("vault: %d data keys were not re-wrapped", failed)
	}

	var retired int32
	err = r.DB.Platform(ctx, func(c context.Context, tx store.Tx) error {
		var err error
		retired, err = queries.New(tx).RetireKEKVersions(c)
		return err
	})
	if err != nil {
		return r.fail(err, false)
	}
	after, err := r.versions(ctx)
	if err != nil {
		return r.fail(err, false)
	}
	r.publish(ctx, after)
	if retired > 0 {
		now := map[string]string{}
		for _, v := range after {
			now[v.ID] = v.State
		}
		for _, v := range versions {
			if v.State == stateRetiring && now[v.ID] == stateRetired {
				r.mu.Lock()
				n := r.rewrapped[v.ID]
				r.mu.Unlock()
				r.logger().Info("key rotation complete", "kek_version", v.ID, "rewrapped", n)
			}
		}
	}
	r.succeeded()
	return nil
}

// stopError ends a pass: the provider wraps under a version that is not the active one.
type stopError struct{ err error }

func (e *stopError) Error() string { return e.err.Error() }
func (e *stopError) Unwrap() error { return e.err }

const stateRetiring = "retiring"

// versions reads the key-encryption key versions in platform scope.
func (r *Rewrapper) versions(ctx context.Context) ([]queries.ListKEKVersionsRow, error) {
	var rows []queries.ListKEKVersionsRow
	err := r.DB.Platform(ctx, func(c context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListKEKVersions(c)
		return err
	})
	return rows, err
}

// publish refreshes the gauges (M50 and M51): the versions by state and the completed deletions
// whose key versions are not all retired. A failure to count leaves the earlier value.
func (r *Rewrapper) publish(ctx context.Context, versions []queries.ListKEKVersionsRow) {
	by := map[string]int64{stateActive: 0, stateRetiring: 0, stateRetired: 0}
	for _, v := range versions {
		by[v.State]++
	}
	var pending int64
	err := r.DB.Platform(ctx, func(c context.Context, tx store.Tx) error {
		var err error
		pending, err = queries.New(tx).CountPendingErasures(c)
		return err
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.Versions = by
	if err == nil {
		r.stats.ErasuresPending = pending
	}
}

// rewrapTenant re-wraps the data keys of one tenant that are not under the active version, in the
// session of the tenant, in one transaction: either every key is under the active version or none
// changed. The plaintext keys are cleared when the function returns.
func (r *Rewrapper) rewrapTenant(ctx context.Context, tid tenant.ID, active string) error {
	counted := map[string]int64{}
	err := r.DB.WithTenant(ctx, tid, func(c context.Context, tx store.Tx) error {
		clear(counted)
		q := queries.New(tx)
		rows, err := q.LockDataKeysForRewrap(c, active)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := r.rewrapKey(c, q, tid, active, row); err != nil {
				return err
			}
			counted[row.KekVersion]++
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rewrapped == nil {
		r.rewrapped = map[string]int64{}
	}
	for v, n := range counted {
		r.rewrapped[v] += n
		r.stats.OK += n
	}
	return nil
}

// rewrapKey unwraps one data key with its version, wraps it with the active one under the same
// associated data, checks that the new value unwraps to the same bytes and replaces the row.
func (r *Rewrapper) rewrapKey(ctx context.Context, q *queries.Queries, tid tenant.ID, active string, row queries.LockDataKeysForRewrapRow) error {
	aad := DataAAD(tid, row.Version)
	plain, err := r.Provider.Unwrap(ctx, row.KekVersion, row.WrappedKey, aad)
	if err != nil {
		return &rewrapError{cause: sealCause(err), err: err}
	}
	defer clear(plain)
	version, wrapped, err := r.Provider.Wrap(ctx, plain, aad)
	if err != nil {
		return &rewrapError{cause: sealCause(err), err: err}
	}
	if version != active {
		return &stopError{err: &rewrapError{cause: "kek_replaced", err: ErrKeyReplaced}}
	}
	back, err := r.Provider.Unwrap(ctx, version, wrapped, aad)
	if err != nil {
		return &rewrapError{cause: sealCause(err), err: err}
	}
	defer clear(back)
	if subtle.ConstantTimeCompare(plain, back) != 1 {
		return &rewrapError{cause: "data_key_unwrap", err: ErrUnwrap}
	}
	n, err := q.RewrapDataKey(ctx, queries.RewrapDataKeyParams{
		KekVersion: version, WrappedKey: wrapped, Version: row.Version, OldKekVersion: row.KekVersion,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("vault: a data key was not updated")
	}
	return nil
}

// rewrapError is a failure of the key material for one data key. Its text is a class; the
// provider's error is kept for errors.Is only.
type rewrapError struct {
	cause string
	err   error
}

func (e *rewrapError) Error() string { return "vault: a data key cannot be re-wrapped: " + e.cause }
func (e *rewrapError) Unwrap() error { return e.err }

// fail counts a failure and logs it at most once a minute; key says that the failure is one of a
// data key (M49 counts keys, not passes). It returns err.
func (r *Rewrapper) fail(err error, key bool) error {
	cause, class := "database", errorClass(err)
	var re *rewrapError
	if errors.As(err, &re) {
		cause, class = re.cause, "key"
	}
	r.mu.Lock()
	if key {
		r.stats.Failed++
	}
	r.failures++
	count := r.failures
	due := time.Since(r.lastWarned) >= rewrapWarnEvery
	if due {
		r.lastWarned = time.Now()
	}
	r.mu.Unlock()
	if due {
		r.logger().Warn("key rewrap failing", "cause", cause, "error_class", class, "failures", count)
	}
	return err
}

func (r *Rewrapper) succeeded() {
	r.mu.Lock()
	r.failures = 0
	r.mu.Unlock()
}

// errorClass is the class of an error that is not about key material.
func errorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case store.IsUnavailable(err):
		return "unavailable"
	case store.SQLState(err) != "":
		return "database"
	default:
		return "other"
	}
}

// Stats returns the counters.
func (r *Rewrapper) Stats() RewrapStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.stats
	out.Versions = make(map[string]int64, len(r.stats.Versions))
	for k, v := range r.stats.Versions {
		out.Versions[k] = v
	}
	return out
}
