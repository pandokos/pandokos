package telemetry_test

import (
	"context"
	"sort"
	"strconv"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

// The tests of this package read what the meter provider recorded through the one door the product
// has besides the scrape endpoint: an OTLP export to the test collector with a short interval. A
// "collect" waits for two exports that began after the recording, so the observable instruments
// were read at least once after it and the cumulative values include everything recorded.

const metricsInterval = 25 * time.Millisecond

// point is one data point of a collected instrument.
type point struct {
	Attrs map[string]string
	Value float64 // sums and gauges

	Count   uint64 // histograms
	Sum     float64
	Bounds  []float64
	Buckets []uint64
}

// series is one collected instrument.
type series struct {
	Name, Unit, Description string
	Kind                    string // "histogram", "sum" or "gauge"
	Monotonic               bool
	Points                  []point
}

// snapshot is what one export held.
type snapshot struct {
	Resource                       map[string]string
	Scope, ScopeVersion, SchemaURL string
	Series                         map[string]*series
}

// names returns the instrument names, sorted.
func (s *snapshot) names() []string {
	out := make([]string, 0, len(s.Series))
	for n := range s.Series {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// points returns the data points of an instrument: the attribute maps and values. The instrument
// not being there is a failure of the test that asks.
func (s *snapshot) points(t testing.TB, name string) []point {
	t.Helper()
	m, ok := s.Series[name]
	if !ok {
		t.Errorf("the instrument %s was not exported (exported: %v)", name, s.names())
		return nil
	}
	return m.Points
}

// find returns the points whose attributes include every pair of want.
func (s *snapshot) find(t testing.TB, name string, want map[string]string) []point {
	t.Helper()
	var out []point
	for _, p := range s.points(t, name) {
		if includes(p.Attrs, want) {
			out = append(out, p)
		}
	}
	return out
}

// total returns the sum of the values (or of the counts, for a histogram) of the points of an
// instrument whose attributes include want.
func (s *snapshot) total(t testing.TB, name string, want map[string]string) float64 {
	t.Helper()
	var sum float64
	for _, p := range s.find(t, name, want) {
		if m := s.Series[name]; m != nil && m.Kind == "histogram" {
			sum += float64(p.Count)
		} else {
			sum += p.Value
		}
	}
	return sum
}

func includes(have, want map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func attrMap(kvs []*commonpb.KeyValue) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		switch v := kv.GetValue().GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			m[kv.Key] = v.StringValue
		case *commonpb.AnyValue_BoolValue:
			m[kv.Key] = strconv.FormatBool(v.BoolValue)
		case *commonpb.AnyValue_IntValue:
			m[kv.Key] = strconv.FormatInt(v.IntValue, 10)
		case *commonpb.AnyValue_DoubleValue:
			m[kv.Key] = strconv.FormatFloat(v.DoubleValue, 'g', -1, 64)
		default:
			m[kv.Key] = ""
		}
	}
	return m
}

func number(p *metricspb.NumberDataPoint) float64 {
	if _, ok := p.GetValue().(*metricspb.NumberDataPoint_AsInt); ok {
		return float64(p.GetAsInt())
	}
	return p.GetAsDouble()
}

// parse flattens one export.
func parse(rm *metricspb.ResourceMetrics) *snapshot {
	s := &snapshot{Resource: attrMap(rm.GetResource().GetAttributes()), Series: map[string]*series{}}
	for _, sm := range rm.GetScopeMetrics() {
		s.Scope, s.ScopeVersion, s.SchemaURL = sm.GetScope().GetName(), sm.GetScope().GetVersion(), sm.GetSchemaUrl()
		for _, m := range sm.GetMetrics() {
			se := &series{Name: m.GetName(), Unit: m.GetUnit(), Description: m.GetDescription()}
			switch d := m.GetData().(type) {
			case *metricspb.Metric_Sum:
				se.Kind, se.Monotonic = "sum", d.Sum.GetIsMonotonic()
				for _, p := range d.Sum.GetDataPoints() {
					se.Points = append(se.Points, point{Attrs: attrMap(p.GetAttributes()), Value: number(p)})
				}
			case *metricspb.Metric_Gauge:
				se.Kind = "gauge"
				for _, p := range d.Gauge.GetDataPoints() {
					se.Points = append(se.Points, point{Attrs: attrMap(p.GetAttributes()), Value: number(p)})
				}
			case *metricspb.Metric_Histogram:
				se.Kind = "histogram"
				for _, p := range d.Histogram.GetDataPoints() {
					se.Points = append(se.Points, point{
						Attrs: attrMap(p.GetAttributes()), Count: p.GetCount(), Sum: p.GetSum(),
						Bounds: p.GetExplicitBounds(), Buckets: p.GetBucketCounts(),
					})
				}
			}
			s.Series[se.Name] = se
		}
	}
	return s
}

// rig is a Metrics exporting to a test collector every metricsInterval.
type rig struct {
	t    *testing.T
	m    *telemetry.Metrics
	c    *otlptest.Collector
	logs *testutil.LogBuffer
	res  *telemetry.Resource
}

// newRig starts a Metrics over a collector; mutate adjusts the options. The Metrics is shut down
// when the test ends.
func newRig(t *testing.T, mutate func(*telemetry.MetricsOptions)) *rig {
	t.Helper()
	c := otlptest.New(t)
	return newRigOn(t, c, mutate)
}

func newRigOn(t *testing.T, c *otlptest.Collector, mutate func(*telemetry.MetricsOptions)) *rig {
	t.Helper()
	logs := &testutil.LogBuffer{}
	res, err := telemetry.NewResource(map[string]string{"deployment.environment": "test"}, serviceVersion)
	if err != nil {
		t.Fatal(err)
	}
	o := telemetry.MetricsOptions{
		Resource: res, Endpoint: c.URL(), OTLP: true, Headers: map[string]string{headerName: headerCanary},
		Timeout: time.Second, Interval: metricsInterval, ServiceVersion: serviceVersion,
		Transport: testutil.TestGuard(t).Transport(true), Logger: telemetry.NewLogger(logs, "debug"),
	}
	if mutate != nil {
		mutate(&o)
	}
	m, err := telemetry.NewMetrics(o)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	t.Cleanup(func() { shutdownMetrics(t, m) })
	return &rig{t: t, m: m, c: c, logs: logs, res: res}
}

func shutdownMetrics(t testing.TB, m *telemetry.Metrics) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx) // a stalled collector may make it report the deadline: the tests that care call it themselves
}

// collect returns what the next two exports after now hold.
func (r *rig) collect() *snapshot { r.t.Helper(); return r.collectT(r.t) }

// collectT is collect for the test t, which is the one that fails: a subtest passes its own.
func (r *rig) collectT(t testing.TB) *snapshot {
	t.Helper()
	n := len(r.c.Metrics())
	testutil.Eventually(t, 5*time.Second, "two metric exports after the recording", func() bool { return len(r.c.Metrics()) >= n+2 })
	ms := r.c.Metrics()
	return parse(ms[len(ms)-1])
}

// observe records the requests and collects.
func (r *rig) observe(reqs ...telemetry.Request) *snapshot {
	r.t.Helper()
	for _, q := range reqs {
		r.m.ObserveRequest(q)
	}
	return r.collect()
}
