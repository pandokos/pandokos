package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// limitEvent inserts a limit event of the kind as the superuser; its event is written by the trigger.
func limitEvent(t *testing.T, d *dbtest.Database, id tenant.ID, kind string) {
	t.Helper()
	percent := "NULL"
	if kind == "threshold_reached" {
		percent = "80"
	}
	setSQL(t, d, `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed)
		VALUES ($1::uuid, $2, pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', date_trunc('day', now()), date_trunc('day', now()) + interval '1 day', `+percent+`, 10, 8)`, id.String(), kind)
}

// TestRelayRound is T-1030: a batch is delivered and the cursor is the last event; a destination
// with a type filter skips the others and ends at the head; the statements of the relay are those
// of plan A2 and none writes the log; a settle with a stale token changes nothing.
func TestRelayRound(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "relay-round")
	recv := newSink(t, nil)
	all, _ := d.SeedEventDestinationNamed(t, tid, "all", recv.url("/all"))
	filtered, _ := d.SeedEventDestinationNamed(t, tid, "filtered", recv.url("/filtered"), "io.pandokos.limit.exhausted.v1")
	d.SeedUsageRecords(t, tid, 3)
	limitEvent(t, d, tid, "exhausted")
	limitEvent(t, d, tid, "threshold_reached")
	ids := eventIDs(t, d, tid)
	if len(ids) != 5 {
		t.Fatalf("%d events in the log", len(ids))
	}
	exhausted := ids[3]
	r := newRig(t, d)

	rounds := r.drain()
	if rounds < 2 {
		t.Errorf("%d rounds for two destinations with work", rounds)
	}
	got, n := recv.ids("/all")
	if n != 5 || len(got) != 5 {
		t.Errorf("the destination for every type received %d requests for %d events, want 5 and 5", n, len(got))
	}
	for _, id := range ids {
		if recv.count("/all", id) != 1 {
			t.Errorf("event %s was sent %d times to /all", id, recv.count("/all", id))
		}
	}
	if got, n := recv.ids("/filtered"); n != 1 || len(got) != 1 || got[0] != exhausted {
		t.Errorf("the filtered destination received %v (%d requests), want the exhausted event only", got, n)
	}
	for _, id := range []string{all, filtered} {
		row := readDest(t, d, id)
		if row.Cursor != 5 || row.Failures != 0 || row.Leased || row.FailingSince != nil || row.LastSuccess == nil || row.LastFailure != nil {
			t.Errorf("destination %s after the round: %+v, want the cursor at the head (5), no failure and no lease", id, row)
		}
	}
	// Every request is a signed delivery of the document the log holds.
	events := d.OutboxEvents(t, tid)
	for _, dl := range recv.deliveries() {
		if dl.Timestamp == "" || !strings.HasPrefix(dl.Signature, "v1,") {
			t.Errorf("a request without its headers: %+v", dl)
		}
		var doc map[string]any
		if err := json.Unmarshal(dl.Body, &doc); err != nil || doc["id"] != dl.ID || doc["source"] != "urn:pandokos" || doc["specversion"] != "1.0" || doc["tenantid"] != tid.String() {
			t.Errorf("the body of a delivery: %s (%v)", dl.Body, err)
		}
		found := false
		for _, e := range events {
			found = found || (e.ID == dl.ID && doc["sequence"] == itoa(e.Seq))
		}
		if !found {
			t.Errorf("a delivery of %s with the sequence %v matches no event of the log", dl.ID, doc["sequence"])
		}
	}
	// The statements: plan A2 and the store's own, none writing the log; no statement names a secret in the clear.
	checkStatements(t, r.proxy.statements())
	st := r.relay.Stats()
	if st.Deliveries[OutcomeDelivered] != 6 || st.DeliveriesOK != 6 || st.DeliveriesFailed != 0 || st.PassesOK < 1 || st.PassesFailed != 0 {
		t.Errorf("the stats after the round: %+v, want 6 deliveries ok", st)
	}
	if st.DeliverySecondsOK <= 0 {
		t.Errorf("the time of the requests is %v", st.DeliverySecondsOK)
	}

	t.Run("a settle with a stale token changes nothing", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		tid := d.SeedTenant(t, "relay-stale")
		recv := newSink(t, nil)
		dest, _ := d.SeedEventDestination(t, tid, recv.url("/x"))
		d.SeedUsageRecords(t, tid, 4)
		r := newRig(t, d, func(rl *Relay) {
			rl.beforeSettle = func(destination string) {
				// The lease expired and somebody else holds the destination.
				setSQL(t, d, `UPDATE pandokos.event_destination SET lease_token = pandokos.uuid_v7(), lease_until = now() + interval '1 minute' WHERE id = $1::uuid`, destination)
			}
		})
		if _, err := r.pass(); err != nil {
			t.Fatal(err)
		}
		if _, n := recv.ids("/x"); n != 4 {
			t.Fatalf("%d requests, want the 4 events delivered before the settle", n)
		}
		row := readDest(t, d, dest)
		if row.Cursor != 0 || row.Failures != 0 || row.LastSuccess != nil || !row.Leased {
			t.Errorf("after a settle with a stale token: %+v, want the destination as the other holder left it", row)
		}
	})
}

// TestRelayHeadOfLine is T-1031: a failing head is sent alone at the stored times; after
// max_attempts it is a dead letter and the events behind it flow; a success clears the state.
func TestRelayHeadOfLine(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "relay-head")
	var badID atomic.Value
	badID.Store("")
	var failUntil atomic.Int64 // the event badID answers 500 for this many requests; then 204
	failUntil.Store(1 << 30)
	recv := newSink(t, func(dl delivery, n int) int {
		if dl.ID == badID.Load().(string) && int64(n) <= failUntil.Load() {
			return 500
		}
		return 204
	})
	dest, _ := d.SeedEventDestination(t, tid, recv.url("/q"))
	d.SeedUsageRecords(t, tid, 5)
	ids := eventIDs(t, d, tid)
	badID.Store(ids[1])
	r := newRig(t, d, func(rl *Relay) { rl.MaxAttempts = 3 })

	// First round: the head is the first event; it is delivered, the second fails, nothing starts after
	// the first failure in sequence order, and the cursor is the event before it.
	if _, err := r.pass(); err != nil {
		t.Fatal(err)
	}
	row := readDest(t, d, dest)
	if row.Cursor != 1 || row.Failures != 1 || row.FailingSince == nil || row.LastError != "status" || row.LastStatus == nil || *row.LastStatus != 500 || row.Leased || row.LastFailure == nil {
		t.Fatalf("after the first failure: %+v, want cursor 1, one failure, last error status 500", row)
	}
	wait := time.Until(row.NextAttempt)
	if wait < 3500*time.Millisecond || wait > 5*time.Second {
		t.Errorf("the next attempt is in %v, want 5 s less up to 20 %%", wait)
	}
	if recv.count("/q", ids[4]) != 0 {
		t.Errorf("the fifth event was sent after the failure of the second in sequence order")
	}
	// Not due: a pass runs no round.
	if n, err := r.pass(); err != nil || n != 0 {
		t.Errorf("a pass before the stored time ran %d rounds (%v)", n, err)
	}
	before := recv.count("/q", ids[1])
	// While failures > 0 a round sends the head alone, at the stored times.
	for attempt := 2; attempt <= 3; attempt++ {
		due(t, d, dest)
		sentBefore := len(recv.deliveries())
		if _, err := r.pass(); err != nil {
			t.Fatal(err)
		}
		fresh := recv.deliveries()[sentBefore:]
		if attempt == 2 {
			if len(fresh) != 1 || fresh[0].ID != ids[1] {
				t.Errorf("the round of attempt 2 sent %d requests, the first for %s: want the head alone", len(fresh), fresh[0].ID)
			}
			row = readDest(t, d, dest)
			if row.Failures != 2 {
				t.Errorf("failures %d after attempt 2", row.Failures)
			}
			if w := time.Until(row.NextAttempt); w < 7500*time.Millisecond || w > 10*time.Second {
				t.Errorf("the next attempt after the second failure is in %v, want 10 s less up to 20 %%", w)
			}
		}
	}
	if got := recv.count("/q", ids[1]) - before; got != 2 {
		t.Errorf("the failing event was tried %d more times, want 2 (attempts 2 and 3)", got)
	}
	// After max_attempts it is parked, the cursor is past it and the failure state is clear.
	row = readDest(t, d, dest)
	if letters := readLetters(t, d, dest); len(letters) != 1 || letters[0].Seq != 2 || letters[0].State != "dead" || letters[0].Attempts != 3 || letters[0].Reason != "status" || letters[0].Status == nil || *letters[0].Status != 500 {
		t.Errorf("the dead letters are %+v, want the second event after 3 attempts", letters)
	}
	if row.Cursor != 2 || row.Failures != 0 || row.FailingSince != nil || time.Until(row.NextAttempt) > time.Second {
		t.Errorf("after the dead letter: %+v, want the cursor past the letter, no failure, due at once", row)
	}
	if st := r.relay.Stats(); st.DeadLetters != 1 {
		t.Errorf("the dead letters counted: %d", st.DeadLetters)
	}
	// The next round delivers the events behind it; each is sent again at most once.
	r.drain()
	row = readDest(t, d, dest)
	if row.Cursor != 5 || row.Failures != 0 {
		t.Errorf("after the queue flowed: %+v", row)
	}
	for i := 2; i < 5; i++ {
		if n := recv.count("/q", ids[i]); n < 1 || n > 2 {
			t.Errorf("event %d was sent %d times, want 1 or 2 (at most once again)", i+1, n)
		}
	}
	if n := recv.count("/q", ids[0]); n != 1 {
		t.Errorf("the first event was sent %d times", n)
	}

	t.Run("a success clears the state", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		tid := d.SeedTenant(t, "relay-recover")
		var bad atomic.Value
		bad.Store("")
		var calls atomic.Int64
		recv := newSink(t, func(dl delivery, _ int) int {
			if dl.ID == bad.Load().(string) && calls.Add(1) <= 2 {
				return 503
			}
			return 204
		})
		dest, _ := d.SeedEventDestination(t, tid, recv.url("/r"))
		d.SeedUsageRecords(t, tid, 3)
		ids := eventIDs(t, d, tid)
		bad.Store(ids[0])
		r := newRig(t, d)
		for i := 0; i < 3; i++ {
			if i > 0 {
				due(t, d, dest)
			}
			if _, err := r.pass(); err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				if row := readDest(t, d, dest); row.Cursor != 0 || row.Failures != 1 {
					t.Fatalf("after the first failure: %+v", row)
				}
			}
		}
		due(t, d, dest)
		r.drain()
		row := readDest(t, d, dest)
		if row.Cursor != 3 || row.Failures != 0 || row.FailingSince != nil || row.LastSuccess == nil {
			t.Errorf("after the receiver recovered: %+v, want the queue flowing and no failure", row)
		}
		if letters := readLetters(t, d, dest); len(letters) != 0 {
			t.Errorf("dead letters %+v after a recovery", letters)
		}
	})
}

// TestRelayCrashMidDelivery is T-1032: a relay that stops after the receiver answered and before the
// settle, or before the request: the lease expires, a second relay delivers again, and the
// receiver's store holds each ID once and none is missing.
func TestRelayCrashMidDelivery(t *testing.T) {
	for _, c := range []struct {
		name       string
		afterSend  bool
		duplicates bool
	}{{"after the answer, before the settle", true, true}, {"before the request", false, false}} {
		t.Run(c.name, func(t *testing.T) {
			d := dbtest.NewMigrated(t)
			tid := d.SeedTenant(t, "relay-crash")
			store := map[string]int{}
			var mu sync.Mutex
			recv := newSink(t, func(dl delivery, _ int) int {
				mu.Lock()
				store[dl.ID]++
				mu.Unlock()
				return 204
			})
			dest, _ := d.SeedEventDestination(t, tid, recv.url("/c"))
			d.SeedUsageRecords(t, tid, 5)
			ids := eventIDs(t, d, tid)

			ctx, kill := context.WithCancel(context.Background())
			dying := newRig(t, d, func(rl *Relay) {
				if c.afterSend {
					rl.afterSend = func(string) { kill() }
				} else {
					rl.beforeRound = func(string) { kill() }
				}
			})
			_, _ = dying.relay.Pass(ctx)
			kill()
			row := readDest(t, d, dest)
			if row.Cursor != 0 || !row.Leased {
				t.Fatalf("after the stop: %+v, want the cursor unmoved and the lease held", row)
			}
			mu.Lock()
			sent := len(store)
			mu.Unlock()
			if c.afterSend && sent != 5 || !c.afterSend && sent != 0 {
				t.Fatalf("the receiver got %d events before the stop", sent)
			}
			// The lease is still held: a second relay finds nothing to claim.
			second := newRig(t, d)
			if n, err := second.pass(); err != nil || n != 0 {
				t.Errorf("a second relay before the lease expired ran %d rounds (%v)", n, err)
			}
			expireLease(t, d, dest)
			second.drain()
			row = readDest(t, d, dest)
			if row.Cursor != 5 || row.Leased || row.Failures != 0 {
				t.Errorf("after the second relay: %+v", row)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range ids {
				if store[id] == 0 {
					t.Errorf("event %s never reached the receiver", id)
				}
				if c.duplicates && store[id] != 2 || !c.duplicates && store[id] != 1 {
					t.Errorf("event %s was delivered %d times, want %d", id, store[id], map[bool]int{true: 2, false: 1}[c.duplicates])
				}
			}
			if len(store) != 5 {
				t.Errorf("the receiver's store holds %d IDs, want 5", len(store))
			}
		})
	}
}

// TestRelayTwoWorkers is T-1033: two relays on one database, five destinations and 2000 events: no
// destination has two rounds at once, and every event reaches every destination.
func TestRelayTwoWorkers(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "relay-two")
	recv := newSink(t, nil)
	var dests []string
	for i := 0; i < 5; i++ {
		id, _ := d.SeedEventDestinationNamed(t, tid, fmt.Sprintf("dest-%d", i), recv.url(fmt.Sprintf("/w%d", i)))
		dests = append(dests, id)
	}
	d.SeedUsageRecords(t, tid, 2000)
	ids := eventIDs(t, d, tid)

	var mu sync.Mutex
	inRound := map[string]int{}
	rounds := map[string]int{}
	overlap := atomic.Int64{}
	hooks := func(rl *Relay) {
		rl.beforeRound = func(dest string) {
			mu.Lock()
			defer mu.Unlock()
			inRound[dest]++
			rounds[dest]++
			if inRound[dest] > 1 {
				overlap.Add(1)
			}
		}
		rl.afterRound = func(dest string) {
			mu.Lock()
			defer mu.Unlock()
			inRound[dest]--
		}
	}
	one, two := newRig(t, d, hooks), newRig(t, d, hooks)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, r := range []*rig{one, two} {
		wg.Add(1)
		go func(r *Relay) { defer wg.Done(); r.Run(ctx) }(r.relay)
	}
	deadline := time.Now().Add(80 * time.Second)
	for {
		done := true
		for _, id := range dests {
			if readDest(t, d, id).Cursor != 2000 {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			wg.Wait()
			t.Fatalf("the destinations did not reach the head in time: %+v", readDest(t, d, dests[0]))
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	if n := overlap.Load(); n != 0 {
		t.Errorf("%d rounds of a destination began while another of it was running", n)
	}
	mu.Lock()
	for _, dest := range dests {
		if rounds[dest] < 20 {
			t.Errorf("destination %s had %d rounds for 2000 events in batches of 100", dest, rounds[dest])
		}
	}
	mu.Unlock()
	for i := range dests {
		got, _ := recv.ids(fmt.Sprintf("/w%d", i))
		if len(got) != len(ids) {
			t.Errorf("destination %d received %d distinct events, want %d", i, len(got), len(ids))
		}
	}
}

// TestRelayReplayAndRewind is T-1034.
func TestRelayReplayAndRewind(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "relay-replay")
	var failSeq4 atomic.Bool
	failSeq4.Store(true)
	var four atomic.Value
	four.Store("")
	recv := newSink(t, func(dl delivery, _ int) int {
		if dl.ID == four.Load().(string) && failSeq4.Load() {
			return 502
		}
		return 204
	})
	dest, _ := d.SeedEventDestination(t, tid, recv.url("/p"))
	d.SeedUsageRecords(t, tid, 6)
	ids := eventIDs(t, d, tid)
	four.Store(ids[3])
	r := newRig(t, d)
	r.drain()
	// Two letters are dead and the cursor is at the head, as after a long outage.
	setSQL(t, d, `UPDATE pandokos.event_destination SET cursor_seq = 6 WHERE id = $1::uuid`, dest)
	for _, seq := range []int{2, 4} {
		setSQL(t, d, `INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason, status)
			SELECT tenant_id, $1::uuid, seq, id, type, 10, 'status', 500 FROM pandokos.outbox_event WHERE tenant_id = $2::uuid AND seq = $3`, dest, tid.String(), seq)
	}
	base2, base4 := recv.count("/p", ids[1]), recv.count("/p", ids[3])
	admin := d.Open(t, store.RoleAdmin)
	replay := func(seqs ...int64) {
		t.Helper()
		err := admin.WithTenant(context.Background(), tid, func(c context.Context, tx store.Tx) error {
			_, err := queries.New(tx).ReplayEventDeadLetters(c, queries.ReplayEventDeadLettersParams{DestinationID: dest, Seqs: append([]int64{}, seqs...)})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("replay of all: one attempt each, removed on success, parked again on failure", func(t *testing.T) {
		replay()
		r.drain()
		if got := recv.count("/p", ids[1]) - base2; got != 1 {
			t.Errorf("the letter of seq 2 was sent %d times", got)
		}
		if got := recv.count("/p", ids[3]) - base4; got != 1 {
			t.Errorf("the letter of seq 4 was sent %d times", got)
		}
		letters := readLetters(t, d, dest)
		if len(letters) != 1 || letters[0].Seq != 4 || letters[0].State != "dead" || letters[0].Attempts != 11 || letters[0].Status == nil || *letters[0].Status != 502 {
			t.Errorf("the letters are %+v, want seq 4 parked again with one more attempt and the new status", letters)
		}
		if row := readDest(t, d, dest); row.Cursor != 6 || row.Failures != 0 {
			t.Errorf("a replay moved the state of the destination: %+v", row)
		}
	})
	t.Run("replay of a chosen letter", func(t *testing.T) {
		failSeq4.Store(false)
		replay(4, 99)
		r.drain()
		if letters := readLetters(t, d, dest); len(letters) != 0 {
			t.Errorf("the letters are %+v, want none after a replay that succeeded", letters)
		}
	})
	t.Run("rewind to a sequence number delivers again", func(t *testing.T) {
		setSQL(t, d, `UPDATE pandokos.event_destination SET cursor_seq = 3, failures = 0, failing_since = NULL, next_attempt_at = now() - interval '1 second', lease_token = NULL, lease_until = NULL WHERE id = $1::uuid`, dest)
		before := map[string]int{}
		for _, id := range ids {
			before[id] = recv.count("/p", id)
		}
		r.drain()
		for i, id := range ids {
			want := 0
			if i >= 3 {
				want = 1
			}
			if got := recv.count("/p", id) - before[id]; got != want {
				t.Errorf("event %d was sent %d times after the rewind, want %d", i+1, got, want)
			}
		}
	})
	t.Run("rewind to a time delivers again", func(t *testing.T) {
		d.AgeOutboxUpTo(t, tid, 2*time.Hour, 3)
		err := admin.WithTenant(context.Background(), tid, func(c context.Context, tx store.Tx) error {
			since := time.Now().Add(-time.Hour)
			cursor, err := queries.New(tx).RewindEventDestination(c, queries.RewindEventDestinationParams{ID: dest, Since: &since})
			if cursor != 3 {
				t.Errorf("the rewind to a time answered the cursor %d, want 3 (the last event recorded before it)", cursor)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		before := recv.count("/p", ids[4])
		r.drain()
		if got := recv.count("/p", ids[4]) - before; got != 1 {
			t.Errorf("an event after the time was sent %d times", got)
		}
	})
	t.Run("a rewind during a round makes its settle a no-op", func(t *testing.T) {
		setSQL(t, d, `UPDATE pandokos.event_destination SET cursor_seq = 0 WHERE id = $1::uuid`, dest)
		due(t, d, dest)
		var once atomic.Bool
		rewinding := newRig(t, d, func(rl *Relay) {
			rl.beforeSettle = func(destination string) {
				if once.CompareAndSwap(false, true) {
					err := admin.WithTenant(context.Background(), tid, func(c context.Context, tx store.Tx) error {
						zero := int64(0)
						_, err := queries.New(tx).RewindEventDestination(c, queries.RewindEventDestinationParams{ID: destination, After: &zero})
						return err
					})
					if err != nil {
						t.Errorf("the rewind: %v", err)
					}
				}
			}
		})
		if _, err := rewinding.pass(); err != nil {
			t.Fatal(err)
		}
		if row := readDest(t, d, dest); row.Cursor != 0 || row.Leased {
			t.Errorf("after the rewind that came during the round: %+v, want the cursor at 0 and no lease: the settle changed nothing", row)
		}
		rewinding.drain()
		if row := readDest(t, d, dest); row.Cursor != 6 {
			t.Errorf("the next round did not deliver again: %+v", row)
		}
	})
}

// rotateWith replaces the secret of a destination as the administration does: it seals a new one
// under the data key and asks for the replaced one to stay valid for the given time.
func rotateWith(t *testing.T, d *dbtest.Database, tid tenant.ID, dest string, seconds int32) (newText string) {
	t.Helper()
	newText = dbtest.NewWebhookSecret(t)
	sealer, err := vault.NewSealer(vaulttest.ProviderFor(t, d))
	if err != nil {
		t.Fatal(err)
	}
	err = d.Open(t, store.RoleAdmin).WithTenant(context.Background(), tid, func(c context.Context, tx store.Tx) error {
		sealed, err := sealer.SealNamed(c, tx, tid, vault.KindWebhookSecret, dest, secret.New(newText))
		if err != nil {
			return err
		}
		n, err := queries.New(tx).RotateEventDestinationSecret(c, queries.RotateEventDestinationSecretParams{ID: dest, Secret: sealed.Secret, SecretKeyVersion: sealed.KeyVersion, PreviousSeconds: seconds})
		if err == nil && n != 1 {
			err = fmt.Errorf("%d rows", n)
		}
		return err
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	return newText
}

// TestRelaySecretRotation is T-1035: after a rotation requests carry two signatures until the
// expiry, then one; a receiver with either key accepts during the overlap.
func TestRelaySecretRotation(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "relay-rotate")
	recv := newSink(t, nil)
	dest, oldKey := d.SeedEventDestination(t, tid, recv.url("/k"))
	r := newRig(t, d)

	d.SeedUsageRecords(t, tid, 1)
	r.drain()
	first := recv.deliveries()
	if len(first) != 1 || len(strings.Fields(first[0].Signature)) != 1 || !first[0].verifies(oldKey) {
		t.Fatalf("before the rotation: %+v, want one signature of the first key", first)
	}

	newKey := rotateWith(t, d, tid, dest, 3600)
	if newKey == oldKey {
		t.Fatal("the rotation kept the key")
	}
	d.SeedUsageRecords(t, tid, 1)
	r.drain()
	during := recv.deliveries()[1:]
	if len(during) != 1 {
		t.Fatalf("%d deliveries during the overlap", len(during))
	}
	if n := len(strings.Fields(during[0].Signature)); n != 2 {
		t.Errorf("the signature header holds %d signatures during the overlap, want 2", n)
	}
	if !during[0].verifies(oldKey) || !during[0].verifies(newKey) {
		t.Errorf("a receiver with either key must accept during the overlap")
	}
	// The expiry passes: one signature, the new key's.
	setSQL(t, d, `UPDATE pandokos.event_destination SET previous_secret_expires_at = now() - interval '1 second' WHERE id = $1::uuid`, dest)
	d.SeedUsageRecords(t, tid, 1)
	r.drain()
	after := recv.deliveries()[2:]
	if len(after) != 1 || len(strings.Fields(after[0].Signature)) != 1 || !after[0].verifies(newKey) || after[0].verifies(oldKey) {
		t.Errorf("after the expiry: %+v, want one signature, the new key's", after)
	}
	// A rotation with no expiry keeps no previous key at all.
	third := rotateWith(t, d, tid, dest, 0)
	d.SeedUsageRecords(t, tid, 1)
	r.drain()
	last := recv.deliveries()[3:]
	if len(last) != 1 || len(strings.Fields(last[0].Signature)) != 1 || !last[0].verifies(third) {
		t.Errorf("after a rotation with no overlap: %+v", last)
	}
}

// failingProvider is a key provider that can be made to fail, as a key-encryption key that is out of
// reach does.
type failingProvider struct {
	vault.KeyProvider
	down atomic.Bool
}

func (p *failingProvider) Unwrap(ctx context.Context, version string, wrapped, aad []byte) ([]byte, error) {
	if p.down.Load() {
		return nil, vault.ErrUnwrap
	}
	return p.KeyProvider.Unwrap(ctx, version, wrapped, aad)
}

func (p *failingProvider) Available(ctx context.Context, version string) error {
	if p.down.Load() {
		return vault.ErrUnwrap
	}
	return p.KeyProvider.Available(ctx, version)
}

// TestRelayNotTheDestinationsFault is T-1036: a database out of reach gives one line "event relay
// failing", then "recovered", and counts the passes; a key-encryption key that is unavailable sends
// no request, counts no attempt, names its cause, and delivery resumes when it is back.
func TestRelayNotTheDestinationsFault(t *testing.T) {
	t.Run("the database out of reach", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		tid := d.SeedTenant(t, "relay-db")
		recv := newSink(t, nil)
		dest, _ := d.SeedEventDestination(t, tid, recv.url("/f"))
		d.SeedUsageRecords(t, tid, 2)
		r := newRig(t, d)
		d.SetAcceptingConnections(t, false)
		for i := 0; i < 3; i++ {
			if _, err := r.pass(); err == nil {
				t.Errorf("pass %d succeeded with the database out of reach", i)
			}
		}
		d.SetAcceptingConnections(t, true)
		lines := r.logs.Find(t, "event relay failing")
		if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["error_class"] == nil || lines[0]["error_class"] == "" {
			t.Errorf("the lines %v, want one warning with an error_class, however many passes failed (at most once a minute)", lines)
		}
		if st := r.relay.Stats(); st.PassesFailed != 3 || st.PassesOK != 0 {
			t.Errorf("the passes counted: %+v, want 3 failed", st)
		}
		if _, n := recv.ids("/f"); n != 0 {
			t.Errorf("%d requests while the database was out of reach", n)
		}
		r.drain()
		rec := r.logs.Find(t, "event relay recovered")
		if len(rec) != 1 || rec[0]["level"] != "INFO" || fmt.Sprint(rec[0]["failures"]) != "3" {
			t.Errorf("the recovery lines %v, want one info line with failures 3", rec)
		}
		if row := readDest(t, d, dest); row.Cursor != 2 || row.Failures != 0 {
			t.Errorf("after the recovery: %+v, want the events delivered and no failure counted against the destination", row)
		}
		if st := r.relay.Stats(); st.PassesOK < 1 {
			t.Errorf("no pass counted as ok: %+v", st)
		}
	})

	t.Run("a key-encryption key that is unavailable", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		tid := d.SeedTenant(t, "relay-kek")
		recv := newSink(t, nil)
		dest, _ := d.SeedEventDestination(t, tid, recv.url("/g"))
		d.SeedUsageRecords(t, tid, 2)
		fp := &failingProvider{KeyProvider: vaulttest.ProviderFor(t, d)}
		fp.down.Store(true)
		r := newRig(t, d, func(rl *Relay) { rl.Provider = fp })
		for i := 0; i < 2; i++ {
			_, _ = r.pass()
		}
		if _, n := recv.ids("/g"); n != 0 {
			t.Errorf("%d requests were made without the key", n)
		}
		row := readDest(t, d, dest)
		if row.Failures != 0 || row.FailingSince != nil || row.LastError != "" || row.Cursor != 0 || row.LastFailure != nil {
			t.Errorf("an attempt was counted against the destination: %+v", row)
		}
		lines := r.logs.Find(t, "event relay failing")
		if len(lines) != 1 || lines[0]["cause"] == nil || lines[0]["cause"] == "" {
			t.Errorf("the lines %v, want one line that names the cause", lines)
		}
		// The destination's lease is released or expires; delivery resumes when the key is back.
		fp.down.Store(false)
		expireLease(t, d, dest)
		r.drain()
		if got, _ := recv.ids("/g"); len(got) != 2 {
			t.Errorf("%d events delivered after the key came back, want 2", len(got))
		}
		if row := readDest(t, d, dest); row.Cursor != 2 || row.Failures != 0 {
			t.Errorf("after the key came back: %+v", row)
		}
	})

	t.Run("the gauges follow the state", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		tid := d.SeedTenant(t, "relay-gauge")
		recv := newSink(t, func(delivery, int) int { return 500 })
		d.SeedEventDestinationNamed(t, tid, "a", recv.url("/a"))
		d.SeedEventDestinationNamed(t, tid, "b", recv.url("/b"))
		off, _ := d.SeedEventDestinationNamed(t, tid, "c", recv.url("/c"))
		setSQL(t, d, `UPDATE pandokos.event_destination SET enabled = false WHERE id = $1::uuid`, off)
		d.SeedUsageRecords(t, tid, 2)
		d.AgeOutbox(t, tid, 90*time.Second)
		r := newRig(t, d)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { r.relay.Run(ctx); close(done) }()
		deadline := time.Now().Add(10 * time.Second)
		var st RelayStats
		for time.Now().Before(deadline) {
			st = r.relay.Stats()
			if st.Destinations == 2 && st.Failing == 2 && st.Disabled == 1 && st.LagSeconds >= 89 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		cancel()
		<-done
		if st.Destinations != 2 || st.Failing != 2 || st.Disabled != 1 || st.LagSeconds < 89 || st.LagSeconds > 300 {
			t.Errorf("the gauges: %+v, want 2 enabled, 2 failing, 1 disabled and a lag of about 90 s", st)
		}
		if st.Deliveries[OutcomeStatus] < 2 || st.DeliveriesFailed < 2 {
			t.Errorf("the failed deliveries are not counted: %+v", st)
		}
	})
}

// TestRelayClosedTenant is T-1037: a destination deleted, disabled, or its tenant closed between the
// claim and the settle: nothing is written, and no line names anything of the tenant.
func TestRelayClosedTenant(t *testing.T) {
	newWorld := func(t *testing.T, name string, mutate ...func(*Relay)) (*dbtest.Database, tenant.ID, string, *sink, *rig, string) {
		t.Helper()
		d := dbtest.NewMigrated(t)
		tid := d.SeedTenant(t, name)
		recv := newSink(t, nil)
		url := recv.url("/z")
		dest, secretText := d.SeedEventDestination(t, tid, url)
		d.SeedUsageRecords(t, tid, 3)
		return d, tid, dest, recv, newRig(t, d, mutate...), secretText
	}
	noLine := func(t *testing.T, r *rig, tid tenant.ID, dest, secretText string) {
		t.Helper()
		out := r.logs.String()
		for _, forbidden := range []string{tid.String(), dest, "localhost", "/z", secretText, strings.TrimPrefix(secretText, "whsec_")} {
			if strings.Contains(out, forbidden) {
				t.Errorf("a log line names %q: %s", forbidden, out)
			}
		}
		for _, l := range r.logs.Lines(t) {
			if l["level"] == "WARN" || l["level"] == "ERROR" {
				t.Errorf("a warning or an error: %v", l)
			}
		}
	}

	t.Run("deleted after the claim", func(t *testing.T) {
		var d *dbtest.Database
		var dest string
		var tid tenant.ID
		var secretText string
		hook := func(rl *Relay) {
			rl.beforeRound = func(destination string) {
				setSQL(t, d, `DELETE FROM pandokos.event_destination WHERE id = $1::uuid`, destination)
			}
		}
		var recv *sink
		var r *rig
		d, tid, dest, recv, r, secretText = newWorld(t, "closed-del", hook)
		if _, err := r.pass(); err != nil {
			t.Fatal(err)
		}
		if _, n := recv.ids("/z"); n != 0 {
			t.Errorf("%d requests for a destination that was deleted", n)
		}
		if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.outbox_dead_letter WHERE tenant_id = $1::uuid`, tid.String()); got != 0 {
			t.Errorf("%d dead letters", got)
		}
		noLine(t, r, tid, dest, secretText)
	})
	t.Run("deleted between the request and the settle", func(t *testing.T) {
		var d *dbtest.Database
		hook := func(rl *Relay) {
			rl.beforeSettle = func(destination string) {
				setSQL(t, d, `DELETE FROM pandokos.event_destination WHERE id = $1::uuid`, destination)
			}
		}
		var tid tenant.ID
		var dest, secretText string
		var r *rig
		d, tid, dest, _, r, secretText = newWorld(t, "closed-del2", hook)
		if _, err := r.pass(); err != nil {
			t.Fatalf("the pass: %v, want a settle that finds nothing to settle and no error", err)
		}
		if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.outbox_dead_letter WHERE tenant_id = $1::uuid`, tid.String()); got != 0 {
			t.Errorf("%d dead letters", got)
		}
		noLine(t, r, tid, dest, secretText)
	})
	t.Run("disabled after the claim", func(t *testing.T) {
		var d *dbtest.Database
		hook := func(rl *Relay) {
			rl.beforeRound = func(destination string) {
				setSQL(t, d, `UPDATE pandokos.event_destination SET enabled = false WHERE id = $1::uuid`, destination)
			}
		}
		var tid tenant.ID
		var dest, secretText string
		var recv *sink
		var r *rig
		d, tid, dest, recv, r, secretText = newWorld(t, "closed-off", hook)
		if _, err := r.pass(); err != nil {
			t.Fatal(err)
		}
		if _, n := recv.ids("/z"); n != 0 {
			t.Errorf("%d requests for a destination that was disabled", n)
		}
		row := readDest(t, d, dest)
		if row.Cursor != 0 || row.Failures != 0 || row.LastSuccess != nil || row.LastFailure != nil || row.Leased {
			t.Errorf("a disabled destination was written to: %+v, want its state as it was and no lease", row)
		}
		noLine(t, r, tid, dest, secretText)
	})
	t.Run("its tenant closed between the request and the settle", func(t *testing.T) {
		var d *dbtest.Database
		var tid tenant.ID
		var dest, secretText string
		var r *rig
		d, tid, dest, _, r, secretText = newWorld(t, "closed-tenant")
		r.relay.beforeSettle = func(string) {
			d.BeginDeletion(t, tid)
			d.StepDeletion(t, tid, 100)
		}
		if _, err := r.pass(); err != nil {
			t.Fatalf("the pass: %v", err)
		}
		if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.event_destination WHERE tenant_id = $1::uuid`, tid.String()); got != 0 {
			t.Fatalf("the deletion left %d destinations", got)
		}
		if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.outbox_dead_letter WHERE tenant_id = $1::uuid`, tid.String()); got != 0 {
			t.Errorf("%d dead letters for a closed tenant", got)
		}
		noLine(t, r, tid, dest, secretText)
	})
}

func dbNumber(t *testing.T, d *dbtest.Database, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := d.Super(t).QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
	return n
}
