package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

// The binary exports metrics every export_interval, which the configuration bounds to 5 s from
// below: the tests that wait for an export use that value.
const metricsInterval = "5s"

const exposition = "text/plain; version=0.0.4; charset=utf-8"

// readKeyFile returns the key a CLI command wrote.
func readKeyFile(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// scrapeAs sends a request to /metrics of the administration listener with the key (no credential
// when it is empty) and returns the status and the whole text of the response.
func (p *proc) scrapeAs(t *testing.T, method, key string) (int, string) {
	t.Helper()
	hdr := http.Header{}
	if key != "" {
		hdr.Set("Authorization", "Bearer "+key)
	}
	return exchangeWith(t, p.client, method, p.adminURL("/metrics"), hdr, nil)
}

// bodyOf returns the body of the text of exchangeWith.
func bodyOf(text string) string {
	_, body, _ := strings.Cut(text, "\n\n")
	return body
}

// headerOf returns the value of a header of the text of exchangeWith.
func headerOf(text, name string) string {
	head, _, _ := strings.Cut(text, "\n\n")
	for _, l := range strings.Split(head, "\n") {
		if k, v, ok := strings.Cut(l, ": "); ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// platformKeyWithRole makes a platform key of the role with the bootstrap key and returns it. A
// platform key carries a role, not scopes: the scraper's key is one of the role platform-auditor.
func (p *proc) platformKeyWithRole(t *testing.T, role string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), role+".key")
	p.mustCLI(t, p.bootstrap(t), "platform-key", "create", "--role", role, "--key-out", file)
	return readKeyFile(t, file)
}

// keyWithRole makes a service account of the tenant with the role and a key for it, with the key
// file of the tenant's owner, and returns the key.
func (p *proc) keyWithRole(t *testing.T, ownerFile, tenantID, role string) string {
	t.Helper()
	name := fmt.Sprintf("role-%s-%d", role, time.Now().UnixNano())
	p.mustCLI(t, ownerFile, "service-account", "create", "--tenant", tenantID, "--role", role, name)
	list := p.mustCLI(t, ownerFile, "service-account", "list", "--tenant", tenantID, "-o", "json")
	m := regexp.MustCompile(`"id":\s*"([0-9a-f-]{36})"[^{}]*?"name":\s*"` + regexp.QuoteMeta(name) + `"`).FindStringSubmatch(list)
	if m == nil {
		m = regexp.MustCompile(`"name":\s*"` + regexp.QuoteMeta(name) + `"[^{}]*?"id":\s*"([0-9a-f-]{36})"`).FindStringSubmatch(list)
	}
	if m == nil {
		t.Fatalf("no service account %s in %s", name, list)
	}
	p.track(m[1])
	file := filepath.Join(t.TempDir(), name+".key")
	p.mustCLI(t, ownerFile, "key", "create", "--tenant", tenantID, "--service-account", m[1], "--key-out", file)
	return readKeyFile(t, file)
}

// TestScrapeEndpoint is T-924: GET /metrics on the administration listener answers 200 to a platform
// key holding metrics:read, with the content type of the text format and the counter of the requests
// after a call; HEAD answers without a body and POST 405; no credential and a malformed one answer
// 401; a tenant key of each built-in tenant role and a token of a tenant-level issuer answer 403; the
// path is the administration API's 404 while telemetry.metrics.prometheus is false; and on the
// inference listener /metrics answers endpoint_not_supported.
//
// Deviation from the design: a platform key carries a role and no scopes (PlatformKeyCreate has no
// scopes), so the keys that hold the permission are one of the role platform-auditor and one of the
// role platform-admin (the bootstrap key); the key "scoped to catalog:read" cannot exist.
func TestScrapeEndpoint(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up, metricsPrometheus: true})
	boot := p.bootstrap(t)
	key := p.inferenceKey(t)
	if status, text := p.chatCall(t, key, fakeprovider.ModelBasic, false); status != 200 {
		t.Fatalf("%d\n%s", status, text)
	}
	admin := readKeyFile(t, boot)
	auditor := p.platformKeyWithRole(t, "platform-auditor")

	t.Run("200 with a platform key", func(t *testing.T) {
		for name, k := range map[string]string{"platform-auditor": auditor, "platform-admin": admin} {
			status, text := p.scrapeAs(t, http.MethodGet, k)
			if status != 200 {
				t.Errorf("%s: %d\n%s", name, status, text)
				continue
			}
			if ct := headerOf(text, "Content-Type"); ct != exposition {
				t.Errorf("%s: Content-Type %q", name, ct)
			}
			if !strings.Contains(bodyOf(text), "pandokos_gateway_requests_total") || !strings.Contains(bodyOf(text), "# TYPE pandokos_gateway_requests_total counter") {
				t.Errorf("%s: the body has no counter of the requests after a call:\n%.600s", name, bodyOf(text))
			}
		}
	})

	t.Run("HEAD answers without a body, POST 405", func(t *testing.T) {
		status, text := p.scrapeAs(t, http.MethodHead, auditor)
		if status != 200 || bodyOf(text) != "" || headerOf(text, "Content-Type") != exposition {
			t.Errorf("HEAD: %d %q\n%s", status, bodyOf(text), text)
		}
		status, text = p.scrapeAs(t, http.MethodPost, auditor)
		if status != 405 || headerOf(text, "Allow") != "GET, HEAD" {
			t.Errorf("POST: %d, Allow %q\n%s", status, headerOf(text, "Allow"), text)
		}
	})

	t.Run("401 without a credential, with a malformed one and with another scheme", func(t *testing.T) {
		for name, hdr := range map[string]http.Header{
			"no credential":  {},
			"malformed":      {"Authorization": {"Bearer pdk_nonsense"}},
			"another scheme": {"Authorization": {"Basic " + "YWxhZGRpbjpvcGVuc2VzYW1l"}},
			"a cookie":       {"Cookie": {"__Host-pandokos_session=pds_synthetic"}},
		} {
			status, text := exchangeWith(t, p.client, "GET", p.adminURL("/metrics"), hdr, nil)
			if status != 401 {
				t.Errorf("%s: %d\n%s", name, status, text)
				continue
			}
			if !strings.Contains(headerOf(text, "WWW-Authenticate"), "Bearer") || headerOf(text, "X-Pandokos-Error") != "unauthenticated" && !strings.Contains(bodyOf(text), "unauthenticated") {
				t.Errorf("%s: the answer is not the unauthenticated problem:\n%s", name, text)
			}
		}
	})

	t.Run("403 for a tenant key of each built-in tenant role", func(t *testing.T) {
		id, owner := p.newTenant(t, boot, "scrape-roles")
		tried := 0
		for _, r := range authz.Roles() {
			if r.Scope != authz.ScopeTenant {
				continue
			}
			k := p.keyWithRole(t, owner, id, r.Name)
			if status, text := p.scrapeAs(t, http.MethodGet, k); status != 403 {
				t.Errorf("the role %s: %d\n%s", r.Name, status, text)
			}
			tried++
		}
		if status, text := p.scrapeAs(t, http.MethodGet, readKeyFile(t, owner)); status != 403 {
			t.Errorf("the owner's key: %d\n%s", status, text)
		}
		if tried < 5 {
			t.Errorf("only %d tenant roles were tried", tried)
		}
	})

	t.Run("on the inference listener /metrics is not served", func(t *testing.T) {
		status, text := exchangeWith(t, p.client, "GET", p.url("/metrics"), authHeader(key), nil)
		assertPandokosError(t, "GET /metrics on the inference listener", status, text, 404, "endpoint_not_supported")
	})

	t.Run("a token of a tenant-level issuer is refused", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		w := startTokenProc(t, idp, opts{level: "info", metricsPrometheus: true, env: []string{"PANDOKOS_TOKENS_ADMIN_LISTENER=true"}})
		tn := w.tenant(t, "scrape-token")
		iss := w.registration(t, tn, "idp", "pandokos-scrape")
		w.mapping(t, tn, iss, "workload-scrape", false)
		token := w.sign("ES256", "workload-scrape", "pandokos-scrape")
		// The token is valid: the same token calls the inference listener.
		if status, text := w.openai(t, token, false); status != 200 {
			t.Fatalf("the token on the inference listener: %d\n%s", status, text)
		}
		if status, text := w.scrapeAs(t, http.MethodGet, token); status != 403 {
			t.Errorf("the token on /metrics: %d\n%s", status, text)
		}
		if code := w.stop(t); code != 0 {
			t.Errorf("exit code %d", code)
		}
	})

	t.Run("404 while telemetry.metrics.prometheus is false", func(t *testing.T) {
		q := startBinary(t, opts{level: "info"})
		k := readKeyFile(t, q.bootstrap(t))
		status, text := q.scrapeAs(t, http.MethodGet, k)
		if status != 404 {
			t.Errorf("%d\n%s", status, text)
		}
		if strings.Contains(bodyOf(text), "pandokos_") {
			t.Errorf("a body of metrics although the switch is off:\n%.300s", bodyOf(text))
		}
		if code := q.stop(t); code != 0 {
			t.Errorf("exit code %d", code)
		}
	})

	if code := p.stop(t); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// exportedPoints walks the metrics a collector received and returns, per instrument name, the
// attribute values of its data points.
func exportedAttributes(c *otlptest.Collector) map[string][]string {
	out := map[string][]string{}
	add := func(name string, kvs []*commonpb.KeyValue) {
		for _, kv := range kvs {
			out[name] = append(out[name], kv.GetValue().GetStringValue())
		}
	}
	for _, rm := range c.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				switch d := m.GetData().(type) {
				case *metricspb.Metric_Sum:
					for _, dp := range d.Sum.GetDataPoints() {
						add(m.GetName(), dp.GetAttributes())
					}
				case *metricspb.Metric_Gauge:
					for _, dp := range d.Gauge.GetDataPoints() {
						add(m.GetName(), dp.GetAttributes())
					}
				case *metricspb.Metric_Histogram:
					for _, dp := range d.Histogram.GetDataPoints() {
						add(m.GetName(), dp.GetAttributes())
					}
				}
			}
		}
	}
	return out
}

// TestMetricsHoldNoTenantData is T-925: two tenants with canary names, service accounts, keys and
// metadata, calling with a prompt that is a canary, including a provider 429 and a refusal of the
// limits: neither the scrape body nor any OTLP export holds the tenants' IDs, names, key IDs,
// service-account IDs, metadata canaries or fixtures.Canaries(); credential IDs appear only on
// pandokos_provider_rate_limited_total (pandokos.provider.rate_limited over OTLP).
func TestMetricsHoldNoTenantData(t *testing.T) {
	up := fakeprovider.New(t)
	col := otlptest.New(t)
	p := startBinary(t, opts{level: "debug", up: up, collector: col, otlpEndpoint: col.URL().String(), exportInterval: metricsInterval, otlpTimeout: "2s", metricsPrometheus: true})
	boot := p.bootstrap(t)
	p.prepareUpstream(t)
	p.mustCLI(t, boot, "price", "override", "--currency", "USD", "--rate", "input=3", "--rate", "output=6", "openai/fake-basic")

	type tenantFixture struct {
		id, name, saName, keyDesc, meta, key string
		cred                                 string
	}
	var tenants []tenantFixture
	var canaries []string
	for i := 0; i < 2; i++ {
		f := tenantFixture{name: fixtures.CanaryLabel("tenant"), saName: fixtures.CanaryLabel("sa"), keyDesc: fixtures.CanaryLabel("keydesc"), meta: fixtures.CanaryLabel("meta")}
		var admin string
		f.id, admin = p.newTenant(t, boot, f.name)
		p.mustCLI(t, admin, "service-account", "create", "--tenant", f.id, "--role", "inference", f.saName)
		list := p.mustCLI(t, admin, "service-account", "list", "--tenant", f.id, "-o", "json")
		m := regexp.MustCompile(`"id":\s*"([0-9a-f-]{36})"[^{}]*?"name":\s*"` + f.saName + `"|"name":\s*"` + f.saName + `"[^{}]*?"id":\s*"([0-9a-f-]{36})"`).FindStringSubmatch(list)
		if m == nil {
			t.Fatalf("no service account %s in %s", f.saName, list)
		}
		sa := m[1] + m[2]
		keyFile := filepath.Join(t.TempDir(), "key")
		out := p.mustCLI(t, admin, "key", "create", "--tenant", f.id, "--service-account", sa, "--description", f.keyDesc, "--key-out", keyFile)
		f.key = readKeyFile(t, keyFile)
		km := regexp.MustCompile(`(?m)^(?:key )?id: (t[a-z2-7]{16})$`).FindStringSubmatch(out)
		if km == nil {
			t.Fatalf("no key ID in the output of key create:\n%s", out)
		}
		p.track(km[1])
		f.cred = p.addCredential(t, admin, "openai", fixtures.CanaryCredential)
		p.track(f.id, sa)
		canaries = append(canaries, f.id, f.name, f.saName, f.keyDesc, f.meta, sa, km[1])
		tenants = append(tenants, f)
	}
	canaries = append(canaries, fixtures.Canaries()...)
	p.track(canaries...)
	// The second tenant has a rate limit of three requests a minute, so that its fourth call is refused.
	tenantID2, err := tenant.ParseID(tenants[1].id)
	if err != nil {
		t.Fatal(err)
	}
	p.db.SeedLimitRule(t, tenantID2, "tenant", "", "requests_per_minute", 3)

	for _, f := range tenants {
		meta := func() http.Header {
			h := authHeader(f.key)
			h.Set("X-Pandokos-Metadata-Note", f.meta)
			return h
		}
		// The provider's own 429, then a call and a stream that are answered.
		exchangeWith(t, p.client, "POST", p.url(chatPath), meta(), chatBody(fakeprovider.ModelError429, false, fixtures.CanaryPrompt))
		for _, stream := range []bool{false, true} {
			if status, text := exchangeWith(t, p.client, "POST", p.url(chatPath), meta(), chatBody(fakeprovider.ModelBasic, stream, fixtures.CanaryPrompt)); status != 200 {
				t.Fatalf("a call of %s: %d\n%s", f.name, status, text)
			}
		}
	}
	status, text := exchangeWith(t, p.client, "POST", p.url(chatPath), authHeader(tenants[1].key), chatBody(fakeprovider.ModelBasic, false, fixtures.CanaryPrompt))
	if status != 429 {
		t.Fatalf("the refusal of the limits: %d\n%s", status, text)
	}

	// Wait for an export that holds the refusal and the provider's 429, then scrape.
	testutil.Eventually(t, 30*time.Second, "an export with the limit refusal and the provider's 429", func() bool {
		attrs := exportedAttributes(col)
		return len(attrs["pandokos.limits.refusals"]) > 0 && len(attrs["pandokos.provider.rate_limited"]) > 0
	})
	auditor := p.platformKeyWithRole(t, "platform-auditor")
	scrapeStatus, scrapeText := p.scrapeAs(t, http.MethodGet, auditor)
	if scrapeStatus != 200 {
		t.Fatalf("the scrape: %d\n%s", scrapeStatus, scrapeText)
	}
	body := bodyOf(scrapeText)

	var raw strings.Builder
	for _, r := range col.Requests() {
		if strings.HasSuffix(r.Path, "/v1/metrics") {
			raw.Write(r.Body)
		}
	}
	for where, text := range map[string]string{"the scrape body": body, "the OTLP exports": raw.String()} {
		testutil.AssertNoCanary(t, where, text)
		testutil.AssertAbsent(t, where, text, canaries...)
		testutil.AssertAbsent(t, where, text, testutil.KeyForms(tenants[0].key)...)
		testutil.AssertAbsent(t, where, text, testutil.KeyForms(tenants[1].key)...)
	}

	// The credential IDs: only on the counter of the provider's 429.
	for _, f := range tenants {
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, f.cred) && !strings.HasPrefix(line, "pandokos_provider_rate_limited_total{") {
				t.Errorf("the credential ID of %s is on another line of the scrape: %.200s", f.name, line)
			}
		}
		for name, values := range exportedAttributes(col) {
			for _, v := range values {
				if v == f.cred && name != "pandokos.provider.rate_limited" {
					t.Errorf("the credential ID of %s is an attribute of %s", f.name, name)
				}
			}
		}
		if !strings.Contains(body, f.cred) {
			t.Errorf("the credential ID of %s, whose provider answered 429, is not on pandokos_provider_rate_limited_total", f.name)
		}
	}
	if code := p.stop(t); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// TestMetricsOverOTLP is T-928: the binary, the test collector and a short interval: after a chat call
// and a stream call on two catalogue models, the collector holds the request duration, the request
// counter, the provider call duration, the time to first chunk (stream only), the token usage with
// gen_ai.request.model equal to the catalogue names and the cost with the currency of the catalogue;
// the resource names the service.
func TestMetricsOverOTLP(t *testing.T) {
	up := fakeprovider.New(t)
	col := otlptest.New(t)
	p := startBinary(t, opts{level: "info", up: up, collector: col, otlpEndpoint: col.URL().String(), exportInterval: metricsInterval, otlpTimeout: "2s", otlpHeader: collectorHeaderCanary})
	boot := p.bootstrap(t)
	key := p.inferenceKey(t)
	for _, m := range []string{fakeprovider.ModelBasic, fakeprovider.ModelDetails} {
		p.mustCLI(t, boot, "price", "override", "--currency", "USD", "--rate", "input=3", "--rate", "output=6", "openai/"+m)
	}
	calls := []struct {
		model  string
		stream bool
	}{{fakeprovider.ModelBasic, false}, {fakeprovider.ModelBasic, true}, {fakeprovider.ModelDetails, false}}
	for _, c := range calls {
		if status, text := p.chatCall(t, key, c.model, c.stream); status != 200 {
			t.Fatalf("%s: %d\n%s", c.model, status, text)
		}
	}

	var last *exportView
	testutil.Eventually(t, 40*time.Second, "an export with every instrument of a call", func() bool {
		ms := col.Metrics()
		if len(ms) == 0 {
			return false
		}
		last = parseExport(ms[len(ms)-1])
		for _, name := range []string{"http.server.request.duration", "pandokos.gateway.requests", "gen_ai.client.operation.duration",
			"gen_ai.client.operation.time_to_first_chunk", "gen_ai.client.token.usage", "pandokos.gen_ai.client.cost"} {
			if !last.has[name] {
				return false
			}
		}
		return len(last.models["gen_ai.client.token.usage"]) == 2
	})

	names := []string{fakeprovider.ModelBasic, fakeprovider.ModelDetails}
	for _, name := range []string{"gen_ai.client.operation.duration", "gen_ai.client.token.usage", "pandokos.gen_ai.client.cost"} {
		got := last.models[name]
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(sortedCopy(names), ",") {
			t.Errorf("%s: gen_ai.request.model values %v, want the catalogue names %v", name, got, names)
		}
	}
	if got := last.models["gen_ai.client.operation.time_to_first_chunk"]; len(got) != 1 || got[0] != fakeprovider.ModelBasic {
		t.Errorf("time to first chunk: models %v, want only the one that streamed", got)
	}
	if got := last.currencies["pandokos.gen_ai.client.cost"]; len(got) != 1 || got[0] != "USD" {
		t.Errorf("cost currencies %v, want the catalogue's USD", got)
	}
	if last.resource["service.name"] != "pandokos" || last.resource["service.instance.id"] == "" {
		t.Errorf("resource %v", last.resource)
	}
	for _, r := range col.Requests() {
		if strings.HasSuffix(r.Path, "/v1/metrics") && r.Header.Get("X-Collector-Key") != collectorHeaderCanary {
			t.Errorf("the metrics export carries header %q", r.Header.Get("X-Collector-Key"))
		}
	}
	if code := p.stop(t); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// TestCollectorStoppedLatencyUnchanged is T-929: as T-511 with metrics on and the scrape endpoint on:
// 20 calls with the collector, then the collector closed (or stalled) and 20 calls, the second run
// taking at most three times the first plus 500 ms; the scrapes keep working; and the process exits 0
// within ledger.shutdown_flush_timeout plus 5 s.
func TestCollectorStoppedLatencyUnchanged(t *testing.T) {
	for _, mode := range []string{"closed", "stalled"} {
		t.Run(mode, func(t *testing.T) {
			up := fakeprovider.New(t)
			col := otlptest.New(t)
			p := startBinary(t, opts{level: "info", up: up, collector: col, otlpEndpoint: col.URL().String(), otlpTimeout: "1s", flushTimeout: "3s",
				exportInterval: metricsInterval, metricsPrometheus: true})
			key := p.inferenceKey(t)
			scraper := p.platformKeyWithRole(t, "platform-auditor")
			timeCalls := func() time.Duration {
				began := time.Now()
				for i := 0; i < 20; i++ {
					if status, text := p.chatCall(t, key, fakeprovider.ModelBasic, i%2 == 1); status != 200 {
						t.Fatalf("%d\n%s", status, text)
					}
				}
				return time.Since(began)
			}
			withCollector := timeCalls()
			testutil.Eventually(t, 30*time.Second, "a first export", func() bool { return len(col.Metrics()) > 0 })
			if mode == "closed" {
				col.Close()
			} else {
				exports := func() (n int) {
					for _, r := range col.Requests() {
						if strings.HasSuffix(r.Path, "/v1/metrics") { // a trace export does not end the wait
							n++
						}
					}
					return n
				}
				before := exports()
				col.Stall()
				// The timed calls must overlap a stalled export: wait until one has reached the collector
				// (it is recorded before it waits) and is held there for the OTLP timeout.
				testutil.Eventually(t, 30*time.Second, "a metric export request held by the stalled collector", func() bool { return exports() > before })
			}
			without := timeCalls()
			if without > withCollector*3+500*time.Millisecond {
				t.Errorf("20 calls took %s with the collector and %s without", withCollector, without)
			}
			// The exports keep failing for a few intervals; the scrape does not depend on them.
			time.Sleep(2 * time.Second)
			if status, text := p.scrapeAs(t, http.MethodGet, scraper); status != 200 || !strings.Contains(bodyOf(text), "pandokos_gateway_requests_total") {
				t.Errorf("the scrape with the collector %s: %d\n%.300s", mode, status, text)
			}
			began := time.Now()
			if code := p.stop(t); code != 0 {
				t.Errorf("exit code %d", code)
			}
			if d := time.Since(began); d > 3*time.Second+5*time.Second {
				t.Errorf("the process took %s to exit, more than ledger.shutdown_flush_timeout plus 5 s", d)
			}
		})
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// exportView is what one export holds, by instrument.
type exportView struct {
	has        map[string]bool
	models     map[string][]string // the distinct values of gen_ai.request.model
	currencies map[string][]string // the distinct values of pandokos.currency
	resource   map[string]string
}

func parseExport(rm *metricspb.ResourceMetrics) *exportView {
	v := &exportView{has: map[string]bool{}, models: map[string][]string{}, currencies: map[string][]string{}, resource: map[string]string{}}
	for _, kv := range rm.GetResource().GetAttributes() {
		v.resource[kv.Key] = kv.GetValue().GetStringValue()
	}
	note := func(name string, kvs []*commonpb.KeyValue) {
		v.has[name] = true
		for _, kv := range kvs {
			var dst map[string][]string
			switch kv.Key {
			case "gen_ai.request.model":
				dst = v.models
			case "pandokos.currency":
				dst = v.currencies
			default:
				continue
			}
			val := kv.GetValue().GetStringValue()
			if !contains(dst[name], val) {
				dst[name] = append(dst[name], val)
			}
		}
	}
	for _, sm := range rm.GetScopeMetrics() {
		for _, m := range sm.GetMetrics() {
			switch d := m.GetData().(type) {
			case *metricspb.Metric_Sum:
				for _, dp := range d.Sum.GetDataPoints() {
					note(m.GetName(), dp.GetAttributes())
				}
			case *metricspb.Metric_Gauge:
				for _, dp := range d.Gauge.GetDataPoints() {
					note(m.GetName(), dp.GetAttributes())
				}
			case *metricspb.Metric_Histogram:
				for _, dp := range d.Histogram.GetDataPoints() {
					note(m.GetName(), dp.GetAttributes())
				}
			}
		}
	}
	return v
}
