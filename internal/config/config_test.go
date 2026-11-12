package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	envToken  = "PANDOKOS_SKELETON_ACCESS_TOKEN_FILE"
	envAPIKey = "PANDOKOS_SKELETON_OPENAI_API_KEY_FILE"
)

// validSource returns a Source with secret files and an environment that sets only the two
// *_FILE variables.
func validSource(t *testing.T) Source {
	t.Helper()
	return Source{Environ: []string{
		envToken + "=" + testutil.WriteSecret(t, "token", fixtures.CanaryAccessToken),
		envAPIKey + "=" + testutil.WriteSecret(t, "key", fixtures.CanaryProviderKey),
	}}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pandokos.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func problemKeys(err error) []string {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return nil
	}
	var keys []string
	for _, p := range ve.Problems {
		keys = append(keys, p.Key)
	}
	return keys
}

func hasProblem(err error, key string) bool {
	for _, k := range problemKeys(err) {
		if k == key {
			return true
		}
	}
	return false
}

func mustLoad(t *testing.T, src Source) (*Config, []Warning) {
	t.Helper()
	c, w, err := Load(src)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c, w
}

func TestDefaults(t *testing.T) {
	d := Defaults()
	checks := []struct {
		key  string
		got  any
		want any
	}{
		{"log.level", d.Log.Level, "info"},
		{"listeners.inference.address", d.Listeners.Inference.Address, "0.0.0.0:8080"},
		{"listeners.inference.read_header_timeout", time.Duration(d.Listeners.Inference.ReadHeaderTimeout), 10 * time.Second},
		{"listeners.inference.idle_timeout", time.Duration(d.Listeners.Inference.IdleTimeout), 120 * time.Second},
		{"listeners.inference.max_header_bytes", d.Listeners.Inference.MaxHeaderBytes, 65536},
		{"limits.max_request_body_bytes", d.Limits.MaxRequestBodyBytes, int64(10485760)},
		{"limits.request_body_read_timeout", time.Duration(d.Limits.RequestBodyReadTimeout), 30 * time.Second},
		{"limits.max_request_duration", time.Duration(d.Limits.MaxRequestDuration), 10 * time.Minute},
		{"upstream.connect_timeout", time.Duration(d.Upstream.ConnectTimeout), 10 * time.Second},
		{"shutdown.drain_timeout", time.Duration(d.Shutdown.DrainTimeout), 30 * time.Second},
		{"skeleton.access_token_file", d.Skeleton.AccessTokenFile, ""},
		{"skeleton.openai.base_url", d.Skeleton.OpenAI.BaseURL, "https://api.openai.com/v1"},
		{"skeleton.openai.api_key_file", d.Skeleton.OpenAI.APIKeyFile, ""},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: got %v, want %v", c.key, c.got, c.want)
		}
	}
}

var wantKeys = []string{
	"log.level",
	"listeners.inference.address",
	"listeners.inference.read_header_timeout",
	"listeners.inference.idle_timeout",
	"listeners.inference.max_header_bytes",
	"limits.max_request_body_bytes",
	"limits.request_body_read_timeout",
	"limits.max_request_duration",
	"upstream.connect_timeout",
	"shutdown.drain_timeout",
	"skeleton.access_token_file",
	"skeleton.openai.base_url",
	"skeleton.openai.api_key_file",
}

var wantDefaultText = []string{"info", "0.0.0.0:8080", "10s", "120s", "65536", "10485760", "30s", "10m", "10s", "30s", "", "https://api.openai.com/v1", ""}

func leafKeys(t reflect.Type, prefix string, out *[]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		name := prefix + tag
		if f.Type.Kind() == reflect.Struct {
			leafKeys(f.Type, name+".", out)
			continue
		}
		*out = append(*out, name)
	}
}

func TestKeysTable(t *testing.T) {
	keys := Keys()
	var names []string
	seenEnv := map[string]bool{}
	for _, k := range keys {
		names = append(names, k.Name)
		want := "PANDOKOS_" + strings.ToUpper(strings.ReplaceAll(k.Name, ".", "_"))
		if k.Env != want {
			t.Errorf("%s: env %q, want %q", k.Name, k.Env, want)
		}
		if seenEnv[k.Env] {
			t.Errorf("duplicate env name %s", k.Env)
		}
		seenEnv[k.Env] = true
		if strings.HasPrefix(k.Name, "service") || strings.HasPrefix(k.Name, "port") {
			t.Errorf("key %s starts with a reserved prefix", k.Name)
		}
		if k.Type == "" || k.Description == "" {
			t.Errorf("%s: type or description empty", k.Name)
		}
	}
	if !reflect.DeepEqual(names, wantKeys) {
		t.Errorf("Keys() = %v, want %v", names, wantKeys)
	}
	for i, k := range keys {
		if i < len(wantDefaultText) && k.Default != wantDefaultText[i] {
			t.Errorf("%s: default text %q, want %q", k.Name, k.Default, wantDefaultText[i])
		}
	}
	var fromStruct []string
	leafKeys(reflect.TypeOf(Config{}), "", &fromStruct)
	if !reflect.DeepEqual(fromStruct, wantKeys) {
		t.Errorf("struct leaves = %v, want %v", fromStruct, wantKeys)
	}
}

// keyCase gives two valid values per key; yamlInt marks keys that are integers in YAML.
type keyCase struct {
	key, a, b string
	yamlInt   bool
}

func keyCases(t *testing.T) []keyCase {
	t.Helper()
	d := t.TempDir()
	mk := func(name, v string) string {
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return []keyCase{
		{key: "log.level", a: "debug", b: "warn"},
		{key: "listeners.inference.address", a: "127.0.0.1:1111", b: "127.0.0.1:2222"},
		{key: "listeners.inference.read_header_timeout", a: "11s", b: "12s"},
		{key: "listeners.inference.idle_timeout", a: "21s", b: "22s"},
		{key: "listeners.inference.max_header_bytes", a: "8192", b: "16384", yamlInt: true},
		{key: "limits.max_request_body_bytes", a: "2048", b: "4096", yamlInt: true},
		{key: "limits.request_body_read_timeout", a: "31s", b: "32s"},
		{key: "limits.max_request_duration", a: "41s", b: "42s"},
		{key: "upstream.connect_timeout", a: "5s", b: "6s"},
		{key: "shutdown.drain_timeout", a: "7s", b: "8s"},
		{key: "skeleton.access_token_file", a: mk("ta", strings.Repeat("a", 40)), b: mk("tb", strings.Repeat("b", 40))},
		{key: "skeleton.openai.base_url", a: "http://127.0.0.1:1/v1", b: "http://127.0.0.1:2/v1"},
		{key: "skeleton.openai.api_key_file", a: mk("ka", "key-a"), b: mk("kb", "key-b")},
	}
}

func yamlFor(key, value string, isInt bool) string {
	parts := strings.Split(key, ".")
	var sb strings.Builder
	for i, p := range parts {
		sb.WriteString(strings.Repeat("  ", i) + p + ":")
		if i < len(parts)-1 {
			sb.WriteString("\n")
		}
	}
	if isInt {
		sb.WriteString(" " + value + "\n")
	} else {
		sb.WriteString(" '" + value + "'\n")
	}
	return sb.String()
}

func field(c *Config, key string) string {
	switch key {
	case "log.level":
		return c.Log.Level
	case "listeners.inference.address":
		return c.Listeners.Inference.Address
	case "listeners.inference.read_header_timeout":
		return time.Duration(c.Listeners.Inference.ReadHeaderTimeout).String()
	case "listeners.inference.idle_timeout":
		return time.Duration(c.Listeners.Inference.IdleTimeout).String()
	case "listeners.inference.max_header_bytes":
		return strconv.Itoa(c.Listeners.Inference.MaxHeaderBytes)
	case "limits.max_request_body_bytes":
		return strconv.FormatInt(c.Limits.MaxRequestBodyBytes, 10)
	case "limits.request_body_read_timeout":
		return time.Duration(c.Limits.RequestBodyReadTimeout).String()
	case "limits.max_request_duration":
		return time.Duration(c.Limits.MaxRequestDuration).String()
	case "upstream.connect_timeout":
		return time.Duration(c.Upstream.ConnectTimeout).String()
	case "shutdown.drain_timeout":
		return time.Duration(c.Shutdown.DrainTimeout).String()
	case "skeleton.access_token_file":
		return c.Skeleton.AccessTokenFile
	case "skeleton.openai.base_url":
		return c.Skeleton.OpenAI.BaseURL
	case "skeleton.openai.api_key_file":
		return c.Skeleton.OpenAI.APIKeyFile
	}
	panic("unknown key " + key)
}

func normalize(key, v string) string {
	if strings.HasSuffix(key, "_timeout") {
		d, err := time.ParseDuration(v)
		if err != nil {
			panic(err)
		}
		return d.String()
	}
	return v
}

func TestLoadFile(t *testing.T) {
	cases := keyCases(t)
	var sb strings.Builder
	sb.WriteString("log:\n  level: debug\n")
	sb.WriteString("listeners:\n  inference:\n    address: '127.0.0.1:1111'\n    read_header_timeout: 11s\n    idle_timeout: 21s\n    max_header_bytes: 8192\n")
	sb.WriteString("limits:\n  max_request_body_bytes: 2048\n  request_body_read_timeout: 31s\n  max_request_duration: 41s\n")
	sb.WriteString("upstream:\n  connect_timeout: 5s\nshutdown:\n  drain_timeout: 7s\n")
	sb.WriteString("skeleton:\n  access_token_file: " + cases[10].a + "\n  openai:\n    base_url: http://127.0.0.1:1/v1\n    api_key_file: " + cases[12].a + "\n")
	c, _ := mustLoad(t, Source{File: writeFile(t, sb.String())})
	for _, kc := range cases {
		if got, want := field(c, kc.key), normalize(kc.key, kc.a); got != want {
			t.Errorf("%s: got %q, want %q", kc.key, got, want)
		}
	}
	if c.Skeleton.AccessToken.Reveal() != strings.Repeat("a", 40) || c.Skeleton.OpenAI.APIKey.Reveal() != "key-a" {
		t.Error("secrets were not read from their files")
	}
	if c.Skeleton.OpenAI.ParsedBaseURL == nil || c.Skeleton.OpenAI.ParsedBaseURL.Host != "127.0.0.1:1" {
		t.Error("ParsedBaseURL not filled")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	for _, kc := range keyCases(t) {
		t.Run(kc.key, func(t *testing.T) {
			var envName string
			for _, k := range Keys() {
				if k.Name == kc.key {
					envName = k.Env
				}
			}
			// The base environment sets the two secret files, except the one under test, so the
			// file value is the only competitor of the environment value.
			var base []string
			for _, e := range validSource(t).Environ {
				if !strings.HasPrefix(e, envName+"=") {
					base = append(base, e)
				}
			}
			file := writeFile(t, yamlFor(kc.key, kc.a, kc.yamlInt))
			load := func(extra ...string) *Config {
				c, _ := mustLoad(t, Source{File: file, Environ: append(append([]string{}, base...), extra...)})
				return c
			}
			if got, want := field(load(), kc.key), normalize(kc.key, kc.a); got != want {
				t.Fatalf("file value: got %q want %q", got, want)
			}
			if got, want := field(load(envName+"="+kc.b), kc.key), normalize(kc.key, kc.b); got != want {
				t.Fatalf("environment value: got %q want %q", got, want)
			}
			if got, want := field(load(envName+"="), kc.key), normalize(kc.key, kc.a); got != want {
				t.Fatalf("empty variable must be ignored: got %q want %q", got, want)
			}
		})
	}
}

func TestNoFile(t *testing.T) {
	src := validSource(t)
	src.Environ = append(src.Environ, "PANDOKOS_LOG_LEVEL=warn", "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0")
	c, _ := mustLoad(t, src)
	if c.Log.Level != "warn" || c.Listeners.Inference.Address != "127.0.0.1:0" {
		t.Errorf("environment not applied: %+v", c.Log)
	}
	if c.Limits.MaxRequestBodyBytes != 10485760 {
		t.Error("defaults not applied")
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	inputs := map[string]string{
		"unknown key":        "nope: 1\n",
		"secret as value":    "skeleton:\n  openai:\n    api_key: " + fixtures.CanaryProviderKey + "\n",
		"second document":    "log:\n  level: info\n---\nlog:\n  level: debug\n",
		"unknown nested key": "log:\n  levl: info\n",
	}
	for name, content := range inputs {
		t.Run(name, func(t *testing.T) {
			src := validSource(t)
			src.File = writeFile(t, content)
			_, _, err := Load(src)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !hasProblem(err, "file") {
				t.Errorf("no problem under key file: %v", err)
			}
			testutil.AssertNoCanary(t, "error text", err.Error())
		})
	}
	t.Run("empty file is valid", func(t *testing.T) {
		src := validSource(t)
		src.File = writeFile(t, "")
		mustLoad(t, src)
	})
	t.Run("unreadable file", func(t *testing.T) {
		src := validSource(t)
		src.File = filepath.Join(t.TempDir(), "missing.yaml")
		if _, _, err := Load(src); err == nil || !hasProblem(err, "file") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestYAMLErrorsDoNotQuoteValues(t *testing.T) {
	inputs := []string{
		"skeleton: " + fixtures.CanaryProviderKey + "\n",
		"listeners:\n  inference:\n    max_header_bytes: " + fixtures.CanaryProviderKey + "\n",
		"log: [" + fixtures.CanaryProviderKey + "]\n",
		"log:\n  level: {" + fixtures.CanaryProviderKey + ": 1}\n",
	}
	for _, content := range inputs {
		src := validSource(t)
		src.File = writeFile(t, content)
		_, _, err := Load(src)
		if err == nil {
			t.Fatalf("expected an error for %q", content)
		}
		testutil.AssertNoCanary(t, "error text", err.Error())
		if strings.Contains(err.Error(), "canary") {
			t.Errorf("error quotes the value: %v", err)
		}
	}
}

func TestUnknownEnvironment(t *testing.T) {
	src := validSource(t)
	src.Environ = append(src.Environ,
		"PANDOKOS_FOO="+fixtures.CanaryPrompt,
		"PANDOKOS_SERVICE_HOST=10.0.0.1",
		"PANDOKOS_PORT=tcp://10.0.0.1:8080",
		"PANDOKOS_PORT_8080_TCP=tcp://10.0.0.1:8080",
		"PANDOKOS_CONFIG=/nonexistent/ignored-here",
		"OTHER=1",
	)
	_, warnings := mustLoad(t, src)
	var env []Warning
	for _, w := range warnings {
		if w.Key == "environment" {
			env = append(env, w)
		}
	}
	if len(env) != 1 || env[0].Message != "unknown variable PANDOKOS_FOO ignored" {
		t.Fatalf("environment warnings: %+v", env)
	}
	for _, w := range warnings {
		testutil.AssertNoCanary(t, "warning", w.Key+" "+w.Message)
	}
}

func TestFileFromEnviron(t *testing.T) {
	if FileFromEnviron([]string{"A=1", "PANDOKOS_CONFIG=/x.yaml"}) != "/x.yaml" {
		t.Error("value not returned")
	}
	if FileFromEnviron([]string{"PANDOKOS_CONFIG="}) != "" || FileFromEnviron(nil) != "" {
		t.Error("empty expected")
	}
}

func TestValidation(t *testing.T) {
	rows := []struct{ key, value string }{
		{"log.level", "verbose"},
		{"listeners.inference.address", "nohost"},
		{"listeners.inference.address", ":99999"},
		{"listeners.inference.address", "host:port:extra"},
		{"listeners.inference.read_header_timeout", "0s"},
		{"listeners.inference.idle_timeout", "0s"},
		{"limits.request_body_read_timeout", "0s"},
		{"limits.max_request_duration", "0s"},
		{"upstream.connect_timeout", "0s"},
		{"shutdown.drain_timeout", "0s"},
		{"listeners.inference.max_header_bytes", "100"},
		{"listeners.inference.max_header_bytes", "2000000"},
		{"limits.max_request_body_bytes", "0"},
		{"limits.max_request_body_bytes", "104857601"},
		{"skeleton.openai.base_url", "ftp://x/v1"},
		{"skeleton.openai.base_url", "https://user:pw@x/v1"},
		{"skeleton.openai.base_url", "https://x/v1?a=b"},
		{"skeleton.openai.base_url", "https://x/v1#f"},
		{"skeleton.openai.base_url", "/v1"},
		{"skeleton.openai.base_url", "https:///v1"},
	}
	envOf := map[string]string{}
	for _, k := range Keys() {
		envOf[k.Name] = k.Env
	}
	for _, r := range rows {
		t.Run(r.key+"="+r.value, func(t *testing.T) {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf[r.key]+"="+r.value)
			_, _, err := Load(src)
			if err == nil || !hasProblem(err, r.key) {
				t.Fatalf("expected a problem under %s, got %v", r.key, err)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error is %T, want *ValidationError", err)
			}
		})
	}
	t.Run("valid addresses", func(t *testing.T) {
		for _, a := range []string{"127.0.0.1:0", ":8080", "localhost:9", "[::1]:80", "example.com:443"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["listeners.inference.address"]+"="+a)
			mustLoad(t, src)
		}
	})
	t.Run("trailing slash is removed", func(t *testing.T) {
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["skeleton.openai.base_url"]+"=https://x/v1/")
		c, _ := mustLoad(t, src)
		if c.Skeleton.OpenAI.ParsedBaseURL.Path != "/v1" {
			t.Errorf("path %q", c.Skeleton.OpenAI.ParsedBaseURL.Path)
		}
	})
}

func TestAllProblemsReported(t *testing.T) {
	src := validSource(t)
	src.Environ = append(src.Environ,
		"PANDOKOS_LOG_LEVEL=verbose",
		"PANDOKOS_LISTENERS_INFERENCE_ADDRESS=nohost",
		"PANDOKOS_LISTENERS_INFERENCE_MAX_HEADER_BYTES=100",
	)
	_, _, err := Load(src)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %v", err)
	}
	if len(ve.Problems) != 3 {
		t.Fatalf("got %d problems: %v", len(ve.Problems), ve)
	}
	if !strings.Contains(ve.Error(), "log.level: ") || !strings.Contains(ve.Error(), "; ") {
		t.Errorf("Error() format: %q", ve.Error())
	}
}

func TestSecretFiles(t *testing.T) {
	good := validSource(t)
	other := func(name, content string) string { return testutil.WriteSecret(t, name, content) }
	t.Run("read and trimmed", func(t *testing.T) {
		src := good
		src.Environ = []string{
			envToken + "=" + writeFile(t, "  "+fixtures.CanaryAccessToken+" \n\n"),
			envAPIKey + "=" + writeFile(t, "\t"+fixtures.CanaryProviderKey+"\r\n"),
		}
		c, _ := mustLoad(t, src)
		if c.Skeleton.AccessToken.Reveal() != fixtures.CanaryAccessToken || c.Skeleton.OpenAI.APIKey.Reveal() != fixtures.CanaryProviderKey {
			t.Error("secrets not trimmed or not read")
		}
	})
	bad := []struct {
		name    string
		environ []string
		key     string
	}{
		{"token file missing", []string{envToken + "=/nonexistent/token", envAPIKey + "=" + other("k1", "k")}, "skeleton.access_token_file"},
		{"token empty", []string{envToken + "=" + writeFile(t, "\n"), envAPIKey + "=" + other("k2", "k")}, "skeleton.access_token_file"},
		{"token short", []string{envToken + "=" + other("t3", strings.Repeat("s", 31)), envAPIKey + "=" + other("k3", "k")}, "skeleton.access_token_file"},
		{"key file missing", []string{envToken + "=" + other("t4", fixtures.CanaryAccessToken), envAPIKey + "=/nonexistent/key"}, "skeleton.openai.api_key_file"},
		{"key empty", []string{envToken + "=" + other("t5", fixtures.CanaryAccessToken), envAPIKey + "=" + writeFile(t, " ")}, "skeleton.openai.api_key_file"},
		{"token equals key", []string{envToken + "=" + other("t6", fixtures.CanaryAccessToken), envAPIKey + "=" + other("k6", fixtures.CanaryAccessToken)}, "skeleton.access_token_file"},
		{"token not set", []string{envAPIKey + "=" + other("k7", "k")}, "skeleton.access_token_file"},
		{"key not set", []string{envToken + "=" + other("t8", fixtures.CanaryAccessToken)}, "skeleton.openai.api_key_file"},
	}
	for _, b := range bad {
		t.Run(b.name, func(t *testing.T) {
			_, _, err := Load(Source{Environ: b.environ})
			if err == nil || !hasProblem(err, b.key) {
				t.Fatalf("expected a problem under %s, got %v", b.key, err)
			}
			testutil.AssertNoCanary(t, "error text", err.Error())
			if strings.Contains(err.Error(), strings.Repeat("s", 31)) {
				t.Error("error contains file content")
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	envOf := map[string]string{}
	for _, k := range Keys() {
		envOf[k.Name] = k.Env
	}
	warn := func(t *testing.T, kv ...string) map[string]string {
		t.Helper()
		src := validSource(t)
		src.Environ = append(src.Environ, kv...)
		_, ws := mustLoad(t, src)
		out := map[string]string{}
		for _, w := range ws {
			out[w.Key] = w.Message
		}
		return out
	}
	const (
		kURL  = "skeleton.openai.base_url"
		kAddr = "listeners.inference.address"
	)
	w := warn(t, envOf[kURL]+"=http://example.com/v1", envOf[kAddr]+"=0.0.0.0:8080")
	if w[kURL] != "the provider key is sent without TLS" {
		t.Errorf("base_url warning: %q", w[kURL])
	}
	if w[kAddr] != "the listener serves plain HTTP; terminate TLS in front of it" {
		t.Errorf("address warning: %q", w[kAddr])
	}
	for _, u := range []string{"http://127.0.0.1:1/v1", "http://localhost:1/v1", "http://[::1]:1/v1", "https://example.com/v1"} {
		if m, ok := warn(t, envOf[kURL]+"="+u, envOf[kAddr]+"=127.0.0.1:0")[kURL]; ok {
			t.Errorf("unexpected warning for %s: %s", u, m)
		}
	}
	for _, a := range []string{"127.0.0.1:0", "localhost:8080", "[::1]:8080"} {
		if m, ok := warn(t, envOf[kAddr]+"="+a)[kAddr]; ok {
			t.Errorf("unexpected warning for %s: %s", a, m)
		}
	}
}

func TestDuration(t *testing.T) {
	ok := map[string]time.Duration{"30s": 30 * time.Second, "10m": 10 * time.Minute}
	for in, want := range ok {
		src := validSource(t)
		src.File = writeFile(t, "upstream:\n  connect_timeout: \""+in+"\"\n")
		c, _ := mustLoad(t, src)
		if time.Duration(c.Upstream.ConnectTimeout) != want {
			t.Errorf("%s: got %v", in, c.Upstream.ConnectTimeout)
		}
	}
	for _, in := range []string{"30", `"abc"`, `"-1s"`, `"0s"`, "[1]", "1.5"} {
		src := validSource(t)
		src.File = writeFile(t, "upstream:\n  connect_timeout: "+in+"\n")
		_, _, err := Load(src)
		if err == nil {
			t.Errorf("%s: expected an error", in)
		}
	}
}
