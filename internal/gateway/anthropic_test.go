package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/familycodes"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	anthropicRoute   = "/anthropic/v1/messages"
	countRoute       = "/anthropic/v1/messages/count_tokens"
	anthropicVersion = "2026-10-21"
)

// aliasModel is a public model name whose upstream identifier is "fake-basic".
const aliasModel = "synthetic-alias"

// anthropicModels are the fake's models, each given a target of the providers "anthropic", "openai"
// and, in the compat tests, "compat".
var anthropicModels = append(append([]string(nil), fakeModels...),
	fakeprovider.ModelCache, fakeprovider.ModelCutAfterStart, fakeprovider.ModelStallAfterFirst, fakeprovider.ModelError529)

// messages is a Messages API request body.
func messages(model string, stream bool) []byte {
	s := `{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"` + fixtures.CanaryPrompt + `"}]`
	if stream {
		s += `,"stream":true`
	}
	return []byte(s + "}")
}

// anthropicHdr authenticates with X-Api-Key, as curl and older SDKs do.
func anthropicHdr() http.Header {
	return http.Header{"X-Api-Key": {testKey}, "Anthropic-Version": {anthropicVersion}, "Content-Type": {"application/json"}}
}

// bearerHdr authenticates with Authorization: Bearer, as the SDKs of this date do.
func bearerHdr() http.Header {
	return http.Header{"Authorization": {"Bearer " + testKey}, "Anthropic-Version": {anthropicVersion}, "Content-Type": {"application/json"}}
}

// newFamilies builds the gateway over the fake provider with both families: the providers
// "anthropic" (family anthropic), "openai" and "compat" (family openai), each with a target for
// every model of the fake, and the canary provider key as the secret of each.
func newFamilies(t *testing.T, mutate func(*gateway.Options)) *harness {
	t.Helper()
	return newFamiliesWith(t, fakeprovider.New(t), testutil.TestGuard(t), mutate)
}

func newFamiliesWith(t *testing.T, up *fakeprovider.Server, guard *egress.Guard, mutate func(*gateway.Options)) *harness {
	t.Helper()
	targets := map[string]gateway.Target{}
	for _, m := range anthropicModels {
		targets["anthropic/"+m] = testutil.TargetForFamily(up, "anthropic", "anthropic", m)
		targets["openai/"+m] = testutil.TargetFor(up, "openai", m)
	}
	alias := testutil.TargetForFamily(up, "anthropic", "anthropic", fakeprovider.ModelBasic)
	alias.Model = aliasModel
	targets["anthropic/"+aliasModel] = alias
	cat := testutil.StaticCatalogue(map[string]string{"openai": "openai", "anthropic": "anthropic", "compat": "openai"}, targets)
	creds := testutil.StaticCredentials(map[string]string{
		"openai": fixtures.CanaryProviderKey, "anthropic": fixtures.CanaryProviderKey, "compat": fixtures.CanaryProviderKey,
	})
	h := build(t, targets, guard, func(o *gateway.Options) {
		o.Families = gateway.Families()
		o.Catalogue = cat
		o.Credentials = creds
		if mutate != nil {
			mutate(o)
		}
	})
	h.up, h.cat, h.creds = up, cat, creds
	return h
}

func (h *harness) sendA(t *testing.T, path string, body []byte, hdr http.Header) captured {
	t.Helper()
	return h.send(t, http.MethodPost, path, body, hdr)
}

// anthropicErrorBody is the body of a Pandokos error in the shape of the anthropic family.
func anthropicErrorBody(code family.ErrorCode) string {
	typ := map[family.ErrorCode]string{
		family.CodeInvalidRequest: "invalid_request_error", family.CodeNoCredential: "invalid_request_error",
		family.CodeUnauthenticated: "authentication_error",
		family.CodeForbidden:       "permission_error", family.CodeTenantSuspended: "permission_error", family.CodeModelNotAllowed: "permission_error",
		family.CodeEndpointNotSupported: "not_found_error",
		family.CodeRequestTimeout:       "timeout_error", family.CodeUpstreamTimeout: "timeout_error",
		family.CodeRequestTooLarge: "request_too_large", family.CodeRateLimited: "rate_limit_error",
		family.CodeQuotaExceeded: "rate_limit_error", family.CodeNoPrice: "invalid_request_error",
		family.CodeInternal: "api_error", family.CodeUpstreamUnreachable: "api_error", family.CodeUnavailable: "api_error",
	}[code]
	msg := strings.ReplaceAll(code.Message(), `"`, `\"`)
	return `{"type":"error","error":{"type":"` + typ + `","message":"` + msg + `"}}`
}

// assertAnthropicContract checks a Pandokos error on an anthropic route: status, both headers and
// the body in the Anthropic shape.
func assertAnthropicContract(t *testing.T, name string, c captured, code family.ErrorCode) {
	t.Helper()
	if c.status != code.Status() {
		t.Errorf("%s: status %d, want %d", name, c.status, code.Status())
	}
	if got := c.header.Get("X-Pandokos-Error"); got != string(code) {
		t.Errorf("%s: X-Pandokos-Error %q, want %q", name, got, code)
	}
	if id := c.header.Get("X-Pandokos-Request-Id"); !reRequestID.MatchString(id) {
		t.Errorf("%s: X-Pandokos-Request-Id %q", name, id)
	}
	if ct := c.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: Content-Type %q", name, ct)
	}
	if string(c.body) != anthropicErrorBody(code) {
		t.Errorf("%s: body %s\nwant %s", name, c.body, anthropicErrorBody(code))
	}
}

// TestAnthropicPassthrough is T-530: plain and streamed calls reach the provider with the
// allow-listed headers and the tenant's secret in X-Api-Key, the body unchanged but for the model,
// and come back with the filtered response headers.
func TestAnthropicPassthrough(t *testing.T) {
	adapter := anthropic.Adapter()
	allowed := map[string][]string{}
	for _, n := range adapter.RequestHeaders() {
		allowed[http.CanonicalHeaderKey(n)] = []string{"synthetic-" + strings.ToLower(n) + "-1"}
	}
	allowed["Content-Type"] = []string{"application/json"}
	allowed["Anthropic-Version"] = []string{anthropicVersion}
	for _, n := range []string{"Accept", "Content-Type", "User-Agent", "Anthropic-Version", "Anthropic-Beta"} {
		if _, ok := allowed[n]; !ok {
			t.Fatalf("the adapter does not list %s", n)
		}
	}
	dropped := map[string]string{
		"Cookie": "session=synthetic", "X-Forwarded-For": "1.2.3.4", "Accept-Encoding": "gzip", "X-Stainless-Lang": "js",
		"X-Stainless-Retry-Count": "0", "Anthropic-Dangerous-Direct-Browser-Access": "true", "Traceparent": callerTP,
		"X-Pandokos-Metadata-Team": "alpha", "Idempotency-Key": "synthetic-idem", "Openai-Organization": "synthetic-org",
		"Proxy-Authorization": "Basic synthetic", "X-Custom": "v",
	}
	for _, cred := range []struct {
		name  string
		first func(http.Header)
	}{
		{"x-api-key", func(h http.Header) { h.Set("X-Api-Key", testKey) }},
		{"bearer", func(h http.Header) { h.Set("Authorization", "Bearer "+testKey) }},
		{"both, the same value", func(h http.Header) { h.Set("X-Api-Key", testKey); h.Set("Authorization", "Bearer "+testKey) }},
	} {
		for _, stream := range []bool{false, true} {
			name := cred.name + " plain"
			if stream {
				name = cred.name + " stream"
			}
			t.Run(name, func(t *testing.T) {
				h := newFamilies(t, nil)
				hdr := http.Header{}
				for n, v := range allowed {
					hdr[n] = v
				}
				for n, v := range dropped {
					hdr.Set(n, v)
				}
				cred.first(hdr)
				body := messages(fakeprovider.ModelBasic, stream)
				c := h.sendA(t, anthropicRoute, body, hdr)
				want := fixtures.ReadAnthropic("message.json")
				if stream {
					want = fixtures.ReadAnthropic("message_stream.sse")
				}
				if c.status != 200 || !bytes.Equal(c.body, want) {
					t.Fatalf("status %d, body differs from the provider's: %.200s", c.status, c.body)
				}
				reqs := h.up.Requests()
				if len(reqs) != 1 {
					t.Fatalf("%d provider requests", len(reqs))
				}
				got := reqs[0]
				if got.Method != "POST" || got.Path != "/v1/messages" || got.RawQuery != "" {
					t.Errorf("the provider saw %s %s?%s", got.Method, got.Path, got.RawQuery)
				}
				if !bytes.Equal(got.Body, body) {
					t.Errorf("the body changed: %q", got.Body)
				}
				for n, v := range allowed {
					if n == "User-Agent" {
						if g := got.Header.Values(n); len(g) != 1 || g[0] != v[0] {
							t.Errorf("User-Agent %v", g)
						}
						continue
					}
					if g := got.Header.Values(n); strings.Join(g, "|") != strings.Join(v, "|") {
						t.Errorf("%s: %v, want %v", n, g, v)
					}
				}
				if v := got.Header.Values("X-Api-Key"); len(v) != 1 || v[0] != fixtures.CanaryProviderKey {
					t.Errorf("X-Api-Key %v, want the tenant's secret", v)
				}
				if v := got.Header.Values("Authorization"); len(v) != 0 {
					t.Errorf("Authorization reached the provider: %v", v)
				}
				for n := range dropped {
					if n != "Accept-Encoding" || got.Header.Get(n) != "" {
						if v := got.Header.Values(n); len(v) != 0 {
							t.Errorf("%s reached the provider: %v", n, v)
						}
					}
				}
				own := map[string]bool{"X-Api-Key": true, "Content-Length": true}
				var extra []string
				for n := range got.Header {
					if _, ok := allowed[n]; !ok && !own[n] {
						extra = append(extra, n)
					}
				}
				sort.Strings(extra)
				if len(extra) != 0 {
					t.Errorf("headers the provider received that no list explains: %v", extra)
				}
				for k, v := range got.Header {
					if strings.Contains(strings.Join(v, ","), testKey) {
						t.Errorf("the virtual key reached the provider in %s", k)
					}
				}
				for k := range got.Header {
					if strings.HasPrefix(strings.ToLower(k), "x-stainless") || strings.HasPrefix(strings.ToLower(k), "x-pandokos") {
						t.Errorf("%s reached the provider", k)
					}
				}

				// The response: filtered headers.
				for _, n := range []string{"Set-Cookie", "Server", "Via", "X-Pandokos-Error"} {
					if v := c.header.Values(n); len(v) != 0 {
						t.Errorf("%s reached the caller: %v", n, v)
					}
				}
				if !strings.HasPrefix(c.header.Get("Request-Id"), "req_synthetic-") || c.header.Get("Anthropic-Ratelimit-Requests-Remaining") != "99" {
					t.Errorf("allowed response headers missing: %v", c.header)
				}
				if c.header.Get("X-Pandokos-Request-Id") == "" {
					t.Error("no X-Pandokos-Request-Id")
				}
				line := h.lastRequestLine(t)
				if line["provider"] != "anthropic" {
					t.Errorf("request line %v", line)
				}
				if line["usage_injected"] != false {
					t.Errorf("usage_injected %v", line["usage_injected"])
				}
				secrets := testutil.SecretForms(fixtures.CanaryProviderKey)
				testutil.AssertAbsent(t, "the response", c.dump(), secrets...)
				testutil.AssertAbsent(t, "the logs", h.logs.String(), append(secrets, testKey)...)
				testutil.AssertNoCanary(t, "the logs", h.logs.String())
			})
		}
	}

	t.Run("the model alias is the only change", func(t *testing.T) {
		for _, stream := range []bool{false, true} {
			h := newFamilies(t, nil)
			body := messages(aliasModel, stream)
			c := h.sendA(t, anthropicRoute, body, anthropicHdr())
			if c.status != 200 {
				t.Fatalf("status %d %s", c.status, c.body)
			}
			want := strings.Replace(string(body), `"model":"`+aliasModel+`"`, `"model":"fake-basic"`, 1)
			if reqs := h.up.Requests(); len(reqs) != 1 || string(reqs[0].Body) != want {
				t.Fatalf("the provider received %q, want %q", reqs[0].Body, want)
			}
			if r := h.oneRecord(t); r.Model != aliasModel {
				t.Errorf("the record names the model %v", r.Model)
			}
		}
	})

	t.Run("a body with odd white space and members is forwarded byte for byte", func(t *testing.T) {
		h := newFamilies(t, nil)
		body := []byte("{ \"max_tokens\" : 1 ,\n \"model\" :  \"fake-basic\" , \"stream\" : false, \"x\":1.0e3, \"metadata\":{\"user_id\":\"synthetic\"} }\n")
		if c := h.sendA(t, anthropicRoute, body, anthropicHdr()); c.status != 200 {
			t.Fatalf("status %d", c.status)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || !bytes.Equal(reqs[0].Body, body) {
			t.Fatalf("the provider received %q", reqs[0].Body)
		}
	})

	t.Run("a request without Anthropic-Version is not given one", func(t *testing.T) {
		h := newFamilies(t, nil)
		hdr := anthropicHdr()
		hdr.Del("Anthropic-Version")
		c := h.sendA(t, anthropicRoute, messages("fake-basic", false), hdr)
		if c.status != 400 || c.header.Get("X-Pandokos-Error") != "" || !strings.Contains(string(c.body), "anthropic-version") {
			t.Errorf("the provider's own 400 was expected: %d %s %v", c.status, c.body, c.header)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || len(reqs[0].Header.Values("Anthropic-Version")) != 0 {
			t.Errorf("the provider saw %+v", reqs)
		}
	})

	t.Run("a provider that echoes the key in its headers", func(t *testing.T) {
		h := newFamilies(t, nil)
		c := h.sendA(t, anthropicRoute, messages(fakeprovider.ModelEchoAuth, false), anthropicHdr())
		if c.status != 200 {
			t.Fatalf("status %d", c.status)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || reqs[0].Header.Get("X-Api-Key") != fixtures.CanaryProviderKey {
			t.Fatalf("the provider did not receive the credential: %+v", reqs)
		}
		for _, n := range []string{"X-Echo-Api-Key", "Www-Authenticate"} {
			if v := c.header.Values(n); len(v) != 0 {
				t.Errorf("%s reached the caller: %v", n, v)
			}
		}
		secrets := testutil.SecretForms(fixtures.CanaryProviderKey)
		testutil.AssertAbsent(t, "the response", c.dump(), secrets...)
		testutil.AssertAbsent(t, "the logs", h.logs.String(), secrets...)
	})

	t.Run("count_tokens reaches its own path and the answer is unchanged", func(t *testing.T) {
		h := newFamilies(t, nil)
		body := messages(fakeprovider.ModelBasic, true) // counting never streams
		c := h.sendA(t, countRoute, body, bearerHdr())
		if c.status != 200 || !bytes.Equal(c.body, fixtures.ReadAnthropic("count_tokens.json")) {
			t.Fatalf("%d %s", c.status, c.body)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || reqs[0].Path != "/v1/messages/count_tokens" || !bytes.Equal(reqs[0].Body, body) {
			t.Errorf("the provider saw %+v", reqs)
		}
	})
}

// anthropicScenarios produces each Pandokos error on an anthropic route by its condition.
func anthropicScenarios() []scenario {
	active := testutil.InferencePrincipal()
	with := func(mutate func(*authz.Principal)) fixedAuth {
		p := active
		mutate(&p)
		return fixedAuth{p: p}
	}
	basic := func(t *testing.T, mutate func(*gateway.Options)) *harness { return newFamilies(t, mutate) }
	return []scenario{
		{family.CodeUnauthenticated, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), http.Header{"Anthropic-Version": {anthropicVersion}}), h
		}},
		{family.CodeTenantSuspended, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) {
				o.Authenticator = with(func(p *authz.Principal) { p.TenantState = "suspended" })
			})
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()), h
		}},
		{family.CodeForbidden, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.Authenticator = with(func(p *authz.Principal) { p.Roles = nil }) })
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()), h
		}},
		{family.CodeModelNotAllowed, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			return h.sendA(t, anthropicRoute, messages("nope", false), anthropicHdr()), h
		}},
		{family.CodeEndpointNotSupported, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			return h.send(t, http.MethodGet, anthropicRoute, nil, anthropicHdr()), h
		}},
		{family.CodeNoCredential, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.Credentials = testutil.StaticCredentials(nil) })
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()), h
		}},
		{family.CodeInvalidRequest, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			return h.sendA(t, anthropicRoute, []byte(`{}`), anthropicHdr()), h
		}},
		{family.CodeRequestTimeout, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			req := httptest.NewRequest(http.MethodPost, anthropicRoute, failingBody{os.ErrDeadlineExceeded})
			req.ContentLength = 50
			req.Header = anthropicHdr()
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			res := rec.Result()
			b, _ := io.ReadAll(res.Body)
			return captured{status: res.StatusCode, line: res.Status, header: res.Header, body: b}, h
		}},
		{family.CodeRequestTooLarge, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.MaxRequestBodyBytes = 100 })
			return h.sendA(t, anthropicRoute, bytes.Repeat([]byte("a"), 200), anthropicHdr()), h
		}},
		{family.CodeUpstreamUnreachable, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			closed, _ := url.Parse("http://127.0.0.1:1")
			tgt := h.targets["anthropic/fake-basic"]
			tgt.BaseURL = closed
			h.targets["anthropic/fake-basic"] = tgt
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()), h
		}},
		{family.CodeUpstreamTimeout, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.MaxRequestDuration = 200 * time.Millisecond })
			return h.sendA(t, anthropicRoute, messages(fakeprovider.ModelHang, false), anthropicHdr()), h
		}},
		{family.CodeUnavailable, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) {
				o.Authenticator = fixedAuth{err: fmt.Errorf("%w: 08006", authz.ErrUnavailable)}
			})
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()), h
		}},
		{family.CodeInternal, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.Private = panicTransport{} })
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()), h
		}},
		{family.CodeRateLimited, func(t *testing.T) (captured, *harness) {
			lim := testutil.NewStaticLimits().Refuse(refusalOf("tokens_per_minute"))
			h := basic(t, func(o *gateway.Options) { o.Limits = lim })
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()), h
		}},
		{family.CodeQuotaExceeded, func(t *testing.T) (captured, *harness) {
			lim := testutil.NewStaticLimits().Refuse(quotaRefusal("tokens_per_month"))
			h := basic(t, func(o *gateway.Options) { o.Limits = lim })
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()), h
		}},
		{family.CodeNoPrice, func(t *testing.T) (captured, *harness) {
			lim := testutil.NewStaticLimits().Refuse(gateway.LimitRefusal{Code: "no_price"})
			h := basic(t, func(o *gateway.Options) { o.Limits = lim })
			return h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()), h
		}},
	}
}

// TestAnthropicErrors is T-531: each Pandokos refusal on an anthropic route has the Anthropic
// shape; the sweep fails on a code without a scenario; provider errors pass unchanged.
func TestAnthropicErrors(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range anthropicScenarios() {
		seen[string(s.code)] = true
		t.Run(string(s.code), func(t *testing.T) {
			c, h := s.run(t)
			assertAnthropicContract(t, string(s.code), c, s.code)
			line := h.awaitOutcome(t, "pandokos_error")
			if line["error_code"] != string(s.code) {
				t.Errorf("the request line: %v", line)
			}
		})
	}
	for _, name := range familycodes.All(t) {
		if !seen[name] {
			t.Errorf("the code %q is declared in family.go and has no anthropic scenario", name)
		}
	}

	t.Run("401 with two different keys", func(t *testing.T) {
		h := newFamilies(t, nil)
		hdr := anthropicHdr()
		hdr.Set("Authorization", "Bearer "+testKey+"-other")
		c := h.sendA(t, anthropicRoute, messages("fake-basic", false), hdr)
		assertAnthropicContract(t, "two keys", c, family.CodeUnauthenticated)
		if len(h.up.Requests()) != 0 {
			t.Error("the provider was contacted")
		}
	})
	t.Run("401 for a wrong key, in either header, and for no key at all", func(t *testing.T) {
		h := newFamilies(t, nil)
		var first string
		for name, hdr := range map[string]http.Header{
			"wrong x-api-key":  {"X-Api-Key": {"wrong"}, "Anthropic-Version": {anthropicVersion}},
			"wrong bearer":     {"Authorization": {"Bearer wrong"}, "Anthropic-Version": {anthropicVersion}},
			"basic":            {"Authorization": {"Basic " + testKey}, "Anthropic-Version": {anthropicVersion}},
			"the provider key": {"X-Api-Key": {fixtures.CanaryProviderKey}, "Anthropic-Version": {anthropicVersion}},
			"none":             {"Anthropic-Version": {anthropicVersion}},
		} {
			c := h.sendA(t, anthropicRoute, messages("fake-basic", false), hdr)
			assertAnthropicContract(t, name, c, family.CodeUnauthenticated)
			if first == "" {
				first = c.normalized()
			} else if c.normalized() != first {
				t.Errorf("%s: the answer differs from the others", name)
			}
		}
	})
	t.Run("404 for a path that is not served", func(t *testing.T) {
		h := newFamilies(t, nil)
		for _, p := range []string{"/anthropic/v1/messages/batches", "/anthropic/v1/models", "/anthropic/v1/complete", "/anthropic/v1/messages/"} {
			assertAnthropicContract(t, p, h.sendA(t, p, messages("fake-basic", false), anthropicHdr()), family.CodeEndpointNotSupported)
		}
		assertAnthropicContract(t, "GET", h.send(t, http.MethodGet, anthropicRoute, nil, anthropicHdr()), family.CodeEndpointNotSupported)
		assertAnthropicContract(t, "an openai path on the anthropic provider", h.sendA(t, "/anthropic/v1/chat/completions", messages("fake-basic", false), anthropicHdr()), family.CodeEndpointNotSupported)
		if len(h.up.Requests()) != 0 {
			t.Error("the provider was contacted")
		}
	})
	t.Run("502 for a redirect", func(t *testing.T) {
		var other atomic.Int64
		second := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { other.Add(1) }))
		t.Cleanup(second.Close)
		h := newFamilies(t, nil)
		h.up.RedirectTo(second.URL + "/elsewhere")
		c := h.sendA(t, anthropicRoute, messages(fakeprovider.ModelRedirect, false), anthropicHdr())
		assertAnthropicContract(t, "redirect", c, family.CodeUpstreamUnreachable)
		if v := c.header.Values("Location"); len(v) != 0 {
			t.Errorf("Location reached the caller: %v", v)
		}
		testutil.AssertAbsent(t, "the response", c.dump(), second.URL, "/elsewhere")
		line := h.awaitOutcome(t, "pandokos_error")
		if line["upstream_status"] != float64(307) || line["upstream_error"] != "redirect" {
			t.Errorf("line %v", line)
		}
		if other.Load() != 0 {
			t.Error("the redirect was followed")
		}
	})
	t.Run("502 for the guard", func(t *testing.T) {
		up := fakeprovider.NewTLS(t)
		h := newFamiliesWith(t, up, testutil.TestGuard(t, trusting(up)), nil)
		tgt := h.targets["anthropic/fake-basic"]
		u := *tgt.BaseURL
		u.Host = "localhost:" + u.Port()
		tgt.BaseURL, tgt.Private = &u, false
		h.targets["anthropic/fake-basic"] = tgt
		c := h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr())
		assertAnthropicContract(t, "guard", c, family.CodeUpstreamUnreachable)
		if up.Connections() != 0 {
			t.Errorf("%d connections", up.Connections())
		}
		line := h.awaitOutcome(t, "pandokos_error")
		if line["egress_reason"] != "private_address" {
			t.Errorf("line %v", line)
		}
	})
	t.Run("every error of the contract keeps credentials and addresses out", func(t *testing.T) {
		secrets := testutil.SecretForms(fixtures.CanaryProviderKey)
		for _, s := range anthropicScenarios() {
			c, h := s.run(t)
			leaks := append(append([]string{}, secrets...), "127.0.0.1", "localhost")
			testutil.AssertAbsent(t, string(s.code)+": the response", c.dump(), leaks...)
			testutil.AssertAbsent(t, string(s.code)+": the logs", h.logs.String(), append(secrets, testKey)...)
		}
	})

	t.Run("provider errors pass unchanged, never with X-Pandokos-Error", func(t *testing.T) {
		h := newFamilies(t, nil)
		for _, c := range []struct {
			model  string
			status int
			body   []byte
		}{
			{fakeprovider.ModelError429, 429, fixtures.ReadAnthropic("error_429.json")},
			{fakeprovider.ModelError529, 529, fixtures.ReadAnthropic("error_529.json")},
		} {
			got := h.sendA(t, anthropicRoute, messages(c.model, false), anthropicHdr())
			if got.status != c.status || !bytes.Equal(got.body, c.body) || got.header.Get("X-Pandokos-Error") != "" {
				t.Errorf("%s: %d %s %v", c.model, got.status, got.body, got.header)
			}
		}
		got := h.sendA(t, anthropicRoute, messages(fakeprovider.ModelError429, false), anthropicHdr())
		if got.header.Get("Retry-After") != "7" || got.header.Get("X-Should-Retry") != "true" || !strings.HasPrefix(got.header.Get("Request-Id"), "req_synthetic-") {
			t.Errorf("the allowed headers of a provider error: %v", got.header)
		}
		got = h.sendA(t, anthropicRoute, messages(fakeprovider.ModelError500Echo, false), anthropicHdr())
		if got.status != 500 || got.header.Get("X-Pandokos-Error") != "" || !strings.Contains(string(got.body), "upstream failure echo") {
			t.Errorf("500: %d %s", got.status, got.body)
		}
	})
	t.Run("a provider 401 (a secret it does not accept) passes unchanged", func(t *testing.T) {
		h := newFamilies(t, nil)
		h.up.Accept("synthetic-another-key")
		c := h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr())
		if c.status != 401 || !bytes.Equal(c.body, fixtures.ReadAnthropic("error_401.json")) || c.header.Get("X-Pandokos-Error") != "" {
			t.Errorf("%d %s %v", c.status, c.body, c.header)
		}
		testutil.AssertAbsent(t, "the response", c.dump(), testutil.SecretForms(fixtures.CanaryProviderKey)...)
	})
}

// TestAnthropicUsageRecord is T-533: one record per case of section 5.4.
func TestAnthropicUsageRecord(t *testing.T) {
	costAt := func(t *testing.T, h *harness, model string, u family.Usage) pricing.Cost {
		t.Helper()
		v, ok := pricing.VersionAt(h.targets["anthropic/"+model].Prices, time.Now())
		return pricing.CostOf(u, v, ok)
	}
	sameCost := func(t *testing.T, got, want pricing.Cost) {
		t.Helper()
		if !got.Known || !want.Known || got.Amount.Cmp(want.Amount) != 0 {
			t.Errorf("cost %+v, want %+v", got, want)
		}
	}
	dec := func(s string) pricing.Decimal {
		d, err := pricing.ParseDecimal(s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	t.Run("a complete stream", func(t *testing.T) {
		h := newFamilies(t, nil)
		c := h.sendA(t, anthropicRoute, messages("fake-basic", true), anthropicHdr())
		if c.status != 200 {
			t.Fatalf("%d", c.status)
		}
		r := h.oneRecord(t)
		if r.Provider != "anthropic" || r.APIFamily != "anthropic" || r.Operation != "messages" || r.Stream == nil || !*r.Stream || r.Outcome != usage.OutcomeOK {
			t.Errorf("record %+v", r)
		}
		if r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceProvider {
			t.Fatalf("sources %q %q", r.InputSource, r.OutputSource)
		}
		if r.Usage.InputTokens != 12 || r.Usage.OutputTokens != 7 || r.Usage.CacheReadTokens != 0 || r.Usage.CacheWriteTokens != 0 || len(r.Usage.Reported) != 4 {
			t.Errorf("usage %+v", r.Usage)
		}
		if !reflect.DeepEqual(r.Estimated, family.Usage{}) {
			t.Errorf("an estimate beside the provider's figures: %+v", r.Estimated)
		}
		sameCost(t, r.Cost, costAt(t, h, "fake-basic", family.Usage{InputTokens: 12, OutputTokens: 7}))
		if r.CostStatus != "known" || r.Cost.Amount.Cmp(dec("0.000078")) != 0 {
			t.Errorf("cost %s %s", r.CostStatus, r.Cost.Amount)
		}
	})

	t.Run("a plain answer", func(t *testing.T) {
		h := newFamilies(t, nil)
		h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr())
		r := h.oneRecord(t)
		if r.Stream == nil || *r.Stream || r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceProvider || r.Usage.InputTokens != 12 || r.Usage.OutputTokens != 7 {
			t.Errorf("record %+v", r)
		}
		sameCost(t, r.Cost, costAt(t, h, "fake-basic", family.Usage{InputTokens: 12, OutputTokens: 7}))
	})

	t.Run("fake-cache: inclusive input, the 1-hour write and its rate", func(t *testing.T) {
		for _, stream := range []bool{false, true} {
			h := newFamilies(t, nil)
			h.sendA(t, anthropicRoute, messages(fakeprovider.ModelCache, stream), anthropicHdr())
			r := h.oneRecord(t)
			if r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceProvider {
				t.Fatalf("stream %v: sources %q %q", stream, r.InputSource, r.OutputSource)
			}
			u := r.Usage
			if u.InputTokens != 2310 || u.CacheReadTokens != 2000 || u.CacheWriteTokens != 300 || u.OutputTokens != 50 || u.Extra["cache_write_1h_tokens"] != 200 {
				t.Errorf("stream %v: usage %+v", stream, u)
			}
			if !stream && u.Extra["web_search_requests"] != 2 {
				t.Errorf("web_search_requests %v: the unit is stored in other_units", u.Extra)
			}
			if u.Reported["input_tokens"] != 10 || u.Reported["cache_creation.ephemeral_1h_input_tokens"] != 200 || u.Reported["output_tokens"] != 50 {
				t.Errorf("provider usage %v", u.Reported)
			}
			// 10 input at 3, 50 output at 6, 2000 cache read at 0.3, 100 five-minute and 200 one-hour writes at 3.75 and 6, per million.
			if r.CostStatus != "known" || r.Cost.Amount.Cmp(dec("0.002505")) != 0 {
				t.Errorf("stream %v: cost %s %s, want 0.002505", stream, r.CostStatus, r.Cost.Amount)
			}
			var oneHour *pricing.CostLine
			for i := range r.Cost.Lines {
				if r.Cost.Lines[i].Category == pricing.CategoryCacheWrite1h {
					oneHour = &r.Cost.Lines[i]
				}
			}
			if oneHour == nil || oneHour.Units != 200 || oneHour.Rate.Cmp(dec("6")) != 0 {
				t.Errorf("stream %v: no cost line for the 1-hour write at its rate: %+v", stream, r.Cost.Lines)
			}
		}
	})

	t.Run("a stream cut after message_start: provider input, estimated output", func(t *testing.T) {
		h := newFamilies(t, nil)
		status, got, _ := h.sendLoose(t, anthropicRoute, messages(fakeprovider.ModelCutAfterStart, true))
		_ = status
		r := h.oneRecord(t)
		if r.Outcome != usage.OutcomeUpstreamError {
			t.Errorf("outcome %q", r.Outcome)
		}
		if r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceEstimated {
			t.Fatalf("sources %q %q", r.InputSource, r.OutputSource)
		}
		wantOut := countDeltas(got)
		if r.Usage.InputTokens != 12 || r.Usage.OutputTokens != 0 || r.Estimated.InputTokens != 0 || r.Estimated.OutputTokens != wantOut {
			t.Errorf("usage %+v estimated %+v, want provider input 12 and estimated output %d", r.Usage, r.Estimated, wantOut)
		}
		if len(r.Usage.Reported) == 0 {
			t.Error("the provider's own counters were not kept")
		}
		sameCost(t, r.Cost, costAt(t, h, fakeprovider.ModelCutAfterStart, family.Usage{InputTokens: 12, OutputTokens: wantOut}))
	})

	t.Run("a stream cancelled after two deltas: the estimate counts the deltas delivered", func(t *testing.T) {
		h := newFamilies(t, nil)
		h.up.GateEachEvent()
		t.Cleanup(h.up.Release)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		resp, err := h.request(ctx, t, http.MethodPost, anthropicRoute, messages(fakeprovider.ModelBasic, true), anthropicHdr())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		br := bufio.NewReader(resp.Body)
		// message_start, content_block_start, ping, then two content_block_delta events.
		for i := 0; i < 5; i++ {
			readEventWithin(t, br, 3*time.Second)
			if i < 4 {
				h.up.Advance()
			}
		}
		cancel()
		if !h.up.WaitCancelled(2 * time.Second) {
			t.Fatal("the provider did not see the cancellation")
		}
		r := h.settle(t, 1)[0]
		if r.Outcome != usage.OutcomeCancelled || r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceEstimated || r.Usage.InputTokens != 12 || r.Estimated.OutputTokens != 2 {
			t.Errorf("record %q %q %q usage %+v estimated %+v", r.Outcome, r.InputSource, r.OutputSource, r.Usage, r.Estimated)
		}
		sameCost(t, r.Cost, costAt(t, h, "fake-basic", family.Usage{InputTokens: 12, OutputTokens: 2}))
	})

	t.Run("an error event in a 200 stream keeps what was reported before it", func(t *testing.T) {
		h := newFamilies(t, nil)
		_, got, err := h.sendLoose(t, anthropicRoute, messages(fakeprovider.ModelStreamError, true))
		if err != nil || !bytes.Equal(got, fixtures.ReadAnthropic("message_stream_error.sse")) {
			t.Fatalf("the stream: %v", err)
		}
		r := h.oneRecord(t)
		if r.Outcome != usage.OutcomeOK || r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceEstimated || r.Usage.InputTokens != 12 || r.Estimated.OutputTokens != 2 {
			t.Errorf("record %q %q %q usage %+v estimated %+v", r.Outcome, r.InputSource, r.OutputSource, r.Usage, r.Estimated)
		}
	})

	t.Run("no usage in the answer: both estimated", func(t *testing.T) {
		for _, stream := range []bool{false, true} {
			h := newFamilies(t, nil)
			_, got, _ := h.sendLoose(t, anthropicRoute, messages(fakeprovider.ModelNoUsage, stream))
			r := h.oneRecord(t)
			if r.InputSource != usage.SourceEstimated || r.OutputSource != usage.SourceEstimated || r.Usage.InputTokens != 0 || r.Usage.OutputTokens != 0 {
				t.Fatalf("stream %v: record %q %q usage %+v", stream, r.InputSource, r.OutputSource, r.Usage)
			}
			wantIn := (*r.RequestBytes + 3) / 4
			wantOut := (*r.ResponseBytes + 3) / 4
			if stream {
				wantOut = countDeltas(got)
			}
			if r.Estimated.InputTokens != wantIn || r.Estimated.OutputTokens != wantOut {
				t.Errorf("stream %v: estimate %+v, want %d/%d", stream, r.Estimated, wantIn, wantOut)
			}
			if line := h.lastRequestLine(t); line["usage_output_source"] != nil {
				t.Errorf("usage_output_source logged for equal sources: %v", line["usage_output_source"])
			}
		}
	})

	t.Run("token counting: no sources, no usage, no cost", func(t *testing.T) {
		h := newFamilies(t, nil)
		c := h.sendA(t, countRoute, messages("fake-basic", false), anthropicHdr())
		if c.status != 200 {
			t.Fatalf("%d", c.status)
		}
		r := h.oneRecord(t)
		if r.Operation != "messages.count_tokens" || r.InputSource != usage.SourceNone || r.OutputSource != usage.SourceNone || r.CostStatus != "none" || r.Cost.Known {
			t.Errorf("record %+v", r)
		}
		if r.Usage.InputTokens != 0 || r.Usage.OutputTokens != 0 || len(r.Usage.Reported) != 0 || !reflect.DeepEqual(r.Estimated, family.Usage{}) {
			t.Errorf("usage %+v estimated %+v", r.Usage, r.Estimated)
		}
	})

	t.Run("a provider error: no usage", func(t *testing.T) {
		h := newFamilies(t, nil)
		h.sendA(t, anthropicRoute, messages(fakeprovider.ModelError529, false), anthropicHdr())
		r := h.oneRecord(t)
		if r.Outcome != usage.OutcomeUpstreamError || r.UpstreamStatus != 529 || r.InputSource != usage.SourceNone || r.OutputSource != usage.SourceNone || r.CostStatus != "none" {
			t.Errorf("record %+v", r)
		}
	})

	t.Run("the openai family is unchanged beside it", func(t *testing.T) {
		h := newFamilies(t, nil)
		h.sendAuthed(t, route, chat("fake-basic", false))
		r := h.oneRecord(t)
		if r.APIFamily != "openai" || r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceProvider || r.Usage.InputTokens != 9 {
			t.Errorf("record %+v", r)
		}
	})
}

// countDeltas counts the complete content_block_delta events of a Messages stream.
func countDeltas(stream []byte) int64 {
	var n int64
	for _, ev := range splitEvents(stream) {
		if complete(ev) && bytes.Contains(ev, []byte(`"type":"content_block_delta"`)) {
			n++
		}
	}
	return n
}

// TestAnthropicNoBuffering is T-534: with the gate, event n+1 is not sent by the provider until
// the caller has read event n, and the bytes are the provider's.
func TestAnthropicNoBuffering(t *testing.T) {
	for _, c := range []struct {
		name    string
		fixture string
		model   string
	}{
		{"a text stream", "message_stream.sse", fakeprovider.ModelBasic},
		{"a stream with cache usage", "message_stream_cumulative.sse", fakeprovider.ModelCache},
		{"a CRLF stream with pings", "message_stream_ping_crlf.sse", fakeprovider.ModelCRLF},
		{"a tool-use stream", "message_stream_tools.sse", fakeprovider.ModelTools},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newFamilies(t, nil)
			h.up.GateEachEvent()
			t.Cleanup(h.up.Release)
			resp, err := h.request(ctx5(t), t, http.MethodPost, anthropicRoute, messages(c.model, true), anthropicHdr())
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			br := bufio.NewReader(resp.Body)
			events := eventStrings(fixtures.ReadAnthropic(c.fixture))
			var got strings.Builder
			for i, ev := range events {
				g := readEventWithin(t, br, 3*time.Second)
				got.WriteString(g)
				if g != ev {
					t.Fatalf("event %d:\n got %q\nwant %q", i, g, ev)
				}
				if i < len(events)-1 {
					h.up.Advance()
				}
			}
			if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
				t.Errorf("after the last event: %q %v", rest, err)
			}
			if got.String() != string(fixtures.ReadAnthropic(c.fixture)) {
				t.Error("the bytes are not the provider's")
			}
			if r := h.oneRecord(t); r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceProvider {
				t.Errorf("sources %q %q", r.InputSource, r.OutputSource)
			}
		})
	}
}

// TestAnthropicCancellation is T-535: the caller disconnects after the first event.
func TestAnthropicCancellation(t *testing.T) {
	baseline := runtime.NumGoroutine()
	h := newFamilies(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := h.request(ctx, t, http.MethodPost, anthropicRoute, messages(fakeprovider.ModelStallAfterFirst, true), anthropicHdr())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	ev, err := testutil.ReadEvent(bufio.NewReader(resp.Body))
	if err != nil || !strings.HasPrefix(ev, "event: message_start") {
		t.Fatalf("first event %q %v", ev, err)
	}
	cancel()
	if !h.up.WaitCancelled(2 * time.Second) {
		t.Fatal("the provider did not see the cancellation within 2 s")
	}
	r := h.settle(t, 1)[0]
	if r.Outcome != usage.OutcomeCancelled || r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceEstimated || r.Usage.InputTokens != 12 || r.Estimated.OutputTokens != 0 {
		t.Errorf("record: %q sources %q %q usage %+v estimated %+v", r.Outcome, r.InputSource, r.OutputSource, r.Usage, r.Estimated)
	}
	if n := h.rec.Admits() - len(h.rec.Records()); n != 0 {
		t.Errorf("%d slots in use", n)
	}
	h.srv.Client().CloseIdleConnections()
	testutil.Eventually(t, 5*time.Second, "the provider handler to finish", func() bool { return h.up.InFlight() == 0 })
	testutil.Eventually(t, 5*time.Second, "the goroutine count to return to its base", func() bool {
		h.srv.Client().CloseIdleConnections()
		return runtime.NumGoroutine() <= baseline+8
	})
}

// TestAnthropicMetadataAndTrace is T-536: metadata is recorded and never forwarded; the caller's
// traceparent parents the span; with propagation on, Pandokos's own context reaches the provider.
func TestAnthropicMetadataAndTrace(t *testing.T) {
	const p = "X-Pandokos-Metadata-"
	t.Run("metadata is on the record and never at the provider", func(t *testing.T) {
		h := newFamilies(t, nil)
		hdr := anthropicHdr()
		hdr.Set(p+"Team", "alpha")
		hdr.Set(p+"Note", metaCanary)
		hdr.Set(p+"-bad", "v")
		c := h.sendA(t, anthropicRoute, messages("fake-basic", false), hdr)
		if c.status != 200 {
			t.Fatalf("%d %s", c.status, c.body)
		}
		r := h.oneRecord(t)
		if len(r.Metadata) != 2 || r.Metadata["team"] != "alpha" || r.Metadata["note"] != metaCanary || r.MetadataDropped != 1 {
			t.Errorf("metadata %v dropped %d", r.Metadata, r.MetadataDropped)
		}
		if got := c.header.Get("X-Pandokos-Metadata-Dropped"); got != "1" {
			t.Errorf("the response header says %q", got)
		}
		for k, v := range h.up.Requests()[0].Header {
			if strings.HasPrefix(strings.ToLower(k), "x-pandokos") || strings.Contains(strings.Join(v, ","), metaCanary) {
				t.Errorf("the provider received %s", k)
			}
		}
		if bytes.Contains(h.up.Requests()[0].Body, []byte(metaCanary)) {
			t.Error("metadata reached the provider's body")
		}
		testutil.AssertAbsent(t, "the response", c.dump(), metaCanary)
		testutil.AssertAbsent(t, "the logs", h.logs.String(), metaCanary)
	})
	t.Run("the caller's traceparent parents the spans and is not forwarded", func(t *testing.T) {
		h := newFamilies(t, nil)
		hdr := anthropicHdr()
		hdr.Set("Traceparent", callerTP)
		h.sendA(t, anthropicRoute, messages("fake-basic", false), hdr)
		server, client := endedSpans(t, h, 2)
		if server.SpanContext().TraceID().String() != callerTrace || client.SpanContext().TraceID().String() != callerTrace {
			t.Errorf("trace %s / %s, want %s", server.SpanContext().TraceID(), client.SpanContext().TraceID(), callerTrace)
		}
		if p := server.Parent(); p.SpanID().String() != callerSpan || !p.IsRemote() {
			t.Errorf("the parent of the server span: %v", p)
		}
		if rec := h.oneRecord(t); rec.TraceID != callerTrace {
			t.Errorf("the record's trace %s", rec.TraceID)
		}
		if v := h.up.Requests()[0].Header.Values("Traceparent"); len(v) != 0 {
			t.Errorf("the provider received traceparent %v", v)
		}
		if name := client.Name(); name != "chat" {
			t.Errorf("client span %q", name)
		}
	})
	t.Run("propagation on: Pandokos's own context", func(t *testing.T) {
		h := newFamilies(t, func(o *gateway.Options) { o.PropagateTrace = true })
		hdr := anthropicHdr()
		hdr.Set("Traceparent", callerTP)
		h.sendA(t, anthropicRoute, messages("fake-basic", false), hdr)
		_, client := endedSpans(t, h, 2)
		m := reTraceparent.FindStringSubmatch(h.up.Requests()[0].Header.Get("Traceparent"))
		if m == nil || m[1] != callerTrace || m[2] != client.SpanContext().SpanID().String() || m[2] == callerSpan {
			t.Errorf("traceparent %q", h.up.Requests()[0].Header.Get("Traceparent"))
		}
	})
}

// TestAnthropicGuard is T-537: a private-address endpoint of an anthropic provider that is not
// marked private is refused; redirects are not followed; there is no retry.
func TestAnthropicGuard(t *testing.T) {
	t.Run("a private address on a public endpoint", func(t *testing.T) {
		for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
			up := fakeprovider.NewTLS(t)
			h := newFamiliesWith(t, up, testutil.TestGuard(t, trusting(up)), nil)
			tgt := h.targets["anthropic/fake-basic"]
			u := *tgt.BaseURL
			u.Host = host + ":" + u.Port()
			tgt.BaseURL, tgt.Private = &u, false
			h.targets["anthropic/fake-basic"] = tgt
			c := h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr())
			assertAnthropicContract(t, host, c, family.CodeUpstreamUnreachable)
			if up.Connections() != 0 {
				t.Errorf("%s: the listener accepted %d connections", host, up.Connections())
			}
			line := h.awaitOutcome(t, "pandokos_error")
			if line["egress_reason"] != "private_address" || line["upstream_error"] != "egress" {
				t.Errorf("%s: line %v", host, line)
			}
			u2, _ := url.Parse(up.URL())
			testutil.AssertAbsent(t, "the response", c.dump(), u2.Host, u2.Hostname(), u2.Port(), "localhost", "::1")
			if l := h.logs.Find(t, "egress blocked"); len(l) == 0 || l[0]["provider"] != "anthropic" {
				t.Errorf("%s: the egress blocked line: %v", host, l)
			}
		}
	})
	t.Run("the same target marked private is reached", func(t *testing.T) {
		up := fakeprovider.NewTLS(t)
		h := newFamiliesWith(t, up, testutil.TestGuard(t, trusting(up)), nil)
		c := h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr())
		if c.status != 200 || len(up.Requests()) != 1 {
			t.Fatalf("status %d, %d requests: %s", c.status, len(up.Requests()), c.body)
		}
	})
	t.Run("plain http on a public endpoint", func(t *testing.T) {
		up := fakeprovider.New(t)
		h := newFamiliesWith(t, up, testutil.TestGuard(t), nil)
		tgt := h.targets["anthropic/fake-basic"]
		tgt.Private = false
		h.targets["anthropic/fake-basic"] = tgt
		c := h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr())
		assertAnthropicContract(t, "scheme", c, family.CodeUpstreamUnreachable)
		if up.Connections() != 0 {
			t.Error("the provider was contacted")
		}
	})
	t.Run("a forbidden address is refused even when private", func(t *testing.T) {
		h := newFamilies(t, nil)
		tgt := h.targets["anthropic/fake-basic"]
		meta, _ := url.Parse("http://169.254.169.254")
		tgt.BaseURL, tgt.Private = meta, true
		h.targets["anthropic/fake-basic"] = tgt
		c := h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr())
		assertAnthropicContract(t, "metadata", c, family.CodeUpstreamUnreachable)
		if line := h.awaitOutcome(t, "pandokos_error"); line["egress_reason"] != "forbidden_address" {
			t.Errorf("line %v", line)
		}
	})
	t.Run("redirects are not followed", func(t *testing.T) {
		var other atomic.Int64
		second := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { other.Add(1) }))
		t.Cleanup(second.Close)
		for _, code := range []int{301, 302, 307, 308} {
			h := newFamilies(t, nil)
			h.up.RedirectTo(second.URL + "/elsewhere")
			hdr := anthropicHdr()
			hdr.Set("X-Fake-Status", fmt.Sprint(code)) // not forwarded: the fake answers 307 whatever
			c := h.sendA(t, anthropicRoute, messages(fakeprovider.ModelRedirect, false), hdr)
			assertAnthropicContract(t, fmt.Sprint(code), c, family.CodeUpstreamUnreachable)
			if n := len(h.up.Requests()); n != 1 {
				t.Errorf("the provider saw %d requests", n)
			}
		}
		if other.Load() != 0 {
			t.Errorf("the redirect target received %d requests", other.Load())
		}
	})
	t.Run("no retry", func(t *testing.T) {
		h := newFamilies(t, nil)
		if c := h.sendA(t, anthropicRoute, messages("fake-basic", false), anthropicHdr()); c.status != 200 {
			t.Fatalf("the warm-up: %d", c.status)
		}
		c := h.sendA(t, anthropicRoute, messages(fakeprovider.ModelClose, false), anthropicHdr())
		assertAnthropicContract(t, "fake-close", c, family.CodeUpstreamUnreachable)
		if n := len(h.up.Requests()); n != 2 {
			t.Errorf("the provider saw %d requests, want 2 (the warm-up and one attempt)", n)
		}
		if n := h.up.Connections(); n != 1 {
			t.Errorf("%d connections: the request was sent again on a new one", n)
		}
	})
}

// TestAnthropicPipelineOrder is T-426 on an anthropic route: a refusal at a step stops every later
// step, as on an openai route.
func TestAnthropicPipelineOrder(t *testing.T) {
	active := testutil.InferencePrincipal()
	with := func(mutate func(*authz.Principal)) authz.Principal {
		p := active
		mutate(&p)
		return p
	}
	good := messages("fake-basic", false)
	type tc struct {
		name         string
		method, path string
		auth         gateway.Authenticator
		body         io.Reader
		contentLen   int64
		maxBody      int64
		resolveErr   error
		noCredential bool
		wantCode     family.ErrorCode
		wantAuth     int64
		wantResolve  int64
		wantOpen     int
		refuse       bool
		wantReserve  int
	}
	cases := []tc{
		{name: "step 2: a path that is not served", method: "POST", path: "/anthropic/v1/models", auth: fixedAuth{p: active}, wantCode: family.CodeEndpointNotSupported},
		{name: "step 2: a method that is not served", method: "GET", path: anthropicRoute, auth: fixedAuth{p: active}, wantCode: family.CodeEndpointNotSupported},
		{name: "step 3: unauthenticated", method: "POST", path: anthropicRoute, auth: fixedAuth{err: authz.ErrUnauthenticated}, wantCode: family.CodeUnauthenticated, wantAuth: 1},
		{name: "step 3: unavailable", method: "POST", path: anthropicRoute, auth: fixedAuth{err: fmt.Errorf("%w: x", authz.ErrUnavailable)}, wantCode: family.CodeUnavailable, wantAuth: 1},
		{name: "step 4: no role", method: "POST", path: anthropicRoute, auth: fixedAuth{p: with(func(p *authz.Principal) { p.Roles = nil })}, wantCode: family.CodeForbidden, wantAuth: 1},
		{name: "step 4: only model:invoke", method: "POST", path: countRoute, auth: fixedAuth{p: with(func(p *authz.Principal) { p.Scopes = []string{authz.ModelInvoke} })}, wantCode: family.CodeForbidden, wantAuth: 1},
		{name: "step 4: a suspended tenant", method: "POST", path: anthropicRoute, auth: fixedAuth{p: with(func(p *authz.Principal) { p.TenantState = "suspended" })}, wantCode: family.CodeTenantSuspended, wantAuth: 1},
		{name: "step 5: a declared length over the limit", method: "POST", path: anthropicRoute, auth: fixedAuth{p: active}, contentLen: 1000, maxBody: 100, wantCode: family.CodeRequestTooLarge, wantAuth: 1},
		{name: "step 6: a body that is not an object", method: "POST", path: anthropicRoute, auth: fixedAuth{p: active}, body: bytes.NewReader([]byte(`{}`)), contentLen: 2, wantCode: family.CodeInvalidRequest, wantAuth: 1},
		{name: "step 7: a model that is not allowed", method: "POST", path: anthropicRoute, auth: fixedAuth{p: active}, body: bytes.NewReader([]byte(`{"model":"nope"}`)), contentLen: 16, wantCode: family.CodeModelNotAllowed, wantAuth: 1, wantResolve: 1},
		{name: "step 7: a decision that cannot be taken", method: "POST", path: countRoute, auth: fixedAuth{p: active}, body: bytes.NewReader(good), contentLen: int64(len(good)), resolveErr: errors.New("synthetic"), wantCode: family.CodeUnavailable, wantAuth: 1, wantResolve: 1},
		{name: "step 7b: a limit refuses", method: "POST", path: anthropicRoute, auth: fixedAuth{p: active}, body: bytes.NewReader(good), contentLen: int64(len(good)), refuse: true, wantCode: family.CodeRateLimited, wantAuth: 1, wantResolve: 1, wantReserve: 1},
		{name: "step 8: no credential", method: "POST", path: anthropicRoute, auth: fixedAuth{p: active}, body: bytes.NewReader(good), contentLen: int64(len(good)), noCredential: true, wantCode: family.CodeNoCredential, wantAuth: 1, wantResolve: 1, wantOpen: 1, wantReserve: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			auth := &orderAuth{inner: c.auth}
			creds := testutil.StaticCredentials(map[string]string{"anthropic": fixtures.CanaryProviderKey})
			if c.noCredential {
				creds = testutil.StaticCredentials(nil)
			}
			lim := testutil.NewStaticLimits()
			if c.refuse {
				lim.Refuse(refusalOf("requests_per_minute"))
			}
			h := newFamilies(t, func(o *gateway.Options) {
				o.Authenticator = auth
				o.Credentials = creds
				o.Limits = lim
				if c.maxBody > 0 {
					o.MaxRequestBodyBytes = c.maxBody
				}
			})
			if c.resolveErr != nil {
				h.cat.ResolveErr = c.resolveErr
			}
			var body io.Reader = &readSpy{t: t, allowed: true}
			if c.body != nil {
				body = c.body
			}
			req := httptest.NewRequest(c.method, c.path, body)
			req.ContentLength = c.contentLen
			req.Header = anthropicHdr()
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			res := rec.Result()
			b, _ := io.ReadAll(res.Body)
			assertAnthropicContract(t, c.name, captured{status: res.StatusCode, header: res.Header, body: b}, c.wantCode)
			if got := len(lim.Of("reserve")); got != c.wantReserve {
				t.Errorf("Reserve called %d times, want %d", got, c.wantReserve)
			}
			if got := auth.n.Load(); got != c.wantAuth {
				t.Errorf("Authenticate called %d times, want %d", got, c.wantAuth)
			}
			if got := h.cat.Resolves.Load(); got != c.wantResolve {
				t.Errorf("Resolve called %d times, want %d", got, c.wantResolve)
			}
			if got := len(creds.Calls()); got != c.wantOpen {
				t.Errorf("Open called %d times, want %d", got, c.wantOpen)
			}
			if n := len(h.up.Requests()); n != 0 {
				t.Errorf("the provider received %d requests", n)
			}
			wantAdmits := 1
			if strings.HasPrefix(c.name, "step 2:") || strings.HasPrefix(c.name, "step 3:") {
				wantAdmits = 0
			}
			if got := h.rec.Admits(); got != wantAdmits {
				t.Errorf("Admit called %d times, want %d", got, wantAdmits)
			}
		})
	}
}
