package declare_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/declare"
)

// Identifiers of the fixture. A document holds none of them but the public key ID.
const (
	tidAcme   = "0192f0f0-1111-7000-8000-0000000000a1"
	tidGlobex = "0192f0f0-1111-7000-8000-0000000000a2"
	saBilling = "0192f0f0-3333-7000-8000-0000000000b1"
	saReports = "0192f0f0-3333-7000-8000-0000000000b2"
	roleID    = "0192f0f0-4444-7000-8000-0000000000c1"
	modelGPT  = "0192f0f0-5555-7000-8000-0000000000d1"
	modelMini = "0192f0f0-5555-7000-8000-0000000000d2"
	mappingID = "0192f0f0-6666-7000-8000-0000000000e1"
	keyID     = "tkeyaaaaaaaaaaaaa"
)

// Canaries: values that live in the fake's state and never in a document.
const (
	canarySecret = "synthetic-canary-credential-31bd"
	canaryMember = "synthetic-canary-member-99ee"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// call is one write the fake received.
type call struct {
	Method, Path string
	Body         string
}

// fakeAPI is an HTTP server over a map of state: the operations of the administration API read
// from it (a list that has no entry is empty, another read is a 404), and every write is recorded
// and answered with its status and an empty object. It is independent of which operations the code
// under test chooses to call.
type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	reads  map[string]any // path -> value; a slice is the items of a list
	writes []call
	// failWrite is the 1-based number of the write that answers 500; 0 for none.
	failWrite int
	// readCount counts the requests of each method.
	readCount int
	created   int
}

type route struct {
	re *regexp.Regexp
	op adminapi.Operation
}

func newFake(t *testing.T, reads map[string]any) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, reads: reads}
	var routes []route
	for _, op := range adminapi.Operations {
		p := regexp.QuoteMeta(op.Path)
		p = regexp.MustCompile(`\\\{[a-z_]+\\\}`).ReplaceAllString(p, `[^/]+`)
		routes = append(routes, route{regexp.MustCompile("^" + p + "$"), op})
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		problem := func(status int, code string) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "urn:pandokos:problem:" + code, "title": code, "status": status, "code": code, "detail": "fake", "request_id": strings.Repeat("0", 32)})
		}
		var matched *adminapi.Operation
		for i := range routes {
			if routes[i].op.Method == r.Method && routes[i].re.MatchString(r.URL.Path) {
				matched = &routes[i].op
				break
			}
		}
		if matched == nil {
			problem(404, "not_found")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodGet {
			f.readCount++
			v, ok := f.reads[r.URL.Path]
			switch {
			case ok:
				if _, isList := listOf(v); isList {
					items, _ := listOf(v)
					v = map[string]any{"items": items}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(v)
			case strings.HasPrefix(matched.ID, "list"):
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"items":[]}`)
			default:
				problem(404, "not_found")
			}
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.writes = append(f.writes, call{Method: r.Method, Path: r.URL.Path, Body: string(bytes.TrimSpace(b))})
		if f.failWrite > 0 && len(f.writes) == f.failWrite {
			problem(500, "internal")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(matched.Status)
		if matched.Status != http.StatusNoContent {
			// A created resource carries the ID the server gave it.
			f.created++
			_, _ = io.WriteString(w, fmt.Sprintf(`{"id":"0192f0f0-ffff-7000-8000-%012d"}`, f.created))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func listOf(v any) ([]any, bool) {
	switch s := v.(type) {
	case []any:
		return s, true
	}
	return nil, false
}

func (f *fakeAPI) client() *adminapi.Client {
	return &adminapi.Client{BaseURL: f.srv.URL, HTTP: f.srv.Client(), Key: func() string { return "pdk_t_fake" }}
}

func (f *fakeAPI) writeCalls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.writes...)
}

func items[T any](in ...T) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

func reverse(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func rates(kv ...string) []adminapi.PriceRate {
	var out []adminapi.PriceRate
	for i := 0; i+2 < len(kv); i += 3 {
		th := 0
		for _, c := range kv[i+1] {
			th = th*10 + int(c-'0')
		}
		out = append(out, adminapi.PriceRate{Category: adminapi.PriceCategory(kv[i]), Threshold: th, Rate: kv[i+2]})
	}
	return out
}

// tenantState is what tenant acme holds: the settings, a restriction, a custom role, two service
// accounts, three rules (one on a subject mapping), a price list of two versions; and what a
// document must never hold: a credential, a member, a virtual key, a person, a usage figure.
func tenantState() map[string]any {
	base := "/v1/tenants/" + tidAcme
	return map[string]any{
		"/v1/tenants": items(
			adminapi.Tenant{ID: tidAcme, Name: "acme", DisplayName: "Acme Inc", State: adminapi.TenantStateActive, CreatedAt: t0, UpdatedAt: t0, StateChangedAt: t0,
				TimeZone: "Europe/Rome", TimeZoneInForce: "Europe/Rome", ActiveCurrency: "EUR", ActiveCurrencyInForce: "EUR"},
			adminapi.Tenant{ID: tidGlobex, Name: "globex", State: adminapi.TenantStateActive, CreatedAt: t0, UpdatedAt: t0, StateChangedAt: t0}),
		base: adminapi.Tenant{ID: tidAcme, Name: "acme", DisplayName: "Acme Inc", State: adminapi.TenantStateActive, CreatedAt: t0, UpdatedAt: t0, StateChangedAt: t0,
			TimeZone: "Europe/Rome", TimeZoneInForce: "Europe/Rome", ActiveCurrency: "EUR", ActiveCurrencyInForce: "EUR"},
		base + "/time-zone":         adminapi.TenantTimeZone{TimeZone: "Europe/Rome", InForce: "Europe/Rome"},
		base + "/active-currency":   adminapi.ActiveCurrency{Currency: "EUR", InForce: "EUR"},
		base + "/metadata-keys":     adminapi.MetadataKeys{Restricted: true, Keys: []string{"project", "team"}},
		base + "/model-restriction": adminapi.ModelRestriction{Restricted: true, Models: []string{"openai/gpt-4o"}},
		base + "/roles": items(adminapi.Role{ID: roleID, TenantID: tidAcme, Name: "billing-readers", Description: "Reads usage and limits for billing",
			Permissions: []string{"limit:read", "usage:read"}, State: adminapi.RoleStateActive, CreatedAt: t0, UpdatedAt: t0}),
		base + "/service-accounts": items(
			adminapi.ServiceAccount{ID: saReports, TenantID: tidAcme, Name: "reports", State: adminapi.ServiceAccountStateActive, Roles: []string{"auditor", "usage-viewer"}, CreatedAt: t0, UpdatedAt: t0},
			adminapi.ServiceAccount{ID: saBilling, TenantID: tidAcme, Name: "billing", Description: "Billing exports", State: adminapi.ServiceAccountStateActive, Roles: []string{"usage-viewer"}, CreatedAt: t0, UpdatedAt: t0}),
		base + "/limits": items(
			adminapi.LimitRule{ID: "0192f0f0-7777-7000-8000-0000000000f1", SubjectKind: adminapi.LimitSubjectKindTenant, Metric: adminapi.LimitMetricRequestsPerDay, Limit: 1000, Enforce: true, Thresholds: []int{80, 100}},
			adminapi.LimitRule{ID: "0192f0f0-7777-7000-8000-0000000000f2", SubjectKind: adminapi.LimitSubjectKind("service_account"), SubjectID: saBilling, Metric: adminapi.LimitMetricTokensPerDay, Limit: 50000, Thresholds: []int{}},
			adminapi.LimitRule{ID: "0192f0f0-7777-7000-8000-0000000000f3", SubjectKind: adminapi.LimitSubjectKindKey, SubjectID: keyID, Provider: "openai", Model: "gpt-4o", Metric: adminapi.LimitMetricRequestsPerMinute, Limit: 30, Enforce: true, Thresholds: []int{}},
			adminapi.LimitRule{ID: "0192f0f0-7777-7000-8000-0000000000f4", SubjectKind: adminapi.LimitSubjectKindKey, SubjectID: mappingID, Metric: adminapi.LimitMetricRequestsPerDay, Limit: 5, Enforce: true, Thresholds: []int{}}),
		base + "/prices": items(
			adminapi.TenantPriceVersion{ID: "0192f0f0-8888-7000-8000-0000000000a1", ModelID: modelGPT, Provider: "openai", Model: "gpt-4o", Kind: "price", ValidFrom: at("2026-06-01T00:00:00Z"), Currency: "EUR", Rates: rates("input", "0", "2.80", "output", "0", "11.00"), CreatedAt: t0},
			adminapi.TenantPriceVersion{ID: "0192f0f0-8888-7000-8000-0000000000a2", ModelID: modelGPT, Provider: "openai", Model: "gpt-4o", Kind: "price", ValidFrom: at("2026-01-01T00:00:00Z"), Currency: "EUR", Rates: rates("input", "0", "2.50", "output", "0", "10.00"), Note: "list price 2026", CreatedAt: t0}),
		// What a document must never hold.
		base + "/credentials":      items(adminapi.Credential{ID: "0192f0f0-9999-7000-8000-0000000000a1", TenantID: tidAcme, Provider: "openai", Label: canarySecret, Fingerprint: canarySecret, Hint: canarySecret}),
		base + "/members":          items(map[string]any{"id": "0192f0f0-9999-7000-8000-0000000000a2", "username": canaryMember, "email": canaryMember + "@example.test"}),
		base + "/virtual-keys":     items(adminapi.VirtualKey{ID: keyID, TenantID: tidAcme, ServiceAccountID: saBilling, Description: canarySecret, State: adminapi.KeyStateActive, CreatedAt: t0}),
		base + "/usage-records":    items(map[string]any{"id": "0192f0f0-9999-7000-8000-0000000000a3", "input_tokens": 424242}),
		"/v1/users":                items(map[string]any{"id": "0192f0f0-9999-7000-8000-0000000000a4", "username": canaryMember}),
		base + "/subject-mappings": items(map[string]any{"id": mappingID, "subject": canarySecret}),
	}
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// platformState is the catalogue of the platform: a provider with an endpoint, two models, a grant, a
// price override beside an imported price, and two ceilings.
func platformState() map[string]any {
	return map[string]any{
		"/v1/tenants": items(
			adminapi.Tenant{ID: tidAcme, Name: "acme", State: adminapi.TenantStateActive, CreatedAt: t0, UpdatedAt: t0, StateChangedAt: t0},
			adminapi.Tenant{ID: tidGlobex, Name: "globex", State: adminapi.TenantStateActive, CreatedAt: t0, UpdatedAt: t0, StateChangedAt: t0}),
		"/v1/providers": items(adminapi.Provider{ID: "0192f0f0-aaaa-7000-8000-0000000000a1", Name: "openai", DisplayName: "OpenAI", ApiFamily: adminapi.ApiFamilyOpenai, Enabled: true,
			DatasetProvider: "openai", StreamUsageOption: adminapi.StreamUsageOptionAdd, CreatedAt: t0, UpdatedAt: t0}),
		"/v1/providers/openai/endpoints": items(adminapi.Endpoint{ID: "0192f0f0-aaaa-7000-8000-0000000000b1", Provider: "openai", Name: "default", BaseURL: "https://api.openai.com", IsDefault: true, Enabled: true, CreatedAt: t0, UpdatedAt: t0}),
		"/v1/models": items(
			adminapi.Model{ID: modelMini, Provider: "openai", ApiFamily: adminapi.ApiFamilyOpenai, Name: "gpt-4o-mini", Ref: "openai/gpt-4o-mini", UpstreamID: "gpt-4o-mini", Status: adminapi.ModelStatusActive,
				Availability: adminapi.ModelAvailabilityAll, Capabilities: []string{"chat"}, ContextWindow: 128000, MaxOutputTokens: 16384, CreatedAt: t0, UpdatedAt: t0},
			adminapi.Model{ID: modelGPT, Provider: "openai", ApiFamily: adminapi.ApiFamilyOpenai, Name: "gpt-4o", Ref: "openai/gpt-4o", UpstreamID: "gpt-4o-2024-11-20", DisplayName: "GPT-4o", Status: adminapi.ModelStatusActive,
				Availability: adminapi.ModelAvailabilitySelected, Capabilities: []string{"chat", "vision"}, ContextWindow: 128000, MaxOutputTokens: 16384, PriceOverride: true, CreatedAt: t0, UpdatedAt: t0}),
		"/v1/models/" + modelGPT + "/grants": items(adminapi.ModelGrant{ModelID: modelGPT, TenantID: tidAcme, TenantName: "acme", CreatedAt: t0}),
		"/v1/models/" + modelGPT + "/prices": items(
			adminapi.PriceVersion{ID: "0192f0f0-bbbb-7000-8000-0000000000a1", ModelID: modelGPT, ValidFrom: at("2026-03-01T00:00:00Z"), Currency: "USD", Source: adminapi.PriceSourceOverride, Note: "negotiated", Rates: rates("input", "0", "2.00", "output", "0", "8.00"), CreatedAt: t0},
			adminapi.PriceVersion{ID: "0192f0f0-bbbb-7000-8000-0000000000a2", ModelID: modelGPT, ValidFrom: at("2026-02-01T00:00:00Z"), Currency: "USD", Source: adminapi.PriceSourceImport, Rates: rates("input", "0", "2.50", "output", "0", "10.00"), CreatedAt: t0}),
		"/v1/platform-limits": items(
			adminapi.PlatformLimitRule{ID: "0192f0f0-cccc-7000-8000-0000000000a1", Tenant: tidAcme, Provider: "openai", Metric: adminapi.LimitMetricTokensPerDay, Limit: 5000000, Enforce: true, Thresholds: []int{}},
			adminapi.PlatformLimitRule{ID: "0192f0f0-cccc-7000-8000-0000000000a2", Metric: adminapi.LimitMetricRequestsPerMinute, Limit: 600, Enforce: true, Thresholds: []int{}}),
	}
}

var _ = declare.Version
