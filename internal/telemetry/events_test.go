package telemetry_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestEventInstruments is T-1048: M52 to M58 exist with their kinds, units and closed attribute
// values and report the snapshot of the relay; none has a tenant attribute; the retention and the
// exports have the new values of M44 and M46; the signing secret is redacted by its shape.
func TestEventInstruments(t *testing.T) {
	spec := map[string]telemetry.InstrumentSpec{}
	for _, s := range telemetry.Catalogue() {
		spec[s.Name] = s
	}
	for name, want := range map[string]struct {
		kind, unit string
		attrs      []string
	}{
		"pandokos.events.deliveries":        {"OC", "{delivery}", []string{"pandokos.delivery.outcome"}},
		"pandokos.events.delivery.duration": {"H", "s", []string{"pandokos.result"}},
		"pandokos.events.dead_letters":      {"OC", "{event}", nil},
		"pandokos.events.expired":           {"OC", "{event}", nil},
		"pandokos.events.destinations":      {"G", "{destination}", []string{"pandokos.destination.state"}},
		"pandokos.events.lag":               {"G", "s", nil},
		"pandokos.events.relay.passes":      {"OC", "{pass}", []string{"pandokos.result"}},
	} {
		got, ok := spec[name]
		if !ok || got.Kind != want.kind || got.Unit != want.unit || got.Description == "" || !slices.Equal(got.Attributes, want.attrs) {
			t.Errorf("%s: %+v (in the catalogue: %v), want kind %s, unit %s, attributes %v", name, got, ok, want.kind, want.unit, want.attrs)
		}
	}
	snap := newRig(t, withFullSources).collect()
	for outcome, n := range map[string]float64{"delivered": 120, "status": 7, "redirect": 1, "timeout": 3, "connection": 2, "tls": 1, "blocked": 1} {
		expectValue(t, snap, "pandokos.events.deliveries", n, "pandokos.delivery.outcome", outcome)
	}
	expectValue(t, snap, "pandokos.events.dead_letters", 4)
	expectValue(t, snap, "pandokos.events.expired", 9)
	expectValue(t, snap, "pandokos.events.destinations", 6, "pandokos.destination.state", "enabled")
	expectValue(t, snap, "pandokos.events.destinations", 2, "pandokos.destination.state", "failing")
	expectValue(t, snap, "pandokos.events.destinations", 3, "pandokos.destination.state", "disabled")
	expectValue(t, snap, "pandokos.events.lag", 90)
	expectValue(t, snap, "pandokos.events.relay.passes", 300, "pandokos.result", "ok")
	expectValue(t, snap, "pandokos.events.relay.passes", 2, "pandokos.result", "failed")

	// The attribute values are closed, and no attribute names a tenant, a destination, a URL or an event.
	if !slices.Equal(telemetry.DeliveryOutcomes, []string{"delivered", "status", "redirect", "timeout", "connection", "tls", "blocked"}) ||
		!slices.Equal(telemetry.DestinationStates, []string{"enabled", "failing", "disabled"}) {
		t.Errorf("the closed values: %v %v", telemetry.DeliveryOutcomes, telemetry.DestinationStates)
	}
	allowed := map[string]map[string]bool{"pandokos.delivery.outcome": {}, "pandokos.destination.state": {"enabled": true, "failing": true, "disabled": true}, "pandokos.result": {"ok": true, "failed": true}}
	for _, o := range telemetry.DeliveryOutcomes {
		allowed["pandokos.delivery.outcome"][o] = true
	}
	// M53 is a histogram that is recorded as it happens: it has no point before the first request
	// (it is checked below), so it is not in this list.
	for _, name := range []string{"pandokos.events.deliveries", "pandokos.events.dead_letters", "pandokos.events.expired", "pandokos.events.destinations", "pandokos.events.lag", "pandokos.events.relay.passes"} {
		if s := snap.Series[name]; s == nil || len(s.Points) == 0 {
			t.Errorf("%s has no data point", name)
			continue
		}
		for _, p := range snap.points(t, name) {
			for k, v := range p.Attrs {
				values, ok := allowed[k]
				if !ok || !values[v] {
					t.Errorf("%s has the attribute %s = %q", name, k, v)
				}
				if strings.Contains(k, "tenant") || strings.Contains(k, "destination.id") || strings.Contains(k, "url") || strings.Contains(k, "host") {
					t.Errorf("%s has the attribute %s: no tenant, no destination, no URL", name, k)
				}
			}
		}
	}

	// M44 and M46 gain values.
	for _, kind := range []string{"outbox_event", "limit_event"} {
		if !slices.Contains(telemetry.RetentionKinds, kind) {
			t.Errorf("RetentionKinds lacks %s", kind)
		}
	}
	for _, kind := range []string{"event", "platform_event"} {
		if !slices.Contains(telemetry.ExportKinds, kind) {
			t.Errorf("ExportKinds lacks %s", kind)
		}
	}
	expectValue(t, snap, "pandokos.retention.removed", 12, "pandokos.retention.kind", "outbox_event")
	expectValue(t, snap, "pandokos.retention.removed", 3, "pandokos.retention.kind", "limit_event")
	expectValue(t, snap, "pandokos.exports", 5, "pandokos.export.kind", "event", "pandokos.result", "complete")
	expectValue(t, snap, "pandokos.exports", 1, "pandokos.export.kind", "platform_event", "pandokos.result", "complete")

	// An empty snapshot reports no series with attributes.
	empty := newRig(t, func(o *telemetry.MetricsOptions) {
		o.Sources = telemetry.Sources{Events: func() telemetry.EventsStats { return telemetry.EventsStats{} }}
	}).collect()
	for _, name := range []string{"pandokos.events.deliveries", "pandokos.events.destinations", "pandokos.events.relay.passes"} {
		if m := empty.Series[name]; m != nil && len(m.Points) != 0 {
			t.Errorf("%s has %d points for an empty snapshot", name, len(m.Points))
		}
	}
	expectValue(t, empty, "pandokos.events.dead_letters", 0)
	expectValue(t, empty, "pandokos.events.lag", 0)

	// The duration of a request is recorded as it happens, by result.
	r := newRig(t, withFullSources)
	r.m.ObserveEventDelivery("ok", 120*time.Millisecond)
	r.m.ObserveEventDelivery("ok", 80*time.Millisecond)
	r.m.ObserveEventDelivery("failed", 3*time.Second)
	hist := r.collectT(t)
	h := hist.Series["pandokos.events.delivery.duration"]
	if h == nil || h.Kind != "histogram" || len(h.Points) != 2 {
		t.Fatalf("the duration histogram: %+v, want one point per result", h)
	}
	counts := map[string]uint64{}
	for _, p := range hist.points(t, "pandokos.events.delivery.duration") {
		counts[p.Attrs["pandokos.result"]] = p.Count
	}
	if counts["ok"] != 2 || counts["failed"] != 1 {
		t.Errorf("the counts of the histogram by result: %v", counts)
	}
}

// TestSigningSecretShape is the shape whsec_ of T-1048: a signing secret is redacted wherever it
// appears, and the redactions are counted (M41, with T-907 for the four other shapes).
func TestSigningSecretShape(t *testing.T) {
	r := newRig(t, nil)
	const secret = "whsec_" + "qyp3AasqdwGrKndAGrKndAGrKndAGrKndAGrKndAGrI="
	bufs := &testutil.LogBuffer{}
	log := telemetry.NewLogger(bufs, "debug")
	before := redactions(t, r)
	log.Info("a delivery used "+secret+" to sign", "key", secret, "nested", map[string]string{"k": secret})
	testutil.AssertAbsent(t, "the log", bufs.String(), secret, strings.TrimPrefix(secret, "whsec_"))
	if !strings.Contains(bufs.String(), "[REDACTED]") {
		t.Errorf("nothing was redacted: %s", bufs.String())
	}
	if got := redactions(t, r) - before; got < 2 {
		t.Errorf("M41 counted %v redactions, want at least the message and the attribute", got)
	}
	// A text that only looks like it is left alone: the shape is whsec_ and 43 characters of the base64 alphabet and one =.
	for _, innocent := range []string{"whsec_short=", "the prefix whsec_ alone", "whsec_" + strings.Repeat("a", 44)} {
		bufs.Reset()
		log.Info(innocent)
		if !strings.Contains(bufs.String(), strings.Fields(innocent)[0]) {
			t.Errorf("%q was rewritten: %s", innocent, bufs.String())
		}
	}
}
