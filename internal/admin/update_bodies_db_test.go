package admin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/fixtures"
)

// TestUpdateBodies is T-407: for the six update bodies, a member that is absent is unchanged, an
// empty string clears, and null for a member that is not nullable is a 400 that names it. The
// sentinel of sprint 2.1 no longer exists.
func TestUpdateBodies(t *testing.T) {
	c := newCat(t)
	api := c.api
	sa, err := api.Client(c.ka).CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: c.a.ID, Body: adminapi.ServiceAccountCreate{Name: "updates", Roles: []string{"inference"}}})
	if err != nil {
		t.Fatal(err)
	}
	cred := api.Credential(t, c.a.ID, c.ka, "openai", "updates")
	p := c.createProvider(t, map[string]any{"display_name": "Provider", "description": "provider description", "dataset_provider": "synthetic"})
	ep := c.createEndpoint(t, p.Name, map[string]any{"region": "eu", "description": "endpoint description"})
	m := c.createModel(t, p.Name, map[string]any{"display_name": "Model", "description": "model description", "capabilities": []string{"text"}, "context_window": 4096, "max_output_tokens": 512})

	type object struct {
		name string
		path string
		key  string
		// set is a valid body; the members it sets must be unchanged when absent from a later body.
		seed map[string]any
		// clear sets members to the empty value of their type; the value they should read back.
		clear map[string]any
		want  map[string]any
		// null lists members that are not nullable.
		null []string
		// nullable lists members for which null is a value (clears).
		nullable []string
	}
	objects := []object{
		{name: "tenant", path: "/v1/tenants/" + c.a.ID, key: c.plat,
			seed: map[string]any{"display_name": "Seeded"}, clear: map[string]any{"display_name": ""}, want: map[string]any{"display_name": ""},
			null: []string{"name", "display_name"}},
		{name: "service account", path: "/v1/tenants/" + c.a.ID + "/service-accounts/" + sa.ID, key: c.ka,
			seed: map[string]any{"description": "seeded", "roles": []string{"inference"}}, clear: map[string]any{"description": "", "roles": []string{}},
			want: map[string]any{"description": "", "roles": []any{}}, null: []string{"name", "description", "roles"},
			nullable: []string{"valid_from", "valid_until"}},
		{name: "credential", path: "/v1/tenants/" + c.a.ID + "/credentials/" + cred.ID, key: c.ka,
			seed: map[string]any{"description": "seeded"}, clear: map[string]any{"description": ""}, want: map[string]any{"description": ""},
			null: []string{"label", "description"}, nullable: []string{"expires_at"}},
		{name: "provider", path: "/v1/providers/" + p.Name, key: c.plat,
			seed: map[string]any{"description": "seeded"}, clear: map[string]any{"description": "", "display_name": "", "dataset_provider": "", "enabled": false},
			want: map[string]any{"description": "", "display_name": "", "dataset_provider": "", "enabled": false}, null: []string{"display_name", "description", "enabled", "dataset_provider"}},
		{name: "endpoint", path: "/v1/providers/" + p.Name + "/endpoints/" + ep.Name, key: c.plat,
			seed: map[string]any{"description": "seeded"}, clear: map[string]any{"description": "", "region": "", "private": false, "enabled": false},
			want: map[string]any{"description": "", "region": "", "private": false, "enabled": false}, null: []string{"base_url", "region", "private", "enabled", "is_default", "description"}},
		{name: "model", path: "/v1/models/" + m.ID, key: c.plat,
			seed: map[string]any{"description": "seeded"}, clear: map[string]any{"description": "", "display_name": "", "capabilities": []string{}, "context_window": 0, "max_output_tokens": 0},
			want: map[string]any{"description": "", "display_name": "", "capabilities": []any{}, "context_window": float64(0), "max_output_tokens": float64(0)},
			null: []string{"upstream_id", "display_name", "description", "status", "availability", "capabilities", "context_window", "max_output_tokens"}},
	}

	for _, o := range objects {
		t.Run(o.name, func(t *testing.T) {
			read := func() map[string]any {
				t.Helper()
				// The resource is read where it lives: the path of the update, with GET.
				return rawJSON(t, api.Do(t, "GET", o.path, o.key, nil, nil))
			}
			patch := func(body map[string]any) (int, map[string]any) {
				t.Helper()
				r := api.Do(t, "PATCH", o.path, o.key, body, nil)
				if r.Status == 200 {
					return r.Status, rawJSON(t, r)
				}
				return r.Status, nil
			}
			before := read()

			// An absent member is unchanged: send one member, see every other as it was.
			if st, got := patch(o.seed); st != 200 {
				t.Fatalf("a valid update: %d", st)
			} else {
				for k := range o.seed {
					if want := jsonRoundTrip(o.seed[k]); !equalJSON(got[k], want) {
						t.Errorf("%s was set to %v and reads back %v", k, o.seed[k], got[k])
					}
				}
			}
			seeded := read()
			for k, v := range before {
				if _, set := o.seed[k]; set || k == "updated_at" {
					continue
				}
				if !equalJSON(seeded[k], v) {
					t.Errorf("%s changed from %v to %v although the update did not name it", k, v, seeded[k])
				}
			}
			// An empty value clears.
			if st, got := patch(o.clear); st != 200 {
				t.Fatalf("clearing: %d", st)
			} else {
				for k, want := range o.want {
					if !equalJSON(got[k], want) {
						t.Errorf("%s: %v, want the empty value %v", k, got[k], want)
					}
				}
			}
			// null is refused for a member that cannot be null, and names it.
			for _, member := range o.null {
				r := api.Do(t, "PATCH", o.path, o.key, map[string]any{member: nil}, nil)
				invalidOn(t, o.name+" with "+member+": null", r, member)
			}
			// null clears a member that can be null.
			for _, member := range o.nullable {
				if st, got := patch(map[string]any{member: nil}); st != 200 || got[member] != nil {
					t.Errorf("%s: null gave %d and %v", member, st, got[member])
				}
			}
			// Nothing was changed by the refused bodies.
			after := read()
			for k := range o.want {
				if !equalJSON(after[k], o.want[k]) {
					t.Errorf("%s was changed by a refused body: %v", k, after[k])
				}
			}
		})
	}

	t.Run("the sentinel and its machinery are gone", func(t *testing.T) {
		root := filepath.Join("..", "..")
		for file, words := range map[string][]string{
			filepath.Join(root, "internal", "adminapi", "runtime.go"): {"EmptyDescription"},
			filepath.Join(root, "internal", "admin", "helpers.go"):    {"peekUpdate", "descriptionGiven"},
			filepath.Join(root, "internal", "cli", "commands.go"):     {"EmptyDescription", "patchWithoutRoles"},
			filepath.Join(root, "internal", "cli", "credentials.go"):  {"EmptyDescription"},
		} {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range words {
				if strings.Contains(string(b), w) {
					t.Errorf("%s still mentions %s", file, w)
				}
			}
		}
		entries, err := os.ReadDir(filepath.Join(root, "internal", "admin"))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), "_test.go") || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			b, _ := os.ReadFile(filepath.Join(root, "internal", "admin", e.Name()))
			for _, w := range []string{"peekUpdate", "descriptionGiven", "EmptyDescription"} {
				if strings.Contains(string(b), w) {
					t.Errorf("internal/admin/%s still mentions %s", e.Name(), w)
				}
			}
		}
	})

	// A canary: the secret of a credential is not in an update response either.
	r := api.Do(t, "PATCH", "/v1/tenants/"+c.a.ID+"/credentials/"+cred.ID, c.ka, map[string]any{"description": "x"}, nil)
	if strings.Contains(string(r.Body), fixtures.CanaryCredential) {
		t.Error("the secret is in an update response")
	}
}

// jsonRoundTrip is what a Go value reads back as from JSON.
func jsonRoundTrip(v any) any {
	var out any
	_ = json.Unmarshal(mustJSONBytes(v), &out)
	return out
}

func equalJSON(a, b any) bool { return string(mustJSONBytes(a)) == string(mustJSONBytes(b)) }
