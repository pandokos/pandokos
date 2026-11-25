package isolation

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestErasureRemovesEvents is T-1021 (sprint 6.4, section 5.14): the deletion of a tenant takes its
// destinations with the access and its event log with the configuration, does not wait for a
// writer that began before it, writes the event "tenant deleted" with the reference of the request
// and counts destinations, not events, in the completion record.
func TestErasureRemovesEvents(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	victim, bystander := d.PopulatedTenant(t), d.PopulatedTenant(t)
	id := victim.String()
	outboxTables := []string{"outbox_event", "outbox_stream", "event_destination", "outbox_dead_letter", "event_destination_directory"}
	count := func(tbl string) int64 { return rowsOf(t, d, tbl, victim) }
	for _, tbl := range outboxTables {
		if count(tbl) == 0 {
			t.Fatalf("PopulatedTenant left no row in %s", tbl)
		}
	}
	bystanderBefore := d.TableDigests(t, bystander)
	bystanderLog := d.OutboxEvents(t, bystander)
	if len(bystanderLog) == 0 {
		t.Fatal("the other tenant has no event")
	}
	setExternalReference(t, d, victim, "cust-at-request")
	d.RequestDeletion(t, victim, time.Now().Add(-time.Minute))
	// The tenant's reference changes after the request: the events of the deletion use the copy.
	setExternalReference(t, d, victim, "cust-changed-later")

	// A writer that began before the deletion: a usage record, and so an event, not committed yet. The
	// tenant is suspended, not closed, so the insert is accepted; the transaction holds the row of the stream.
	writer, err := d.Connect(t, "gateway").Begin(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx(t), `SELECT pg_catalog.set_config('app.tenant_id', $1, true)`, id); err != nil {
		t.Fatal(err)
	}
	lateID := dbText(t, d, `SELECT pandokos.uuid_v7()::text`)
	if _, err := writer.Exec(ctx(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation,
			outcome, duration_ms, input_source, output_source, cost_status)
		VALUES ($1::uuid, $2::uuid, now(), md5('late-event'), 'service_account', gen_random_uuid()::text, 'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none')`, id, lateID); err != nil {
		t.Fatalf("the late record: %v", err)
	}

	worker := d.Connect(t, "worker")
	step := func() string {
		t.Helper()
		next, err := stepOnce(t, worker, victim, 100)
		if err != nil {
			t.Fatalf("a step: %v", err)
		}
		if next == "settling" {
			d.AgeDeletion(t, victim)
		}
		return next
	}
	var answers []string
	run := func(until string) {
		t.Helper()
		for i := 0; i < 50; i++ {
			next := step()
			answers = append(answers, next)
			if next == until {
				return
			}
		}
		t.Fatalf("the deletion did not answer %q: %v", until, answers)
	}

	// After the step access: no destination, dead letter or directory row; the log is still there.
	run("secrets")
	for _, tbl := range []string{"event_destination", "outbox_dead_letter", "event_destination_directory"} {
		if got := count(tbl); got != 0 {
			t.Errorf("after the step access %s holds %d rows of the tenant", tbl, got)
		}
	}
	if count("outbox_event") == 0 || count("outbox_stream") == 0 {
		t.Error("the step access removed the event log: that is the step configuration's")
	}
	run("configuration")

	// The step configuration does not wait for the writer that holds the stream row.
	type stepResult struct {
		next string
		err  error
	}
	done := make(chan stepResult, 1)
	go func() {
		var next string
		err := inSessionNoFatal(worker, tenantSession(victim), func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT pandokos.tenant_erasure_step(100)`).Scan(&next)
		})
		done <- stepResult{next, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || r.next != "usage" {
			t.Fatalf("the step configuration: %q, %v", r.next, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the step configuration waited for a transaction that holds the stream row")
	}
	answers = append(answers, "usage")
	if count("outbox_event") != 0 {
		t.Errorf("after the step configuration %d events of the tenant remain", count("outbox_event"))
	}
	if count("outbox_stream") != 1 {
		t.Errorf("after the step configuration the locked stream row is %d rows, want it skipped and still there", count("outbox_stream"))
	}
	// The writer commits: its event and its record are the tenant's residue; the second pass removes them.
	if err := writer.Commit(ctx(t)); err != nil {
		t.Fatalf("the writer commits: %v", err)
	}
	if got := count("outbox_event"); got != 1 {
		t.Errorf("the late writer's event is %d rows, want 1", got)
	}
	run("done")
	if n := dbNumber(t, d, `SELECT passes FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id); n != 2 {
		t.Errorf("%d passes, want 2: the late event is residue and makes a second pass (%v)", n, answers)
	}
	for _, tbl := range outboxTables {
		if got := count(tbl); got != 0 {
			t.Errorf("at the end %s holds %d rows of the deleted tenant", tbl, got)
		}
	}

	// The event "tenant deleted" is written with the completion, with the reference of the request.
	var deleted platformEvent
	for _, e := range platformEvents(t, d) {
		if e.Type == "io.pandokos.tenant.deleted.v1" && e.Tenant != nil && *e.Tenant == id {
			deleted = e
		}
	}
	if deleted.Seq == 0 {
		t.Fatal("no tenant.deleted event")
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(deleted.Data), &data); err != nil {
		t.Fatal(err)
	}
	seq := dbNumber(t, d, `SELECT completion_seq FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id)
	if data["external_reference"] != "cust-at-request" || data["completion_seq"] != float64(seq) || seq < 1 {
		t.Errorf("tenant.deleted says %s: want the reference copied at the request and completion_seq %d", deleted.Data, seq)
	}
	if got := dbText(t, d, `SELECT external_reference FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id); got != "cust-at-request" {
		t.Errorf("the deletion holds %q", got)
	}

	// The completion record counts the destinations and no events.
	var detail string
	if err := d.Super(t).QueryRow(ctx(t), `SELECT detail::text FROM pandokos.platform_audit_event WHERE action = 'tenant.delete_complete' AND resource_id = $1`, id).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Removed map[string]int64 `json:"removed"`
	}
	if err := json.Unmarshal([]byte(detail), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Removed["event_destination"] < 1 {
		t.Errorf("the completion record counts %d destinations, want at least 1: %s", rec.Removed["event_destination"], detail)
	}
	for _, k := range []string{"outbox_event", "outbox_stream", "outbox_dead_letter", "event_destination_directory"} {
		if _, ok := rec.Removed[k]; ok {
			t.Errorf("the completion record holds a count for %s (ADR 0054)", k)
		}
	}

	// The other tenant's log and destinations are as they were.
	if got := d.OutboxEvents(t, bystander); !reflect.DeepEqual(got, bystanderLog) {
		t.Error("the log of the other tenant changed")
	}
	after := d.TableDigests(t, bystander)
	for _, tbl := range outboxTables {
		if after[tbl] != bystanderBefore[tbl] {
			t.Errorf("the table %s changed for the other tenant", tbl)
		}
	}
}
