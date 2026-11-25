package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// TestDeletionEndToEnd is T-1005: a tenant is exported, deleted through the command line with no grace
// period, and the worker removes it; its keys stop working, the record verifies, a tampered kept log
// does not, another tenant is untouched throughout, and the name can be used again.
func TestDeletionEndToEnd(t *testing.T) {
	t.Parallel()
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up, gracePeriod: "0s", deletionInterval: "10s"})
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "doomed", "workload-a")
	b := newLifeTenant(t, p, boot, "bystander", "workload-b")
	aID := a.tenantID(t)

	// The tenant exports its data before the request.
	export := filepath.Join(t.TempDir(), "export")
	p.mustCLI(t, a.adminFile, "tenant-export", "--tenant", a.id, "--out", export)
	if _, err := os.Stat(filepath.Join(export, "manifest.json")); err != nil {
		t.Fatalf("no manifest in the export: %v", err)
	}
	bAudit := func() string { return p.mustCLI(t, b.adminFile, "audit", "verify", "--tenant", b.id) }
	if out := bAudit(); !strings.HasPrefix(out, "verified ") {
		t.Fatalf("the log of the other tenant before the deletion: %s", out)
	}

	// The request; with the grace period 0 the worker starts at its next tick.
	out := p.mustCLI(t, boot, "tenant", "delete", a.id, "--reason", "end of contract", "--confirm-name", a.name)
	if !strings.Contains(out, "scheduled") {
		t.Errorf("tenant delete printed:\n%s", out)
	}
	for _, x := range []struct{ args []string }{{[]string{"tenant", "delete", a.id, "--reason", "again", "--confirm-name", "not-the-name"}}} {
		if code, _, errs := p.cli(t, boot, x.args...); code == 0 || !strings.Contains(errs, "confirm_name") {
			t.Errorf("a wrong confirmation: exit %d, stderr %q", code, errs)
		}
	}
	waitDeleted(t, p.db, aID)
	// B is in use the whole time.
	if !b.ok(t, p) {
		t.Error("the other tenant's calls fail during the deletion")
	}

	// A's keys end on both listeners.
	if status, text := p.chatCall(t, a.inference, fakeprovider.ModelBasic, false); status != 401 {
		t.Errorf("the inference key of the deleted tenant: %d\n%s", status, text)
	}
	if status := statusOf(t, p, "/v1/me", a.adminKey); status != 401 {
		t.Errorf("the administrator's key of the deleted tenant on the administration listener: %d, want 401", status)
	}

	// The deletion, as the platform reads it.
	d := cliJSON(t, p, boot, "tenant", "deletion", "get", a.id)
	if d["state"] != "completed" || d["step"] != "done" || d["reason"] != "end of contract" || d["completed_at"] == nil {
		t.Errorf("tenant deletion get: %v", d)
	}
	for _, k := range []string{"requested_by", "request_id", "requested_at", "not_before", "started_at", "passes", "removed", "kek_versions", "backup_erasure", "audit", "completion_seq"} {
		if d[k] == nil {
			t.Errorf("tenant deletion get has no %s: %v", k, d)
		}
	}
	if _, has := d["salt"]; has {
		t.Error("tenant deletion get shows a salt")
	}
	list := cliJSON(t, p, boot, "tenant", "deletion", "list", "--state", "completed")
	if items, _ := list["items"].([]any); len(items) != 1 {
		t.Errorf("tenant deletion list: %v", list)
	}
	verified := regexp.MustCompile(`^deletion of ` + a.id + ` verified: record at seq \d+, \d+ audit rows kept, head [0-9a-f]{64}$`)
	if code, out, errs := p.cli(t, boot, "tenant", "deletion", "verify", a.id); code != 0 || !verified.MatchString(strings.TrimSpace(out)) {
		t.Errorf("tenant deletion verify: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	if code, out, errs := p.cli(t, boot, "platform-audit", "verify"); code != 0 {
		t.Errorf("platform-audit verify: exit %d\n%s\n%s", code, out, errs)
	}
	head := cliJSON(t, p, boot, "tenant", "deletion", "audit-head", a.id)
	if auditField, _ := d["audit"].(map[string]any); head["hash"] != auditField["resealed_head"] {
		t.Errorf("the head of the kept log %v is not the resealed head %v", head["hash"], auditField["resealed_head"])
	}

	// One kept row is changed by whoever can write the table: the chain breaks, and the command says so.
	p.db.Tamper(t, `UPDATE pandokos.audit_event SET action = 'tampered.row' WHERE tenant_id = $1::uuid AND seq = (SELECT min(seq) FROM pandokos.audit_event WHERE tenant_id = $1::uuid)`, a.id)
	code, out, errs := p.cli(t, boot, "tenant", "deletion", "verify", a.id)
	if code != 1 || !strings.Contains(out+errs, "tenant_chain_break") {
		t.Errorf("tenant deletion verify after the change: exit %d\nstdout: %s\nstderr: %s, want exit 1 and tenant_chain_break", code, out, errs)
	}

	// The other tenant is unaffected: its calls, its report and its log.
	if !b.ok(t, p) {
		t.Error("the other tenant's calls fail after the deletion")
	}
	if out := bAudit(); !strings.HasPrefix(out, "verified ") {
		t.Errorf("the log of the other tenant after the deletion: %s", out)
	}
	if out := p.mustCLI(t, b.adminFile, "usage", "report", "--tenant", b.id, "--month", time.Now().UTC().Format("2006-01")); out == "" {
		t.Error("the usage report of the other tenant is empty")
	}
	// The name is free again.
	newFile := filepath.Join(t.TempDir(), "again.key")
	if out := p.mustCLI(t, boot, "tenant", "create", "--admin-key-out", newFile, a.name); !strings.Contains(out, a.name) {
		t.Errorf("tenant create with the name of the deleted tenant:\n%s", out)
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestLifecycleOutputsHoldNoTenantData is T-1011: over a deletion and a rotation of the key, the process
// log and the scrape hold no tenant ID or name, no key ID, no version of a key outside the two lines that
// are meant to say it, none of the canaries; and the kept log, exported, holds none of the tenant's
// original identifiers.
func TestLifecycleOutputsHoldNoTenantData(t *testing.T) {
	t.Parallel()
	up := fakeprovider.New(t)
	d := dbtest.NewMigrated(t)
	keys := vaulttest.Keys(t, 2)
	d.UseKEK(t, keys[0].Version)
	name, saName := fixtures.CanaryLabel("tenant"), fixtures.CanaryLabel("workload")
	p := startBinary(t, opts{level: "debug", up: up, db: d, kekFiles: keys[:1], gracePeriod: "0s", deletionInterval: "10s", metricsPrometheus: true})
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, name, saName)
	b := newLifeTenant(t, p, boot, "bystander", "workload-b")
	aKeyID := a.keyID

	// The deletion.
	p.mustCLI(t, boot, "tenant", "delete", a.id, "--reason", "closing", "--confirm-name", a.name)
	waitDeleted(t, d, a.tenantID(t))
	auditExport := filepath.Join(t.TempDir(), "kept.ndjson")
	p.mustCLI(t, boot, "tenant", "deletion", "audit-export", a.id, "--out", auditExport)
	kept, err := os.ReadFile(auditExport)
	if err != nil {
		t.Fatal(err)
	}
	// The rotation.
	_ = p.stop(t)
	p2 := restart(t, p, opts{level: "debug", up: up, kekFiles: []vaulttest.Key{keys[1], keys[0]}, metricsPrometheus: true, gracePeriod: "0s", deletionInterval: "10s"})
	_ = b
	testutil.Eventually(t, 90*time.Second, "the old version to be retired", func() bool { return kekStates(t, p2, boot)[keys[0].Version] == "retired" })
	status, scrape := p2.scrapeAs(t, "GET", readKeyFile(t, boot))
	if status != 200 {
		t.Fatalf("the scrape: %d", status)
	}
	if code := p2.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	logs := p.stdout.String() + "\n" + p2.stdout.String()
	logOutsideVaultLines := func(text string) []string {
		var out []string
		for _, line := range strings.Split(text, "\n") {
			var obj map[string]any
			if json.Unmarshal([]byte(line), &obj) != nil {
				continue
			}
			if msg := obj["msg"]; msg != "vault ready" && msg != "key rotation complete" {
				out = append(out, line)
			}
		}
		return out
	}
	subjects := map[string]string{"the tenant ID": a.id, "the tenant name": a.name, "the name of its service account": a.saName, "a key ID": aKeyID, "the platform's request": a.inference}
	for what, v := range subjects {
		if v == "" {
			continue
		}
		for _, line := range logOutsideVaultLines(logs) {
			if strings.Contains(line, v) {
				t.Errorf("a log line holds %s: %.300s", what, line)
				break
			}
		}
		if strings.Contains(scrape, v) {
			t.Errorf("the scrape holds %s", what)
		}
	}
	for _, v := range keys {
		for _, line := range logOutsideVaultLines(logs) {
			if strings.Contains(line, v.Version) {
				t.Errorf("a log line other than vault ready and key rotation complete holds a key version: %.300s", line)
				break
			}
		}
		if strings.Contains(scrape, v.Version) {
			t.Errorf("the scrape holds the key version %s", v.Version)
		}
		if strings.Contains(logs, v.Secret.Reveal()) || strings.Contains(scrape, v.Secret.Reveal()) {
			t.Error("an output holds a key of the vault")
		}
	}
	for _, c := range fixtures.Canaries() {
		if strings.Contains(logs, c) || strings.Contains(scrape, c) || strings.Contains(string(kept), c) {
			t.Errorf("an output holds the canary %q", c)
		}
	}
	// The fields of the lines that are meant to say it are there.
	if !strings.Contains(logs, `"msg":"key rotation complete"`) {
		t.Error("no key rotation complete line")
	}
	// The kept log holds none of the original identifiers.
	for what, v := range map[string]string{"the tenant's name": a.name, "its service account's name": a.saName, "its key ID": aKeyID, "its inference key": a.inference, "the e-mail of an actor": "@example"} {
		if v != "" && strings.Contains(string(kept), v) {
			t.Errorf("the kept log holds %s", what)
		}
	}
	if len(kept) == 0 || !strings.Contains(string(kept), "tenant.delete") {
		t.Error("the kept log is empty or does not end with the closing row")
	}
}
