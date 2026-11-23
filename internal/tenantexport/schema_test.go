package tenantexport_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/tenantexport"
	"github.com/pandokos/pandokos/test/testutil"
)

// refsOf collects every "$ref" in a decoded JSON value.
func refsOf(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if k == "$ref" {
				if s, ok := e.(string); ok {
					*out = append(*out, s)
				}
				continue
			}
			refsOf(e, out)
		}
	case []any:
		for _, e := range x {
			refsOf(e, out)
		}
	}
}

// TestSchema is T-1002: the schema of the export is generated from the administration API's
// document, committed, embedded in the binary, and consistent with what the writer writes.
func TestSchema(t *testing.T) {
	root := testutil.RepoRoot(t)
	committed, err := os.ReadFile(filepath.Join(root, "docs", "reference", "tenant-export.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the committed schema is what the generator makes", func(t *testing.T) {
		tmp := t.TempDir()
		cmd := exec.Command("go", "run", "./internal/apigen", "-spec", "api/openapi/admin.v1.yaml", "-out", filepath.Join(tmp, "adminapi.gen.go"), "-export-schema", filepath.Join(tmp, "schema.json"))
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("apigen -export-schema: %v\n%s", err, out)
		}
		got, err := os.ReadFile(filepath.Join(tmp, "schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, committed) {
			t.Error("docs/reference/tenant-export.v1.schema.json is not what the generator makes from api/openapi/admin.v1.yaml: run make generate")
		}
		if !bytes.Equal(tenantexport.Schema, committed) {
			t.Error("the schema embedded in the binary is not the committed one: make generate copies it to internal/tenantexport")
		}
		if !bytes.HasSuffix(committed, []byte("}\n")) || bytes.Contains(committed, []byte("\r")) || bytes.Contains(committed, []byte("\t")) {
			t.Error("the schema is not in the generator's form: two-space indentation and one final newline")
		}
	})

	var schema map[string]any
	if err := json.Unmarshal(committed, &schema); err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}

	t.Run("it is a JSON Schema 2020-12 with the manifest at the root", func(t *testing.T) {
		if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["$id"] != "urn:pandokos:schema:tenant-export:v1" || schema["title"] == nil {
			t.Errorf("$schema %v, $id %v, title %v", schema["$schema"], schema["$id"], schema["title"])
		}
		props, _ := schema["properties"].(map[string]any)
		for _, k := range []string{"format", "version", "tenant_id", "created_at", "pandokos_version", "files", "audit_head", "usage_from", "usage_to"} {
			if props[k] == nil {
				t.Errorf("the manifest schema has no member %s", k)
			}
		}
		required, _ := schema["required"].([]any)
		for _, k := range []string{"format", "version", "tenant_id", "files"} {
			found := false
			for _, r := range required {
				found = found || r == k
			}
			if !found {
				t.Errorf("%s is not required by the manifest schema", k)
			}
		}
		// The format version is a constant of the manifest.
		if v, _ := props["version"].(map[string]any); v["const"] != float64(1) && !(len(asSlice(v["enum"])) == 1 && asSlice(v["enum"])[0] == float64(1)) {
			t.Errorf("the version member is %v, want the constant 1", v)
		}
		blob := string(committed)
		if strings.Contains(blob, "x-pandokos-") {
			t.Error("the schema keeps a vendor member of the OpenAPI document")
		}
		if strings.Contains(blob, "#/components/") {
			t.Error("the schema refers to the OpenAPI document")
		}
	})

	t.Run("it has an entry for every kind the writer writes, and no unused one", func(t *testing.T) {
		defs, _ := schema["$defs"].(map[string]any)
		for _, k := range tenantexport.Kinds {
			if defs[k.Def] == nil {
				t.Errorf("no $defs entry %s for the kind %s", k.Def, k.Name)
			}
		}
		// Every entry is reached from the root or from a kind.
		reached := map[string]bool{}
		var visit func(v any)
		visit = func(v any) {
			var refs []string
			refsOf(v, &refs)
			for _, r := range refs {
				name := strings.TrimPrefix(r, "#/$defs/")
				if !reached[name] {
					reached[name] = true
					visit(defs[name])
				}
			}
		}
		rootWithoutDefs := map[string]any{}
		for k, v := range schema {
			if k != "$defs" {
				rootWithoutDefs[k] = v
			}
		}
		visit(rootWithoutDefs)
		for _, k := range tenantexport.Kinds {
			reached[k.Def] = true
			visit(defs[k.Def])
		}
		var unused []string
		for name := range defs {
			if !reached[name] {
				unused = append(unused, name)
			}
		}
		sort.Strings(unused)
		if len(unused) != 0 {
			t.Errorf("$defs entries that no kind reaches: %v", unused)
		}
	})

	t.Run("every reference resolves inside the file", func(t *testing.T) {
		defs, _ := schema["$defs"].(map[string]any)
		var refs []string
		refsOf(schema, &refs)
		if len(refs) == 0 {
			t.Error("the schema has no $ref: the kinds share no component")
		}
		for _, r := range refs {
			if r == "#" {
				continue
			}
			name, ok := strings.CutPrefix(r, "#/$defs/")
			if !ok || defs[name] == nil {
				t.Errorf("the reference %s does not resolve inside the file", r)
			}
		}
	})

	t.Run("the output is deterministic", func(t *testing.T) {
		var round any
		if err := json.Unmarshal(committed, &round); err != nil {
			t.Fatal(err)
		}
		again, err := json.MarshalIndent(round, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(append(again, '\n'), committed) {
			t.Error("the committed schema is not sorted and indented as json.MarshalIndent makes it (sorted keys, two spaces, one final newline)")
		}
	})
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
