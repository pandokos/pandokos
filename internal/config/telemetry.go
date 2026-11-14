package config

import (
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

const (
	maxHeadersFileBytes = 131072
	maxOTLPHeaders      = 16
	maxHeaderValueBytes = 4096
	maxResourceAttrs    = 32
)

var (
	reAttributeKey = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,62}$`)
	// forbiddenOTLPHeaders are set by the exporter itself.
	forbiddenOTLPHeaders = map[string]bool{"host": true, "content-type": true, "content-length": true, "content-encoding": true, "user-agent": true}
)

// validateLedger applies the rules of the ledger keys; only serve reads them.
func validateLedger(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: key, Message: msg}) }
	l := &c.Ledger
	if d := time.Duration(l.FlushInterval); d < time.Millisecond || d > 10*time.Second {
		add("ledger.flush_interval", "must be between 1ms and 10s")
	}
	if l.BatchSize < 1 || l.BatchSize > 5000 {
		add("ledger.batch_size", "must be between 1 and 5000")
	}
	switch {
	case l.BufferSize < 100 || l.BufferSize > 1000000:
		add("ledger.buffer_size", "must be between 100 and 1000000")
	case l.BufferSize < l.BatchSize:
		add("ledger.buffer_size", "must be at least ledger.batch_size")
	}
	if d := time.Duration(l.ShutdownFlushTimeout); d < time.Second || d > 5*time.Minute {
		add("ledger.shutdown_flush_timeout", "must be between 1s and 5m")
	}
	return ps
}

// validateTelemetry applies the rules of the telemetry keys and fills the parsed forms; only
// serve reads them. No message quotes a value: the endpoint may hold a token, the headers are
// secrets and an attribute may be copied from anywhere.
func validateTelemetry(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: key, Message: msg}) }
	t := &c.Telemetry

	if raw := t.OTLP.Endpoint; raw != "" {
		const key = "telemetry.otlp.endpoint"
		u, err := url.Parse(raw)
		switch {
		case err != nil || strings.ContainsAny(raw, " \t\r\n"):
			add(key, "must be a valid URL")
		case u.Scheme != "http" && u.Scheme != "https":
			add(key, "the scheme must be http or https")
		case u.Host == "" || u.Hostname() == "":
			add(key, "must have a host")
		case u.User != nil:
			add(key, "must not contain user information")
		case u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "?"):
			add(key, "must not contain a query")
		case u.Fragment != "" || strings.Contains(raw, "#"):
			add(key, "must not contain a fragment")
		case !validPort(u.Port()):
			add(key, "the port must be an integer between 1 and 65535")
		default:
			t.OTLP.ParsedEndpoint = u
		}
	}
	if t.OTLP.Protocol != "http/protobuf" {
		add("telemetry.otlp.protocol", "must be http/protobuf")
	}
	if t.OTLP.HeadersFile != "" {
		headers, hp := readOTLPHeaders(t.OTLP.HeadersFile)
		ps = append(ps, hp...)
		if len(hp) == 0 {
			t.OTLP.Headers = headers
		}
	}
	if d := time.Duration(t.OTLP.Timeout); d < time.Second || d > time.Minute {
		add("telemetry.otlp.timeout", "must be between 1s and 60s")
	}
	if r := t.Traces.SampleRatio; !(r >= 0 && r <= 1) {
		add("telemetry.traces.sample_ratio", "must be between 0 and 1")
	}
	if t.Resource.Attributes != "" {
		attrs, msg := parseResourceAttributes(t.Resource.Attributes)
		if msg != "" {
			add("telemetry.resource.attributes", msg)
		} else {
			t.ResourceAttributes = attrs
		}
	}
	return ps
}

func validPort(p string) bool {
	if p == "" {
		return true
	}
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// parseResourceAttributes parses "key=value,key=value"; the message never quotes the input.
func parseResourceAttributes(raw string) (map[string]string, string) {
	pairs := strings.Split(raw, ",")
	if len(pairs) > maxResourceAttrs {
		return nil, "at most 32 attributes"
	}
	out := make(map[string]string, len(pairs))
	for i, p := range pairs {
		n := strconv.Itoa(i + 1)
		k, v, ok := strings.Cut(p, "=")
		switch {
		case !ok:
			return nil, "attribute " + n + " must be key=value"
		case !reAttributeKey.MatchString(k):
			return nil, "the key of attribute " + n + " must match [a-z][a-z0-9._-]{0,62}"
		case len(v) < 1 || len(v) > 256 || !printableASCII(v) || strings.Contains(v, "="):
			return nil, "the value of attribute " + n + " must be 1 to 256 printable ASCII characters without , and ="
		}
		if _, dup := out[k]; dup {
			return nil, "attribute " + n + " repeats an earlier key"
		}
		out[k] = v
	}
	return out, ""
}

// readOTLPHeaders reads the headers file. Problems name the line, never its content.
func readOTLPHeaders(path string) (map[string]secret.Secret, []Problem) {
	const key = "telemetry.otlp.headers_file"
	f, err := os.Open(path) //nolint:gosec // the path is operator configuration
	if err != nil {
		return nil, []Problem{{key, "the file cannot be read"}}
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxHeadersFileBytes+1))
	if err != nil {
		return nil, []Problem{{key, "the file cannot be read"}}
	}
	if len(b) > maxHeadersFileBytes {
		return nil, []Problem{{key, "the file is larger than 131072 bytes"}}
	}
	var ps []Problem
	bad := func(line int, msg string) {
		ps = append(ps, Problem{key, "line " + strconv.Itoa(line) + ": " + msg})
	}
	out := map[string]secret.Secret{}
	seen := map[string]bool{}
	for i, line := range strings.Split(string(b), "\n") {
		n := i + 1
		line = strings.Trim(line, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimRight(name, " \t")
		value = strings.Trim(value, " \t")
		switch {
		case !ok:
			bad(n, "must be Name: value")
		case !isToken(name):
			bad(n, "the name must be an HTTP token")
		case forbiddenOTLPHeaders[strings.ToLower(name)]:
			bad(n, "this header is set by the exporter and cannot be configured")
		case seen[strings.ToLower(name)]:
			bad(n, "the header repeats an earlier line")
		case len(value) < 1 || len(value) > maxHeaderValueBytes || !printableASCII(value):
			bad(n, "the value must be 1 to 4096 printable ASCII characters")
		default:
			seen[strings.ToLower(name)] = true
			out[name] = secret.New(value)
		}
	}
	if len(out) > maxOTLPHeaders && len(ps) == 0 {
		ps = append(ps, Problem{key, "at most 16 headers"})
	}
	return out, ps
}

func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", ch) >= 0:
		default:
			return false
		}
	}
	return true
}

// otelEnvironmentWarnings names the OTEL_* variables that matter and are set, never their value.
// The SDK adds the attributes of OTEL_RESOURCE_ATTRIBUTES to the resource whatever Pandokos does;
// the endpoint variables are ignored, which is surprising when the key is empty.
func otelEnvironmentWarnings(c *Config, environ []string) []Warning {
	set := map[string]bool{}
	for _, e := range environ {
		if name, v, ok := strings.Cut(e, "="); ok && v != "" {
			set[name] = true
		}
	}
	var ws []Warning
	if set["OTEL_RESOURCE_ATTRIBUTES"] {
		ws = append(ws, Warning{Key: "telemetry.resource.attributes", Message: "OTEL_RESOURCE_ATTRIBUTES is set in the environment: the OpenTelemetry SDK adds its attributes to the resource (they never replace those Pandokos sets); prefer telemetry.resource.attributes"})
	}
	if c.Telemetry.OTLP.Endpoint == "" {
		for _, name := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
			if set[name] {
				ws = append(ws, Warning{Key: "telemetry.otlp.endpoint", Message: name + " is set in the environment and is not used; set telemetry.otlp.endpoint to export traces"})
			}
		}
	}
	return ws
}
