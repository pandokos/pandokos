package telemetry_test

import (
	"reflect"
	"testing"

	"github.com/pandokos/pandokos/internal/telemetry"
)

var telemetryEstimate = telemetry.EstimateObservation{ReservedTokens: 1000, SettledTokens: 600, ReservedCost: 1, SettledCost: 0.5, CostComparable: true}

// TestBucketBoundaries is T-899: the histograms carry the boundaries of section 5.4 exactly.
func TestBucketBoundaries(t *testing.T) {
	duration := []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}
	tokens := []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864}
	ratio := []float64{0.1, 0.25, 0.5, 0.75, 0.9, 1, 1.1, 1.25, 1.5, 2, 4}

	r := newRig(t, nil)
	q := streamRequest("model-a")
	q.Estimate = &telemetryEstimate
	snap := r.observe(q)

	for name, bounds := range map[string][]float64{
		"http.server.request.duration":                duration, // the GenAI boundaries, not the HTTP advice (O-5)
		"gen_ai.client.operation.duration":            duration,
		"gen_ai.client.operation.time_to_first_chunk": duration,
		"gen_ai.client.token.usage":                   tokens,
		"pandokos.limits.estimate_ratio":              ratio,
	} {
		pts := snap.points(t, name)
		if len(pts) == 0 {
			continue
		}
		for _, p := range pts {
			if !reflect.DeepEqual(p.Bounds, bounds) {
				t.Errorf("%s: boundaries %v, want %v", name, p.Bounds, bounds)
			}
			if len(p.Buckets) != len(bounds)+1 {
				t.Errorf("%s: %d buckets for %d boundaries", name, len(p.Buckets), len(bounds))
			}
		}
	}
}
