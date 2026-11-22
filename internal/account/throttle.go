package account

import (
	"context"
	"crypto/sha256"
	"net/netip"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// maxUsernameBytes is the part of a username that the key of the account covers.
const maxUsernameBytes = 64

// UsernameKey is the key of the throttle row of a username: SHA-256 of "account", a NUL and the first
// 64 bytes of the lower-cased username. A row exists for a username whether or not an account has it.
func UsernameKey(username string) []byte {
	u := strings.ToLower(username)
	if len(u) > maxUsernameBytes {
		u = u[:maxUsernameBytes]
	}
	sum := sha256.Sum256([]byte("account\x00" + u))
	return sum[:]
}

// sourceKey is the key of the throttle row of a source address: SHA-256 of "source", a NUL and the
// address prefix, /32 for IPv4 and /64 for IPv6.
func sourceKey(a netip.Addr) []byte {
	text := "unknown"
	if a.IsValid() {
		a = a.WithZone("").Unmap()
		bits := 64
		if a.Is4() {
			bits = 32
		}
		text = netip.PrefixFrom(a, bits).Masked().String()
	}
	sum := sha256.Sum256([]byte("source\x00" + text))
	return sum[:]
}

// delayAfter is the delay that follows n failures in the window: none below three, then one second
// doubling to at most thirty.
func delayAfter(n int32) time.Duration {
	if n < 3 {
		return 0
	}
	if n > 8 {
		return 30 * time.Second
	}
	d := time.Second << (n - 3)
	return min(d, 30*time.Second)
}

// retryAfter says how long the attempt must wait, from the rows that were read: the lock of either
// row, or the delay that the failures of the account have earned (section 5.5, step 3). The delay is
// the account's alone: a source that many people share is not slowed down by one person's failures,
// it is locked at source_max_failures. Zero: the attempt may proceed. The clock is the database's,
// which stamped the rows.
func (s *Service) retryAfter(rows []queries.GetThrottleRow, account []byte) time.Duration {
	var wait time.Duration
	for _, r := range rows {
		if r.LockedUntil != nil && r.LockedUntil.After(r.Now) {
			wait = max(wait, r.LockedUntil.Sub(r.Now))
		}
		if string(r.Key) != string(account) || r.Now.Sub(r.WindowStartedAt) >= s.o.FailureWindow {
			continue
		}
		if d := delayAfter(r.Failures) - r.Now.Sub(r.LastFailureAt); d > 0 {
			wait = max(wait, d)
		}
	}
	return wait
}

// countFailure counts one failure for each key; it reports whether this failure locked the account.
func (s *Service) countFailure(ctx context.Context, tx store.Tx, account, source []byte) (lockedAccount bool, failures int32, err error) {
	q := queries.New(tx)
	window := store.Interval(s.o.FailureWindow)
	lock := store.Interval(s.o.LockDuration)
	for _, k := range []struct {
		key  []byte
		kind string
		max  int
	}{{account, "account", s.o.MaxFailures}, {source, "source", s.o.SourceMaxFailures}} {
		row, err := q.CountFailure(ctx, queries.CountFailureParams{
			Key: k.key, Kind: k.kind, FailureWindow: window, MaxFailures: int32(k.max), LockDuration: lock, //nolint:gosec // bounded by the configuration
		})
		if err != nil {
			return false, 0, err
		}
		if k.kind == "account" {
			lockedAccount = row.LockedNow != nil && *row.LockedNow
			failures = row.Failures
		}
	}
	return lockedAccount, failures, nil
}
