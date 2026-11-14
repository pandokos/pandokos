package admin_test

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestCatalogLifecycle is T-400: providers, endpoints and models, from creation to deletion, with
// every conflict, invalid_state and invalid_request of section 5.8.
func TestCatalogLifecycle(t *testing.T) {
	c := newCat(t)

	t.Run("the seed", func(t *testing.T) {
		ps := decode[adminapi.ProviderList](t, c.platform(t, "GET", "/v1/providers", nil), 200)
		names := map[string]adminapi.Provider{}
		for _, p := range ps.Items {
			names[p.Name] = p
		}
		for name, family := range map[string]adminapi.ApiFamily{"openai": adminapi.ApiFamilyOpenai, "anthropic": adminapi.ApiFamilyAnthropic} {
			p, ok := names[name]
			if !ok || p.ApiFamily != family || !p.Enabled || p.DatasetProvider != name || p.DisplayName == "" || len(p.ID) != 36 {
				t.Errorf("seeded provider %s: %+v", name, p)
			}
		}
		eps := decode[adminapi.EndpointList](t, c.platform(t, "GET", "/v1/providers/openai/endpoints", nil), 200)
		if len(eps.Items) != 1 || eps.Items[0].Name != "default" || eps.Items[0].BaseURL != "https://api.openai.com" || !eps.Items[0].IsDefault || !eps.Items[0].Enabled || eps.Items[0].Private {
			t.Errorf("the endpoint of openai: %+v", eps.Items)
		}
		eps = decode[adminapi.EndpointList](t, c.platform(t, "GET", "/v1/providers/anthropic/endpoints", nil), 200)
		if len(eps.Items) != 1 || eps.Items[0].BaseURL != "https://api.anthropic.com" || !eps.Items[0].IsDefault {
			t.Errorf("the endpoint of anthropic: %+v", eps.Items)
		}
		for provider, want := range map[string]int{"openai": 12, "anthropic": 5} {
			ms := decode[adminapi.ModelList](t, c.platform(t, "GET", "/v1/models?provider="+provider+"&limit=200", nil), 200)
			if len(ms.Items) != want {
				t.Errorf("%s has %d seeded models, want %d", provider, len(ms.Items), want)
			}
			for _, m := range ms.Items {
				if m.Availability != adminapi.ModelAvailabilityAll || m.UpstreamID != m.Name || m.Ref != provider+"/"+m.Name || m.Status != adminapi.ModelStatusActive || m.ContextWindow <= 0 || m.PriceOverride {
					t.Errorf("seeded model %+v", m)
				}
			}
		}
		one := decode[adminapi.ModelList](t, c.platform(t, "GET", "/v1/models?provider=openai&name=gpt-4o", nil), 200)
		if len(one.Items) != 1 || one.Items[0].Name != "gpt-4o" || one.Items[0].ContextWindow != 128000 {
			t.Errorf("gpt-4o: %+v", one.Items)
		}
	})

	t.Run("providers", func(t *testing.T) {
		p := c.createProvider(t, map[string]any{"name": "synthetic-prov", "display_name": "Synthetic, ünï", "description": "d", "dataset_provider": "synthetic"})
		if p.Name != "synthetic-prov" || p.DisplayName != "Synthetic, ünï" || p.Description != "d" || p.ApiFamily != adminapi.ApiFamilyOpenai || !p.Enabled || p.DatasetProvider != "synthetic" || len(p.ID) != 36 || p.CreatedAt.IsZero() {
			t.Errorf("created: %+v", p)
		}
		if got := decode[adminapi.Provider](t, c.platform(t, "GET", "/v1/providers/synthetic-prov", nil), 200); got != p {
			t.Errorf("get: %+v, want %+v", got, p)
		}
		bare := c.createProvider(t, map[string]any{"name": "synthetic-bare", "api_family": "anthropic"})
		if bare.DisplayName != "" || bare.Description != "" || bare.DatasetProvider != "" || bare.ApiFamily != adminapi.ApiFamilyAnthropic || !bare.Enabled {
			t.Errorf("a provider with nothing but a name and a family: %+v", bare)
		}

		// A name that is taken, reserved or malformed; a family that does not exist.
		expect(t, c.platform(t, "POST", "/v1/providers", map[string]any{"name": "synthetic-prov", "api_family": "openai"}), 409, adminapi.ProblemCodeConflict)
		expect(t, c.platform(t, "POST", "/v1/providers", map[string]any{"name": "openai", "api_family": "openai"}), 409, adminapi.ProblemCodeConflict)
		for _, bad := range []string{"healthz", "readyz", "v1"} {
			invalidOn(t, "the reserved name "+bad, c.platform(t, "POST", "/v1/providers", map[string]any{"name": bad, "api_family": "openai"}), "name")
		}
		for _, bad := range []string{"Upper", "1leading", "has_underscore", "has space", "", strings.Repeat("a", 64)} {
			invalidOn(t, "the name "+bad, c.platform(t, "POST", "/v1/providers", map[string]any{"name": bad, "api_family": "openai"}), "name")
		}
		invalidOn(t, "a family nobody implements", c.platform(t, "POST", "/v1/providers", map[string]any{"name": "synthetic-fam", "api_family": "gemini"}), "api_family")
		invalidOn(t, "a missing family", c.platform(t, "POST", "/v1/providers", map[string]any{"name": "synthetic-fam"}), "api_family")
		invalidOn(t, "a bad dataset provider", c.platform(t, "POST", "/v1/providers", map[string]any{"name": "synthetic-fam", "api_family": "openai", "dataset_provider": "Not Valid"}), "dataset_provider")
		invalidOn(t, "an unknown member", c.platform(t, "POST", "/v1/providers", map[string]any{"name": "synthetic-fam", "api_family": "openai", "enabled": false}), "enabled")

		// The list is by name, with pages.
		var all []string
		cursor := ""
		for i := 0; i < 20; i++ {
			q := "?limit=2"
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			page := decode[adminapi.ProviderList](t, c.platform(t, "GET", "/v1/providers"+q, nil), 200)
			if len(page.Items) > 2 {
				t.Fatalf("a page of %d with limit 2", len(page.Items))
			}
			for _, it := range page.Items {
				all = append(all, it.Name)
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if !sort.StringsAreSorted(all) || len(all) < 4 {
			t.Errorf("the providers over pages of two: %v", all)
		}

		// Update: each member, absent is unchanged, empty clears.
		u := decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/synthetic-prov", map[string]any{"display_name": "Renamed"}), 200)
		if u.DisplayName != "Renamed" || u.Description != "d" || !u.Enabled || u.DatasetProvider != "synthetic" || !u.UpdatedAt.After(p.UpdatedAt) && !u.UpdatedAt.Equal(p.UpdatedAt) {
			t.Errorf("display name only: %+v", u)
		}
		u = decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/synthetic-prov", map[string]any{"description": "", "enabled": false, "dataset_provider": ""}), 200)
		if u.Description != "" || u.Enabled || u.DatasetProvider != "" || u.DisplayName != "Renamed" {
			t.Errorf("cleared and disabled: %+v", u)
		}
		u = decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/synthetic-prov", map[string]any{"enabled": true, "description": "again", "dataset_provider": "synthetic-2"}), 200)
		if !u.Enabled || u.Description != "again" || u.DatasetProvider != "synthetic-2" {
			t.Errorf("enabled again: %+v", u)
		}
		for name, body := range map[string]map[string]any{
			"the name":          {"name": "other"},
			"the family":        {"api_family": "anthropic"},
			"null display name": {"display_name": nil},
			"null enabled":      {"enabled": nil},
			"a number":          {"display_name": 5},
			"a bad dataset":     {"dataset_provider": "Bad Value"},
		} {
			r := c.platform(t, "PATCH", "/v1/providers/synthetic-prov", body)
			if r.Status != 400 {
				t.Errorf("update with %s: %d\n%s", name, r.Status, r.Body)
			}
		}
		expect(t, c.platform(t, "PATCH", "/v1/providers/no-such-provider", map[string]any{"description": "x"}), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "GET", "/v1/providers/no-such-provider", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "DELETE", "/v1/providers/no-such-provider", nil), 404, adminapi.ProblemCodeNotFound)
		// Malformed names are not routes.
		expect(t, c.platform(t, "GET", "/v1/providers/Not-A-Name", nil), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("endpoints", func(t *testing.T) {
		p := c.createProvider(t, nil)
		e1 := c.createEndpoint(t, p.Name, map[string]any{"name": "main", "base_url": "https://a.example.com/v1", "region": "eu", "description": "first"})
		if !e1.IsDefault || !e1.Enabled || e1.Private || e1.Region != "eu" || e1.Provider != p.Name || e1.BaseURL != "https://a.example.com/v1" || e1.Description != "first" || len(e1.ID) != 36 {
			t.Errorf("the first endpoint of a provider is its default whatever it asks for: %+v", e1)
		}
		e2 := c.createEndpoint(t, p.Name, map[string]any{"name": "second", "base_url": "http://10.0.0.5:8000/v1", "private": true})
		if e2.IsDefault || !e2.Private || e2.BaseURL != "http://10.0.0.5:8000/v1" {
			t.Errorf("the second: %+v", e2)
		}
		// A trailing slash is stripped, the host and the scheme are lower-cased.
		e3 := c.createEndpoint(t, p.Name, map[string]any{"name": "third", "base_url": "https://API.Example.COM/Path/"})
		if e3.BaseURL != "https://api.example.com/Path" {
			t.Errorf("the URL was stored as %q", e3.BaseURL)
		}
		// The URL rules.
		for name, bad := range map[string]string{
			"http, public":         "http://api.example.com",
			"a private IPv4":       "https://10.1.2.3",
			"a loopback IPv4":      "https://127.0.0.1",
			"a link-local address": "https://169.254.169.254",
			"an IPv6 loopback":     "https://[::1]",
			"an IPv6 unique local": "https://[fd00::1]",
			"user information":     "https://user:synthetic-password@api.example.com",
			"a query":              "https://api.example.com/?a=1",
			"a fragment":           "https://api.example.com/#x",
			"another scheme":       "ftp://api.example.com",
			"no scheme":            "api.example.com",
		} {
			p := invalidOn(t, name, c.platform(t, "POST", "/v1/providers/"+p.Name+"/endpoints", map[string]any{"name": apitest.UniqueName("bad"), "base_url": bad}), "base_url")
			if strings.Contains(string(mustJSON2(p)), "synthetic-password") {
				t.Errorf("%s: the problem quotes the URL", name)
			}
		}
		if pe := invalidOn(t, "a private address", c.platform(t, "POST", "/v1/providers/"+p.Name+"/endpoints", map[string]any{"name": "priv", "base_url": "https://192.168.0.7"}), "base_url"); !strings.Contains(pe.Errors[0].Message, "private: true") {
			t.Errorf("the message does not tell what to do: %+v", pe.Errors)
		}
		e4 := c.createEndpoint(t, p.Name, map[string]any{"name": "lan", "base_url": "https://192.168.0.7", "private": true})
		if !e4.Private {
			t.Errorf("a private address on a private endpoint: %+v", e4)
		}
		invalidOn(t, "a bad region", c.platform(t, "POST", "/v1/providers/"+p.Name+"/endpoints", map[string]any{"name": "reg", "base_url": "https://x.example.com", "region": "Not Valid"}), "region")
		invalidOn(t, "a bad name", c.platform(t, "POST", "/v1/providers/"+p.Name+"/endpoints", map[string]any{"name": "Bad_Name", "base_url": "https://x.example.com"}), "name")
		expect(t, c.platform(t, "POST", "/v1/providers/"+p.Name+"/endpoints", map[string]any{"name": "main", "base_url": "https://x.example.com"}), 409, adminapi.ProblemCodeConflict)
		expect(t, c.platform(t, "POST", "/v1/providers/no-such-provider/endpoints", map[string]any{"name": "x", "base_url": "https://x.example.com"}), 404, adminapi.ProblemCodeNotFound)

		// The default moves, in one transaction, when another endpoint asks.
		e5 := c.createEndpoint(t, p.Name, map[string]any{"name": "fifth", "base_url": "https://e.example.com", "is_default": true})
		if !e5.IsDefault {
			t.Errorf("a new endpoint that asked to be the default: %+v", e5)
		}
		defaults := func() []string {
			t.Helper()
			var out []string
			for _, e := range decode[adminapi.EndpointList](t, c.platform(t, "GET", "/v1/providers/"+p.Name+"/endpoints?limit=100", nil), 200).Items {
				if e.IsDefault {
					out = append(out, e.Name)
				}
			}
			return out
		}
		if got := defaults(); len(got) != 1 || got[0] != "fifth" {
			t.Errorf("the defaults: %v", got)
		}
		u := decode[adminapi.Endpoint](t, c.platform(t, "PATCH", "/v1/providers/"+p.Name+"/endpoints/main", map[string]any{"is_default": true}), 200)
		if !u.IsDefault {
			t.Errorf("main asked to be the default: %+v", u)
		}
		if got := defaults(); len(got) != 1 || got[0] != "main" {
			t.Errorf("the defaults after the move: %v", got)
		}
		invalidOn(t, "is_default false on the default", c.platform(t, "PATCH", "/v1/providers/"+p.Name+"/endpoints/main", map[string]any{"is_default": false}), "is_default")
		// A non-default one can say false.
		if u := decode[adminapi.Endpoint](t, c.platform(t, "PATCH", "/v1/providers/"+p.Name+"/endpoints/second", map[string]any{"is_default": false, "enabled": false}), 200); u.IsDefault || u.Enabled {
			t.Errorf("second: %+v", u)
		}
		// The list is by name; each member updates; empty clears; a bad URL is refused.
		eps := decode[adminapi.EndpointList](t, c.platform(t, "GET", "/v1/providers/"+p.Name+"/endpoints?limit=2", nil), 200)
		if len(eps.Items) != 2 || eps.NextCursor == "" || eps.Items[0].Name > eps.Items[1].Name {
			t.Errorf("the first page of endpoints: %+v", eps)
		}
		u = decode[adminapi.Endpoint](t, c.platform(t, "PATCH", "/v1/providers/"+p.Name+"/endpoints/lan", map[string]any{"base_url": "https://lan2.example.com", "private": false, "region": "", "description": "moved"}), 200)
		if u.BaseURL != "https://lan2.example.com" || u.Private || u.Region != "" || u.Description != "moved" {
			t.Errorf("lan: %+v", u)
		}
		invalidOn(t, "a public endpoint made private-address", c.platform(t, "PATCH", "/v1/providers/"+p.Name+"/endpoints/lan", map[string]any{"base_url": "https://10.9.9.9"}), "base_url")
		invalidOn(t, "plain http on a public endpoint", c.platform(t, "PATCH", "/v1/providers/"+p.Name+"/endpoints/lan", map[string]any{"base_url": "http://lan3.example.com"}), "base_url")
		for name, body := range map[string]map[string]any{"the name": {"name": "other"}, "null": {"base_url": nil}, "null private": {"private": nil}} {
			if r := c.platform(t, "PATCH", "/v1/providers/"+p.Name+"/endpoints/lan", body); r.Status != 400 {
				t.Errorf("update with %s: %d\n%s", name, r.Status, r.Body)
			}
		}
		if got := decode[adminapi.Endpoint](t, c.platform(t, "GET", "/v1/providers/"+p.Name+"/endpoints/lan", nil), 200); got.Name != "lan" || got.BaseURL != "https://lan2.example.com" {
			t.Errorf("get: %+v", got)
		}
		expect(t, c.platform(t, "GET", "/v1/providers/"+p.Name+"/endpoints/none", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "PATCH", "/v1/providers/"+p.Name+"/endpoints/none", map[string]any{"region": "eu"}), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "DELETE", "/v1/providers/"+p.Name+"/endpoints/none", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "GET", "/v1/providers/no-such-provider/endpoints/main", nil), 404, adminapi.ProblemCodeNotFound)

		// Deleting the default is refused while others exist; the others go freely; the last one too.
		expect(t, c.platform(t, "DELETE", "/v1/providers/"+p.Name+"/endpoints/main", nil), 409, adminapi.ProblemCodeInvalidState)
		for _, name := range []string{"second", "third", "lan", "fifth"} {
			if d := decode[adminapi.Endpoint](t, c.platform(t, "DELETE", "/v1/providers/"+p.Name+"/endpoints/"+name, nil), 200); d.Name != name {
				t.Errorf("delete %s: %+v", name, d)
			}
		}
		if d := decode[adminapi.Endpoint](t, c.platform(t, "DELETE", "/v1/providers/"+p.Name+"/endpoints/main", nil), 200); !d.IsDefault {
			t.Errorf("delete of the last: %+v", d)
		}
		expect(t, c.platform(t, "GET", "/v1/providers/"+p.Name+"/endpoints/main", nil), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("models", func(t *testing.T) {
		p := c.createProvider(t, nil)
		m := c.createModel(t, p.Name, map[string]any{"name": "meta/synthetic-llama"})
		if m.Provider != p.Name || m.ApiFamily != adminapi.ApiFamilyOpenai || m.Name != "meta/synthetic-llama" || m.Ref != p.Name+"/meta/synthetic-llama" || m.UpstreamID != "meta/synthetic-llama" ||
			m.Status != adminapi.ModelStatusActive || m.Availability != adminapi.ModelAvailabilitySelected || m.Capabilities == nil || len(m.Capabilities) != 0 ||
			m.ContextWindow != 0 || m.MaxOutputTokens != 0 || m.PriceOverride || len(m.ID) != 36 {
			t.Errorf("defaults: %+v (selected, active, upstream identifier = name, no bounds)", m)
		}
		full := c.createModel(t, p.Name, map[string]any{"name": "synthetic-full", "upstream_id": "up/full", "display_name": "Full, ünï", "description": "d", "status": "deprecated",
			"availability": "all", "capabilities": []string{"text", "tools"}, "context_window": 8192, "max_output_tokens": 1024})
		if full.UpstreamID != "up/full" || full.DisplayName != "Full, ünï" || full.Status != adminapi.ModelStatusDeprecated || full.Availability != adminapi.ModelAvailabilityAll ||
			len(full.Capabilities) != 2 || full.ContextWindow != 8192 || full.MaxOutputTokens != 1024 {
			t.Errorf("full: %+v", full)
		}
		if got := decode[adminapi.Model](t, c.platform(t, "GET", "/v1/models/"+full.ID, nil), 200); got.ID != full.ID || got.Name != full.Name || got.ContextWindow != 8192 {
			t.Errorf("get: %+v", got)
		}
		// Conflicts and refusals.
		expect(t, c.platform(t, "POST", "/v1/models", map[string]any{"provider": p.Name, "name": "synthetic-full"}), 409, adminapi.ProblemCodeConflict)
		expect(t, c.platform(t, "POST", "/v1/models", map[string]any{"provider": p.Name, "name": "synthetic-other", "upstream_id": "up/full"}), 409, adminapi.ProblemCodeConflict)
		invalidOn(t, "an unknown provider", c.platform(t, "POST", "/v1/models", map[string]any{"provider": "no-such-provider", "name": "x"}), "provider")
		for name, body := range map[string]map[string]any{
			"a name with a space":      {"name": "has space"},
			"a leading slash":          {"name": "/x"},
			"a name of 129 characters": {"name": "a" + strings.Repeat("b", 128)},
		} {
			body["provider"] = p.Name
			invalidOn(t, name, c.platform(t, "POST", "/v1/models", body), "name")
		}
		for name, body := range map[string]map[string]any{
			"a capability nobody knows": {"capabilities": []string{"teleport"}},
			"a duplicate capability":    {"capabilities": []string{"text", "text"}},
			"a bad status":              {"status": "gone"},
			"a bad availability":        {"availability": "some"},
			"a negative bound":          {"context_window": -1},
			"a bound over 1e8":          {"max_output_tokens": 100000001},
			"a space in the upstream":   {"upstream_id": "has space"},
		} {
			body["provider"], body["name"] = p.Name, apitest.UniqueName("synthetic-bad")
			if r := c.platform(t, "POST", "/v1/models", body); r.Status != 400 {
				t.Errorf("create with %s: %d\n%s", name, r.Status, r.Body)
			}
		}

		// Lists: filters, sort, pages.
		more := c.createModel(t, p.Name, map[string]any{"name": "synthetic-third", "status": "retired", "availability": "all"})
		list := func(q string) []adminapi.Model {
			t.Helper()
			return decode[adminapi.ModelList](t, c.platform(t, "GET", "/v1/models?"+q, nil), 200).Items
		}
		if got := list("provider=" + p.Name + "&limit=100"); len(got) != 3 {
			t.Errorf("by provider: %d models", len(got))
		}
		if got := list("provider=" + p.Name + "&status=retired"); len(got) != 1 || got[0].ID != more.ID {
			t.Errorf("by status: %+v", got)
		}
		if got := list("provider=" + p.Name + "&availability=all"); len(got) != 2 {
			t.Errorf("by availability: %d models", len(got))
		}
		if got := list("provider=" + p.Name + "&name=synthetic-full"); len(got) != 1 || got[0].ID != full.ID {
			t.Errorf("by name: %+v", got)
		}
		if got := list("provider=" + p.Name + "&name=" + url.QueryEscape("meta/synthetic-llama")); len(got) != 1 || got[0].ID != m.ID {
			t.Errorf("by a name with a slash: %+v", got)
		}
		if got := list("provider=no-such-provider"); len(got) != 0 {
			t.Errorf("by a provider that is not there: %+v", got)
		}
		asc, desc := list("provider="+p.Name+"&sort=created_at"), list("provider="+p.Name+"&sort=-created_at")
		if len(asc) != 3 || len(desc) != 3 || asc[0].ID != desc[2].ID || asc[0].ID != m.ID {
			t.Errorf("sorted: %v / %v", asc, desc)
		}
		page := decode[adminapi.ModelList](t, c.platform(t, "GET", "/v1/models?provider="+p.Name+"&limit=2", nil), 200)
		if len(page.Items) != 2 || page.NextCursor == "" {
			t.Fatalf("the first page: %+v", page)
		}
		rest := decode[adminapi.ModelList](t, c.platform(t, "GET", "/v1/models?provider="+p.Name+"&limit=2&cursor="+url.QueryEscape(page.NextCursor), nil), 200)
		if len(rest.Items) != 1 || rest.NextCursor != "" || rest.Items[0].ID == page.Items[0].ID || rest.Items[0].ID == page.Items[1].ID {
			t.Errorf("the second page: %+v", rest)
		}
		for _, q := range []string{"status=bogus", "availability=bogus", "limit=0", "sort=name", "unknown=1"} {
			if r := c.platform(t, "GET", "/v1/models?"+q, nil); r.Status != 400 {
				t.Errorf("?%s: %d", q, r.Status)
			}
		}

		// Update: each member, an empty value clears, 0 clears a bound, the capabilities are replaced.
		u := decode[adminapi.Model](t, c.platform(t, "PATCH", "/v1/models/"+full.ID, map[string]any{"display_name": "", "description": "new", "status": "retired", "availability": "selected",
			"capabilities": []string{"vision"}, "context_window": 0, "max_output_tokens": 2048, "upstream_id": "up/full2"}), 200)
		if u.DisplayName != "" || u.Description != "new" || u.Status != adminapi.ModelStatusRetired || u.Availability != adminapi.ModelAvailabilitySelected || len(u.Capabilities) != 1 ||
			u.Capabilities[0] != "vision" || u.ContextWindow != 0 || u.MaxOutputTokens != 2048 || u.UpstreamID != "up/full2" || u.Name != "synthetic-full" {
			t.Errorf("updated: %+v", u)
		}
		u = decode[adminapi.Model](t, c.platform(t, "PATCH", "/v1/models/"+full.ID, map[string]any{"capabilities": []string{}}), 200)
		if u.Capabilities == nil || len(u.Capabilities) != 0 || u.Description != "new" {
			t.Errorf("capabilities emptied, nothing else changed: %+v", u)
		}
		for name, body := range map[string]map[string]any{
			"the name": {"name": "other"}, "the provider": {"provider": "openai"}, "null status": {"status": nil}, "null bound": {"context_window": nil}, "null capabilities": {"capabilities": nil},
			"a bad status": {"status": "gone"}, "a bad capability": {"capabilities": []string{"x"}}, "a taken upstream": {"upstream_id": "meta/synthetic-llama"},
		} {
			r := c.platform(t, "PATCH", "/v1/models/"+full.ID, body)
			if name == "a taken upstream" {
				expect(t, r, 409, adminapi.ProblemCodeConflict)
			} else if r.Status != 400 {
				t.Errorf("update with %s: %d\n%s", name, r.Status, r.Body)
			}
		}
		expect(t, c.platform(t, "GET", "/v1/models/0192f0f0-0000-7000-8000-00000000beef", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "PATCH", "/v1/models/0192f0f0-0000-7000-8000-00000000beef", map[string]any{"description": "x"}), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "DELETE", "/v1/models/0192f0f0-0000-7000-8000-00000000beef", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "GET", "/v1/models/not-an-id", nil), 404, adminapi.ProblemCodeNotFound)

		// A priced model is retired, not deleted; an unpriced one goes, and so do its grants.
		if _, err := c.api.Client(c.plat).CreatePriceOverride(ctx(t), adminapi.CreatePriceOverrideInput{Model: m.ID, Body: adminapi.PriceOverride{
			Currency: "USD", Rates: []adminapi.PriceRate{{Category: adminapi.PriceCategoryInput, Rate: "1"}}}}); err != nil {
			t.Fatal(err)
		}
		expect(t, c.platform(t, "DELETE", "/v1/models/"+m.ID, nil), 409, adminapi.ProblemCodeInvalidState)
		c.grant(t, full.ID, c.a.ID)
		if d := decode[adminapi.Model](t, c.platform(t, "DELETE", "/v1/models/"+full.ID, nil), 200); d.ID != full.ID {
			t.Errorf("delete: %+v", d)
		}
		expect(t, c.platform(t, "GET", "/v1/models/"+full.ID, nil), 404, adminapi.ProblemCodeNotFound)
		var grants int
		if err := c.api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.model_tenant_grant WHERE model_id = $1::uuid`, full.ID).Scan(&grants); err != nil || grants != 0 {
			t.Errorf("%d grants of a deleted model: %v", grants, err)
		}
	})

	t.Run("a provider is deleted when nothing references it", func(t *testing.T) {
		withEndpoint := c.createProvider(t, nil)
		c.createEndpoint(t, withEndpoint.Name, nil)
		expect(t, c.platform(t, "DELETE", "/v1/providers/"+withEndpoint.Name, nil), 409, adminapi.ProblemCodeInvalidState)
		withModel := c.createProvider(t, nil)
		c.createModel(t, withModel.Name, nil)
		expect(t, c.platform(t, "DELETE", "/v1/providers/"+withModel.Name, nil), 409, adminapi.ProblemCodeInvalidState)
		withCredential := c.createProvider(t, nil)
		cred := c.api.Credential(t, c.a.ID, c.ka, withCredential.Name, "on-a-new-provider")
		if cred.Provider != withCredential.Name {
			t.Errorf("a credential of a provider created by the platform administrator: %+v", cred)
		}
		expect(t, c.platform(t, "DELETE", "/v1/providers/"+withCredential.Name, nil), 409, adminapi.ProblemCodeInvalidState)
		alone := c.createProvider(t, nil)
		if d := decode[adminapi.Provider](t, c.platform(t, "DELETE", "/v1/providers/"+alone.Name, nil), 200); d.Name != alone.Name {
			t.Errorf("delete: %+v", d)
		}
		expect(t, c.platform(t, "GET", "/v1/providers/"+alone.Name, nil), 404, adminapi.ProblemCodeNotFound)
		// The seed is deletable only when nothing references it: it has endpoints and models.
		expect(t, c.platform(t, "DELETE", "/v1/providers/openai", nil), 409, adminapi.ProblemCodeInvalidState)
	})

	t.Run("a credential names a provider of the catalogue", func(t *testing.T) {
		r := c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/credentials", c.ka, map[string]any{"provider": "no-such-provider", "label": "x", "secret": "synthetic-credential-0001"})
		invalidOn(t, "a provider that is not in the catalogue", r, "provider")
		// A disabled provider still accepts credentials.
		off := c.createProvider(t, nil)
		decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/"+off.Name, map[string]any{"enabled": false}), 200)
		if cred := c.api.Credential(t, c.a.ID, c.ka, off.Name, "on-a-disabled-provider"); cred.Provider != off.Name {
			t.Errorf("%+v", cred)
		}
	})
}

func mustJSON2(p adminapi.Problem) []byte {
	b, _ := json.Marshal(p)
	return b
}

// TestConcurrentDefaultEndpoints: writes that move the default endpoint of a provider serialize
// on the provider row; none of them fails with a constraint error, and one default remains.
func TestConcurrentDefaultEndpoints(t *testing.T) {
	c := newCat(t)
	p := c.createProvider(t, nil)
	first := c.createEndpoint(t, p.Name, nil)
	const n = 8
	var wg sync.WaitGroup
	status := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status[i] = c.platform(t, "POST", "/v1/providers/"+p.Name+"/endpoints", map[string]any{
				"name": "concurrent-" + strconv.Itoa(i), "base_url": "https://synthetic.invalid", "is_default": true}).Status
		}()
	}
	wg.Wait()
	for i, s := range status {
		if s != 201 {
			t.Errorf("create %d: status %d", i, s)
		}
	}
	list := decode[adminapi.EndpointList](t, c.platform(t, "GET", "/v1/providers/"+p.Name+"/endpoints", nil), 200)
	defaults := 0
	for _, e := range list.Items {
		if e.IsDefault {
			defaults++
		}
	}
	if len(list.Items) != n+1 || defaults != 1 {
		t.Errorf("%d endpoints, %d defaults (the first was %s)", len(list.Items), defaults, first.Name)
	}
}
