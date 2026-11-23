package telemetry_test

import (
	"slices"
	"testing"

	"github.com/pandokos/pandokos/internal/telemetry"
)

// TestLifecycleInstruments is T-1009: M47 to M51 exist with their kinds, units and closed attribute
// values and report the snapshot of the deletion job and of the re-wrap job; the retention and the
// exports have the new values of M44 and M46. T-900 covers the catalogue as a whole.
func TestLifecycleInstruments(t *testing.T) {
	spec := map[string]telemetry.InstrumentSpec{}
	for _, s := range telemetry.Catalogue() {
		spec[s.Name] = s
	}
	for name, want := range map[string]struct {
		kind, unit string
		attrs      []string
	}{
		"pandokos.tenant_deletion.steps":  {"OC", "{step}", []string{"pandokos.deletion.step", "pandokos.result"}},
		"pandokos.tenant_deletion.open":   {"G", "{deletion}", []string{"pandokos.deletion.state"}},
		"pandokos.vault.rewraps":          {"OC", "{key}", []string{"pandokos.result"}},
		"pandokos.vault.kek.versions":     {"G", "{version}", []string{"pandokos.kek.state"}},
		"pandokos.vault.erasures.pending": {"G", "{deletion}", nil},
	} {
		got, ok := spec[name]
		if !ok || got.Kind != want.kind || got.Unit != want.unit || got.Description == "" || !slices.Equal(got.Attributes, want.attrs) {
			t.Errorf("%s: %+v (in the catalogue: %v), want kind %s, unit %s, attributes %v", name, got, ok, want.kind, want.unit, want.attrs)
		}
	}

	snap := newRig(t, withFullSources).collect()
	for _, c := range []struct {
		step, result string
		n            float64
	}{{"begin", "ok", 4}, {"access", "ok", 4}, {"secrets", "ok", 4}, {"configuration", "ok", 3}, {"usage", "ok", 9}, {"usage", "failed", 1}, {"audit", "ok", 3}, {"complete", "ok", 2}, {"complete", "failed", 1}} {
		expectValue(t, snap, "pandokos.tenant_deletion.steps", c.n, "pandokos.deletion.step", c.step, "pandokos.result", c.result)
	}
	expectValue(t, snap, "pandokos.tenant_deletion.open", 2, "pandokos.deletion.state", "scheduled")
	expectValue(t, snap, "pandokos.tenant_deletion.open", 1, "pandokos.deletion.state", "running")
	expectValue(t, snap, "pandokos.vault.rewraps", 40, "pandokos.result", "ok")
	expectValue(t, snap, "pandokos.vault.rewraps", 2, "pandokos.result", "failed")
	expectValue(t, snap, "pandokos.vault.kek.versions", 1, "pandokos.kek.state", "active")
	expectValue(t, snap, "pandokos.vault.kek.versions", 1, "pandokos.kek.state", "retiring")
	expectValue(t, snap, "pandokos.vault.kek.versions", 3, "pandokos.kek.state", "retired")
	expectValue(t, snap, "pandokos.vault.erasures.pending", 5)

	// The attribute values are closed: the steps and the states of the design, nothing else; no
	// attribute names a tenant, a request, a key or a version.
	allowed := map[string]map[string]bool{
		"pandokos.deletion.step":  {},
		"pandokos.deletion.state": {"scheduled": true, "running": true},
		"pandokos.kek.state":      {"active": true, "retiring": true, "retired": true},
		"pandokos.result":         {"ok": true, "failed": true},
	}
	for _, s := range []string{"begin", "access", "secrets", "configuration", "usage", "audit", "complete"} {
		allowed["pandokos.deletion.step"][s] = true
	}
	if !slices.Equal(telemetry.DeletionSteps, []string{"begin", "access", "secrets", "configuration", "usage", "audit", "complete"}) ||
		!slices.Equal(telemetry.DeletionStates, []string{"scheduled", "running"}) || !slices.Equal(telemetry.KEKStates, []string{"active", "retiring", "retired"}) {
		t.Errorf("the closed values: %v %v %v", telemetry.DeletionSteps, telemetry.DeletionStates, telemetry.KEKStates)
	}
	for _, name := range []string{"pandokos.tenant_deletion.steps", "pandokos.tenant_deletion.open", "pandokos.vault.rewraps", "pandokos.vault.kek.versions", "pandokos.vault.erasures.pending"} {
		for _, p := range snap.points(t, name) {
			for k, v := range p.Attrs {
				values, ok := allowed[k]
				if !ok || !values[v] {
					t.Errorf("%s has the attribute %s = %q", name, k, v)
				}
			}
		}
	}

	// M44 and M46 gain values: the records kept for deleted tenants leave with retention, and the
	// kept log is exported.
	for _, kind := range []string{"deleted_tenant_usage_record", "deleted_tenant_audit_event"} {
		if !slices.Contains(telemetry.RetentionKinds, kind) {
			t.Errorf("RetentionKinds lacks %s", kind)
		}
	}
	if !slices.Contains(telemetry.ExportKinds, "deleted_tenant_audit") {
		t.Error("ExportKinds lacks deleted_tenant_audit")
	}
	expectValue(t, snap, "pandokos.retention.removed", 6, "pandokos.retention.kind", "deleted_tenant_usage_record")
	expectValue(t, snap, "pandokos.retention.removed", 8, "pandokos.retention.kind", "deleted_tenant_audit_event")
	expectValue(t, snap, "pandokos.exports", 3, "pandokos.export.kind", "deleted_tenant_audit", "pandokos.result", "complete")

	// An empty snapshot reports no series with attributes: nothing is invented.
	empty := newRig(t, func(o *telemetry.MetricsOptions) {
		o.Sources = telemetry.Sources{Lifecycle: func() telemetry.LifecycleStats { return telemetry.LifecycleStats{} }}
	}).collect()
	for _, name := range []string{"pandokos.tenant_deletion.steps", "pandokos.tenant_deletion.open", "pandokos.vault.rewraps", "pandokos.vault.kek.versions"} {
		if m := empty.Series[name]; m != nil && len(m.Points) != 0 { // an instrument with no observation is not exported at all
			t.Errorf("%s has %d points for an empty snapshot", name, len(m.Points))
		}
	}
	expectValue(t, empty, "pandokos.vault.erasures.pending", 0)
}
