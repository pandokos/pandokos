package telemetry

import "time"

// The snapshots the Sources functions return. They hold counts keyed by the closed values of the
// metrics reference, or fixed fields, never an identifier, a URL, a host or the name of a tenant's
// object (ADR 0055, ADR 0062).

// LedgerStats is the usage ledger writer.
type LedgerStats struct {
	Written, Rejected, Unwritten int64 // records committed, refused by the database, lost at shutdown
	Refused                      int64 // admissions refused
	InUse, Queued                int64 // buffer slots taken, records waiting
}

// EgressStats counts the connections and URLs the egress guard refused, by reason.
type EgressStats struct{ Blocked map[string]int64 }

// AuthnStats counts the authentication attempts of one authenticator.
type AuthnStats struct {
	// Attempts is keyed by kind (key, token, unknown), result (ok, failed, unavailable) and reason
	// (empty unless the result is failed).
	Attempts map[AuthnKey]int64
}

// AuthnKey is the key of AuthnStats.Attempts.
type AuthnKey struct{ Kind, Result, Reason string }

// JWKSStats is the cache of token-issuer keys.
type JWKSStats struct {
	// Fetches is keyed by document (keys, metadata) and result (ok or a fetch class).
	Fetches map[JWKSFetchKey]int64
	// Sources counts the sources by state (fresh, stale, missing).
	Sources map[string]int64
	// OldestStale is when the oldest last success among the stale sources happened; zero when none.
	OldestStale time.Time
}

// JWKSFetchKey is the key of JWKSStats.Fetches.
type JWKSFetchKey struct{ Document, Result string }

// SignInStats counts sign-ins by method (password, oidc) and result.
type SignInStats struct {
	Attempts map[SignInKey]int64
	Locks    int64
}

// SignInKey is the key of SignInStats.Attempts.
type SignInKey struct{ Method, Result string }

// HashStats is the password hasher: slots taken and callers waiting.
type HashStats struct{ Active, Waiting int64 }

// AdminStats counts what the administration API does that no other metric sees.
type AdminStats struct {
	GrantsRefused int64
	SupportOpened int64
	SupportEnded  map[string]int64 // by cause: closed, ended
}

// LimitsStats is the limits machinery.
type LimitsStats struct {
	Reservations, Counters int64 // outstanding in memory
	ReservationsExpired    int64 // expired by the sweep: stays at zero

	Reconciliations   map[string]int64 // by result: ok, failed
	ReconcileDuration time.Duration    // of the last tick
	LastCut           time.Time        // the last cut of the reconciler; zero before the first
	Events            map[string]int64 // by result: recorded, dropped, failed
	RuleLoads         map[string]int64 // by result: ok, failed
}

// ResolverStats is the resolver's limits port and unlimited-traffic auditor.
type ResolverStats struct {
	AdmittedUnpriced int64
	Unlimited        map[string]int64 // by result: written, skipped, failed
}
