package telemetry_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/testutil"
)

// scrapeMetrics builds a Metrics with the scrape switch only (no endpoint, no OTLP) over a fixed
// resource, so that the exposition is deterministic.
func scrapeMetrics(t *testing.T, sources telemetry.Sources) *telemetry.Metrics {
	t.Helper()
	res, err := telemetry.NewResource(map[string]string{"service.instance.id": "instance-1", "deployment.environment": "test"}, "v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	m, err := telemetry.NewMetrics(telemetry.MetricsOptions{
		Resource: res, Prometheus: true, Interval: time.Minute, ServiceVersion: "v9.9.9",
		Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "debug"), Sources: sources,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownMetrics(t, m) })
	return m
}

func scrape(t *testing.T, m *telemetry.Metrics, method string) *httptest.ResponseRecorder {
	t.Helper()
	h := m.Handler()
	if h == nil {
		t.Fatal("Handler() is nil with telemetry.metrics.prometheus on")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/metrics", nil))
	return rec
}

// helpOf returns the escaped HELP text of an instrument: its description from the catalogue with
// backslash and newline escaped.
func helpOf(t *testing.T, name string) string {
	t.Helper()
	for _, s := range telemetry.Catalogue() {
		if s.Name == name {
			return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s.Description)
		}
	}
	t.Fatalf("the catalogue has no %s", name)
	return ""
}

func fmtBound(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// histogramText is the exposition of one histogram series: cumulative buckets, +Inf, sum, count.
func histogramText(name, labels string, bounds []float64, cumulative []int, sum string, count int) string {
	var b strings.Builder
	for i, le := range bounds {
		b.WriteString(name + `_bucket{` + labels + `,le="` + fmtBound(le) + `"} ` + strconv.Itoa(cumulative[i]) + "\n")
	}
	b.WriteString(name + `_bucket{` + labels + `,le="+Inf"} ` + strconv.Itoa(count) + "\n")
	b.WriteString(name + `_sum{` + labels + `} ` + sum + "\n")
	b.WriteString(name + `_count{` + labels + `} ` + strconv.Itoa(count) + "\n")
	return b.String()
}

func repeat(n, v int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// dropFamilies removes the families whose name starts with a prefix: the self-observation
// instruments read process-wide counters that other tests move.
func dropFamilies(body string, prefixes ...string) string {
	var out []string
	skip := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# HELP ") {
			name := strings.Fields(line)[2]
			skip = false
			for _, p := range prefixes {
				if strings.HasPrefix(name, p) {
					skip = true
				}
			}
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// TestPrometheusExposition is T-902: the text exposition 0.0.4 of a fixed set of measurements, as a
// whole: HELP and TYPE lines, sorting, escaping, cumulative buckets ending in +Inf, _sum and _count,
// _total, float formatting, target_info and the content type.
func TestPrometheusExposition(t *testing.T) {
	m := scrapeMetrics(t, telemetry.Sources{Sessions: func() (int64, bool) { return 5, true }})

	model := "quo\"te\\back\nnl" // a label value with a quote, a backslash and a newline
	slow := chatRequest(model)
	fast := chatRequest(model)
	fast.Duration, fast.Call = 7812500*time.Nanosecond, 7812500*time.Nanosecond // 2^-7 s: the first bucket
	slow.Duration, slow.Call = 128*time.Second, 128*time.Second                 // beyond the last bound
	m.ObserveRequest(fast)
	m.ObserveRequest(slow)

	rec := scrape(t, m, http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	body := dropFamilies(rec.Body.String(), "pandokos_telemetry_", "pandokos_log_")
	body = regexp.MustCompile(`(?m)^# HELP target_info .*$`).ReplaceAllString(body, "# HELP target_info {{any}}")

	escaped := `quo\"te\\back\nnl`
	genai := `gen_ai_operation_name="chat",gen_ai_provider_name="acme",gen_ai_request_model="` + escaped + `"`
	durationBounds := []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}
	tokenBounds := []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864}

	var w strings.Builder
	family := func(name, otel, typ string) {
		w.WriteString("# HELP " + name + " " + helpOf(t, otel) + "\n# TYPE " + name + " " + typ + "\n")
	}
	family("gen_ai_client_operation_duration_seconds", "gen_ai.client.operation.duration", "histogram")
	w.WriteString(histogramText("gen_ai_client_operation_duration_seconds", genai, durationBounds, repeat(14, 1), "128.0078125", 2))
	family("gen_ai_client_token_usage", "gen_ai.client.token.usage", "histogram")
	w.WriteString(histogramText("gen_ai_client_token_usage", genai+`,gen_ai_token_type="input"`, tokenBounds,
		append(repeat(4, 0), repeat(10, 2)...), "200", 2))
	w.WriteString(histogramText("gen_ai_client_token_usage", genai+`,gen_ai_token_type="output"`, tokenBounds,
		append(repeat(3, 0), repeat(11, 2)...), "40", 2))
	family("http_server_request_duration_seconds", "http.server.request.duration", "histogram")
	w.WriteString(histogramText("http_server_request_duration_seconds",
		`http_request_method="POST",http_response_status_code="200",http_route="/{provider}/v1/chat/completions",url_scheme="https"`,
		durationBounds, repeat(14, 1), "128.0078125", 2))
	family("pandokos_gateway_requests_total", "pandokos.gateway.requests", "counter")
	w.WriteString(`pandokos_gateway_requests_total{` + genai + `,pandokos_api_family="openai",pandokos_limit_state="limited",pandokos_outcome="ok"} 2` + "\n")
	family("pandokos_gen_ai_client_cost_total", "pandokos.gen_ai.client.cost", "counter")
	w.WriteString(`pandokos_gen_ai_client_cost_total{` + genai + `,pandokos_currency="EUR"} 0.5` + "\n")
	family("pandokos_sessions_open", "pandokos.sessions.open", "gauge")
	w.WriteString("pandokos_sessions_open 5\n")
	w.WriteString("# HELP target_info {{any}}\n# TYPE target_info gauge\n")
	w.WriteString(`target_info{deployment_environment="test",service_instance_id="instance-1",service_name="pandokos",service_version="v9.9.9"} 1` + "\n")

	if got, wantBody := strings.TrimRight(body, "\n"), strings.TrimRight(w.String(), "\n"); got != wantBody {
		t.Errorf("the exposition differs from the golden text.\n--- got ---\n%s\n--- want ---\n%s", got, wantBody)
	}
	if !strings.HasSuffix(rec.Body.String(), "\n") {
		t.Error("the exposition does not end with a newline")
	}
	if strings.Contains(rec.Body.String(), "NaN") || regexp.MustCompile(`(?m)^[^#].* [0-9]{13,}$`).MatchString(rec.Body.String()) {
		t.Error("the exposition has a timestamp or a NaN")
	}

	t.Run("HEAD answers without a body, other methods 405", func(t *testing.T) {
		head := scrape(t, m, http.MethodHead)
		if head.Code != http.StatusOK || head.Body.Len() != 0 {
			t.Errorf("HEAD: %d with %d bytes", head.Code, head.Body.Len())
		}
		if head.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
			t.Errorf("HEAD content type %q", head.Header().Get("Content-Type"))
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			rec := scrape(t, m, method)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: %d, want 405", method, rec.Code)
			}
			if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
				t.Errorf("%s: Allow %q", method, allow)
			}
		}
	})

	t.Run("a second scrape is cumulative and the scrapes are serialised", func(t *testing.T) {
		m.ObserveRequest(fast)
		rec := scrape(t, m, http.MethodGet)
		if !strings.Contains(rec.Body.String(), `pandokos_gateway_requests_total{`+genai+`,pandokos_api_family="openai",pandokos_limit_state="limited",pandokos_outcome="ok"} 3`) {
			t.Errorf("the counter did not reach 3:\n%s", rec.Body.String())
		}
		done := make(chan string, 8)
		for i := 0; i < 8; i++ {
			go func() {
				r := httptest.NewRecorder()
				m.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				b, _ := io.ReadAll(r.Body)
				done <- string(b)
			}()
		}
		for i := 0; i < 8; i++ {
			if b := <-done; !strings.Contains(b, "# TYPE pandokos_gateway_requests_total counter") {
				t.Error("a concurrent scrape lost a family")
			}
		}
	})
}

// TestPrometheusNames is T-903: rule R over every instrument of section 5.3 gives the names of the
// table of docs/reference/metrics.md, which is read from the file.
func TestPrometheusNames(t *testing.T) {
	rows := metricsRows(t)
	if len(rows) == 0 {
		t.Fatal("docs/reference/metrics.md has no table")
	}
	for _, w := range wantCatalogue {
		row, ok := rows[w.Name]
		if !ok {
			t.Errorf("%s: docs/reference/metrics.md has no row for %s", w.ID, w.Name)
			continue
		}
		if !contains(row, w.Prom) {
			t.Errorf("%s (%s): the row has no Prometheus name %q: %v", w.ID, w.Name, w.Prom, row)
		}
	}
	// The catalogue computes the same names.
	for i, spec := range telemetry.Catalogue() {
		if i < len(wantCatalogue) && spec.PrometheusName != wantCatalogue[i].Prom {
			t.Errorf("%s: PrometheusName %q, want %q", spec.Name, spec.PrometheusName, wantCatalogue[i].Prom)
		}
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
