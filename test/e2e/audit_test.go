package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
)

// auditWorld is a process with a tenant that has 30 or more events in its log, from calls of the
// administration API and from authentication failures of both listeners.
type auditWorld struct {
	p      *proc
	boot   string
	ops    string // key file of a platform administrator
	tenant string
	admin  string
	key    string
}

func newAuditWorld(t *testing.T) *auditWorld {
	t.Helper()
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	w := &auditWorld{p: p, boot: boot, ops: boot}
	w.tenant, w.admin = p.newTenant(t, boot, "audited")
	_, w.key = p.newInferenceKey(t, w.admin, w.tenant)
	p.prepareTenant(t, w.admin)
	for i := 0; i < 20; i++ {
		p.mustCLI(t, w.admin, "service-account", "create", "--tenant", w.tenant, "--role", "inference", fmt.Sprintf("audited-%d", i))
	}
	for i := 0; i < 3; i++ {
		p.mustCLI(t, w.admin, "role", "create", "--tenant", w.tenant, "--permission", "usage:read", fmt.Sprintf("auditing-%d", i))
	}
	// An authentication failure of the inference listener (a revoked key) and one of the admin listener.
	revoked := filepath.Join(t.TempDir(), "revoked.key")
	sa := p.mustCLI(t, w.admin, "service-account", "list", "--tenant", w.tenant, "-o", "json")
	m := regexp.MustCompile(`"id":\s*"([0-9a-f-]{36})"`).FindStringSubmatch(sa)
	if m == nil {
		t.Fatalf("no service account in %s", sa)
	}
	out := p.mustCLI(t, w.admin, "key", "create", "--tenant", w.tenant, "--service-account", m[1], "--key-out", revoked)
	kid := regexp.MustCompile(`(?m)^(?:key )?id: (t[a-z2-7]{16})$`).FindStringSubmatch(out)
	if kid == nil {
		t.Fatalf("no key id in %s", out)
	}
	p.mustCLI(t, w.admin, "key", "revoke", "--tenant", w.tenant, kid[1])
	b, _ := os.ReadFile(revoked)
	if status, _ := p.openaiCall(t, strings.TrimSpace(string(b))); status != 401 {
		t.Fatalf("a revoked key: %d, want 401", status)
	}
	if status, _ := exchange(t, "GET", p.adminURL("/v1/tenants/"+w.tenant+"/service-accounts"), authHeader(strings.TrimSpace(string(b))), nil); status != 401 {
		t.Fatalf("a revoked key on the admin listener: %d, want 401", status)
	}
	return w
}

func (w *auditWorld) seqs(t *testing.T, scope string) []int {
	t.Helper()
	sql := `SELECT seq FROM pandokos.platform_audit_event ORDER BY seq`
	var args []any
	if scope != "" {
		sql, args = `SELECT seq FROM pandokos.audit_event WHERE tenant_id = $1::uuid ORDER BY seq`, []any{scope}
	}
	rows, err := w.p.db.Super(t).Query(ctxT(t), sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// TestAuditVerifyDetectsTampering is T-938, the exit criterion of chained audit: a log that nobody
// touched verifies; each way of changing it with write access to the tables is found, and the first
// broken sequence number and its reason are named; the same for the platform's log.
func TestAuditVerifyDetectsTampering(t *testing.T) {
	t.Parallel()
	w := newAuditWorld(t)
	p := w.p
	d := p.db
	seqs := w.seqs(t, w.tenant)
	if len(seqs) < 30 {
		t.Fatalf("the tenant's log holds %d events, want at least 30", len(seqs))
	}
	last := seqs[len(seqs)-1]
	verify := func(log string) (int, string) {
		t.Helper()
		args := []string{"audit", "verify", "--tenant", w.tenant}
		key := w.admin
		if log == "platform" {
			args, key = []string{"platform-audit", "verify"}, w.ops
		}
		code, out, errs := p.cli(t, key, args...)
		return code, out + errs
	}

	if code, out := verify("tenant"); code != 0 || !strings.Contains(out, fmt.Sprintf("verified %d rows, seq 1..%d, head ", len(seqs), last)) {
		t.Fatalf("a log that nobody touched: exit %d, %q", code, out)
	}
	if code, out := verify("platform"); code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "verified ") {
		t.Fatalf("the platform's log that nobody touched: exit %d, %q", code, out)
	}
	snap := d.SnapshotAudit(t)

	tenantCases := []struct {
		name   string
		sql    string
		want   string // the line of the first break
		anyway bool   // only the number of the break is pinned, not its reason
	}{
		{"the detail of row 10 is changed", `UPDATE pandokos.audit_event SET detail = '{"forged": true}'::jsonb WHERE tenant_id = '` + w.tenant + `' AND seq = 10`, "break at seq 10: hash_mismatch", false},
		{"the actor name of row 3 is changed", `UPDATE pandokos.audit_event SET actor_name = 'someone else' WHERE tenant_id = '` + w.tenant + `' AND seq = 3`, "break at seq 3: hash_mismatch", false},
		{"row 15 is deleted", `DELETE FROM pandokos.audit_event WHERE tenant_id = '` + w.tenant + `' AND seq = 15`, "break at seq 16: seq_gap", false},
		{"the first row is deleted", `DELETE FROM pandokos.audit_event WHERE tenant_id = '` + w.tenant + `' AND seq = ` + fmt.Sprint(seqs[0]), fmt.Sprintf("break at seq %d: seq_gap", seqs[1]), false},
		{"the first two rows are deleted", `DELETE FROM pandokos.audit_event WHERE tenant_id = '` + w.tenant + `' AND seq IN (` + fmt.Sprint(seqs[0], ",", seqs[1]) + `)`, fmt.Sprintf("break at seq %d: seq_gap", seqs[2]), false},
		{"the last row is deleted", `DELETE FROM pandokos.audit_event WHERE tenant_id = '` + w.tenant + `' AND seq = ` + fmt.Sprint(last), "head_mismatch", false},
		{"the seq of rows 5 and 6 is swapped", `DO $$ BEGIN
			UPDATE pandokos.audit_event SET seq = 1000000 WHERE tenant_id = '` + w.tenant + `' AND seq = 5;
			UPDATE pandokos.audit_event SET seq = 5 WHERE tenant_id = '` + w.tenant + `' AND seq = 6;
			UPDATE pandokos.audit_event SET seq = 6 WHERE tenant_id = '` + w.tenant + `' AND seq = 1000000; END $$`, "break at seq 5:", true},
		{"a row with a forged seq and a hash computed with a wrong key is inserted", `DO $$ BEGIN
			ALTER TABLE pandokos.audit_event DISABLE TRIGGER audit_event_seal;
			INSERT INTO pandokos.audit_event (tenant_id, id, occurred_at, seq, prev_hash, hash, actor_type, action, resource_type, outcome)
			SELECT tenant_id, pandokos.uuid_v7(), now(), seq + 1, hash, sha256('a wrong key'::bytea), 'platform', 'forged.row', 'test', 'success' FROM pandokos.audit_chain WHERE tenant_id = '` + w.tenant + `';
			ALTER TABLE pandokos.audit_event ENABLE TRIGGER audit_event_seal; END $$`, fmt.Sprintf("break at seq %d:", last+1), true},
	}
	for _, c := range tenantCases {
		d.Tamper(t, c.sql)
		code, out := verify("tenant")
		if code != 1 || !strings.Contains(out, c.want) {
			t.Errorf("%s: exit %d, %q, want exit 1 and %q", c.name, code, out, c.want)
		}
		if !c.anyway && !strings.Contains(out, "break at seq") && !strings.Contains(out, "head_mismatch") {
			t.Errorf("%s: the output does not name the first broken seq: %q", c.name, out)
		}
		d.RestoreAudit(t, snap)
		if code, out := verify("tenant"); code != 0 {
			t.Fatalf("after restoring the log: exit %d, %q", code, out)
		}
	}

	pseqs := w.seqs(t, "")
	if len(pseqs) < 4 {
		t.Fatalf("the platform's log holds %d events", len(pseqs))
	}
	platformCases := []struct{ name, sql, want string }{
		{"the detail of the platform's row 2 is changed", `UPDATE pandokos.platform_audit_event SET detail = '{"forged": true}'::jsonb WHERE seq = 2`, "break at seq 2: hash_mismatch"},
		{"the platform's row 3 is deleted", `DELETE FROM pandokos.platform_audit_event WHERE seq = 3`, "break at seq 4: seq_gap"},
		{"the platform's first row is deleted", fmt.Sprintf(`DELETE FROM pandokos.platform_audit_event WHERE seq = %d`, pseqs[0]), fmt.Sprintf("break at seq %d: seq_gap", pseqs[1])},
		{"the platform's last row is deleted", fmt.Sprintf(`DELETE FROM pandokos.platform_audit_event WHERE seq = %d`, pseqs[len(pseqs)-1]), "head_mismatch"},
	}
	for _, c := range platformCases {
		d.Tamper(t, c.sql)
		if code, out := verify("platform"); code != 1 || !strings.Contains(out, c.want) {
			t.Errorf("%s: exit %d, %q, want exit 1 and %q", c.name, code, out, c.want)
		}
		d.RestoreAudit(t, snap)
	}

	// An explicit --from-seq: the first row read must be that row; above the anchor its link to the
	// rows before it is only reported as not verified.
	if code, out, errs := p.cli(t, w.admin, "audit", "verify", "--tenant", w.tenant, "--from-seq", fmt.Sprint(seqs[5])); code != 0 || !strings.Contains(out+errs, "not verified") {
		t.Errorf("--from-seq above the anchor: exit %d %s %s, want 0 and the note", code, out, errs)
	}
	d.Tamper(t, `DELETE FROM pandokos.audit_event WHERE tenant_id = '`+w.tenant+`' AND seq = `+fmt.Sprint(seqs[5]))
	if code, out, errs := p.cli(t, w.admin, "audit", "verify", "--tenant", w.tenant, "--from-seq", fmt.Sprint(seqs[5])); code != 1 || !strings.Contains(out+errs, fmt.Sprintf("break at seq %d: seq_gap", seqs[6])) {
		t.Errorf("--from-seq of a deleted row: exit %d %s %s, want exit 1 and seq_gap at %d", code, out, errs, seqs[6])
	}
	d.RestoreAudit(t, snap)

	// A whole rewritten chain passes: the limit stated in the audit guide, and --expect-head catches it.
	head := p.mustCLI(t, w.admin, "audit", "head", "--tenant", w.tenant, "-o", "json")
	hash := regexp.MustCompile(`"hash":\s*"([0-9a-f]{64})"`).FindStringSubmatch(head)
	if hash == nil {
		t.Fatalf("no hash in %s", head)
	}
	if code, out, errs := p.cli(t, w.admin, "audit", "verify", "--tenant", w.tenant, "--expect-head", hash[1]); code != 0 {
		t.Errorf("--expect-head equal: exit %d %s %s", code, out, errs)
	}
	if code, out, errs := p.cli(t, w.admin, "audit", "verify", "--tenant", w.tenant, "--expect-head", strings.Repeat("0", 64)); code != 1 || !strings.Contains(out+errs, "expected_head_mismatch") {
		t.Errorf("--expect-head different: exit %d %s %s", code, out, errs)
	}
}

// TestAuditReadEndToEnd is T-974: a support action opened and used in a tenant: the tenant's
// audit list with --support-action-id shows exactly its rows, the platform's log shows the open, the
// interventions and the close, the authentication failures of both listeners are listed, and an
// export verifies offline.
func TestAuditReadEndToEnd(t *testing.T) {
	t.Parallel()
	w := newAuditWorld(t)
	p := w.p
	out := p.mustCLI(t, w.ops, "tenant-support", "open", "--tenant", w.tenant, "--reason", "e2e support", "--permission", "service_account:write", "--permission", "service_account:read")
	m := regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no support action ID in:\n%s", out)
	}
	action := m[1]
	for i := 0; i < 2; i++ {
		p.mustCLI(t, w.ops, "service-account", "create", "--tenant", w.tenant, "--role", "inference", fmt.Sprintf("by-support-%d", i))
	}
	p.mustCLI(t, w.ops, "tenant-support", "close", "--tenant", w.tenant, "--reason", "done", action)

	list := p.mustCLI(t, w.admin, "audit", "list", "--tenant", w.tenant, "--support-action-id", action, "-o", "json")
	if n := strings.Count(list, `"action": "service_account.create"`) + strings.Count(list, `"action":"service_account.create"`); n != 2 {
		t.Errorf("the rows under the support action: %d service_account.create, want 2:\n%s", n, list)
	}
	if strings.Contains(list, `"member.`) || strings.Contains(list, "auth.failure") {
		t.Errorf("a row that is not the action's:\n%s", list)
	}
	platform := p.mustCLI(t, w.ops, "platform-audit", "list", "-o", "json")
	for _, a := range []string{"support_action.open", "support_action.close"} {
		if !strings.Contains(platform, a) {
			t.Errorf("the platform's log does not show %s:\n%s", a, platform)
		}
	}
	// The interventions of a support action are its open and its close: the two rows both logs hold (ADR 0061).
	if n := strings.Count(platform, `"action": "support_action.`) + strings.Count(platform, `"action":"support_action.`); n < 2 {
		t.Errorf("the platform's log shows %d interventions, want at least 2", n)
	}
	failures := p.mustCLI(t, w.admin, "audit", "list", "--tenant", w.tenant, "--action", "auth.failure", "-o", "json")
	if !strings.Contains(failures, `"listener": "inference"`) && !strings.Contains(failures, `"listener":"inference"`) {
		t.Errorf("no failure of the inference listener:\n%s", failures)
	}
	if !strings.Contains(failures, `"listener": "admin"`) && !strings.Contains(failures, `"listener":"admin"`) {
		t.Errorf("no failure of the administration listener:\n%s", failures)
	}

	file := filepath.Join(t.TempDir(), "audit.ndjson")
	p.mustCLI(t, w.admin, "audit", "export", "--tenant", w.tenant, "--out", file)
	// Offline: no server, no key.
	code, vout, verrs := runBinary(t, nil, "audit", "verify", "--tenant", w.tenant, "--file", file)
	if code != 0 || !strings.HasPrefix(vout, "verified ") {
		t.Errorf("audit verify --file: exit %d, %q %q", code, vout, verrs)
	}
	_ = fixtures.CanaryPrompt
}
