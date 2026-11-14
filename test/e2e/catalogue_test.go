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
	"github.com/pandokos/pandokos/test/testutil"
)

func priceFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "test", "fixtures", "prices", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFixtureFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func count(t *testing.T, d *dbtest.Database, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Super(t).QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	return n
}

// TestCatalogue is T-410 (exit condition 3, end to end): serve imports the embedded snapshot from an
// empty database; the CLI makes a provider, a model and a grant; a file import that changes one
// price adds one version and its audit rows; a restart skips the import it must not repeat.
func TestCatalogue(t *testing.T) {
	up := fakeprovider.New(t)
	d := dbtest.New(t)
	importAtStart := []string{"PANDOKOS_PRICING_IMPORT_AT_START=true"}

	p := startBinary(t, opts{level: "info", up: up, db: d, env: importAtStart})
	var imported []map[string]any
	testutil.Eventually(t, 10*time.Second, "the price import of the first start", func() bool {
		imported = completeLines(p.stdout, "prices imported")
		return len(imported) > 0
	})
	if len(imported) != 1 || imported[0]["source"] != "embedded" || imported[0]["level"] != "INFO" || imported[0]["created"] != float64(17) || imported[0]["import_id"] == "" {
		t.Fatalf("`prices imported` lines: %v", imported)
	}
	if n := count(t, d, `SELECT count(*) FROM pandokos.price_version WHERE source = 'import'`); n != 17 {
		t.Errorf("%d versions after the first start, want 17", n)
	}
	if skipped := completeLines(p.stdout, "price import skipped"); len(skipped) != 0 {
		t.Errorf("an import was skipped on an empty database: %v", skipped)
	}
	testutil.AssertAbsent(t, "stdout", p.stdout.String(), d.Passwords()...)

	platform := p.bootstrap(t)

	// The CLI makes a provider with a model and grants it to a tenant; the tenant sees it, and its
	// administrator can read the models it may use, with their prices.
	p.mustCLI(t, platform, "provider", "create", "--family", "openai", "--dataset-provider", "synthetic", "--display-name", "Synthetic", "synthetic")
	p.mustCLI(t, platform, "endpoint", "create", "--base-url", "https://synthetic.invalid", "synthetic", "main")
	out := p.mustCLI(t, platform, "model", "create", "--capability", "text", "--context-window", "8192", "synthetic/synthetic-chat")
	if !strings.Contains(out, "ref: synthetic/synthetic-chat") || !strings.Contains(out, "availability: selected") {
		t.Errorf("model create:\n%s", out)
	}
	p.mustCLI(t, platform, "model", "create", "--availability", "all", "synthetic/synthetic-tiered")
	tenantID, adminFile := p.newTenant(t, platform, "acme")
	other, otherAdmin := p.newTenant(t, platform, "other")
	p.mustCLI(t, platform, "model", "grant", "--tenant", tenantID, "synthetic/synthetic-chat")
	refs := func(adminFile, id string) map[string]bool {
		t.Helper()
		var list struct {
			Items []struct {
				Model struct {
					Ref string `json:"ref"`
				} `json:"model"`
				Allowed bool `json:"allowed"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(p.mustCLI(t, adminFile, "tenant-model", "list", "--tenant", id, "--all", "--limit", "100", "-o", "json")), &list); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, it := range list.Items {
			out[it.Model.Ref] = it.Allowed
		}
		return out
	}
	if m := refs(adminFile, tenantID); !m["synthetic/synthetic-chat"] || !m["synthetic/synthetic-tiered"] || !m["openai/gpt-4o"] {
		t.Errorf("the models of the tenant it was granted to: %v", m)
	}
	if m := refs(otherAdmin, other); m["synthetic/synthetic-chat"] || !m["synthetic/synthetic-tiered"] {
		t.Errorf("the models of the other tenant: %v", m)
	}
	// A restriction by the tenant, and a key with a list.
	p.mustCLI(t, adminFile, "model-restriction", "set", "--tenant", tenantID, "--model", "openai/gpt-4o")
	if m := refs(adminFile, tenantID); !m["openai/gpt-4o"] || m["synthetic/synthetic-chat"] {
		t.Errorf("the models of a restricted tenant: %v", m)
	}
	p.mustCLI(t, adminFile, "model-restriction", "clear", "--tenant", tenantID)

	// A price list from a file: the CLI is a host command, run against the same database.
	baseFile := writeFixtureFile(t, "base.json", priceFixture(t, "base.json"))
	changedFile := writeFixtureFile(t, "changed.json", priceFixture(t, "changed.json"))
	code, out, errs := runBinary(t, p.env, "import-prices", "--file", baseFile)
	if code != 0 || errs != "" {
		t.Fatalf("import-prices base: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	// base.json prices gpt-4o and o4-mini (which the snapshot priced differently) and synthetic-chat and synthetic-tiered.
	for _, line := range []string{"models_seen: 19", "created: 4", "unchanged: 0", "overridden: 0", "skipped: 0"} {
		if !regexp.MustCompile(`(?m)^` + line + `$`).MatchString(out) {
			t.Errorf("import-prices lacks %q:\n%s", line, out)
		}
	}
	if !strings.Contains(out, "synthetic/synthetic-chat") || !strings.Contains(out, "created") {
		t.Errorf("import-prices names no item:\n%s", out)
	}
	before := count(t, d, `SELECT count(*) FROM pandokos.price_version`)

	// A dry run reports and writes nothing.
	code, out, errs = runBinary(t, p.env, "import-prices", "--file", changedFile, "--dry-run")
	if code != 0 || errs != "" || !regexp.MustCompile(`(?m)^created: 1$`).MatchString(out) {
		t.Fatalf("a dry run: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	if n := count(t, d, `SELECT count(*) FROM pandokos.price_version`); n != before {
		t.Errorf("a dry run wrote versions: %d, was %d", n, before)
	}

	runsBefore := count(t, d, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'price_import.run'`)
	createsBefore := count(t, d, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'price.create'`)
	code, out, errs = runBinary(t, p.env, "import-prices", "--file", changedFile)
	if code != 0 || errs != "" {
		t.Fatalf("import-prices changed: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	if !regexp.MustCompile(`(?m)^created: 1$`).MatchString(out) || !regexp.MustCompile(`(?m)^unchanged: 3$`).MatchString(out) {
		t.Errorf("one price changed:\n%s", out)
	}
	if n := count(t, d, `SELECT count(*) FROM pandokos.price_version`); n != before+1 {
		t.Errorf("%d versions after the change, want %d", n, before+1)
	}
	if n := count(t, d, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'price.create'`); n != createsBefore+1 {
		t.Errorf("%d price.create rows, want %d", n, createsBefore+1)
	}
	if n := count(t, d, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'price_import.run'`); n != runsBefore+1 {
		t.Errorf("%d price_import.run rows, want %d", n, runsBefore+1)
	}
	var actorType, actorName, source, previous string
	if err := d.Super(t).QueryRow(t.Context(), `SELECT actor_type, actor_name, detail ->> 'source', detail::text FROM pandokos.platform_audit_event WHERE action = 'price.create' ORDER BY id DESC LIMIT 1`).
		Scan(&actorType, &actorName, &source, &previous); err != nil {
		t.Fatal(err)
	}
	if actorType != "host" || actorName != "import-prices" || source != "import" {
		t.Errorf("the audit row of the change: %s %s %s", actorType, actorName, source)
	}
	var oldCount int
	if err := d.Super(t).QueryRow(t.Context(), `SELECT count(*) FROM pandokos.price_version v JOIN pandokos.model m ON m.id = v.model_id WHERE m.name = 'gpt-4o'`).Scan(&oldCount); err != nil || oldCount != 3 {
		t.Errorf("gpt-4o has %d versions (snapshot, base, changed): %v", oldCount, err)
	}
	// The same file again changes nothing.
	code, out, _ = runBinary(t, p.env, "import-prices", "--file", changedFile)
	if code != 0 || !regexp.MustCompile(`(?m)^created: 0$`).MatchString(out) {
		t.Errorf("a second import of the same file: exit %d\n%s", code, out)
	}
	// The embedded snapshot is a source too.
	if code, out, errs := runBinary(t, p.env, "import-prices", "--embedded", "--dry-run"); code != 0 || errs != "" || !strings.Contains(out, "created:") {
		t.Errorf("--embedded: exit %d\n%s\n%s", code, out, errs)
	}
	if code, _, errs := runBinary(t, p.env, "import-prices", "--file", filepath.Join(t.TempDir(), "missing.json")); code != 1 || errs == "" {
		t.Errorf("a file that is not there: exit %d %q", code, errs)
	}
	if code, _, errs := runBinary(t, p.env, "import-prices", "--file", writeFixtureFile(t, "invalid.json", priceFixture(t, "invalid.json"))); code != 1 || errs == "" {
		t.Errorf("a document that is not a dataset: exit %d %q", code, errs)
	}
	if code, _, _ := runBinary(t, p.env, "import-prices"); code != 2 {
		t.Errorf("no source: exit %d", code)
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// A restart: a file was imported after the snapshot was made, so the snapshot does not go over it.
	p2 := startBinary(t, opts{level: "info", up: up, db: d, env: importAtStart})
	var skipped []map[string]any
	testutil.Eventually(t, 10*time.Second, "the skipped import of the restart", func() bool {
		skipped = completeLines(p2.stdout, "price import skipped")
		return len(skipped) > 0
	})
	if len(skipped) != 1 || skipped[0]["reason"] != "newer_import" || skipped[0]["source"] != "embedded" {
		t.Errorf("`price import skipped` lines: %v", skipped)
	}
	if got := completeLines(p2.stdout, "prices imported"); len(got) != 0 {
		t.Errorf("the restart imported: %v", got)
	}
	if n := count(t, d, `SELECT count(*) FROM pandokos.price_version`); n != before+1 {
		t.Errorf("the restart changed the versions: %d", n)
	}
	if code := p2.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// TestPriceRefresh is T-411: the periodic download, against a local TLS server whose certificate
// the child trusts through SSL_CERT_FILE; a failing server is logged with its class and the
// process stays ready.
func TestPriceRefresh(t *testing.T) {
	up := fakeprovider.New(t)
	ca := testutil.SSLCertFile(t)
	// The download is a public one, made by the egress guard: it refuses a loopback address, so
	// the test reaches its local server by name, through a proxy of the operator's kind
	// (upstream.proxy.url): the proxy resolves the name.
	refreshEnv := func(t *testing.T, url string) []string {
		proxy, _ := testutil.ConnectProxy(t)
		url = strings.Replace(url, "127.0.0.1", "localhost", 1)
		return []string{
			"PANDOKOS_UPSTREAM_PROXY_URL=" + proxy.String(),
			"PANDOKOS_PRICING_REFRESH_ENABLED=true",
			"PANDOKOS_PRICING_REFRESH_URL=" + url,
			"PANDOKOS_PRICING_REFRESH_INTERVAL=1h",
			"PANDOKOS_TEST_REFRESH_DELAY=1s", // a test hook of serve: only the first delay is shortened
			"SSL_CERT_FILE=" + ca,
		}
	}

	t.Run("one remote import", func(t *testing.T) {
		url := testutil.PriceServer(t, priceFixture(t, "changed.json"), testutil.PriceOK)
		d := dbtest.New(t)
		p := startBinary(t, opts{level: "info", up: up, db: d, env: refreshEnv(t, url)})
		var imported []map[string]any
		testutil.Eventually(t, 15*time.Second, "the first refresh", func() bool {
			imported = completeLines(p.stdout, "prices imported")
			return len(imported) > 0
		})
		if len(imported) != 1 || imported[0]["source"] != "remote" || imported[0]["created"] != float64(2) || imported[0]["unmatched"] != float64(15) {
			t.Fatalf("`prices imported` lines: %v\n%s", imported, p.stdout.String())
		}
		if n := count(t, d, `SELECT count(*) FROM pandokos.price_import WHERE source = 'remote' AND outcome = 'success'`); n != 1 {
			t.Errorf("%d remote imports", n)
		}
		var ref string
		if err := d.Super(t).QueryRow(t.Context(), `SELECT source_ref FROM pandokos.price_import WHERE source = 'remote'`).Scan(&ref); err != nil || ref == "" {
			t.Errorf("the source of the import: %q %v", ref, err)
		}
		// Not again within the interval: one run per start of the interval, however many instances.
		time.Sleep(2 * time.Second)
		if got := completeLines(p.stdout, "prices imported"); len(got) != 1 {
			t.Errorf("the refresh ran again within an hour: %v", got)
		}
		testutil.AssertAbsent(t, "stdout", p.stdout.String()+p.stderr.String(), d.Passwords()...)
		if code := p.stop(t); code != 0 {
			t.Fatalf("exit code %d", code)
		}
	})

	t.Run("a server that says 404", func(t *testing.T) {
		url := testutil.PriceServer(t, []byte("synthetic-marker-in-a-404"), testutil.PriceNotFound)
		d := dbtest.New(t)
		p := startBinary(t, opts{level: "info", up: up, db: d, env: refreshEnv(t, url)})
		var failed []map[string]any
		testutil.Eventually(t, 15*time.Second, "the failed refresh", func() bool {
			failed = completeLines(p.stdout, "price import failed")
			return len(failed) > 0
		})
		if failed[0]["error_class"] != "status" || failed[0]["source"] != "remote" || failed[0]["level"] != "WARN" {
			t.Errorf("`price import failed` lines: %v", failed)
		}
		testutil.AssertAbsent(t, "stdout", p.stdout.String()+p.stderr.String(), "synthetic-marker-in-a-404")
		// The process stays ready and goes on serving.
		resp, err := p.client.Get(p.scheme + "://" + p.addr + "/readyz")
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("readiness after a failed refresh: %v %v", resp, err)
		} else {
			resp.Body.Close()
		}
		if n := count(t, d, `SELECT count(*) FROM pandokos.price_version`); n != 0 {
			t.Errorf("a failed refresh created %d versions", n)
		}
		if code := p.stop(t); code != 0 {
			t.Errorf("exit code %d", code)
		}
	})

	t.Run("refresh is off by default and the test hook alone does nothing", func(t *testing.T) {
		d := dbtest.New(t)
		p := startBinary(t, opts{level: "info", up: up, db: d, env: []string{"PANDOKOS_TEST_REFRESH_DELAY=1s"}})
		time.Sleep(3 * time.Second)
		if got := completeLines(p.stdout, "prices imported"); len(got) != 0 {
			t.Errorf("an import without a configuration: %v", got)
		}
		if code := p.stop(t); code != 0 {
			t.Errorf("exit code %d", code)
		}
	})
}
