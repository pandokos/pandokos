package migrations_test

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestUpgrade0017 is T-1024: migration 0017 on a database at 0016 with usage records, limit events,
// a scheduled and a completed deletion. After it no event exists for the past, every reference is
// empty, no row of the earlier tables changes (the columns of 0016 are compared), the triggers
// exist, and a new record has its event.
func TestUpgrade0017(t *testing.T) {
	d := dbtest.New(t)
	d.MigrateTo(t, 16)
	super := d.Super(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}

	a := d.SeedTenant(t, "up17-a")
	b := d.SeedTenant(t, "up17-b")
	scheduled := d.SeedTenant(t, "up17-scheduled")
	completed := d.SeedTenant(t, "up17-completed")
	for _, id := range []string{a.String(), b.String()} {
		exec(`INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, provider, api_family, operation,
				outcome, duration_ms, input_source, output_source, cost_status)
			SELECT $1::uuid, pandokos.uuid_v7(), now() - make_interval(secs => g), md5(random()::text), 'service_account', gen_random_uuid()::text, 'openai', 'openai', 'chat.completions',
				'ok', 1, 'none', 'none', 'none'
			FROM generate_series(1, 5) g`, id)
		exec(`INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed)
			VALUES ($1::uuid, 'threshold_reached', pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', date_trunc('day', now()), date_trunc('day', now()) + interval '1 day', 80, 10, 8)`, id)
	}
	d.RequestDeletion(t, scheduled, time.Now().Add(time.Hour))
	d.SuperExecReplica(t, "a completed deletion", `INSERT INTO pandokos.tenant_deletion (tenant_id, state, step, reason, requested_by_type, not_before, started_at, completed_at, completion_seq)
		VALUES ($1::uuid, 'completed', 'done', 'old', 'platform', now() - interval '2 days', now() - interval '1 day', now() - interval '1 day', 1)`, completed.String())

	// Every row of every table, over the columns of 0016, before the migration.
	type table struct {
		name string
		cols []string
	}
	var tables []table
	rows, err := super.Query(ctx, `SELECT c.relname, array_agg(a.attname::text ORDER BY a.attnum)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') AND NOT c.relispartition GROUP BY c.relname ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tb table
		if err := rows.Scan(&tb.name, &tb.cols); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, tb)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, tb := range tables {
			var parts []string
			for _, c := range tb.cols {
				parts = append(parts, fmt.Sprintf("'%s', x.%q", c, c))
			}
			var s string
			q := fmt.Sprintf(`SELECT COALESCE(string_agg(j, E'\n' ORDER BY j), '') FROM (SELECT jsonb_build_object(%s)::text AS j FROM pandokos.%q x) y`, strings.Join(parts, ", "), tb.name)
			if err := super.QueryRow(ctx, q).Scan(&s); err != nil {
				t.Fatalf("snapshot of %s: %v", tb.name, err)
			}
			out[tb.name] = s
		}
		return out
	}
	before := snapshot()
	for _, name := range []string{"tenant", "usage_record", "limit_event", "tenant_deletion"} {
		if before[name] == "" {
			t.Fatalf("the test needs rows in %s", name)
		}
	}

	// Migrate to 0017 and no further: the harness activates no key version, so the platform's log is
	// empty unless the migration writes to it.
	d.MigrateTo(t, 17)

	t.Run("no event exists for the past", func(t *testing.T) {
		for _, tbl := range []string{"outbox_event", "outbox_stream", "platform_outbox_event", "event_destination", "outbox_dead_letter", "event_destination_directory"} {
			var n int
			if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.`+tbl).Scan(&n); err != nil || n != 0 {
				t.Errorf("%s holds %d rows after the migration (%v): the past has no event", tbl, n, err)
			}
		}
		var last int
		if err := super.QueryRow(ctx, `SELECT last_seq FROM pandokos.platform_outbox_stream WHERE id = 1`).Scan(&last); err != nil || last != 0 {
			t.Errorf("the platform's stream is at %d (%v), want 0", last, err)
		}
	})

	t.Run("every reference is empty and every row of the earlier tables is unchanged", func(t *testing.T) {
		for _, q := range []string{`SELECT count(*) FROM pandokos.tenant WHERE external_reference <> ''`, `SELECT count(*) FROM pandokos.tenant_deletion WHERE external_reference <> ''`} {
			var n int
			if err := super.QueryRow(ctx, q).Scan(&n); err != nil || n != 0 {
				t.Errorf("%s: %d (%v), want 0", q, n, err)
			}
		}
		after := snapshot()
		var changed []string
		for name, was := range before {
			if name == "schema_migration" { // the migrator's own record gains the row of 0017
				continue
			}
			if after[name] != was {
				changed = append(changed, name)
			}
		}
		sort.Strings(changed)
		if len(changed) > 0 {
			t.Errorf("rows of %v changed by the migration", changed)
		}
	})

	t.Run("the triggers exist and are enabled", func(t *testing.T) {
		names := regexp.MustCompile(`CREATE TRIGGER (\w+) `).FindAllStringSubmatch(migrationSQL(t, 17), -1)
		if len(names) < 15 {
			t.Fatalf("%d triggers in the migration", len(names))
		}
		for _, m := range names {
			var state string
			if err := super.QueryRow(ctx, `SELECT tgenabled::text FROM pg_trigger WHERE tgname = $1 AND NOT tgisinternal LIMIT 1`, m[1]).Scan(&state); err != nil || state != "O" {
				t.Errorf("trigger %s: state %q (%v), want O", m[1], state, err)
			}
		}
		for _, want := range []string{"usage_record_outbox", "limit_event_outbox", "tenant_deletion_outbox", "kek_version_outbox", "event_destination_directory_sync", "tenant_external_reference_guard",
			"outbox_event_append_only", "outbox_event_tenant_open", "event_destination_tenant_open", "event_destination_guard"} {
			found := false
			for _, m := range names {
				found = found || m[1] == want
			}
			if !found {
				t.Errorf("the migration creates no trigger %s", want)
			}
		}
	})

	t.Run("a new record has its event, and the other tables behave as at 0016", func(t *testing.T) {
		d.SeedUsageRecords(t, a, 2)
		got := d.OutboxEvents(t, a)
		if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 || got[0].Type != "io.pandokos.usage.recorded.v1" {
			t.Errorf("the events of a new record: %+v", got)
		}
		if other := d.OutboxEvents(t, b); len(other) != 0 {
			t.Errorf("%d events for a tenant that wrote nothing since the migration", len(other))
		}
		// A deletion request after the migration is an event of the platform, with the reference.
		d.RequestDeletion(t, b, time.Now().Add(time.Hour))
		var n int
		if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.platform_outbox_event WHERE type = 'io.pandokos.tenant.deletion_requested.v1' AND tenant_id = $1::uuid`, b.String()).Scan(&n); err != nil || n != 1 {
			t.Errorf("%d events for a request after the migration (%v), want 1", n, err)
		}
	})

	t.Run("the log of events and the destinations are row-level secured and owned", func(t *testing.T) {
		for _, tbl := range []string{"outbox_stream", "outbox_event", "event_destination", "outbox_dead_letter"} {
			var forced, enabled bool
			if err := super.QueryRow(ctx, `SELECT relforcerowsecurity, relrowsecurity FROM pg_class WHERE oid = ('pandokos.' || $1)::regclass`, tbl).Scan(&forced, &enabled); err != nil || !forced || !enabled {
				t.Errorf("%s: forced %v enabled %v (%v), want both", tbl, forced, enabled, err)
			}
		}
	})
}
