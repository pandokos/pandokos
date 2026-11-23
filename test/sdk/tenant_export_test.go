package sdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/tenantexport"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

const schemaURL = "urn:pandokos:schema:tenant-export:v1"

var (
	binaryOnce sync.Once
	binaryPath string
	binaryErr  error
)

// pandokosBinary builds the binary of the main module once for the tests of this file.
func pandokosBinary(t *testing.T) string {
	t.Helper()
	binaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pandokos-sdk-")
		if err != nil {
			binaryErr = err
			return
		}
		binaryPath = filepath.Join(dir, "pandokos")
		root := testutil.RepoRoot(t)
		cmd := exec.Command("go", "build", "-o", binaryPath, "github.com/pandokos/pandokos/cmd/pandokos")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			binaryErr = &buildError{err: err, out: string(out)}
		}
	})
	if binaryErr != nil {
		t.Fatalf("building the binary: %v", binaryErr)
	}
	return binaryPath
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

// run runs the binary with the environment and returns the exit code and the two outputs.
func run(t *testing.T, env []string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pandokosBinary(t), args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

// serve starts `pandokos serve` on the database and returns the address of its administration listener.
func serve(t *testing.T, d *dbtest.Database) string {
	t.Helper()
	return serveWith(t, d, d.Env(t))
}

// serveWith is serve with the environment given (d.Env or d.EnvWith).
func serveWith(t *testing.T, d *dbtest.Database, env []string) string {
	t.Helper()
	_ = d
	cfg := filepath.Join(t.TempDir(), "pandokos.yaml")
	if err := os.WriteFile(cfg, []byte("log:\n  level: info\nlisteners:\n  inference:\n    address: 127.0.0.1:0\n  admin:\n    address: 127.0.0.1:0\nshutdown:\n  drain_timeout: 5s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &testutil.LogBuffer{}
	cmd := exec.Command(pandokosBinary(t), "serve", "--config", cfg)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	})
	var admin string
	testutil.Eventually(t, 15*time.Second, "the listening log lines", func() bool {
		for _, l := range logs.Lines(t) {
			if l["msg"] == "listening" && l["listener"] == "admin" {
				admin, _ = l["address"].(string)
			}
		}
		return admin != ""
	})
	return admin
}

// TestTenantExportReadsWithSchema is T-1003, an exit test of the sprint: the binary exports the
// data of a tenant that has a row of every kind, and what it writes is read with the schema that the
// binary prints, by a validator that is not ours.
func TestTenantExportReadsWithSchema(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.PopulatedTenant(t)
	id := tid.String()

	// An owner of the tenant, with a key: the export reads through the API as the tenant does.
	key, keyID, hash, err := authn.Generate(authn.KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	sa := d.SeedServiceAccount(t, tid, "exporter")
	ctx := context.Background()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'owner')`, []any{id, sa}},
		{`INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, $4)`, []any{id, sa, keyID, hash[:]}},
	} {
		if _, err := d.Super(t).Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	keyFile := testutil.WriteSecret(t, "owner-key", key.Reveal())
	admin := serve(t, d)
	cli := []string{"PANDOKOS_CLI_SERVER=http://" + admin, "PANDOKOS_CLI_KEY_FILE=" + keyFile}

	// The schema as the binary prints it, compiled by the validator.
	code, schemaText, errs := run(t, nil, "tenant-export", "schema")
	if code != 0 || errs != "" {
		t.Fatalf("tenant-export schema: exit %d\nstderr: %s", code, errs)
	}
	if !bytes.Equal([]byte(schemaText), tenantexport.Schema) {
		t.Error("the schema the binary prints is not the one embedded in the package")
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemaText))
	if err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, doc); err != nil {
		t.Fatalf("add the schema: %v", err)
	}
	root, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatalf("compile the schema: %v", err)
	}
	defs := map[string]*jsonschema.Schema{}
	for _, k := range tenantexport.Kinds {
		s, err := compiler.Compile(schemaURL + "#/$defs/" + k.Def)
		if err != nil {
			t.Fatalf("compile $defs/%s: %v", k.Def, err)
		}
		defs[k.Name] = s
	}

	// The export.
	out := filepath.Join(t.TempDir(), "export")
	code, stdout, errs := run(t, cli, "tenant-export", "--tenant", id, "--out", out)
	if code != 0 {
		t.Fatalf("tenant-export: exit %d\nstdout: %s\nstderr: %s", code, stdout, errs)
	}
	rawManifest, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestValue, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawManifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Validate(manifestValue); err != nil {
		t.Errorf("the manifest does not validate against the schema: %v", err)
	}
	var m tenantexport.Manifest
	if err := json.Unmarshal(rawManifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.TenantID != id || len(m.Files) != len(tenantexport.Kinds) {
		t.Fatalf("the manifest: %+v", m)
	}

	var validated int
	var firstLine = map[string][]byte{}
	for _, f := range m.Files {
		raw, err := os.ReadFile(filepath.Join(out, f.Name))
		if err != nil {
			t.Errorf("%s: %v", f.Name, err)
			continue
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Errorf("%s: the digest in the manifest is not the file's", f.Name)
		}
		schema := defs[f.Kind]
		if schema == nil {
			t.Errorf("%s: the manifest names the kind %s, which has no entry in the schema", f.Name, f.Kind)
			continue
		}
		var lines [][]byte
		if f.Kind == "settings" {
			lines = [][]byte{bytes.TrimSpace(raw)}
		} else {
			for _, l := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
				if len(l) > 0 {
					lines = append(lines, l)
				}
			}
		}
		if int64(len(lines)) != f.Records {
			t.Errorf("%s: %d lines, the manifest says %d records", f.Name, len(lines), f.Records)
		}
		if f.Records < 1 {
			t.Errorf("%s: no record: the tenant of the test has a row of every kind", f.Name)
		}
		for i, l := range lines {
			v, err := jsonschema.UnmarshalJSON(bytes.NewReader(l))
			if err != nil {
				t.Errorf("%s line %d is not JSON: %v", f.Name, i+1, err)
				continue
			}
			if err := schema.Validate(v); err != nil {
				t.Errorf("%s line %d does not validate against $defs/%s: %v\n%s", f.Name, i+1, f.Kind, err, l)
			}
			validated++
		}
		if len(lines) > 0 {
			firstLine[f.Kind] = lines[0]
		}
	}
	if validated < len(tenantexport.Kinds) {
		t.Errorf("%d lines validated for %d kinds", validated, len(tenantexport.Kinds))
	}

	// What the schema must catch: a required member removed, a member of the wrong type.
	caught := 0
	for kind, line := range firstLine {
		if kind == "settings" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			t.Fatal(err)
		}
		required := requiredOf(t, string(tenantexport.Schema), kind)
		if len(required) == 0 {
			t.Errorf("the schema of %s names no required member", kind)
			continue
		}
		member := required[0]
		broken := map[string]any{}
		for k, v := range obj {
			if k != member {
				broken[k] = v
			}
		}
		if err := defs[kind].Validate(roundTrip(t, broken)); err == nil {
			t.Errorf("a line of %s without its required member %s validates", kind, member)
		} else {
			caught++
		}
		wrong := map[string]any{}
		for k, v := range obj {
			wrong[k] = v
		}
		wrong[member] = []any{1, 2, 3} // an array where no member is one
		if err := defs[kind].Validate(roundTrip(t, wrong)); err == nil {
			t.Errorf("a line of %s with %s of the wrong type validates", kind, member)
		}
	}
	if caught == 0 {
		t.Error("no malformed line was tried")
	}
}

// requiredOf returns the required members of the $defs entry of the kind, following a $ref to another
// entry once.
func requiredOf(t *testing.T, schema, kind string) []string {
	t.Helper()
	var s struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		t.Fatal(err)
	}
	var def string
	for _, k := range tenantexport.Kinds {
		if k.Name == kind {
			def = k.Def
		}
	}
	d := s.Defs[def]
	if ref, ok := d["$ref"].(string); ok {
		d = s.Defs[strings.TrimPrefix(ref, "#/$defs/")]
	}
	var out []string
	if r, ok := d["required"].([]any); ok {
		for _, x := range r {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
	}
	return out
}

func roundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
