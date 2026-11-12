package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	envToken  = "PANDOKOS_SKELETON_ACCESS_TOKEN_FILE"
	envAPIKey = "PANDOKOS_SKELETON_OPENAI_API_KEY_FILE"
)

func randomSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "pw-" + hex.EncodeToString(b)
}

// dbServeEnv sets the database variables that the serve purpose needs: the address, the TLS mode
// (disable, so that no CA file is needed) and the three runtime password files.
func dbServeEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PANDOKOS_DATABASE_ADDRESS=127.0.0.1:5432",
		"PANDOKOS_DATABASE_TLS_MODE=disable",
		"PANDOKOS_DATABASE_GATEWAY_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_gateway", randomSecret(t)),
		"PANDOKOS_DATABASE_ADMIN_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_admin", randomSecret(t)),
		"PANDOKOS_DATABASE_WORKER_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_worker", randomSecret(t)),
	}
}

// validSource returns a Source for the serve purpose with every secret file and the database
// settings in the environment.
func validSource(t *testing.T) Source {
	t.Helper()
	return Source{Environ: append([]string{
		envToken + "=" + testutil.WriteSecret(t, "token", fixtures.CanaryAccessToken),
		envAPIKey + "=" + testutil.WriteSecret(t, "key", fixtures.CanaryProviderKey),
	}, dbServeEnv(t)...)}
}

// validMigrateSource returns a Source for the migrate purpose: the address, the TLS mode and
// the owner password file only.
func validMigrateSource(t *testing.T) Source {
	t.Helper()
	return Source{Purpose: PurposeMigrate, Environ: []string{
		"PANDOKOS_DATABASE_ADDRESS=127.0.0.1:5432",
		"PANDOKOS_DATABASE_TLS_MODE=disable",
		"PANDOKOS_DATABASE_OWNER_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_owner", randomSecret(t)),
	}}
}

// without returns environ minus the variables with the given names.
func without(environ []string, names ...string) []string {
	var out []string
outer:
	for _, e := range environ {
		for _, n := range names {
			if strings.HasPrefix(e, n+"=") {
				continue outer
			}
		}
		out = append(out, e)
	}
	return out
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
		{"database.address", d.Database.Address, ""},
		{"database.name", d.Database.Name, "pandokos"},
		{"database.tls.mode", d.Database.TLS.Mode, "verify-full"},
		{"database.tls.ca_file", d.Database.TLS.CAFile, ""},
		{"database.connect_timeout", time.Duration(d.Database.ConnectTimeout), 5 * time.Second},
		{"database.gateway.user", d.Database.Gateway.User, "pandokos_gateway"},
		{"database.gateway.password_file", d.Database.Gateway.PasswordFile, ""},
		{"database.gateway.max_connections", d.Database.Gateway.MaxConnections, 10},
		{"database.admin.user", d.Database.Admin.User, "pandokos_admin"},
		{"database.admin.password_file", d.Database.Admin.PasswordFile, ""},
		{"database.admin.max_connections", d.Database.Admin.MaxConnections, 10},
		{"database.worker.user", d.Database.Worker.User, "pandokos_worker"},
		{"database.worker.password_file", d.Database.Worker.PasswordFile, ""},
		{"database.worker.max_connections", d.Database.Worker.MaxConnections, 5},
		{"database.owner.user", d.Database.Owner.User, "pandokos_owner"},
		{"database.owner.password_file", d.Database.Owner.PasswordFile, ""},
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
	"database.address",
	"database.name",
	"database.tls.mode",
	"database.tls.ca_file",
	"database.connect_timeout",
	"database.gateway.user",
	"database.gateway.password_file",
	"database.gateway.max_connections",
	"database.admin.user",
	"database.admin.password_file",
	"database.admin.max_connections",
	"database.worker.user",
	"database.worker.password_file",
	"database.worker.max_connections",
	"database.owner.user",
	"database.owner.password_file",
	"skeleton.access_token_file",
	"skeleton.openai.base_url",
	"skeleton.openai.api_key_file",
}

var wantDefaultText = []string{"info", "0.0.0.0:8080", "10s", "120s", "65536", "10485760", "30s", "10m", "10s", "30s",
	"", "pandokos", "verify-full", "", "5s", "pandokos_gateway", "", "10", "pandokos_admin", "", "10", "pandokos_worker", "", "5", "pandokos_owner", "",
	"", "https://api.openai.com/v1", ""}

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
		segments := strings.Split(k.Name, ".")
		if segments[len(segments)-1] == "port" {
			t.Errorf("key %s has port as its last segment: Kubernetes service links would collide", k.Name)
		}
		for _, seg := range segments {
			if strings.HasPrefix(seg, "service_") {
				t.Errorf("key %s has a segment that starts with service_", k.Name)
			}
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
		{key: "database.address", a: "db-a.example.test:5432", b: "db-b.example.test:6432"},
		{key: "database.name", a: "dba", b: "dbb"},
		{key: "database.tls.mode", a: "disable", b: "verify-full"},
		{key: "database.tls.ca_file", a: testutil.WriteCA(t), b: testutil.WriteCA(t)},
		{key: "database.connect_timeout", a: "3s", b: "4s"},
		{key: "database.gateway.user", a: "gw-a", b: "gw-b"},
		{key: "database.gateway.password_file", a: mk("gwp_a", "pw-gateway-a"), b: mk("gwp_b", "pw-gateway-b")},
		{key: "database.gateway.max_connections", a: "21", b: "22", yamlInt: true},
		{key: "database.admin.user", a: "adm-a", b: "adm-b"},
		{key: "database.admin.password_file", a: mk("adp_a", "pw-admin-a"), b: mk("adp_b", "pw-admin-b")},
		{key: "database.admin.max_connections", a: "31", b: "32", yamlInt: true},
		{key: "database.worker.user", a: "wk-a", b: "wk-b"},
		{key: "database.worker.password_file", a: mk("wkp_a", "pw-worker-a"), b: mk("wkp_b", "pw-worker-b")},
		{key: "database.worker.max_connections", a: "41", b: "42", yamlInt: true},
		{key: "database.owner.user", a: "own-a", b: "own-b"},
		{key: "database.owner.password_file", a: mk("owp_a", "pw-owner-a"), b: mk("owp_b", "pw-owner-b")},
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
	case "database.address":
		return c.Database.Address
	case "database.name":
		return c.Database.Name
	case "database.tls.mode":
		return c.Database.TLS.Mode
	case "database.tls.ca_file":
		return c.Database.TLS.CAFile
	case "database.connect_timeout":
		return time.Duration(c.Database.ConnectTimeout).String()
	case "database.gateway.user":
		return c.Database.Gateway.User
	case "database.gateway.password_file":
		return c.Database.Gateway.PasswordFile
	case "database.gateway.max_connections":
		return strconv.Itoa(c.Database.Gateway.MaxConnections)
	case "database.admin.user":
		return c.Database.Admin.User
	case "database.admin.password_file":
		return c.Database.Admin.PasswordFile
	case "database.admin.max_connections":
		return strconv.Itoa(c.Database.Admin.MaxConnections)
	case "database.worker.user":
		return c.Database.Worker.User
	case "database.worker.password_file":
		return c.Database.Worker.PasswordFile
	case "database.worker.max_connections":
		return strconv.Itoa(c.Database.Worker.MaxConnections)
	case "database.owner.user":
		return c.Database.Owner.User
	case "database.owner.password_file":
		return c.Database.Owner.PasswordFile
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

// yamlOf renders the "a" value of every case, except the keys in skip, as one YAML document.
func yamlOf(t *testing.T, cases []keyCase, skip ...string) string {
	t.Helper()
	root := map[string]any{}
outer:
	for _, kc := range cases {
		for _, k := range skip {
			if kc.key == k {
				continue outer
			}
		}
		parts := strings.Split(kc.key, ".")
		m := root
		for _, p := range parts[:len(parts)-1] {
			sub, ok := m[p].(map[string]any)
			if !ok {
				sub = map[string]any{}
				m[p] = sub
			}
			m = sub
		}
		if kc.yamlInt {
			n, err := strconv.Atoi(kc.a)
			if err != nil {
				t.Fatal(err)
			}
			m[parts[len(parts)-1]] = n
		} else {
			m[parts[len(parts)-1]] = kc.a
		}
	}
	b, err := yaml.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func caseOf(t *testing.T, cases []keyCase, key string) keyCase {
	t.Helper()
	for _, kc := range cases {
		if kc.key == key {
			return kc
		}
	}
	t.Fatalf("no case for %s", key)
	return keyCase{}
}

func TestLoadFile(t *testing.T) {
	cases := keyCases(t)
	// The CA file is only valid with TLS mode verify-full, which the other cases do not use.
	c, _ := mustLoad(t, Source{File: writeFile(t, yamlOf(t, cases, "database.tls.ca_file"))})
	for _, kc := range cases {
		if kc.key == "database.tls.ca_file" {
			continue
		}
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
	d := c.Database
	if d.Host != "db-a.example.test" || d.Port != 5432 {
		t.Errorf("Host and Port: %q %d", d.Host, d.Port)
	}
	if d.Gateway.Password.Reveal() != "pw-gateway-a" || d.Admin.Password.Reveal() != "pw-admin-a" || d.Worker.Password.Reveal() != "pw-worker-a" {
		t.Error("the runtime passwords were not read from their files")
	}
	if !d.Owner.Password.IsZero() {
		t.Error("serve must not read the owner password file")
	}

	t.Run("CA file", func(t *testing.T) {
		src := validSource(t)
		src.Environ = without(src.Environ, "PANDOKOS_DATABASE_TLS_MODE")
		ca := caseOf(t, cases, "database.tls.ca_file").a
		src.File = writeFile(t, "database:\n  tls:\n    ca_file: "+ca+"\n")
		got, _ := mustLoad(t, src)
		if got.Database.TLS.Mode != "verify-full" || got.Database.TLS.CAFile != ca {
			t.Errorf("TLS: %+v", got.Database.TLS)
		}
	})
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
			drop := []string{envName}
			if kc.key == "database.tls.ca_file" {
				drop = append(drop, "PANDOKOS_DATABASE_TLS_MODE") // the CA file needs verify-full
			}
			base := without(validSource(t).Environ, drop...)
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
		"PANDOKOS_DATABASE_PORT=tcp://10.0.0.1:5432",
		"PANDOKOS_FOO_SERVICE_HOST=10.0.0.1",
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
		src.Environ = append([]string{
			envToken + "=" + writeFile(t, "  "+fixtures.CanaryAccessToken+" \n\n"),
			envAPIKey + "=" + writeFile(t, "\t"+fixtures.CanaryProviderKey+"\r\n"),
		}, dbServeEnv(t)...)
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
			_, _, err := Load(Source{Environ: append(b.environ, dbServeEnv(t)...)})
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
	t.Run("unknown environment variable", func(t *testing.T) {
		w := warn(t, "PANDOKOS_BOGUS=x")
		if w["environment"] != "unknown variable PANDOKOS_BOGUS ignored" {
			t.Errorf("environment warning: %q", w["environment"])
		}
		if m, ok := warn(t)["environment"]; ok {
			t.Errorf("unexpected environment warning: %s", m)
		}
	})
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

func problemMessage(err error, key string) (string, bool) {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return "", false
	}
	for _, p := range ve.Problems {
		if p.Key == key {
			return p.Message, true
		}
	}
	return "", false
}

// TestDatabaseValidation is T-113: one case per rule of sprint.md section 5.2.
func TestDatabaseValidation(t *testing.T) {
	const (
		nameMsg = "must be 1 to 63 characters: letters, digits, underscore, dot, hyphen"
		portMsg = "the port must be an integer between 1 and 65535"
		hostMsg = "the host must be an IP address or a host name"
		rangeMs = "must be between 1 and 1000"
	)
	envOf := map[string]string{}
	for _, k := range Keys() {
		envOf[k.Name] = k.Env
	}
	dir := t.TempDir()
	mkfile := func(name string, content []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	notPEM := mkfile("not-pem", []byte("this is not a certificate\n"))
	huge := mkfile("huge", []byte(strings.Repeat("x", 1048577)))
	ca := testutil.WriteCA(t)
	long := strings.Repeat("a", 64)

	type row struct {
		name, key string
		file      string   // YAML of the file
		env       []string // extra environment
		dropTLS   bool     // remove the TLS mode of the base environment (so verify-full applies)
		want      string
		prefix    bool // want is a prefix of the message
	}
	rows := []row{
		{name: "address empty", key: "database.address", file: "database:\n  address: ''\n", want: "must be set"},
		{name: "address without port", key: "database.address", file: "database:\n  address: nohost\n", want: "must be host:port"},
		{name: "port zero", key: "database.address", file: "database:\n  address: 'db:0'\n", want: portMsg},
		{name: "port too large", key: "database.address", file: "database:\n  address: 'db:70000'\n", want: portMsg},
		{name: "host empty", key: "database.address", file: "database:\n  address: ':5432'\n", want: hostMsg},
		{name: "host is a path", key: "database.address", file: "database:\n  address: '/var/run/postgresql:5432'\n", want: hostMsg},
		{name: "tls mode", key: "database.tls.mode", env: []string{envOf["database.tls.mode"] + "=require"}, want: "must be verify-full or disable"},
		{name: "ca file with disable", key: "database.tls.ca_file", env: []string{envOf["database.tls.ca_file"] + "=" + ca}, want: "is only used when database.tls.mode is verify-full"},
		{name: "ca file missing", key: "database.tls.ca_file", dropTLS: true, env: []string{envOf["database.tls.ca_file"] + "=/nonexistent/ca.pem"}, want: "cannot read the file: ", prefix: true},
		{name: "ca file without certificate", key: "database.tls.ca_file", dropTLS: true, env: []string{envOf["database.tls.ca_file"] + "=" + notPEM}, want: "the file contains no PEM certificate"},
		{name: "ca file too large", key: "database.tls.ca_file", dropTLS: true, env: []string{envOf["database.tls.ca_file"] + "=" + huge}, want: "the file is larger than 1048576 bytes"},
		{name: "connect timeout", key: "database.connect_timeout", env: []string{envOf["database.connect_timeout"] + "=0s"}, want: "must be greater than zero"},
		{name: "admin equals gateway", key: "database.admin.user",
			env:  []string{envOf["database.gateway.user"] + "=same-user", envOf["database.admin.user"] + "=same-user"},
			want: "must differ from database.gateway.user: each role needs its own database user"},
		{name: "worker equals admin", key: "database.worker.user",
			env:  []string{envOf["database.admin.user"] + "=same-user", envOf["database.worker.user"] + "=same-user"},
			want: "must differ from database.admin.user: each role needs its own database user"},
		{name: "owner equals worker", key: "database.owner.user",
			env:  []string{envOf["database.worker.user"] + "=same-user", envOf["database.owner.user"] + "=same-user"},
			want: "must differ from database.worker.user: each role needs its own database user"},
		{name: "owner equals gateway", key: "database.owner.user",
			env:  []string{envOf["database.gateway.user"] + "=same-user", envOf["database.owner.user"] + "=same-user"},
			want: "must differ from database.gateway.user: each role needs its own database user"},
	}
	for _, key := range []string{"database.name", "database.gateway.user", "database.admin.user", "database.worker.user", "database.owner.user"} {
		parts := strings.Split(key, ".")
		render := func(v string) string {
			if len(parts) == 2 {
				return "database:\n  " + parts[1] + ": '" + v + "'\n"
			}
			return "database:\n  " + parts[1] + ":\n    " + parts[2] + ": '" + v + "'\n"
		}
		for label, v := range map[string]string{"empty": "", "space": "a b", "leading hyphen": "-x", "64 characters": long} {
			rows = append(rows, row{name: key + " " + label, key: key, file: render(v), want: nameMsg})
		}
	}
	for _, role := range []string{"gateway", "admin", "worker"} {
		key := "database." + role + ".max_connections"
		for _, v := range []string{"0", "1001"} {
			rows = append(rows, row{name: key + "=" + v, key: key, env: []string{envOf[key] + "=" + v}, want: rangeMs})
		}
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			src := validSource(t)
			drop := []string{envOf[r.key]}
			for _, e := range r.env {
				name, _, _ := strings.Cut(e, "=")
				drop = append(drop, name)
			}
			if r.dropTLS {
				drop = append(drop, envOf["database.tls.mode"])
			}
			src.Environ = append(without(src.Environ, drop...), r.env...)
			if r.file != "" {
				src.File = writeFile(t, r.file)
			}
			_, _, err := Load(src)
			msg, ok := problemMessage(err, r.key)
			if !ok {
				t.Fatalf("expected a problem under %s, got %v", r.key, err)
			}
			if r.prefix && !strings.HasPrefix(msg, r.want) || !r.prefix && msg != r.want {
				t.Errorf("message %q, want %q", msg, r.want)
			}
		})
	}
	t.Run("valid values", func(t *testing.T) {
		for _, a := range []string{"127.0.0.1:5432", "db:5432", "[::1]:5432", "pooler.internal.example:6432", "10.0.0.1:1", "db:65535"} {
			src := validSource(t)
			src.Environ = append(without(src.Environ, envOf["database.address"]), envOf["database.address"]+"="+a)
			c, _ := mustLoad(t, src)
			if c.Database.Host == "" || c.Database.Port == 0 {
				t.Errorf("%s: Host and Port were not filled: %q %d", a, c.Database.Host, c.Database.Port)
			}
		}
		src := validSource(t)
		src.Environ = append(without(src.Environ, envOf["database.tls.mode"]), envOf["database.tls.ca_file"]+"="+ca)
		mustLoad(t, src) // verify-full with a CA file
	})
}

// TestPurpose is T-114.
func TestPurpose(t *testing.T) {
	envOf := map[string]string{}
	for _, k := range Keys() {
		envOf[k.Name] = k.Env
	}
	t.Run("serve reads the runtime secrets and not the owner's", func(t *testing.T) {
		gw, ad, wk := randomSecret(t), randomSecret(t), randomSecret(t)
		src := validSource(t)
		src.Environ = append(without(src.Environ, envOf["database.gateway.password_file"], envOf["database.admin.password_file"], envOf["database.worker.password_file"]),
			envOf["database.gateway.password_file"]+"="+testutil.WriteSecret(t, "g", gw),
			envOf["database.admin.password_file"]+"="+testutil.WriteSecret(t, "a", ad),
			envOf["database.worker.password_file"]+"="+testutil.WriteSecret(t, "w", wk),
			"PANDOKOS_DATABASE_OWNER_PASSWORD_FILE=/nonexistent/owner",
		)
		c, _ := mustLoad(t, src)
		if c.Database.Gateway.Password.Reveal() != gw || c.Database.Admin.Password.Reveal() != ad || c.Database.Worker.Password.Reveal() != wk {
			t.Error("runtime passwords not read")
		}
		if !c.Database.Owner.Password.IsZero() {
			t.Error("the owner password must not be read by serve")
		}
		if c.Skeleton.AccessToken.IsZero() {
			t.Error("serve must read the skeleton secrets")
		}
		for _, k := range []string{"database.gateway.password_file", "database.admin.password_file", "database.worker.password_file"} {
			s := validSource(t)
			s.Environ = without(s.Environ, envOf[k])
			_, _, err := Load(s)
			if msg, ok := problemMessage(err, k); !ok || msg != "must be set" {
				t.Errorf("%s not set: %q %v", k, msg, err)
			}
			s.Environ = append(s.Environ, envOf[k]+"=/nonexistent/pw")
			_, _, err = Load(s)
			if msg, ok := problemMessage(err, k); !ok || !strings.Contains(msg, "cannot read secret file") {
				t.Errorf("%s missing file: %q %v", k, msg, err)
			}
		}
	})
	t.Run("migrate reads the owner secret only", func(t *testing.T) {
		ow := randomSecret(t)
		src := validMigrateSource(t)
		src.Environ = append(without(src.Environ, envOf["database.owner.password_file"]), envOf["database.owner.password_file"]+"="+testutil.WriteSecret(t, "o", ow))
		// The runtime password files and the skeleton secrets may point to files that do not exist.
		src.Environ = append(src.Environ,
			envOf["database.gateway.password_file"]+"=/nonexistent/gateway",
			envOf["database.admin.password_file"]+"=/nonexistent/admin",
			envOf["database.worker.password_file"]+"=/nonexistent/worker",
			envToken+"=/nonexistent/token",
			envAPIKey+"=/nonexistent/key",
		)
		c, _ := mustLoad(t, src)
		if c.Database.Owner.Password.Reveal() != ow {
			t.Error("the owner password was not read")
		}
		if !c.Database.Gateway.Password.IsZero() || !c.Database.Admin.Password.IsZero() || !c.Database.Worker.Password.IsZero() || !c.Skeleton.AccessToken.IsZero() {
			t.Error("migrate must not read the other secrets")
		}
		// And it needs none of them to be set.
		mustLoad(t, validMigrateSource(t))

		s := validMigrateSource(t)
		s.Environ = without(s.Environ, envOf["database.owner.password_file"])
		_, _, err := Load(s)
		if msg, ok := problemMessage(err, "database.owner.password_file"); !ok || msg != "must be set" {
			t.Errorf("owner file not set: %q %v", msg, err)
		}
		s.Environ = append(s.Environ, envOf["database.owner.password_file"]+"=/nonexistent/owner")
		_, _, err = Load(s)
		if msg, ok := problemMessage(err, "database.owner.password_file"); !ok || !strings.Contains(msg, "cannot read secret file") {
			t.Errorf("owner file missing: %q %v", msg, err)
		}
	})
	t.Run("the format rules apply to both purposes", func(t *testing.T) {
		s := validMigrateSource(t)
		s.Environ = append(s.Environ, "PANDOKOS_LOG_LEVEL=verbose")
		if _, _, err := Load(s); !hasProblem(err, "log.level") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("no problem text contains a password", func(t *testing.T) {
		pw := randomSecret(t)
		s := validSource(t)
		s.Environ = append(s.Environ,
			envOf["database.admin.user"]+"=pandokos_gateway",
			envOf["database.gateway.max_connections"]+"=0",
			envOf["database.gateway.password_file"]+"="+testutil.WriteSecret(t, "gw", pw),
		)
		_, _, err := Load(s)
		if err == nil {
			t.Fatal("expected problems")
		}
		testutil.AssertAbsent(t, "error text", err.Error(), pw)
	})
}

// TestKubernetesServiceVariables is T-115.
func TestKubernetesServiceVariables(t *testing.T) {
	src := validSource(t)
	src.Environ = append(src.Environ,
		"PANDOKOS_DATABASE_PORT=tcp://10.0.0.1:5432",
		"PANDOKOS_DATABASE_PORT_5432_TCP=tcp://10.0.0.1:5432",
		"PANDOKOS_DATABASE_PORT_5432_TCP_PROTO=tcp",
		"PANDOKOS_DATABASE_PORT_5432_TCP_PORT=5432",
		"PANDOKOS_DATABASE_PORT_5432_TCP_ADDR=10.0.0.1",
		"PANDOKOS_DATABASE_SERVICE_HOST=10.0.0.1",
		"PANDOKOS_DATABASE_SERVICE_PORT=5432",
		"PANDOKOS_DATABASE_SERVICE_PORT_POSTGRES=5432",
	)
	_, ws := mustLoad(t, src)
	for _, w := range ws {
		if w.Key == "environment" {
			t.Errorf("unexpected warning: %s", w.Message)
		}
	}
	src.Environ = append(src.Environ, "PANDOKOS_DATABASE_FOO=1")
	_, ws = mustLoad(t, src)
	var got []string
	for _, w := range ws {
		if w.Key == "environment" {
			got = append(got, w.Message)
		}
	}
	if len(got) != 1 || got[0] != "unknown variable PANDOKOS_DATABASE_FOO ignored" {
		t.Errorf("environment warnings: %v", got)
	}
}

// TestDatabaseWarnings is T-116.
func TestDatabaseWarnings(t *testing.T) {
	warn := func(addr string, tlsMode string) (string, bool) {
		src := validSource(t)
		src.Environ = without(src.Environ, "PANDOKOS_DATABASE_ADDRESS", "PANDOKOS_DATABASE_TLS_MODE")
		src.Environ = append(src.Environ, "PANDOKOS_DATABASE_ADDRESS="+addr, "PANDOKOS_DATABASE_TLS_MODE="+tlsMode)
		_, ws := mustLoad(t, src)
		for _, w := range ws {
			if w.Key == "database.tls.mode" {
				return w.Message, true
			}
		}
		return "", false
	}
	if m, ok := warn("db.example.test:5432", "disable"); !ok || m != "the database connection is not encrypted" {
		t.Errorf("disable with a remote host: %q %v", m, ok)
	}
	if m, ok := warn("10.0.0.5:5432", "disable"); !ok || m != "the database connection is not encrypted" {
		t.Errorf("disable with a private address: %q %v", m, ok)
	}
	for _, a := range []string{"127.0.0.1:5432", "[::1]:5432", "localhost:5432"} {
		if m, ok := warn(a, "disable"); ok {
			t.Errorf("disable with %s warned: %s", a, m)
		}
	}
	if m, ok := warn("db.example.test:5432", "verify-full"); ok {
		t.Errorf("verify-full warned: %s", m)
	}
}
