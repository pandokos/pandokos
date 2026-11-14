package config

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testutil"
)

// otelVariables are the environment variables the OpenTelemetry SDK reads and that Pandokos does
// not use as configuration.
var otelEndpointVariables = []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"}

// TestTelemetryConfiguration is T-505: the seven telemetry keys of sprint 3.2, section 5.11.
func TestTelemetryConfiguration(t *testing.T) {
	envOf := envOfKeys()
	const (
		endpointKey = "telemetry.otlp.endpoint"
		protocolKey = "telemetry.otlp.protocol"
		headersKey  = "telemetry.otlp.headers_file"
		timeoutKey  = "telemetry.otlp.timeout"
		ratioKey    = "telemetry.traces.sample_ratio"
		propKey     = "telemetry.traces.propagate"
		attrsKey    = "telemetry.resource.attributes"
	)
	keys := map[string]struct{ env, typ, def string }{
		endpointKey: {"PANDOKOS_TELEMETRY_OTLP_ENDPOINT", "URL", ""},
		protocolKey: {"PANDOKOS_TELEMETRY_OTLP_PROTOCOL", "http/protobuf", "http/protobuf"},
		headersKey:  {"PANDOKOS_TELEMETRY_OTLP_HEADERS_FILE", "path", ""},
		timeoutKey:  {"PANDOKOS_TELEMETRY_OTLP_TIMEOUT", "duration", "10s"},
		ratioKey:    {"PANDOKOS_TELEMETRY_TRACES_SAMPLE_RATIO", "decimal", "1"},
		propKey:     {"PANDOKOS_TELEMETRY_TRACES_PROPAGATE", "boolean", "false"},
		attrsKey:    {"PANDOKOS_TELEMETRY_RESOURCE_ATTRIBUTES", "list", ""},
	}
	load := func(t *testing.T, kv ...string) (*Config, []Warning, error) {
		t.Helper()
		src := validSource(t)
		for i := 0; i < len(kv); i += 2 {
			src.Environ = append(src.Environ, envOf[kv[i]]+"="+kv[i+1])
		}
		return Load(src)
	}
	must := func(t *testing.T, kv ...string) (*Config, []Warning) {
		t.Helper()
		c, w, err := load(t, kv...)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return c, w
	}

	t.Run("the keys, their environment names, types and defaults", func(t *testing.T) {
		found := 0
		for _, k := range Keys() {
			w, ok := keys[k.Name]
			if !ok {
				continue
			}
			found++
			if k.Env != w.env || k.Type != w.typ || k.Default != w.def || k.Description == "" {
				t.Errorf("%s: %+v, want env %s type %s default %q", k.Name, k, w.env, w.typ, w.def)
			}
		}
		if found != len(keys) {
			t.Errorf("%d of the seven keys are in Keys()", found)
		}
		c, ws := mustLoad(t, validSource(t))
		tel := c.Telemetry
		if tel.OTLP.Endpoint != "" || tel.OTLP.ParsedEndpoint != nil || tel.OTLP.Protocol != "http/protobuf" || tel.OTLP.HeadersFile != "" || len(tel.OTLP.Headers) != 0 ||
			time.Duration(tel.OTLP.Timeout) != 10*time.Second || tel.Traces.SampleRatio != 1 || tel.Traces.Propagate || tel.Resource.Attributes != "" || len(tel.ResourceAttributes) != 0 {
			t.Errorf("a loaded configuration without the keys: %+v", tel)
		}
		for _, w := range ws {
			if strings.HasPrefix(w.Key, "telemetry.") {
				t.Errorf("an unexpected warning: %+v", w)
			}
		}
	})

	t.Run("telemetry.otlp.endpoint", func(t *testing.T) {
		for _, v := range []string{
			"ftp://collector.example.test", "collector.example.test:4318", "//collector.example.test", "http://", "http:///x", "https:///x",
			"http://user:synthetic-pw@collector.example.test:4318", "http://user@collector.example.test", "http://collector.example.test:4318?a=b",
			"http://collector.example.test#frag", "grpc://collector.example.test:4317", "http://collector.example.test:99999",
		} {
			t.Run(v, func(t *testing.T) {
				_, _, err := load(t, endpointKey, v)
				if !hasProblem(err, endpointKey) {
					t.Fatalf("expected a problem under %s, got %v", endpointKey, err)
				}
				testutil.AssertAbsent(t, "the error", err.Error(), "synthetic-pw")
			})
		}
		for _, v := range []string{"http://collector.example.test:4318", "https://collector.example.test", "http://127.0.0.1:4318/base/path", "http://[::1]:4318", "https://c.example.test/v1/"} {
			c, _ := must(t, endpointKey, v)
			if c.Telemetry.OTLP.Endpoint != v || c.Telemetry.OTLP.ParsedEndpoint == nil || c.Telemetry.OTLP.ParsedEndpoint.Host == "" ||
				c.Telemetry.OTLP.ParsedEndpoint.String() != v {
				t.Errorf("%s: %+v", v, c.Telemetry.OTLP)
			}
		}
	})

	t.Run("telemetry.otlp.protocol", func(t *testing.T) {
		if c, _ := must(t, protocolKey, "http/protobuf"); c.Telemetry.OTLP.Protocol != "http/protobuf" {
			t.Errorf("%q", c.Telemetry.OTLP.Protocol)
		}
		for _, v := range []string{"grpc", "http/json", "HTTP/PROTOBUF", "protobuf", "http"} {
			if _, _, err := load(t, protocolKey, v); !hasProblem(err, protocolKey) {
				t.Errorf("%q: %v", v, err)
			}
		}
	})

	t.Run("telemetry.otlp.timeout is 1 s to 60 s", func(t *testing.T) {
		for v, ok := range map[string]bool{"1s": true, "10s": true, "1m": true, "999ms": false, "0s": false, "61s": false, "2m": false, "soon": false} {
			c, _, err := load(t, timeoutKey, v)
			if ok != (err == nil) {
				t.Errorf("%q: %v", v, err)
			}
			if ok && err == nil {
				if got, _ := time.ParseDuration(v); time.Duration(c.Telemetry.OTLP.Timeout) != got {
					t.Errorf("%q parsed as %v", v, c.Telemetry.OTLP.Timeout)
				}
			}
			if !ok && !hasProblem(err, timeoutKey) {
				t.Errorf("%q: problems %v", v, problemKeys(err))
			}
		}
	})

	t.Run("telemetry.traces.sample_ratio is 0 to 1", func(t *testing.T) {
		for v, want := range map[string]float64{"0": 0, "1": 1, "0.25": 0.25, "0.5": 0.5, "1.0": 1} {
			if c, _ := must(t, ratioKey, v); c.Telemetry.Traces.SampleRatio != want {
				t.Errorf("%q parsed as %v", v, c.Telemetry.Traces.SampleRatio)
			}
		}
		for _, v := range []string{"-0.1", "1.1", "2", "NaN", "Inf", "-Inf", "half", "50%", "0,5"} {
			if _, _, err := load(t, ratioKey, v); !hasProblem(err, ratioKey) {
				t.Errorf("%q: %v", v, err)
			}
		}
	})

	t.Run("telemetry.traces.propagate", func(t *testing.T) {
		if c, _ := must(t, propKey, "true"); !c.Telemetry.Traces.Propagate {
			t.Error("true")
		}
		if c, _ := must(t, propKey, "false"); c.Telemetry.Traces.Propagate {
			t.Error("false")
		}
		if _, _, err := load(t, propKey, "sometimes"); !hasProblem(err, propKey) {
			t.Errorf("sometimes: %v", err)
		}
	})

	t.Run("telemetry.resource.attributes", func(t *testing.T) {
		c, _ := must(t, attrsKey, "deployment.environment=prod,service.namespace=ns-1,team_x=alpha")
		want := map[string]string{"deployment.environment": "prod", "service.namespace": "ns-1", "team_x": "alpha"}
		got := c.Telemetry.ResourceAttributes
		if len(got) != len(want) {
			t.Fatalf("%v", got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
		pair := func(i int) string {
			return "k" + strings.Repeat("a", i%10) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "=v"
		}
		var many []string
		for i := 0; i < 32; i++ {
			many = append(many, pair(i))
		}
		if c, _ := must(t, attrsKey, strings.Join(many, ",")); len(c.Telemetry.ResourceAttributes) != 32 {
			t.Errorf("32 attributes: %d", len(c.Telemetry.ResourceAttributes))
		}
		bad := map[string]string{
			"no equals sign":                 "key",
			"an empty key":                   "=v",
			"an upper-case key":              "Key=v",
			"a key that starts with a digit": "1key=v",
			"a key with a space":             "a b=v",
			"a key of 64 characters":         strings.Repeat("a", 64) + "=v",
			"an empty value":                 "k=",
			"a value with =":                 "k=v=w",
			"a value of 257":                 "k=" + strings.Repeat("v", 257),
			"a non-ASCII value":              "k=caf\u00e9",
			"a control byte":                 "k=a\x01b",
			"33 attributes":                  strings.Join(append(many, "zzz=v"), ","),
			"an empty pair":                  "a=b,,c=d",
		}
		for name, v := range bad {
			t.Run(name, func(t *testing.T) {
				_, _, err := load(t, attrsKey, v)
				if !hasProblem(err, attrsKey) {
					t.Fatalf("%q: %v", v, err)
				}
				if len(v) > 8 && strings.Contains(err.Error(), v) {
					t.Errorf("the problem quotes the value: %v", err)
				}
			})
		}
		t.Run("a key of 63 characters and a value of 256 are accepted", func(t *testing.T) {
			must(t, attrsKey, "a"+strings.Repeat("b", 62)+"="+strings.Repeat("v", 256))
		})
	})

	t.Run("telemetry.otlp.headers_file", func(t *testing.T) {
		const canary = "synthetic-header-secret-5d21"
		write := func(content string) string {
			p := filepath.Join(t.TempDir(), "headers")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}
		t.Run("a valid file", func(t *testing.T) {
			file := write("# the collector\n\nAuthorization: Bearer " + canary + "\nX-Tenant:   alpha  \r\n   \n# done\nx-lower: v\n")
			c, _ := must(t, headersKey, file)
			headers := map[string]string{}
			for k, v := range c.Telemetry.OTLP.Headers {
				headers[http.CanonicalHeaderKey(k)] = v.Reveal()
			}
			want := map[string]string{"Authorization": "Bearer " + canary, "X-Tenant": "alpha", "X-Lower": "v"}
			if len(headers) != len(want) {
				t.Fatalf("headers %v", headers)
			}
			for k, v := range want {
				if headers[k] != v {
					t.Errorf("%s = %q, want %q", k, headers[k], v)
				}
			}
			if c.Telemetry.OTLP.HeadersFile != file {
				t.Errorf("HeadersFile %q", c.Telemetry.OTLP.HeadersFile)
			}
			// The values are secrets: they print redacted.
			testutil.AssertAbsent(t, "the printed configuration", strings.TrimSpace(fmt.Sprintf("%+v %#v", c.Telemetry, c.Telemetry)), canary)
		})
		t.Run("an empty file is no headers", func(t *testing.T) {
			if c, _ := must(t, headersKey, write("# nothing\n\n")); len(c.Telemetry.OTLP.Headers) != 0 {
				t.Errorf("%v", c.Telemetry.OTLP.Headers)
			}
		})

		var sixteen, seventeen strings.Builder
		for i := 0; i < 17; i++ {
			line := "X-H" + strings.Repeat("a", i) + ": v\n"
			if i < 16 {
				sixteen.WriteString(line)
			}
			seventeen.WriteString(line)
		}
		t.Run("16 headers are accepted", func(t *testing.T) {
			if c, _ := must(t, headersKey, write(sixteen.String())); len(c.Telemetry.OTLP.Headers) != 16 {
				t.Errorf("%d headers", len(c.Telemetry.OTLP.Headers))
			}
		})

		type bad struct{ name, content, line string }
		cases := []bad{
			{"a line without a colon", "X-Ok: v\nnot a header " + canary + "\n", "line 2"},
			{"an empty name", "X-Ok: v\n: " + canary + "\n", "line 2"},
			{"a name with a space", "bad name: " + canary + "\n", "line 1"},
			{"a name with a control byte", "X-\x01: " + canary + "\n", "line 1"},
			{"a name that is not a token", "X-(a): " + canary + "\n", "line 1"},
			{"Host", "Host: " + canary + "\n", "line 1"},
			{"host in lower case", "# c\nhost: " + canary + "\n", "line 2"},
			{"Content-Type", "Content-Type: " + canary + "\n", "line 1"},
			{"CONTENT-LENGTH", "CONTENT-LENGTH: " + canary + "\n", "line 1"},
			{"Content-Encoding", "Content-Encoding: " + canary + "\n", "line 1"},
			{"User-Agent", "User-Agent: " + canary + "\n", "line 1"},
			{"an empty value", "X-A:\nX-B: v\n", "line 1"},
			{"a value of spaces", "X-A:    \n", "line 1"},
			{"a non-ASCII value", "X-A: caf\u00e9" + canary + "\n", "line 1"},
			{"a control byte in a value", "X-Ok: v\nX-A: a\x01" + canary + "\n", "line 2"},
			{"a value of 4097 bytes", "X-A: " + strings.Repeat("v", 4097) + "\n", "line 1"},
			{"17 headers", seventeen.String(), ""},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				_, _, err := load(t, headersKey, write(c.content))
				msg, ok := problemMessage(err, headersKey)
				if !ok {
					t.Fatalf("no problem under %s: %v", headersKey, err)
				}
				if c.line != "" && !strings.Contains(msg, c.line) {
					t.Errorf("the problem %q does not name %s", msg, c.line)
				}
				testutil.AssertAbsent(t, "the error", err.Error(), canary, strings.Repeat("v", 100))
			})
		}
		t.Run("a value of 4096 bytes is accepted", func(t *testing.T) {
			must(t, headersKey, write("X-A: "+strings.Repeat("v", 4096)+"\n"))
		})
		t.Run("a file that cannot be read", func(t *testing.T) {
			dir := t.TempDir()
			for name, p := range map[string]string{"missing": filepath.Join(dir, "missing"), "a directory": dir} {
				_, _, err := load(t, headersKey, p)
				if msg, ok := problemMessage(err, headersKey); !ok || !strings.Contains(msg, "cannot be read") {
					t.Errorf("%s: %q %v", name, msg, err)
				}
			}
		})
	})

	t.Run("a YAML file sets them too", func(t *testing.T) {
		src := validSource(t)
		src.File = writeFile(t, "telemetry:\n  otlp:\n    endpoint: http://collector.example.test:4318\n    timeout: 20s\n  traces:\n    sample_ratio: 0.5\n    propagate: true\n  resource:\n    attributes: 'deployment.environment=prod,team=alpha'\n")
		c, _ := mustLoad(t, src)
		if c.Telemetry.OTLP.ParsedEndpoint == nil || time.Duration(c.Telemetry.OTLP.Timeout) != 20*time.Second || c.Telemetry.Traces.SampleRatio != 0.5 ||
			!c.Telemetry.Traces.Propagate || c.Telemetry.ResourceAttributes["team"] != "alpha" {
			t.Errorf("%+v", c.Telemetry)
		}
	})

	t.Run("the keys are read for serve only", func(t *testing.T) {
		for name, src := range map[string]Source{"init": validInitSource(t), "migrate": validMigrateSource(t)} {
			src.Environ = append(src.Environ, envOf[endpointKey]+"=ftp://not-a-collector", envOf[ratioKey]+"=7", envOf[headersKey]+"=/nonexistent/headers",
				envOf[attrsKey]+"=Bad", envOf[protocolKey]+"=grpc", envOf[timeoutKey]+"=1ms")
			c, ws := mustLoad(t, src)
			if c.Telemetry.OTLP.ParsedEndpoint != nil || len(c.Telemetry.OTLP.Headers) != 0 || len(c.Telemetry.ResourceAttributes) != 0 {
				t.Errorf("%s read the telemetry settings: %+v", name, c.Telemetry)
			}
			for _, w := range ws {
				if strings.HasPrefix(w.Key, "telemetry.") {
					t.Errorf("%s: a warning: %+v", name, w)
				}
			}
		}
	})

	// The two warnings of section 5.9.
	t.Run("OTEL_RESOURCE_ATTRIBUTES gives a warning that names it, not its value", func(t *testing.T) {
		const value = "synthetic-resource-value"
		src := validSource(t)
		src.Environ = append(src.Environ, "OTEL_RESOURCE_ATTRIBUTES=deployment.environment="+value)
		_, ws := mustLoad(t, src)
		n := 0
		for _, w := range ws {
			if strings.Contains(w.Message, "OTEL_RESOURCE_ATTRIBUTES") {
				n++
				if w.Key != attrsKey {
					t.Errorf("the warning is under %q, want %q", w.Key, attrsKey)
				}
			}
			testutil.AssertAbsent(t, "a warning", w.Key+w.Message, value)
		}
		if n != 1 {
			t.Errorf("%d warnings name OTEL_RESOURCE_ATTRIBUTES: %+v", n, ws)
		}
		// It does not depend on the other settings.
		src = validSource(t)
		src.Environ = append(src.Environ, "OTEL_RESOURCE_ATTRIBUTES=a=b", envOf[attrsKey]+"=c=d", envOf[endpointKey]+"=http://collector.example.test:4318")
		_, ws = mustLoad(t, src)
		n = 0
		for _, w := range ws {
			if strings.Contains(w.Message, "OTEL_RESOURCE_ATTRIBUTES") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("with the keys set: %d warnings", n)
		}
	})

	t.Run("an OTEL endpoint variable with no endpoint key gives a warning that names it", func(t *testing.T) {
		const value = "http://synthetic-collector-value.example.test:4318"
		for _, name := range otelEndpointVariables {
			src := validSource(t)
			src.Environ = append(src.Environ, name+"="+value)
			_, ws := mustLoad(t, src)
			n := 0
			for _, w := range ws {
				if strings.Contains(w.Message, name) && (name != "OTEL_EXPORTER_OTLP_ENDPOINT" || !strings.Contains(w.Message, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) {
					n++
					if w.Key != endpointKey {
						t.Errorf("%s: the warning is under %q, want %q", name, w.Key, endpointKey)
					}
				}
				testutil.AssertAbsent(t, "a warning for "+name, w.Key+w.Message, value, "synthetic-collector-value")
			}
			if n != 1 {
				t.Errorf("%s: %d warnings name it: %+v", name, n, ws)
			}
		}
		t.Run("no warning when the key is set", func(t *testing.T) {
			src := validSource(t)
			src.Environ = append(src.Environ, otelEndpointVariables[0]+"="+value, otelEndpointVariables[1]+"="+value, envOf[endpointKey]+"=http://collector.example.test:4318")
			_, ws := mustLoad(t, src)
			for _, w := range ws {
				for _, name := range otelEndpointVariables {
					if strings.Contains(w.Message, name) {
						t.Errorf("an unexpected warning: %+v", w)
					}
				}
			}
		})
		t.Run("no warning without the variables", func(t *testing.T) {
			_, ws := mustLoad(t, validSource(t))
			for _, w := range ws {
				if strings.Contains(w.Message, "OTEL_") {
					t.Errorf("an unexpected warning: %+v", w)
				}
			}
		})
		t.Run("other OTEL variables are not mentioned", func(t *testing.T) {
			src := validSource(t)
			src.Environ = append(src.Environ, "OTEL_TRACES_SAMPLER=always_off", "OTEL_SDK_DISABLED=true")
			_, ws := mustLoad(t, src)
			for _, w := range ws {
				if strings.Contains(w.Message, "OTEL_") {
					t.Errorf("an unexpected warning: %+v", w)
				}
			}
		})
	})
}
