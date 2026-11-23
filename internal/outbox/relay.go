package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
)

// Relay delivers the events of every tenant to its destinations: it claims destinations with a
// lease, reads the events after the cursor of each and delivers them in order (sprint 6.4,
// section 5.6). The worker role runs it.
type Relay struct {
	DB          *store.DB
	Provider    vault.KeyProvider
	Deliverer   *Deliverer
	Source      string
	Interval    time.Duration
	Workers     int
	BatchSize   int
	MaxAttempts int
	BackoffMax  time.Duration
	Logger      *slog.Logger
	Now         func() time.Time
	// OnDelivery, when set, is called with the result (ok or failed) and the time of every
	// request, for the histogram M53.
	OnDelivery func(result string, d time.Duration)

	// Hooks for tests (plan step C4); nil in production. The round of a destination calls them in
	// this order: beforeRound after the claim, afterSend after its last request and before the
	// settle, beforeSettle just before the settle transaction begins, afterRound when the round is
	// over, settled or not. A test that cancels the context of Pass in afterSend or beforeRound
	// stands for a worker that dies at that point: the lease is left to expire.
	beforeRound  func(destination string)
	afterSend    func(destination string)
	beforeSettle func(destination string)
	afterRound   func(destination string)

	more atomic.Bool // a settled round had a full batch and no failure: the next pass has work

	mu            sync.Mutex // guards what follows
	stats         RelayStats
	failedPasses  int                // consecutive failed passes
	lastFailLine  time.Time          // the last "event relay failing"
	failedByKind  map[Outcome]uint64 // failed requests since the last "event deliveries failing"
	lastDelivLine time.Time
	lastGauges    time.Time
}

// RelayStats holds the counters behind the instruments M52 to M58. Nothing in it names a tenant, a
// destination or a URL.
type RelayStats struct {
	// Deliveries counts the requests sent, by outcome (M52).
	Deliveries map[Outcome]uint64
	// DeliveriesOK and DeliveriesFailed count the requests by result, and the *Seconds fields add
	// up their time (M53).
	DeliveriesOK, DeliveriesFailed           uint64
	DeliverySecondsOK, DeliverySecondsFailed float64
	DeadLetters                              uint64  // events parked (M54)
	Destinations, Failing, Disabled          int64   // the gauges of M56, as the last state read
	LagSeconds                               float64 // M57
	PassesOK, PassesFailed                   uint64  // M58
}

const (
	gaugeEvery   = time.Minute // the gauges are refreshed once a minute
	lineEvery    = time.Minute // "event relay failing" and "event deliveries failing" at most once a minute
	replayWidth  = 4           // requests in flight for one destination
	minLease     = 60 * time.Second
	maxLease     = 600 * time.Second
	defaultBatch = 100
)

// relayError says which step of a pass failed; the cause is a short label, never a name.
type relayError struct {
	cause string
	err   error
}

func (e *relayError) Error() string { return "outbox relay: " + e.cause + ": " + e.err.Error() }
func (e *relayError) Unwrap() error { return e.err }

var errBadSecret = errors.New("outbox: the signing secret of the destination is not valid")

func (r *Relay) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Relay) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (r *Relay) batch() int {
	if r.BatchSize > 0 {
		return r.BatchSize
	}
	return defaultBatch
}

func (r *Relay) workers() int {
	if r.Workers > 0 {
		return r.Workers
	}
	return 4
}

func (r *Relay) maxAttempts() int {
	if r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return 10
}

func (r *Relay) backoffMax() time.Duration {
	if r.BackoffMax > 0 {
		return r.BackoffMax
	}
	return 15 * time.Minute
}

// lease is max(60 s, 6 x the delivery timeout), within what the claim accepts.
func (r *Relay) lease() time.Duration {
	return min(max(minLease, 6*r.Deliverer.Timeout), maxLease)
}

// Run passes until ctx ends: every Interval, and again at once while a pass found a full batch.
// The gauges are refreshed once a minute, after a pass.
func (r *Relay) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = time.Second
	}
	for ctx.Err() == nil {
		_, _ = r.Pass(ctx) // the failure is counted and logged by the pass
		r.refreshGauges(ctx)
		if r.more.Swap(false) {
			continue
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// refreshGauges reads the state of delivery over the installation, at most once a minute.
func (r *Relay) refreshGauges(ctx context.Context) {
	now := r.now()
	r.mu.Lock()
	if !r.lastGauges.IsZero() && now.Sub(r.lastGauges) < gaugeEvery {
		r.mu.Unlock()
		return
	}
	r.lastGauges = now
	r.mu.Unlock()
	var st queries.GetOutboxRelayStateRow
	err := r.DB.Platform(ctx, func(c context.Context, tx store.Tx) (err error) {
		st, err = queries.New(tx).GetOutboxRelayState(c)
		return err
	})
	if err != nil {
		if ctx.Err() == nil {
			r.noteFailure(&relayError{cause: "state", err: err})
		}
		return
	}
	r.mu.Lock()
	r.stats.Destinations, r.stats.Failing, r.stats.Disabled = st.SDestinations, st.SFailing, st.SDisabled
	r.stats.LagSeconds = st.SOldestPendingSeconds
	r.mu.Unlock()
}

// Pass claims and settles one round of destinations; it returns how many rounds it ran.
func (r *Relay) Pass(ctx context.Context) (rounds int, err error) {
	started := r.now()
	defer func() { r.endPass(ctx, err) }()
	token := newToken()
	var claimed []queries.ClaimOutboxDestinationsRow
	err = r.DB.Platform(ctx, func(c context.Context, tx store.Tx) (err error) {
		claimed, err = queries.New(tx).ClaimOutboxDestinations(c, queries.ClaimOutboxDestinationsParams{
			Token: token, LeaseSeconds: int32(r.lease() / time.Second), Max: int32(r.workers() * 4), //nolint:gosec // bounded by the configuration
		})
		return err
	})
	if err != nil {
		return 0, &relayError{cause: "claim", err: err}
	}
	var (
		wg    sync.WaitGroup
		emu   sync.Mutex
		first error
		sem   = make(chan struct{}, r.workers())
	)
	for _, c := range claimed {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.round(ctx, started, token, c); err != nil {
				emu.Lock()
				if first == nil {
					first = err
				}
				emu.Unlock()
			}
		}()
	}
	wg.Wait()
	return len(claimed), first
}

// endPass counts the pass and writes the lines of section 5.15 for it. A pass that ends because
// its context ended is neither: that is a worker that stops.
func (r *Relay) endPass(ctx context.Context, err error) {
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		r.stats.PassesFailed++
		r.mu.Unlock()
		r.noteFailure(err)
		return
	}
	r.mu.Lock()
	r.stats.PassesOK++
	n := r.failedPasses
	r.failedPasses = 0
	r.mu.Unlock()
	if n > 0 {
		r.log().Info("event relay recovered", slog.Int("failures", n))
	}
}

// noteFailure counts a failure that is not the destination's and writes "event relay failing", at
// most once a minute.
func (r *Relay) noteFailure(err error) {
	cause := "other"
	var re *relayError
	if errors.As(err, &re) {
		cause = re.cause
	}
	now := r.now()
	r.mu.Lock()
	r.failedPasses++
	n := r.failedPasses
	quiet := !r.lastFailLine.IsZero() && now.Sub(r.lastFailLine) < lineEvery
	if !quiet {
		r.lastFailLine = now
	}
	r.mu.Unlock()
	if quiet {
		return
	}
	r.log().Warn("event relay failing", slog.String("error_class", errorClass(err)), slog.String("sqlstate", store.SQLState(err)),
		slog.String("cause", cause), slog.Int("failures", n))
}

// errorClass names the kind of a failure that is not the destination's; it never quotes the error.
func errorClass(err error) string {
	switch {
	case errors.Is(err, vault.ErrUndecryptable), errors.Is(err, errBadSecret):
		return "secret"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, vault.ErrUnavailable), store.IsUnavailable(err):
		return "unavailable"
	case store.SQLState(err) != "":
		return "database"
	default:
		return "other"
	}
}

// newToken returns a random UUID (version 4) for the lease of one pass.
func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// roundInput is what the read transaction of a round gives it.
type roundInput struct {
	row     queries.GetEventDestinationForRelayRow
	keys    []Key
	replays []queries.ListReplayDeadLettersRow
	events  []queries.ListOutboxEventsAfterRow
	limit   int  // the size of the read of events
	work    bool // enabled and due: events and replays are sent
	gone    bool // the destination is not ours any more
}

// round is one destination: read, deliver, settle (section 5.6). It returns an error only for a
// failure that is not the destination's.
func (r *Relay) round(ctx context.Context, started time.Time, token string, c queries.ClaimOutboxDestinationsRow) error {
	if r.beforeRound != nil {
		r.beforeRound(c.DestinationID)
	}
	if r.afterRound != nil {
		defer r.afterRound(c.DestinationID)
	}
	tid, err := tenant.ParseID(c.TenantID)
	if err != nil {
		return &relayError{cause: "claim", err: err}
	}
	in, err := r.read(ctx, tid, token, c.DestinationID)
	if err != nil {
		return err
	}
	if in.gone {
		return nil
	}
	row := in.row
	env := Envelope{Source: r.Source, TenantID: tid, TenantRef: row.ExternalReference}
	deadline := started.Add(r.lease() / 2) // no request starts after half the lease
	send := func(e Event) Result {
		res := r.Deliverer.Send(ctx, row.Url, e, env, in.keys...)
		r.observe(res)
		return res
	}
	now := r.now()
	set := queries.SettleEventDestinationParams{
		CursorSeq: row.CursorSeq, Failures: row.Failures, NextAttemptAt: row.NextAttemptAt, LastError: row.LastError,
		ID: row.ID, Token: token,
	}
	var (
		park    *queries.ParkDeadLetterParams
		deleted []int64 // replays that succeeded
		back    []queries.ReturnDeadLetterParams
		full    bool
	)

	if row.TestPending && row.TestEventID != nil {
		data, _ := json.Marshal(struct {
			DestinationID string `json:"destination_id"`
			Name          string `json:"name"`
		}{row.ID, row.Name})
		res := send(Event{ID: *row.TestEventID, Type: "io.pandokos.destination.test.v1", Time: now, Data: data})
		set.TestOutcome, set.TestStatus = string(res.Outcome), statusPtr(res)
	}

	if in.work {
		// Replays: one attempt each, out of order, with no effect on the failure state.
		letters := in.replays
		results := r.sendAll(ctx, deadline, len(letters), false, func(i int) Result {
			l := letters[i]
			return send(Event{Seq: l.Seq, ID: l.ID, Type: l.Type, Subject: l.Subject, Time: l.OccurredAt, Data: json.RawMessage(l.Data)})
		})
		for i, res := range results {
			switch {
			case !res.started:
			case res.Outcome == OutcomeDelivered:
				deleted = append(deleted, letters[i].Seq)
			default:
				back = append(back, queries.ReturnDeadLetterParams{DestinationID: row.ID, Seq: letters[i].Seq, Reason: string(res.Outcome), Status: statusPtr(res.Result)})
			}
		}

		events := in.events
		results = r.sendAll(ctx, deadline, len(events), true, func(i int) Result {
			e := events[i]
			return send(Event{Seq: e.Seq, ID: e.ID, Type: e.Type, Subject: e.Subject, Time: e.OccurredAt, Data: json.RawMessage(e.Data)})
		})
		k := 0 // the leading successes, in sequence order
		for k < len(events) && results[k].started && results[k].Outcome == OutcomeDelivered {
			k++
		}
		if k > 0 {
			set.CursorSeq = events[k-1].Seq
			set.LastSuccessAt = &now
		}
		switch {
		case k < len(events) && results[k].started: // the first failure in sequence order
			res := results[k]
			if k == 0 {
				set.Failures = row.Failures + 1
			} else {
				set.Failures = 1
			}
			set.LastError, set.LastStatus, set.LastFailureAt = string(res.Outcome), statusPtr(res.Result), &now
			if int(set.Failures) >= r.maxAttempts() {
				e := events[k]
				park = &queries.ParkDeadLetterParams{DestinationID: row.ID, Seq: e.Seq, EventID: e.ID, Type: e.Type, Attempts: set.Failures,
					Reason: string(res.Outcome), Status: statusPtr(res.Result)}
				set.CursorSeq, set.Failures = e.Seq, 0
				set.NextAttemptAt = minTime(row.NextAttemptAt, now)
			} else {
				set.NextAttemptAt = now.Add(Backoff(int(set.Failures), r.backoffMax(), nil))
			}
		case k == len(events):
			set.Failures = 0
			if k > 0 {
				set.LastError = ""
			}
			if len(events) < in.limit {
				// Nothing is left of the types of the destination: the cursor goes to the head.
				set.CursorSeq = max(set.CursorSeq, row.HeadSeq)
			} else {
				full = in.limit == r.batch()
			}
			set.NextAttemptAt = minTime(row.NextAttemptAt, now)
		default: // the time ran out before the next request
			if k > 0 {
				set.Failures, set.LastError = 0, ""
				set.NextAttemptAt = minTime(row.NextAttemptAt, now)
			}
		}
	}

	if r.afterSend != nil {
		r.afterSend(row.ID)
	}
	if r.beforeSettle != nil {
		r.beforeSettle(row.ID)
	}
	if err := ctx.Err(); err != nil { // a worker that stops settles nothing: the lease expires
		return err
	}
	var settled bool
	err = r.DB.WithTenant(ctx, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		n, err := q.SettleEventDestination(c, set)
		if err != nil || n == 0 { // 0 rows: the destination is deleted, or the lease is not ours any more
			return err
		}
		settled = true
		if park != nil {
			if err := q.ParkDeadLetter(c, *park); err != nil {
				return err
			}
		}
		for _, seq := range deleted {
			if err := q.DeleteDeadLetter(c, queries.DeleteDeadLetterParams{DestinationID: row.ID, Seq: seq}); err != nil {
				return err
			}
		}
		for _, b := range back {
			if err := q.ReturnDeadLetter(c, b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return &relayError{cause: "settle", err: err}
	}
	if settled {
		if park != nil {
			r.mu.Lock()
			r.stats.DeadLetters++
			r.mu.Unlock()
		}
		if full {
			r.more.Store(true)
		}
	}
	return nil
}

// read is the first transaction of a round, in the session of the tenant: the destination if it
// still holds the token, then what the round sends. A destination that is not enabled and has no
// test waiting, or that is not due, is only released by the settle.
func (r *Relay) read(ctx context.Context, tid tenant.ID, token, id string) (in roundInput, err error) {
	err = r.DB.WithTenant(ctx, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		row, err := q.GetEventDestinationForRelay(c, queries.GetEventDestinationForRelayParams{ID: id, Token: token})
		if errors.Is(err, store.ErrNoRows) {
			in.gone = true // deleted, or the lease expired and another worker holds it
			return nil
		}
		if err != nil {
			return &relayError{cause: "read", err: err}
		}
		in.row = row
		in.work = row.Enabled && !row.NextAttemptAt.After(r.now())
		if !in.work && !row.TestPending {
			return nil
		}
		if in.keys, err = r.openKeys(c, tx, tid, row); err != nil {
			return &relayError{cause: "secret", err: err}
		}
		if !in.work {
			return nil
		}
		in.limit = r.batch()
		if row.Failures > 0 {
			in.limit = 1 // the head, alone
		}
		if in.replays, err = q.ListReplayDeadLetters(c, queries.ListReplayDeadLettersParams{DestinationID: id, Max: int32(r.batch())}); err != nil { //nolint:gosec // bounded by the configuration
			return &relayError{cause: "read", err: err}
		}
		if in.events, err = q.ListOutboxEventsAfter(c, queries.ListOutboxEventsAfterParams{After: row.CursorSeq, Types: nonNil(row.EventTypes), Max: int32(in.limit)}); err != nil { //nolint:gosec // bounded by the configuration
			return &relayError{cause: "read", err: err}
		}
		return nil
	})
	return in, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// openKeys opens the secret of the destination and, while its expiry is in the future, the
// previous one.
func (r *Relay) openKeys(ctx context.Context, tx store.Tx, tid tenant.ID, row queries.GetEventDestinationForRelayRow) ([]Key, error) {
	open := func(sealed []byte, version int32) (Key, error) {
		plain, err := vault.OpenNamed(ctx, tx, r.Provider, tid, vault.KindWebhookSecret, row.ID, version, sealed)
		if err != nil {
			return Key{}, err
		}
		k, err := ParseKey(plain)
		if err != nil {
			return Key{}, errBadSecret
		}
		return k, nil
	}
	k, err := open(row.Secret, row.SecretKeyVersion)
	if err != nil {
		return nil, err
	}
	keys := []Key{k}
	if row.PreviousSecret != nil && row.PreviousSecretKeyVersion != nil && row.PreviousSecretExpiresAt != nil && row.PreviousSecretExpiresAt.After(r.now()) {
		p, err := open(row.PreviousSecret, *row.PreviousSecretKeyVersion)
		if err != nil {
			return nil, err
		}
		keys = append(keys, p)
	}
	return keys, nil
}

// sent is the result of the i-th request of a series, and whether it was started at all.
type sent struct {
	Result
	started bool
}

// sendAll runs n requests in waves of replayWidth, in index order: a wave starts when the one before
// it is over. With stopOnFailure, no wave starts after one that held a failure, so that no request
// starts after the first failure in sequence order, whatever the speed of the receiver. None starts
// after the deadline or after the context ended.
func (r *Relay) sendAll(ctx context.Context, deadline time.Time, n int, stopOnFailure bool, do func(i int) Result) []sent {
	out := make([]sent, n)
	for from := 0; from < n; from += replayWidth {
		if ctx.Err() != nil || r.now().After(deadline) {
			break
		}
		to := min(from+replayWidth, n)
		var wg sync.WaitGroup
		for i := from; i < to; i++ {
			out[i].started = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				out[i].Result = do(i)
			}()
		}
		wg.Wait()
		if stopOnFailure {
			failed := false
			for i := from; i < to; i++ {
				failed = failed || out[i].Outcome != OutcomeDelivered
			}
			if failed {
				break
			}
		}
	}
	return out
}

func statusPtr(res Result) *int32 {
	if res.Status == 0 {
		return nil
	}
	s := int32(res.Status) //nolint:gosec // an HTTP status
	return &s
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// observe counts one request (M52, M53) and writes "event deliveries failing", at most once a
// minute, with the most frequent outcome since the last line.
func (r *Relay) observe(res Result) {
	secs := res.Duration.Seconds()
	now := r.now()
	if r.OnDelivery != nil {
		result := "failed"
		if res.Outcome == OutcomeDelivered {
			result = "ok"
		}
		r.OnDelivery(result, res.Duration)
	}
	r.mu.Lock()
	if r.stats.Deliveries == nil {
		r.stats.Deliveries = map[Outcome]uint64{}
	}
	r.stats.Deliveries[res.Outcome]++
	var (
		worst Outcome
		count uint64
		line  bool
	)
	if res.Outcome == OutcomeDelivered {
		r.stats.DeliveriesOK++
		r.stats.DeliverySecondsOK += secs
	} else {
		r.stats.DeliveriesFailed++
		r.stats.DeliverySecondsFailed += secs
		if r.failedByKind == nil {
			r.failedByKind = map[Outcome]uint64{}
		}
		r.failedByKind[res.Outcome]++
		if r.lastDelivLine.IsZero() || now.Sub(r.lastDelivLine) >= lineEvery {
			r.lastDelivLine, line = now, true
			var total uint64
			for o, n := range r.failedByKind {
				total += n
				if n > count || (n == count && o < worst) {
					worst, count = o, n
				}
			}
			count = total
			clear(r.failedByKind)
		}
	}
	r.mu.Unlock()
	if line {
		r.log().Warn("event deliveries failing", slog.String("outcome", string(worst)), slog.Uint64("count", count))
	}
}

// Stats returns a copy of the counters.
func (r *Relay) Stats() RelayStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Deliveries = make(map[Outcome]uint64, len(r.stats.Deliveries))
	for o, n := range r.stats.Deliveries {
		s.Deliveries[o] = n
	}
	return s
}
