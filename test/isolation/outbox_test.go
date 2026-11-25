package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// The database tests of sprint 6.4 (T-1013 to T-1023, T-1025): the event log, the destinations, the
// claim of the relay, retention and the log of the platform. They test migration 0017 and pass
// with it; the ones that need `Spec` test the harness of part A.

const eventTypeUsage = "io.pandokos.usage.recorded.v1"

// jsonSame reports whether the two JSON texts are the same document (member order is not).
func jsonSame(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("not JSON (the golden): %v\n%s", err, want)
	}
	return reflect.DeepEqual(g, w)
}

// destination inserts a destination of the tenant as the superuser, with a placeholder for the
// sealed secret (these tests never open it), and returns its ID.
func destination(t *testing.T, d *dbtest.Database, id tenant.ID, name string) string {
	t.Helper()
	return dbText(t, d, `INSERT INTO pandokos.event_destination (tenant_id, name, url, secret, secret_key_version)
		VALUES ($1::uuid, $2, 'https://hooks.example.test/in', decode(repeat('ab', 40), 'hex'), 1) RETURNING id::text`, id.String(), name)
}

// outboxWorld is two tenants with events, a destination and a dead letter each.
type outboxWorld struct {
	d    *dbtest.Database
	a, b tenant.ID
	dest map[tenant.ID]string
}

func newOutboxWorld(t *testing.T) *outboxWorld {
	t.Helper()
	d := dbtest.NewMigrated(t)
	w := &outboxWorld{d: d, a: d.SeedTenant(t, "ob-a"), b: d.SeedTenant(t, "ob-b"), dest: map[tenant.ID]string{}}
	for _, id := range []tenant.ID{w.a, w.b} {
		d.SeedUsageRecords(t, id, 3)
		w.dest[id] = destination(t, d, id, "hook-"+id.String()[:8])
		if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason, status)
			VALUES ($1::uuid, $2::uuid, 1, pandokos.uuid_v7(), $3, 10, 'status', 500)`, id.String(), w.dest[id], eventTypeUsage); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// TestOutboxTables is T-1013: each runtime role, in the session of the tenant, in another
// tenant's, in none and in the platform scope, sees and writes what section 6 says on the four
// tenant tables; nothing writes an event or a stream row, whatever the settings; TRUNCATE is refused;
// the directory and the platform's stream give no privilege; the platform's log is read by the admin
// role in the platform scope only.
func TestOutboxTables(t *testing.T) {
	t.Parallel()
	w := newOutboxWorld(t)
	d, a, b := w.d, w.a, w.b
	sessA := sessions(a)[0]
	sessB := session{name: "tenant-b", setting: "app.tenant_id", value: b.String()}
	none, empty := session{name: "none"}, session{name: "empty", setting: "app.tenant_id", value: ""}
	all := []session{sessA, sessB, none, empty, platformSession}
	conns := map[string]*pgx.Conn{}
	for _, r := range runtimeRoles {
		conns[r] = d.Connect(t, r)
	}
	// An event of the platform's log, so that the reads have something to see.
	d.RequestDeletion(t, d.SeedTenant(t, "ob-plat"), time.Now().Add(time.Hour))

	count := func(role string, s session, tbl string) (int64, error) {
		rows, err := query(t, conns[role], s, `SELECT count(*) AS n FROM `+table(tbl))
		if err != nil {
			return 0, err
		}
		return rows[0]["n"].(int64), nil
	}

	t.Run("reads", func(t *testing.T) {
		for _, tbl := range []string{"outbox_event", "outbox_stream"} {
			n3 := int64(1)
			if tbl == "outbox_event" {
				n3 = 3
			}
			want := map[string]int64{"tenant-a": n3, "tenant-b": n3}
			for _, role := range []string{"admin", "worker"} {
				for _, s := range all {
					n, err := count(role, s, tbl)
					if err != nil {
						t.Errorf("%s reads %s in %s: %v", role, tbl, s.name, err)
						continue
					}
					if n != want[s.name] {
						t.Errorf("%s sees %d rows of %s in %s, want %d", role, n, tbl, s.name, want[s.name])
					}
				}
			}
			for _, s := range all {
				if _, err := count("gateway", s, tbl); sqlstate(err) != insufficientPrivilege {
					t.Errorf("the gateway reads %s in %s: %v, want SQLSTATE %s", tbl, s.name, err, insufficientPrivilege)
				}
			}
		}
		// The tables of a destination: the admin role reads every column but the two secrets, the
		// worker all of them, the gateway nothing; dead letters are read by both.
		for _, role := range []string{"admin", "worker"} {
			for _, s := range all {
				n, err := count(role, s, "event_destination")
				if sqlstate(err) != "" {
					t.Errorf("%s counts destinations in %s: %v", role, s.name, err)
				}
				if want := map[string]int64{"tenant-a": 1, "tenant-b": 1}[s.name]; err == nil && n != want {
					t.Errorf("%s sees %d destinations in %s, want %d", role, n, s.name, want)
				}
				if n, err := count(role, s, "outbox_dead_letter"); err != nil || n != map[string]int64{"tenant-a": 1, "tenant-b": 1}[s.name] {
					t.Errorf("%s sees %d dead letters in %s: %v", role, n, s.name, err)
				}
			}
		}
		for _, s := range all {
			for _, tbl := range []string{"event_destination", "outbox_dead_letter"} {
				if _, err := count("gateway", s, tbl); sqlstate(err) != insufficientPrivilege {
					t.Errorf("the gateway reads %s in %s: %v", tbl, s.name, err)
				}
			}
		}
		// Another tenant's rows never appear: the rows seen by tenant A's session are A's.
		for _, tbl := range []string{"outbox_event", "outbox_stream", "event_destination", "outbox_dead_letter"} {
			rows, err := query(t, conns["worker"], sessA, `SELECT tenant_id::text AS tenant_id FROM `+table(tbl))
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if r["tenant_id"] != a.String() {
					t.Errorf("%s: the session of A sees a row of %v", tbl, r["tenant_id"])
				}
			}
		}
	})

	t.Run("the admin role cannot read a sealed secret", func(t *testing.T) {
		for _, col := range []string{"secret", "previous_secret"} {
			if _, err := query(t, conns["admin"], sessA, `SELECT `+col+` FROM pandokos.event_destination`); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role selects %s: %v", col, err)
			}
		}
		if _, err := query(t, conns["admin"], sessA, `SELECT * FROM pandokos.event_destination`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role selects *: %v, want SQLSTATE %s (two columns are not granted)", err, insufficientPrivilege)
		}
		if _, err := query(t, conns["worker"], sessA, `SELECT secret, previous_secret FROM pandokos.event_destination`); err != nil {
			t.Errorf("the worker cannot read the secrets it signs with: %v", err)
		}
	})

	t.Run("nobody writes an event or a stream row, with or without the settings", func(t *testing.T) {
		settings := []map[string]string{nil, {"pandokos.retention": "on"}, {"pandokos.erasure": "on"}, {"pandokos.retention": "on", "pandokos.erasure": "on"}}
		for _, tbl := range []string{"outbox_event", "outbox_stream"} {
			for _, role := range runtimeRoles {
				for _, s := range all {
					for _, set := range settings {
						for what, sql := range map[string]string{
							"INSERT": `INSERT INTO pandokos.` + tbl + ` (tenant_id) SELECT id FROM pandokos.tenant WHERE false`,
							"UPDATE": `UPDATE pandokos.` + tbl + ` SET tenant_id = tenant_id`,
							"DELETE": `DELETE FROM pandokos.` + tbl,
						} {
							if err := attemptInsert(t, conns[role], s, set, sql); sqlstate(err) != insufficientPrivilege {
								t.Errorf("%s %s as %s in %s with %v: %v, want SQLSTATE %s", what, tbl, role, s.name, set, err, insufficientPrivilege)
							}
						}
					}
				}
				if err := attemptInsert(t, conns[role], sessA, nil, `TRUNCATE `+table(tbl)); sqlstate(err) != insufficientPrivilege {
					t.Errorf("TRUNCATE %s as %s: %v", tbl, role, err)
				}
			}
		}
		for _, tbl := range []string{"outbox_event", "outbox_stream", "event_destination", "outbox_dead_letter"} {
			for _, role := range runtimeRoles {
				if err := attemptInsert(t, conns[role], sessA, nil, `TRUNCATE `+table(tbl)); sqlstate(err) != insufficientPrivilege {
					t.Errorf("TRUNCATE %s as %s: %v, want SQLSTATE %s", tbl, role, err, insufficientPrivilege)
				}
			}
		}
		// The owner and the superuser cannot change or remove an event either: the trigger refuses.
		for what, sql := range map[string]string{"UPDATE": `UPDATE pandokos.outbox_event SET subject = 'x'`, "DELETE": `DELETE FROM pandokos.outbox_event`, "TRUNCATE": `TRUNCATE pandokos.outbox_event`} {
			if _, err := d.Super(t).Exec(ctx(t), sql); sqlstate(err) != restrictViolation {
				t.Errorf("%s by the superuser: %v, want SQLSTATE %s", what, err, restrictViolation)
			}
		}
	})

	t.Run("destinations and dead letters: the writes of section 6", func(t *testing.T) {
		own := w.dest[a]
		// The gateway writes nothing.
		for _, tbl := range []string{"event_destination", "outbox_dead_letter"} {
			if err := attemptInsert(t, conns["gateway"], sessA, nil, `DELETE FROM `+table(tbl)); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the gateway deletes from %s: %v", tbl, err)
			}
		}
		// The admin role: insert and delete a destination of its tenant, none of another's.
		insert := `INSERT INTO pandokos.event_destination (tenant_id, name, url, secret, secret_key_version) VALUES ($1::uuid, 'ins-' || $2::text, 'https://hooks.example.test/x', decode(repeat('cd', 40), 'hex'), 1)`
		if err := attemptInsert(t, conns["admin"], sessA, nil, insert, a.String(), "a"); err != nil {
			t.Errorf("the admin role inserts a destination: %v", err)
		}
		for _, s := range []session{sessB, none, empty, platformSession} {
			if err := attemptInsert(t, conns["admin"], s, nil, insert, a.String(), "b"); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role inserts a destination of A in %s: %v", s.name, err)
			}
		}
		if err := attemptInsert(t, conns["worker"], sessA, nil, insert, a.String(), "w"); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker inserts a destination: %v", err)
		}
		if n, err := execIn(t, conns["admin"], sessB, `DELETE FROM pandokos.event_destination WHERE id = $1::uuid`, own); err != nil || n != 0 {
			t.Errorf("the admin role of B deletes A's destination: %d rows, %v", n, err)
		}
		if err := attemptInsert(t, conns["worker"], sessA, nil, `DELETE FROM pandokos.event_destination`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker deletes a destination: %v", err)
		}
		// The worker settles a round: cursor and failure state; the admin role replays dead letters.
		if n, err := execIn(t, conns["worker"], sessA, `UPDATE pandokos.event_destination SET cursor_seq = 1, last_error = 'status', last_status = 500 WHERE id = $1::uuid`, own); err != nil || n != 1 {
			t.Errorf("the worker settles: %d rows, %v", n, err)
		}
		if n, err := execIn(t, conns["worker"], sessB, `UPDATE pandokos.event_destination SET cursor_seq = 2 WHERE id = $1::uuid`, own); err != nil || n != 0 {
			t.Errorf("the worker of B settles A's destination: %d rows, %v", n, err)
		}
		if n, err := execIn(t, conns["admin"], sessA, `UPDATE pandokos.outbox_dead_letter SET state = 'replay', replay_requested_at = now() WHERE destination_id = $1::uuid`, own); err != nil || n != 1 {
			t.Errorf("the admin role asks for a replay: %d rows, %v", n, err)
		}
		if n, err := execIn(t, conns["admin"], sessB, `UPDATE pandokos.outbox_dead_letter SET state = 'replay' WHERE destination_id = $1::uuid`, own); err != nil || n != 0 {
			t.Errorf("the admin role of B asks for a replay of A's letter: %d rows, %v", n, err)
		}
		for _, col := range []string{"attempts = 3", "reason = 'timeout'", "seq = 9", "event_id = pandokos.uuid_v7()"} {
			if err := attemptInsert(t, conns["admin"], sessA, nil, `UPDATE pandokos.outbox_dead_letter SET `+col); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role updates a dead letter's %s: %v", col, err)
			}
		}
		if err := attemptInsert(t, conns["admin"], sessA, nil, `DELETE FROM pandokos.outbox_dead_letter`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role deletes a dead letter: %v", err)
		}
		insertLetter := `INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason) VALUES ($1::uuid, $2::uuid, 2, pandokos.uuid_v7(), $3, 1, 'timeout')`
		if err := attemptInsert(t, conns["admin"], sessA, nil, insertLetter, a.String(), own, eventTypeUsage); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role inserts a dead letter: %v", err)
		}
		if err := attemptInsert(t, conns["worker"], sessA, nil, insertLetter, a.String(), own, eventTypeUsage); err != nil {
			t.Errorf("the worker parks a dead letter: %v", err)
		}
		if err := attemptInsert(t, conns["worker"], sessB, nil, insertLetter, a.String(), own, eventTypeUsage); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker of B parks a letter of A: %v", err)
		}
		if n, err := execIn(t, conns["worker"], sessB, `DELETE FROM pandokos.outbox_dead_letter`); err != nil || n != 1 {
			t.Errorf("the worker of B deletes %d letters, %v: its own only", n, err)
		}
		if n := dbNumber(t, d, `SELECT count(*) FROM pandokos.outbox_dead_letter WHERE tenant_id = $1::uuid`, a.String()); n != 1 {
			t.Errorf("tenant A has %d dead letters after B's worker deleted its own, want 1", n)
		}
	})

	t.Run("the directory and the platform's stream give no privilege", func(t *testing.T) {
		for _, tbl := range []string{"event_destination_directory", "platform_outbox_stream"} {
			for _, role := range runtimeRoles {
				for _, s := range all {
					for what, sql := range map[string]string{
						"SELECT": `SELECT count(*) FROM ` + table(tbl), "UPDATE": `UPDATE ` + table(tbl) + ` SET ` + map[string]string{"event_destination_directory": "tenant_id = tenant_id", "platform_outbox_stream": "last_seq = 0"}[tbl],
						"DELETE": `DELETE FROM ` + table(tbl),
					} {
						if err := attemptInsert(t, conns[role], s, nil, sql); sqlstate(err) != insufficientPrivilege {
							t.Errorf("%s %s as %s in %s: %v, want SQLSTATE %s", what, tbl, role, s.name, err, insufficientPrivilege)
						}
					}
				}
			}
		}
		if n := dbNumber(t, d, `SELECT count(*) FROM pandokos.event_destination_directory WHERE tenant_id = $1::uuid`, a.String()); n < 1 {
			t.Errorf("the directory has no row of tenant A (the trigger writes one per destination)")
		}
	})

	t.Run("the platform's log is read by the admin role in the platform scope only", func(t *testing.T) {
		if n, err := count("admin", platformSession, "platform_outbox_event"); err != nil || n < 1 {
			t.Errorf("the admin role in platform scope: %d, %v, want the events of the deletion request", n, err)
		}
		for _, s := range []session{sessA, none, empty} {
			if n, err := count("admin", s, "platform_outbox_event"); err != nil || n != 0 {
				t.Errorf("the admin role in %s sees %d events of the platform, %v", s.name, n, err)
			}
		}
		for _, role := range []string{"worker", "gateway"} {
			for _, s := range all {
				if _, err := count(role, s, "platform_outbox_event"); sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s reads the platform's log in %s: %v", role, s.name, err)
				}
			}
		}
		for _, role := range runtimeRoles {
			for _, sql := range []string{`INSERT INTO pandokos.platform_outbox_event (seq, type, data) VALUES (999, 'io.pandokos.kek.version_retired.v1', '{}')`,
				`UPDATE pandokos.platform_outbox_event SET subject = 'x'`, `DELETE FROM pandokos.platform_outbox_event`} {
				if err := attemptInsert(t, conns[role], platformSession, map[string]string{"pandokos.retention": "on"}, sql); sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s: %v on the platform's log, want SQLSTATE %s", role, err, insufficientPrivilege)
				}
			}
		}
	})
}

// fullRecord is a usage record with every column that the event carries set, and a provider-specific
// usage object that it does not. The golden below is written from section 5.3 of the design.
const fullRecord = `{"id":"0192f0f0-0000-7000-8000-0000000000c1","started_at":"2026-11-23T17:20:03.100000Z","request_id":"eccbc87e4b5ce2fe28308fd9f2a7baf3",
 "trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","principal_kind":"service_account","principal_id":"0192f0f0-0000-7000-8000-0000000000a1","key_id":"k1abc",
 "credential_id":"0192f0f0-0000-7000-8000-0000000000b1","provider":"openai","api_family":"openai","operation":"chat.completions","endpoint":"primary",
 "model":"gpt-4o-mini","model_id":null,"upstream_model":"gpt-4o-mini-2024-07-18","stream":true,"outcome":"upstream_error","error_code":"upstream_unavailable",
 "upstream_status":503,"ttfb_ms":120,"duration_ms":950,"request_bytes":100,"response_bytes":200,"input_source":"provider","output_source":"provider",
 "input_tokens":1000,"output_tokens":200,"cache_read_tokens":300,"cache_write_tokens":40,"reasoning_tokens":50,"other_units":{"audio_seconds":3},
 "provider_usage":{"prompt_tokens":1000,"completion_tokens":200},"estimated_input_tokens":null,"estimated_output_tokens":null,"cost_status":"known",
 "cost_reason":"","cost":"0.004200000000000000","currency":"EUR","price_version_id":"0192f0f0-0000-7000-8000-0000000000d1","price_source":"tenant",
 "cost_fallbacks":["cache_read"],"metadata":{"team":"blue"},"metadata_dropped":0,"limit_state":"limited"}`

const fullRecordData = `{"record_id":"0192f0f0-0000-7000-8000-0000000000c1","request_id":"eccbc87e4b5ce2fe28308fd9f2a7baf3","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736",
 "started_at":"2026-11-23T17:20:03.100000Z","duration_ms":950,"principal":{"kind":"service_account","id":"0192f0f0-0000-7000-8000-0000000000a1"},"key_id":"k1abc",
 "provider":"openai","api_family":"openai","operation":"chat.completions","model":"gpt-4o-mini","upstream_model":"gpt-4o-mini-2024-07-18","stream":true,
 "outcome":"upstream_error","error_code":"upstream_unavailable",
 "usage":{"input_tokens":1000,"output_tokens":200,"cache_read_tokens":300,"cache_write_tokens":40,"reasoning_tokens":50,"input_source":"provider","output_source":"provider","other_units":{"audio_seconds":3}},
 "cost":{"status":"known","amount":"0.0042","currency":"EUR","price_version_id":"0192f0f0-0000-7000-8000-0000000000d1","price_source":"tenant","fallbacks":["cache_read"]},
 "limit_state":"limited","metadata":{"team":"blue"}}`

// minimumRecord is the smallest record the gateway writes: no token figure, no cost, no key.
const minimumRecord = `{"id":"0192f0f0-0000-7000-8000-0000000000c2","started_at":"2026-11-23T17:21:00.000000Z","request_id":"00112233445566778899aabbccddeeff",
 "trace_id":"","principal_kind":"service_account","principal_id":"0192f0f0-0000-7000-8000-0000000000a1","key_id":"","provider":"openai","api_family":"openai",
 "operation":"models.list","endpoint":"","model":"","upstream_model":"","outcome":"rejected_policy","error_code":"","duration_ms":0,"input_source":"none","output_source":"none",
 "other_units":{},"provider_usage":{},"cost_status":"none","cost_reason":"","cost_fallbacks":[],"metadata":{},"metadata_dropped":0}`

const minimumRecordData = `{"record_id":"0192f0f0-0000-7000-8000-0000000000c2","request_id":"00112233445566778899aabbccddeeff","started_at":"2026-11-23T17:21:00.000000Z","duration_ms":0,
 "principal":{"kind":"service_account","id":"0192f0f0-0000-7000-8000-0000000000a1"},"provider":"openai","api_family":"openai","operation":"models.list",
 "outcome":"rejected_policy","usage":{"input_source":"none","output_source":"none"},"cost":{"status":"none"}}`

// insertRecords inserts the JSON records as the ledger writer does, as the gateway role in the
// session of the tenant; it returns the number of rows inserted.
func insertRecords(t *testing.T, d *dbtest.Database, id tenant.ID, records string) (n int64, err error) {
	t.Helper()
	err = d.Open(t, store.RoleGateway).WithTenant(ctx(t), id, func(c context.Context, tx store.Tx) error {
		n, err = queries.New(tx).InsertUsageRecords(c, records)
		return err
	})
	return n, err
}

// TestUsageRecordOutbox is T-1014: one event per record inserted, in the same transaction.
func TestUsageRecordOutbox(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "uro-a"), d.SeedTenant(t, "uro-b")

	t.Run("one event per record, in the transaction of the record", func(t *testing.T) {
		gw := d.Open(t, store.RoleGateway)
		rollback := fmt.Errorf("rolled back on purpose")
		err := gw.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			if _, err := queries.New(tx).InsertUsageRecords(c, "["+fullRecord+"]"); err != nil {
				return err
			}
			return rollback
		})
		if err == nil {
			t.Fatal("the transaction committed")
		}
		if n := dbNumber(t, d, `SELECT count(*) FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, a.String()); n != 0 {
			t.Errorf("%d usage records after a rollback", n)
		}
		if got := d.OutboxEvents(t, a); len(got) != 0 {
			t.Errorf("%d events after a rollback: a rolled back record leaves no event", len(got))
		}
		if n := dbNumber(t, d, `SELECT count(*) FROM pandokos.outbox_stream WHERE tenant_id = $1::uuid`, a.String()); n != 0 {
			t.Errorf("%d stream rows after a rollback", n)
		}
		ids := d.SeedUsageRecords(t, a, 3)
		got := d.OutboxEvents(t, a)
		if len(got) != 3 {
			t.Fatalf("%d events for 3 records", len(got))
		}
		for i, e := range got {
			if e.Seq != int64(i+1) || e.ID != ids[i] || e.Type != eventTypeUsage {
				t.Errorf("event %d: seq %d, id %s, type %s; want seq %d, the ID of the record %s", i, e.Seq, e.ID, e.Type, i+1, ids[i])
			}
		}
	})

	t.Run("a batch of 500 gives 500 consecutive sequence numbers", func(t *testing.T) {
		before := d.OutboxEvents(t, a)
		last := before[len(before)-1].Seq
		ids := d.SeedUsageRecords(t, a, 500)
		got := d.OutboxEvents(t, a)[len(before):]
		if len(got) != 500 {
			t.Fatalf("%d events for a batch of 500", len(got))
		}
		seen := map[string]bool{}
		for i, e := range got {
			if e.Seq != last+int64(i)+1 {
				t.Fatalf("event %d has seq %d, want %d", i, e.Seq, last+int64(i)+1)
			}
			if seen[e.ID] {
				t.Fatalf("the ID %s twice", e.ID)
			}
			seen[e.ID] = true
		}
		for _, id := range ids {
			if !seen[id] {
				t.Errorf("no event has the ID of the record %s", id)
			}
		}
		if got := dbNumber(t, d, `SELECT last_seq FROM pandokos.outbox_stream WHERE tenant_id = $1::uuid`, a.String()); got != last+500 {
			t.Errorf("the stream says %d, want %d", got, last+500)
		}
	})

	t.Run("a record left out by the conflict clause has no event", func(t *testing.T) {
		before := len(d.OutboxEvents(t, b))
		for i, want := range []int64{1, 0} {
			n, err := insertRecords(t, d, b, "["+minimumRecord+"]")
			if err != nil || n != want {
				t.Fatalf("insert %d: %d rows, %v, want %d", i, n, err, want)
			}
		}
		if got := len(d.OutboxEvents(t, b)); got != before+1 {
			t.Errorf("%d events after the same record twice, want %d", got, before+1)
		}
	})

	t.Run("the event of a record with every column set, and of the smallest", func(t *testing.T) {
		c := d.SeedTenant(t, "uro-c")
		// The superuser writes the time of the record, which the gateway role cannot set: the time of
		// the event is that time.
		for _, rec := range []string{fullRecord, minimumRecord} {
			if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, recorded_at, request_id, trace_id, principal_kind, principal_id, key_id,
					credential_id, provider, api_family, operation, endpoint, model, upstream_model, stream, outcome, error_code, upstream_status, ttfb_ms, duration_ms, request_bytes,
					response_bytes, input_source, output_source, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, other_units, provider_usage,
					estimated_input_tokens, estimated_output_tokens, cost_status, cost_reason, cost, currency, price_version_id, price_source, cost_fallbacks, metadata, metadata_dropped, limit_state)
				SELECT $1::uuid, r.id, r.started_at, '2026-11-23T17:20:04.426849Z'::timestamptz, r.request_id, r.trace_id, r.principal_kind, r.principal_id, r.key_id, r.credential_id, r.provider,
					r.api_family, r.operation, r.endpoint, r.model, r.upstream_model, r.stream, r.outcome, r.error_code, r.upstream_status, r.ttfb_ms, r.duration_ms, r.request_bytes,
					r.response_bytes, r.input_source, r.output_source, r.input_tokens, r.output_tokens, r.cache_read_tokens, r.cache_write_tokens, r.reasoning_tokens, r.other_units,
					r.provider_usage, r.estimated_input_tokens, r.estimated_output_tokens, r.cost_status, r.cost_reason, r.cost, r.currency, r.price_version_id, r.price_source,
					COALESCE(r.cost_fallbacks, '{}'), r.metadata, r.metadata_dropped, r.limit_state
				FROM jsonb_to_recordset($2::text::jsonb) AS r(id uuid, started_at timestamptz, request_id text, trace_id text, principal_kind text, principal_id text, key_id text,
					credential_id uuid, provider text, api_family text, operation text, endpoint text, model text, upstream_model text, stream boolean, outcome text, error_code text,
					upstream_status integer, ttfb_ms bigint, duration_ms bigint, request_bytes bigint, response_bytes bigint, input_source text, output_source text, input_tokens bigint,
					output_tokens bigint, cache_read_tokens bigint, cache_write_tokens bigint, reasoning_tokens bigint, other_units jsonb, provider_usage jsonb,
					estimated_input_tokens bigint, estimated_output_tokens bigint, cost_status text, cost_reason text, cost numeric, currency text, price_version_id uuid,
					price_source text, cost_fallbacks text[], metadata jsonb, metadata_dropped integer, limit_state text)`, c.String(), "["+rec+"]"); err != nil {
				t.Fatalf("insert the record: %v", err)
			}
		}
		got := d.OutboxEvents(t, c)
		if len(got) != 2 {
			t.Fatalf("%d events for two records", len(got))
		}
		for i, want := range []struct{ id, subject, data string }{
			{"0192f0f0-0000-7000-8000-0000000000c1", "eccbc87e4b5ce2fe28308fd9f2a7baf3", fullRecordData},
			{"0192f0f0-0000-7000-8000-0000000000c2", "00112233445566778899aabbccddeeff", minimumRecordData},
		} {
			e := got[i]
			// Events are in the order of the record's start: the full one starts first.
			if e.ID != want.id || e.Type != eventTypeUsage || e.Subject != want.subject || e.Seq != int64(i+1) {
				t.Errorf("event %d: %+v", i, e)
			}
			if !e.OccurredAt.Equal(time.Date(2026, 11, 23, 17, 20, 4, 426849000, time.UTC)) {
				t.Errorf("event %d occurred at %v, want the recorded_at of the record", i, e.OccurredAt)
			}
			if !jsonSame(t, string(e.Data), want.data) {
				t.Errorf("the data of event %d is\n%s\nwant\n%s", i, e.Data, want.data)
			}
			for _, banned := range []string{"provider_usage", "prompt_tokens", "completion_tokens", "content", "endpoint", "credential"} {
				if strings.Contains(string(e.Data), banned) {
					t.Errorf("the data of event %d holds %q: no provider usage object, no content", i, banned)
				}
			}
		}
	})

	t.Run("a statement of a superuser without a session over two tenants writes each log", func(t *testing.T) {
		x, y := d.SeedTenant(t, "uro-x"), d.SeedTenant(t, "uro-y")
		err := pgx.BeginFunc(ctx(t), d.Super(t), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation,
					outcome, duration_ms, input_source, output_source, cost_status)
				SELECT t.id, pandokos.uuid_v7(), now() - make_interval(secs => g), md5(random()::text), 'service_account', gen_random_uuid()::text, 'openai', 'openai', 'chat.completions',
					'ok', 1, 'none', 'none', 'none'
				FROM (VALUES ($1::uuid), ($2::uuid)) AS t(id), generate_series(1, 3) g`, x.String(), y.String()); err != nil {
				return err
			}
			var prev string
			if err := tx.QueryRow(ctx(t), `SELECT COALESCE(pg_catalog.current_setting('app.tenant_id', true), '')`).Scan(&prev); err != nil {
				return err
			}
			if prev != "" {
				return fmt.Errorf("app.tenant_id is %q after the statement: the trigger must put the session back", prev)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []tenant.ID{x, y} {
			got := d.OutboxEvents(t, id)
			if len(got) != 3 {
				t.Fatalf("%d events for 3 records of %s", len(got), id)
			}
			for i, e := range got {
				if e.Seq != int64(i+1) {
					t.Errorf("tenant %s: event %d has seq %d", id, i, e.Seq)
				}
			}
		}
	})

	t.Run("a statement for a closed tenant is refused whole", func(t *testing.T) {
		deleting, _ := closedTenants(t, d)
		before := dbNumber(t, d, `SELECT count(*) FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, deleting.String())
		events := len(d.OutboxEvents(t, deleting))
		gw := d.Connect(t, "gateway")
		err := inSession(t, gw, tenantSession(deleting), func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation,
					outcome, duration_ms, input_source, output_source, cost_status)
				SELECT $1::uuid, pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text, 'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none'
				FROM generate_series(1, 2)`, deleting.String())
			return err
		})
		if sqlstate(err) != restrictViolation {
			t.Errorf("a record of a closed tenant: %v, want SQLSTATE %s", err, restrictViolation)
		}
		if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, deleting.String()); got != before {
			t.Errorf("%d records, was %d: the statement is refused whole", got, before)
		}
		if got := len(d.OutboxEvents(t, deleting)); got != events {
			t.Errorf("%d events, was %d", got, events)
		}
	})
}

// TestLimitEventOutbox is T-1015: a limit event is also an event, of the type of its kind, with
// the ID of the row.
func TestLimitEventOutbox(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "leo-a")
	rule := "00000000-0000-4000-8000-0000000000a1"
	const insert = `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model, metric, currency, period_start, period_end, percent, limit_value, observed, occurred_at)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11::timestamptz, $12::timestamptz, $13, $14, $15::numeric, '2026-11-17T09:30:00.250000Z'::timestamptz)
		ON CONFLICT DO NOTHING RETURNING id::text`
	run := func(args ...any) (string, bool) {
		t.Helper()
		var id string
		err := d.Super(t).QueryRow(ctx(t), insert, args...).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false
		}
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		return id, true
	}
	count := func(id tenant.ID, t0 *testing.T) int { return len(d.OutboxEvents(t0, id)) }

	t.Run("a threshold on a count metric", func(t *testing.T) {
		id, _ := run(a.String(), "threshold_reached", rule, "tenant", "key", "k1", "openai", "gpt-4", "tokens_per_day", "", "2026-11-17T00:00:00Z", "2026-11-18T00:00:00Z", 80, 100, "81.5")
		e := d.OutboxEvents(t, a)[0]
		if e.Type != "io.pandokos.limit.threshold_reached.v1" || e.ID != id || e.Subject != rule || e.Seq != 1 || !e.OccurredAt.Equal(time.Date(2026, 11, 17, 9, 30, 0, 250000000, time.UTC)) {
			t.Errorf("the event is %+v, want the type of the kind, the ID %s, the rule as subject", e, id)
		}
		want := `{"limit_event_id":"` + id + `","rule_id":"` + rule + `","rule_level":"tenant","subject":{"kind":"key","id":"k1"},"provider":"openai","model":"gpt-4",
			"metric":"tokens_per_day","period":{"start":"2026-11-17T00:00:00.000000Z","end":"2026-11-18T00:00:00.000000Z"},"percent":80,"limit":100,"observed":"81.5"}`
		if !jsonSame(t, string(e.Data), want) {
			t.Errorf("data %s\nwant %s", e.Data, want)
		}
	})

	t.Run("an exhaustion of a budget: an amount, no percentage", func(t *testing.T) {
		id, _ := run(a.String(), "exhausted", rule, "platform", "tenant", "", "", "", "cost_per_month", "EUR", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z", 0, 250500000, "251")
		var e dbtest.OutboxEvent
		for _, x := range d.OutboxEvents(t, a) {
			if x.ID == id {
				e = x
			}
		}
		if e.Type != "io.pandokos.limit.exhausted.v1" || e.Seq != 2 {
			t.Fatalf("the event is %+v", e)
		}
		want := `{"limit_event_id":"` + id + `","rule_id":"` + rule + `","rule_level":"platform","subject":{"kind":"tenant"},"metric":"cost_per_month","currency":"EUR",
			"period":{"start":"2026-11-01T00:00:00.000000Z","end":"2026-12-01T00:00:00.000000Z"},"amount":"250.5","observed":"251"}`
		if !jsonSame(t, string(e.Data), want) {
			t.Errorf("data %s\nwant %s", e.Data, want)
		}
	})

	t.Run("a second insert that the unique key refuses writes no event", func(t *testing.T) {
		before := count(a, t)
		if _, ok := run(a.String(), "threshold_reached", rule, "tenant", "key", "k1", "openai", "gpt-4", "tokens_per_day", "", "2026-11-17T00:00:00Z", "2026-11-18T00:00:00Z", 80, 100, "90"); ok {
			t.Fatal("the same event was inserted twice")
		}
		if got := count(a, t); got != before {
			t.Errorf("%d events after a refused insert, was %d", got, before)
		}
		_, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model, metric, currency, period_start, period_end, percent, limit_value, observed)
			VALUES ($1::uuid, 'threshold_reached', $2::uuid, 'tenant', 'key', 'k1', 'openai', 'gpt-4', 'tokens_per_day', '', '2026-11-17T00:00:00Z', '2026-11-18T00:00:00Z', 80, 100, 1)`, a.String(), rule)
		if sqlstate(err) != uniqueViolation {
			t.Errorf("a plain insert of the same event: %v, want SQLSTATE %s", err, uniqueViolation)
		}
		if got := count(a, t); got != before {
			t.Errorf("%d events after a refused plain insert, was %d", got, before)
		}
	})

	t.Run("the gateway's insert writes the event in its transaction", func(t *testing.T) {
		before := count(a, t)
		gw := d.Connect(t, "gateway")
		n, err := execIn(t, gw, tenantSession(a), `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model, metric, currency, period_start, period_end, percent, limit_value, observed)
			VALUES ($1::uuid, 'threshold_reached', $2::uuid, 'tenant', 'key', 'k2', '', '', 'requests_per_day', '', '2026-11-17T00:00:00Z', '2026-11-18T00:00:00Z', 50, 10, 5)`, a.String(), rule)
		if err != nil || n != 1 {
			t.Fatalf("the gateway: %d rows, %v", n, err)
		}
		if got := count(a, t); got != before+1 {
			t.Errorf("%d events, want %d", got, before+1)
		}
	})
}

// TestOutboxSequenceOrder is T-1016: seq grows in the order of the commits, so a reader that asks
// for "seq > cursor" misses nothing; a second writer of a tenant waits for the first to end and the
// writers of two tenants do not wait for each other.
func TestOutboxSequenceOrder(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)

	t.Run("eight writers, one reader", func(t *testing.T) {
		a := d.SeedTenant(t, "seq-a")
		const writers, perWriter = 8, 200
		gw := d.Open(t, store.RoleGateway)
		var wg sync.WaitGroup
		var mu sync.Mutex
		wrote := map[string]bool{}
		var failures []error
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < perWriter; i++ {
					id := dbtest.NewWebhookSecret(t)[6:14] // a pause of a few milliseconds, never the same
					time.Sleep(time.Duration(id[0]%7) * time.Millisecond)
					err := gw.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
						_, err := tx.Exec(c, `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation,
								outcome, duration_ms, input_source, output_source, cost_status)
							VALUES (pandokos.current_tenant_id(), pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text, 'openai', 'openai', 'chat.completions',
								'ok', 1, 'none', 'none', 'none')`)
						return err
					})
					if err != nil {
						mu.Lock()
						failures = append(failures, err)
						mu.Unlock()
					}
				}
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()

		// The reader polls "seq > cursor" and keeps what it sees.
		reader := d.Connect(t, "admin")
		var cursor int64
		var got []string
		var seqs []int64
		poll := func() {
			rows, err := query(t, reader, tenantSession(a), `SELECT seq, id::text AS id FROM pandokos.outbox_event WHERE seq > $1 ORDER BY seq`, cursor)
			if err != nil {
				t.Fatalf("the reader: %v", err)
			}
			for _, r := range rows {
				seq := r["seq"].(int64)
				if seq <= cursor {
					t.Fatalf("the reader got seq %d after %d", seq, cursor)
				}
				cursor = seq
				seqs = append(seqs, seq)
				got = append(got, r["id"].(string))
			}
		}
		for finished := false; !finished; {
			select {
			case <-done:
				finished = true
			default:
				poll()
				time.Sleep(3 * time.Millisecond)
			}
		}
		poll()
		for _, err := range failures {
			t.Errorf("a writer failed: %v", err)
		}
		// What the writers committed: the records of the ledger.
		rows, err := d.Super(t).Query(ctx(t), `SELECT id::text FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, a.String())
		if err != nil {
			t.Fatal(err)
		}
		recorded, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range recorded {
			wrote[id] = true
		}
		if len(got) != writers*perWriter-len(failures) {
			t.Fatalf("the reader got %d events, want %d", len(got), writers*perWriter-len(failures))
		}
		seen := map[string]bool{}
		for _, id := range got {
			if seen[id] {
				t.Fatalf("the reader got %s twice", id)
			}
			seen[id] = true
			if !wrote[id] {
				t.Errorf("the reader got %s, which no writer committed", id)
			}
		}
		for id := range wrote {
			if !seen[id] {
				t.Errorf("the reader never got the event of %s (committed with a smaller seq than one it had already seen)", id)
			}
		}
		for i := 1; i < len(seqs); i++ {
			if seqs[i] <= seqs[i-1] {
				t.Fatalf("seq %d after %d", seqs[i], seqs[i-1])
			}
		}
		// The log is what the reader saw: no number is skipped and none is used twice.
		if want := int64(len(wrote)); seqs[len(seqs)-1] != want {
			t.Errorf("the last seq is %d for %d events", seqs[len(seqs)-1], want)
		}
	})

	t.Run("a second writer of a tenant waits; the writers of two tenants do not", func(t *testing.T) {
		a, b := d.SeedTenant(t, "seq-wait-a"), d.SeedTenant(t, "seq-wait-b")
		insert := `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation,
				outcome, duration_ms, input_source, output_source, cost_status)
			VALUES ($1::uuid, pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text, 'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none')`
		begin := func(id tenant.ID) pgx.Tx {
			t.Helper()
			tx, err := d.Connect(t, "gateway").Begin(ctx(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
			if _, err := tx.Exec(ctx(t), `SELECT pg_catalog.set_config('app.tenant_id', $1, true)`, id.String()); err != nil {
				t.Fatal(err)
			}
			return tx
		}
		first := begin(a)
		if _, err := first.Exec(ctx(t), insert, a.String()); err != nil {
			t.Fatal(err)
		}
		// Another tenant: not blocked.
		other := begin(b)
		start := time.Now()
		if _, err := other.Exec(ctx(t), insert, b.String()); err != nil {
			t.Fatal(err)
		}
		if err := other.Commit(ctx(t)); err != nil {
			t.Fatal(err)
		}
		if waited := time.Since(start); waited > 2*time.Second {
			t.Errorf("the writer of another tenant waited %v", waited)
		}
		// The same tenant: blocked until the first ends.
		second := begin(a)
		finished := make(chan error, 1)
		go func() {
			_, err := second.Exec(ctx(t), insert, a.String())
			if err == nil {
				err = second.Commit(ctx(t))
			}
			finished <- err
		}()
		select {
		case err := <-finished:
			t.Fatalf("the second writer of the tenant did not wait for the first: %v", err)
		case <-time.After(700 * time.Millisecond):
		}
		if err := first.Commit(ctx(t)); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-finished:
			if err != nil {
				t.Fatalf("the second writer: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the second writer did not finish after the first committed")
		}
		got := d.OutboxEvents(t, a)
		if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 || !got[0].RecordedAt.Before(got[1].RecordedAt) {
			t.Errorf("the log of the tenant is %+v, want the first writer's event, then the second's", got)
		}
	})

	t.Run("a rollback leaves no trace and no number is used twice", func(t *testing.T) {
		// The sequence is increasing, not gapless (a consumer must not rely on the absence of gaps:
		// retention makes them). This implementation reserves the numbers in the transaction of the
		// events, so a rollback leaves none; what it never does is hand out a number twice.
		a := d.SeedTenant(t, "seq-rb")
		d.SeedUsageRecords(t, a, 2)
		rolled := fmt.Errorf("on purpose")
		for i := 0; i < 3; i++ {
			_ = d.Open(t, store.RoleGateway).WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
				if _, err := tx.Exec(c, `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation,
						outcome, duration_ms, input_source, output_source, cost_status)
					SELECT pandokos.current_tenant_id(), pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text, 'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none'
					FROM generate_series(1, 5)`); err != nil {
					return err
				}
				return rolled
			})
		}
		d.SeedUsageRecords(t, a, 1)
		var seqs []int64
		for _, e := range d.OutboxEvents(t, a) {
			seqs = append(seqs, e.Seq)
		}
		if !reflect.DeepEqual(seqs, []int64{1, 2, 3}) {
			t.Errorf("the sequence numbers are %v after three rollbacks, want 1 2 3", seqs)
		}
	})
}

// TestEventDestinationGuards is T-1017.
func TestEventDestinationGuards(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "edg-a")
	sessA := sessions(a)[0]
	ad, wk := d.Connect(t, "admin"), d.Connect(t, "worker")
	insertAdmin := `INSERT INTO pandokos.event_destination (tenant_id, name, url, secret, secret_key_version) VALUES (pandokos.current_tenant_id(), $1, $2, decode(repeat('ab', 40), 'hex'), 1) RETURNING id::text AS id`

	t.Run("a new destination starts at the head of the log", func(t *testing.T) {
		d.SeedUsageRecords(t, a, 4)
		rows, err := query(t, ad, sessA, insertAdmin, "first", "https://hooks.example.test/in")
		if err != nil {
			t.Fatal(err)
		}
		id := rows[0]["id"].(string)
		if got := dbNumber(t, d, `SELECT cursor_seq FROM pandokos.event_destination WHERE id = $1::uuid`, id); got != 4 {
			t.Errorf("the cursor of a new destination is %d, want the head, 4", got)
		}
		if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.event_destination_directory WHERE destination_id = $1::uuid AND tenant_id = $2::uuid`, id, a.String()); got != 1 {
			t.Errorf("the directory has %d rows for the destination, want 1", got)
		}
		if n, err := execIn(t, ad, sessA, `DELETE FROM pandokos.event_destination WHERE id = $1::uuid`, id); err != nil || n != 1 {
			t.Fatalf("delete: %d, %v", n, err)
		}
		if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.event_destination_directory WHERE destination_id = $1::uuid`, id); got != 0 {
			t.Errorf("the directory still has the deleted destination")
		}
		// The administration cannot set the state of a new row: the trigger does.
		if _, err := query(t, ad, sessA, `INSERT INTO pandokos.event_destination (tenant_id, name, url, secret, secret_key_version, cursor_seq) VALUES (pandokos.current_tenant_id(), 'x', 'https://h.example.test/', decode(repeat('ab', 40), 'hex'), 1, 0)`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role inserts a cursor: %v, want SQLSTATE %s", err, insufficientPrivilege)
		}
	})

	t.Run("a ninth destination is refused, also under concurrent inserts", func(t *testing.T) {
		b := d.SeedTenant(t, "edg-b")
		sessB := sessions(b)[0]
		for i := 0; i < 8; i++ {
			if _, err := query(t, ad, sessB, insertAdmin, fmt.Sprintf("d%d", i), "https://hooks.example.test/in"); err != nil {
				t.Fatalf("destination %d: %v", i, err)
			}
		}
		_, err := query(t, ad, sessB, insertAdmin, "d8", "https://hooks.example.test/in")
		if sqlstate(err) != checkViolation || constraintOf(err) != "event_destination_limit" {
			t.Errorf("a ninth destination: %v, want event_destination_limit", err)
		}
		c := d.SeedTenant(t, "edg-c")
		sessC := sessions(c)[0]
		var wg sync.WaitGroup
		var mu sync.Mutex
		var ok, refused int
		for i := 0; i < 12; i++ {
			conn := d.Connect(t, "admin")
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				err := inSessionNoFatal(conn, sessC, func(c context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(c, insertAdmin, fmt.Sprintf("p%d", i), "https://hooks.example.test/in")
					return err
				})
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					ok++
				} else if sqlstate(err) == checkViolation {
					refused++
				} else {
					t.Errorf("concurrent insert: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if ok != 8 || refused != 4 {
			t.Errorf("%d inserts succeeded and %d were refused, want 8 and 4", ok, refused)
		}
		if n := dbNumber(t, d, `SELECT count(*) FROM pandokos.event_destination WHERE tenant_id = $1::uuid`, c.String()); n != 8 {
			t.Errorf("%d destinations, want 8", n)
		}
	})

	t.Run("every check of the table", func(t *testing.T) {
		e := d.SeedTenant(t, "edg-e")
		id := destination(t, d, e, "base")
		bad := func(what, code string, sql string, args ...any) {
			t.Helper()
			if _, err := d.Super(t).Exec(ctx(t), sql, args...); sqlstate(err) != code {
				t.Errorf("%s: %v, want SQLSTATE %s", what, err, code)
			}
		}
		ins := func(name, url string, types []string, secret string) (string, []any) {
			if types == nil {
				types = []string{}
			}
			return `INSERT INTO pandokos.event_destination (tenant_id, name, url, event_types, secret, secret_key_version) VALUES ($1::uuid, $2, $3, $4::text[], decode($5, 'hex'), 1)`,
				[]any{e.String(), name, url, types, secret}
		}
		okSecret := strings.Repeat("ab", 40)
		for what, c := range map[string]struct {
			name, url string
			types     []string
			secret    string
			code      string
		}{
			"an upper-case name":      {"Bad", "https://h.example.test/", nil, okSecret, checkViolation},
			"a name with a dot":       {"a.b", "https://h.example.test/", nil, okSecret, checkViolation},
			"an empty name":           {"", "https://h.example.test/", nil, okSecret, checkViolation},
			"a name of 64 characters": {strings.Repeat("a", 64), "https://h.example.test/", nil, okSecret, checkViolation},
			"an ftp URL":              {"u1", "ftp://h.example.test/", nil, okSecret, checkViolation},
			"a URL with a space":      {"u2", "https://h.example.test/a b", nil, okSecret, checkViolation},
			"a URL of 2049 bytes":     {"u3", "https://h.example.test/" + strings.Repeat("a", 2049-len("https://h.example.test/")), nil, okSecret, checkViolation},
			"an unknown type":         {"u4", "https://h.example.test/", []string{"io.pandokos.tenant.deleted.v1"}, okSecret, checkViolation},
			"the test type":           {"u5", "https://h.example.test/", []string{"io.pandokos.destination.test.v1"}, okSecret, checkViolation},
			"a secret of 29 bytes":    {"u6", "https://h.example.test/", nil, strings.Repeat("ab", 29), checkViolation},
			"a secret of 513 bytes":   {"u7", "https://h.example.test/", nil, strings.Repeat("ab", 513), checkViolation},
			"a name already used":     {"base", "https://h.example.test/", nil, okSecret, uniqueViolation},
		} {
			sql, args := ins(c.name, c.url, c.types, c.secret)
			bad(what, c.code, sql, args...)
		}
		sql, args := ins("types17", "https://h.example.test/", make([]string, 17), okSecret)
		bad("17 types", checkViolation, sql, args...)
		for what, set := range map[string]string{
			"a description of 501 characters":   `description = repeat('a', 501)`,
			"a previous expiry without secret":  `previous_secret_expires_at = now()`,
			"failures without failing_since":    `failures = 1`,
			"failing_since without failures":    `failing_since = now()`,
			"a lease token without a lease end": `lease_token = pandokos.uuid_v7()`,
			"a negative cursor":                 `cursor_seq = -1`,
			"a negative expired count":          `expired_events = -1`,
			"a last error with a space":         `last_error = 'a b'`,
			"a last error in capitals":          `last_error = 'TIMEOUT'`,
			"a last status of 99":               `last_status = 99`,
			"a last status of 600":              `last_status = 600`,
			"a test request without an ID":      `test_requested_at = now()`,
			"a test ID without a request":       `test_event_id = pandokos.uuid_v7()`,
			"a test outcome with a space":       `test_outcome = 'a b'`,
			"a test status of 99":               `test_status = 99`,
		} {
			bad(what, checkViolation, `UPDATE pandokos.event_destination SET `+set+` WHERE id = $1::uuid`, id)
		}
	})

	t.Run("the secrets: the admin role cannot read them, and a rotation keeps the replaced one only with an expiry", func(t *testing.T) {
		x := d.SeedTenant(t, "edg-x")
		sessX := sessions(x)[0]
		id := destination(t, d, x, "rot")
		for what, sql := range map[string]string{
			"select the secret":          `SELECT secret FROM pandokos.event_destination`,
			"select the previous secret": `SELECT previous_secret FROM pandokos.event_destination`,
			"assign the previous one":    `UPDATE pandokos.event_destination SET secret = previous_secret`,
			"assign the secret":          `UPDATE pandokos.event_destination SET previous_secret = secret`,
			"keep the secret by hand":    `UPDATE pandokos.event_destination SET secret = secret`,
			"set the previous one":       `UPDATE pandokos.event_destination SET previous_secret = decode(repeat('cd', 40), 'hex'), previous_secret_key_version = 1`,
			"filter on the secret":       `SELECT id FROM pandokos.event_destination WHERE secret IS NOT NULL`,
		} {
			if err := attemptInsert(t, ad, sessX, nil, sql); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s: %v, want SQLSTATE %s", what, err, insufficientPrivilege)
			}
		}
		old := dbText(t, d, `SELECT encode(secret, 'hex') FROM pandokos.event_destination WHERE id = $1::uuid`, id)
		// A new secret with an expiry: the replaced one becomes the previous secret.
		if n, err := execIn(t, ad, sessX, `UPDATE pandokos.event_destination SET secret = decode(repeat('cd', 40), 'hex'), secret_key_version = 1, previous_secret_expires_at = now() + interval '1 day' WHERE id = $1::uuid`, id); err != nil || n != 1 {
			t.Fatalf("rotate with an expiry: %d, %v", n, err)
		}
		if got := dbText(t, d, `SELECT encode(previous_secret, 'hex') FROM pandokos.event_destination WHERE id = $1::uuid`, id); got != old {
			t.Errorf("the previous secret is %s..., want the replaced one", got[:8])
		}
		if got := dbText(t, d, `SELECT encode(secret, 'hex') FROM pandokos.event_destination WHERE id = $1::uuid`, id); got != strings.Repeat("cd", 40) {
			t.Errorf("the secret is %s..., want the new one", got[:8])
		}
		// Another with no expiry: the previous one is dropped.
		if n, err := execIn(t, ad, sessX, `UPDATE pandokos.event_destination SET secret = decode(repeat('ef', 40), 'hex'), previous_secret_expires_at = NULL WHERE id = $1::uuid`, id); err != nil || n != 1 {
			t.Fatalf("rotate without an expiry: %d, %v", n, err)
		}
		if got := dbText(t, d, `SELECT (previous_secret IS NULL)::text || (previous_secret_key_version IS NULL)::text || (previous_secret_expires_at IS NULL)::text FROM pandokos.event_destination WHERE id = $1::uuid`, id); got != "truetruetrue" {
			t.Errorf("after a rotation without an expiry (previous null, key null, expiry null): %s", got)
		}
		// A change that is not a rotation leaves the previous secret alone.
		if _, err := execIn(t, ad, sessX, `UPDATE pandokos.event_destination SET secret = decode(repeat('01', 40), 'hex'), previous_secret_expires_at = now() + interval '2 days' WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := execIn(t, ad, sessX, `UPDATE pandokos.event_destination SET description = 'changed' WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		if got := dbText(t, d, `SELECT (previous_secret IS NOT NULL)::text FROM pandokos.event_destination WHERE id = $1::uuid`, id); got != "true" {
			t.Errorf("an update that does not replace the secret dropped the previous one")
		}
	})

	t.Run("who writes the state", func(t *testing.T) {
		y := d.SeedTenant(t, "edg-y")
		sessY := sessions(y)[0]
		id := destination(t, d, y, "state")
		for _, set := range []string{"last_success_at = now()", "last_failure_at = now()", "last_error = 'timeout'", "last_status = 500", "expired_events = 1", "test_completed_at = now()", "test_outcome = 'delivered'", "test_status = 200"} {
			if err := attemptInsert(t, ad, sessY, nil, `UPDATE pandokos.event_destination SET `+set+` WHERE id = $1::uuid`, id); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role writes %s: %v, want SQLSTATE %s", set, err, insufficientPrivilege)
			}
		}
		for _, set := range []string{"url = 'https://elsewhere.example.test/'", "event_types = '{}'", "enabled = false", "secret = decode(repeat('ab', 40), 'hex')", "name = 'other'", "description = 'x'",
			"previous_secret_expires_at = now()", "updated_at = now()", "secret_created_at = now()"} {
			if err := attemptInsert(t, wk, sessY, nil, `UPDATE pandokos.event_destination SET `+set+` WHERE id = $1::uuid`, id); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the worker writes %s: %v, want SQLSTATE %s", set, err, insufficientPrivilege)
			}
		}
		if n, err := execIn(t, wk, sessY, `UPDATE pandokos.event_destination SET cursor_seq = 0, failures = 1, failing_since = now(), next_attempt_at = now(), last_failure_at = now(), last_error = 'timeout', last_status = 500,
			lease_token = pandokos.uuid_v7(), lease_until = now(), test_completed_at = now(), test_outcome = 'delivered', test_status = 200 WHERE id = $1::uuid`, id); err != nil || n != 1 {
			t.Errorf("the worker settles a round: %d, %v", n, err)
		}
		if n, err := execIn(t, ad, sessY, `UPDATE pandokos.event_destination SET cursor_seq = 0, failures = 0, failing_since = NULL, next_attempt_at = now(), lease_token = NULL, lease_until = NULL,
			test_requested_at = now(), test_event_id = pandokos.uuid_v7() WHERE id = $1::uuid`, id); err != nil || n != 1 {
			t.Errorf("the admin role rewinds and asks for a test: %d, %v", n, err)
		}
	})
}

// inSessionNoFatal is inSession for a goroutine: it returns every error instead of failing the test.
func inSessionNoFatal(conn *pgx.Conn, s session, fn func(ctx context.Context, tx pgx.Tx) error) error {
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tx, err := conn.Begin(c)
	if err != nil {
		return err
	}
	if s.setting != "" {
		if _, err := tx.Exec(c, "SELECT pg_catalog.set_config($1, $2, true)", s.setting, s.value); err != nil {
			_ = tx.Rollback(c)
			return err
		}
	}
	if err := fn(c, tx); err != nil {
		_ = tx.Rollback(c)
		return err
	}
	return tx.Commit(c)
}

// claimRows runs outbox_relay_claim as the role in the session and returns "tenant/destination"
// pairs and the error.
func claimRows(t *testing.T, conn *pgx.Conn, s session, token string, lease, limit any) ([][2]string, []string, error) {
	t.Helper()
	var out [][2]string
	var cols []string
	err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(c, `SELECT * FROM pandokos.outbox_relay_claim($1::uuid, $2::integer, $3::integer)`, token, lease, limit)
		if err != nil {
			return err
		}
		for _, f := range rows.FieldDescriptions() {
			cols = append(cols, f.Name)
		}
		for rows.Next() {
			var tn, dst string
			if err := rows.Scan(&tn, &dst); err != nil {
				return err
			}
			out = append(out, [2]string{tn, dst})
		}
		return rows.Err()
	})
	return out, cols, err
}

func destinationsOf(pairs [][2]string) []string {
	var out []string
	for _, p := range pairs {
		out = append(out, p[1])
	}
	sort.Strings(out)
	return out
}

// TestOutboxRelayClaim is T-1018.
func TestOutboxRelayClaim(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	wk := d.Connect(t, "worker")
	const token = "0192f0f0-0000-7000-8000-0000000000e1"
	set := func(id, sql string) {
		t.Helper()
		if _, err := d.Super(t).Exec(ctx(t), `UPDATE pandokos.event_destination SET `+sql+` WHERE id = $1::uuid`, id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	t.Run("who may call it, and with what", func(t *testing.T) {
		x := d.SeedTenant(t, "claim-x")
		for _, s := range []session{tenantSession(x), {name: "none"}, {"empty", "app.tenant_id", ""}} {
			if _, _, err := claimRows(t, wk, s, token, 60, 10); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the worker in %s: %v, want SQLSTATE %s", s.name, err, insufficientPrivilege)
			}
		}
		for _, role := range []string{"admin", "gateway"} {
			if _, _, err := claimRows(t, d.Connect(t, role), platformSession, token, 60, 10); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the %s role in platform scope: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
			}
		}
		for what, c := range map[string]struct{ lease, limit any }{
			"a lease of 9 seconds": {9, 10}, "a lease of 601 seconds": {601, 10}, "a limit of 0": {60, 0}, "a limit of 1001": {60, 1001}, "no lease": {nil, 10}, "no limit": {60, nil},
		} {
			if _, _, err := claimRows(t, wk, platformSession, token, c.lease, c.limit); sqlstate(err) != checkViolation {
				t.Errorf("%s: %v, want SQLSTATE %s", what, err, checkViolation)
			}
		}
		if _, _, err := claimRows(t, wk, platformSession, "", 60, 10); err == nil {
			t.Error("an empty token was accepted")
		}
		if _, cols, err := claimRows(t, wk, platformSession, token, 60, 10); err != nil || !reflect.DeepEqual(cols, []string{"tenant_id", "destination_id"}) {
			t.Errorf("the answer has the columns %v (%v), want the two identifiers", cols, err)
		}
	})

	t.Run("only what has work, is enabled, due and not leased", func(t *testing.T) {
		// A tenant has at most 8 destinations: the cases are in two tenants.
		x, y := d.SeedTenant(t, "claim-work"), d.SeedTenant(t, "claim-more")
		dest := map[string]string{}
		for _, name := range []string{"work", "idle", "disabled", "leased", "replay", "test"} {
			dest[name] = destination(t, d, x, name)
		}
		for _, name := range []string{"later", "tested", "expired", "failing"} {
			dest[name] = destination(t, d, y, name)
		}
		d.SeedUsageRecords(t, x, 3)
		d.SeedUsageRecords(t, y, 3)
		set(dest["idle"], `cursor_seq = 3`)
		set(dest["disabled"], `enabled = false`)
		set(dest["leased"], `lease_token = pandokos.uuid_v7(), lease_until = now() + interval '1 minute'`)
		set(dest["later"], `next_attempt_at = now() + interval '1 minute'`)
		set(dest["replay"], `cursor_seq = 3`)
		if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, state, attempts, reason)
			VALUES ($1::uuid, $2::uuid, 2, pandokos.uuid_v7(), $3, 'replay', 10, 'status')`, x.String(), dest["replay"], eventTypeUsage); err != nil {
			t.Fatal(err)
		}
		set(dest["test"], `enabled = false, cursor_seq = 3, test_requested_at = now(), test_event_id = pandokos.uuid_v7()`)
		set(dest["tested"], `enabled = false, cursor_seq = 3, test_requested_at = now() - interval '1 minute', test_event_id = pandokos.uuid_v7(), test_completed_at = now()`)
		set(dest["expired"], `lease_token = pandokos.uuid_v7(), lease_until = now() - interval '1 second'`)
		set(dest["failing"], `failures = 2, failing_since = now() - interval '1 minute', next_attempt_at = now() - interval '1 second'`)
		// A dead letter that is only dead is no work.
		if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason)
			VALUES ($1::uuid, $2::uuid, 2, pandokos.uuid_v7(), $3, 10, 'status')`, x.String(), dest["idle"], eventTypeUsage); err != nil {
			t.Fatal(err)
		}

		got, _, err := claimRows(t, wk, platformSession, token, 60, 100)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, name := range []string{"work", "replay", "test", "expired", "failing"} {
			want = append(want, dest[name])
		}
		sort.Strings(want)
		if !reflect.DeepEqual(destinationsOf(got), want) {
			names := map[string]string{}
			for n, id := range dest {
				names[id] = n
			}
			var gotNames []string
			for _, id := range destinationsOf(got) {
				gotNames = append(gotNames, names[id])
			}
			t.Fatalf("the claim returned %v, want work, replay, test, expired and failing", gotNames)
		}
		for _, p := range got {
			if p[0] != x.String() && p[0] != y.String() {
				t.Errorf("the claim returned the tenant %s", p[0])
			}
		}
		// Each claimed destination holds the caller's lease for the time asked; the others are as they were.
		for name, id := range dest {
			leased := dbText(t, d, `SELECT COALESCE((lease_token::text = $2 AND lease_until > now() + interval '50 seconds' AND lease_until <= now() + interval '61 seconds')::text, 'null') FROM pandokos.event_destination WHERE id = $1::uuid`, id, token)
			claimed := name == "work" || name == "replay" || name == "test" || name == "expired" || name == "failing"
			if claimed != (leased == "true") {
				t.Errorf("%s: leased to the caller = %s, claimed = %v", name, leased, claimed)
			}
		}
		// A second claim finds nothing: everything that could be claimed is leased now.
		if again, _, err := claimRows(t, wk, platformSession, "0192f0f0-0000-7000-8000-0000000000e2", 60, 100); err != nil || len(again) != 0 {
			t.Errorf("a second claim returned %v, %v", again, err)
		}
		// A lease that ends is claimed again.
		set(dest["work"], `lease_until = now() - interval '1 second'`)
		if again, _, err := claimRows(t, wk, platformSession, "0192f0f0-0000-7000-8000-0000000000e3", 60, 100); err != nil || !reflect.DeepEqual(destinationsOf(again), []string{dest["work"]}) {
			t.Errorf("after the lease ended the claim returned %v, %v", again, err)
		}
	})

	t.Run("two concurrent claims are disjoint, the limit holds across tenants", func(t *testing.T) {
		var tenants []tenant.ID
		for i := 0; i < 3; i++ {
			x := d.SeedTenant(t, fmt.Sprintf("claim-c%d", i))
			for j := 0; j < 4; j++ {
				destination(t, d, x, fmt.Sprintf("c%d", j))
			}
			d.SeedUsageRecords(t, x, 2)
			tenants = append(tenants, x)
		}
		// Twelve destinations are due. The first claim holds its rows' locks until its transaction ends.
		c1, c2 := d.Connect(t, "worker"), d.Connect(t, "worker")
		tx1, err := c1.Begin(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx1.Rollback(ctx(t)) }()
		tx2, err := c2.Begin(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx2.Rollback(ctx(t)) }()
		claim := func(tx pgx.Tx, token string, limit int) []string {
			t.Helper()
			if _, err := tx.Exec(ctx(t), `SELECT pg_catalog.set_config('app.scope', 'platform', true)`); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(ctx(t), `SELECT destination_id::text FROM pandokos.outbox_relay_claim($1::uuid, 60, $2::integer)`, token, limit)
			if err != nil {
				t.Fatal(err)
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}
		first := claim(tx1, token, 7)
		second := claim(tx2, "0192f0f0-0000-7000-8000-0000000000e4", 7)
		if len(first) != 7 || len(second) < 1 {
			t.Fatalf("the claims returned %d and %d destinations", len(first), len(second))
		}
		inFirst := map[string]bool{}
		for _, id := range first {
			inFirst[id] = true
		}
		for _, id := range second {
			if inFirst[id] {
				t.Errorf("both claims returned %s", id)
			}
		}
		if err := tx1.Commit(ctx(t)); err != nil {
			t.Fatal(err)
		}
		if err := tx2.Commit(ctx(t)); err != nil {
			t.Fatal(err)
		}
		_ = tenants
	})

	t.Run("the settings are as before, and a role that sets them first gains nothing", func(t *testing.T) {
		x := d.SeedTenant(t, "claim-set")
		destination(t, d, x, "s")
		d.SeedUsageRecords(t, x, 1)
		err := inSession(t, wk, platformSession, func(c context.Context, tx pgx.Tx) error {
			for k, v := range map[string]string{"pandokos.retention": "on", "pandokos.erasure": "on", "app.tenant_id": ""} {
				if _, err := tx.Exec(c, `SELECT pg_catalog.set_config($1, $2, true)`, k, v); err != nil {
					return err
				}
			}
			rows, err := tx.Query(c, `SELECT destination_id::text FROM pandokos.outbox_relay_claim($1::uuid, 60, 100)`, token)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil || len(ids) < 1 {
				return fmt.Errorf("the claim returned %v: %w", ids, err)
			}
			var tenantSetting, scope, retention, erasure *string
			if err := tx.QueryRow(c, `SELECT pg_catalog.current_setting('app.tenant_id', true), pg_catalog.current_setting('app.scope', true),
				pg_catalog.current_setting('pandokos.retention', true), pg_catalog.current_setting('pandokos.erasure', true)`).Scan(&tenantSetting, &scope, &retention, &erasure); err != nil {
				return err
			}
			if tenantSetting == nil || *tenantSetting != "" || scope == nil || *scope != "platform" || retention == nil || *retention != "on" || erasure == nil || *erasure != "on" {
				return fmt.Errorf("after the claim: app.tenant_id %v, app.scope %v, retention %v, erasure %v: the settings are not as before", tenantSetting, scope, retention, erasure)
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
		// The role gained nothing: it still cannot remove an event with the settings on.
		if err := attemptInsert(t, wk, platformSession, map[string]string{"pandokos.retention": "on", "pandokos.erasure": "on"}, `DELETE FROM pandokos.outbox_event`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker deleted events with the settings on: %v", err)
		}
	})
}

// TestTenantOutboxRetention is T-1019.
func TestTenantOutboxRetention(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	wk := d.Connect(t, "worker")
	retain := func(id tenant.ID, hours, months, batch any) (events, expired, limits int64, err error) {
		err = inSession(t, wk, tenantSession(id), func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT events, expired, limit_events FROM pandokos.tenant_outbox_retention($1::integer, $2::integer, $3::integer)`, hours, months, batch).Scan(&events, &expired, &limits)
		})
		return
	}

	t.Run("the oldest events first, at most a batch, the cursors moved and the loss counted", func(t *testing.T) {
		a, b := d.SeedTenant(t, "ret-a"), d.SeedTenant(t, "ret-b")
		enabled, disabled, ahead := destination(t, d, a, "enabled"), destination(t, d, a, "disabled"), destination(t, d, a, "ahead")
		d.SeedUsageRecords(t, a, 300)
		d.SeedUsageRecords(t, b, 150)
		d.AgeOutbox(t, b, 400*time.Hour)
		d.AgeOutboxUpTo(t, a, 200*time.Hour, 250)
		if _, err := d.Super(t).Exec(ctx(t), `UPDATE pandokos.event_destination SET enabled = false WHERE id = $1::uuid`, disabled); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Super(t).Exec(ctx(t), `UPDATE pandokos.event_destination SET cursor_seq = 300 WHERE id = $1::uuid`, ahead); err != nil {
			t.Fatal(err)
		}
		for _, seq := range []int{5, 120, 260} {
			if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason)
				VALUES ($1::uuid, $2::uuid, $3, pandokos.uuid_v7(), $4, 10, 'status')`, a.String(), enabled, seq, eventTypeUsage); err != nil {
				t.Fatal(err)
			}
		}
		bBefore := d.OutboxEvents(t, b)

		// The first batch removes the oldest 100 and counts them for the destination that had not passed them.
		events, expired, limits, err := retain(a, 168, 0, 100)
		if err != nil || events != 100 || expired != 100 || limits != 0 {
			t.Fatalf("the first call: %d events, %d expired, %d limit events, %v; want 100, 100, 0", events, expired, limits, err)
		}
		left := d.OutboxEvents(t, a)
		if len(left) != 200 || left[0].Seq != 101 {
			t.Fatalf("%d events remain, the oldest %d: want 200 from seq 101", len(left), left[0].Seq)
		}
		cursors := func() map[string]int64 {
			out := map[string]int64{}
			for name, id := range map[string]string{"enabled": enabled, "disabled": disabled, "ahead": ahead} {
				out[name] = dbNumber(t, d, `SELECT cursor_seq FROM pandokos.event_destination WHERE id = $1::uuid`, id)
			}
			return out
		}
		if got := cursors(); got["enabled"] != 100 || got["disabled"] != 100 || got["ahead"] != 300 {
			t.Errorf("the cursors are %v, want the two behind moved to 100 and the other left", got)
		}
		expiredOf := func(id string) int64 {
			return dbNumber(t, d, `SELECT expired_events FROM pandokos.event_destination WHERE id = $1::uuid`, id)
		}
		if expiredOf(enabled) != 100 || expiredOf(disabled) != 0 || expiredOf(ahead) != 0 {
			t.Errorf("expired events: enabled %d, disabled %d, ahead %d; want 100, 0, 0 (a disabled destination is not counted)", expiredOf(enabled), expiredOf(disabled), expiredOf(ahead))
		}
		letters := func() []int64 {
			rows, err := d.Super(t).Query(ctx(t), `SELECT seq FROM pandokos.outbox_dead_letter WHERE tenant_id = $1::uuid ORDER BY seq`, a.String())
			if err != nil {
				t.Fatal(err)
			}
			out, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		if got := letters(); !reflect.DeepEqual(got, []int64{120, 260}) {
			t.Errorf("the dead letters are at %v, want 120 and 260: the letter of a removed event goes with it", got)
		}
		// The second removes the rest of the aged ones (150 of them, fewer than a batch).
		events, expired, _, err = retain(a, 168, 0, 100)
		if err != nil || events != 100 || expired != 100 {
			t.Fatalf("the second call: %d, %d, %v", events, expired, err)
		}
		events, expired, _, err = retain(a, 168, 0, 100)
		if err != nil || events != 50 || expired != 50 {
			t.Fatalf("the third call: %d, %d, %v, want 50 and 50: fewer than a batch ends the loop", events, expired, err)
		}
		if left := d.OutboxEvents(t, a); len(left) != 50 || left[0].Seq != 251 {
			t.Errorf("%d events remain from seq %d, want the 50 that are not old", len(left), left[0].Seq)
		}
		if got := letters(); !reflect.DeepEqual(got, []int64{260}) {
			t.Errorf("the dead letters are at %v, want 260", got)
		}
		if events, _, _, err := retain(a, 168, 0, 100); err != nil || events != 0 {
			t.Errorf("a call with nothing due removed %d, %v", events, err)
		}
		// The other tenant's events are untouched: its call is its own.
		if got := d.OutboxEvents(t, b); !reflect.DeepEqual(got, bBefore) {
			t.Errorf("the events of another tenant changed")
		}
		if expiredOf(enabled) != 250 {
			t.Errorf("%d expired in all, want 250", expiredOf(enabled))
		}
		if got := cursors(); got["enabled"] != 250 || got["ahead"] != 300 {
			t.Errorf("the cursors are %v", got)
		}
	})

	t.Run("limit events by months, with an audit row", func(t *testing.T) {
		c := d.SeedTenant(t, "ret-limits")
		insert := func(ago string, percent int) {
			t.Helper()
			if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed, occurred_at)
				VALUES ($1::uuid, 'threshold_reached', pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', now() - $2::interval, now() - $2::interval + interval '1 day', $3, 10, 8, now() - $2::interval)`, c.String(), ago, percent); err != nil {
				t.Fatal(err)
			}
		}
		insert("200 days", 10)
		insert("150 days", 20)
		insert("1 day", 30)
		audits := func() int {
			n := 0
			for _, r := range d.AuditRows(t, c) {
				if r.Action == "limit_event.retention" {
					n++
				}
			}
			return n
		}
		// With the months at 0 nothing of the limit events goes.
		if _, _, limits, err := retain(c, 168, 0, 100); err != nil || limits != 0 {
			t.Fatalf("months 0: %d, %v", limits, err)
		}
		if _, _, limits, err := retain(c, 168, 2, 100); err != nil || limits != 2 {
			t.Fatalf("months 2: %d limit events removed, %v, want the two older than the cut", limits, err)
		}
		if n := dbNumber(t, d, `SELECT count(*) FROM pandokos.limit_event WHERE tenant_id = $1::uuid`, c.String()); n != 1 {
			t.Errorf("%d limit events remain, want 1", n)
		}
		if audits() != 1 {
			t.Errorf("%d limit_event.retention rows, want 1", audits())
		}
		var detail map[string]any
		for _, r := range d.AuditRows(t, c) {
			if r.Action == "limit_event.retention" {
				detail = r.Detail
				if r.ActorType != "platform" || r.Outcome != "success" || r.ResourceType != "limit_event" {
					t.Errorf("the audit row is %+v", r)
				}
			}
		}
		if detail["deleted"] != float64(2) || detail["months"] != float64(2) || detail["before"] == nil {
			t.Errorf("the audit detail is %v", detail)
		}
		if _, _, limits, err := retain(c, 168, 2, 100); err != nil || limits != 0 {
			t.Fatalf("again: %d, %v", limits, err)
		}
		if audits() != 1 {
			t.Errorf("a call that removed nothing wrote an audit row (%d rows)", audits())
		}
	})

	t.Run("arguments, scope and roles", func(t *testing.T) {
		x := d.SeedTenant(t, "ret-args")
		for what, c := range map[string]struct{ hours, months, batch any }{
			"0 hours": {0, 0, 100}, "2161 hours": {2161, 0, 100}, "1 month": {168, 1, 100}, "121 months": {168, 121, 100}, "a batch of 99": {168, 0, 99},
			"a batch of 50001": {168, 0, 50001}, "no hours": {nil, 0, 100}, "no months": {168, nil, 100}, "no batch": {168, 0, nil},
		} {
			if _, _, _, err := retain(x, c.hours, c.months, c.batch); sqlstate(err) != checkViolation {
				t.Errorf("%s: %v, want SQLSTATE %s", what, err, checkViolation)
			}
		}
		for _, ok := range []struct{ hours, months, batch int }{{1, 0, 100}, {2160, 120, 50000}, {168, 2, 100}} {
			if _, _, _, err := retain(x, ok.hours, ok.months, ok.batch); err != nil {
				t.Errorf("%+v: %v", ok, err)
			}
		}
		call := `SELECT events FROM pandokos.tenant_outbox_retention(168, 0, 100)`
		for _, s := range []session{platformSession, {name: "none"}, {"empty", "app.tenant_id", ""}} {
			if err := attemptInsert(t, wk, s, nil, call); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the worker in %s: %v, want SQLSTATE %s", s.name, err, insufficientPrivilege)
			}
		}
		for _, role := range []string{"admin", "gateway"} {
			if err := attemptInsert(t, d.Connect(t, role), tenantSession(x), nil, call); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the %s role: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
			}
		}
	})
}

// TestOutboxRelayState is T-1022: the four counts and the age over three tenants.
func TestOutboxRelayState(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	wk := d.Connect(t, "worker")
	state := func(s session, role string) (map[string]float64, error) {
		conn := wk
		if role != "worker" {
			conn = d.Connect(t, role)
		}
		rows, err := query(t, conn, s, `SELECT destinations::float8 AS destinations, failing::float8 AS failing, disabled::float8 AS disabled, dead_letters::float8 AS dead_letters,
			oldest_pending_seconds AS oldest FROM pandokos.outbox_relay_state()`)
		if err != nil {
			return nil, err
		}
		out := map[string]float64{}
		for k, v := range rows[0] {
			out[k] = v.(float64)
		}
		return out, nil
	}
	base, err := state(platformSession, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if base["destinations"] != 0 || base["failing"] != 0 || base["disabled"] != 0 || base["dead_letters"] != 0 || base["oldest"] != 0 {
		t.Fatalf("with nothing pending: %v, want zeros", base)
	}
	if got, err := state(platformSession, "worker"); err != nil || len(got) != 5 {
		t.Fatalf("the answer is %v, %v: four counts and one age", got, err)
	}

	var ids [3]tenant.ID
	for i := range ids {
		ids[i] = d.SeedTenant(t, fmt.Sprintf("state-%d", i))
	}
	one, two, three := destination(t, d, ids[0], "one"), destination(t, d, ids[0], "two"), destination(t, d, ids[1], "three")
	four := destination(t, d, ids[2], "four")
	d.SeedUsageRecords(t, ids[0], 2)
	d.SeedUsageRecords(t, ids[1], 3)
	d.AgeOutbox(t, ids[0], 90*time.Second)
	for _, st := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE pandokos.event_destination SET failures = 3, failing_since = now() WHERE id = $1::uuid`, []any{one}},
		{`UPDATE pandokos.event_destination SET enabled = false WHERE id = $1::uuid`, []any{two}},
		{`UPDATE pandokos.event_destination SET cursor_seq = 3 WHERE id = $1::uuid`, []any{three}},
		{`INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason) VALUES
		  ($1::uuid, $2::uuid, 1, pandokos.uuid_v7(), $3, 10, 'status'), ($1::uuid, $2::uuid, 2, pandokos.uuid_v7(), $3, 10, 'status'), ($4::uuid, $5::uuid, 1, pandokos.uuid_v7(), $3, 10, 'timeout')`,
			[]any{ids[0].String(), one, eventTypeUsage, ids[1].String(), three}},
		{`INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, state, attempts, reason) VALUES ($1::uuid, $2::uuid, 2, pandokos.uuid_v7(), $3, 'replay', 10, 'timeout')`,
			[]any{ids[1].String(), three, eventTypeUsage}},
	} {
		if _, err := d.Super(t).Exec(ctx(t), st.sql, st.args...); err != nil {
			t.Fatal(err)
		}
	}
	_ = four
	got, err := state(platformSession, "worker")
	if err != nil {
		t.Fatal(err)
	}
	// Enabled: one (failing), three (at the head) and four (no event); disabled: two. Dead letters in state
	// 'dead': two of one and one of three. The oldest event an enabled destination has not settled is the
	// first of tenant 0, aged 90 seconds, for one.
	if got["destinations"] != 3 || got["failing"] != 1 || got["disabled"] != 1 || got["dead_letters"] != 3 {
		t.Errorf("the counts are %v, want 3 enabled, 1 failing, 1 disabled, 3 dead letters", got)
	}
	if got["oldest"] < 89 || got["oldest"] > 120 {
		t.Errorf("the age of the oldest pending event is %v, want about 90 seconds", got["oldest"])
	}
	// Scope and roles.
	for _, s := range []session{tenantSession(ids[0]), {name: "none"}, {"empty", "app.tenant_id", ""}} {
		if _, err := state(s, "worker"); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker in %s: %v, want SQLSTATE %s", s.name, err, insufficientPrivilege)
		}
	}
	for _, role := range []string{"admin", "gateway"} {
		if _, err := state(platformSession, role); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the %s role: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
		}
	}
}

// setExternalReference sets the reference of a tenant as the platform does (the guard wants the
// platform scope).
func setExternalReference(t *testing.T, d *dbtest.Database, id tenant.ID, ref string) {
	t.Helper()
	err := pgx.BeginFunc(ctx(t), d.Super(t), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx(t), `SELECT pg_catalog.set_config('app.scope', 'platform', true)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx(t), `UPDATE pandokos.tenant SET external_reference = $2 WHERE id = $1::uuid`, id.String(), ref)
		return err
	})
	if err != nil {
		t.Fatalf("set the external reference: %v", err)
	}
}

type platformEvent struct {
	Seq             int64
	ID, Type, Subj  string
	Tenant          *string
	Data            string
	Occurred, Stamp time.Time
}

func platformEvents(t *testing.T, d *dbtest.Database) []platformEvent {
	t.Helper()
	rows, err := d.Super(t).Query(ctx(t), `SELECT seq, id::text, type, subject, tenant_id::text, data::text, occurred_at FROM pandokos.platform_outbox_event ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []platformEvent
	for rows.Next() {
		var e platformEvent
		if err := rows.Scan(&e.Seq, &e.ID, &e.Type, &e.Subj, &e.Tenant, &e.Data, &e.Occurred); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// TestPlatformOutbox is T-1020: the five types of the platform's log, each written by its trigger in
// the transaction of its cause.
func TestPlatformOutbox(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	before := len(platformEvents(t, d)) // the activation of the key of the test database

	t.Run("a deletion requested, cancelled and requested again", func(t *testing.T) {
		x := d.SeedTenant(t, "plat-x")
		setExternalReference(t, d, x, "cust-42")
		// A request that rolls back leaves no event.
		_ = pgx.BeginFunc(ctx(t), d.Super(t), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx(t), `UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid`, x.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx(t), `INSERT INTO pandokos.tenant_deletion (tenant_id, reason, requested_by_type, requested_by_id, requested_by_name, request_id, not_before)
				VALUES ($1::uuid, 'test', 'platform', 'pkey', 'test', 'req-rb', now() + interval '1 hour')`, x.String()); err != nil {
				t.Fatal(err)
			}
			return fmt.Errorf("rolled back")
		})
		if got := len(platformEvents(t, d)); got != before {
			t.Fatalf("%d events after a request that rolled back, was %d", got, before)
		}
		d.RequestDeletion(t, x, time.Now().Add(time.Hour))
		if _, err := d.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid AND state = 'scheduled'`, x.String()); err != nil {
			t.Fatal(err)
		}
		got := platformEvents(t, d)[before:]
		if len(got) != 2 {
			t.Fatalf("%d events for a request and a cancel", len(got))
		}
		req, can := got[0], got[1]
		if req.Type != "io.pandokos.tenant.deletion_requested.v1" || can.Type != "io.pandokos.tenant.deletion_cancelled.v1" || req.Seq+1 != can.Seq {
			t.Fatalf("the events are %s (%d) and %s (%d)", req.Type, req.Seq, can.Type, can.Seq)
		}
		for _, e := range got {
			if e.Subj != x.String() || e.Tenant == nil || *e.Tenant != x.String() {
				t.Errorf("%s: subject %q, tenant %v, want the tenant", e.Type, e.Subj, e.Tenant)
			}
		}
		reqData := dbText(t, d, `SELECT jsonb_build_object('tenant_id', tenant_id, 'external_reference', external_reference, 'requested_at', pandokos.rfc3339(requested_at), 'not_before', pandokos.rfc3339(not_before))::text
			FROM (SELECT $1::uuid AS tenant_id, 'cust-42' AS external_reference, now() AS requested_at, now() AS not_before) x`, x.String())
		var gotData, wantData map[string]any
		_ = json.Unmarshal([]byte(req.Data), &gotData)
		_ = json.Unmarshal([]byte(reqData), &wantData)
		for _, k := range []string{"tenant_id", "external_reference"} {
			if gotData[k] != wantData[k] {
				t.Errorf("the request's %s is %v, want %v", k, gotData[k], wantData[k])
			}
		}
		if len(gotData) != 4 || gotData["requested_at"] == nil || gotData["not_before"] == nil {
			t.Errorf("the data of a request: %s, want tenant_id, external_reference, requested_at, not_before", req.Data)
		}
		var canData map[string]any
		_ = json.Unmarshal([]byte(can.Data), &canData)
		if len(canData) != 3 || canData["tenant_id"] != x.String() || canData["external_reference"] != "cust-42" || canData["requested_at"] == nil {
			t.Errorf("the data of a cancel: %s, want tenant_id, external_reference, requested_at", can.Data)
		}
	})

	t.Run("a deletion completed, with the reference of the request", func(t *testing.T) {
		y := d.SeedTenant(t, "plat-y")
		setExternalReference(t, d, y, "cust-7")
		d.SeedUsageRecords(t, y, 2)
		n := len(platformEvents(t, d))
		deleteTenant(t, d, y, 100)
		got := platformEvents(t, d)[n:]
		if len(got) != 2 || got[0].Type != "io.pandokos.tenant.deletion_requested.v1" || got[1].Type != "io.pandokos.tenant.deleted.v1" {
			t.Fatalf("the events of a deletion: %+v", got)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(got[1].Data), &data); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"tenant_id", "external_reference", "requested_at", "started_at", "completed_at", "kek_versions", "completion_seq"} {
			if _, ok := data[k]; !ok {
				t.Errorf("the deleted event has no %s: %s", k, got[1].Data)
			}
		}
		if len(data) != 7 || data["external_reference"] != "cust-7" || data["tenant_id"] != y.String() {
			t.Errorf("the data of the deleted event: %s", got[1].Data)
		}
		if want := dbNumber(t, d, `SELECT completion_seq FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, y.String()); data["completion_seq"] != float64(want) || want < 1 {
			t.Errorf("completion_seq is %v, the completion record is %d", data["completion_seq"], want)
		}
		if keks, _ := data["kek_versions"].([]any); keks == nil {
			t.Errorf("kek_versions is %v, want a list", data["kek_versions"])
		}
	})

	t.Run("a key version activated and retired", func(t *testing.T) {
		n := len(platformEvents(t, d))
		const v2 = "local-2222222222222222"
		if got := d.ActivateKEK(t, v2); got != "activated" {
			t.Fatalf("the activation: %s", got)
		}
		err := inSession(t, d.Connect(t, "worker"), platformSession, func(c context.Context, tx pgx.Tx) error {
			var retired int
			if err := tx.QueryRow(c, `SELECT pandokos.kek_retire()`).Scan(&retired); err != nil || retired != 1 {
				return fmt.Errorf("kek_retire: %d: %w", retired, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		got := platformEvents(t, d)[n:]
		if len(got) != 2 || got[0].Type != "io.pandokos.kek.version_activated.v1" || got[1].Type != "io.pandokos.kek.version_retired.v1" {
			t.Fatalf("the events of an activation and a retirement: %+v", got)
		}
		if got[0].Subj != v2 || got[0].Tenant != nil || got[1].Tenant != nil {
			t.Errorf("subject %q, tenants %v %v: the subject is the version, and no tenant", got[0].Subj, got[0].Tenant, got[1].Tenant)
		}
		if !jsonSame(t, got[0].Data, `{"version":"`+v2+`","provider":"local"}`) {
			t.Errorf("data %s", got[0].Data)
		}
		if got[1].Subj != d.KEKVersion(t) || !jsonSame(t, got[1].Data, `{"version":"`+d.KEKVersion(t)+`","provider":"local"}`) {
			t.Errorf("the retired event: %+v", got[1])
		}
	})

	t.Run("no figure of usage, sequence in commit order", func(t *testing.T) {
		all := platformEvents(t, d)
		allowed := map[string]bool{"tenant_id": true, "external_reference": true, "requested_at": true, "not_before": true, "started_at": true, "completed_at": true,
			"kek_versions": true, "completion_seq": true, "version": true, "provider": true}
		for i, e := range all {
			if e.Seq != int64(i+1) {
				t.Errorf("event %d has seq %d: the platform's log is gapless and in order", i, e.Seq)
			}
			var data map[string]any
			_ = json.Unmarshal([]byte(e.Data), &data)
			for k := range data {
				if !allowed[k] {
					t.Errorf("%s has the member %s: no figure of usage, no name of a person", e.Type, k)
				}
			}
		}
		if got := dbNumber(t, d, `SELECT last_seq FROM pandokos.platform_outbox_stream`); got != int64(len(all)) {
			t.Errorf("the stream says %d for %d events", got, len(all))
		}
	})

	t.Run("a request and a completing erasure step run together without a deadlock", func(t *testing.T) {
		var requesters []tenant.ID
		for i := 0; i < 6; i++ {
			requesters = append(requesters, d.SeedTenant(t, fmt.Sprintf("plat-req-%d", i)))
		}
		victim := d.SeedTenant(t, "plat-erased")
		d.RequestDeletion(t, victim, time.Now().Add(-time.Minute))
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for _, r := range requesters {
			conn := d.Super(t)
			wg.Add(1)
			go func(id tenant.ID) {
				defer wg.Done()
				time.Sleep(time.Duration(len(id.String())%5) * 10 * time.Millisecond)
				c := context.Background()
				err := pgx.BeginFunc(c, conn, func(tx pgx.Tx) error {
					if _, err := tx.Exec(c, `UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid`, id.String()); err != nil {
						return err
					}
					_, err := tx.Exec(c, `INSERT INTO pandokos.tenant_deletion (tenant_id, reason, requested_by_type, requested_by_id, requested_by_name, request_id, not_before)
						VALUES ($1::uuid, 'test', 'platform', 'pkey', 'test', 'req-' || $1::text, now() + interval '1 hour')`, id.String())
					return err
				})
				if err != nil {
					errs <- fmt.Errorf("request: %w", err)
				}
			}(r)
		}
		wg.Add(1)
		worker, super := d.Connect(t, "worker"), d.Super(t)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				var next string
				err := inSessionNoFatal(worker, tenantSession(victim), func(c context.Context, tx pgx.Tx) error {
					return tx.QueryRow(c, `SELECT pandokos.tenant_erasure_step(100)`).Scan(&next)
				})
				if err != nil {
					errs <- fmt.Errorf("step: %w", err)
					return
				}
				if next == "done" {
					return
				}
				if next == "settling" || next == "scheduled" {
					if _, err := super.Exec(context.Background(), `UPDATE pandokos.tenant_deletion SET not_before = least(not_before, now()), settle_at = CASE WHEN settle_at IS NULL THEN NULL ELSE least(settle_at, now()) END WHERE tenant_id = $1::uuid`, victim.String()); err != nil {
						errs <- err
						return
					}
				}
			}
			errs <- fmt.Errorf("the erasure did not finish")
		}()
		finished := make(chan struct{})
		go func() { wg.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(90 * time.Second):
			t.Fatal("the request and the erasure step did not finish: a deadlock")
		}
		close(errs)
		for err := range errs {
			t.Errorf("%v", err)
		}
		var types []string
		for _, e := range platformEvents(t, d) {
			if e.Tenant != nil && *e.Tenant == victim.String() {
				types = append(types, e.Type)
			}
		}
		if !reflect.DeepEqual(types, []string{"io.pandokos.tenant.deletion_requested.v1", "io.pandokos.tenant.deleted.v1"}) {
			t.Errorf("the victim's events are %v", types)
		}
	})

	t.Run("who reads it, who changes it", func(t *testing.T) {
		for _, role := range []string{"worker", "gateway"} {
			if _, err := query(t, d.Connect(t, role), platformSession, `SELECT count(*) FROM pandokos.platform_outbox_event`); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s reads the log: %v", role, err)
			}
		}
		x := d.SeedTenant(t, "plat-sess")
		for _, s := range []session{tenantSession(x), {name: "none"}} {
			rows, err := query(t, d.Connect(t, "admin"), s, `SELECT count(*) AS n FROM pandokos.platform_outbox_event`)
			if err != nil || rows[0]["n"].(int64) != 0 {
				t.Errorf("the admin role in %s: %v, %v", s.name, rows, err)
			}
		}
		for what, sql := range map[string]string{"UPDATE": `UPDATE pandokos.platform_outbox_event SET subject = 'x'`, "DELETE": `DELETE FROM pandokos.platform_outbox_event`, "TRUNCATE": `TRUNCATE pandokos.platform_outbox_event`} {
			if _, err := d.Super(t).Exec(ctx(t), sql); sqlstate(err) != restrictViolation {
				t.Errorf("%s by the superuser: %v, want SQLSTATE %s", what, err, restrictViolation)
			}
		}
	})
}

// TestExternalReference is T-1023.
func TestExternalReference(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	ad := d.Connect(t, "admin")
	t.Run("set at insert and update in the platform scope", func(t *testing.T) {
		name := "ref-" + dbtest.NewWebhookSecret(t)[6:12]
		name = strings.ToLower(strings.NewReplacer("+", "p", "/", "s").Replace(name))
		if _, err := execIn(t, ad, platformSession, `INSERT INTO pandokos.tenant (name, external_reference) VALUES ($1, 'cust-1')`, name); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if got := dbText(t, d, `SELECT external_reference FROM pandokos.tenant WHERE name = $1`, name); got != "cust-1" {
			t.Errorf("the reference is %q", got)
		}
		if n, err := execIn(t, ad, platformSession, `UPDATE pandokos.tenant SET external_reference = 'cust-2' WHERE name = $1`, name); err != nil || n != 1 {
			t.Errorf("update in the platform scope: %d, %v", n, err)
		}
		// Two tenants may share a reference (two tenants may be one customer), and the default is empty.
		other := d.SeedTenant(t, "ref-other")
		if got := dbText(t, d, `SELECT external_reference FROM pandokos.tenant WHERE id = $1::uuid`, other.String()); got != "" {
			t.Errorf("the default is %q, want empty", got)
		}
		if _, err := execIn(t, ad, platformSession, `UPDATE pandokos.tenant SET external_reference = 'cust-2' WHERE id = $1::uuid`, other.String()); err != nil {
			t.Errorf("a reference used twice: %v", err)
		}
	})

	t.Run("refused in a tenant session, in no session and in the others", func(t *testing.T) {
		x := d.SeedTenant(t, "ref-x")
		for _, s := range []session{tenantSession(x), {name: "none"}, {"empty", "app.tenant_id", ""}} {
			if _, err := execIn(t, ad, s, `UPDATE pandokos.tenant SET external_reference = 'mine' WHERE id = $1::uuid`, x.String()); err == nil {
				if s.name == "tenant" {
					t.Errorf("a tenant's session set its own reference")
				}
			} else if s.name == "tenant" && sqlstate(err) != insufficientPrivilege {
				t.Errorf("the tenant's session: %v, want SQLSTATE %s", err, insufficientPrivilege)
			}
		}
		if got := dbText(t, d, `SELECT external_reference FROM pandokos.tenant WHERE id = $1::uuid`, x.String()); got != "" {
			t.Errorf("the reference became %q", got)
		}
		for _, role := range []string{"worker", "gateway"} {
			if err := attemptInsert(t, d.Connect(t, role), platformSession, nil, `UPDATE pandokos.tenant SET external_reference = 'x'`); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the %s role: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
			}
		}
	})

	t.Run("length and control characters", func(t *testing.T) {
		x := d.SeedTenant(t, "ref-len")
		set := func(ref string) error {
			_, err := execIn(t, ad, platformSession, `UPDATE pandokos.tenant SET external_reference = $2 WHERE id = $1::uuid`, x.String(), ref)
			return err
		}
		for what, ref := range map[string]string{"255 characters": strings.Repeat("a", 255), "255 multibyte characters": strings.Repeat("é", 255), "spaces and symbols": "ACME / 42 (EU)"} {
			if err := set(ref); err != nil {
				t.Errorf("%s: %v", what, err)
			}
		}
		for what, ref := range map[string]string{"256 characters": strings.Repeat("a", 256), "a newline": "a\nb", "a tab": "a\tb", "a NUL-like control": "a\x01b", "a DEL": "a\x7fb"} {
			if err := set(ref); sqlstate(err) != checkViolation && sqlstate(err) != "22021" {
				t.Errorf("%s: %v, want SQLSTATE %s", what, err, checkViolation)
			}
		}
	})

	t.Run("copied by the deletion request", func(t *testing.T) {
		x := d.SeedTenant(t, "ref-del")
		setExternalReference(t, d, x, "cust-copy")
		d.RequestDeletion(t, x, time.Now().Add(time.Hour))
		if got := dbText(t, d, `SELECT external_reference FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, x.String()); got != "cust-copy" {
			t.Errorf("the deletion holds %q, want the reference of the request", got)
		}
		setExternalReference(t, d, x, "cust-changed")
		if got := dbText(t, d, `SELECT external_reference FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, x.String()); got != "cust-copy" {
			t.Errorf("a later change reached the copy: %q", got)
		}
		rows, err := query(t, ad, platformSession, `SELECT external_reference FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, x.String())
		if err != nil || len(rows) != 1 || rows[0]["external_reference"] != "cust-copy" {
			t.Errorf("the admin role reads %v, %v", rows, err)
		}
		// The request cannot set it: no column grant, and the trigger overwrites it.
		if err := attemptInsert(t, ad, platformSession, nil, `INSERT INTO pandokos.tenant_deletion (tenant_id, external_reference) VALUES ($1::uuid, 'forged')`, x.String()); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role inserts a reference into a deletion: %v", err)
		}
	})
}

// TestSchemaLintOutbox is T-1025: the lint has no finding for the new objects (L-08 to L-13, L-17,
// L-21 and L-22 among them), and L-23 sees a missing, disabled or mistyped trigger of the facts
// that are events.
func TestSchemaLintOutbox(t *testing.T) {
	t.Parallel()
	lint := func(d *dbtest.Database) []Finding {
		t.Helper()
		findings, err := Lint(ctx(t), d.Super(t), DefaultSpec())
		if err != nil {
			t.Fatal(err)
		}
		return findings
	}
	t.Run("no finding with the migration applied", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		for _, f := range lint(d) {
			t.Errorf("%s %s: %s", f.Rule, f.Object, f.Detail)
		}
		// The objects are in the spec: the tables, the fixtures and the definer functions.
		spec := DefaultSpec()
		for _, tbl := range []string{"outbox_stream", "outbox_event", "event_destination", "outbox_dead_letter"} {
			if _, ok := spec.Fixtures[tbl]; !ok {
				t.Errorf("Spec.Fixtures has no %s", tbl)
			}
			if _, global := spec.Global[tbl]; global {
				t.Errorf("%s is listed as a global table", tbl)
			}
		}
		for _, tbl := range []string{"event_destination_directory", "platform_outbox_stream", "platform_outbox_event"} {
			if _, ok := spec.Global[tbl]; !ok {
				t.Errorf("Spec.Global has no %s", tbl)
			}
		}
		for _, tbl := range []string{"outbox_event", "platform_outbox_event"} {
			found := false
			for _, a := range spec.AppendOnly {
				found = found || a == tbl
			}
			if !found {
				t.Errorf("Spec.AppendOnly has no %s", tbl)
			}
		}
		for _, fn := range []string{"usage_record_outbox", "limit_event_outbox", "tenant_deletion_outbox", "kek_version_outbox", "event_destination_directory_sync",
			"outbox_relay_claim", "outbox_relay_state", "tenant_outbox_retention"} {
			found := false
			for _, s := range spec.SecurityDefiner {
				found = found || s == fn
			}
			if !found {
				t.Errorf("Spec.SecurityDefiner has no %s", fn)
			}
		}
	})

	for _, c := range []struct {
		name, ddl, object string
	}{
		{"usage-trigger-dropped", `DROP TRIGGER usage_record_outbox ON pandokos.usage_record`, "pandokos.usage_record"},
		{"usage-trigger-disabled", `ALTER TABLE pandokos.usage_record DISABLE TRIGGER usage_record_outbox`, "pandokos.usage_record"},
		{"usage-trigger-per-row", `DROP TRIGGER usage_record_outbox ON pandokos.usage_record;
CREATE TRIGGER usage_record_outbox AFTER INSERT ON pandokos.usage_record FOR EACH ROW EXECUTE FUNCTION pandokos.limit_event_outbox()`, "pandokos.usage_record"},
		{"usage-trigger-before", `DROP TRIGGER usage_record_outbox ON pandokos.usage_record;
CREATE TRIGGER usage_record_outbox BEFORE INSERT ON pandokos.usage_record FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only()`, "pandokos.usage_record"},
		{"limit-trigger-dropped", `DROP TRIGGER limit_event_outbox ON pandokos.limit_event`, "pandokos.limit_event"},
		{"limit-trigger-disabled", `ALTER TABLE pandokos.limit_event DISABLE TRIGGER limit_event_outbox`, "pandokos.limit_event"},
		{"limit-trigger-per-statement", `DROP TRIGGER limit_event_outbox ON pandokos.limit_event;
CREATE TRIGGER limit_event_outbox AFTER INSERT ON pandokos.limit_event FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only()`, "pandokos.limit_event"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := dbtest.NewMigrated(t)
			owned(t, d, c.ddl)
			var l23 []Finding
			for _, f := range lint(d) {
				if f.Rule == "L-23" {
					l23 = append(l23, f)
				}
			}
			if len(l23) != 1 || l23[0].Object != c.object {
				t.Errorf("the L-23 findings are %+v, want one on %s", l23, c.object)
			}
		})
	}

	t.Run("the append-only and tenant-open rules see the new tables", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		owned(t, d, `ALTER TABLE pandokos.outbox_event DISABLE TRIGGER outbox_event_append_only;
ALTER TABLE pandokos.outbox_event DISABLE TRIGGER outbox_event_tenant_open;
GRANT UPDATE ON pandokos.outbox_event TO pandokos_worker`)
		rules := map[string]bool{}
		for _, f := range lint(d) {
			rules[f.Rule] = true
		}
		for _, want := range []string{"L-17", "L-21", "L-12"} {
			if !rules[want] {
				t.Errorf("rule %s did not fire for the log of events; findings %v", want, rules)
			}
		}
	})
}
