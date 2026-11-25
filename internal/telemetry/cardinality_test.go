package telemetry_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
)

// TestCardinality is T-901: 2500 distinct models on M3 give at most 2000 attribute sets, the surplus
// going to the one set otel.metric.overflow=true without losing a measurement; a request without a
// model records no model attribute; exemplars are absent from the collected data and the limit stays
// 2000 even with the SDK's environment variables set to other values.
func TestCardinality(t *testing.T) {
	t.Setenv("OTEL_METRICS_EXEMPLAR_FILTER", "always_on")
	t.Setenv("OTEL_GO_X_CARDINALITY_LIMIT", "5")
	// An export of 2000 attribute sets takes 0.5 s to 0.9 s under the race detector on an idle
	// machine: the 1 s that the rig gives a collection and its export is missed under load, and no
	// export then reaches the collector. The time of the export is not what this test is about.
	r := newRig(t, func(o *telemetry.MetricsOptions) { o.Timeout = 30 * time.Second })

	const models = 2500
	for i := 0; i < models; i++ {
		r.m.ObserveRequest(chatRequest(fmt.Sprintf("model-%04d", i)))
	}
	snap := r.collectWithin(t, time.Minute)

	var overflow, regular, counted int
	for _, p := range snap.points(t, "gen_ai.client.operation.duration") {
		if p.Attrs["otel.metric.overflow"] == "true" {
			overflow++
			if len(p.Attrs) != 1 {
				t.Errorf("the overflow set has other attributes: %v", p.Attrs)
			}
		} else if p.Attrs["gen_ai.request.model"] != "" {
			regular++
		}
		counted += int(p.Count)
	}
	if overflow != 1 {
		t.Errorf("%d overflow sets, want exactly 1", overflow)
	}
	// The SDK counts the overflow set inside the limit: 1999 sets of models and the overflow, or 2000 and it.
	if regular < 1999 || regular > 2000 {
		t.Errorf("%d attribute sets of models, want 1999 or 2000 (the limit of 2000 and not the 5 of the environment)", regular)
	}
	if counted != models {
		t.Errorf("%d measurements counted across the sets, want %d: the overflow must not lose any", counted, models)
	}
	// The request without a model is recorded on a Metrics of its own: after 2500 models the instrument
	// is at its limit and every new set goes to the overflow set.
	noModel := chatRequest("")
	noModel.Provider = "acme"
	snap = newRig(t, nil).observe(noModel)
	for _, p := range snap.points(t, "pandokos.gateway.requests") {
		if p.Attrs["gen_ai.provider.name"] == "acme" && p.Attrs["pandokos.outcome"] == "ok" && !hasKey(p, "gen_ai.request.model") && p.Value >= 1 {
			return // the request without a model has a point without the model attribute
		}
	}
	t.Error("a request without a model has no point without gen_ai.request.model on M2")
}

// TestNoExemplars checks the data the collector received for exemplars (T-901, second half): the
// protobuf of every export has none, even with the exemplar filter of the environment on.
func TestNoExemplars(t *testing.T) {
	t.Setenv("OTEL_METRICS_EXEMPLAR_FILTER", "always_on")
	r := newRig(t, nil)
	r.observe(chatRequest("model-a"), streamRequest("model-a"))
	var checked int
	for _, rm := range r.c.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if h := m.GetHistogram(); h != nil {
					for _, p := range h.GetDataPoints() {
						checked++
						if len(p.GetExemplars()) != 0 {
							t.Errorf("%s has exemplars", m.GetName())
						}
					}
				}
				if s := m.GetSum(); s != nil {
					for _, p := range s.GetDataPoints() {
						if len(p.GetExemplars()) != 0 {
							t.Errorf("%s has exemplars", m.GetName())
						}
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no histogram was exported")
	}
	_ = telemetry.Catalogue
}
