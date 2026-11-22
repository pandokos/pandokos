package telemetry

import (
	"strings"
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
