package telemetry_test

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

// exercise drives every recording path of section 5.3: the pushed instruments through
// ObserveRequest, M39 through a trace provider that ends a span, M40 through one failed export and
// M41 through one redaction. The observable instruments need Sources.
func exercise(t *testing.T, r *rig) {
	t.Helper()
	tracesCollector := otlptest.New(t)
	tr, _ := newTraces(t, tracesCollector.URL(), nil)
	emit(tr, "one")
	// M41: a secret reaches a logger.
	telemetry.NewLogger(io.Discard, "info").Info("exercise", "detail", "Bearer synthetic.exercise.value")

	for _, q := range exerciseRequests() {
		r.m.ObserveRequest(q)
	}
}

// exerciseRequests is a table of observations that touches every pushed instrument.
func exerciseRequests() []telemetry.Request {
	est := chatRequest("model-b")
	est.OutputSource, est.CostKnown, est.CostAmount, est.CostCurrency = "estimated", false, 0, ""
	est.CostReason = "no_price"
	rate := chatRequest("model-a")
	rate.Status, rate.CallError, rate.UpstreamStatus, rate.CredentialID = 429, "429", 429, "cred-0001"
	rate.CostKnown, rate.InputSource, rate.OutputSource = false, "none", "none"
	refused := telemetry.Request{
		Method: "POST", Scheme: "https", Route: "/{provider}/v1/chat/completions", Status: 429, Outcome: "pandokos_error",
		ErrorCode: "rate_limited", LimitState: "limited", Provider: "acme", Family: "openai", Operation: "chat", Model: "model-a",
		Duration: 3 * time.Millisecond, InputSource: "none", OutputSource: "none",
		Refusal: &telemetry.RefusalObservation{Code: "rate_limited", Metric: "requests_per_minute", Level: "tenant", SubjectKind: "key", Scope: "model"},
	}
	settled := chatRequest("model-a")
	settled.Estimate = &telemetry.EstimateObservation{ReservedTokens: 1000, SettledTokens: 600, ReservedCost: 1, SettledCost: 0.5, CostComparable: true}
	return []telemetry.Request{streamRequest("model-a"), est, rate, refused, settled}
}

// TestInstrumentCatalogue is T-897: every instrument of section 5.3 exists with its name, kind and
// unit after one exercise of every recording path and every source, and no other instrument
// exists; the catalogue (telemetry.Catalogue) is the table of the design.
func TestInstrumentCatalogue(t *testing.T) {
	t.Run("Catalogue is the table of the design", func(t *testing.T) {
		got := telemetry.Catalogue()
		if len(got) != len(wantCatalogue) {
			t.Fatalf("Catalogue has %d instruments, want %d", len(got), len(wantCatalogue))
		}
		for i, w := range wantCatalogue {
			g := got[i]
			if g.Name != w.Name || g.Kind != w.Kind || g.Unit != w.Unit || g.PrometheusName != w.Prom {
				t.Errorf("%s: got {%s %s %s %s}, want {%s %s %s %s}", w.ID, g.Name, g.Kind, g.Unit, g.PrometheusName, w.Name, w.Kind, w.Unit, w.Prom)
			}
			if strings.TrimSpace(g.Description) == "" {
				t.Errorf("%s (%s): no description", w.ID, w.Name)
			}
			if !sameSet(g.Attributes, w.Attrs) {
				t.Errorf("%s (%s): attributes %v, want %v", w.ID, w.Name, g.Attributes, w.Attrs)
			}
		}
	})

	t.Run("every instrument exists after one exercise, and no other", func(t *testing.T) {
		c := otlptest.New(t)
		c.Fail(1) // the first export fails: M40
		r := newRigOn(t, c, withFullSources)
		exercise(t, r)
		snap := r.collect()

		wants := wantByName()
		for _, w := range wantCatalogue {
			s, ok := snap.Series[w.Name]
			if !ok {
				t.Errorf("%s: %s is not exported", w.ID, w.Name)
				continue
			}
			shape, mono := dataKind(w.Kind)
			if s.Kind != shape || (shape == "sum" && s.Monotonic != mono) {
				t.Errorf("%s (%s): exported as %s (monotonic %v), want %s", w.ID, w.Name, s.Kind, s.Monotonic, shape)
			}
			if s.Unit != w.Unit {
				t.Errorf("%s (%s): unit %q, want %q", w.ID, w.Name, s.Unit, w.Unit)
			}
			if strings.TrimSpace(s.Description) == "" {
				t.Errorf("%s (%s): no description", w.ID, w.Name)
			}
			if len(s.Points) == 0 {
				t.Errorf("%s (%s): no data point", w.ID, w.Name)
			}
		}
		for name := range snap.Series {
			if _, ok := wants[name]; !ok {
				t.Errorf("the instrument %q is not in the catalogue", name)
			}
		}
		// The catalogue's descriptions are the ones exported.
		for _, spec := range telemetry.Catalogue() {
			if s, ok := snap.Series[spec.Name]; ok && s.Description != spec.Description {
				t.Errorf("%s: exported description %q differs from the catalogue's %q", spec.Name, s.Description, spec.Description)
			}
		}
	})

	t.Run("a nil source leaves its instruments out", func(t *testing.T) {
		r := newRig(t, nil) // no Sources at all
		snap := r.observe(chatRequest("model-a"))
		for _, w := range wantCatalogue {
			id := w.ID
			sourced := len(id) == 3 && (id >= "M12" && id <= "M38" || id >= "M42" && id <= "M51") // M12 to M38 and M42 to M51 (two digits)
			if sourced {
				if _, ok := snap.Series[w.Name]; ok {
					t.Errorf("%s (%s) is exported although its source is nil", id, w.Name)
				}
			}
		}
		if len(snap.Series["pandokos.gateway.requests"].Points) == 0 {
			t.Error("the pushed instruments are missing")
		}
	})

	t.Run("sessions with ok false are not reported", func(t *testing.T) {
		r := newRig(t, func(o *telemetry.MetricsOptions) {
			o.Sources = telemetry.Sources{Sessions: func() (int64, bool) { return 99, false }}
		})
		snap := r.observe(chatRequest("model-a"))
		if _, ok := snap.Series["pandokos.sessions.open"]; ok {
			t.Error("pandokos.sessions.open is reported although the count failed")
		}
		r2 := newRig(t, func(o *telemetry.MetricsOptions) {
			o.Sources = telemetry.Sources{Sessions: func() (int64, bool) { return 17, true }}
		})
		snap2 := r2.observe(chatRequest("model-a"))
		if got := snap2.total(t, "pandokos.sessions.open", nil); got != 17 {
			t.Errorf("pandokos.sessions.open = %v, want 17", got)
		}
	})
}

func sameSet(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sortStrings(x)
	sortStrings(y)
	return reflect.DeepEqual(x, y) || (len(x) == 0 && len(y) == 0)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// metricsRows reads the instrument table of docs/reference/metrics.md: the rows (cells without
// backticks) keyed by their first cell.
func metricsRows(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "docs", "reference", "metrics.md"))
	if err != nil {
		t.Fatalf("docs/reference/metrics.md: %v", err)
	}
	rows := map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		var cells []string
		for _, c := range strings.Split(strings.Trim(line, "|"), "|") {
			cells = append(cells, strings.Trim(strings.TrimSpace(c), "`"))
		}
		if len(cells) > 0 {
			rows[cells[0]] = cells
		}
	}
	return rows
}
