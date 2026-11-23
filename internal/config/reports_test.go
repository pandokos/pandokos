package config

import (
	"strings"
	"testing"
	"time"
)

// TestReportsRetentionKeys is T-972: the three keys of sprint 6.2, section 5.16: types, defaults,
// bounds (0 accepted; 1 refused for usage; 1201 refused), environment names, serve only, and the
// warning for an audit retention below 12 months.
func TestReportsRetentionKeys(t *testing.T) {
	envOf := envOfKeys()
	const (
		rollupKey = "reports.rollup_interval"
		usageKey  = "retention.usage_months"
		auditKey  = "retention.audit_months"
	)
	load := func(t *testing.T, kv ...string) (*Config, []Warning, error) {
		t.Helper()
		src := validSource(t)
		for i := 0; i < len(kv); i += 2 {
			src.Environ = append(src.Environ, envOf[kv[i]]+"="+kv[i+1])
		}
		return Load(src)
	}

	t.Run("the keys, their environment names, types and defaults", func(t *testing.T) {
		want := map[string]struct{ env, typ, def string }{
			rollupKey: {"PANDOKOS_REPORTS_ROLLUP_INTERVAL", "duration", "1m"},
			usageKey:  {"PANDOKOS_RETENTION_USAGE_MONTHS", "integer", "0"},
			auditKey:  {"PANDOKOS_RETENTION_AUDIT_MONTHS", "integer", "0"},
		}
		found := 0
		for _, k := range Keys() {
			w, ok := want[k.Name]
			if !ok {
				continue
			}
			found++
			if k.Env != w.env || k.Type != w.typ || k.Default != w.def || k.Description == "" {
				t.Errorf("%s: %+v, want env %s, type %s, default %q", k.Name, k, w.env, w.typ, w.def)
			}
		}
		if found != len(want) {
			t.Errorf("%d of the three keys are in Keys()", found)
		}
		c, ws, err := load(t)
		if err != nil {
			t.Fatal(err)
		}
		if time.Duration(c.Reports.RollupInterval) != time.Minute || c.Retention.UsageMonths != 0 || c.Retention.AuditMonths != 0 {
			t.Errorf("defaults: %+v %+v", c.Reports, c.Retention)
		}
		for _, w := range ws {
			if w.Key == rollupKey || w.Key == usageKey || w.Key == auditKey {
				t.Errorf("a warning with the defaults: %+v", w)
			}
		}
	})

	t.Run("reports.rollup_interval is 10s to 1h", func(t *testing.T) {
		for _, bad := range []string{"0s", "9s", "9999ms", "1h1s", "2h", "-1m", "often"} {
			if _, _, err := load(t, rollupKey, bad); !hasProblem(err, rollupKey) {
				t.Errorf("%s: %v", bad, err)
			}
		}
		for good, want := range map[string]time.Duration{"10s": 10 * time.Second, "1m": time.Minute, "1h": time.Hour, "90s": 90 * time.Second} {
			c, _, err := load(t, rollupKey, good)
			if err != nil || time.Duration(c.Reports.RollupInterval) != want {
				t.Errorf("%s: %v %v", good, c, err)
			}
		}
	})

	t.Run("retention.usage_months is 0 or 2 to 120", func(t *testing.T) {
		for _, bad := range []string{"1", "-1", "121", "1201", "2.5", "many"} {
			if _, _, err := load(t, usageKey, bad); !hasProblem(err, usageKey) {
				t.Errorf("%s: %v", bad, err)
			}
		}
		for good, want := range map[string]int{"0": 0, "2": 2, "12": 12, "120": 120} {
			c, _, err := load(t, usageKey, good)
			if err != nil || c.Retention.UsageMonths != want {
				t.Errorf("%s: %v", good, err)
			}
		}
	})

	t.Run("retention.audit_months is 0 or 1 to 1200", func(t *testing.T) {
		for _, bad := range []string{"-1", "1201", "5000", "0.5", "forever"} {
			if _, _, err := load(t, auditKey, bad); !hasProblem(err, auditKey) {
				t.Errorf("%s: %v", bad, err)
			}
		}
		for good, want := range map[string]int{"0": 0, "1": 1, "12": 12, "1200": 1200} {
			c, _, err := load(t, auditKey, good)
			if err != nil || c.Retention.AuditMonths != want {
				t.Errorf("%s: %v", good, err)
			}
		}
	})

	t.Run("audit retention below 12 months is a start-up warning", func(t *testing.T) {
		for _, below := range []string{"1", "6", "11"} {
			_, ws, err := load(t, auditKey, below)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, w := range ws {
				if w.Key == auditKey {
					n++
					if !strings.Contains(w.Message, "12") {
						t.Errorf("the warning does not say what is short: %q", w.Message)
					}
				}
			}
			if n != 1 {
				t.Errorf("%s: %d warnings, want one", below, n)
			}
		}
		for _, ok := range []string{"0", "12", "60"} {
			_, ws, err := load(t, auditKey, ok)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range ws {
				if w.Key == auditKey {
					t.Errorf("%s: unexpected warning %q", ok, w.Message)
				}
			}
		}
		// The usage key never warns.
		_, ws, _ := load(t, usageKey, "2")
		for _, w := range ws {
			if w.Key == usageKey {
				t.Errorf("unexpected warning %q", w.Message)
			}
		}
	})

	t.Run("serve only: the other purposes ignore the values", func(t *testing.T) {
		src := validMigrateSource(t)
		src.Environ = append(src.Environ, envOf[rollupKey]+"=1s", envOf[usageKey]+"=1", envOf[auditKey]+"=9999")
		_, ws := mustLoad(t, src)
		for _, w := range ws {
			if w.Key == rollupKey || w.Key == usageKey || w.Key == auditKey {
				t.Errorf("migrate warns about %s: %q", w.Key, w.Message)
			}
		}
		src = validInitSource(t)
		src.Environ = append(src.Environ, envOf[rollupKey]+"=1s", envOf[usageKey]+"=1", envOf[auditKey]+"=9999")
		mustLoad(t, src)
	})

	t.Run("the problems name the keys and all are reported together", func(t *testing.T) {
		_, _, err := load(t, rollupKey, "1s", usageKey, "1", auditKey, "1201")
		for _, k := range []string{rollupKey, usageKey, auditKey} {
			if !hasProblem(err, k) {
				t.Errorf("no problem for %s: %v", k, err)
			}
		}
	})
}
