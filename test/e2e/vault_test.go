package e2e

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const kekEnv = "PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE"

var reKEKWritten = regexp.MustCompile(`(?m)^key-encryption key written to (.+); version (local-[0-9a-f]{16})$`)

// generateKEK runs "kek generate" and returns the path of the file and the version it reports.
func generateKEK(t *testing.T) (path, version string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "kek")
	code, out, errs := runBinary(t, nil, "kek", "generate", "--out", path)
	if code != 0 || errs != "" {
		t.Fatalf("kek generate: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	m := reKEKWritten.FindStringSubmatch(out)
	if m == nil || m[1] != path {
		t.Fatalf("kek generate printed %q", out)
	}
	return path, m[2]
}

// withoutVar returns env without the variable.
func withoutVar(env []string, name string) []string {
	var out []string
	for _, e := range env {
		if !strings.HasPrefix(e, name+"=") {
			out = append(out, e)
		}
	}
	return out
}

var reCredentialID = regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`)

// TestVault is T-360: the key-encryption key from "kek generate" to a restart, a wrong key and no key.
func TestVault(t *testing.T) {
	up := fakeprovider.New(t)
	d := dbtest.New(t)
	keyFile, version := generateKEK(t)
	secretFile := testutil.WriteSecret(t, "provider-secret", fixtures.CanaryCredential)

	p := startBinary(t, opts{level: "info", baseURL: up.BaseURL(), db: d, env: []string{kekEnv + "=" + keyFile}})
	ready := completeLines(p.stdout, "vault ready")
	if len(ready) != 1 || ready[0]["kek_provider"] != "local" || ready[0]["kek_version"] != version || ready[0]["level"] != "INFO" {
		t.Fatalf("`vault ready` lines: %v, want one with the provider local and the version %s", ready, version)
	}
	platform := p.bootstrap(t)
	tenantID, adminFile := p.newTenant(t, platform, "vaulted")

	out := p.mustCLI(t, adminFile, "credential", "create", "--tenant", tenantID, "--provider", "openai", "--secret-file", secretFile, "main")
	m := reCredentialID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("credential create printed no ID:\n%s", out)
	}
	credentialID := m[1]
	for _, want := range []string{"provider: openai", "label: main", "state: active", "is_default: true", "hint: f365", "owner: tenant"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("credential create lacks the line %q:\n%s", want, out)
		}
	}
	testutil.AssertAbsent(t, "credential create", out, testutil.SecretForms(fixtures.CanaryCredential)...)
	fingerprint := regexp.MustCompile(`(?m)^fingerprint: ([0-9a-f]{32})$`).FindStringSubmatch(out)
	if fingerprint == nil {
		t.Fatalf("no fingerprint in the output:\n%s", out)
	}
	list := p.mustCLI(t, adminFile, "credential", "list", "--tenant", tenantID)
	if !strings.Contains(list, credentialID) || !strings.Contains(list, "main") {
		t.Errorf("credential list:\n%s", list)
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	t.Run("restart with the same key", func(t *testing.T) {
		p2 := startBinary(t, opts{level: "info", baseURL: up.BaseURL(), db: d, env: []string{kekEnv + "=" + keyFile}})
		ready := completeLines(p2.stdout, "vault ready")
		if len(ready) != 1 || ready[0]["kek_version"] != version {
			t.Fatalf("`vault ready` after the restart: %v", ready)
		}
		got := p2.mustCLI(t, adminFile, "credential", "get", "--tenant", tenantID, credentialID)
		if !strings.Contains(got, "fingerprint: "+fingerprint[1]+"\n") || !strings.Contains(got, "label: main\n") || !strings.Contains(got, "state: active\n") {
			t.Errorf("the metadata after the restart:\n%s", got)
		}
		// A write after the restart works: the data key of the tenant unwraps.
		p2.mustCLI(t, adminFile, "credential", "replace", "--tenant", tenantID, "--secret-file", secretFile, credentialID)
		if code := p2.stop(t); code != 0 {
			t.Fatalf("exit code %d", code)
		}
	})

	t.Run("another key: the process refuses to start and names the version", func(t *testing.T) {
		other, otherVersion := generateKEK(t)
		if otherVersion == version {
			t.Fatal("two generated keys have one version")
		}
		code, out, errs := runBinary(t, serveEnv(t, d, kekEnv+"="+other), "serve", "--config", writeConfig(t, "info"))
		failed := logLines(t, out, "startup failed")
		if code != 1 || len(failed) != 1 {
			t.Fatalf("exit %d, startup failed lines %v\nstdout: %s\nstderr: %s", code, failed, out, errs)
		}
		msg := fmt.Sprint(failed[0]["error"])
		want := "the database holds data keys wrapped by key-encryption key version " + version + " which this configuration does not provide; see docs/operations/vault.md"
		if !strings.Contains(msg, want) {
			t.Errorf("the error is %q, want it to contain %q", msg, want)
		}
		if len(logLines(t, out, "listening")) != 0 || len(logLines(t, out, "vault ready")) != 0 {
			t.Error("the process listened or announced the vault")
		}
		for _, f := range []string{other, keyFile} {
			b, _ := os.ReadFile(f)
			line := strings.TrimSpace(string(b))
			testutil.AssertAbsent(t, "output", out+errs, testutil.SecretForms(line)...)
			if raw, err := base64.StdEncoding.DecodeString(line); err == nil {
				testutil.AssertAbsent(t, "output", out+errs, testutil.SecretForms(string(raw))...)
			}
		}
	})

	t.Run("no key: a configuration error on the key file", func(t *testing.T) {
		env := withoutVar(serveEnv(t, d), kekEnv)
		code, out, errs := runBinary(t, env, "serve", "--config", writeConfig(t, "info"))
		lines := logLines(t, out, "configuration error")
		if code != 1 || len(lines) != 1 || lines[0]["key"] != "vault.kek.local.key_file" {
			t.Fatalf("exit %d, lines %v\nstdout: %s\nstderr: %s", code, lines, out, errs)
		}
		if len(logLines(t, out, "listening")) != 0 {
			t.Error("the process listened")
		}
		// A file that is there and is not a key is the same kind of error, and quotes nothing.
		bad := filepath.Join(t.TempDir(), "not-a-key")
		if err := os.WriteFile(bad, []byte("this-is-not-a-key-0123456789\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errs = runBinary(t, serveEnv(t, d, kekEnv+"="+bad), "serve", "--config", writeConfig(t, "info"))
		lines = logLines(t, out, "configuration error")
		if code != 1 || len(lines) != 1 || lines[0]["key"] != "vault.kek.local.key_file" {
			t.Fatalf("a file that is not a key: exit %d, lines %v", code, lines)
		}
		testutil.AssertAbsent(t, "output", out+errs, "this-is-not-a-key")
		// config check says the same, and migrate and init need no key.
		code, _, errs = runBinary(t, env, "config", "check", "--for", "serve")
		if code != 1 || !strings.Contains(errs, "error: vault.kek.local.key_file: must be set") {
			t.Errorf("config check --for serve: exit %d, stderr %q", code, errs)
		}
		for _, purpose := range []string{"migrate", "init"} {
			if code, out, errs := runBinary(t, env, "config", "check", "--for", purpose); code != 0 {
				t.Errorf("config check --for %s: exit %d\nstdout: %s\nstderr: %s", purpose, code, out, errs)
			}
		}
		if code, out, errs := runBinary(t, env, "migrate", "status"); code != 0 {
			t.Errorf("migrate status without a key: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
		}
	})
}

// forms returns every form of a key-encryption key or a secret that must not be found in an
// output: the plain value, standard and URL base64 and hexadecimal.
func forms(raw []byte) []string {
	return testutil.SecretForms(string(raw))
}

// TestVaultCanaries is T-361: at log level debug, with a provider that echoes the keys it is
// shown, every credential operation and every failure through the CLI and the API leaves no
// canary and no key-encryption key in any output, and none in any column of any table.
func TestVaultCanaries(t *testing.T) {
	up := fakeprovider.New(t)
	up.Accept(fixtures.CanaryCredential)
	d := dbtest.New(t)
	keyFile, _ := generateKEK(t)
	kekText, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	kekLine := strings.TrimSpace(string(kekText))
	kekRaw, err := base64.StdEncoding.DecodeString(kekLine)
	if err != nil || len(kekRaw) != 32 {
		t.Fatalf("the generated key: %d bytes, %v", len(kekRaw), err)
	}
	secretFile := testutil.WriteSecret(t, "provider-secret", fixtures.CanaryCredential)
	otherSecret := "synthetic-provider-secret-for-the-second-credential"
	otherFile := testutil.WriteSecret(t, "provider-secret-2", otherSecret)

	p := startBinary(t, opts{level: "debug", baseURL: up.BaseURL(), db: d, env: []string{kekEnv + "=" + keyFile}})
	platform := p.bootstrap(t)
	tenantID, adminFile := p.newTenant(t, platform, "canaries")

	type output struct{ where, text string }
	var outs []output
	run := func(keyF string, args ...string) (int, string) {
		code, out, errs := p.cli(t, keyF, args...)
		name := strings.Join(args, " ")
		outs = append(outs, output{name + " stdout", out}, output{name + " stderr", errs})
		return code, out
	}
	must := func(keyF string, args ...string) string {
		t.Helper()
		code, out := run(keyF, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d\n%s", args, code, outs[len(outs)-1].text)
		}
		return out
	}
	tenantArgs := func(rest ...string) []string { return append([]string{"--tenant", tenantID}, rest...) }

	// Writes, with validation against the provider that echoes keys it does not accept.
	out := must(adminFile, append([]string{"credential", "create"}, tenantArgs("--provider", "openai", "--secret-file", secretFile, "--validate", "main")...)...)
	id1 := reCredentialID.FindStringSubmatch(out)[1]
	out = must(adminFile, append([]string{"credential", "create"}, tenantArgs("--provider", "openai", "--secret-file", otherFile, "second")...)...)
	id2 := reCredentialID.FindStringSubmatch(out)[1]
	if code, _ := run(adminFile, append([]string{"credential", "create"}, tenantArgs("--provider", "openai", "--secret-file", otherFile, "--validate", "rejected")...)...); code != 1 {
		t.Errorf("a credential that the provider rejects: exit %d", code)
	}
	up.ModelsBehaviour("500")
	if code, _ := run(adminFile, append([]string{"credential", "create"}, tenantArgs("--provider", "openai", "--secret-file", secretFile, "--validate", "unavailable")...)...); code != 1 {
		t.Errorf("a provider that fails: exit %d", code)
	}
	up.ModelsBehaviour("")
	if code, _ := run(adminFile, append([]string{"credential", "create"}, tenantArgs("--provider", "openai", "--secret-file", secretFile, "main")...)...); code != 1 {
		t.Errorf("a label that is taken: exit %d", code)
	}
	if code, _ := run(adminFile, append([]string{"credential", "create"}, tenantArgs("--provider", "gemini", "--secret-file", secretFile, "x")...)...); code != 1 {
		t.Errorf("an unknown provider: exit %d", code)
	}
	if code, _ := run(adminFile, append([]string{"credential", "create"}, tenantArgs("--provider", "openai", "--secret", fixtures.CanaryCredential, "x")...)...); code != 2 {
		t.Errorf("a flag that does not exist: exit %d", code)
	}
	must(adminFile, append([]string{"credential", "replace"}, tenantArgs("--secret-file", secretFile, "--validate", id1)...)...)
	must(adminFile, append([]string{"credential", "update"}, tenantArgs("--description", "the main one", id1)...)...)
	for _, verb := range []string{"list", "get", "disable", "enable", "set-default"} {
		switch verb {
		case "list":
			must(adminFile, append([]string{"credential", "list"}, tenantArgs()...)...)
			must(adminFile, append([]string{"credential", "list"}, tenantArgs("-o", "json")...)...)
		case "set-default":
			must(adminFile, append([]string{"credential", verb}, tenantArgs(id2)...)...)
		default:
			must(adminFile, append([]string{"credential", verb}, tenantArgs(id1)...)...)
		}
	}
	must(adminFile, append([]string{"credential", "get"}, tenantArgs("-o", "json", id1)...)...)
	// The platform's side.
	must(platform, "tenant-credential", "list", "--tenant", tenantID)
	must(platform, "tenant-credential", "get", "--tenant", tenantID, id1)
	must(platform, "tenant-credential", "disable", "--tenant", tenantID, "--reason", "abuse report", id1)
	must(adminFile, append([]string{"credential", "enable"}, tenantArgs(id1)...)...)
	// A key pinned to a credential, and one that is refused.
	must(adminFile, "service-account", "create", "--tenant", tenantID, "--role", "inference", "caller")
	keyOut := must(adminFile, "key", "create", "--tenant", tenantID, "--service-account", "caller", "--credential", id1)
	pinned := regexp.MustCompile(`(?m)^id: (t[a-z2-7]{16})$`).FindStringSubmatch(keyOut)
	if pinned == nil {
		t.Fatalf("no key ID in the output of key create:\n%s", keyOut)
	}
	if code, _ := run(adminFile, "key", "create", "--tenant", tenantID, "--service-account", "caller", "--credential", "0192f0f0-0000-7000-8000-00000000beef"); code != 1 {
		t.Errorf("a pin to a credential that is not there: exit %d", code)
	}
	must(platform, "tenant-credential", "revoke", "--tenant", tenantID, "--reason", "abuse confirmed", id1)
	must(adminFile, append([]string{"credential", "revoke"}, tenantArgs(id2)...)...)
	if code, _ := run(adminFile, append([]string{"credential", "replace"}, tenantArgs("--secret-file", secretFile, id2)...)...); code != 1 {
		t.Errorf("a replacement of a revoked credential: exit %d", code)
	}
	must(adminFile, append([]string{"credential", "delete"}, tenantArgs(id2)...)...)
	if code, _ := run(adminFile, append([]string{"credential", "delete"}, tenantArgs(id1)...)...); code != 1 {
		t.Errorf("deleting a credential that a usable key is pinned to: exit %d", code)
	}
	must(adminFile, "key", "revoke", "--tenant", tenantID, pinned[1])
	must(adminFile, append([]string{"credential", "delete"}, tenantArgs(id1)...)...)
	// Mistakes of the caller, through the API itself, with the canary where it must not be echoed.
	var responses []output
	raw := func(label, method, path, key string, body any, hdr http.Header) {
		t.Helper()
		responses = append(responses, output{label, p.api(t, method, path, key, body, hdr)})
	}
	base := "/v1/tenants/" + tenantID + "/credentials"
	adminKey := readKey(t, adminFile)
	raw("a body that is not JSON", "POST", base, adminKey, `{"provider":"openai","label":"x","secret":"`+fixtures.CanaryCredential, nil)
	raw("another content type", "POST", base, adminKey, `{"secret":"`+fixtures.CanaryCredential+`"}`, http.Header{"Content-Type": {"text/plain"}})
	raw("an unknown member", "POST", base, adminKey, map[string]any{"provider": "openai", "label": "x", "secret": fixtures.CanaryCredential, fixtures.CanaryCredential: 1}, nil)
	raw("the secret in the query", "GET", base+"?secret="+fixtures.CanaryCredential, adminKey, nil, nil)
	raw("the secret in the path", "GET", base+"/"+fixtures.CanaryCredential, adminKey, nil, nil)
	raw("the secret as the key", "GET", base, fixtures.CanaryCredential, nil, nil)
	raw("a secret that is too short", "POST", base, adminKey, map[string]any{"provider": "openai", "label": "x", "secret": fixtures.CanaryCredential[:5]}, nil)
	raw("a rejected credential", "POST", base, adminKey, map[string]any{"provider": "openai", "label": "x", "secret": otherSecret, "validate": true}, nil)
	raw("a replacement body with the canary as a member name", "PUT", base+"/"+id2+"/secret", adminKey, map[string]any{fixtures.CanaryCredential: otherSecret}, nil)
	raw("the platform with a secret", "POST", "/v1/tenant-credentials/"+tenantID+"/"+id1+"/disable", readKey(t, platform), map[string]any{"reason": "x", "secret": fixtures.CanaryCredential}, nil)

	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	outs = append(outs, output{"server stdout", p.stdout.String()}, output{"server stderr", p.stderr.String()})

	// Every column of every table, as the superuser sees it: bytea comes out in hexadecimal.
	super := d.Super(t)
	rows, err := super.Query(context.Background(), `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) < 10 {
		t.Fatalf("only %d tables were found", len(tables))
	}
	for _, table := range tables {
		var dump string
		if err := super.QueryRow(context.Background(), `SELECT COALESCE(string_agg(to_jsonb(t)::text, E'\n'), '') FROM pandokos."`+strings.ReplaceAll(table, `"`, `""`)+`" t`).Scan(&dump); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		outs = append(outs, output{"table " + table, dump})
	}
	find := func(where string) string {
		for _, o := range outs {
			if o.where == where {
				return o.text
			}
		}
		return ""
	}
	if !strings.Contains(find("table tenant_data_key"), `"wrapped_key"`) {
		t.Error("the dump of tenant_data_key has no wrapped key: the scan would prove nothing")
	}
	if !strings.Contains(find("table kek_version"), `"id"`) {
		t.Error("the dump of kek_version is empty")
	}

	forbidden := map[string][]string{
		"the credential":                  forms([]byte(fixtures.CanaryCredential)),
		"the second secret":               forms([]byte(otherSecret)),
		"the key-encryption key (raw)":    forms(kekRaw),
		"the key-encryption key (file)":   {kekLine},
		"the hexadecimal key":             {hex.EncodeToString(kekRaw), strings.ToUpper(hex.EncodeToString(kekRaw))},
		"the hexadecimal canary":          {hex.EncodeToString([]byte(fixtures.CanaryCredential))},
		"the base64 of the canary (URL)":  {base64.URLEncoding.EncodeToString([]byte(fixtures.CanaryCredential))},
		"the base64 of the second secret": {base64.StdEncoding.EncodeToString([]byte(otherSecret))},
	}
	scanned := 0
	for _, o := range append(append([]output{}, outs...), responses...) {
		for what, fs := range forbidden {
			for _, f := range fs {
				if len(f) >= 8 && strings.Contains(o.text, f) {
					t.Errorf("%s is in %s", what, o.where)
				}
			}
		}
		scanned++
	}
	if scanned < 60 {
		t.Errorf("only %d outputs were scanned", scanned)
	}
	// The provider echoed what it did not accept: the fake saw the rejected secret, and nothing of it came back.
	sawRejected := false
	for _, r := range up.Requests() {
		if strings.Contains(r.Header.Get("Authorization"), otherSecret) {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Error("the validation of the rejected credential never reached the provider: the echo was not exercised")
	}
	// And the debug log has the lines it should.
	if len(logLines(t, p.stdout.String(), "credential validation")) < 3 {
		t.Errorf("the debug log has %d `credential validation` lines, want at least 3", len(logLines(t, p.stdout.String(), "credential validation")))
	}
}

// api sends a request to the administration listener and returns the whole response as text.
// body is nil, a string (sent as it is) or a value (sent as JSON).
func (p *proc) api(t *testing.T, method, path, key string, body any, extra http.Header) string {
	t.Helper()
	var b []byte
	switch v := body.(type) {
	case nil:
	case string:
		b = []byte(v)
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	h := http.Header{"Content-Type": {"application/json"}}
	if key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	for k, v := range extra {
		h[k] = v
	}
	_, text := exchangeWith(t, p.client, method, p.adminURL(path), h, b)
	return text
}

// readKey returns the key in a key file.
func readKey(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
