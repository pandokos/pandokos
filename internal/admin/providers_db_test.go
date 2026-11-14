package admin_test

import (
	"reflect"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestProviderStreamUsageOption is T-542: create, update, get and list carry stream_usage_option;
// omit is refused for a provider of family anthropic; the audit detail holds it.
func TestProviderStreamUsageOption(t *testing.T) {
	c := newCat(t)
	seen := seenIDs(t, c.api)

	t.Run("the default is add", func(t *testing.T) {
		p := c.createProvider(t, map[string]any{"name": "synthetic-opt-default"})
		if p.StreamUsageOption != adminapi.StreamUsageOptionAdd {
			t.Errorf("created with %q", p.StreamUsageOption)
		}
		got := decode[adminapi.Provider](t, c.platform(t, "GET", "/v1/providers/"+p.Name, nil), 200)
		if got.StreamUsageOption != adminapi.StreamUsageOptionAdd {
			t.Errorf("get: %q", got.StreamUsageOption)
		}
	})

	t.Run("omit at creation, and every read carries it", func(t *testing.T) {
		p := c.createProvider(t, map[string]any{"name": "synthetic-opt-omit", "stream_usage_option": "omit"})
		if p.StreamUsageOption != adminapi.StreamUsageOptionOmit {
			t.Fatalf("created with %q", p.StreamUsageOption)
		}
		if got := decode[adminapi.Provider](t, c.platform(t, "GET", "/v1/providers/"+p.Name, nil), 200); got.StreamUsageOption != adminapi.StreamUsageOptionOmit {
			t.Errorf("get: %q", got.StreamUsageOption)
		}
		found := false
		for _, it := range decode[adminapi.ProviderList](t, c.platform(t, "GET", "/v1/providers?limit=200", nil), 200).Items {
			if it.Name == p.Name {
				found = true
				if it.StreamUsageOption != adminapi.StreamUsageOptionOmit {
					t.Errorf("list: %q", it.StreamUsageOption)
				}
			}
		}
		if !found {
			t.Error("the provider is not listed")
		}
		for _, it := range decode[adminapi.ProviderList](t, c.platform(t, "GET", "/v1/providers?limit=200", nil), 200).Items {
			if (it.Name == "openai" || it.Name == "anthropic") && it.StreamUsageOption != adminapi.StreamUsageOptionAdd {
				t.Errorf("the seeded provider %s has %q", it.Name, it.StreamUsageOption)
			}
		}
	})

	t.Run("update", func(t *testing.T) {
		p := c.createProvider(t, map[string]any{"name": "synthetic-opt-update"})
		got := decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/"+p.Name, map[string]any{"stream_usage_option": "omit"}), 200)
		if got.StreamUsageOption != adminapi.StreamUsageOptionOmit {
			t.Fatalf("updated to %q", got.StreamUsageOption)
		}
		if again := decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/"+p.Name, map[string]any{"description": "other"}), 200); again.StreamUsageOption != adminapi.StreamUsageOptionOmit {
			t.Errorf("an update of another member changed the option to %q", again.StreamUsageOption)
		}
		if back := decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/"+p.Name, map[string]any{"stream_usage_option": "add"}), 200); back.StreamUsageOption != adminapi.StreamUsageOptionAdd {
			t.Errorf("back to %q", back.StreamUsageOption)
		}
	})

	t.Run("an unknown value is refused", func(t *testing.T) {
		for name, r := range map[string]func() *apitest.Response{
			"create": func() *apitest.Response {
				return c.platform(t, "POST", "/v1/providers", map[string]any{"name": "synthetic-opt-bad", "api_family": "openai", "stream_usage_option": "sometimes"})
			},
			"update": func() *apitest.Response {
				return c.platform(t, "PATCH", "/v1/providers/synthetic-opt-default", map[string]any{"stream_usage_option": "sometimes"})
			},
		} {
			p := expect(t, r(), 400, adminapi.ProblemCodeInvalidRequest)
			if len(p.Errors) != 1 || p.Errors[0].Field != "stream_usage_option" {
				t.Errorf("%s: errors %+v", name, p.Errors)
			}
		}
	})

	t.Run("omit is refused for a provider of family anthropic", func(t *testing.T) {
		const message = "must be add for a provider of family anthropic"
		r := c.platform(t, "POST", "/v1/providers", map[string]any{"name": "synthetic-opt-claude", "api_family": "anthropic", "stream_usage_option": "omit"})
		p := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		if len(p.Errors) != 1 || p.Errors[0].Field != "stream_usage_option" || p.Errors[0].Message != message {
			t.Errorf("create: errors %+v", p.Errors)
		}
		if r := c.platform(t, "GET", "/v1/providers/synthetic-opt-claude", nil); r.Status != 404 {
			t.Errorf("the refused provider was created: %d", r.Status)
		}
		claude := c.createProvider(t, map[string]any{"name": "synthetic-opt-claude", "api_family": "anthropic"})
		if claude.StreamUsageOption != adminapi.StreamUsageOptionAdd {
			t.Errorf("an anthropic provider has %q", claude.StreamUsageOption)
		}
		p = expect(t, c.platform(t, "PATCH", "/v1/providers/"+claude.Name, map[string]any{"stream_usage_option": "omit"}), 400, adminapi.ProblemCodeInvalidRequest)
		if len(p.Errors) != 1 || p.Errors[0].Field != "stream_usage_option" || p.Errors[0].Message != message {
			t.Errorf("update: errors %+v", p.Errors)
		}
		if got := decode[adminapi.Provider](t, c.platform(t, "GET", "/v1/providers/"+claude.Name, nil), 200); got.StreamUsageOption != adminapi.StreamUsageOptionAdd {
			t.Errorf("after the refusal: %q", got.StreamUsageOption)
		}
		// Saying add for an anthropic provider is fine, at creation and at update.
		c.createProvider(t, map[string]any{"name": "synthetic-opt-claude-2", "api_family": "anthropic", "stream_usage_option": "add"})
		decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/"+claude.Name, map[string]any{"stream_usage_option": "add"}), 200)
	})

	t.Run("the audit detail holds the member", func(t *testing.T) {
		newRows(t, c.api, seen)
		c.createProvider(t, map[string]any{"name": "synthetic-opt-audit", "stream_usage_option": "omit"})
		rows := newRows(t, c.api, seen)
		if len(rows) != 1 || rows[0].Action != "provider.create" || rows[0].Detail["stream_usage_option"] != "omit" {
			t.Fatalf("create: %+v", rows)
		}
		decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/synthetic-opt-audit", map[string]any{"stream_usage_option": "add"}), 200)
		rows = newRows(t, c.api, seen)
		want := map[string]any{"from": "omit", "to": "add"}
		if len(rows) != 1 || rows[0].Action != "provider.update" || !reflect.DeepEqual(rows[0].Detail["stream_usage_option"], want) {
			t.Fatalf("update: %+v", rows)
		}
		// An update that changes nothing writes nothing.
		decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/synthetic-opt-audit", map[string]any{"stream_usage_option": "add"}), 200)
		if rows := newRows(t, c.api, seen); len(rows) != 0 {
			t.Errorf("an unchanged update wrote %+v", rows)
		}
	})
}
