package e2e

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// viewerKey makes a service account with the role usage-viewer in the tenant, and a key for it, and
// returns the key file.
func (p *proc) viewerKey(t *testing.T, admin, tenantID, name string) string {
	t.Helper()
	p.mustCLI(t, admin, "service-account", "create", "--tenant", tenantID, "--role", "usage-viewer", name)
	list := p.mustCLI(t, admin, "service-account", "list", "--tenant", tenantID, "-o", "json")
	var sas struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(list), &sas); err != nil {
		t.Fatal(err)
	}
	for _, sa := range sas.Items {
		if sa.Name == name {
			file := filepath.Join(t.TempDir(), name+".key")
			p.mustCLI(t, admin, "key", "create", "--tenant", tenantID, "--service-account", sa.ID, "--key-out", file)
			return file
		}
	}
	t.Fatalf("no service account %s in %s", name, list)
	return ""
}

type reportOut struct {
	Rows []struct {
		Model    string  `json:"model"`
		Currency string  `json:"currency"`
		Cost     *string `json:"cost"`
		Requests int     `json:"requests"`
	} `json:"rows"`
}

func costs(t *testing.T, out string) map[string]*big.Rat {
	t.Helper()
	var r reportOut
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	m := map[string]*big.Rat{}
	for _, row := range r.Rows {
		if row.Cost == nil {
			continue
		}
		c, ok := new(big.Rat).SetString(*row.Cost)
		if !ok {
			t.Fatalf("cost %q", *row.Cost)
		}
		k := row.Model + "/" + row.Currency
		if m[k] == nil {
			m[k] = new(big.Rat)
		}
		m[k].Add(m[k], c)
	}
	return m
}

func sameCosts(t *testing.T, what string, got map[string]*big.Rat, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d groups %v, want %d %v", what, len(got), got, len(want), want)
	}
	for k, w := range want {
		wr, _ := new(big.Rat).SetString(w)
		if got[k] == nil || got[k].Cmp(wr) != 0 {
			t.Errorf("%s: %s is %v, want %s", what, k, got[k], w)
		}
	}
}

// TestReportLastMonthByModel is T-955, the exit criterion of reports: two tenants with ledger rows of
// last month and calls of this month; a usage-viewer of A reads last month by model and gets the cost
// per currency equal to the sum of A's rows and nothing of B's; A's key on B's path is a 404 like an
// unknown tenant's; B's viewer sees only B's; a platform key gets 403; the answer is the same before
// and after the rollup caught up.
func TestReportLastMonthByModel(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up, rollupInterval: "10s"})
	boot := p.bootstrap(t)
	ops := filepath.Join(t.TempDir(), "ops.key")
	p.mustCLI(t, boot, "platform-key", "create", "--key-out", ops)
	idA, adminA := p.newTenant(t, boot, "reports-a")
	idB, adminB := p.newTenant(t, boot, "reports-b")
	_, keyA := p.newInferenceKey(t, adminA, idA)
	_, keyB := p.newInferenceKey(t, adminB, idB)
	p.prepareTenant(t, adminA)
	p.prepareTenant(t, adminB)
	viewerA := p.viewerKey(t, adminA, idA, "viewer-a")
	viewerB := p.viewerKey(t, adminB, idB, "viewer-b")

	now := time.Now().UTC()
	last := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.UTC)
	month := last.Format("2006-01")
	tidA, _ := tenant.ParseID(idA)
	tidB, _ := tenant.ParseID(idB)
	old := now.Add(-10 * time.Minute) // recorded long enough ago for the rollup to take them
	m1, m2 := fakeprovider.ModelBasic, fakeprovider.ModelDetails
	row := func(day int, model, cost, cur string) dbtest.UsageRow {
		return dbtest.UsageRow{StartedAt: last.AddDate(0, 0, day).Add(12 * time.Hour), RecordedAt: old, PrincipalID: "0192f0f0-0000-7000-8000-0000000000a1",
			Provider: "openai", Model: model, InputTokens: 10, OutputTokens: 5, Cost: cost, Currency: cur}
	}
	p.db.SeedUsage(t, tidA, row(2, m1, "1.5", "EUR"), row(3, m1, "2.25", "EUR"), row(4, m2, "4", "USD"), row(5, m2, "0.5", "EUR"))
	p.db.SeedUsage(t, tidB, row(2, m1, "100", "EUR"), row(3, m2, "200", "USD"))
	// Calls of this month on two models: their records are not in last month's report.
	for _, model := range []string{m1, m2} {
		if status, text := p.chatCall(t, keyA, model, false); status != 200 {
			t.Fatalf("a call of A on %s: %d %s", model, status, text)
		}
	}
	if status, text := p.chatCall(t, keyB, m1, false); status != 200 {
		t.Fatalf("a call of B: %d %s", status, text)
	}

	wantA := map[string]string{m1 + "/EUR": "3.75", m2 + "/USD": "4", m2 + "/EUR": "0.5"}
	before := p.mustCLI(t, viewerA, "usage", "report", "--tenant", idA, "--month", month, "--group-by", "model", "-o", "json")
	sameCosts(t, "A before the rollup", costs(t, before), wantA)
	for _, foreign := range []string{idB, "100", "200", "reports-b"} {
		if strings.Contains(before, foreign) {
			t.Errorf("A's report holds %q, which is tenant B's", foreign)
		}
	}
	sameCosts(t, "B", costs(t, p.mustCLI(t, viewerB, "usage", "report", "--tenant", idB, "--month", month, "--group-by", "model", "-o", "json")), map[string]string{m1 + "/EUR": "100", m2 + "/USD": "200"})

	// A's key on B's path is the answer for a tenant that does not exist.
	code, _, errs := p.cli(t, viewerA, "usage", "report", "--tenant", idB, "--month", month)
	unknown := "0192f0f0-0000-7000-8000-00000000dead"
	code2, _, errs2 := p.cli(t, viewerA, "usage", "report", "--tenant", unknown, "--month", month)
	norm := func(s string) string {
		return regexp.MustCompile(`request [0-9a-f]{32}`).ReplaceAllString(s, "request ID")
	}
	if code != 1 || code2 != 1 || !strings.Contains(errs, "not_found") || norm(errs) != norm(errs2) {
		t.Errorf("another tenant's path: exit %d %q; an unknown tenant: exit %d %q", code, errs, code2, errs2)
	}
	if code, _, errs := p.cli(t, ops, "usage", "report", "--tenant", idA, "--month", month); code != 1 || !strings.Contains(errs, "forbidden") {
		t.Errorf("a platform key: exit %d %q, want 1 and forbidden", code, errs)
	}

	// The rollup catches up; the answer does not change.
	super := p.db.Super(t) // one connection for the polling, not one for each poll
	testutil.Eventually(t, 40*time.Second, "the rollup to hold the rows of tenant A", func() bool {
		var n int
		if err := super.QueryRow(ctxT(t), `SELECT count(*) FROM pandokos.usage_rollup WHERE tenant_id = $1::uuid AND hour < $2`, idA, last.AddDate(0, 1, 0)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	})
	after := p.mustCLI(t, viewerA, "usage", "report", "--tenant", idA, "--month", month, "--group-by", "model", "-o", "json")
	sameCosts(t, "A after the rollup", costs(t, after), wantA)
	if before != after {
		t.Errorf("the report changed when the rollup caught up:\n%s\n%s", before, after)
	}
}

// TestExportsHoldNoForeignData is T-959: what tenant A can read and export holds nothing of tenant
// B, and the platform's document holds no tenant secret and no usage figure.
func TestExportsHoldNoForeignData(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	ops := filepath.Join(t.TempDir(), "ops.key")
	p.mustCLI(t, boot, "platform-key", "create", "--key-out", ops)
	nameB := fixtures.CanaryLabel("tenantb")
	idA, adminA := p.newTenant(t, boot, "exports-a")
	idB, adminB := p.newTenant(t, boot, nameB)
	keyIDA, keyA := p.newInferenceKey(t, adminA, idA)
	keyIDB, keyB := p.newInferenceKey(t, adminB, idB)
	p.prepareTenant(t, adminA)
	credB := p.addCredential(t, adminB, "openai", fixtures.CanaryCredential)
	metaB := fixtures.CanaryLabel("metab")
	metaA := fixtures.CanaryLabel("metaa")
	for who, c := range map[string]struct{ key, meta string }{"A": {keyA, metaA}, "B": {keyB, metaB}} {
		status, text := exchange(t, "POST", p.url(chatPath), metaHeader(c.key, c.meta), chatBody(fakeprovider.ModelBasic, false, fixtures.CanaryPrompt))
		if status != 200 {
			t.Fatalf("a call of %s: %d %s", who, status, text)
		}
	}
	p.mustCLI(t, adminB, "role", "create", "--tenant", idB, "--permission", "usage:read", fixtures.CanaryLabel("roleb"))
	testutil.Eventually(t, 10*time.Second, "both records", func() bool { return len(usageRows(t, p.db, idA)) == 1 && len(usageRows(t, p.db, idB)) == 1 })

	from := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	outputs := map[string]string{
		"usage export (ndjson)": p.mustCLI(t, adminA, "usage", "export", "--tenant", idA, "--from", from, "--to", to),
		"usage export (csv)":    p.mustCLI(t, adminA, "usage", "export", "--tenant", idA, "--from", from, "--to", to, "--format", "csv"),
		"usage report":          p.mustCLI(t, adminA, "usage", "report", "--tenant", idA, "--group-by", "provider,model,caller,key", "-o", "json"),
		"audit list":            p.mustCLI(t, adminA, "audit", "list", "--tenant", idA, "--limit", "200", "-o", "json"),
		"audit export":          p.mustCLI(t, adminA, "audit", "export", "--tenant", idA),
		"export":                p.mustCLI(t, adminA, "export", "--tenant", idA),
	}
	needles := append([]string{idB, nameB, keyIDB, credB, metaB}, fixtures.Canaries()...)
	for name, out := range outputs {
		for _, n := range needles {
			if n != "" && strings.Contains(out, n) {
				t.Errorf("%s holds %q", name, n)
			}
		}
		for _, secret := range []string{keyA, keyB} {
			if strings.Contains(out, secret) {
				t.Errorf("%s holds a key secret", name)
			}
		}
	}
	if !strings.Contains(outputs["usage export (ndjson)"], metaA) {
		t.Errorf("the export of A does not hold A's own metadata: the test proves nothing")
	}
	if !strings.Contains(outputs["usage export (ndjson)"], keyIDA) {
		t.Errorf("the export of A does not hold A's key ID")
	}

	platform := p.mustCLI(t, ops, "export", "--platform")
	for _, n := range append(append([]string{keyA, keyB, keyIDA, keyIDB, metaA, metaB, "input_tokens", "cost_status"}, fixtures.Canaries()...), "secret_hash") {
		if strings.Contains(platform, n) {
			t.Errorf("the platform's document holds %q", n)
		}
	}
	_ = fmt.Sprint
}

// TestRetentionEndToEnd is T-976: with retention.usage_months 2 and retention.audit_months 1, a
// database with old partitions and old sealed rows is cleaned at the start of the process: the old
// partitions are gone, the logs say so, both chains verify, a report of the dropped months is empty and
// the log of the process says retention applied.
func TestRetentionEndToEnd(t *testing.T) {
	up := fakeprovider.New(t)
	d := dbtest.New(t)
	d.Migrate(t)
	first := startBinary(t, opts{level: "info", up: up, db: d})
	boot := first.bootstrap(t)
	idT, admin := first.newTenant(t, boot, "retained")
	first.stop(t)
	tid, _ := tenant.ParseID(idT)

	now := time.Now().UTC()
	month := func(back int) time.Time {
		return time.Date(now.Year(), now.Month()-time.Month(back), 15, 12, 0, 0, 0, time.UTC)
	}
	partition := d.OldPartition(t, month(6))
	d.SeedUsage(t, tid, dbtest.UsageRow{StartedAt: month(6), Cost: "1", Currency: "EUR"}, dbtest.UsageRow{StartedAt: month(10), Cost: "2", Currency: "EUR"}, dbtest.UsageRow{StartedAt: now})
	for _, back := range []int{5, 3} {
		d.SealedRow(t, tid, month(back), "old.row")
		d.SealedPlatformRow(t, month(back), "old.row")
	}

	p := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true, retentionUsageMonths: "2", retentionAuditMonths: "1"})
	p.platformFile = boot
	testutil.Eventually(t, 20*time.Second, "the line retention applied", func() bool { return len(completeLines(p.stdout, "retention applied")) > 0 })
	if ready := completeLines(p.stdout, "retention ready"); len(ready) != 1 || ready[0]["usage_months"] != float64(2) || ready[0]["audit_months"] != float64(1) {
		t.Errorf("the line retention ready: %v", ready)
	}
	var n int
	if err := d.Super(t).QueryRow(ctxT(t), `SELECT count(*) FROM pg_class WHERE relname = $1`, partition).Scan(&n); err != nil || n != 0 {
		t.Errorf("the old partition %s is still there (%d, %v)", partition, n, err)
	}
	for _, c := range []struct{ log, sql string }{
		{"the tenant's log", `SELECT string_agg(action, ',' ORDER BY seq) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action IN ('usage.retention', 'audit.retention')`},
		{"the platform's log", `SELECT string_agg(action, ',' ORDER BY seq) FROM pandokos.platform_audit_event WHERE action IN ('usage.retention', 'audit.retention') AND $1::text <> ''`},
	} {
		var got *string
		if err := d.Super(t).QueryRow(ctxT(t), c.sql, idT).Scan(&got); err != nil || got == nil || !strings.Contains(*got, "usage.retention") || !strings.Contains(*got, "audit.retention") {
			t.Errorf("%s: %v (%v), want usage.retention and audit.retention", c.log, got, err)
		}
	}
	if code, out, errs := p.cli(t, admin, "audit", "verify", "--tenant", idT); code != 0 {
		t.Errorf("the tenant's chain after retention: exit %d %s %s", code, out, errs)
	}
	if code, out, errs := p.cli(t, boot, "platform-audit", "verify"); code != 0 {
		t.Errorf("the platform's chain after retention: exit %d %s %s", code, out, errs)
	}
	for _, back := range []int{6, 10} {
		m := month(back)
		out := p.mustCLI(t, admin, "usage", "report", "--tenant", idT, "--from", m.Format("2006-01")+"-01", "--to", m.AddDate(0, 1, 0).Format("2006-01")+"-01", "-o", "json")
		var r reportOut
		if err := json.Unmarshal([]byte(out), &r); err != nil || len(r.Rows) != 0 {
			t.Errorf("a report of the dropped month %s: %v rows (%v), want none", m.Format("2006-01"), len(r.Rows), err)
		}
	}
}

func metaHeader(key, team string) http.Header {
	h := authHeader(key)
	h.Set("X-Pandokos-Metadata-Team", team)
	return h
}
