package telemetry

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// promSanitize applies the character rule: [a-zA-Z0-9_] (and ':' in metric names) are kept.
func promSanitize(s string, colon bool) string {
	var b strings.Builder
	b.Grow(len(s) + 1)
	for i, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (colon && r == ':')
		if i == 0 && r >= '0' && r <= '9' {
			b.WriteByte('_')
		}
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// The kinds of a Prometheus family.
const (
	promCounter   = "counter"
	promGauge     = "gauge"
	promHistogram = "histogram"
)

// promFamilyName is rule R (section 5.7): the name of the Prometheus family of an instrument with
// this OpenTelemetry name and unit. Dots and other characters outside [a-zA-Z0-9_:] become '_'; the
// unit adds a suffix (s: _seconds, By: _bytes, 1: _ratio on gauges and nothing otherwise, an
// annotation in braces: nothing) unless the name already ends with it; counters get _total. It is the
// translation of Prometheus's OTLP receiver with UnderscoreEscapingWithSuffixes.
func promFamilyName(name, unit, kind string) string {
	n := promSanitize(name, true)
	var suffix string
	switch unitWithoutAnnotations(unit) {
	case "s":
		suffix = "seconds"
	case "By":
		suffix = "bytes"
	case "1":
		if kind == promGauge {
			suffix = "ratio"
		}
	}
	if suffix != "" && !strings.HasSuffix(n, "_"+suffix) {
		n += "_" + suffix
	}
	if kind == promCounter && !strings.HasSuffix(n, "_total") {
		n += "_total"
	}
	return n
}

// unitWithoutAnnotations removes the {…} annotations of a UCUM unit: "{request}" is "".
func unitWithoutAnnotations(unit string) string {
	for {
		i := strings.IndexByte(unit, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(unit[i:], '}')
		if j < 0 {
			break
		}
		unit = unit[:i] + unit[i+j+1:]
	}
	return strings.TrimSpace(unit)
}

// promName is rule R for a catalogue entry; its Kind is the letter of the table (H, C, G or OC).
func promName(s InstrumentSpec) string {
	kind := promCounter
	switch s.Kind {
	case "H":
		kind = promHistogram
	case "G":
		kind = promGauge
	}
	return promFamilyName(s.Name, s.Unit, kind)
}

// promLabel is the character rule of Prometheus names applied to an attribute key.
func promLabel(key string) string { return promSanitize(key, false) }

const promContentType = "text/plain; version=0.0.4; charset=utf-8"

// newHandler returns the handler that serves the text exposition (format 0.0.4) of what the scrape reader collects. The
// scrapes are serialised: a manual reader collects into one value at a time, and a slow scraper
// does not make two collections of the observable instruments overlap.
func (m *Metrics) newHandler() http.Handler {
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "", http.StatusMethodNotAllowed)
			return
		}
		var body []byte
		if r.Method == http.MethodGet {
			mu.Lock()
			var rm metricdata.ResourceMetrics
			err := m.reader.Collect(r.Context(), &rm)
			if err == nil {
				body = appendExposition(nil, &rm)
			}
			mu.Unlock()
			if err != nil {
				m.h.Handle(err)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Type", promContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

// family is one Prometheus family: its header and its series.
type family struct {
	name, help, typ string
	series          []string // the lines, in order
	order           []string // the sort key of each series (its labels)
}

// pair is a label of a series.
type pair struct{ k, v string }

// labelsOf converts an attribute set to sorted Prometheus labels.
func labelsOf(set attribute.Set) []pair {
	out := make([]pair, 0, set.Len())
	for it := set.Iter(); it.Next(); {
		kv := it.Attribute()
		out = append(out, pair{promLabel(string(kv.Key)), kv.Value.String()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].k < out[j].k })
	return out
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

// renderLabels renders {k="v",…}, with extra pairs after the series' own; "" without any label.
func renderLabels(ps []pair, extra ...pair) string {
	if len(ps)+len(extra) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range append(append([]pair(nil), ps...), extra...) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p.k + `="` + labelEscaper.Replace(p.v) + `"`)
	}
	b.WriteByte('}')
	return b.String()
}

// promFloat formats a sample value.
func promFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

type number interface{ int64 | float64 }

func numbers[N number](f *family, dps []metricdata.DataPoint[N]) {
	for _, dp := range dps {
		ls := labelsOf(dp.Attributes)
		f.add(renderLabels(ls), f.name+renderLabels(ls)+" "+promFloat(float64(dp.Value)))
	}
}

func histograms[N number](f *family, dps []metricdata.HistogramDataPoint[N]) {
	for _, dp := range dps {
		ls := labelsOf(dp.Attributes)
		var b strings.Builder
		var cum uint64
		for i, bound := range dp.Bounds {
			if i < len(dp.BucketCounts) {
				cum += dp.BucketCounts[i]
			}
			b.WriteString(f.name + "_bucket" + renderLabels(ls, pair{"le", promFloat(bound)}) + " " + strconv.FormatUint(cum, 10) + "\n")
		}
		b.WriteString(f.name + "_bucket" + renderLabels(ls, pair{"le", "+Inf"}) + " " + strconv.FormatUint(dp.Count, 10) + "\n")
		b.WriteString(f.name + "_sum" + renderLabels(ls) + " " + promFloat(float64(dp.Sum)) + "\n")
		b.WriteString(f.name + "_count" + renderLabels(ls) + " " + strconv.FormatUint(dp.Count, 10))
		f.add(renderLabels(ls), b.String())
	}
}

func (f *family) add(order, text string) {
	f.order = append(f.order, order)
	f.series = append(f.series, text)
}

// appendExposition writes the families of a collection, sorted, then target_info.
func appendExposition(dst []byte, rm *metricdata.ResourceMetrics) []byte {
	families := map[string]*family{}
	get := func(name, unit, kind, help string) *family {
		n := promFamilyName(name, unit, kind)
		f, ok := families[n]
		if !ok {
			f = &family{name: n, help: help, typ: kind}
			families[n] = f
		}
		return f
	}
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			switch d := mt.Data.(type) {
			case metricdata.Sum[int64]:
				numbers(get(mt.Name, mt.Unit, sumKind(d.IsMonotonic), mt.Description), d.DataPoints)
			case metricdata.Sum[float64]:
				numbers(get(mt.Name, mt.Unit, sumKind(d.IsMonotonic), mt.Description), d.DataPoints)
			case metricdata.Gauge[int64]:
				numbers(get(mt.Name, mt.Unit, promGauge, mt.Description), d.DataPoints)
			case metricdata.Gauge[float64]:
				numbers(get(mt.Name, mt.Unit, promGauge, mt.Description), d.DataPoints)
			case metricdata.Histogram[int64]:
				histograms(get(mt.Name, mt.Unit, promHistogram, mt.Description), d.DataPoints)
			case metricdata.Histogram[float64]:
				histograms(get(mt.Name, mt.Unit, promHistogram, mt.Description), d.DataPoints)
			}
		}
	}
	if rm.Resource != nil {
		f := &family{name: "target_info", help: "Target metadata", typ: promGauge}
		ls := labelsOf(attribute.NewSet(rm.Resource.Attributes()...))
		f.add("", f.name+renderLabels(ls)+" 1")
		families[f.name] = f
	}
	names := make([]string, 0, len(families))
	for n := range families {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := families[n]
		idx := make([]int, len(f.series))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return f.order[idx[a]] < f.order[idx[b]] })
		dst = append(dst, "# HELP "+f.name+" "+helpEscaper.Replace(f.help)+"\n# TYPE "+f.name+" "+f.typ+"\n"...)
		for _, i := range idx {
			dst = append(dst, f.series[i]...)
			dst = append(dst, '\n')
		}
	}
	return dst
}

func sumKind(monotonic bool) string {
	if monotonic {
		return promCounter
	}
	return promGauge
}
