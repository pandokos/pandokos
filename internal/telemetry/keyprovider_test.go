package telemetry_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
)

// TestKeyProviderInstruments is T-1079: M59 to M62 exist with their kinds, units and Prometheus
// names, report the snapshot of the provider and the time of its requests, take every attribute
// value from the closed lists and stay under the cap on attribute sets; M61 and M62 are absent
// without a transit provider and M62 for a token that does not expire.
func TestKeyProviderInstruments(t *testing.T) {
	spec := map[string]telemetry.InstrumentSpec{}
	for _, s := range telemetry.Catalogue() {
		spec[s.Name] = s
	}
	for name, want := range map[string]struct {
		kind, unit, prom string
		attrs            []string
	}{
		"pandokos.vault.provider.requests":         {"OC", "{request}", "pandokos_vault_provider_requests_total", []string{"pandokos.kek.operation", "pandokos.kek.outcome"}},
		"pandokos.vault.provider.request.duration": {"H", "s", "pandokos_vault_provider_request_duration_seconds", []string{"pandokos.kek.operation"}},
		"pandokos.vault.provider.up":               {"G", "{provider}", "pandokos_vault_provider_up", nil},
		"pandokos.vault.provider.token.ttl":        {"G", "s", "pandokos_vault_provider_token_ttl_seconds", nil},
	} {
		got, ok := spec[name]
		if !ok || got.Kind != want.kind || got.Unit != want.unit || got.PrometheusName != want.prom || got.Description == "" || !slices.Equal(got.Attributes, want.attrs) {
			t.Errorf("%s: %+v (in the catalogue: %v), want kind %s, unit %s, Prometheus name %s, attributes %v", name, got, ok, want.kind, want.unit, want.prom, want.attrs)
		}
	}
	if !slices.Equal(telemetry.KeyOperations, []string{"wrap", "unwrap", "describe", "identify", "lookup", "renew", "login"}) ||
		!slices.Equal(telemetry.KeyOutcomes, []string{"ok", "unavailable", "denied", "not_found", "rejected", "refused"}) {
		t.Errorf("the closed values: %v %v", telemetry.KeyOperations, telemetry.KeyOutcomes)
	}

	t.Run("the snapshot of the provider", func(t *testing.T) {
		snap := newRig(t, withFullSources).collect()
		for c, n := range map[[2]string]float64{
			{"wrap", "ok"}: 100, {"wrap", "unavailable"}: 5, {"wrap", "refused"}: 7, {"unwrap", "ok"}: 50, {"unwrap", "rejected"}: 2,
			{"describe", "ok"}: 1, {"identify", "ok"}: 1, {"lookup", "ok"}: 1, {"renew", "ok"}: 3, {"login", "denied"}: 1,
		} {
			expectValue(t, snap, "pandokos.vault.provider.requests", n, "pandokos.kek.operation", c[0], "pandokos.kek.outcome", c[1])
		}
		expectValue(t, snap, "pandokos.vault.provider.up", 1)
		expectValue(t, snap, "pandokos.vault.provider.token.ttl", 90)
		if s := snap.Series["pandokos.vault.provider.requests"]; s == nil || len(s.Points) != 10 {
			t.Errorf("M59 has %v points, want only the 10 of the snapshot", s)
		}
	})

	t.Run("the gauge says 0 while the refusal is in force", func(t *testing.T) {
		snap := newRig(t, func(o *telemetry.MetricsOptions) {
			o.Sources = telemetry.Sources{KeyProvider: func() telemetry.KeyProviderStats {
				return telemetry.KeyProviderStats{Up: false, TokenExpires: true, TokenTTL: 5 * time.Second}
			}}
		}).collect()
		expectValue(t, snap, "pandokos.vault.provider.up", 0)
		expectValue(t, snap, "pandokos.vault.provider.token.ttl", 5)
	})

	t.Run("the time of a request is recorded as it happens, by operation", func(t *testing.T) {
		r := newRig(t, withFullSources)
		r.m.ObserveKeyProviderRequest("wrap", 3*time.Millisecond)
		r.m.ObserveKeyProviderRequest("wrap", 5*time.Millisecond)
		r.m.ObserveKeyProviderRequest("unwrap", 2*time.Second)
		snap := r.collectT(t)
		h := snap.Series["pandokos.vault.provider.request.duration"]
		if h == nil || h.Kind != "histogram" || len(h.Points) != 2 {
			t.Fatalf("the duration histogram: %+v, want one point per operation", h)
		}
		counts := map[string]uint64{}
		for _, p := range snap.points(t, "pandokos.vault.provider.request.duration") {
			counts[p.Attrs["pandokos.kek.operation"]] = p.Count
		}
		if counts["wrap"] != 2 || counts["unwrap"] != 1 {
			t.Errorf("the counts by operation: %v", counts)
		}
	})

	t.Run("the attribute values are closed and the sets stay under the cap", func(t *testing.T) {
		all := map[[2]string]int64{}
		for _, op := range telemetry.KeyOperations {
			for _, out := range telemetry.KeyOutcomes {
				all[[2]string{op, out}] = 1
			}
		}
		r := newRig(t, func(o *telemetry.MetricsOptions) {
			o.Sources = telemetry.Sources{KeyProvider: func() telemetry.KeyProviderStats {
				return telemetry.KeyProviderStats{Requests: all, Up: true, TokenExpires: true, TokenTTL: time.Minute}
			}}
		})
		for _, op := range telemetry.KeyOperations {
			r.m.ObserveKeyProviderRequest(op, time.Millisecond)
		}
		snap := r.collectT(t)
		if n := len(snap.points(t, "pandokos.vault.provider.requests")); n != 42 {
			t.Errorf("M59 has %d attribute sets, want at most and exactly 42 (7 operations by 6 outcomes)", n)
		}
		if n := len(snap.points(t, "pandokos.vault.provider.request.duration")); n != 7 {
			t.Errorf("M60 has %d attribute sets, want 7", n)
		}
		for _, name := range []string{"pandokos.vault.provider.requests", "pandokos.vault.provider.request.duration", "pandokos.vault.provider.up", "pandokos.vault.provider.token.ttl"} {
			for _, p := range snap.points(t, name) {
				for k, v := range p.Attrs {
					if !closedValue(name, k, v, nil) {
						t.Errorf("%s has the attribute %s = %q", name, k, v)
					}
					if strings.Contains(k, "address") || strings.Contains(k, "namespace") || strings.Contains(k, "key.name") || strings.Contains(k, "version") || strings.Contains(k, "tenant") {
						t.Errorf("%s has the attribute %s: no address, namespace, key or version", name, k)
					}
				}
			}
		}
		for _, name := range []string{"pandokos.vault.provider.up", "pandokos.vault.provider.token.ttl"} {
			for _, p := range snap.points(t, name) {
				if len(p.Attrs) != 0 {
					t.Errorf("%s has attributes %v, want none", name, p.Attrs)
				}
			}
		}
	})

	t.Run("without a transit provider M61 and M62 are absent", func(t *testing.T) {
		snap := newRig(t, func(o *telemetry.MetricsOptions) {
			o.Sources = telemetry.Sources{Lifecycle: func() telemetry.LifecycleStats { return telemetry.LifecycleStats{} }}
		}).collect()
		for _, name := range []string{"pandokos.vault.provider.requests", "pandokos.vault.provider.up", "pandokos.vault.provider.token.ttl"} {
			if s := snap.Series[name]; s != nil {
				t.Errorf("%s is exported without a transit provider", name)
			}
		}
	})

	t.Run("M62 is absent for a token that does not expire", func(t *testing.T) {
		snap := newRig(t, func(o *telemetry.MetricsOptions) {
			o.Sources = telemetry.Sources{KeyProvider: func() telemetry.KeyProviderStats {
				return telemetry.KeyProviderStats{Up: true, TokenExpires: false}
			}}
		}).collect()
		expectValue(t, snap, "pandokos.vault.provider.up", 1)
		if s := snap.Series["pandokos.vault.provider.token.ttl"]; s != nil {
			t.Errorf("M62 is exported for a token that does not expire: %+v", s)
		}
	})
}
