package telemetry_test

import (
	"testing"

	"github.com/pandokos/pandokos/internal/telemetry"
)

func expectValue(t *testing.T, s *snapshot, name string, want float64, kv ...string) {
	t.Helper()
	pts := s.find(t, name, attrs(kv...))
	if len(pts) != 1 {
		t.Errorf("%s %v: %d points, want 1 (all: %+v)", name, kv, len(pts), s.Series[name].pointsOrNil())
		return
	}
	if !near(pts[0].Value, want) {
		t.Errorf("%s %v = %v, want %v", name, kv, pts[0].Value, want)
	}
}

// TestLedgerSource is T-916 (telemetry part): M12 to M15 report the snapshot of the ledger.
func TestLedgerSource(t *testing.T) {
	r := newRig(t, withFullSources)
	snap := r.collect()
	expectValue(t, snap, "pandokos.ledger.records", 105, "pandokos.result", "written")
	expectValue(t, snap, "pandokos.ledger.records", 7, "pandokos.result", "rejected")
	expectValue(t, snap, "pandokos.ledger.records", 3, "pandokos.result", "lost")
	expectValue(t, snap, "pandokos.ledger.admission_refused", 11)
	expectValue(t, snap, "pandokos.ledger.buffer.usage", 13, "pandokos.ledger.state", "in_use")
	expectValue(t, snap, "pandokos.ledger.buffer.usage", 5, "pandokos.ledger.state", "queued")
	expectValue(t, snap, "pandokos.ledger.buffer.limit", 256)
}

// TestSourceValues covers the sources of M16 to M38: each snapshot field is reported on the
// instrument and with the attributes of section 5.3, and the seconds come from the clock of the
// options.
func TestSourceValues(t *testing.T) {
	r := newRig(t, withFullSources)
	snap := r.collect()

	expectValue(t, snap, "pandokos.egress.blocked", 4, "pandokos.egress.reason", "private_address")
	expectValue(t, snap, "pandokos.egress.blocked", 2, "pandokos.egress.reason", "scheme")

	expectValue(t, snap, "pandokos.authn.attempts", 20, "pandokos.listener", "inference", "pandokos.authn.kind", "key", "pandokos.authn.result", "ok")
	expectValue(t, snap, "pandokos.authn.attempts", 3, "pandokos.listener", "inference", "pandokos.authn.kind", "key", "pandokos.authn.result", "failed", "pandokos.authn.reason", "revoked")
	expectValue(t, snap, "pandokos.authn.attempts", 1, "pandokos.listener", "inference", "pandokos.authn.kind", "token", "pandokos.authn.result", "unavailable")
	expectValue(t, snap, "pandokos.authn.attempts", 6, "pandokos.listener", "admin", "pandokos.authn.kind", "key", "pandokos.authn.result", "ok")
	expectValue(t, snap, "pandokos.authn.attempts", 2, "pandokos.listener", "admin", "pandokos.authn.kind", "unknown", "pandokos.authn.result", "failed", "pandokos.authn.reason", "malformed")
	for _, p := range snap.points(t, "pandokos.authn.attempts") {
		if p.Attrs["pandokos.authn.result"] != "failed" && hasKey(p, "pandokos.authn.reason") {
			t.Errorf("a result that is not a failure has a reason: %v", p.Attrs)
		}
	}

	expectValue(t, snap, "pandokos.jwks.fetches", 9, "pandokos.jwks.document", "keys", "pandokos.result", "ok")
	expectValue(t, snap, "pandokos.jwks.fetches", 2, "pandokos.jwks.document", "keys", "pandokos.result", "timeout")
	expectValue(t, snap, "pandokos.jwks.fetches", 4, "pandokos.jwks.document", "metadata", "pandokos.result", "ok")
	expectValue(t, snap, "pandokos.jwks.sources", 2, "pandokos.jwks.state", "fresh")
	expectValue(t, snap, "pandokos.jwks.sources", 1, "pandokos.jwks.state", "stale")
	expectValue(t, snap, "pandokos.jwks.sources", 1, "pandokos.jwks.state", "missing")
	expectValue(t, snap, "pandokos.jwks.staleness", 90) // fixtureNow minus the oldest success of a stale source

	expectValue(t, snap, "pandokos.signin.attempts", 30, "pandokos.signin.method", "password", "pandokos.result", "ok")
	expectValue(t, snap, "pandokos.signin.attempts", 5, "pandokos.signin.method", "password", "pandokos.result", "failed")
	expectValue(t, snap, "pandokos.signin.attempts", 2, "pandokos.signin.method", "oidc", "pandokos.result", "refused")
	expectValue(t, snap, "pandokos.account.locks", 3)
	expectValue(t, snap, "pandokos.sessions.open", 17)
	expectValue(t, snap, "pandokos.password.hashes", 2, "pandokos.password.state", "active")
	expectValue(t, snap, "pandokos.password.hashes", 6, "pandokos.password.state", "waiting")
	expectValue(t, snap, "pandokos.idempotency.replays", 8)

	expectValue(t, snap, "pandokos.authz.grants_refused", 4)
	expectValue(t, snap, "pandokos.support_actions.opened", 6)
	expectValue(t, snap, "pandokos.support_actions.ended", 3, "pandokos.support.cause", "closed")
	expectValue(t, snap, "pandokos.support_actions.ended", 2, "pandokos.support.cause", "ended")

	expectValue(t, snap, "pandokos.limits.reservations", 12)
	expectValue(t, snap, "pandokos.limits.counters", 34)
	expectValue(t, snap, "pandokos.limits.reservations_expired", 0) // reported at zero: it must stay there
	expectValue(t, snap, "pandokos.limits.reconciliations", 21, "pandokos.result", "ok")
	expectValue(t, snap, "pandokos.limits.reconciliations", 1, "pandokos.result", "failed")
	expectValue(t, snap, "pandokos.limits.reconciliation.last_duration", 1.5)
	expectValue(t, snap, "pandokos.limits.cut_lag", 45) // fixtureNow minus the last cut
	expectValue(t, snap, "pandokos.limits.events", 50, "pandokos.result", "recorded")
	expectValue(t, snap, "pandokos.limits.events", 2, "pandokos.result", "dropped")
	expectValue(t, snap, "pandokos.limits.events", 1, "pandokos.result", "failed")
	expectValue(t, snap, "pandokos.limits.rule_loads", 14, "pandokos.result", "ok")
	expectValue(t, snap, "pandokos.limits.rule_loads", 1, "pandokos.result", "failed")
	expectValue(t, snap, "pandokos.limits.admitted_unpriced", 9)
	expectValue(t, snap, "pandokos.limits.unlimited_traffic", 40, "pandokos.result", "written")
	expectValue(t, snap, "pandokos.limits.unlimited_traffic", 3, "pandokos.result", "skipped")
	expectValue(t, snap, "pandokos.limits.unlimited_traffic", 1, "pandokos.result", "failed")
}

// TestReportsInstruments is T-973: M42 to M46 exist with their kinds, units and closed attribute
// values, and report the snapshot of the rollup, of retention and of the exports (T-900 covers their
// attributes: see TestAttributesAreClosed).
func TestReportsInstruments(t *testing.T) {
	r := newRig(t, withFullSources)
	snap := r.collect()

	spec := map[string]telemetry.InstrumentSpec{}
	for _, s := range telemetry.Catalogue() {
		spec[s.Name] = s
	}
	for name, want := range map[string]struct{ kind, unit string }{
		"pandokos.reports.rollups":    {"OC", "{run}"},
		"pandokos.reports.rollup.lag": {"G", "s"},
		"pandokos.retention.removed":  {"OC", "{item}"},
		"pandokos.retention.runs":     {"OC", "{run}"},
		"pandokos.exports":            {"OC", "{export}"},
	} {
		got, ok := spec[name]
		if !ok || got.Kind != want.kind || got.Unit != want.unit || got.Description == "" {
			t.Errorf("%s: %+v (in the catalogue: %v), want kind %s unit %s", name, got, ok, want.kind, want.unit)
		}
	}
	expectValue(t, snap, "pandokos.reports.rollups", 61, "pandokos.result", "ok")
	expectValue(t, snap, "pandokos.reports.rollups", 2, "pandokos.result", "failed")
	expectValue(t, snap, "pandokos.reports.rollup.lag", 125)
	for kind, n := range map[string]float64{"usage_partition": 3, "usage_record": 41, "usage_rollup": 17, "audit_event": 23, "platform_audit_event": 5} {
		expectValue(t, snap, "pandokos.retention.removed", n, "pandokos.retention.kind", kind)
	}
	expectValue(t, snap, "pandokos.retention.runs", 7, "pandokos.result", "ok")
	expectValue(t, snap, "pandokos.retention.runs", 1, "pandokos.result", "failed")
	expectValue(t, snap, "pandokos.exports", 11, "pandokos.export.kind", "usage", "pandokos.result", "complete")
	expectValue(t, snap, "pandokos.exports", 1, "pandokos.export.kind", "usage", "pandokos.result", "aborted")
	expectValue(t, snap, "pandokos.exports", 4, "pandokos.export.kind", "audit", "pandokos.result", "complete")
	expectValue(t, snap, "pandokos.exports", 2, "pandokos.export.kind", "platform_audit", "pandokos.result", "complete")

	// No attribute names a tenant, a partition, a key or a request, and only the keys of the table.
	allowed := map[string]bool{"pandokos.result": true, "pandokos.retention.kind": true, "pandokos.export.kind": true}
	for _, name := range []string{"pandokos.reports.rollups", "pandokos.reports.rollup.lag", "pandokos.retention.removed", "pandokos.retention.runs", "pandokos.exports"} {
		for _, p := range snap.points(t, name) {
			for k := range p.Attrs {
				if !allowed[k] {
					t.Errorf("%s has the attribute %s", name, k)
				}
			}
		}
	}
	// A rollup before its first pass reports a lag of zero, and an empty snapshot reports no series
	// with attributes (nothing is invented).
	empty := newRig(t, func(o *telemetry.MetricsOptions) {
		o.Sources = telemetry.Sources{Reports: func() telemetry.ReportsStats { return telemetry.ReportsStats{} }}
	}).collect()
	expectValue(t, empty, "pandokos.reports.rollup.lag", 0)
}

// TestSourcesAreReadAtCollection: a source function is called at each collection and its values are
// the ones of that moment (the observable instruments hold no copy).
func TestSourcesAreReadAtCollection(t *testing.T) {
	var n int64
	r := newRig(t, func(o *telemetry.MetricsOptions) {
		o.Sources.Idempotency = func() int64 { n += 10; return n }
	})
	first := r.collect()
	second := r.collect()
	a, b := first.total(t, "pandokos.idempotency.replays", nil), second.total(t, "pandokos.idempotency.replays", nil)
	if !(b > a) {
		t.Errorf("the source was not read again at the next collection: %v then %v", a, b)
	}
}
