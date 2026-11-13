package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/tenant"
)

// TestDetailGuard is T-209.
func TestDetailGuard(t *testing.T) {
	refused := map[string]map[string]any{
		"secret":                  {"secret": "x"},
		"secret_hash":             {"secret_hash": "x"},
		"hash":                    {"hash": "x"},
		"token":                   {"token": "x"},
		"password":                {"password": "x"},
		"authorization":           {"Authorization": "x"},
		"suffix _key":             {"api_key": "x"},
		"depth three":             {"a": map[string]any{"b": map[string]any{"secret": "x"}}},
		"depth three, in a list":  {"a": []any{map[string]any{"b": map[string]any{"password": "x"}}}},
		"depth one, other casing": {"Secret": "x"},
		"too large":               {"note": strings.Repeat("x", 8193)},
	}
	for name, d := range refused {
		if _, err := marshalDetail(d); err == nil {
			t.Errorf("%s: the detail was accepted", name)
		}
		// Both writers refuse before they touch the transaction, so a nil one is enough.
		e := Event{Action: "test.guard", ResourceType: "test", Outcome: "success", Detail: d}
		if err := RecordPlatform(context.Background(), nil, e); err == nil {
			t.Errorf("%s: RecordPlatform accepted the detail", name)
		}
		if err := RecordTenant(context.Background(), nil, tenant.ID{1}, e); err == nil {
			t.Errorf("%s: RecordTenant accepted the detail", name)
		}
	}
	ok, err := marshalDetail(map[string]any{
		"name": "acme", "from": "a", "to": "b", "roles": []string{"inference"}, "expires_at": "2026-11-13T10:00:00Z",
		"nested": map[string]any{"from": 1, "to": 2}, "key_id": "tabcdefghijklmnop",
	})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(ok), &back); err != nil || back["name"] != "acme" {
		t.Errorf("an ordinary detail came out as %q, %v", ok, err)
	}
	for _, nilDetail := range []map[string]any{nil, {}} {
		if s, err := marshalDetail(nilDetail); err != nil || s != "{}" {
			t.Errorf("an empty detail is %q, %v; want {}", s, err)
		}
	}
	if _, err := marshalDetail(map[string]any{"note": strings.Repeat("x", 8000)}); err != nil {
		t.Errorf("a detail under the limit was refused: %v", err)
	}
}

func TestActorOf(t *testing.T) {
	var tid tenant.ID
	tid[0] = 1
	sa := ActorOf(authz.Principal{Kind: authz.KindServiceAccount, ID: "sa-id", Name: "bot", KeyID: "tkey", TenantID: tid})
	if sa != (Actor{Type: ActorType("service_account"), ID: "sa-id", Name: "bot", KeyID: "tkey"}) {
		t.Errorf("service account actor: %+v", sa)
	}
	pl := ActorOf(authz.Principal{Kind: authz.KindPlatform, ID: "pkey", Name: "bootstrap", KeyID: "pkey"})
	if pl != (Actor{Type: ActorType("platform"), ID: "pkey", Name: "bootstrap", KeyID: "pkey"}) {
		t.Errorf("platform actor: %+v", pl)
	}
}
