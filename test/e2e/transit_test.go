package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/vault/transittest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// transitWorld is a double of the key service with a key and the files to reach it.
type transitWorld struct {
	s *transittest.Server
	f vaulttest.TransitFiles
}

func newTransitWorld(t *testing.T, setup vaulttest.TransitSetup) *transitWorld {
	t.Helper()
	s := transittest.New(t)
	return &transitWorld{s: s, f: vaulttest.Transit(t, s, setup)}
}

// conf is the vault.kek.transit.* of a process that reaches the double.
func (w *transitWorld) conf(change ...func(*transitOpts)) *transitOpts {
	o := &transitOpts{address: w.s.URL(), namespace: w.f.Namespace, mount: w.f.Mount, key: w.f.Key, authMethod: w.f.Method,
		tokenFile: w.f.TokenFile, appRoleMount: w.f.AppRoleMount, roleIDFile: w.f.RoleIDFile, secretIDFile: w.f.SecretIDFile, requestTimeout: "500ms"}
	for _, c := range change {
		c(o)
	}
	return o
}

// idOf is the identifier of a version of the key of the double, as the design defines it.
func (w *transitWorld) idOf(version int) string {
	m := hmac.New(sha256.New, w.s.Namespace(w.f.Namespace).HMACKey(w.f.Mount, w.f.Key, version))
	m.Write([]byte("pandokos kek id v1"))
	return fmt.Sprintf("transit-%s-v%d", hex.EncodeToString(m.Sum(nil)[:8]), version)
}

// freshDB is a migrated database that has no key-encryption key version: an installation that
// never started and whose hosts hold no key.
func freshDB(t *testing.T) *dbtest.Database {
	t.Helper()
	d := dbtest.NewMigrated(t)
	if _, err := d.Super(t).Exec(context.Background(), `DELETE FROM pandokos.kek_version`); err != nil {
		t.Fatal(err)
	}
	return d
}

// transitOptions are the options of a process on the service with the settings given.
func transitOptions(d *dbtest.Database, up *fakeprovider.Server, w *transitWorld, change ...func(*opts)) opts {
	o := opts{level: "info", up: up, db: d, noMigrate: true, kekProvider: "transit", transit: w.conf(), events: fastEvents, metricsPrometheus: true}
	for _, c := range change {
		c(&o)
	}
	return o
}

func withEnv(kv ...string) func(*opts) { return func(o *opts) { o.env = append(o.env, kv...) } }

// kekRows returns the key-encryption key version and the first byte of every wrapped data key.
func kekRows(t *testing.T, d *dbtest.Database) map[string]int {
	t.Helper()
	rows, err := d.Super(t).Query(context.Background(), `SELECT kek_version, get_byte(wrapped_key, 0) FROM pandokos.tenant_data_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var v string
		var b int
		if err := rows.Scan(&v, &b); err != nil {
			t.Fatal(err)
		}
		out[fmt.Sprintf("%s/%#x", v, b)]++
	}
	return out
}

// digest reads one value of the database as the superuser.
func digest(t *testing.T, d *dbtest.Database, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := d.Super(t).QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	credentialDigest  = `SELECT COALESCE(md5(string_agg(to_jsonb(c)::text, ',' ORDER BY c.id)), '') FROM pandokos.provider_credential c`
	destinationDigest = `SELECT COALESCE(md5(string_agg(to_jsonb(e)::text, ',' ORDER BY e.id)), '') FROM pandokos.event_destination e`
)

// linesOf returns the complete log lines of a process with the message.
func linesOf(p *proc, msg string) []map[string]any { return completeLines(p.stdout, msg) }

// waitLine waits for a log line of a process.
func waitLine(t *testing.T, p *proc, msg string, within time.Duration) map[string]any {
	t.Helper()
	var l map[string]any
	testutil.Eventually(t, within, "the line "+msg, func() bool {
		ls := linesOf(p, msg)
		if len(ls) > 0 {
			l = ls[len(ls)-1]
		}
		return l != nil
	})
	return l
}

// callsWork reports whether a call of every tenant is let through.
func callsWork(t *testing.T, p *proc, ts ...lifeTenant) bool {
	t.Helper()
	for _, x := range ts {
		if !x.ok(t, p) {
			return false
		}
	}
	return true
}

// carry gives a restarted process what the first one knew.
func carry(n, p *proc) {
	n.platformFile, n.upReady, n.credN, n.tenants = p.platformFile, true, p.credN, p.tenants
	n.ids = append(n.ids, p.ids...)
}

// TestTransitFreshInstall is T-1082, an exit test of the sprint (no key on the hosts): the binary
// with provider transit, AppRole and no key of its own, from the first start to a credential used.
func TestTransitFreshInstall(t *testing.T) {
	up := fakeprovider.New(t)
	d := freshDB(t)
	w := newTransitWorld(t, vaulttest.TransitSetup{Method: "approle"})
	p := startBinary(t, transitOptions(d, up, w))
	for _, kv := range p.env {
		if strings.HasPrefix(kv, "PANDOKOS_VAULT_KEK_LOCAL_") {
			t.Fatalf("the process was given %s: this host must hold no key", strings.SplitN(kv, "=", 2)[0])
		}
	}
	ready := linesOf(p, "key provider ready")
	if len(ready) != 1 || ready[0]["kek_provider"] != "transit" || ready[0]["auth_method"] != "approle" || ready[0]["level"] != "INFO" ||
		!strings.HasPrefix(fmt.Sprint(ready[0]["kek_version"]), "transit-") || ready[0]["oldest_version"] != float64(1) || ready[0]["newest_version"] != float64(1) {
		t.Fatalf("key provider ready: %v", ready)
	}
	vr := linesOf(p, "vault ready")
	if len(vr) != 1 || vr[0]["kek_provider"] != "transit" || vr[0]["kek_activation"] != "activated" || vr[0]["kek_version"] != ready[0]["kek_version"] || vr[0]["kek_unwrap_provider"] != "" {
		t.Fatalf("vault ready: %v", vr)
	}
	version := fmt.Sprint(vr[0]["kek_version"])
	if version != w.idOf(1) {
		t.Errorf("the active version is %s, want %s", version, w.idOf(1))
	}

	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "transit-fresh", "workload") // registers a credential and calls a model
	if rows := kekRows(t, d); len(rows) != 1 || rows[version+"/0x2"] != 1 {
		t.Errorf("the data key rows: %v, want one under %s with the first byte 0x02", rows, version)
	}
	versions := kekStates(t, p, boot)
	if versions[version] != "active" {
		t.Errorf("kek-version list: %v", versions)
	}
	list := cliJSON(t, p, boot, "kek-version", "list")
	items, _ := list["items"].([]any)
	found := false
	for _, it := range items {
		if m, _ := it.(map[string]any); m["id"] == version && m["provider"] == "transit" {
			found = true
		}
	}
	if !found {
		t.Errorf("kek-version list does not show the provider transit: %v", list)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.platform_audit_event WHERE action = 'kek.activate' AND resource_id = $1 AND detail->>'provider' = 'transit'`, version); n != 1 {
		t.Errorf("%d audit rows of the activation with provider transit, want 1", n)
	}
	exported := filepath.Join(t.TempDir(), "platform-events.ndjson")
	p.mustCLI(t, boot, "platform-event", "export", "--out", exported)
	raw, err := os.ReadFile(exported)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "io.pandokos.kek.version_activated.v1") || !strings.Contains(string(raw), `"provider":"transit"`) {
		t.Errorf("the event kek.version_activated is not in the pull export: %s", raw)
	}
	if !a.ok(t, p) {
		t.Errorf("a second call failed")
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestMoveBetweenProviders is T-1083, an exit test of the sprint (no action by tenants): from a
// key file to the service and back, with calls succeeding throughout and every sealed row
// untouched.
func TestMoveBetweenProviders(t *testing.T) {
	up := fakeprovider.New(t)
	d := dbtest.NewMigrated(t)
	keys := vaulttest.Keys(t, 2)
	d.UseKEK(t, keys[0].Version)
	w := newTransitWorld(t, vaulttest.TransitSetup{Method: "approle"})
	recv := newFaultReceiver(t, "/hook")

	// Two tenants on a key file, each with a credential and a destination.
	p0 := startBinary(t, opts{level: "info", up: up, db: d, kekFiles: keys[:1], events: fastEvents})
	boot := p0.bootstrap(t)
	a := newLifeTenant(t, p0, boot, "move-a", "workload-a")
	b := newLifeTenant(t, p0, boot, "move-b", "workload-b")
	newDestination(t, p0, a.adminFile, a.id, "hook-a", recv.url("/hook"))
	newDestination(t, p0, b.adminFile, b.id, "hook-b", recv.url("/hook"))
	if code := p0.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	credentials, destinations := digest(t, d, credentialDigest), digest(t, d, destinationDigest)
	if credentials == "" || destinations == "" {
		t.Fatal("no credential or destination to compare")
	}
	tenants := []lifeTenant{a, b}
	unwrapOld := withEnv("PANDOKOS_VAULT_KEK_LOCAL_UNWRAP_KEY_FILES=" + keys[0].File)

	// Step 1: the service wraps, the old key file only unwraps. Calls succeed at every poll.
	p1 := startBinary(t, transitOptions(d, up, w, unwrapOld))
	carry(p1, p0)
	if vr := linesOf(p1, "vault ready"); len(vr) != 1 || vr[0]["kek_provider"] != "transit" || vr[0]["kek_activation"] != "activated" || vr[0]["kek_unwrap_provider"] != "local" {
		t.Fatalf("vault ready on the service: %v", vr)
	}
	deadline := time.Now().Add(90 * time.Second)
	for kekStates(t, p1, boot)[keys[0].Version] != "retired" {
		if !callsWork(t, p1, tenants...) {
			t.Fatal("a call failed while the worker re-wrapped")
		}
		if time.Now().After(deadline) {
			t.Fatalf("the key file's version is not retired after 90 s: %v", kekStates(t, p1, boot))
		}
		time.Sleep(300 * time.Millisecond)
	}
	if l := linesOf(p1, "key rotation complete"); len(l) != 1 || l[0]["kek_version"] != keys[0].Version {
		t.Errorf("key rotation complete: %v, want one line naming %s", l, keys[0].Version)
	}
	if code := p1.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// The next start says the key file is no longer needed.
	p2 := startBinary(t, transitOptions(d, up, w, unwrapOld))
	carry(p2, p0)
	if l := linesOf(p2, "key provider no longer needed"); len(l) != 1 || l[0]["kek_provider"] != "local" {
		t.Errorf("key provider no longer needed: %v", l)
	}
	if !callsWork(t, p2, tenants...) {
		t.Error("a call failed after the key file was declared no longer needed")
	}
	if code := p2.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// Step 2: no key file configured at all.
	p3 := startBinary(t, transitOptions(d, up, w))
	carry(p3, p0)
	if !callsWork(t, p3, tenants...) {
		t.Error("a call failed with no key file configured")
	}
	if code := p3.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// Step 3: back to a new key file, the service only unwraps.
	p4 := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true, kekProvider: "local", kekFiles: keys[1:], transit: w.conf(), events: fastEvents})
	carry(p4, p0)
	if vr := linesOf(p4, "vault ready"); len(vr) != 1 || vr[0]["kek_provider"] != "local" || vr[0]["kek_version"] != keys[1].Version || vr[0]["kek_activation"] != "activated" || vr[0]["kek_unwrap_provider"] != "transit" {
		t.Fatalf("vault ready on the new key file: %v", vr)
	}
	deadline = time.Now().Add(90 * time.Second)
	for {
		states := kekStates(t, p4, boot)
		if states[w.idOf(1)] == "retired" {
			break
		}
		if !callsWork(t, p4, tenants...) {
			t.Fatal("a call failed while the worker re-wrapped to the key file")
		}
		if time.Now().After(deadline) {
			t.Fatalf("the service's version is not retired after 90 s: %v", states)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if l := linesOf(p4, "key rotation complete"); len(l) != 1 || l[0]["kek_version"] != w.idOf(1) {
		t.Errorf("key rotation complete: %v, want one line naming %s", l, w.idOf(1))
	}
	if code := p4.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// A last start without the service configured and with the double stopped.
	w.s.Stop()
	p5 := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true, kekProvider: "local", kekFiles: keys[1:], events: fastEvents})
	carry(p5, p0)
	if !callsWork(t, p5, tenants...) {
		t.Error("a call failed on the key file alone with the service gone")
	}
	if code := p5.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// Nothing of the tenants was touched: the sealed rows are the same bytes with the same times.
	if got := digest(t, d, credentialDigest); got != credentials {
		t.Errorf("a credential row changed during the moves")
	}
	if got := digest(t, d, destinationDigest); got != destinations {
		t.Errorf("a destination's secret changed during the moves")
	}

	// The old key file as the key file is refused, naming both versions.
	code, out, errs := runBinary(t, serveEnv(t, d, kekEnv+"="+keys[0].File), "serve", "--config", writeConfig(t, "info"))
	if failed := logLines(t, out, "startup failed"); code != 1 || len(failed) != 1 || !strings.Contains(out, keys[0].Version) || !strings.Contains(out, keys[1].Version) {
		t.Errorf("the old key file as key_file: exit %d, stdout %s, stderr %s; want a refusal naming %s and %s", code, out, errs, keys[0].Version, keys[1].Version)
	}
}

// TestTransitOutage is T-1084, an exit test of the sprint (serves nothing, recovers alone): the
// service away while running, a restart during the outage and its recovery.
func TestTransitOutage(t *testing.T) {
	up := fakeprovider.New(t)
	d := freshDB(t)
	w := newTransitWorld(t, vaulttest.TransitSetup{})
	short := withEnv("PANDOKOS_CACHE_CREDENTIAL_TTL=1s", "PANDOKOS_CACHE_DATA_KEY_TTL=2s")
	p := startBinary(t, transitOptions(d, up, w, short))
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "outage-a", "workload-a")
	b := newLifeTenant(t, p, boot, "outage-b", "workload-b")
	// A tenant that was never called: nothing of it is cached.
	cid, cadmin := p.newTenant(t, boot, "outage-c")
	_, ckey := p.newInferenceKey(t, cadmin, cid)
	p.prepareTenant(t, cadmin)
	// Let the caches of a and b lapse (two of the lifetimes of the data keys).
	time.Sleep(2100 * time.Millisecond)
	if !a.ok(t, p) {
		t.Fatal("a call before the outage")
	}
	creds := d.Count(t, `SELECT count(*)::int FROM pandokos.provider_credential`)

	w.s.Stop()
	// A cached tenant is served until the lifetime ends, then 503.
	if !a.ok(t, p) {
		t.Errorf("a cached tenant was refused at once")
	}
	testutil.Eventually(t, 10*time.Second, "the cached tenant to get 503", func() bool {
		status, _ := p.chatCall(t, a.inference, fakeprovider.ModelBasic, false)
		return status == 503
	})
	// A tenant that is not cached gets 503 unavailable with Retry-After, within the request timeout.
	start := time.Now()
	status, text := p.chatCall(t, ckey, fakeprovider.ModelBasic, false)
	assertPandokosError(t, "a tenant that is not cached", status, text, 503, "unavailable")
	if !strings.Contains(text, "Retry-After: 5") {
		t.Errorf("the 503 has no Retry-After: 5\n%s", text)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the refusal took %v with a request timeout of 500 ms", d)
	}
	if lines := linesOf(p, "credential cannot be opened"); len(lines) == 0 || lines[len(lines)-1]["cause"] != "kek_provider_unavailable" {
		t.Errorf("credential cannot be opened: %v, want cause kek_provider_unavailable", lines)
	}
	// A credential write gets 503 and stores nothing.
	code, out, errs := p.cli(t, cadmin, "credential", "create", "--provider", "openai", "--secret-file", testutil.WriteSecret(t, "provider-secret", "synthetic-new-credential"), "written-in-an-outage")
	if code == 0 || !strings.Contains(out+errs, "unavailable") {
		t.Errorf("a credential write during the outage: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	if got := d.Count(t, `SELECT count(*)::int FROM pandokos.provider_credential`); got != creds {
		t.Errorf("the credential count went from %d to %d: a write during the outage stored something", creds, got)
	}
	if l := linesOf(p, "credential cannot be sealed"); len(l) == 0 {
		t.Errorf("no line credential cannot be sealed")
	}
	// Readiness and the administration reads are unaffected.
	for _, addr := range []string{p.addr, p.adminAddr} {
		resp, err := p.client.Get(p.scheme + "://" + addr + "/readyz")
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("/readyz of %s during the outage: %v %v", addr, resp, err)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	p.mustCLI(t, boot, "tenant", "list")
	// M61 reads 0 once the refusal began.
	testutil.Eventually(t, 15*time.Second, "pandokos_vault_provider_up 0", func() bool {
		p.chatCall(t, ckey, fakeprovider.ModelBasic, false)
		_, scrape := p.scrapeAs(t, "GET", readKeyFile(t, boot))
		return regexp.MustCompile(`(?m)^pandokos_vault_provider_up(\{[^}]*\})? 0$`).MatchString(scrape)
	})
	if lines := linesOf(p, "key provider unavailable"); len(lines) == 0 || lines[0]["phase"] != "run" {
		t.Errorf("key provider unavailable while running: %v", lines)
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// A restart during the outage: it does not exit and listens on nothing.
	inf, adm := freeAddr(t), freeAddr(t)
	addrs := withEnv("PANDOKOS_LISTENERS_INFERENCE_ADDRESS="+inf, "PANDOKOS_LISTENERS_ADMIN_ADDRESS="+adm)
	p2 := startBinaryNoWait(t, transitOptions(d, up, w, short, addrs))
	carry(p2, p)
	testutil.Eventually(t, 15*time.Second, "key provider unavailable with phase start", func() bool {
		for _, l := range linesOf(p2, "key provider unavailable") {
			if l["phase"] == "start" {
				return true
			}
		}
		return false
	})
	select {
	case <-p2.exited:
		t.Fatalf("the process exited during the outage: code %d\n%s", p2.code, p2.stdout.String())
	case <-time.After(1500 * time.Millisecond):
	}
	for _, addr := range []string{inf, adm} {
		if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			_ = c.Close()
			t.Errorf("%s accepts connections while the process waits for the key service", addr)
		}
	}
	if len(linesOf(p2, "listening")) != 0 {
		t.Errorf("the process said it listens during the outage")
	}
	w.s.Start()
	waitLine(t, p2, "key provider recovered", 20*time.Second)
	waitLine(t, p2, "vault ready", 20*time.Second)
	p2.waitReady(t)
	if !callsWork(t, p2, a, b) {
		t.Errorf("calls fail after the service came back")
	}
	if l := linesOf(p2, "key provider recovered"); len(l) != 1 || l[0]["phase"] != "start" {
		t.Errorf("key provider recovered: %v", l)
	}
	if code := p2.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// With start_timeout the wait ends: exit 1 and startup failed.
	w.s.Stop()
	p3 := startBinaryNoWait(t, transitOptions(d, up, w, withEnv("PANDOKOS_VAULT_KEK_TRANSIT_START_TIMEOUT=3s")))
	select {
	case <-p3.exited:
		if p3.code != 1 {
			t.Errorf("exit code %d, want 1", p3.code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the process did not end after start_timeout")
	}
	if l := linesOf(p3, "startup failed"); len(l) != 1 {
		t.Errorf("startup failed: %v", l)
	}
}

// TestTransitStartRefusals is T-1085: the causes that only a person can repair end the start at once.
func TestTransitStartRefusals(t *testing.T) {
	up := fakeprovider.New(t)
	run := func(t *testing.T, d *dbtest.Database, extra ...string) (code int, out, errs string, took time.Duration) {
		t.Helper()
		start := time.Now()
		code, out, errs = runBinary(t, serveEnv(t, d, extra...), "serve", "--config", writeConfig(t, "info"))
		return code, out, errs, time.Since(start)
	}
	transitEnv := func(w *transitWorld) []string {
		env := w.conf().env()
		env = append(env, "PANDOKOS_VAULT_KEK_PROVIDER=transit", "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0", "PANDOKOS_LISTENERS_ADMIN_ADDRESS=127.0.0.1:0")
		return env
	}
	refused := func(t *testing.T, what string, d *dbtest.Database, w *transitWorld, env []string, mustName ...string) {
		t.Helper()
		code, out, errs, took := run(t, d, env...)
		testutil.AssertAbsent(t, what, out+errs, transittest.Secrets(w.s, w.f.Token, w.f.RoleID, w.f.SecretID)...)
		failed := logLines(t, out, "startup failed")
		if code != 1 || len(failed) != 1 {
			t.Errorf("%s: exit %d, stdout %s, stderr %s; want 1 and one line startup failed", what, code, out, errs)
		}
		if took > 10*time.Second {
			t.Errorf("%s: it took %v", what, took)
		}
		if len(logLines(t, out, "listening")) != 0 {
			t.Errorf("%s: the process listened", what)
		}
		for _, n := range mustName {
			if !strings.Contains(out+errs, n) {
				t.Errorf("%s: the refusal does not name %s: %s", what, n, out)
			}
		}
	}
	// plain is a world that has never started.
	plain := func(t *testing.T, setup vaulttest.TransitSetup) (*dbtest.Database, *transitWorld) {
		t.Helper()
		return freshDB(t), newTransitWorld(t, setup)
	}

	t.Run("a refused token", func(t *testing.T) {
		d, w := plain(t, vaulttest.TransitSetup{})
		w.s.Namespace("").RevokeToken(w.f.Token)
		refused(t, "a refused token", d, w, transitEnv(w))
	})
	t.Run("a missing key", func(t *testing.T) {
		d, w := plain(t, vaulttest.TransitSetup{})
		w.s.Namespace("").DeleteKey(w.f.Mount, w.f.Key)
		refused(t, "a missing key", d, w, transitEnv(w))
	})
	t.Run("a key of another type", func(t *testing.T) {
		d, w := plain(t, vaulttest.TransitSetup{KeyOptions: transittest.KeyOptions{Type: "rsa-2048"}})
		refused(t, "a key of another type", d, w, transitEnv(w))
	})
	t.Run("a service that ignores associated data", func(t *testing.T) {
		d, w := plain(t, vaulttest.TransitSetup{})
		w.s.IgnoreAssociatedData()
		refused(t, "a service that ignores associated data", d, w, transitEnv(w))
	})

	// An installation that started on the service, with a tenant, and stopped.
	started := func(t *testing.T) (*dbtest.Database, *transitWorld, string) {
		t.Helper()
		d, w := freshDB(t), newTransitWorld(t, vaulttest.TransitSetup{})
		p := startBinary(t, transitOptions(d, up, w))
		boot := p.bootstrap(t)
		newLifeTenant(t, p, boot, "refusals", "workload")
		version := w.idOf(1)
		if code := p.stop(t); code != 0 {
			t.Fatalf("exit code %d", code)
		}
		return d, w, version
	}
	t.Run("a key of the same name created again", func(t *testing.T) {
		d, w, version := started(t)
		sc := w.s.Namespace("")
		sc.DeleteKey(w.f.Mount, w.f.Key)
		sc.CreateKey(w.f.Mount, w.f.Key, transittest.KeyOptions{})
		w.s.Namespace("").RevokeToken(w.f.Token)
		w.f.Token = sc.Token(transittest.TokenOptions{Mount: w.f.Mount, Key: w.f.Key})
		writeKey(t, w.f.TokenFile, w.f.Token)
		refused(t, "a key of the same name created again", d, w, transitEnv(w), version)
	})
	t.Run("min_decryption_version above a retiring version", func(t *testing.T) {
		d, w, version := started(t)
		sc := w.s.Namespace("")
		sc.Rotate(w.f.Mount, w.f.Key)
		sc.Configure(w.f.Mount, w.f.Key, 2, 0)
		// The installation as a restart after the rotation would have left it, before the job ran.
		for _, q := range []string{
			fmt.Sprintf(`UPDATE pandokos.kek_version SET state = 'retiring' WHERE id = '%s'`, version),
			fmt.Sprintf(`INSERT INTO pandokos.kek_version (id, provider, state) VALUES ('%s', 'transit', 'active')`, w.idOf(2)),
		} {
			if _, err := d.Super(t).Exec(context.Background(), q); err != nil {
				t.Fatal(err)
			}
		}
		refused(t, "min_decryption_version above a retiring version", d, w, transitEnv(w), version)
	})
	t.Run("a transit version needed with no transit provider configured", func(t *testing.T) {
		d, w, version := started(t)
		keys := vaulttest.Keys(t, 1)
		refused(t, "a transit version without the provider", d, w, []string{kekEnv + "=" + keys[0].File, "PANDOKOS_VAULT_KEK_PROVIDER=local",
			"PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0", "PANDOKOS_LISTENERS_ADMIN_ADDRESS=127.0.0.1:0"}, version)
	})
}

// writeKey replaces the content of a secret file.
func writeKey(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// promValue returns the value of the line of the scrape for a metric that has every label.
func promValue(scrape, metric string, labels ...string) (float64, bool) {
	for _, line := range strings.Split(scrape, "\n") {
		if !strings.HasPrefix(line, metric+"{") && !strings.HasPrefix(line, metric+" ") {
			continue
		}
		ok := true
		for _, l := range labels {
			ok = ok && strings.Contains(line, l)
		}
		if !ok {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(line[strings.LastIndexByte(line, ' ')+1:], "%g", &v); err == nil {
			return v, true
		}
	}
	return 0, false
}

// tokensSeen returns the tokens the double received.
func tokensSeen(s *transittest.Server) []string {
	seen := map[string]bool{}
	for _, q := range s.Requests() {
		if tok := q.Header.Get("X-Vault-Token"); tok != "" {
			seen[tok] = true
		}
	}
	var out []string
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// TestTransitCanary is T-1086: sections 5.13 of the design with the binary. A run that registers
// credentials and destinations, calls, moves between providers and lives through an outage, an
// expired token and a refused login; then every output is searched for the secrets of the service
// and the requests the service received for anything of the tenants.
func TestTransitCanary(t *testing.T) {
	up := fakeprovider.New(t)
	d := dbtest.NewMigrated(t)
	keys := vaulttest.Keys(t, 1)
	d.UseKEK(t, keys[0].Version)
	w := newTransitWorld(t, vaulttest.TransitSetup{Method: "approle", TTL: 3 * time.Second, KeyOptions: transittest.KeyOptions{Exportable: true}})
	col := otlptest.New(t)
	recv := newFaultReceiver(t, "/hook")
	type output struct{ where, text string }
	var outs []output
	add := func(where, text string) { outs = append(outs, output{where, text}) }
	withTelemetry := func(o *opts) {
		o.level, o.otlpEndpoint, o.collector = "debug", col.URL().String(), col
	}

	// A key file, two tenants with credentials and destinations.
	p0 := startBinary(t, opts{level: "debug", up: up, db: d, kekFiles: keys, events: fastEvents, metricsPrometheus: true, otlpEndpoint: col.URL().String(), collector: col})
	boot := p0.bootstrap(t)
	a := newLifeTenant(t, p0, boot, fixtures.CanaryLabel("tenant"), fixtures.CanaryLabel("workload"))
	b := newLifeTenant(t, p0, boot, fixtures.CanaryLabel("tenant"), fixtures.CanaryLabel("workload"))
	_, secretA := newDestination(t, p0, a.adminFile, a.id, "hook-a", recv.url("/hook"))
	recv.setSecret(secretA)
	if code := p0.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	add("the log of the key file run", p0.stdout.String()+p0.stderr.String())

	// The move to the service, an outage, an expired token and a refused login.
	p := startBinary(t, transitOptions(d, up, w, withTelemetry, withEnv("PANDOKOS_VAULT_KEK_LOCAL_UNWRAP_KEY_FILES="+keys[0].File,
		"PANDOKOS_CACHE_CREDENTIAL_TTL=1s", "PANDOKOS_CACHE_DATA_KEY_TTL=2s")))
	carry(p, p0)
	var responses []string
	record := func(status int, text string) { responses = append(responses, fmt.Sprintf("%d\n%s", status, text)) }
	for i := 0; i < 5; i++ {
		record(p.chatCall(t, a.inference, fakeprovider.ModelBasic, false))
		record(p.chatCall(t, b.inference, fakeprovider.ModelBasic, false))
	}
	w.s.EchoInErrors("synthetic-echo-in-an-error")
	w.s.Stop()
	time.Sleep(2200 * time.Millisecond) // the caches lapse
	for i := 0; i < 6; i++ {
		record(p.chatCall(t, a.inference, fakeprovider.ModelBasic, false))
	}
	w.s.Start()
	// An expired token and a login that is refused: the secret ID of the file stops working.
	w.s.Advance(time.Hour)
	newSecret := w.s.Namespace("").NewSecretID(w.f.AppRoleMount, w.f.RoleID)
	for i := 0; i < 4; i++ {
		record(p.chatCall(t, b.inference, fakeprovider.ModelBasic, false))
	}
	waitLine(t, p, "key provider authentication failing", 20*time.Second)
	writeKey(t, w.f.SecretIDFile, newSecret)
	testutil.Eventually(t, 30*time.Second, "calls to work with the new secret ID", func() bool { return callsWork(t, p, a, b) })
	_, scrape := p.scrapeAs(t, "GET", readKeyFile(t, boot))
	add("the scrape", scrape)
	for i, r := range responses {
		add(fmt.Sprintf("response %d", i), r)
	}
	exportDir := t.TempDir()
	for name, args := range map[string][]string{"the event export": {"event", "export", "--tenant", a.id, "--out", filepath.Join(exportDir, "events")},
		"the platform event export": {"platform-event", "export", "--out", filepath.Join(exportDir, "platform")}} {
		file := args[len(args)-1]
		key := boot
		if args[0] == "event" {
			key = a.adminFile
		}
		p.mustCLI(t, key, args...)
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		add(name, string(raw))
	}
	checkCode, checkOut, checkErrs := runBinary(t, append(p.env, "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0"), "kek", "check")
	_ = checkCode
	add("kek check stdout", checkOut)
	add("kek check stderr", checkErrs)
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	add("server stdout", p.stdout.String())
	add("server stderr", p.stderr.String())
	for _, table := range []string{"audit_event", "platform_audit_event"} {
		add(table, digestAll(t, d, `SELECT COALESCE(jsonb_agg(to_jsonb(t))::text, '') FROM pandokos.`+table+` t`))
	}
	for _, s := range col.Spans() {
		add("a span", s.String())
	}

	secrets := transittest.Secrets(w.s, append(tokensSeen(w.s), w.f.RoleID, w.f.SecretID, newSecret, "synthetic-echo-in-an-error")...)
	for _, o := range outs {
		for _, s := range secrets {
			if len(s) >= 6 && strings.Contains(o.text, s) {
				t.Errorf("%s holds a secret of the service or a data key (%.12q...)", o.where, s)
				break
			}
		}
	}
	// The service saw nothing of the tenants.
	forbidden := append([]string{a.id, b.id, a.name, b.name, a.saName, b.saName, a.keyID, b.keyID}, fixtures.Canaries()...)
	forbidden = append(forbidden, p.ids...)
	for _, q := range w.s.Requests() {
		text := q.Path + "\n" + fmt.Sprint(q.Header) + "\n" + string(q.Body)
		for _, f := range forbidden {
			if f != "" && strings.Contains(text, f) {
				t.Errorf("a request to the key service holds %.20q", f)
			}
		}
	}
	if w.s.Count(transittest.OpUnwrap) == 0 || w.s.Count(transittest.OpWrap) == 0 {
		t.Errorf("the run did not use the service: %d wraps, %d unwraps", w.s.Count(transittest.OpWrap), w.s.Count(transittest.OpUnwrap))
	}
}

// digestAll reads a text value that may be long.
func digestAll(t *testing.T, d *dbtest.Database, sql string) string { return digest(t, d, sql) }

// TestTransitRotation is T-1087: a rotation in the service is the operator's act and takes effect
// at a restart.
func TestTransitRotation(t *testing.T) {
	up := fakeprovider.New(t)
	d := freshDB(t)
	w := newTransitWorld(t, vaulttest.TransitSetup{})
	p1 := startBinary(t, transitOptions(d, up, w))
	boot := p1.bootstrap(t)
	a := newLifeTenant(t, p1, boot, "rotation-a", "workload-a")
	v1, v2 := w.idOf(1), ""
	// A rotation while the binary runs changes nothing: a new tenant's data key is under the active version.
	w.s.Namespace("").Rotate(w.f.Mount, w.f.Key)
	v2 = w.idOf(2)
	b := newLifeTenant(t, p1, boot, "rotation-b", "workload-b")
	rows := kekRows(t, d)
	if len(rows) != 1 || rows[v1+"/0x2"] != 2 {
		t.Fatalf("the data keys after a rotation in the service: %v, want both under %s", rows, v1)
	}
	if states := kekStates(t, p1, boot); len(states) != 1 || states[v1] != "active" {
		t.Fatalf("the versions after a rotation in the service: %v", states)
	}

	// A restart of another instance activates the new version; the first, not restarted, goes on.
	p2 := startBinary(t, transitOptions(d, up, w))
	carry(p2, p1)
	if vr := linesOf(p2, "vault ready"); len(vr) != 1 || vr[0]["kek_version"] != v2 || vr[0]["kek_activation"] != "activated" {
		t.Fatalf("vault ready after the restart: %v", vr)
	}
	if ready := linesOf(p2, "key provider ready"); len(ready) != 1 || ready[0]["oldest_version"] != float64(1) || ready[0]["newest_version"] != float64(2) {
		t.Errorf("key provider ready: %v", ready)
	}
	// The instance that was not restarted still opens credentials.
	if !callsWork(t, p1, a, b) {
		t.Errorf("an instance that was not restarted stopped serving")
	}
	// And answers 503 with kek_replaced for a new tenant's first secret.
	cid, cadmin := p2.newTenant(t, boot, "rotation-c")
	code, out, errs := p1.cli(t, cadmin, "credential", "create", "--provider", "openai", "--secret-file", testutil.WriteSecret(t, "provider-secret", "synthetic-first-secret"), "first")
	if code == 0 || !strings.Contains(out+errs, "unavailable") {
		t.Errorf("a first secret through the instance that was not restarted: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	if l := linesOf(p1, "credential cannot be sealed"); len(l) == 0 || l[len(l)-1]["cause"] != "kek_replaced" {
		t.Errorf("credential cannot be sealed: %v, want cause kek_replaced", l)
	}
	_ = cid
	if code := p1.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	// The worker of the restarted instance re-wraps and retires the old version.
	testutil.Eventually(t, 90*time.Second, "the old version to be retired", func() bool { return kekStates(t, p2, boot)[v1] == "retired" })
	if l := linesOf(p2, "key rotation complete"); len(l) != 1 || l[0]["kek_version"] != v1 {
		t.Errorf("key rotation complete: %v", l)
	}
	if rows := kekRows(t, d); rows[v2+"/0x2"] != 2 {
		t.Errorf("the data keys after the re-wrap: %v, want both under %s", rows, v2)
	}
	if code := p2.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// min_decryption_version above the retired version does not disturb a start.
	w.s.Namespace("").Configure(w.f.Mount, w.f.Key, 2, 0)
	p3 := startBinary(t, transitOptions(d, up, w))
	carry(p3, p1)
	if ready := linesOf(p3, "key provider ready"); len(ready) != 1 || ready[0]["oldest_version"] != float64(2) {
		t.Errorf("key provider ready: %v, want oldest_version 2", ready)
	}
	if !callsWork(t, p3, a, b) {
		t.Errorf("calls fail after min_decryption_version was raised above the retired version")
	}
	if code := p3.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestTransitTokenLifetime is T-1088: tokens that live a few seconds never fail a call.
func TestTransitTokenLifetime(t *testing.T) {
	for name, setup := range map[string]vaulttest.TransitSetup{
		"AppRole tokens of 3 s":  {Method: "approle", TTL: 3 * time.Second},
		"a renewable token file": {Method: "token", TTL: 3 * time.Second, Renewable: true},
		"AppRole with a maximum": {Method: "approle", TTL: 3 * time.Second, MaxTTL: 8 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			up := fakeprovider.New(t)
			d := freshDB(t)
			w := newTransitWorld(t, setup)
			p := startBinary(t, transitOptions(d, up, w, withEnv("PANDOKOS_CACHE_CREDENTIAL_TTL=1s", "PANDOKOS_CACHE_DATA_KEY_TTL=1s")))
			boot := p.bootstrap(t)
			var ts []lifeTenant
			for i := 0; i < 3; i++ {
				ts = append(ts, newLifeTenant(t, p, boot, fmt.Sprintf("lifetime-%d", i), fmt.Sprintf("workload-%d", i)))
			}
			deadline := time.Now().Add(12 * time.Second)
			calls := 0
			for time.Now().Before(deadline) {
				for _, x := range ts {
					status, text := p.chatCall(t, x.inference, fakeprovider.ModelBasic, false)
					calls++
					if status != 200 {
						t.Fatalf("a call of %s failed after %d calls: %d\n%s", x.name, calls, status, text)
					}
				}
				time.Sleep(150 * time.Millisecond)
			}
			if n := w.s.Count(transittest.OpRenew) + w.s.Count(transittest.OpLogin); n < 2 {
				t.Errorf("the service saw %d renewals and logins in 12 s of tokens of 3 s, want at least 2", n)
			}
			if code := p.stop(t); code != 0 {
				t.Fatalf("exit code %d", code)
			}
		})
	}
}

// TestTransitTelemetry is T-1089: M59 to M62 and the seven lines of the key service.
func TestTransitTelemetry(t *testing.T) {
	up := fakeprovider.New(t)
	short := withEnv("PANDOKOS_CACHE_CREDENTIAL_TTL=1s", "PANDOKOS_CACHE_DATA_KEY_TTL=1s")

	// A token that cannot be renewed, a weak key, traffic and an outage.
	d := freshDB(t)
	w := newTransitWorld(t, vaulttest.TransitSetup{Method: "token", TTL: 30 * time.Second, KeyOptions: transittest.KeyOptions{Exportable: true}})
	p := startBinary(t, transitOptions(d, up, w, short, withEnv("PANDOKOS_LOG_LEVEL=debug"), func(o *opts) { o.level = "debug" }))
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "telemetry-a", "workload-a")
	b := newLifeTenant(t, p, boot, "telemetry-b", "workload-b")
	for i := 0; i < 5; i++ {
		callsWork(t, p, a, b)
		time.Sleep(1100 * time.Millisecond)
	}
	adminKey := readKeyFile(t, boot)
	// The counters agree with what the service counted, operation by operation, once nothing is in flight.
	ops := map[string]string{"wrap": transittest.OpWrap, "unwrap": transittest.OpUnwrap, "describe": transittest.OpDescribe, "identify": transittest.OpIdentify, "lookup": transittest.OpLookup}
	testutil.Eventually(t, 10*time.Second, "M59 to agree with the requests the service counted", func() bool {
		_, scrape := p.scrapeAs(t, "GET", adminKey)
		for op, name := range ops {
			got, ok := promValue(scrape, "pandokos_vault_provider_requests_total", `pandokos_kek_operation="`+op+`"`, `pandokos_kek_outcome="ok"`)
			if !ok || int(got) != w.s.Count(name) {
				return false
			}
		}
		return true
	})
	_, scrape := p.scrapeAs(t, "GET", adminKey)
	if v, ok := promValue(scrape, "pandokos_vault_provider_up"); !ok || v != 1 {
		t.Errorf("M61 = %v, %v while the service answers", v, ok)
	}
	if v, ok := promValue(scrape, "pandokos_vault_provider_token_ttl_seconds"); !ok || v <= 0 || v > 30 {
		t.Errorf("M62 = %v, %v for a token of 30 s", v, ok)
	}
	if v, ok := promValue(scrape, "pandokos_vault_provider_request_duration_seconds_count", `pandokos_kek_operation="unwrap"`); !ok || v < 1 {
		t.Errorf("M60 for unwrap: %v, %v", v, ok)
	}
	// The outage: five failures start the refusal, the gauge reads 0, and the outcomes are counted.
	w.s.Stop()
	testutil.Eventually(t, 20*time.Second, "M61 to read 0", func() bool {
		callsWork(t, p, a, b)
		_, s := p.scrapeAs(t, "GET", adminKey)
		v, ok := promValue(s, "pandokos_vault_provider_up")
		return ok && v == 0
	})
	_, scrape = p.scrapeAs(t, "GET", adminKey)
	if v, ok := promValue(scrape, "pandokos_vault_provider_requests_total", `pandokos_kek_outcome="unavailable"`); !ok || v < 5 {
		t.Errorf("unavailable outcomes: %v, %v, want at least 5", v, ok)
	}
	if v, ok := promValue(scrape, "pandokos_vault_provider_requests_total", `pandokos_kek_outcome="refused"`); !ok || v < 1 {
		t.Errorf("refused outcomes: %v, %v", v, ok)
	}
	w.s.Start()
	// The token cannot be renewed: it is said to be expiring at two thirds of its life.
	waitLine(t, p, "key provider token expiring", 30*time.Second)
	testutil.Eventually(t, 30*time.Second, "calls to work again", func() bool { return callsWork(t, p, a, b) })
	waitLine(t, p, "key provider recovered", 10*time.Second)
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// A refused login.
	d2 := freshDB(t)
	w2 := newTransitWorld(t, vaulttest.TransitSetup{Method: "approle", TTL: 3 * time.Second})
	p2 := startBinary(t, transitOptions(d2, up, w2, short, func(o *opts) { o.level = "debug" }))
	boot2 := p2.bootstrap(t)
	c := newLifeTenant(t, p2, boot2, "telemetry-c", "workload-c")
	w2.s.Namespace("").NewSecretID(w2.f.AppRoleMount, w2.f.RoleID)
	w2.s.Advance(time.Hour)
	for i := 0; i < 3; i++ {
		c.ok(t, p2)
	}
	waitLine(t, p2, "key provider authentication failing", 20*time.Second)
	testutil.Eventually(t, 10*time.Second, "the refused login to be counted", func() bool {
		_, s := p2.scrapeAs(t, "GET", readKeyFile(t, boot2))
		v, ok := promValue(s, "pandokos_vault_provider_requests_total", `pandokos_kek_operation="login"`, `pandokos_kek_outcome="rejected"`)
		return ok && v >= 1
	})
	if code := p2.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// A provider that only unwraps and owns nothing.
	d3 := freshDB(t)
	w3 := newTransitWorld(t, vaulttest.TransitSetup{})
	keys := vaulttest.Keys(t, 1)
	p3 := startBinary(t, opts{level: "debug", up: up, db: d3, noMigrate: true, kekProvider: "local", kekFiles: keys, transit: w3.conf(), events: fastEvents})
	waitLine(t, p3, "key provider no longer needed", 10*time.Second)
	if code := p3.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// Every line of the design was written, with exactly its attributes.
	want := map[string][]string{
		"key provider ready":                  {"kek_provider", "kek_version", "auth_method", "oldest_version", "newest_version"},
		"key provider configuration weak":     {"kek_provider", "setting"},
		"key provider unavailable":            {"kek_provider", "phase", "operation", "error_class", "failures"},
		"key provider recovered":              {"kek_provider", "phase", "outage_seconds"},
		"key provider token expiring":         {"kek_provider", "auth_method", "expires_in_seconds"},
		"key provider authentication failing": {"kek_provider", "auth_method", "error_class", "failures"},
		"key provider no longer needed":       {"kek_provider"},
	}
	for msg, attrs := range want {
		sort.Strings(attrs)
		var lines []map[string]any
		for _, pr := range []*proc{p, p2, p3} {
			lines = append(lines, linesOf(pr, msg)...)
		}
		if len(lines) == 0 {
			t.Errorf("the line %q was never written", msg)
			continue
		}
		for _, l := range lines {
			var got []string
			for k := range l {
				if k != "time" && k != "level" && k != "msg" {
					got = append(got, k)
				}
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(attrs, ",") {
				t.Errorf("%q has the attributes %v, want %v", msg, got, attrs)
				break
			}
		}
	}
}
