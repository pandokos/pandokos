//go:build compose

package compose

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	promURL    = "http://127.0.0.1:9090"
	grafanaURL = "http://127.0.0.1:3000"
	obsBoot    = "/home/nonroot/bootstrap.key"
	obsDemo    = "/home/nonroot/demo-admin.key"
)

// obsStack is the reference deployment plus the observability overlay, with a tenant, two priced
// catalogue models of the fake provider, a key and the client that trusts the deployment's
// certificate.
type obsStack struct {
	*stack
	repo     string
	key      string
	trusting *http.Client
	models   []string
}

// compose runs docker compose with the overlay.
func (s *obsStack) compose(stdin string, bound time.Duration, args ...string) (string, string, error) {
	s.t.Helper()
	full := append([]string{"compose", "-p", s.project, "-f", "compose.yaml", "-f", "override.yaml", "-f", "observability/compose.yaml"}, args...)
	return run(s.t, s.dir, stdin, bound, "docker", full...)
}

func (s *obsStack) mustCompose(stdin string, bound time.Duration, args ...string) string {
	s.t.Helper()
	out, errs, err := s.compose(stdin, bound, args...)
	if err != nil {
		s.t.Fatalf("docker compose %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, errs)
	}
	return out
}

func (s *obsStack) pdk(keyFile, stdin string, args ...string) string {
	s.t.Helper()
	full := append([]string{"exec", "-T", "-e", "PANDOKOS_CLI_KEY_FILE=" + keyFile, "pandokos", "/pandokos"}, args...)
	return s.mustCompose(stdin, 60*time.Second, full...)
}

// startObservability brings up the reference with the overlay (docker compose -f compose.yaml -f
// observability/compose.yaml up -d --wait) and makes the tenant of the quick start.
func startObservability(t *testing.T) *obsStack {
	t.Helper()
	image := os.Getenv("PANDOKOS_TEST_IMAGE")
	if image == "" {
		t.Fatal("PANDOKOS_TEST_IMAGE is not set: build the image (make image) and name it; this test fails, it does not skip")
	}
	if _, errs, err := run(t, ".", "", 30*time.Second, "docker", "compose", "version"); err != nil {
		t.Fatalf("docker compose v2 is required: %v %s", err, errs)
	}
	repo := testutil.RepoRoot(t)
	tmp := t.TempDir()
	copyTree(t, filepath.Join(repo, "deploy", "compose"), filepath.Join(tmp, "deploy", "compose"))
	copyTree(t, filepath.Join(repo, "deploy", "postgres"), filepath.Join(tmp, "deploy", "postgres"))
	dir := filepath.Join(tmp, "deploy", "compose")
	override, err := os.ReadFile(filepath.Join(repo, "test", "compose", "override.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "override.yaml"), override, 0o644); err != nil {
		t.Fatal(err)
	}
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	s := &obsStack{stack: &stack{t: t, dir: dir, project: "pdk-obs-" + hex.EncodeToString(nonce[:])}, repo: repo, models: []string{fakeprovider.ModelBasic, fakeprovider.ModelDetails}}
	t.Setenv("PANDOKOS_TEST_IMAGE", image)
	if out, errs, err := run(t, dir, "", 60*time.Second, "./setup.sh"); err != nil {
		t.Fatalf("setup.sh: %v\n%s\n%s", err, out, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets", "grafana_admin")); err != nil {
		t.Errorf("setup.sh does not create secrets/grafana_admin: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _, _ := s.compose("", 60*time.Second, "logs", "--no-color")
			t.Logf("docker compose logs:\n%s", out)
		}
		_, _, _ = s.compose("", 120*time.Second, "down", "-v", "--remove-orphans")
	})

	pool := x509.NewCertPool()
	pem, err := os.ReadFile(filepath.Join(dir, "secrets", "tls_cert"))
	if err != nil {
		t.Fatal(err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("secrets/tls_cert holds no certificate")
	}
	s.trusting = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}}

	s.mustCompose("", 15*time.Minute, "up", "-d", "--wait")
	testutil.Eventually(t, 90*time.Second, "readiness over TLS", func() bool {
		resp, err := s.trusting.Get("https://localhost:8080/readyz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 200
	})

	fake := fakeprovider.NewOn(t, "0.0.0.0:0")
	fake.Accept(fixtures.CanaryCredential)
	s.mustCompose("", 60*time.Second, "exec", "-T", "pandokos", "/pandokos", "init", "--key-out", obsBoot)
	s.mustCompose("", 60*time.Second, "exec", "-T", "-e", "PANDOKOS_CLI_KEY_FILE="+obsBoot, "pandokos", "/pandokos", "tenant", "create", "demo", "--admin-key-out", obsDemo)
	s.pdk(obsBoot, "", "endpoint", "update", "--base-url", fmt.Sprintf("http://host.docker.internal:%d", fake.Port()), "--private", "openai", "default")
	for _, m := range append(append([]string(nil), s.models...), fakeprovider.ModelError429) {
		s.pdk(obsBoot, "", "model", "create", "--availability", "all", "openai/"+m)
	}
	for _, m := range s.models {
		s.pdk(obsBoot, "", "price", "override", "--currency", "USD", "--rate", "input=3", "--rate", "output=6", "openai/"+m)
	}
	s.pdk(obsDemo, fixtures.CanaryCredential, "credential", "create", "--provider", "openai", "--secret-file", "/dev/stdin", "openai-main")
	s.pdk(obsDemo, "", "service-account", "create", "--role", "inference", "app")
	s.pdk(obsDemo, "", "key", "create", "--service-account", "app", "--key-out", "/home/nonroot/app.key")
	keyCopy := filepath.Join(tmp, "app.key")
	s.mustCompose("", 60*time.Second, "cp", "pandokos:/home/nonroot/app.key", keyCopy)
	kb, err := os.ReadFile(keyCopy)
	if err != nil {
		t.Fatal(err)
	}
	s.key = strings.TrimSpace(string(kb))
	return s
}

// call makes one chat call to the deployment and returns the status.
func (s *obsStack) call(model string, stream bool) (int, time.Duration) {
	s.t.Helper()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"` + fixtures.CanaryPrompt + `"}]`
	if stream {
		body += `,"stream":true`
	}
	req, _ := http.NewRequestWithContext(s.t.Context(), http.MethodPost, "https://localhost:8080/openai/v1/chat/completions", strings.NewReader(body+"}"))
	req.Header = http.Header{"Authorization": {"Bearer " + s.key}, "Content-Type": {"application/json"}}
	began := time.Now()
	resp, err := s.trusting.Do(req)
	if err != nil {
		s.t.Fatalf("a call: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, time.Since(began)
}

// promQuery runs an instant query on the Prometheus of the overlay and returns the series.
func promQuery(t testing.TB, query string) []map[string]string {
	t.Helper()
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(promURL + "/api/v1/query?query=" + url.QueryEscape(query))
	if err != nil {
		t.Fatalf("Prometheus: %v", err)
	}
	defer resp.Body.Close()
	var doc struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("Prometheus answered %d: %v", resp.StatusCode, err)
	}
	if doc.Status != "success" {
		t.Fatalf("the query %q failed: %s", query, doc.Error)
	}
	var out []map[string]string
	for _, r := range doc.Data.Result {
		m := map[string]string{}
		for k, v := range r.Metric {
			m[k] = v
		}
		if len(r.Value) == 2 {
			m["__value__"] = fmt.Sprint(r.Value[1])
		}
		out = append(out, m)
	}
	return out
}

// dashboardPanel is a panel of a Grafana dashboard that has queries.
type dashboardPanel struct {
	Dashboard, Title string
	Queries          []string
}

// panelsOf reads a dashboard file: every panel with a query, the nested ones of rows included.
func panelsOf(t *testing.T, path string) []dashboardPanel {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v", err)
	}
	var doc struct {
		Title  string `json:"title"`
		Panels []json.RawMessage
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var out []dashboardPanel
	var walk func(raw json.RawMessage)
	walk = func(raw json.RawMessage) {
		var p struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
			Panels []json.RawMessage `json:"panels"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		dp := dashboardPanel{Dashboard: filepath.Base(path), Title: p.Title}
		for _, tg := range p.Targets {
			if strings.TrimSpace(tg.Expr) != "" {
				dp.Queries = append(dp.Queries, tg.Expr)
			}
		}
		if len(dp.Queries) > 0 {
			out = append(out, dp)
		}
		for _, c := range p.Panels {
			walk(c)
		}
	}
	for _, p := range doc.Panels {
		walk(p)
	}
	return out
}

var reVariable = regexp.MustCompile(`\$\{?(model|provider|datasource)\}?(:\w+)?`)

// expand makes a panel query runnable: the variables model and provider are .*, the rate interval is
// one minute.
func expand(q string) string {
	q = strings.NewReplacer("$__rate_interval", "1m", "${__rate_interval}", "1m", "$__interval", "1m", "${__interval}", "1m", "$__range", "1h").Replace(q)
	return reVariable.ReplaceAllString(q, ".*")
}

func median(d []time.Duration) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func p95(d []time.Duration) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)*95/100]
}

// TestObservabilityDashboards is T-931: the reference plus the overlay with the built image and the
// fake provider for two models. After 40 calls (streams and not) every panel query of both
// dashboards, with the variables expanded to .*, returns at least one series from Prometheus; the
// latency, token and cost panels of the Inference dashboard return a series for each model, with
// gen_ai_request_model equal to the two catalogue names; Grafana lists both dashboards; and the set
// of metric names of Prometheus (OTLP path) equals the set of GET /metrics (scrape path).
func TestObservabilityDashboards(t *testing.T) {
	s := startObservability(t)

	// Calls: 40 alternating the two models, half of them streams; then the provider's 429 and a
	// refusal of the limits, so that every panel has something to show.
	s.pdk(obsDemo, "", "limit", "create", "--metric", "requests_per_minute", "--limit", "42")
	for i := 0; i < 40; i++ {
		model := s.models[i%2]
		stream := i%4 >= 2 && model == fakeprovider.ModelBasic // fake-details answers whole documents only
		if status, _ := s.call(model, stream); status != 200 {
			t.Fatalf("call %d: status %d", i, status)
		}
	}
	s.call(fakeprovider.ModelError429, false)
	s.call(fakeprovider.ModelBasic, false)
	if status, _ := s.call(fakeprovider.ModelBasic, false); status != 429 {
		t.Errorf("the call over the limit: status %d, want 429", status)
	}

	// A rate needs two samples of a series: wait until the series of both models and the series born
	// last (the refusal of the limits) have been exported twice, 10 s apart.
	testutil.Eventually(t, 90*time.Second, "both models in Prometheus, each series with two samples", func() bool {
		models := map[string]bool{}
		for _, m := range promQuery(t, "count_over_time(gen_ai_client_operation_duration_seconds_count[5m]) >= 2") {
			models[m["gen_ai_request_model"]] = true
		}
		return models[s.models[0]] && models[s.models[1]] && len(promQuery(t, "count_over_time(pandokos_limits_refusals_total[5m]) >= 2")) > 0
	})

	files, err := filepath.Glob(filepath.Join(s.repo, "deploy", "compose", "observability", "grafana", "dashboards", "*.json"))
	if err != nil || len(files) != 2 {
		t.Fatalf("the dashboards: %v %v", files, err)
	}
	for _, f := range files {
		for _, panel := range panelsOf(t, f) {
			for _, q := range panel.Queries {
				series := promQuery(t, expand(q))
				if len(series) == 0 {
					t.Errorf("%s, %q: the query %q returns no series", panel.Dashboard, panel.Title, q)
					continue
				}
				inference := strings.Contains(panel.Dashboard, "inference")
				perModel := inference && strings.Contains(q, "gen_ai_request_model") &&
					(strings.Contains(q, "gen_ai_client_operation_duration") || strings.Contains(q, "gen_ai_client_token_usage") || strings.Contains(q, "pandokos_gen_ai_client_cost_total"))
				if perModel {
					got := map[string]bool{}
					for _, m := range series {
						got[m["gen_ai_request_model"]] = true
					}
					if len(got) != 2 || !got[s.models[0]] || !got[s.models[1]] {
						t.Errorf("%s, %q: the series of %q are for the models %v, want exactly %v", panel.Dashboard, panel.Title, q, got, s.models)
					}
				}
			}
		}
	}

	t.Run("Grafana lists both dashboards", func(t *testing.T) {
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(grafanaURL + "/api/search?query=Pandokos")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var found []struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&found); err != nil || resp.StatusCode != 200 {
			t.Fatalf("/api/search: %d %v", resp.StatusCode, err)
		}
		var inference, internals bool
		for _, f := range found {
			inference = inference || strings.Contains(f.Title, "Inference")
			internals = internals || strings.Contains(f.Title, "Internals")
		}
		if !inference || !internals {
			t.Errorf("Grafana lists %+v, want both Pandokos dashboards", found)
		}
	})

	t.Run("the names of the OTLP path equal the names of the scrape path", func(t *testing.T) {
		s.pdk(obsBoot, "", "platform-key", "create", "--role", "platform-auditor", "--key-out", "/home/nonroot/scraper.key")
		keyCopy := filepath.Join(t.TempDir(), "scraper.key")
		s.mustCompose("", 60*time.Second, "cp", "pandokos:/home/nonroot/scraper.key", keyCopy)
		kb, err := os.ReadFile(keyCopy)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://127.0.0.1:8081/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(kb)))
		resp, err := s.trusting.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("the scrape: %d\n%s", resp.StatusCode, body)
		}
		strip := func(n string) string {
			for _, suf := range []string{"_bucket", "_sum", "_count"} {
				n = strings.TrimSuffix(n, suf)
			}
			return n
		}
		kept := func(n string) bool {
			return strings.HasPrefix(n, "gen_ai_") || strings.HasPrefix(n, "http_server_") || strings.HasPrefix(n, "pandokos_")
		}
		scraped := map[string]bool{}
		for _, line := range strings.Split(string(body), "\n") {
			if f := strings.Fields(line); len(f) >= 3 && f[0] == "#" && f[1] == "TYPE" && kept(f[2]) {
				scraped[strip(f[2])] = true
			}
		}
		var names struct {
			Data []string `json:"data"`
		}
		var viaOTLP map[string]bool
		testutil.Eventually(t, 60*time.Second, "the OTLP names to match the scrape names", func() bool {
			r, err := (&http.Client{Timeout: 15 * time.Second}).Get(promURL + "/api/v1/label/__name__/values")
			if err != nil {
				return false
			}
			defer r.Body.Close()
			if json.NewDecoder(r.Body).Decode(&names) != nil {
				return false
			}
			viaOTLP = map[string]bool{}
			for _, n := range names.Data {
				if kept(n) {
					viaOTLP[strip(n)] = true
				}
			}
			if len(viaOTLP) != len(scraped) {
				return false
			}
			for n := range scraped {
				if !viaOTLP[n] {
					return false
				}
			}
			return true
		})
		var missing, extra []string
		for n := range scraped {
			if !viaOTLP[n] {
				missing = append(missing, n)
			}
		}
		for n := range viaOTLP {
			if !scraped[n] {
				extra = append(extra, n)
			}
		}
		if len(missing)+len(extra) > 0 {
			sort.Strings(missing)
			sort.Strings(extra)
			t.Errorf("names in the scrape and not in Prometheus: %v; names in Prometheus and not in the scrape: %v", missing, extra)
		}
	})
}

// TestObservabilityCollectorStopped is T-932: 30 calls timed, `docker compose stop otel-collector`, 30
// calls timed: the median after is at most 1.5 times the median before plus 50 ms and the 95th
// percentile at most twice plus 100 ms; every call succeeds; the log of Pandokos has `metric export
// failing` within two intervals and no line at error level; after `start` the series resume.
func TestObservabilityCollectorStopped(t *testing.T) {
	s := startObservability(t)
	timed := func() []time.Duration {
		var out []time.Duration
		for i := 0; i < 30; i++ {
			status, d := s.call(fakeprovider.ModelBasic, i%2 == 1)
			if status != 200 {
				t.Fatalf("call %d: status %d", i, status)
			}
			out = append(out, d)
		}
		return out
	}
	// Warm up, then measure with the collector.
	for i := 0; i < 5; i++ {
		s.call(fakeprovider.ModelBasic, false)
	}
	before := timed()
	testutil.Eventually(t, 90*time.Second, "the requests in Prometheus", func() bool { return len(promQuery(t, "sum(pandokos_gateway_requests_total)")) > 0 })
	value := func() float64 {
		r := promQuery(t, "sum(pandokos_gateway_requests_total)")
		if len(r) == 0 {
			return 0
		}
		v, _ := strconv.ParseFloat(r[0]["__value__"], 64)
		return v
	}
	atStop := value()

	s.mustCompose("", 120*time.Second, "stop", "otel-collector")
	stopped := time.Now()
	after := timed()

	if m, mb := median(after), median(before); m > mb*3/2+50*time.Millisecond {
		t.Errorf("median %s with the collector stopped, %s before: more than 1.5 times plus 50 ms", m, mb)
	}
	if p, pb := p95(after), p95(before); p > 2*pb+100*time.Millisecond {
		t.Errorf("p95 %s with the collector stopped, %s before: more than twice plus 100 ms", p, pb)
	}

	testutil.Eventually(t, 60*time.Second, "`metric export failing` in the log of Pandokos", func() bool {
		return strings.Contains(s.mustCompose("", 60*time.Second, "logs", "--no-color", "pandokos"), `"msg":"metric export failing"`)
	})
	if d := time.Since(stopped); d > 2*time.Minute {
		t.Errorf("the failure line took %s to appear", d)
	}
	logs := s.mustCompose("", 60*time.Second, "logs", "--no-color", "pandokos")
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, `"level":"ERROR"`) {
			t.Errorf("a line at error level while the collector is away: %.300s", line)
		}
	}

	s.mustCompose("", 120*time.Second, "start", "otel-collector")
	testutil.Eventually(t, 60*time.Second, "the series of the requests to increase again", func() bool {
		s.call(fakeprovider.ModelBasic, false)
		return value() > atStop
	})
}
