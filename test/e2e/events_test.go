package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

var fastEvents = eventOpts{interval: "100ms", timeout: "1s", attempts: "3", backoff: "5s", privateHosts: "localhost"}

// expireLeases ends the lease of every destination, as the passing of its time does: a binary that
// was killed held its lease for a minute.
func expireLeases(t *testing.T, p *proc) {
	t.Helper()
	if _, err := p.db.Super(t).Exec(context.Background(), `UPDATE pandokos.event_destination SET lease_until = now() - interval '1 second' WHERE lease_token IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
}

// TestEventsExactlyOncePerCall is T-1052, an exit test of the sprint: 300 calls through the gateway
// while the receiver is scripted to refuse connections, answer 500, stall and answer after the client
// gave up, and the binary is killed (SIGKILL) three times in the middle of a delivery and restarted:
// at the end the receiver's store has exactly one entry per call, each with the request ID of a call,
// duplicates were seen and absorbed, and no dead letter remains after a replay.
func TestEventsExactlyOncePerCall(t *testing.T) {
	up := fakeprovider.New(t)
	o := opts{level: "info", up: up, events: fastEvents}
	p := startBinary(t, o)
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "events-once", "workload")
	recv := newFaultReceiver(t, "/hook")
	destID, secretText := newDestination(t, p, a.adminFile, a.id, "billing", recv.url("/hook"))
	recv.setSecret(secretText)
	before := ledgerOf(t, p, a.id) // the call that prepared the tenant: its event is before the destination

	var cur atomic.Pointer[proc]
	cur.Store(p)
	const total = 300
	var next, done atomic.Int64
	var gate sync.RWMutex
	var wg sync.WaitGroup
	stopScript := make(chan struct{})
	go func() {
		modes := []string{"refuse", "500", "stall", "late", "ok"}
		for i := 0; ; i++ {
			select {
			case <-stopScript:
				return
			default:
			}
			recv.setMode(modes[i%len(modes)])
			select {
			case <-stopScript:
				return
			case <-time.After(1200 * time.Millisecond):
			}
		}
	}()
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for next.Add(1) <= total {
				for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
					gate.RLock()
					q := cur.Load()
					status := quietCall(q.client, q.url(chatPath), a.inference, fakeprovider.ModelBasic)
					gate.RUnlock()
					if status == 200 {
						done.Add(1)
						break
					}
				}
			}
		}()
	}

	// Three kills in the middle of a delivery: the calls are held, what they recorded is flushed, the
	// receiver stalls so that the requests of the relay are in flight, and the process is killed.
	for i, at := range []int64{50, 140, 230} {
		testutil.Eventually(t, 3*time.Minute, "the calls before the kill", func() bool { return done.Load() >= at })
		gate.Lock()
		completed := int(done.Load())
		testutil.Eventually(t, 30*time.Second, "the ledger to hold what was answered", func() bool {
			return len(ledgerOf(t, cur.Load(), a.id)) >= len(before)+completed
		})
		recv.setMode("stall")
		time.Sleep(400 * time.Millisecond)
		old := cur.Load()
		_ = old.cmd.Process.Kill()
		<-old.exited
		n := restart(t, old, o)
		cur.Store(n)
		expireLeases(t, n)
		t.Logf("kill %d after %d calls: the binary was restarted", i+1, completed)
		gate.Unlock()
	}
	wg.Wait()
	close(stopScript)
	recv.setMode("ok")

	final := cur.Load()
	records := ledgerOf(t, final, a.id)
	expected := map[string]string{}
	for id, req := range records {
		if _, was := before[id]; !was {
			expected[id] = req
		}
	}
	if len(expected) != total {
		t.Fatalf("the ledger holds %d records of the calls, want %d", len(expected), total)
	}
	// The queue drains, dead letters are replayed until none is left.
	testutil.Eventually(t, 4*time.Minute, "the receiver to hold one entry per call", func() bool {
		expireLeases(t, final)
		if d := destinationDelivery(t, final, a.adminFile, a.id, destID); d.DeadLetters > 0 {
			final.mustCLI(t, a.adminFile, "event-destination", "replay", "--tenant", a.id, destID)
		}
		return recv.store.size() >= len(expected)
	})
	testutil.Eventually(t, 60*time.Second, "no dead letter to remain", func() bool {
		d := destinationDelivery(t, final, a.adminFile, a.id, destID)
		if d.DeadLetters > 0 {
			final.mustCLI(t, a.adminFile, "event-destination", "replay", "--tenant", a.id, destID)
		}
		return d.DeadLetters == 0 && d.Cursor == d.Head
	})
	got, dups := recv.store.snapshot()
	if len(got) != total {
		t.Errorf("the receiver's store holds %d entries for %d calls", len(got), total)
	}
	for id, req := range expected {
		e, ok := got[id]
		if !ok {
			t.Errorf("the call whose record is %s has no entry at the receiver", id)
			continue
		}
		if e.Subject != req || e.Type != "io.pandokos.usage.recorded.v1" || e.TenantID != a.id {
			t.Errorf("the entry of %s: subject %q, type %s, want the request ID %s", id, e.Subject, e.Type, req)
		}
	}
	for id := range got {
		if _, ok := expected[id]; !ok {
			t.Errorf("the receiver holds %s, which is no call", id)
		}
	}
	if dups == 0 {
		t.Error("no duplicate was seen: the faults did not make the relay deliver twice, so the test proves nothing about absorbing them")
	}
	t.Logf("300 calls, %d duplicates absorbed", dups)
	for name, text := range map[string]string{"the log of the first process": p.stdout.String(), "the log of the last": final.stdout.String()} {
		assertNoEventSecrets(t, name, text, map[string]string{"the tenant": a.id, "the destination": destID, "the URL": recv.url("/hook"), "the secret": secretText})
	}
	if code := final.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestEventsReceiverDown is T-1053, an exit test: with a receiver that accepts connections and never
// answers, 200 calls complete, the ledger writer's queue returns to 0, the readiness check stays
// ready, the lag grows and the destination is failing; the outbox holds the 200 events; when the
// receiver answers, all 200 arrive.
func TestEventsReceiverDown(t *testing.T) {
	up := fakeprovider.New(t)
	e := fastEvents
	e.attempts = "100"
	p := startBinary(t, opts{level: "info", up: up, events: e, metricsPrometheus: true})
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "events-down", "workload")
	recv := newFaultReceiver(t, "/hook")
	recv.setMode("stall")
	destID, secretText := newDestination(t, p, a.adminFile, a.id, "billing", recv.url("/hook"))
	recv.setSecret(secretText)
	before := ledgerOf(t, p, a.id)

	var wg sync.WaitGroup
	var next, failed atomic.Int64
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for next.Add(1) <= 200 {
				if quietCall(p.client, p.url(chatPath), a.inference, fakeprovider.ModelBasic) != 200 {
					failed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Errorf("%d of the 200 calls failed while the receiver was down: inference never waits for a destination", failed.Load())
	}
	testutil.Eventually(t, 60*time.Second, "the ledger to hold the 200 calls", func() bool { return len(ledgerOf(t, p, a.id)) >= len(before)+200 })

	// The queue of the ledger writer is empty.
	key := readKeyFile(t, boot)
	testutil.Eventually(t, 30*time.Second, "the ledger writer's queue to return to 0", func() bool {
		status, scrape := p.scrapeAs(t, "GET", key)
		if status != 200 {
			return false
		}
		sum := 0.0
		for _, line := range strings.Split(bodyOf(scrape), "\n") {
			if strings.HasPrefix(line, "pandokos_ledger_buffer_usage{") {
				v, _ := strconv.ParseFloat(strings.Fields(line)[len(strings.Fields(line))-1], 64)
				sum += v
			}
		}
		return sum == 0
	})
	if status, _ := exchangeWith(t, p.client, "GET", p.url("/readyz"), nil, nil); status != 200 {
		t.Errorf("the readiness check answered %d: a destination that fails must not make the process unready", status)
	}
	// The destination is failing, its events are in the log, and the lag grows.
	testutil.Eventually(t, 60*time.Second, "the destination to be failing", func() bool {
		d := destinationDelivery(t, p, a.adminFile, a.id, destID)
		return d.State == "failing" && d.Failures > 0
	})
	expected := 200
	if n := p.db.Count(t, `SELECT count(*)::int FROM pandokos.outbox_event WHERE tenant_id = $1::uuid`, a.id); n != len(before)+expected {
		t.Errorf("the outbox holds %d events, want %d", n, len(before)+expected)
	}
	testutil.Eventually(t, 120*time.Second, "the lag of the relay to show in the scrape", func() bool {
		_, scrape := p.scrapeAs(t, "GET", key)
		for _, line := range strings.Split(bodyOf(scrape), "\n") {
			if strings.HasPrefix(line, "pandokos_events_lag_seconds") {
				v, _ := strconv.ParseFloat(strings.Fields(line)[len(strings.Fields(line))-1], 64)
				return v > 0
			}
		}
		return false
	})
	if recv.store.size() != 0 {
		t.Errorf("the receiver stored %d events while it never answered", recv.store.size())
	}

	// The receiver answers again: all 200 arrive.
	recv.setMode("ok")
	testutil.Eventually(t, 3*time.Minute, "all 200 events to arrive", func() bool {
		expireLeases(t, p)
		return recv.store.size() >= expected
	})
	got, _ := recv.store.snapshot()
	records := ledgerOf(t, p, a.id)
	for id, req := range records {
		if _, was := before[id]; was {
			continue
		}
		if e, ok := got[id]; !ok || e.Subject != req {
			t.Errorf("the call whose record is %s: entry %+v", id, e)
		}
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestEventsPull is T-1054: a consumer that pulls by sequence across two restarts of the binary reads
// each event once, and `pandokos event export` gives the same lines.
func TestEventsPull(t *testing.T) {
	up := fakeprovider.New(t)
	o := opts{level: "info", up: up}
	p := startBinary(t, o)
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "events-pull", "workload")

	var lines []string
	var after int64
	seen := map[string]int{}
	pull := func(p *proc) {
		t.Helper()
		for {
			out := filepath.Join(t.TempDir(), "page.ndjson")
			p.mustCLI(t, a.adminFile, "event", "export", "--tenant", a.id, "--after", strconv.FormatInt(after, 10), "--limit", "10", "--out", out)
			b, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			page := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(page) == 1 && page[0] == "" {
				return
			}
			for _, l := range page {
				var doc struct{ ID, Sequence string }
				if err := json.Unmarshal([]byte(l), &doc); err != nil {
					t.Fatalf("a line of the export: %v\n%s", err, l)
				}
				seq, _ := strconv.ParseInt(doc.Sequence, 10, 64)
				if seq <= after {
					t.Fatalf("sequence %d after %d", seq, after)
				}
				after = seq
				seen[doc.ID]++
				lines = append(lines, l)
			}
		}
	}
	calls := func(p *proc, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if status, text := p.chatCall(t, a.inference, fakeprovider.ModelBasic, false); status != 200 {
				t.Fatalf("a call: %d\n%s", status, text)
			}
		}
		testutil.Eventually(t, 30*time.Second, "the records to be written", func() bool { return len(ledgerOf(t, p, a.id)) >= len(seen)+n })
	}
	calls(p, 12)
	pull(p)
	_ = p.stop(t)
	p2 := restart(t, p, o)
	calls(p2, 13)
	pull(p2)
	_ = p2.stop(t)
	p3 := restart(t, p2, o)
	calls(p3, 9)
	pull(p3)

	total := len(ledgerOf(t, p3, a.id))
	if len(seen) != total {
		t.Errorf("the consumer read %d events for %d records", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("event %s was read %d times", id, n)
		}
	}
	all := filepath.Join(t.TempDir(), "all.ndjson")
	p3.mustCLI(t, a.adminFile, "event", "export", "--tenant", a.id, "--limit", "10000", "--out", all)
	b, err := os.ReadFile(all)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSpace(string(b)), "\n"); !reflect.DeepEqual(got, lines) {
		t.Errorf("the export of the command line has %d lines, the consumer's pages %d: they must be the same lines", len(got), len(lines))
	}
	if code := p3.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestEventsAndDeletion is T-1055: pending events are delivered during the grace period; after the
// deletion no destination, event or secret of the tenant remains; `platform-event export` shows
// requested and deleted with the external reference; the sealed secret cannot be opened any more.
func TestEventsAndDeletion(t *testing.T) {
	up := fakeprovider.New(t)
	d := dbtest.NewMigrated(t)
	e := fastEvents
	p := startBinary(t, opts{level: "info", up: up, db: d, events: e, gracePeriod: "25s", deletionInterval: "10s", metricsPrometheus: true})
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "events-doomed", "workload")
	b := newLifeTenant(t, p, boot, "events-bystander", "workload-b")
	p.mustCLI(t, boot, "tenant", "update", "--external-reference", "cust-9", a.id)
	recv := newFaultReceiver(t, "/hook")
	recv.setMode("refuse")
	destID, secretText := newDestination(t, p, a.adminFile, a.id, "billing", recv.url("/hook"))
	recv.setSecret(secretText)
	beforeRecords := ledgerOf(t, p, a.id)
	for i := 0; i < 5; i++ {
		if status, text := p.chatCall(t, a.inference, fakeprovider.ModelBasic, false); status != 200 {
			t.Fatalf("a call: %d\n%s", status, text)
		}
	}
	testutil.Eventually(t, 30*time.Second, "the five events", func() bool {
		return p.db.Count(t, `SELECT count(*)::int FROM pandokos.outbox_event WHERE tenant_id = $1::uuid`, a.id) >= len(beforeRecords)+5
	})
	var sealed []byte
	var version int32
	if err := d.Super(t).QueryRow(context.Background(), `SELECT secret, secret_key_version FROM pandokos.event_destination WHERE id = $1::uuid`, destID).Scan(&sealed, &version); err != nil {
		t.Fatal(err)
	}

	// The request; the receiver comes back during the grace period: the pending events are delivered.
	p.mustCLI(t, boot, "tenant", "delete", a.id, "--reason", "end of contract", "--confirm-name", a.name)
	recv.setMode("ok")
	testutil.Eventually(t, 20*time.Second, "the pending events to be delivered during the grace period", func() bool {
		expireLeases(t, p)
		return recv.store.size() >= 5
	})
	if got := d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid AND state = 'completed'`, a.id); got != 0 {
		t.Fatal("the deletion completed before the grace period ended: the test cannot show what it means to")
	}
	waitDeleted(t, d, a.tenantID(t))

	// Nothing of the tenant's events remains.
	for _, tbl := range []string{"event_destination", "outbox_dead_letter", "event_destination_directory", "outbox_event", "outbox_stream"} {
		if n := d.Count(t, `SELECT count(*)::int FROM pandokos.`+tbl+` WHERE tenant_id = $1::uuid`, a.id); n != 0 {
			t.Errorf("%s still holds %d rows of the deleted tenant", tbl, n)
		}
	}
	// The sealed secret does not open: the data key that sealed it is gone with the tenant.
	worker := d.Open(t, store.RoleWorker)
	err := worker.WithTenant(context.Background(), a.tenantID(t), func(c context.Context, tx store.Tx) error {
		_, err := vault.OpenNamed(c, tx, vaulttest.ProviderFor(t, d), a.tenantID(t), vault.KindWebhookSecret, destID, version, sealed)
		return err
	})
	if err == nil {
		t.Error("the signing secret of a deleted tenant opens: the destination must not be able to sign after the deletion")
	}

	// The platform sees the deletion with the reference of the request.
	out := filepath.Join(t.TempDir(), "platform.ndjson")
	p.mustCLI(t, boot, "platform-event", "export", "--out", out)
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var requested, deleted bool
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var doc map[string]any
		if err := json.Unmarshal([]byte(l), &doc); err != nil {
			t.Fatal(err)
		}
		if doc["tenantid"] == a.id {
			switch doc["type"] {
			case "io.pandokos.tenant.deletion_requested.v1":
				requested = doc["tenantref"] == "cust-9"
			case "io.pandokos.tenant.deleted.v1":
				deleted = doc["tenantref"] == "cust-9"
			}
		}
	}
	if !requested || !deleted {
		t.Errorf("the platform's export: requested with the reference %v, deleted with the reference %v", requested, deleted)
	}
	// The other tenant is as it was.
	if !b.ok(t, p) {
		t.Error("the other tenant's calls fail after the deletion")
	}
	for name, text := range map[string]string{"the log": p.stdout.String()} {
		assertNoEventSecrets(t, name, text, map[string]string{"the tenant": a.id, "the destination": destID, "the URL": recv.url("/hook"), "the secret": secretText})
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestEventsTelemetryCanary is T-1056: the output of a process that delivers, fails, parks a dead
// letter, expires nothing and deletes a tenant, and a scrape of its metrics, hold no tenant ID, no
// destination ID, no URL, no host and no secret; every line is in the log reference (the check runs
// when the process ends, for every process of every test).
func TestEventsTelemetryCanary(t *testing.T) {
	up := fakeprovider.New(t)
	name := fixtures.CanaryLabel("tenant")
	p := startBinary(t, opts{level: "debug", up: up, events: fastEvents, gracePeriod: "0s", deletionInterval: "10s", metricsPrometheus: true})
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, name, "workload")
	recv := newFaultReceiver(t, "/hook")
	canaryPath := "/hook/" + fixtures.CanaryLabel("path") + "?token=" + fixtures.CanaryLabel("query")
	recv.setMode("500")
	destID, secretText := newDestination(t, p, a.adminFile, a.id, "billing", recv.url(canaryPath))
	recv.setSecret(secretText)
	for i := 0; i < 3; i++ {
		if status, text := p.chatCall(t, a.inference, fakeprovider.ModelBasic, false); status != 200 {
			t.Fatalf("a call: %d\n%s", status, text)
		}
	}
	// Failures until the first event is a dead letter; then the receiver answers and the letters are replayed.
	testutil.Eventually(t, 90*time.Second, "a dead letter", func() bool {
		due := destinationDelivery(t, p, a.adminFile, a.id, destID)
		if due.DeadLetters == 0 {
			if _, err := p.db.Super(t).Exec(context.Background(), `UPDATE pandokos.event_destination SET next_attempt_at = now() - interval '1 second'`); err != nil {
				t.Fatal(err)
			}
		}
		return due.DeadLetters > 0
	})
	recv.setMode("ok")
	p.mustCLI(t, a.adminFile, "event-destination", "replay", "--tenant", a.id, destID)
	testutil.Eventually(t, 60*time.Second, "the receiver to get an event", func() bool { expireLeases(t, p); return recv.store.size() >= 1 })
	p.mustCLI(t, a.adminFile, "event-destination", "test", "--wait", "--tenant", a.id, destID)
	key := readKeyFile(t, boot)
	testutil.Eventually(t, 120*time.Second, "the instruments of events in the scrape", func() bool {
		_, scrape := p.scrapeAs(t, "GET", key)
		return strings.Contains(scrape, "pandokos_events_deliveries_total") && strings.Contains(scrape, "pandokos_events_destinations")
	})
	_, scrape := p.scrapeAs(t, "GET", key)
	p.mustCLI(t, boot, "tenant", "delete", a.id, "--reason", "closing", "--confirm-name", a.name)
	waitDeleted(t, p.db, a.tenantID(t))
	_, scrape2 := p.scrapeAs(t, "GET", key)
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	forbidden := map[string]string{"the tenant ID": a.id, "the tenant name": a.name, "the destination ID": destID, "the URL": recv.url(canaryPath), "the host and port": strings.TrimPrefix(recv.url(""), "http://"),
		"the path": canaryPath, "the query": canaryPath[strings.Index(canaryPath, "?")+1:], "the secret": secretText, "the secret without its prefix": strings.TrimPrefix(secretText, "whsec_"), "a key ID": a.keyID}
	for what, text := range map[string]string{"the standard output": p.stdout.String(), "the standard error": p.stderr.String(), "the first scrape": scrape, "the second scrape": scrape2} {
		assertNoEventSecrets(t, what, text, forbidden)
	}
	for _, line := range []string{"event relay ready"} {
		if !strings.Contains(p.stdout.String(), line) {
			t.Errorf("the process never wrote %q", line)
		}
	}
}
