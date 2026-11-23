package isolation

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fixtures"
)

// erasureGroups is the table of section 5.3 of the design of sprint 6.3: every table that holds a
// tenant's data, by the step that removes it. TestTenantErasureSteps compares it with the catalogue.
var erasureGroups = map[string][]string{
	"access": {"limit_rule", "subject_mapping", "trusted_issuer", "issuer_directory", "platform_issuer_tenant", "virtual_key",
		"virtual_key_directory", "role_assignment", "idempotency_key", "support_action", "membership_role", "membership",
		"membership_directory", "user_session", "local_account", "user_identity", "app_user", "service_account", "tenant_role"},
	"secrets":       {"tenant_data_key", "provider_credential"},
	"configuration": {"platform_limit_rule", "model_tenant_grant", "tenant_model_restriction", "metadata_key_policy", "tenant_price_rate", "tenant_price_version", "limit_event", "usage_rollup", "usage_rollup_state"},
	"kept":          {"usage_record", "audit_event", "audit_chain", "tenant", "tenant_deletion", "tenant_closed"},
}

// noTenantColumn are the tables of the groups that have no tenant_id column: they refer to the
// tenant through a user, or are the tenant's own row.
var noTenantColumn = map[string]bool{"user_session": true, "local_account": true, "user_identity": true, "tenant": true}

// rowsOf counts the rows a tenant has in a table of the groups, as the superuser.
func rowsOf(t *testing.T, d *dbtest.Database, tbl string, id tenant.ID) int64 {
	t.Helper()
	switch tbl {
	case "user_session", "local_account", "user_identity":
		return dbNumber(t, d, `SELECT count(*) FROM pandokos.`+tbl+` x JOIN pandokos.app_user u ON u.id = x.user_id WHERE u.tenant_id = $1::uuid`, id.String())
	case "tenant":
		return dbNumber(t, d, `SELECT count(*) FROM pandokos.tenant WHERE id = $1::uuid`, id.String())
	}
	return dbNumber(t, d, `SELECT count(*) FROM pandokos.`+tbl+` x WHERE x.tenant_id = $1::uuid`, id.String())
}

// TestTenantErasureSteps is T-979: the table of section 5.3 against the catalogue, the answers and
// the effect of each step, and what a deletion leaves of the tenant, of another tenant and of the
// global tables.
func TestTenantErasureSteps(t *testing.T) {
	d := dbtest.NewMigrated(t)

	// Every table of the database with a tenant column is in exactly one group.
	inGroup := map[string]string{}
	for g, tables := range erasureGroups {
		for _, tbl := range tables {
			if prev, dup := inGroup[tbl]; dup {
				t.Errorf("%s is in the groups %s and %s", tbl, prev, g)
			}
			inGroup[tbl] = g
		}
	}
	rows, err := d.Super(t).Query(ctx(t), `SELECT c.relname FROM pg_catalog.pg_attribute a
			JOIN pg_catalog.pg_class c ON c.oid = a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		  AND a.attname = 'tenant_id' AND NOT a.attisdropped AND a.attnum > 0 ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	catalogue, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for tbl := range inGroup {
		if !noTenantColumn[tbl] {
			want = append(want, tbl)
		}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(catalogue, want) {
		t.Fatalf("the tables with a tenant column are\n%v\nthe groups of section 5.3 hold\n%v\n(a table that holds a tenant's data must be in a group, and so in a step of the deletion)", catalogue, want)
	}

	victim, bystander := d.PopulatedTenant(t), d.PopulatedTenant(t)
	for g, tables := range erasureGroups {
		if g == "kept" {
			continue
		}
		for _, tbl := range tables {
			if tbl == "user_identity" { // a federated user is the platform's, not the tenant's
				continue
			}
			if rowsOf(t, d, tbl, victim) == 0 {
				t.Errorf("PopulatedTenant left no row in %s", tbl)
			}
		}
	}
	before := d.TableDigests(t, bystander)
	bystanderTenantRow := dbText(t, d, `SELECT md5(row_to_json(t)::text) FROM pandokos.tenant t WHERE id = $1::uuid`, bystander.String())
	bystanderPeople := dbText(t, d, `SELECT md5(string_agg(x::text, ',' ORDER BY x::text)) FROM (
		SELECT s.user_id::text || s.token_hash::text FROM pandokos.user_session s JOIN pandokos.app_user u ON u.id = s.user_id WHERE u.tenant_id = $1::uuid
		UNION ALL SELECT a.user_id::text || a.username FROM pandokos.local_account a JOIN pandokos.app_user u ON u.id = a.user_id WHERE u.tenant_id = $1::uuid) x`, bystander.String())
	federated := dbNumber(t, d, `SELECT count(*) FROM pandokos.app_user u JOIN pandokos.user_identity i ON i.user_id = u.id WHERE u.tenant_id IS NULL`)
	if federated != 2 {
		t.Fatalf("%d federated users, want the two that PopulatedTenant made", federated)
	}
	platformBefore := dbNumber(t, d, `SELECT count(*) FROM pandokos.platform_audit_event`)

	// The answers of the steps, in order, and the tables each leaves empty.
	d.RequestDeletion(t, victim, time.Now().Add(-time.Minute))
	worker := d.Connect(t, "worker")
	emptyAfter := map[string]string{ // the answer of the call that ran the step -> the group now empty
		"secrets": "access", "configuration": "secrets", "usage": "configuration",
	}
	var answers []string
	for i := 0; i < 40; i++ {
		next, err := stepOnce(t, worker, victim, 100)
		if err != nil {
			t.Fatalf("step %d after %v: %v", i, answers, err)
		}
		answers = append(answers, next)
		if g, ok := emptyAfter[next]; ok && (len(answers) < 2 || answers[len(answers)-2] != next) {
			for _, tbl := range erasureGroups[g] {
				if got := rowsOf(t, d, tbl, victim); got != 0 {
					t.Errorf("after the step %s, %s still holds %d rows of the tenant", g, tbl, got)
				}
			}
			// the groups of the steps to come are untouched
			for later, tables := range erasureGroups {
				if later == "kept" || stepOrder(later) <= stepOrder(g) {
					continue
				}
				for _, tbl := range tables {
					if tbl != "user_identity" && rowsOf(t, d, tbl, victim) == 0 {
						t.Errorf("after the step %s, %s (step %s) is already empty", g, tbl, later)
					}
				}
			}
		}
		if next == "settling" {
			d.AgeDeletion(t, victim)
		}
		if next == "done" {
			break
		}
	}
	wantAnswers := []string{"access", "secrets", "configuration", "usage", "usage", "audit", "complete", "settling", "done"}
	if !reflect.DeepEqual(answers, wantAnswers) {
		t.Fatalf("the answers of the steps are %v, want %v (two records in the ledger, one batch, then the empty one)", answers, wantAnswers)
	}

	// At the end only the kept tables hold the tenant's ID.
	for _, tbl := range catalogue {
		got := rowsOf(t, d, tbl, victim)
		kept := tbl == "usage_record" || tbl == "audit_event" || tbl == "audit_chain" || tbl == "tenant_deletion" || tbl == "tenant_closed"
		if kept && got == 0 {
			t.Errorf("%s holds no row of the deleted tenant, want the kept records", tbl)
		}
		if !kept && got != 0 {
			t.Errorf("%s still holds %d rows of the deleted tenant", tbl, got)
		}
	}
	for _, tbl := range []string{"user_session", "local_account", "user_identity"} {
		if got := rowsOf(t, d, tbl, victim); got != 0 {
			t.Errorf("%s still holds %d rows of the users the tenant managed", tbl, got)
		}
	}
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.app_user WHERE tenant_id = $1::uuid`, victim.String()); got != 0 {
		t.Errorf("%d users managed by the deleted tenant remain", got)
	}

	// Nothing of the other tenant or of the global tables changed.
	after := d.TableDigests(t, bystander)
	exempt := map[string]bool{"platform_audit_event": true, "platform_audit_chain": true, "tenant": true, "user_session": true, "local_account": true, "user_identity": true}
	for tbl, dg := range before {
		if !exempt[tbl] && after[tbl] != dg {
			t.Errorf("the table %s changed for the other tenant or as a global table", tbl)
		}
	}
	if got := dbText(t, d, `SELECT md5(row_to_json(t)::text) FROM pandokos.tenant t WHERE id = $1::uuid`, bystander.String()); got != bystanderTenantRow {
		t.Error("the other tenant's row changed")
	}
	if got := dbText(t, d, `SELECT md5(string_agg(x::text, ',' ORDER BY x::text)) FROM (
		SELECT s.user_id::text || s.token_hash::text FROM pandokos.user_session s JOIN pandokos.app_user u ON u.id = s.user_id WHERE u.tenant_id = $1::uuid
		UNION ALL SELECT a.user_id::text || a.username FROM pandokos.local_account a JOIN pandokos.app_user u ON u.id = a.user_id WHERE u.tenant_id = $1::uuid) x`, bystander.String()); got != bystanderPeople {
		t.Error("the sessions or accounts of the other tenant's users changed")
	}
	for _, tbl := range []string{"provider", "model", "price_version", "price_rate", "platform_issuer", "platform_key", "platform_role_assignment", "kek_version"} {
		if _, ok := before[tbl]; !ok {
			t.Errorf("TableDigests has no digest of %s", tbl)
		}
	}
	// The member the platform manages is still a user, without a membership.
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.app_user u JOIN pandokos.user_identity i ON i.user_id = u.id WHERE u.tenant_id IS NULL`); got != federated {
		t.Errorf("%d federated users after the deletion, want %d: a user the platform manages is not deleted (D-22)", got, federated)
	}
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.membership WHERE tenant_id = $1::uuid`, victim.String()); got != 0 {
		t.Errorf("%d memberships remain in the deleted tenant", got)
	}

	// The completion record.
	var detail string
	var resource string
	if err := d.Super(t).QueryRow(ctx(t), `SELECT detail::text, resource_id FROM pandokos.platform_audit_event WHERE action = 'tenant.delete_complete' AND resource_id = $1`, victim.String()).Scan(&detail, &resource); err != nil {
		t.Fatalf("the completion record: %v", err)
	}
	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(detail), &rec); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "requested_at", "started_at", "passes", "removed", "kek_versions", "usage", "audit", "residue"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("the completion record has no member %s: %s", k, detail)
		}
	}
	var removed map[string]int64
	if err := json.Unmarshal(rec["removed"], &removed); err != nil {
		t.Fatalf("removed: %v", err)
	}
	for _, tbl := range []string{"limit_rule", "subject_mapping", "trusted_issuer", "platform_issuer_tenant", "virtual_key", "role_assignment", "idempotency_key", "support_action",
		"membership_role", "membership", "app_user", "service_account", "tenant_role", "tenant_data_key", "provider_credential", "platform_limit_rule", "model_tenant_grant",
		"tenant_model_restriction", "metadata_key_policy", "tenant_price_version"} {
		if removed[tbl] < 1 {
			t.Errorf("the completion record counts %d rows removed from %s, want at least 1", removed[tbl], tbl)
		}
	}
	for _, tbl := range []string{"usage_record", "limit_event", "usage_rollup", "usage_rollup_state"} {
		if _, ok := removed[tbl]; ok {
			t.Errorf("the completion record holds a count for %s: a figure of the tenant's usage (ADR 0054)", tbl)
		}
	}
	for _, forbidden := range []string{"usage_record", "limit_event", "usage_rollup", "ledger"} {
		if strings.Contains(detail, `"`+forbidden) {
			t.Errorf("the completion record names %s: %s", forbidden, detail)
		}
	}
	var usage string
	var residue int
	_ = json.Unmarshal(rec["usage"], &usage)
	_ = json.Unmarshal(rec["residue"], &residue)
	if usage != "pseudonymized" || residue != 0 {
		t.Errorf("usage %q and residue %d, want pseudonymized and 0", usage, residue)
	}
	var auditRec map[string]json.RawMessage
	if err := json.Unmarshal(rec["audit"], &auditRec); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"anchor_seq", "last_seq", "rows", "original_head", "resealed_head"} {
		if _, ok := auditRec[k]; !ok {
			t.Errorf("the audit member of the completion record has no %s: %s", k, rec["audit"])
		}
	}
	var keks []string
	_ = json.Unmarshal(rec["kek_versions"], &keks)
	if !reflect.DeepEqual(keks, []string{d.KEKVersion(t)}) {
		t.Errorf("the completion record names the key versions %v, want [%s]", keks, d.KEKVersion(t))
	}
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action IN ('tenant.delete_start', 'tenant.delete_complete') AND resource_id = $1`, victim.String()); got != 2 {
		t.Errorf("%d lifecycle rows for the tenant in the platform log, want 2 (start and complete)", got)
	}
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.platform_audit_event`); got < platformBefore+2 {
		t.Errorf("the platform log has %d rows, was %d", got, platformBefore)
	}
	if got := dbNumber(t, d, `SELECT completion_seq FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, victim.String()); got < 1 {
		t.Errorf("completion_seq is %d", got)
	}
	if got := dbNumber(t, d, `SELECT seq FROM pandokos.platform_audit_event WHERE action = 'tenant.delete_complete' AND resource_id = $1`, victim.String()); got != dbNumber(t, d, `SELECT completion_seq FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, victim.String()) {
		t.Error("completion_seq is not the sequence number of the completion record")
	}

	// The tenant is deleted, its salt is gone, its state is final and its name can be reused.
	if got := dbText(t, d, `SELECT t.state || '/' || (d.salt IS NULL)::text || '/' || d.state FROM pandokos.tenant t JOIN pandokos.tenant_deletion d ON d.tenant_id = t.id WHERE t.id = $1::uuid`, victim.String()); got != "deleted/true/completed" {
		t.Errorf("tenant/salt null/deletion = %s, want deleted/true/completed", got)
	}
	admin := d.Connect(t, "admin")
	for _, state := range []string{"active", "suspended", "provisioning", "deleting"} {
		err := inSession(t, admin, platformSession, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `UPDATE pandokos.tenant SET state = $2 WHERE id = $1::uuid`, victim.String(), state)
			return err
		})
		if err == nil {
			t.Errorf("a deleted tenant was set %s by the admin role", state)
		}
	}
	name := dbText(t, d, `SELECT name FROM pandokos.tenant WHERE id = $1::uuid`, victim.String())
	if reused := d.SeedTenant(t, name); reused == victim {
		t.Error("a new tenant took the ID of the deleted one")
	}
}

func stepOrder(group string) int {
	return map[string]int{"access": 1, "secrets": 2, "configuration": 3, "kept": 9}[group]
}

// TestTenantErasureIdempotent is T-980: a step that ran is not run again, whoever calls and
// however the call ends; the answers that do nothing change nothing; and what the function refuses.
func TestTenantErasureIdempotent(t *testing.T) {
	d := dbtest.NewMigrated(t)
	worker := d.Connect(t, "worker")

	t.Run("scheduled and settling change nothing", func(t *testing.T) {
		id := d.PopulatedTenant(t)
		d.RequestDeletion(t, id, time.Now().Add(time.Hour))
		digests := d.TableDigests(t, id)
		for i := 0; i < 2; i++ {
			if next, err := stepOnce(t, worker, id, 100); err != nil || next != "scheduled" {
				t.Fatalf("a call before not_before: %q, %v, want scheduled", next, err)
			}
		}
		if !reflect.DeepEqual(d.TableDigests(t, id), digests) {
			t.Error("a call before not_before changed a table")
		}
		if st := dbText(t, d, `SELECT state FROM pandokos.tenant WHERE id = $1::uuid`, id.String()); st != "suspended" {
			t.Errorf("the tenant is %s after a call before not_before", st)
		}
		d.AgeDeletion(t, id)
		stepUntil(t, d, worker, id, 100, "complete")
		settle := dbText(t, d, `SELECT settle_at::text FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String())
		digests = d.TableDigests(t, id)
		for i := 0; i < 2; i++ {
			if next, err := stepOnce(t, worker, id, 100); err != nil || next != "settling" {
				t.Fatalf("the call at complete before settle_at: %q, %v, want settling", next, err)
			}
		}
		if got := dbText(t, d, `SELECT settle_at::text FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String()); got != settle {
			t.Errorf("settle_at moved from %s to %s", settle, got)
		}
		if !reflect.DeepEqual(d.TableDigests(t, id), digests) {
			t.Error("a call before settle_at changed a table")
		}
		d.AgeDeletion(t, id)
		if next, err := stepOnce(t, worker, id, 100); err != nil || next != "done" {
			t.Fatalf("the last call: %q, %v", next, err)
		}
		for i := 0; i < 2; i++ { // a call after done answers done and changes nothing
			digests = d.TableDigests(t, id)
			if next, err := stepOnce(t, worker, id, 100); err != nil || next != "done" {
				t.Errorf("a call after done: %q, %v, want done", next, err)
			}
			if !reflect.DeepEqual(d.TableDigests(t, id), digests) {
				t.Error("a call after done changed a table")
			}
		}
	})

	t.Run("two sessions at once run a step once", func(t *testing.T) {
		id := d.PopulatedTenant(t)
		d.RequestDeletion(t, id, time.Now().Add(-time.Minute))
		if next, err := stepOnce(t, worker, id, 100); err != nil || next != "access" {
			t.Fatal(next, err)
		}
		removedBefore := dbNumber(t, d, `SELECT count(*) FROM pandokos.service_account WHERE tenant_id = $1::uuid`, id.String())
		var wg sync.WaitGroup
		answers := make([]string, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for i := range answers {
			conn := d.Connect(t, "worker")
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				answers[i], errs[i] = stepOnce(t, conn, id, 100)
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("a concurrent call failed: %v", err)
			}
		}
		sort.Strings(answers)
		if !reflect.DeepEqual(answers, []string{"configuration", "secrets"}) {
			t.Errorf("two calls at the step access answered %v, want one step each: secrets (the access step) and configuration (the next)", answers)
		}
		if got := dbText(t, d, `SELECT passes::text || '/' || (removed ->> 'service_account') FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String()); got != fmt.Sprintf("1/%d", removedBefore) {
			t.Errorf("passes/service_account removed = %s, want 1/%d (the access step ran once)", got, removedBefore)
		}
	})

	t.Run("a rolled back call leaves everything as it was", func(t *testing.T) {
		id := d.PopulatedTenant(t)
		d.RequestDeletion(t, id, time.Now().Add(-time.Minute))
		if next, err := stepOnce(t, worker, id, 100); err != nil || next != "access" {
			t.Fatal(next, err)
		}
		digests := d.TableDigests(t, id)
		step := dbText(t, d, `SELECT step FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String())
		err := inSession(t, worker, tenantSession(id), func(c context.Context, tx pgx.Tx) error {
			var next string
			if err := tx.QueryRow(c, `SELECT pandokos.tenant_erasure_step(100)`).Scan(&next); err != nil || next != "secrets" {
				t.Errorf("the call inside the transaction: %q, %v", next, err)
			}
			return errRollBack
		})
		if !errors.Is(err, errRollBack) {
			t.Fatal(err)
		}
		if got := dbText(t, d, `SELECT step FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String()); got != step {
			t.Errorf("the step is %s after a rolled back call, was %s", got, step)
		}
		if !reflect.DeepEqual(d.TableDigests(t, id), digests) {
			t.Error("a rolled back call changed a table")
		}
		if next, err := stepOnce(t, worker, id, 100); err != nil || next != "secrets" {
			t.Errorf("the same call again: %q, %v, want secrets", next, err)
		}
	})

	t.Run("a row left behind makes the completion run the removal again", func(t *testing.T) {
		id := d.PopulatedTenant(t)
		d.RequestDeletion(t, id, time.Now().Add(-time.Minute))
		stepUntil(t, d, worker, id, 100, "complete")
		accounts := dbNumber(t, d, `SELECT (removed ->> 'service_account')::bigint FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String())
		// A transaction that began before the deletion commits after it: the superuser writes the row with the triggers off.
		super := d.Super(t)
		if err := pgx.BeginFunc(ctx(t), super, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx(t), `SET LOCAL session_replication_role = replica`); err != nil {
				return err
			}
			_, err := tx.Exec(ctx(t), `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'late-row')`, id.String())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		d.AgeDeletion(t, id)
		if next, err := stepOnce(t, worker, id, 100); err != nil || next != "access" {
			t.Fatalf("the completion with a row left behind: %q, %v, want access", next, err)
		}
		var answers []string
		for i := 0; i < 20; i++ {
			next, err := stepOnce(t, worker, id, 100)
			if err != nil {
				t.Fatal(err)
			}
			answers = append(answers, next)
			if next == "settling" {
				d.AgeDeletion(t, id)
			}
			if next == "done" {
				break
			}
		}
		if want := []string{"secrets", "configuration", "complete", "settling", "done"}; !reflect.DeepEqual(answers, want) {
			t.Errorf("the second pass answered %v, want %v (it skips the ledger and the log)", answers, want)
		}
		if got := dbText(t, d, `SELECT passes::text || '/' || (removed ->> 'service_account')`+` FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String()); got != fmt.Sprintf("2/%d", accounts+1) {
			t.Errorf("passes/service_account = %s, want 2/%d", got, accounts+1)
		}
		if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.service_account WHERE tenant_id = $1::uuid`, id.String()); got != 0 {
			t.Errorf("%d service accounts remain", got)
		}
		if got := dbText(t, d, `SELECT detail ->> 'passes' FROM pandokos.platform_audit_event WHERE action = 'tenant.delete_complete' AND resource_id = $1`, id.String()); got != "2" {
			t.Errorf("the completion record says %s passes, want 2", got)
		}
	})

	t.Run("what the function refuses", func(t *testing.T) {
		id := d.PopulatedTenant(t)
		d.RequestDeletion(t, id, time.Now().Add(-time.Minute))
		for _, batch := range []int{99, 50001, 0, -1} {
			if _, err := stepOnce(t, worker, id, batch); sqlstate(err) != checkViolation {
				t.Errorf("batch %d: %v, want SQLSTATE %s", batch, err, checkViolation)
			}
		}
		if got := dbText(t, d, `SELECT step FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String()); got != "begin" {
			t.Errorf("a refused call moved the step to %s", got)
		}
		for _, s := range []session{platformSession, {name: "none"}, {"empty", "app.tenant_id", ""}} {
			err := inSession(t, worker, s, func(c context.Context, tx pgx.Tx) error {
				var next string
				return tx.QueryRow(c, `SELECT pandokos.tenant_erasure_step(100)`).Scan(&next)
			})
			if sqlstate(err) != insufficientPrivilege {
				t.Errorf("session %s: %v, want SQLSTATE %s", s.name, err, insufficientPrivilege)
			}
		}
		noRequest := d.PopulatedTenant(t)
		if _, err := stepOnce(t, worker, noRequest, 100); sqlstate(err) != "55000" {
			t.Errorf("a tenant without a request: %v, want SQLSTATE 55000", err)
		}
		// A tenant that is not suspended when the deletion would begin.
		if err := pgx.BeginFunc(ctx(t), d.Super(t), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx(t), `SET LOCAL session_replication_role = replica`); err != nil {
				return err
			}
			_, err := tx.Exec(ctx(t), `UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid`, id.String())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := stepOnce(t, worker, id, 100); sqlstate(err) != "55000" {
			t.Errorf("a tenant that is not suspended: %v, want SQLSTATE 55000", err)
		}
		if got := dbText(t, d, `SELECT state FROM pandokos.tenant WHERE id = $1::uuid`, id.String()); got != "active" {
			t.Errorf("the refused beginning changed the tenant to %s", got)
		}
		for _, role := range []string{"admin", "gateway"} {
			conn := d.Connect(t, role)
			err := inSession(t, conn, tenantSession(id), func(c context.Context, tx pgx.Tx) error {
				var next string
				return tx.QueryRow(c, `SELECT pandokos.tenant_erasure_step(100)`).Scan(&next)
			})
			if sqlstate(err) != insufficientPrivilege {
				t.Errorf("the %s role: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
			}
		}
	})
}

var uuidShaped = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ensurePartition makes the monthly partition of the ledger that holds the month, when it is not there.
func ensurePartition(t *testing.T, d *dbtest.Database, month time.Time) {
	t.Helper()
	m := month.UTC()
	name := fmt.Sprintf("usage_record_y%04dm%02d", m.Year(), int(m.Month()))
	if dbNumber(t, d, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'pandokos' AND c.relname = $1`, name) == 0 {
		d.OldPartition(t, m)
	}
}

type ledgerRow struct{ principal, key, rest, metadata string }

// ledgerOf reads the tenant's ledger as the superuser: by ID, the identifiers, the metadata and the
// rest of the row as JSON.
func ledgerOf(t *testing.T, d *dbtest.Database, id tenant.ID) map[string]ledgerRow {
	t.Helper()
	rows, err := d.Super(t).Query(ctx(t), `SELECT u.id::text, u.principal_id, u.key_id, u.metadata::text, (to_jsonb(u) - 'principal_id' - 'key_id' - 'metadata')::text
		FROM pandokos.usage_record u WHERE u.tenant_id = $1::uuid`, id.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]ledgerRow{}
	for rows.Next() {
		var rid string
		var r ledgerRow
		if err := rows.Scan(&rid, &r.principal, &r.key, &r.metadata, &r.rest); err != nil {
			t.Fatal(err)
		}
		out[rid] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedLedger writes n records of the tenant over the three previous months and the default
// partition, with the identifiers given in rotation and metadata that holds a canary.
func seedLedger(t *testing.T, d *dbtest.Database, id tenant.ID, n int, principals, keys []string) {
	t.Helper()
	d.SharedSuper(t)
	_, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, key_id, provider, api_family,
			operation, model, outcome, duration_ms, input_source, output_source, cost_status, metadata)
		SELECT $1::uuid, pandokos.uuid_v7(),
		       now() - make_interval(months => CASE g % 4 WHEN 0 THEN 1 WHEN 1 THEN 2 WHEN 2 THEN 3 ELSE 14 END) - make_interval(secs => g),
		       md5(g::text || $1::text), 'service_account', ($2::text[])[1 + g % cardinality($2::text[])], ($3::text[])[1 + g % cardinality($3::text[])],
		       'openai', 'openai', 'chat.completions', 'gpt-x', 'ok', g, 'none', 'none', 'none',
		       jsonb_build_object('team', 'canary-team', 'n', g::text)
		  FROM generate_series(1, $4::int) g`, id.String(), principals, keys, n)
	if err != nil {
		t.Fatalf("seed the ledger: %v", err)
	}
}

// TestErasurePseudonymizesLedger is T-981: the ledger of a deleted tenant is kept with its
// identifiers replaced by pseudonyms and its metadata emptied, in batches, across partitions.
func TestErasurePseudonymizesLedger(t *testing.T) {
	d := dbtest.NewMigrated(t)
	now := time.Now().UTC()
	for _, m := range []int{1, 2, 3} {
		ensurePartition(t, d, now.AddDate(0, -m, 0))
	}
	principals := []string{"0192f0f0-1111-7000-8000-000000000001", "0192f0f0-1111-7000-8000-000000000002", "0192f0f0-1111-7000-8000-000000000003",
		"0192f0f0-1111-7000-8000-000000000004", "0192f0f0-1111-7000-8000-000000000005"}
	keys := []string{"", dbtest.NewKeyID(t), dbtest.NewKeyID(t), dbtest.NewKeyID(t)}
	a, b, bystander := d.SeedTenant(t, "ledger-a"), d.SeedTenant(t, "ledger-b"), d.SeedTenant(t, "ledger-c")
	seedLedger(t, d, a, 260, principals, keys)
	seedLedger(t, d, b, 260, principals, keys)
	seedLedger(t, d, bystander, 20, principals, keys)
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.usage_record_default WHERE tenant_id = $1::uuid`, a.String()); got != 65 {
		t.Fatalf("%d records in the default partition, want 65", got)
	}
	bystanderDigest := d.TableDigests(t, bystander)["usage_record"]

	pseudonyms := func(id tenant.ID) map[string]string {
		before := ledgerOf(t, d, id)
		d.RequestDeletion(t, id, time.Now().Add(-time.Minute))
		worker := d.Connect(t, "worker")
		stepUntil(t, d, worker, id, 100, "usage")
		var calls []string
		var lastAt time.Time
		var lastID string
		for i := 0; i < 20; i++ {
			next, err := stepOnce(t, worker, id, 100)
			if err != nil {
				t.Fatal(err)
			}
			calls = append(calls, next)
			if next == "audit" {
				break
			}
			var at time.Time
			var cursor string
			if err := d.Super(t).QueryRow(ctx(t), `SELECT usage_started_at, usage_id::text FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String()).Scan(&at, &cursor); err != nil {
				t.Fatalf("the cursor: %v", err)
			}
			if i > 0 && !at.After(lastAt) && (!at.Equal(lastAt) || cursor <= lastID) {
				t.Errorf("the cursor did not advance: (%v, %s) then (%v, %s)", lastAt, lastID, at, cursor)
			}
			lastAt, lastID = at, cursor
		}
		if want := []string{"usage", "usage", "usage", "audit"}; !reflect.DeepEqual(calls, want) {
			t.Errorf("the calls of the step usage answered %v, want %v (260 records in batches of 100, then the empty batch)", calls, want)
		}
		after := ledgerOf(t, d, id)
		if len(after) != len(before) {
			t.Fatalf("%d records after, %d before: a record was added or removed", len(after), len(before))
		}
		mapping := map[string]string{}
		for rid, was := range before {
			now, ok := after[rid]
			if !ok {
				t.Fatalf("the record %s is gone", rid)
			}
			if now.rest != was.rest {
				t.Errorf("a column other than the identifiers and the metadata changed:\n%s\n%s", was.rest, now.rest)
			}
			if now.metadata != "{}" {
				t.Errorf("the metadata is %s, want {}", now.metadata)
			}
			if !uuidShaped.MatchString(now.principal) || now.principal == was.principal {
				t.Errorf("the principal %s became %q", was.principal, now.principal)
			}
			switch {
			case was.key == "" && now.key != "":
				t.Errorf("an empty key_id became %q", now.key)
			case was.key != "" && (!uuidShaped.MatchString(now.key) || now.key == was.key):
				t.Errorf("the key %s became %q", was.key, now.key)
			}
			for from, to := range map[string]string{was.principal: now.principal, was.key: now.key} {
				if prev, ok := mapping[from]; ok && prev != to {
					t.Errorf("%q has two pseudonyms in one tenant: %q and %q", from, prev, to)
				}
				mapping[from] = to
			}
		}
		seen := map[string]string{}
		for from, to := range mapping {
			if from != "" {
				if other, dup := seen[to]; dup {
					t.Errorf("%q and %q share the pseudonym %s", from, other, to)
				}
				seen[to] = from
			}
		}
		stepUntil(t, d, worker, id, 100, "done")
		return mapping
	}
	mapA := pseudonyms(a)
	mapB := pseudonyms(b)
	for from, toA := range mapA {
		if from != "" && toA == mapB[from] {
			t.Errorf("the identifier %s has the pseudonym %s in two deleted tenants", from, toA)
		}
	}
	if mapA[""] != "" || mapB[""] != "" {
		t.Error("an empty identifier got a pseudonym")
	}
	if got := d.TableDigests(t, bystander)["usage_record"]; got != bystanderDigest {
		t.Error("the ledger of the other tenant changed")
	}
}

// auditRow is one row of a tenant's log as the superuser reads it.
type auditRow struct {
	seq                                                                                    int64
	id, occurredAt, actorType, actorID, actorName, keyID, action, resourceType, resourceID string
	outcome, requestID, reason, supportAction, detail                                      string
}

func auditRowsOf(t *testing.T, d *dbtest.Database, id tenant.ID) []auditRow {
	t.Helper()
	rows, err := d.Super(t).Query(ctx(t), `SELECT seq, id::text, occurred_at::text, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason,
			COALESCE(support_action_id::text, ''), detail::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid ORDER BY seq`, id.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.seq, &r.id, &r.occurredAt, &r.actorType, &r.actorID, &r.actorName, &r.keyID, &r.action, &r.resourceType, &r.resourceID, &r.outcome, &r.requestID, &r.reason, &r.supportAction, &r.detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestErasureResealsAuditChain is T-982: the log of a deleted tenant is closed, pseudonymized and
// sealed again as an ordinary chain, and the platform's chain, which records it, is not rewritten.
func TestErasureResealsAuditChain(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.PopulatedTenant(t)
	id := tid.String()
	worker := d.Connect(t, "worker")
	now := time.Now().UTC()

	// A chain whose anchor has moved: an old row, then retention.
	d.SealedRow(t, tid, now.AddDate(0, -40, 0), "old.first")
	if err := inSession(t, worker, tenantSession(tid), func(c context.Context, tx pgx.Tx) error {
		var u, r, a int64
		return tx.QueryRow(c, `SELECT usage_rows, rollup_rows, audit_rows FROM pandokos.tenant_retention(0, 24)`).Scan(&u, &r, &a)
	}); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if anchor := dbNumber(t, d, `SELECT anchor_seq FROM pandokos.audit_chain WHERE tenant_id = $1::uuid`, id); anchor < 2 {
		t.Fatalf("the anchor is %d: retention did not move it", anchor)
	}

	// Rows of every writer, with names, addresses and canaries in the columns that are emptied.
	canary := []string{fixtures.CanaryLabel("actor"), fixtures.CanaryLabel("reason"), fixtures.CanaryLabel("detail"), fixtures.CanaryLabel("resource")}
	email := "jane.doe-" + canary[0] + "@example.test"
	ledgerPrincipal := "0192f0f0-2222-7000-8000-00000000000a"
	supportAction := dbText(t, d, `SELECT id::text FROM pandokos.support_action WHERE tenant_id = $1::uuid LIMIT 1`, id)
	keyID := dbtest.NewKeyID(t)
	type seed struct{ actorType, actorID, actorName, keyID, action, resourceType, resourceID, reason, detail, support string }
	seeds := []seed{
		{"user", ledgerPrincipal, "Jane Doe", "", "member.create", "member", "0192f0f0-2222-7000-8000-00000000000b", canary[1], `{"email":"` + email + `","label":"` + canary[2] + `"}`, ""},
		{"platform", "pkey" + canary[0][:6], "ops " + canary[0], keyID, "tenant.suspend", "tenant", id, canary[1], `{"note":"` + canary[2] + `"}`, ""},
		{"service_account", "0192f0f0-2222-7000-8000-00000000000c", "ci-bot", keyID, "virtual_key.create", "virtual_key", canary[3], "", `{"models":["gpt-4o"]}`, ""},
		{"anonymous", "", "", keyID, "auth.failure", "virtual_key", keyID, "", `{"reason":"bad_secret","listener":"inference"}`, ""},
		{"user", "0192f0f0-2222-7000-8000-00000000000d", "Support " + canary[0], "", "member.read", "member", "0192f0f0-2222-7000-8000-00000000000b", "", `{}`, supportAction},
	}
	for _, s := range seeds {
		var sa any
		if s.support != "" {
			sa = s.support
		}
		_, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, detail, support_action_id)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, 'success', 'req-' || $6, $9, $10::jsonb, $11::uuid)`,
			id, s.actorType, s.actorID, s.actorName, s.keyID, s.action, s.resourceType, s.resourceID, s.reason, s.detail, sa)
		if err != nil {
			t.Fatalf("seed an audit row: %v", err)
		}
	}
	// The same identifier is a principal of the ledger and an actor of the log.
	seedLedger(t, d, tid, 5, []string{ledgerPrincipal}, []string{""})
	// The populated tenant has records of its own: only those of the shared identifier are looked at.
	sharedRecords := map[string]bool{}
	for rid, r := range ledgerOf(t, d, tid) {
		if r.principal == ledgerPrincipal {
			sharedRecords[rid] = true
		}
	}

	beforeRows := auditRowsOf(t, d, tid)
	beforeChain, beforeHead := chainOf(t, d.Super(t), "tenant:"+id)
	requireChain(t, "before the deletion", beforeChain, beforeHead)
	platformBefore, _ := chainOf(t, d.Super(t), "platform")
	original := map[string]bool{}
	for _, r := range beforeRows {
		for _, v := range []string{r.actorID, r.keyID} {
			if v != "" {
				original[v] = true
			}
		}
	}

	d.RequestDeletion(t, tid, time.Now().Add(-time.Minute))
	stepUntil(t, d, worker, tid, 100, "audit")
	next, err := stepOnce(t, worker, tid, 100)
	if err != nil || next != "complete" {
		t.Fatalf("the audit step: %q, %v", next, err)
	}
	afterRows := auditRowsOf(t, d, tid)
	if len(afterRows) != len(beforeRows)+1 {
		t.Fatalf("%d rows after the step, want %d: the rows and one closing row", len(afterRows), len(beforeRows)+1)
	}
	last := afterRows[len(afterRows)-1]
	if last.action != "tenant.delete" || last.actorType != "platform" || last.resourceType != "tenant" || last.resourceID != id || last.outcome != "success" {
		t.Errorf("the last row is %+v, want the closing row tenant.delete", last)
	}
	ledgerPseudonym := ledgerOf(t, d, tid) // after the usage step the ledger holds the pseudonyms
	var principalPseudonym string
	for rid, r := range ledgerPseudonym {
		if sharedRecords[rid] {
			principalPseudonym = r.principal
		}
	}
	for i, was := range beforeRows {
		now := afterRows[i]
		if now.seq != was.seq || now.id != was.id || now.occurredAt != was.occurredAt || now.actorType != was.actorType || now.action != was.action ||
			now.resourceType != was.resourceType || now.outcome != was.outcome || now.requestID != was.requestID || now.supportAction != was.supportAction {
			t.Errorf("a column that stays changed:\n%+v\n%+v", was, now)
		}
		if now.actorName != "" || now.reason != "" || now.detail != "{}" {
			t.Errorf("seq %d keeps a name, a reason or a detail: %q %q %s", was.seq, now.actorName, now.reason, now.detail)
		}
		for _, c := range []struct{ was, now string }{{was.actorID, now.actorID}, {was.keyID, now.keyID}} {
			switch {
			case c.was == "" && c.now != "":
				t.Errorf("seq %d: an empty identifier became %q", was.seq, c.now)
			case c.was != "" && (!uuidShaped.MatchString(c.now) || c.now == c.was):
				t.Errorf("seq %d: %q became %q, want a pseudonym", was.seq, c.was, c.now)
			}
		}
		if was.resourceID == id {
			if now.resourceID != id {
				t.Errorf("seq %d: the tenant's own ID as a resource became %q", was.seq, now.resourceID)
			}
		} else if was.resourceID != "" && (!uuidShaped.MatchString(now.resourceID) || now.resourceID == was.resourceID) {
			t.Errorf("seq %d: the resource %q became %q", was.seq, was.resourceID, now.resourceID)
		}
		if was.actorID == ledgerPrincipal && now.actorID != principalPseudonym {
			t.Errorf("the identifier of a ledger principal and of an audit actor has two pseudonyms: %s in the log, %s in the ledger", now.actorID, principalPseudonym)
		}
	}
	// The same identifier has one pseudonym in the log.
	seen := map[string]string{}
	for i, was := range beforeRows {
		if was.actorID != "" {
			if prev, ok := seen[was.actorID]; ok && prev != afterRows[i].actorID {
				t.Errorf("%q has two pseudonyms in the log", was.actorID)
			}
			seen[was.actorID] = afterRows[i].actorID
		}
	}

	// An ordinary chain: it verifies from the anchor and its head is the one the deletion recorded.
	chain, head := chainOf(t, d.Super(t), "tenant:"+id)
	requireChain(t, "the kept log", chain, head)
	if chain[0].Seq != beforeHead.AnchorSeq || head.AnchorSeq != beforeHead.AnchorSeq || !bytes.Equal(head.AnchorHash, beforeHead.AnchorHash) {
		t.Errorf("the anchor moved: %d %x, was %d %x", head.AnchorSeq, head.AnchorHash, beforeHead.AnchorSeq, beforeHead.AnchorHash)
	}
	var recorded struct {
		AnchorSeq    int64  `json:"anchor_seq"`
		LastSeq      int64  `json:"last_seq"`
		Rows         int64  `json:"rows"`
		OriginalHead string `json:"original_head"`
		ResealedHead string `json:"resealed_head"`
	}
	if err := json.Unmarshal([]byte(dbText(t, d, `SELECT audit::text FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id)), &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.ResealedHead != hex.EncodeToString(head.Hash) || recorded.AnchorSeq != head.AnchorSeq || recorded.LastSeq != head.Seq || recorded.Rows != int64(len(chain)) {
		t.Errorf("the deletion records %+v, the chain has anchor %d, head %d %x, %d rows", recorded, head.AnchorSeq, head.Seq, head.Hash, len(chain))
	}
	// original_head is the head of the log as it was before the rewrite: the rows read before, and the
	// closing row as the step wrote it (the name 'deletion' is the one column the rewrite changed).
	closing := chain[len(chain)-1]
	closing.ActorName = "deletion"
	closing.PrevHash = beforeChain[len(beforeChain)-1].Hash
	h, err := auditchain.Hash(closing)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.OriginalHead != hex.EncodeToString(h[:]) {
		t.Errorf("original_head is %s, the head of the log before the rewrite is %x", recorded.OriginalHead, h)
	}
	if recorded.OriginalHead == recorded.ResealedHead {
		t.Error("the original and the resealed heads are equal")
	}

	// Nothing that named a person or a workload is left in any column of the tenant's rows.
	stepUntil(t, d, worker, tid, 100, "done")
	text := dbText(t, d, `SELECT COALESCE(string_agg(to_jsonb(e)::text, ' '), '') FROM pandokos.audit_event e WHERE e.tenant_id = $1::uuid`, id) + " " +
		dbText(t, d, `SELECT COALESCE(string_agg(to_jsonb(u)::text, ' '), '') FROM pandokos.usage_record u WHERE u.tenant_id = $1::uuid`, id) + " " +
		dbText(t, d, `SELECT COALESCE(string_agg(to_jsonb(c)::text, ' '), '') FROM pandokos.audit_chain c WHERE c.tenant_id = $1::uuid`, id)
	forbidden := append([]string{email, "Jane Doe", "ci-bot", "canary-team"}, canary...)
	for v := range original {
		forbidden = append(forbidden, v)
	}
	for _, v := range forbidden {
		if strings.Contains(text, v) {
			t.Errorf("the kept records still hold %q", v)
		}
	}

	// The platform's chain verifies, and no row of it that existed before changed.
	platformAfter, platformHead := chainOf(t, d.Super(t), "platform")
	requireChain(t, "the platform log", platformAfter, platformHead)
	if len(platformAfter) < len(platformBefore) {
		t.Fatal("rows of the platform log are gone")
	}
	for i, r := range platformBefore {
		if !bytes.Equal(r.Hash, platformAfter[i].Hash) || !bytes.Equal(r.PrevHash, platformAfter[i].PrevHash) || r.Seq != platformAfter[i].Seq {
			t.Errorf("the row %d of the platform log changed", r.Seq)
		}
	}
}

// TestAppendOnlyUnderErasure is T-983: the two settings of the erasure and the retention are the
// only ways round the append-only triggers, each admits one statement, and no runtime role has any.
func TestAppendOnlyUnderErasure(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.PopulatedTenant(t)
	id := tid.String()
	// One row in each table of the five (the platform log and the importer's runs by the superuser).
	d.SeedUsage(t, tid, dbtest.UsageRow{StartedAt: time.Now().Add(-time.Minute)})
	if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, actor_type) VALUES (now(), 'embedded', 'genai-prices-v2', 'success', 'host')`); err != nil {
		t.Fatalf("price_import: %v", err)
	}
	tables := []struct {
		name   string
		update string // a SET clause
		scope  string // "tenant" or "platform"
	}{
		{"audit_event", "reason = 'x'", "tenant"},
		{"usage_record", "model = 'x'", "tenant"},
		{"limit_event", "percent = 81", "tenant"},
		{"platform_audit_event", "reason = 'x'", "platform"},
		{"price_import", "source_ref = 'x'", "platform"},
	}
	for _, tb := range tables {
		if n := dbNumber(t, d, `SELECT count(*) FROM pandokos.`+tb.name); n == 0 {
			t.Fatalf("%s has no row to test with", tb.name)
		}
	}

	// A runtime role that sets either setting still holds no privilege.
	settings := []map[string]string{nil, {"pandokos.erasure": "on"}, {"pandokos.retention": "on"}, {"pandokos.erasure": "on", "pandokos.retention": "on"}}
	for _, role := range runtimeRoles {
		conn := d.Connect(t, role)
		for _, tb := range tables {
			for _, set := range settings {
				for _, sql := range []string{`UPDATE pandokos.` + tb.name + ` SET ` + tb.update, `DELETE FROM pandokos.` + tb.name, `TRUNCATE pandokos.` + tb.name} {
					sess := platformSession
					if tb.scope == "tenant" {
						sess = tenantSession(tid)
					}
					if err := attemptInsert(t, conn, sess, set, sql); sqlstate(err) != insufficientPrivilege {
						t.Errorf("%s runs %q with %v: %v, want SQLSTATE %s", role, sql, set, err, insufficientPrivilege)
					}
				}
			}
		}
	}

	// The triggers themselves, for everybody who has the privilege (the superuser, and the owner in the
	// session of the tenant): only the owner changes a row, under the setting. Each statement runs in
	// a transaction that is rolled back.
	run := func(who string, _ struct{ name, update, scope string }, set map[string]string, sql string) error {
		var got error
		err := pgx.BeginFunc(ctx(t), d.Super(t), func(tx pgx.Tx) error {
			if who == "owner" {
				if _, err := tx.Exec(ctx(t), `SET LOCAL ROLE pandokos_owner`); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx(t), `SELECT set_config('app.tenant_id', $1, true)`, id); err != nil {
					return err
				}
			}
			for k, v := range set {
				if _, err := tx.Exec(ctx(t), `SELECT set_config($1, $2, true)`, k, v); err != nil {
					return err
				}
			}
			_, got = tx.Exec(ctx(t), sql)
			return errRollBack
		})
		if !errors.Is(err, errRollBack) {
			t.Fatal(err)
		}
		return got
	}
	for _, who := range []string{"superuser", "owner"} {
		for _, tb := range tables {
			if who == "owner" && tb.scope == "platform" {
				continue // the owner reads no row of the platform tables: it can neither update nor delete one
			}
			upd, del, trunc := `UPDATE pandokos.`+tb.name+` SET `+tb.update, `DELETE FROM pandokos.`+tb.name, `TRUNCATE pandokos.`+tb.name
			for _, c := range []struct {
				set        map[string]string
				updateOK   bool
				deleteOK   bool
				describing string
			}{
				{nil, false, false, "without a setting"},
				{map[string]string{"pandokos.erasure": "on"}, true, false, "under the erasure"},
				{map[string]string{"pandokos.retention": "on"}, false, true, "under the retention"},
			} {
				for _, s := range []struct {
					sql string
					ok  bool
				}{{upd, c.updateOK}, {del, c.deleteOK}} {
					err := run(who, tb, c.set, s.sql)
					if who == "superuser" {
						s.ok = false // the triggers admit a change for the owner alone, whatever the settings
					}
					switch {
					case s.ok && err != nil:
						t.Errorf("%s %s on %s: %q failed: %v", who, c.describing, tb.name, s.sql, err)
					case !s.ok && (sqlstate(err) != restrictViolation):
						t.Errorf("%s %s on %s: %q gave %v, want SQLSTATE %s", who, c.describing, tb.name, s.sql, err, restrictViolation)
					}
				}
			}
			for _, set := range settings {
				if err := run(who, tb, set, trunc); err == nil {
					t.Errorf("%s truncated %s with %v", who, tb.name, set)
				}
			}
		}
	}

	// A started price version is removed only under the retention setting.
	for _, c := range []struct {
		set map[string]string
		ok  bool
	}{{nil, false}, {map[string]string{"pandokos.erasure": "on"}, false}, {map[string]string{"pandokos.retention": "on"}, true}} {
		err := run("owner", struct{ name, update, scope string }{"tenant_price_version", "", "tenant"}, c.set, `DELETE FROM pandokos.tenant_price_version WHERE valid_from <= now()`)
		if c.ok && err != nil {
			t.Errorf("removing a started price version under %v: %v", c.set, err)
		}
		if !c.ok && sqlstate(err) != restrictViolation {
			t.Errorf("removing a started price version under %v: %v, want SQLSTATE %s", c.set, err, restrictViolation)
		}
	}
	if err := run("owner", struct{ name, update, scope string }{"tenant_price_version", "", "tenant"}, map[string]string{"pandokos.erasure": "on", "pandokos.retention": "on"}, `UPDATE pandokos.tenant_price_version SET note = 'x'`); sqlstate(err) != restrictViolation {
		t.Errorf("updating a price version: %v, want SQLSTATE %s", err, restrictViolation)
	}
	// Only the owner, inside a function of the migrations: a runtime role that sets the retention
	// removes no started version, whether it holds DELETE (the admin role) or not.
	started := `DELETE FROM pandokos.tenant_price_version WHERE valid_from <= now()`
	before := dbNumber(t, d, `SELECT count(*) FROM pandokos.tenant_price_version WHERE tenant_id = $1::uuid AND valid_from <= now()`, id)
	for _, role := range runtimeRoles {
		conn := d.Connect(t, role)
		for _, set := range settings {
			var removed int64
			err := inSession(t, conn, tenantSession(tid), func(c context.Context, tx pgx.Tx) error {
				for k, v := range set {
					if _, err := tx.Exec(c, `SELECT pg_catalog.set_config($1, $2, true)`, k, v); err != nil {
						t.Fatalf("set %s: %v", k, err)
					}
				}
				tag, err := tx.Exec(c, started)
				removed = tag.RowsAffected()
				if err != nil {
					return err
				}
				return errRollBack
			})
			if err == nil || errors.Is(err, errRollBack) || removed != 0 {
				t.Errorf("%s removes a started price version with %v: %d rows, %v", role, set, removed, err)
			}
			want := insufficientPrivilege
			if role == "admin" {
				want = restrictViolation // it holds DELETE: the guard refuses
			}
			if sqlstate(err) != want {
				t.Errorf("%s removes a started price version with %v: %v, want SQLSTATE %s", role, set, err, want)
			}
		}
	}
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.tenant_price_version WHERE tenant_id = $1::uuid AND valid_from <= now()`, id); got != before || got == 0 {
		t.Errorf("%d started price versions, were %d", got, before)
	}

	findings, err := Lint(ctx(t), d.Super(t), DefaultSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("%s %s: %s", f.Rule, f.Object, f.Detail)
	}
}

// TestDeletedTenantRetention is T-984: the records kept for a deleted tenant leave with the
// installation's retention, which is recorded in the platform's log and never in the closed chain.
func TestDeletedTenantRetention(t *testing.T) {
	d := dbtest.NewMigrated(t)
	worker := d.Connect(t, "worker")
	now := time.Now().UTC()
	tid := d.SeedTenant(t, "retained")
	id := tid.String()
	// Three old rows, then two recent, in the log; two old records (the default partition) and two recent ones in the ledger.
	for i := 0; i < 3; i++ {
		d.SealedRow(t, tid, now.AddDate(0, -35, -i), "old.event")
	}
	for i := 0; i < 2; i++ {
		d.SealedRow(t, tid, now.Add(-time.Duration(i+1)*time.Hour), "recent.event")
	}
	d.SeedUsage(t, tid,
		dbtest.UsageRow{StartedAt: now.AddDate(0, -20, 0)}, dbtest.UsageRow{StartedAt: now.AddDate(0, -21, 0)},
		dbtest.UsageRow{StartedAt: now.Add(-time.Hour)}, dbtest.UsageRow{StartedAt: now.Add(-2 * time.Hour)})
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.usage_record_default WHERE tenant_id = $1::uuid`, id); got != 2 {
		t.Fatalf("%d records in the default partition, want 2", got)
	}

	retention := func(conn *pgx.Conn, as tenant.ID, usageMonths, auditMonths int) (u, a int64, err error) {
		err = inSession(t, conn, tenantSession(as), func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT usage_rows, audit_rows FROM pandokos.deleted_tenant_retention($1, $2)`, usageMonths, auditMonths).Scan(&u, &a)
		})
		return u, a, err
	}

	// Refused for a tenant that is not deleted (active, suspended with a request, being deleted), and for other roles.
	if _, _, err := retention(worker, tid, 12, 24); sqlstate(err) != insufficientPrivilege {
		t.Errorf("an active tenant: %v, want SQLSTATE %s", err, insufficientPrivilege)
	}
	d.RequestDeletion(t, tid, time.Now().Add(-time.Minute))
	if _, _, err := retention(worker, tid, 12, 24); sqlstate(err) != insufficientPrivilege {
		t.Errorf("a tenant with a request: %v, want SQLSTATE %s", err, insufficientPrivilege)
	}
	stepUntil(t, d, worker, tid, 100, "done")
	for _, role := range []string{"admin", "gateway"} {
		if _, _, err := retention(d.Connect(t, role), tid, 12, 24); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the %s role: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
		}
	}
	for _, c := range [][2]int{{1, 24}, {121, 24}, {12, -1}, {12, 1201}, {-1, 0}} {
		if _, _, err := retention(worker, tid, c[0], c[1]); sqlstate(err) != checkViolation {
			t.Errorf("months %v: %v, want SQLSTATE %s", c, err, checkViolation)
		}
	}
	if _, _, err := retention(worker, d.SeedTenant(t, "other-active"), 12, 24); sqlstate(err) != insufficientPrivilege {
		t.Errorf("another tenant that is not deleted: %v, want SQLSTATE %s", err, insufficientPrivilege)
	}

	// Nothing is removed when both keys are 0.
	chainBefore, headBefore := chainOf(t, d.Super(t), "tenant:"+id)
	var resealed string
	if err := d.Super(t).QueryRow(ctx(t), `SELECT audit ->> 'resealed_head' FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id).Scan(&resealed); err != nil {
		t.Fatal(err)
	}
	platformRows := dbNumber(t, d, `SELECT count(*) FROM pandokos.platform_audit_event`)
	if u, a, err := retention(worker, tid, 0, 0); err != nil || u != 0 || a != 0 {
		t.Errorf("both keys 0: %d %d %v, want nothing removed", u, a, err)
	}

	// The prefix of the log and the old records of the default partition go.
	u, a, err := retention(worker, tid, 12, 24)
	if err != nil || u != 2 || a != 3 {
		t.Fatalf("retention: %d usage records, %d audit rows, %v; want 2 and 3", u, a, err)
	}
	chain, head := chainOf(t, d.Super(t), "tenant:"+id)
	requireChain(t, "the kept log after retention", chain, head)
	if head.AnchorSeq != headBefore.AnchorSeq+3 || len(chain) != len(chainBefore)-3 {
		t.Errorf("the anchor is %d with %d rows, want %d with %d", head.AnchorSeq, len(chain), headBefore.AnchorSeq+3, len(chainBefore)-3)
	}
	if !bytes.Equal(head.AnchorHash, chainBefore[2].Hash) {
		t.Errorf("the anchor hash is %x, want the hash of the last row removed %x", head.AnchorHash, chainBefore[2].Hash)
	}
	if hex.EncodeToString(head.Hash) != resealed {
		t.Errorf("the head is %x, want the resealed head %s", head.Hash, resealed)
	}
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, id); got != 2 {
		t.Errorf("%d records remain in the ledger, want the 2 recent ones", got)
	}
	var detail string
	if err := d.Super(t).QueryRow(ctx(t), `SELECT detail::text FROM pandokos.platform_audit_event WHERE action = 'tenant.records_retention' AND resource_id = $1`, id).Scan(&detail); err != nil {
		t.Fatalf("the record of the retention in the platform log: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(detail), &rec); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"before", "months", "audit_events", "first_seq", "last_seq", "last_hash"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("the retention record has no %s: %s", k, detail)
		}
	}
	if strings.Contains(detail, "usage") || strings.Contains(detail, "ledger") {
		t.Errorf("the retention record holds a figure of usage: %s", detail)
	}
	if rec["audit_events"] != float64(3) {
		t.Errorf("the retention record counts %v audit events, want 3", rec["audit_events"])
	}
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.platform_audit_event`); got != platformRows+1 {
		t.Errorf("the platform log has %d rows, want %d", got, platformRows+1)
	}

	// A second run does and writes nothing.
	if u, a, err := retention(worker, tid, 12, 24); err != nil || u != 0 || a != 0 {
		t.Errorf("a second run: %d %d %v", u, a, err)
	}
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.platform_audit_event`); got != platformRows+1 {
		t.Errorf("the second run wrote to the platform log")
	}

	// Everything goes: the chain verifies as empty, and the head stays the resealed one.
	d.Tamper(t, `UPDATE pandokos.audit_event SET occurred_at = now() - interval '40 months' WHERE tenant_id = $1::uuid`, id)
	if _, a, err := retention(worker, tid, 12, 24); err != nil || a != int64(len(chain)) {
		t.Fatalf("the last run removed %d audit rows (%v), want %d", a, err, len(chain))
	}
	empty, emptyHead := chainOf(t, d.Super(t), "tenant:"+id)
	requireChain(t, "the emptied log", empty, emptyHead)
	if len(empty) != 0 || emptyHead.AnchorSeq != headBefore.AnchorSeq+int64(len(chainBefore)) || hex.EncodeToString(emptyHead.Hash) != resealed {
		t.Errorf("the emptied log has %d rows, anchor %d, head %x", len(empty), emptyHead.AnchorSeq, emptyHead.Hash)
	}
}

// TestErasureLateRows is the part of T-981 and T-982 about rows that arrive late (review): a ledger
// record whose insert passed the guard before the deletion began and that committed after the step
// usage had passed it, and a row appended to the log after the step audit, still with the values
// they were written with. The step complete finds both, pseudonymizes them like the others (the
// same pseudonyms), seals the log again behind a new closing row and runs a second pass; the
// deletion completes with no row that holds an identifier.
func TestErasureLateRows(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.PopulatedTenant(t)
	id := tid.String()
	principal := "0192f0f0-3333-7000-8000-00000000000a"
	key := dbtest.NewKeyID(t)
	seedLedger(t, d, tid, 20, []string{principal}, []string{key})
	seeded := map[string]bool{}
	for rid, r := range ledgerOf(t, d, tid) {
		seeded[rid] = r.principal == principal
	}
	d.RequestDeletion(t, tid, time.Now().Add(-time.Minute))

	// A writer of the ledger whose statement passed the guard before the start, still open.
	gw := d.Connect(t, "gateway")
	writer, err := gw.Begin(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(ctx(t)) }()
	if _, err := writer.Exec(ctx(t), `SELECT pg_catalog.set_config('app.tenant_id', $1, true)`, id); err != nil {
		t.Fatal(err)
	}
	lateID := dbText(t, d, `SELECT pandokos.uuid_v7()::text`)
	if _, err := writer.Exec(ctx(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, key_id, provider, api_family,
			operation, model, outcome, duration_ms, input_source, output_source, cost_status, metadata)
		VALUES ($1::uuid, $4::uuid, now(), md5('late'), 'service_account', $2, $3, 'openai', 'openai', 'chat.completions', 'gpt-x', 'ok', 1,
		        'none', 'none', 'none', '{"team":"late-team"}')`, id, principal, key, lateID); err != nil {
		t.Fatalf("the late record: %v", err)
	}
	worker := d.Connect(t, "worker")
	stepUntil(t, d, worker, tid, 100, "audit") // the step usage passed the record, which it did not see
	if err := writer.Commit(ctx(t)); err != nil {
		t.Fatalf("the writer commits after the step usage: %v", err)
	}
	if got := ledgerOf(t, d, tid)[lateID]; got.principal != principal {
		t.Fatalf("the late record holds %q after the step usage, want the principal it was written with", got.principal)
	}
	if next, err := stepOnce(t, worker, tid, 100); err != nil || next != "complete" {
		t.Fatalf("the step audit: %q, %v", next, err)
	}

	// A row appended to the log after the step audit, sealed as any row is (the guard is held off
	// for this transaction only: no role of the product could write it, a transaction that began
	// before the deletion can).
	err = pgx.BeginFunc(ctx(t), d.Super(t), func(tx pgx.Tx) error {
		for _, q := range []string{
			`ALTER TABLE pandokos.audit_event DISABLE TRIGGER audit_event_tenant_open`,
			`ALTER TABLE pandokos.audit_chain DISABLE TRIGGER audit_chain_tenant_open`,
		} {
			if _, err := tx.Exec(ctx(t), q); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx(t), `INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, reason, detail)
			VALUES ($1::uuid, 'service_account', $2, 'late-bot', $3, 'virtual_key.update', 'virtual_key', $3, 'success', 'late reason', '{"label":"late-label"}')`, id, principal, key); err != nil {
			return err
		}
		for _, q := range []string{
			`ALTER TABLE pandokos.audit_event ENABLE TRIGGER audit_event_tenant_open`,
			`ALTER TABLE pandokos.audit_chain ENABLE TRIGGER audit_chain_tenant_open`,
		} {
			if _, err := tx.Exec(ctx(t), q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("the late audit row: %v", err)
	}

	answers := stepUntil(t, d, worker, tid, 100, "done")
	if want := []string{"settling", "access", "secrets", "configuration", "complete", "settling", "done"}; !reflect.DeepEqual(answers, want) {
		t.Errorf("the step complete and after answered %v, want %v (the late rows make a second pass)", answers, want)
	}
	if n := dbNumber(t, d, `SELECT passes FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id); n != 2 {
		t.Errorf("%d passes, want 2", n)
	}

	// The ledger: the late record has the pseudonyms of the others, and no record an identifier.
	ledger := ledgerOf(t, d, tid)
	var pseudoPrincipal, pseudoKey string
	for rid, r := range ledger {
		if r.principal == principal || r.key == key || strings.Contains(r.metadata, "team") {
			t.Errorf("the record %s holds %q, %q, %s after the deletion", rid, r.principal, r.key, r.metadata)
		}
		if seeded[rid] && pseudoPrincipal == "" {
			pseudoPrincipal, pseudoKey = r.principal, r.key
		}
	}
	if late := ledger[lateID]; late.principal != pseudoPrincipal || late.key != pseudoKey || late.metadata != "{}" {
		t.Errorf("the late record became %q, %q, %s; the records of the same principal and key hold %q, %q", late.principal, late.key, late.metadata, pseudoPrincipal, pseudoKey)
	}

	// The log: an ordinary chain, ending with a closing row, holding no identifier, and the deletion's
	// figures are those of the chain.
	rows, head := chainOf(t, d.Super(t), "tenant:"+id)
	requireChain(t, "the kept log", rows, head)
	if len(rows) == 0 || rows[len(rows)-1].Action != "tenant.delete" {
		t.Fatal("the last row of the kept log is not tenant.delete")
	}
	for _, r := range auditRowsOf(t, d, tid) {
		if r.actorID == principal || r.keyID == key || r.resourceID == key || r.actorName != "" || r.reason != "" || r.detail != "{}" {
			t.Errorf("the row %d of the kept log is not pseudonymized: %+v", r.seq, r)
		}
		if r.action == "virtual_key.update" && r.actorID != pseudoPrincipal {
			t.Errorf("the late row's actor became %q, the ledger's pseudonym of the same identifier is %q", r.actorID, pseudoPrincipal)
		}
	}
	var lastSeq, rowCount int64
	var resealed string
	if err := d.Super(t).QueryRow(ctx(t), `SELECT (audit ->> 'last_seq')::bigint, (audit ->> 'rows')::bigint, audit ->> 'resealed_head' FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id).
		Scan(&lastSeq, &rowCount, &resealed); err != nil {
		t.Fatal(err)
	}
	if lastSeq != head.Seq || resealed != hex.EncodeToString(head.Hash) || rowCount != int64(len(rows)) {
		t.Errorf("the deletion says last_seq %d, %d rows, head %s; the chain has %d, %d rows, head %x", lastSeq, rowCount, resealed, head.Seq, len(rows), head.Hash)
	}
	if got := dbText(t, d, `SELECT (detail -> 'audit')::text FROM pandokos.platform_audit_event WHERE action = 'tenant.delete_complete' AND resource_id = $1`, id); got != dbText(t, d, `SELECT audit::text FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id) {
		t.Errorf("the completion record's audit %s differs from the deletion's", got)
	}
}
