package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/familycodes"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// captured is a response as a caller sees it.
type captured struct {
	status int
	line   string
	header http.Header
	body   []byte
}

// dump is the status line, the headers and the body: everything a caller sees.
func (c captured) dump() string {
	var sb strings.Builder
	sb.WriteString(c.line + "\n")
	keys := make([]string, 0, len(c.header))
	for k := range c.header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(k + ": " + strings.Join(c.header[k], ", ") + "\n")
	}
	sb.Write(c.body)
	return sb.String()
}

// normalized is the dump without what differs between two refusals of the same kind: the
// request ID and the date.
func (c captured) normalized() string {
	h := c.header.Clone()
	h.Del("X-Pandokos-Request-Id")
	h.Del("Date")
	return captured{line: c.line, header: h, body: c.body}.dump()
}

func (h *harness) send(t *testing.T, method, path string, body []byte, hdr http.Header) captured {
	t.Helper()
	resp, err := h.request(ctx5(t), t, method, path, body, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	return captured{status: resp.StatusCode, line: resp.Proto + " " + resp.Status, header: resp.Header, body: b}
}

// sendAuthed is an authenticated POST of body to path.
func (h *harness) sendAuthed(t *testing.T, path string, body []byte) captured {
	t.Helper()
	return h.send(t, http.MethodPost, path, body, authed())
}

var reRequestID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// errorBody is the body of a Pandokos error in the shape of the openai family.
func errorBody(code family.ErrorCode) string {
	msg := strings.ReplaceAll(code.Message(), `"`, `\"`)
	return `{"error":{"message":"` + msg + `","type":"pandokos_error","param":null,"code":"` + string(code) + `"}}`
}

// assertContract checks the contract of a Pandokos error: status, both headers, the shape of the
// family and the constant message.
func assertContract(t *testing.T, name string, c captured, code family.ErrorCode) {
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
	if string(c.body) != errorBody(code) {
		t.Errorf("%s: body %s\nwant %s", name, c.body, errorBody(code))
	}
}

// providersFor returns the options that make the catalogue serve the given providers (name to
// family) over the harness's targets, and give each of the named providers the same secret.
func providersFor(cat map[string]string, targets map[string]gateway.Target, secrets map[string]string) func(*gateway.Options) {
	return func(o *gateway.Options) {
		o.Catalogue = testutil.StaticCatalogue(cat, targets)
		o.Credentials = testutil.StaticCredentials(secrets)
	}
}

// TestRouteByProvider is T-425: the first path segment names a catalogue provider.
func TestRouteByProvider(t *testing.T) {
	up1, up2 := fakeprovider.New(t), fakeprovider.New(t)
	prefixed := testutil.TargetFor(up2, "compat", "fake-basic")
	prefixed.BaseURL, _ = url.Parse(up2.URL() + "/api/prefix")
	targets := map[string]gateway.Target{
		"openai/fake-basic": testutil.TargetFor(up1, "openai", "fake-basic"),
		"compat/fake-basic": prefixed,
	}
	cat := map[string]string{"openai": "openai", "compat": "openai", "anthropic": "anthropic"}
	secrets := map[string]string{"openai": fixtures.CanaryProviderKey, "compat": fixtures.CanaryProviderKey}
	h := newHarness(t, providersFor(cat, targets, secrets))

	t.Run("each provider reaches its own endpoint at base plus native path, without a query", func(t *testing.T) {
		if c := h.sendAuthed(t, "/openai/v1/chat/completions?a=b&token=zzz", chat("fake-basic", false)); c.status != 200 {
			t.Fatalf("openai: %d %s", c.status, c.body)
		}
		if c := h.sendAuthed(t, "/compat/v1/chat/completions?x=y", chat("fake-basic", false)); c.status == 0 {
			t.Fatal("no answer")
		}
		r1, r2 := up1.Requests(), up2.Requests()
		if len(r1) != 1 || r1[0].Path != "/v1/chat/completions" || r1[0].RawQuery != "" {
			t.Errorf("the first provider saw %+v", r1)
		}
		if len(r2) != 1 || r2[0].Path != "/api/prefix/v1/chat/completions" || r2[0].RawQuery != "" {
			t.Errorf("the second provider saw %+v", r2)
		}
	})

	t.Run("an escaped base path stays escaped", func(t *testing.T) {
		up := fakeprovider.New(t)
		esc := testutil.TargetFor(up, "openai", "fake-basic")
		esc.BaseURL, _ = url.Parse(up.URL() + "/a%2Fb")
		he := newHarness(t, providersFor(map[string]string{"openai": "openai"}, map[string]gateway.Target{"openai/fake-basic": esc},
			map[string]string{"openai": fixtures.CanaryProviderKey}))
		// The fake answers 404 at a prefixed path; what counts is the path it recorded.
		if c := he.sendAuthed(t, "/openai/v1/chat/completions", chat("fake-basic", false)); c.status == 0 {
			t.Fatal("no answer")
		}
		r := up.Requests()
		if len(r) != 1 || r[0].EscapedPath != "/a%2Fb/v1/chat/completions" {
			t.Errorf("the provider saw %+v, want the path /a%%2Fb/v1/chat/completions", r)
		}
	})

	t.Run("a name that is not served is one and the same 404", func(t *testing.T) {
		before1, before2 := len(up1.Requests()), len(up2.Requests())
		paths := []string{
			"/nope/v1/chat/completions",      // unknown, or disabled: the catalogue says the same
			"/v1/chat/completions",           // reserved
			"/healthz",                       // reserved
			"/metrics/v1/chat/completions",   // reserved
			"/anthropic/v1/chat/completions", // a family without an adapter
			"//openai/v1/chat/completions",
			"/",
			"/openai",
			"/openai/",
			"/openai/v1/embeddings",
			"/openai/chat/completions",
			"/Openai/v1/chat/completions",
			"/openai.x/v1/chat/completions",
			"/" + strings.Repeat("a", 64) + "/v1/chat/completions",
		}
		var first string
		for _, p := range paths {
			c := h.send(t, http.MethodPost, p, chat("fake-basic", false), authed())
			assertContract(t, p, c, family.CodeEndpointNotSupported)
			if first == "" {
				first = c.normalized()
			} else if got := c.normalized(); got != first {
				t.Errorf("%s: the answer differs from the others:\n%s\nvs\n%s", p, got, first)
			}
		}
		if len(up1.Requests()) != before1 || len(up2.Requests()) != before2 {
			t.Error("a request was forwarded for a name that is not served")
		}
		for _, l := range h.logs.Find(t, "request") {
			if l["route"] == "unmatched" {
				if _, ok := l["provider"]; ok {
					t.Errorf("a provider is logged for an unmatched route: %v", l)
				}
			}
		}
	})

	t.Run("the catalogue is asked only about plausible names", func(t *testing.T) {
		cat := testutil.StaticCatalogue(map[string]string{"openai": "openai"}, nil)
		h := newHarness(t, func(o *gateway.Options) { o.Catalogue = cat })
		for _, p := range []string{"/Openai/v1/x", "/open.ai/v1/x", "/a_b/v1/x", "/" + strings.Repeat("a", 64) + "/v1/x", "//v1/x", "/%20/v1/x"} {
			h.send(t, http.MethodPost, p, nil, authed())
		}
		if n := cat.Providers.Load(); n != 0 {
			t.Errorf("the catalogue was asked %d times about names that cannot be provider names", n)
		}
		h.send(t, http.MethodPost, "/openai/v1/embeddings", nil, authed())
		h.send(t, http.MethodPost, "/"+strings.Repeat("a", 63)+"/v1/chat/completions", nil, authed())
		if n := cat.Providers.Load(); n != 2 {
			t.Errorf("the catalogue was asked %d times, want 2 (a plausible name each)", n)
		}
	})

	t.Run("a catalogue that cannot be read is 503", func(t *testing.T) {
		cat := testutil.StaticCatalogue(map[string]string{"openai": "openai"}, nil)
		cat.ProviderErr = errors.New("synthetic database failure " + fixtures.CanaryCompletion)
		h := newHarness(t, func(o *gateway.Options) { o.Catalogue = cat })
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		assertContract(t, "catalogue error", c, family.CodeUnavailable)
		testutil.AssertNoCanary(t, "the response", c.dump())
		testutil.AssertNoCanary(t, "the logs", h.logs.String())
	})
}

// readSpy is a request body that fails the test when it is read, unless that is allowed.
type readSpy struct {
	t       *testing.T
	allowed bool
	read    atomic.Bool
}

func (r *readSpy) Read([]byte) (int, error) {
	r.read.Store(true)
	if !r.allowed {
		r.t.Error("the request body was read")
	}
	return 0, io.EOF
}

// failingBody returns a fixed error on its first read.
type failingBody struct{ err error }

func (f failingBody) Read([]byte) (int, error) { return 0, f.err }

// orderAuth counts the calls to Authenticate.
type orderAuth struct {
	inner gateway.Authenticator
	n     atomic.Int64
}

func (a *orderAuth) Authenticate(ctx context.Context, presented string) (authz.Principal, error) {
	a.n.Add(1)
	return a.inner.Authenticate(ctx, presented)
}

// TestPipelineOrder is T-426: a refusal at a step stops every later step.
func TestPipelineOrder(t *testing.T) {
	active := testutil.InferencePrincipal()
	with := func(mutate func(*authz.Principal)) authz.Principal {
		p := active
		mutate(&p)
		return p
	}
	good := []byte(`{"model":"fake-basic","messages":[]}`)
	type tc struct {
		name         string
		path         string
		auth         gateway.Authenticator
		body         io.Reader
		contentLen   int64
		maxBody      int64
		resolveErr   error
		noCredential bool
		wantCode     family.ErrorCode
		mayRead      bool
		wantAuth     int64
		wantResolve  int64
		wantOpen     int
	}
	cases := []tc{
		{name: "step 2: a path that is not served", path: "/nope/v1/chat/completions", auth: fixedAuth{p: active}, wantCode: family.CodeEndpointNotSupported},
		{name: "step 2: a method that is not served", path: "/openai/v1/models", auth: fixedAuth{p: active}, wantCode: family.CodeEndpointNotSupported},
		{name: "step 3: unauthenticated", path: route, auth: fixedAuth{err: authz.ErrUnauthenticated}, wantCode: family.CodeUnauthenticated, wantAuth: 1},
		{name: "step 3: unavailable", path: route, auth: fixedAuth{err: fmt.Errorf("%w: x", authz.ErrUnavailable)}, wantCode: family.CodeUnavailable, wantAuth: 1},
		{name: "step 4: no role", path: route, auth: fixedAuth{p: with(func(p *authz.Principal) { p.Roles = nil })}, wantCode: family.CodeForbidden, wantAuth: 1},
		{name: "step 4: only model:invoke", path: route, auth: fixedAuth{p: with(func(p *authz.Principal) { p.Scopes = []string{authz.ModelInvoke} })}, wantCode: family.CodeForbidden, wantAuth: 1},
		{name: "step 4: only credential:use", path: route, auth: fixedAuth{p: with(func(p *authz.Principal) { p.Scopes = []string{authz.CredentialUse} })}, wantCode: family.CodeForbidden, wantAuth: 1},
		{name: "step 4: a suspended tenant", path: route, auth: fixedAuth{p: with(func(p *authz.Principal) { p.TenantState = "suspended" })}, wantCode: family.CodeTenantSuspended, wantAuth: 1},
		{name: "step 5: a declared length over the limit", path: route, auth: fixedAuth{p: active}, contentLen: 1000, maxBody: 100, wantCode: family.CodeRequestTooLarge, wantAuth: 1},
		{name: "step 5: a body that does not arrive in time", path: route, auth: fixedAuth{p: active}, body: failingBody{os.ErrDeadlineExceeded}, contentLen: 10, wantCode: family.CodeRequestTimeout, mayRead: true, wantAuth: 1},
		{name: "step 6: a body that is not an object", path: route, auth: fixedAuth{p: active}, body: bytes.NewReader([]byte(`{}`)), contentLen: 2, wantCode: family.CodeInvalidRequest, mayRead: true, wantAuth: 1},
		{name: "step 7: a model that is not allowed", path: route, auth: fixedAuth{p: active}, body: bytes.NewReader([]byte(`{"model":"nope"}`)), contentLen: 16, wantCode: family.CodeModelNotAllowed, mayRead: true, wantAuth: 1, wantResolve: 1},
		{name: "step 7: a provider without an endpoint", path: route, auth: fixedAuth{p: active}, body: bytes.NewReader(good), contentLen: int64(len(good)), resolveErr: gateway.ErrProviderUnavailable, wantCode: family.CodeEndpointNotSupported, mayRead: true, wantAuth: 1, wantResolve: 1},
		{name: "step 7: a decision that cannot be taken", path: route, auth: fixedAuth{p: active}, body: bytes.NewReader(good), contentLen: int64(len(good)), resolveErr: errors.New("synthetic"), wantCode: family.CodeUnavailable, mayRead: true, wantAuth: 1, wantResolve: 1},
		{name: "step 8: no credential", path: route, auth: fixedAuth{p: active}, body: bytes.NewReader(good), contentLen: int64(len(good)), noCredential: true, wantCode: family.CodeNoCredential, mayRead: true, wantAuth: 1, wantResolve: 1, wantOpen: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			auth := &orderAuth{inner: c.auth}
			up := fakeprovider.New(t)
			var creds *testutil.Credentials
			if c.noCredential {
				creds = testutil.StaticCredentials(nil)
			} else {
				creds = testutil.StaticCredentials(map[string]string{"openai": fixtures.CanaryProviderKey})
			}
			h := newHarnessWith(t, up, testutil.TestGuard(t), func(o *gateway.Options) {
				o.Authenticator = auth
				o.Credentials = creds
				if c.maxBody > 0 {
					o.MaxRequestBodyBytes = c.maxBody
				}
			})
			if c.resolveErr != nil {
				h.cat.ResolveErr = c.resolveErr
			}
			method := http.MethodPost
			if strings.HasSuffix(c.path, "/models") {
				method = http.MethodGet
			}
			spy := &readSpy{t: t, allowed: c.mayRead}
			var body io.Reader = spy
			if c.body != nil {
				body = c.body
			}
			req := httptest.NewRequest(method, c.path, body)
			req.ContentLength = c.contentLen
			req.Header = authed()
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			res := rec.Result()
			b, _ := io.ReadAll(res.Body)
			assertContract(t, c.name, captured{status: res.StatusCode, header: res.Header, body: b}, c.wantCode)
			if got := auth.n.Load(); got != c.wantAuth {
				t.Errorf("Authenticate called %d times, want %d", got, c.wantAuth)
			}
			if got := h.cat.Resolves.Load(); got != c.wantResolve {
				t.Errorf("Resolve called %d times, want %d", got, c.wantResolve)
			}
			if got := len(creds.Calls()); got != c.wantOpen {
				t.Errorf("Open called %d times, want %d", got, c.wantOpen)
			}
			if n := len(up.Requests()); n != 0 {
				t.Errorf("the provider received %d requests", n)
			}
			if c.wantCode == family.CodeRequestTimeout && res.Header.Get("Connection") != "close" {
				t.Errorf("a 408 must carry Connection: close, got %q", res.Header.Get("Connection"))
			}
		})
	}
}

// TestModelCheck is T-427.
func TestModelCheck(t *testing.T) {
	h := newHarness(t, nil)
	var first string
	for _, model := range []string{"nope", "other-provider-model", "upstream-id-1", "fake-basic-but-not-granted"} {
		c := h.sendAuthed(t, route, chat(model, false))
		assertContract(t, model, c, family.CodeModelNotAllowed)
		if strings.Contains(string(c.body), model) {
			t.Errorf("the refusal names the model %q", model)
		}
		if first == "" {
			first = c.normalized()
		} else if got := c.normalized(); got != first {
			t.Errorf("%s: the refusal differs from the others:\n%s\nvs\n%s", model, got, first)
		}
	}
	for name, tc := range map[string]struct {
		err  error
		code family.ErrorCode
	}{
		"wrapped refusal":         {fmt.Errorf("lookup: %w", gateway.ErrModelNotAllowed), family.CodeModelNotAllowed},
		"provider unavailable":    {gateway.ErrProviderUnavailable, family.CodeEndpointNotSupported},
		"wrapped unavailable":     {fmt.Errorf("lookup: %w", gateway.ErrProviderUnavailable), family.CodeEndpointNotSupported},
		"any other error":         {errors.New("synthetic database failure"), family.CodeUnavailable},
		"a context that was done": {context.Canceled, family.CodeUnavailable},
	} {
		h := newHarness(t, nil)
		h.cat.ResolveErr = tc.err
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		if name == "a context that was done" && c.status == 0 {
			continue
		}
		assertContract(t, name, c, tc.code)
		if n := len(h.up.Requests()); n != 0 {
			t.Errorf("%s: the provider received %d requests", name, n)
		}
		if n := len(h.creds.Calls()); n != 0 {
			t.Errorf("%s: a credential was opened", name)
		}
	}
	if n := len(h.up.Requests()); n != 0 {
		t.Errorf("the provider received %d requests", n)
	}
	if n := len(h.creds.Calls()); n != 0 {
		t.Errorf("%d credentials were opened for refused models", n)
	}
}

// TestInvalidRequest is T-428.
func TestInvalidRequest(t *testing.T) {
	canary := fixtures.CanaryPrompt
	msg := `"messages":[{"role":"user","content":"` + canary + `"}]`
	cases := []struct{ name, body, reason string }{
		{"truncated", `{` + msg, "not_object"},
		{"an array", `[` + msg + `]`, "not_object"},
		{"trailing data", `{"model":"fake-basic",` + msg + `} ` + canary, "not_object"},
		{"an empty body", ``, "not_object"},
		{"no model", `{` + msg + `}`, "model_missing"},
		{"two models", `{"model":"fake-basic","model":"fake-basic",` + msg + `}`, "model_duplicate"},
		{"two models, one escaped", `{"model":"fake-basic","model":"x",` + msg + `}`, "model_duplicate"},
		{"a number", `{"model":7,` + msg + `}`, "model_not_string"},
		{"an empty name", `{"model":"",` + msg + `}`, "model_not_string"},
		{"null", `{"model":null,` + msg + `}`, "model_not_string"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			r := h.sendAuthed(t, route, []byte(c.body))
			assertContract(t, c.name, r, family.CodeInvalidRequest)
			line := h.awaitOutcome(t, "pandokos_error")
			if line["invalid_reason"] != c.reason || line["error_code"] != "invalid_request" || line["status"] != float64(400) {
				t.Errorf("line: %v", line)
			}
			if _, ok := line["model"]; ok {
				t.Errorf("a model is logged for a refused body: %v", line["model"])
			}
			if n := len(h.up.Requests()); n != 0 {
				t.Errorf("the provider received %d requests", n)
			}
			if n := h.cat.Resolves.Load(); n != 0 {
				t.Errorf("the model was resolved for a refused body")
			}
			testutil.AssertNoCanary(t, "the response", r.dump())
			testutil.AssertNoCanary(t, "the logs", h.logs.String())
			testutil.AssertAbsent(t, "the logs", h.logs.String(), testutil.KeyForms(testKey)...)
		})
	}
}

// TestCredentialResolution is T-429.
func TestCredentialResolution(t *testing.T) {
	t.Run("the provider receives the secret of Credentials and never the caller's key", func(t *testing.T) {
		creds := testutil.StaticCredentials(map[string]string{"openai": fixtures.CanaryCredential})
		creds.Pinned = true
		up := fakeprovider.New(t)
		up.Accept(fixtures.CanaryCredential)
		h := newHarnessWith(t, up, testutil.TestGuard(t), func(o *gateway.Options) { o.Credentials = creds })
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		if c.status != 200 {
			t.Fatalf("status %d %s", c.status, c.body)
		}
		reqs := up.Requests()
		if len(reqs) != 1 {
			t.Fatalf("%d requests", len(reqs))
		}
		if got := reqs[0].Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer "+fixtures.CanaryCredential {
			t.Errorf("the provider saw %v", got)
		}
		for k, v := range reqs[0].Header {
			testutil.AssertAbsent(t, "the provider's header "+k, strings.Join(v, ","), testutil.KeyForms(testKey)...)
		}
		calls := creds.Calls()
		if len(calls) != 1 || calls[0].Provider != "openai" || calls[0].RequestID != c.header.Get("X-Pandokos-Request-Id") {
			t.Errorf("Open was called with %+v, the response's request ID is %q", calls, c.header.Get("X-Pandokos-Request-Id"))
		}
		line := h.lastRequestLine(t)
		if line["credential_id"] != "synthetic-credential-openai" || line["credential_pinned"] != true || line["funding"] != "byok" {
			t.Errorf("line: %v", line)
		}
		testutil.AssertAbsent(t, "the logs", h.logs.String(), testutil.SecretForms(fixtures.CanaryCredential)...)
		testutil.AssertAbsent(t, "the response", c.dump(), testutil.SecretForms(fixtures.CanaryCredential)...)
	})

	t.Run("the refusals of Credentials", func(t *testing.T) {
		for name, tc := range map[string]struct {
			err  error
			code family.ErrorCode
		}{
			"no usable credential": {gateway.ErrNoCredential, family.CodeNoCredential},
			"denied":               {gateway.ErrCredentialDenied, family.CodeForbidden},
			"wrapped, no credential": {
				fmt.Errorf("open: %w", gateway.ErrNoCredential), family.CodeNoCredential,
			},
			"another error": {errors.New("synthetic vault failure " + fixtures.CanaryCredential), family.CodeUnavailable},
		} {
			creds := testutil.StaticCredentials(map[string]string{"openai": fixtures.CanaryCredential})
			creds.Err = tc.err
			h := newHarnessWith(t, fakeprovider.New(t), testutil.TestGuard(t), func(o *gateway.Options) { o.Credentials = creds })
			c := h.sendAuthed(t, route, chat("fake-basic", false))
			assertContract(t, name, c, tc.code)
			if n := len(h.up.Requests()); n != 0 {
				t.Errorf("%s: the provider received %d requests", name, n)
			}
			testutil.AssertAbsent(t, name+": the response", c.dump(), testutil.SecretForms(fixtures.CanaryCredential)...)
			testutil.AssertAbsent(t, name+": the logs", h.logs.String(), testutil.SecretForms(fixtures.CanaryCredential)...)
			line := h.awaitOutcome(t, "pandokos_error")
			for _, f := range []string{"credential_id", "credential_pinned", "funding"} {
				if _, ok := line[f]; ok {
					t.Errorf("%s: %s is logged although no credential was opened", name, f)
				}
			}
		}
	})
}

// TestUpstreamModel is T-430: the public name is replaced by the upstream identifier.
func TestUpstreamModel(t *testing.T) {
	setup := func(t *testing.T) *harness {
		h := newHarness(t, nil)
		tgt := h.targets["openai/fake-basic"]
		tgt.Model, tgt.UpstreamModel = "nick", "fake-basic"
		h.targets["openai/nick"] = tgt
		return h
	}
	t.Run("non-streaming", func(t *testing.T) {
		h := setup(t)
		body := chat("nick", false)
		c := h.sendAuthed(t, route, body)
		if c.status != 200 || !bytes.Equal(c.body, fixtures.Read("chat_completion.json")) {
			t.Fatalf("status %d, body differs from the provider's", c.status)
		}
		want := strings.Replace(string(body), `"model":"nick"`, `"model":"fake-basic"`, 1)
		if reqs := h.up.Requests(); len(reqs) != 1 || string(reqs[0].Body) != want {
			t.Fatalf("the provider received %q, want %q", reqs[0].Body, want)
		}
		line := h.lastRequestLine(t)
		if line["model"] != "nick" {
			t.Errorf("the log names the model %v, want the public name nick", line["model"])
		}
	})
	t.Run("streaming, with the usage splice", func(t *testing.T) {
		h := setup(t)
		body := chat("nick", true)
		c := h.sendAuthed(t, route, body)
		var want []byte
		for i, ev := range bytes.SplitAfter(fixtures.Read("chat_stream_usage.sse"), []byte("\n\n")) {
			if i != 4 {
				want = append(want, ev...)
			}
		}
		if c.status != 200 || !bytes.Equal(c.body, want) {
			t.Fatalf("the stream differs: %d %s", c.status, c.body)
		}
		wantBody := strings.Replace(string(body[:len(body)-1]), `"model":"nick"`, `"model":"fake-basic"`, 1) + `,"stream_options":{"include_usage":true}}`
		if reqs := h.up.Requests(); len(reqs) != 1 || string(reqs[0].Body) != wantBody {
			t.Fatalf("the provider received %q, want %q", reqs[0].Body, wantBody)
		}
	})
	t.Run("the same name sends the caller's bytes", func(t *testing.T) {
		h := newHarness(t, nil)
		body := []byte(`{"model" : "fake-basic" , "messages":[]}`)
		h.sendAuthed(t, route, body)
		if reqs := h.up.Requests(); len(reqs) != 1 || !bytes.Equal(reqs[0].Body, body) {
			t.Fatalf("the provider received %q", reqs[0].Body)
		}
	})
}

// roundTripFunc is an http.RoundTripper made of a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// scenario produces one Pandokos error by its condition.
type scenario struct {
	code family.ErrorCode
	run  func(t *testing.T) (c captured, h *harness)
}

func closedPortTarget(h *harness) {
	closed, _ := url.Parse("http://127.0.0.1:1")
	tgt := h.targets["openai/fake-basic"]
	tgt.BaseURL = closed
	h.targets["openai/fake-basic"] = tgt
}

func scenarios() []scenario {
	active := testutil.InferencePrincipal()
	with := func(mutate func(*authz.Principal)) fixedAuth {
		p := active
		mutate(&p)
		return fixedAuth{p: p}
	}
	basic := func(t *testing.T, mutate func(*gateway.Options)) *harness { return newHarness(t, mutate) }
	return []scenario{
		{family.CodeUnauthenticated, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			return h.send(t, http.MethodPost, route, chat("fake-basic", false), http.Header{}), h
		}},
		{family.CodeTenantSuspended, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) {
				o.Authenticator = with(func(p *authz.Principal) { p.TenantState = "suspended" })
			})
			return h.sendAuthed(t, route, chat("fake-basic", false)), h
		}},
		{family.CodeForbidden, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.Authenticator = with(func(p *authz.Principal) { p.Roles = nil }) })
			return h.sendAuthed(t, route, chat("fake-basic", false)), h
		}},
		{family.CodeModelNotAllowed, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			return h.sendAuthed(t, route, chat("nope", false)), h
		}},
		{family.CodeEndpointNotSupported, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			return h.send(t, http.MethodGet, route, nil, authed()), h
		}},
		{family.CodeNoCredential, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.Credentials = testutil.StaticCredentials(nil) })
			return h.sendAuthed(t, route, chat("fake-basic", false)), h
		}},
		{family.CodeInvalidRequest, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			return h.sendAuthed(t, route, []byte(`{}`)), h
		}},
		{family.CodeRequestTimeout, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			req := httptest.NewRequest(http.MethodPost, route, failingBody{os.ErrDeadlineExceeded})
			req.ContentLength = 50
			req.Header = authed()
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			res := rec.Result()
			b, _ := io.ReadAll(res.Body)
			return captured{status: res.StatusCode, line: res.Status, header: res.Header, body: b}, h
		}},
		{family.CodeRequestTooLarge, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.MaxRequestBodyBytes = 100 })
			return h.sendAuthed(t, route, bytes.Repeat([]byte("a"), 200)), h
		}},
		{family.CodeUpstreamUnreachable, func(t *testing.T) (captured, *harness) {
			h := basic(t, nil)
			closedPortTarget(h)
			return h.sendAuthed(t, route, chat("fake-basic", false)), h
		}},
		{family.CodeUpstreamTimeout, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.MaxRequestDuration = 200 * time.Millisecond })
			return h.sendAuthed(t, route, chat(fakeprovider.ModelHang, false)), h
		}},
		{family.CodeUnavailable, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) {
				o.Authenticator = fixedAuth{err: fmt.Errorf("%w: 08006", authz.ErrUnavailable)}
			})
			return h.sendAuthed(t, route, chat("fake-basic", false)), h
		}},
		{family.CodeInternal, func(t *testing.T) (captured, *harness) {
			h := basic(t, func(o *gateway.Options) { o.Private = panicTransport{} })
			return h.sendAuthed(t, route, chat("fake-basic", false)), h
		}},
	}
}

// TestErrorContract is T-439: each code of section 5.8 by its condition, with the status, both
// headers, the family's body and the constant message. The sweep fails on a code this table of
// scenarios does not know.
func TestErrorContract(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range scenarios() {
		seen[string(s.code)] = true
		t.Run(string(s.code), func(t *testing.T) {
			c, h := s.run(t)
			assertContract(t, string(s.code), c, s.code)
			line := h.awaitOutcome(t, "pandokos_error")
			if line["error_code"] != string(s.code) {
				t.Errorf("the request line: %v", line)
			}
		})
	}
	for _, name := range familycodes.All(t) {
		if !seen[name] {
			t.Errorf("the code %q is declared in family.go and has no scenario", name)
		}
	}

	t.Run("a provider error passes unchanged, never with X-Pandokos-Error", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.sendAuthed(t, route, chat(fakeprovider.ModelError429, false))
		if c.status != 429 || !bytes.Equal(c.body, fixtures.Read("error_429.json")) || c.header.Get("X-Pandokos-Error") != "" {
			t.Errorf("429: %d %s %v", c.status, c.body, c.header)
		}
		c = h.sendAuthed(t, route, chat(fakeprovider.ModelError500Echo, false))
		if c.status != 500 || c.header.Get("X-Pandokos-Error") != "" || !strings.Contains(string(c.body), "upstream failure echo") {
			t.Errorf("500: %d %s %v", c.status, c.body, c.header)
		}
		if h.up.Requests()[0].Header.Get("X-Pandokos-Error") != "" {
			t.Error("a caller header reached the provider")
		}
	})
}

// TestCredentialNeverReachesCaller is T-432.
func TestCredentialNeverReachesCaller(t *testing.T) {
	secrets := testutil.SecretForms(fixtures.CanaryProviderKey)
	t.Run("echoed headers do not reach the caller", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.sendAuthed(t, route, chat(fakeprovider.ModelEchoAuth, false))
		if c.status != 200 {
			t.Fatalf("status %d", c.status)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "Bearer "+fixtures.CanaryProviderKey {
			t.Fatalf("the provider did not receive the credential: %+v", reqs)
		}
		for _, n := range []string{"X-Echo-Authorization", "Www-Authenticate"} {
			if v := c.header.Values(n); len(v) != 0 {
				t.Errorf("%s reached the caller: %v", n, v)
			}
		}
		testutil.AssertAbsent(t, "the response", c.dump(), secrets...)
		testutil.AssertAbsent(t, "the logs", h.logs.String(), secrets...)
	})

	t.Run("a provider 401 that echoes the credential in its headers", func(t *testing.T) {
		h := newHarness(t, nil)
		h.up.Accept() // nothing is accepted: the fake answers 401 and echoes what it received
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		if c.status != 401 {
			t.Fatalf("status %d", c.status)
		}
		testutil.AssertAbsent(t, "the response", c.dump(), secrets...)
		testutil.AssertAbsent(t, "the logs", h.logs.String(), secrets...)
	})

	t.Run("every error of the contract", func(t *testing.T) {
		for _, s := range scenarios() {
			c, h := s.run(t)
			var leaks []string
			leaks = append(leaks, secrets...)
			leaks = append(leaks, "127.0.0.1", "localhost")
			if h.up != nil {
				u, _ := url.Parse(h.up.URL())
				leaks = append(leaks, u.Port())
			}
			testutil.AssertAbsent(t, string(s.code)+": the response", c.dump(), leaks...)
			testutil.AssertAbsent(t, string(s.code)+": the logs", h.logs.String(), secrets...)
		}
	})

	t.Run("a transport error that quotes the URL and the credential", func(t *testing.T) {
		up := fakeprovider.New(t)
		target := up.URL() + "/v1/chat/completions"
		// The private transport fails with the error of net/http: it names the URL and, here,
		// the credential.
		h := newHarnessWith(t, up, testutil.TestGuard(t), func(o *gateway.Options) {
			o.Private = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, &url.Error{Op: "Post", URL: target, Err: errors.New(fixtures.CanaryProviderKey)}
			})
		})
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		assertContract(t, "transport error", c, family.CodeUpstreamUnreachable)
		u, _ := url.Parse(up.URL())
		leaks := append(append([]string{}, secrets...), u.Host, u.Hostname(), u.Port())
		testutil.AssertAbsent(t, "the response", c.dump(), leaks...)
		testutil.AssertAbsent(t, "the logs", h.logs.String(), leaks...)
	})
}

// TestRedirectNotFollowed is T-433.
func TestRedirectNotFollowed(t *testing.T) {
	var other atomic.Int64
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		other.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(second.Close)

	check := func(t *testing.T, h *harness, code int) {
		t.Helper()
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		assertContract(t, fmt.Sprint(code), c, family.CodeUpstreamUnreachable)
		if v := c.header.Values("Location"); len(v) != 0 {
			t.Errorf("%d: Location reached the caller: %v", code, v)
		}
		testutil.AssertAbsent(t, "the response", c.dump(), second.URL, "/elsewhere")
		line := h.awaitOutcome(t, "pandokos_error")
		if line["upstream_status"] != float64(code) || line["upstream_error"] != "redirect" || line["error_code"] != "upstream_unreachable" {
			t.Errorf("%d: line %v", code, line)
		}
	}
	for _, code := range []int{301, 302, 307, 308} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			h := newRaw(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Location", second.URL+"/elsewhere")
				w.WriteHeader(code)
			})
			check(t, h, code)
		})
	}
	t.Run("the fake provider's fake-redirect", func(t *testing.T) {
		h := newHarness(t, nil)
		h.up.RedirectTo(second.URL + "/elsewhere")
		c := h.sendAuthed(t, route, chat(fakeprovider.ModelRedirect, false))
		assertContract(t, "fake-redirect", c, family.CodeUpstreamUnreachable)
		line := h.awaitOutcome(t, "pandokos_error")
		if line["upstream_status"] != float64(307) || line["upstream_error"] != "redirect" {
			t.Errorf("line %v", line)
		}
	})
	if n := other.Load(); n != 0 {
		t.Errorf("the redirect target received %d requests", n)
	}
}

// TestEgressClassFollowsEndpoint is T-434: a real guard decides by the endpoint's class.
func TestEgressClassFollowsEndpoint(t *testing.T) {
	check := func(t *testing.T, h *harness, up *fakeprovider.Server, reason string) {
		t.Helper()
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		assertContract(t, reason, c, family.CodeUpstreamUnreachable)
		u, _ := url.Parse(up.URL())
		testutil.AssertAbsent(t, "the response", c.dump(), u.Host, u.Hostname(), u.Port(), "localhost", "::1")
		if n := up.Connections(); n != 0 {
			t.Errorf("the listener accepted %d connections", n)
		}
		testutil.Eventually(t, 5*time.Second, "the egress blocked line", func() bool { return len(h.logs.Find(t, "egress blocked")) >= 1 })
		l := h.logs.Find(t, "egress blocked")[0]
		if l["level"] != "WARN" || l["reason"] != reason || l["provider"] != "openai" || l["endpoint"] == nil || len(fmt.Sprint(l["request_id"])) != 32 || (strings.HasSuffix(reason, "_address") && l["address"] == nil) {
			t.Errorf("the egress blocked line: %v", l)
		}
		line := h.awaitOutcome(t, "pandokos_error")
		if line["egress_reason"] != reason || line["upstream_error"] != "egress" || line["error_code"] != "upstream_unreachable" {
			t.Errorf("the request line: %v", line)
		}
		if _, ok := line["address"]; ok {
			t.Error("the address is on the request line")
		}
	}
	retarget := func(h *harness, host string, private bool, scheme string) {
		tgt := h.targets["openai/fake-basic"]
		u := *tgt.BaseURL
		if scheme != "" {
			u.Scheme = scheme
		}
		if host != "" {
			u.Host = host + ":" + u.Port()
		}
		tgt.BaseURL, tgt.Private = &u, private
		h.targets["openai/fake-basic"] = tgt
	}
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		t.Run("a public endpoint on "+host, func(t *testing.T) {
			up := fakeprovider.NewTLS(t)
			h := newHarnessWith(t, up, testutil.TestGuard(t, trusting(up)), nil)
			retarget(h, host, false, "")
			check(t, h, up, "private_address")
		})
	}
	t.Run("plain http on a public endpoint: the scheme is refused first", func(t *testing.T) {
		up := fakeprovider.New(t)
		h := newHarnessWith(t, up, testutil.TestGuard(t), nil)
		retarget(h, "", false, "")
		check(t, h, up, "scheme")
	})
	t.Run("the same target, marked private, is reached", func(t *testing.T) {
		up := fakeprovider.NewTLS(t)
		h := newHarnessWith(t, up, testutil.TestGuard(t, trusting(up)), nil)
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		if c.status != 200 || len(up.Requests()) != 1 {
			t.Fatalf("status %d, %d requests: %s", c.status, len(up.Requests()), c.body)
		}
	})
	t.Run("a forbidden address is refused even when private", func(t *testing.T) {
		h := newHarness(t, nil)
		tgt := h.targets["openai/fake-basic"]
		meta, _ := url.Parse("http://169.254.169.254")
		tgt.BaseURL, tgt.Private = meta, true
		h.targets["openai/fake-basic"] = tgt
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		assertContract(t, "metadata", c, family.CodeUpstreamUnreachable)
		testutil.AssertAbsent(t, "the response", c.dump(), "169.254")
		line := h.awaitOutcome(t, "pandokos_error")
		if line["egress_reason"] != "forbidden_address" {
			t.Errorf("the request line: %v", line)
		}
	})
}

// TestNoRetry is T-435: one attempt, never a replay.
func TestNoRetry(t *testing.T) {
	h := newHarness(t, nil)
	if c := h.sendAuthed(t, route, chat("fake-basic", false)); c.status != 200 {
		t.Fatalf("the warm-up: %d", c.status)
	}
	if n := h.up.Connections(); n != 1 {
		t.Fatalf("%d connections after the warm-up", n)
	}
	// The provider closes the reused connection without answering.
	hdr := authed()
	hdr.Set("Idempotency-Key", "synthetic-idempotency-key")
	c := h.send(t, http.MethodPost, route, chat(fakeprovider.ModelClose, false), hdr)
	assertContract(t, "fake-close", c, family.CodeUpstreamUnreachable)
	reqs := h.up.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the provider saw %d requests, want 2 (the warm-up and one attempt)", len(reqs))
	}
	if v := reqs[1].Header.Values("Idempotency-Key"); len(v) != 0 {
		t.Errorf("Idempotency-Key was forwarded: %v", v)
	}
	if n := h.up.Connections(); n != 1 {
		t.Errorf("%d connections: the request was sent again on a new one", n)
	}
	// Once more, without the header, on a connection that is warm again.
	h.sendAuthed(t, route, chat("fake-basic", false))
	before := len(h.up.Requests())
	c = h.sendAuthed(t, route, chat(fakeprovider.ModelClose, false))
	assertContract(t, "fake-close again", c, family.CodeUpstreamUnreachable)
	if n := len(h.up.Requests()) - before; n != 1 {
		t.Errorf("%d attempts, want exactly one", n)
	}
}

// readResponse reads the response a raw connection gets.
func readResponse(t *testing.T, conn net.Conn) *http.Response {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response within 3 s: %v", err)
	}
	return resp
}

// TestRequestTimeout is T-436: a stalled body gets 408, a vanished caller gets nothing.
func TestRequestTimeout(t *testing.T) {
	t.Run("headers and half a body, then silence", func(t *testing.T) {
		h := newHarness(t, func(o *gateway.Options) { o.RequestBodyReadTimeout = 200 * time.Millisecond })
		conn, err := net.Dial("tcp", h.srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"model\":", route, testKey)
		start := time.Now()
		resp := readResponse(t, conn)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		assertContract(t, "408", captured{status: resp.StatusCode, header: resp.Header, body: b}, family.CodeRequestTimeout)
		// net/http's client removes "Connection: close" from the header map and reports it as
		// resp.Close, so that is where a 408's Connection: close is observed.
		if !resp.Close {
			t.Errorf("a 408 must carry Connection: close: %v close=%v", resp.Header, resp.Close)
		}
		if d := time.Since(start); d < 150*time.Millisecond || d > 3*time.Second {
			t.Errorf("the answer came after %s with a timeout of 200ms", d)
		}
		if n := len(h.up.Requests()); n != 0 {
			t.Errorf("the provider was contacted %d times", n)
		}
		if n := h.cat.Resolves.Load(); n != 0 {
			t.Errorf("the model was resolved for a body that never arrived")
		}
		line := h.awaitOutcome(t, "pandokos_error")
		if line["error_code"] != "request_timeout" || line["status"] != float64(408) {
			t.Errorf("line: %v", line)
		}
	})
	t.Run("a caller that closes in the middle of the body", func(t *testing.T) {
		h := newHarness(t, nil)
		conn, err := net.Dial("tcp", h.srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"model\":", route, testKey)
		time.Sleep(50 * time.Millisecond)
		conn.Close()
		line := h.awaitOutcome(t, "client_error")
		if line["status"] != float64(0) {
			t.Errorf("a response was written to a caller that left: %v", line)
		}
		if n := len(h.up.Requests()); n != 0 {
			t.Errorf("the provider was contacted %d times", n)
		}
		if n := h.cat.Resolves.Load(); n != 0 {
			t.Error("the model was resolved for a body that was cut")
		}
	})
}

// TestAuthFailureRecorded is T-437.
func TestAuthFailureRecorded(t *testing.T) {
	const keyID, tenantID = "tabcdefghijklmnop", "0192f0f0-0000-7000-8000-000000000001"
	type tc struct {
		name     string
		err      error
		header   bool // send the Authorization header
		nilRec   bool
		answer   bool
		wantCall bool
	}
	cases := []tc{
		{"a failure with a tenant, recorded", reasoned{"revoked", keyID, tenantID}, true, false, true, true},
		{"a failure with a tenant, not recorded", reasoned{"expired", keyID, tenantID}, true, false, false, true},
		{"a failure without a tenant", reasoned{"unknown_key", "", ""}, true, false, true, false},
		{"a failure with a key and no tenant", reasoned{"wrong_secret", keyID, ""}, true, false, true, false},
		{"no recorder", reasoned{"revoked", keyID, tenantID}, true, true, true, false},
		{"a malformed header", authz.ErrUnauthenticated, false, false, true, false},
		{"an error that says nothing", authz.ErrUnauthenticated, true, false, true, false},
	}
	var first string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &failureRecorder{answer: c.answer}
			h := newHarness(t, func(o *gateway.Options) {
				o.Authenticator = fixedAuth{err: c.err}
				if !c.nilRec {
					o.Failures = rec
				}
			})
			hdr := authed()
			if !c.header {
				hdr = http.Header{}
			}
			r := h.send(t, http.MethodPost, route, chat("fake-basic", false), hdr)
			assertContract(t, c.name, r, family.CodeUnauthenticated)
			if first == "" {
				first = r.normalized()
			} else if got := r.normalized(); got != first {
				t.Errorf("the 401 differs between cases:\n%s\nvs\n%s", got, first)
			}
			calls := rec.recorded()
			if !c.wantCall {
				if len(calls) != 0 {
					t.Fatalf("the recorder was called: %+v", calls)
				}
				for _, l := range h.logs.Find(t, "authentication failed") {
					if _, ok := l["audited"]; ok {
						t.Errorf("audited is logged without a record: %v", l)
					}
				}
				return
			}
			if len(calls) != 1 {
				t.Fatalf("the recorder was called %d times", len(calls))
			}
			want := gateway.AuthFailure{RequestID: r.header.Get("X-Pandokos-Request-Id"), KeyID: keyID, TenantID: tenantID}
			var why reasoned
			if !errors.As(c.err, &why) {
				t.Fatal("not a reasoned failure")
			}
			want.Reason = why.reason
			if calls[0] != want {
				t.Errorf("recorded %+v, want %+v", calls[0], want)
			}
			lines := h.logs.Find(t, "authentication failed")
			if len(lines) != 1 || lines[0]["audited"] != c.answer {
				t.Errorf("the log lines: %v, want audited=%v", lines, c.answer)
			}
		})
	}
}

// requestFields returns the names of the fields of a log line.
func requestFields(line map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range line {
		out[k] = true
	}
	return out
}

func setOf(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func union(sets ...map[string]bool) map[string]bool {
	m := map[string]bool{}
	for _, s := range sets {
		for k := range s {
			m[k] = true
		}
	}
	return m
}

// TestRequestLogFields is T-438: the field set of section 5.9.
func TestRequestLogFields(t *testing.T) {
	base := setOf("time", "level", "msg", "request_id", "method", "route", "status", "outcome", "duration_ms")
	op := setOf("provider", "api_family", "operation")
	who := setOf("tenant_id", "service_account_id", "key_id")
	resolved := setOf("model", "model_id", "endpoint")
	credential := setOf("credential_id", "credential_pinned", "funding")
	response := setOf("stream", "usage_injected", "upstream_status", "ttfb_ms", "request_bytes", "response_bytes", "usage_source", "usage", "upstream_request_id")
	forbid := func(line map[string]any, names ...string) {
		t.Helper()
		for _, n := range names {
			if _, ok := line[n]; ok {
				t.Errorf("the field %s is logged: %v", n, line)
			}
		}
	}
	exactly := func(t *testing.T, line map[string]any, want map[string]bool) {
		t.Helper()
		got := requestFields(line)
		var missing, extra []string
		for k := range want {
			if !got[k] {
				missing = append(missing, k)
			}
		}
		for k := range got {
			if !want[k] {
				extra = append(extra, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("fields: missing %v, unexpected %v", missing, extra)
		}
	}
	within := func(t *testing.T, line map[string]any, required, allowed map[string]bool) {
		t.Helper()
		got := requestFields(line)
		for k := range required {
			if !got[k] {
				t.Errorf("the field %s is missing: %v", k, line)
			}
		}
		for k := range got {
			if !required[k] && !allowed[k] {
				t.Errorf("the field %s is not expected: %v", k, line)
			}
		}
	}

	t.Run("a success", func(t *testing.T) {
		h := newHarness(t, nil)
		h.sendAuthed(t, route, chat("fake-basic", false))
		line := h.lastRequestLine(t)
		exactly(t, line, union(base, op, who, resolved, credential, response))
		for k, v := range map[string]any{"api_family": "openai", "provider": "openai", "operation": "chat.completions", "model": "fake-basic",
			"endpoint": "synthetic-endpoint", "model_id": "synthetic-model-openai-fake-basic", "funding": "byok", "credential_pinned": false,
			"upstream_request_id": "req_fake_0001", "route": "POST /{provider}/v1/chat/completions", "outcome": "ok"} {
			if line[k] != v {
				t.Errorf("%s = %v, want %v", k, line[k], v)
			}
		}
		forbid(line, "invalid_reason", "upstream_error", "egress_reason", "error_code")
	})
	t.Run("a stream", func(t *testing.T) {
		h := newHarness(t, nil)
		h.sendAuthed(t, route, chat("fake-basic", true))
		line := h.lastRequestLine(t)
		exactly(t, line, union(base, op, who, resolved, credential, response))
		if line["stream"] != true || line["usage_injected"] != true {
			t.Errorf("line: %v", line)
		}
	})
	t.Run("a provider error", func(t *testing.T) {
		h := newHarness(t, nil)
		h.sendAuthed(t, route, chat(fakeprovider.ModelError429, false))
		// A provider error carries no usage: usage_source says none and there is no usage group.
		withoutUsage := union(base, op, who, resolved, credential, response)
		delete(withoutUsage, "usage")
		exactly(t, h.lastRequestLine(t), withoutUsage)
	})

	refusal := func(name string, mutate func(*gateway.Options), do func(t *testing.T, h *harness), required, allowed map[string]bool, absent ...string) {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, mutate)
			do(t, h)
			line := h.awaitOutcome(t, "pandokos_error")
			within(t, line, union(required, setOf("error_code")), allowed)
			forbid(line, absent...)
			testutil.AssertNoCanary(t, "the logs", h.logs.String())
			testutil.AssertAbsent(t, "the logs", h.logs.String(), testutil.KeyForms(testKey)...)
		})
	}
	active := testutil.InferencePrincipal()
	noRole := active
	noRole.Roles = nil
	post := func(model string) func(t *testing.T, h *harness) {
		return func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat(model, false)) }
	}
	afterBody := setOf("request_bytes", "stream", "usage_injected")
	refusal("step 2: not served", nil, func(t *testing.T, h *harness) { h.send(t, http.MethodGet, "/nope/v1/x", nil, authed()) },
		base, nil, "provider", "api_family", "operation", "tenant_id", "key_id", "model", "credential_id", "upstream_status")
	refusal("step 3: unauthenticated", func(o *gateway.Options) {
		o.Authenticator = fixedAuth{err: reasoned{"wrong_secret", "tabcdefghijklmnop", "0192f0f0-0000-7000-8000-000000000001"}}
	}, post("fake-basic"), union(base, op, setOf("key_id")), nil, "tenant_id", "service_account_id", "model", "credential_id", "upstream_status", "request_bytes")
	refusal("step 4: forbidden", func(o *gateway.Options) { o.Authenticator = fixedAuth{p: noRole} }, post("fake-basic"),
		union(base, op, who), nil, "model", "credential_id", "upstream_status", "request_bytes")
	refusal("step 5: too large", func(o *gateway.Options) { o.MaxRequestBodyBytes = 10 }, post("fake-basic"),
		union(base, op, who), setOf("request_bytes"), "model", "credential_id", "upstream_status")
	refusal("step 6: invalid", nil, func(t *testing.T, h *harness) { h.sendAuthed(t, route, []byte(`{}`)) },
		union(base, op, who, setOf("invalid_reason", "request_bytes")), setOf("stream", "usage_injected"), "model", "model_id", "credential_id", "upstream_status")
	refusal("step 7: model not allowed", nil, post("nope"), union(base, op, who, setOf("request_bytes")), afterBody,
		"model", "model_id", "endpoint", "credential_id", "credential_pinned", "funding", "upstream_status")
	refusal("step 8: no credential", func(o *gateway.Options) { o.Credentials = testutil.StaticCredentials(nil) }, post("fake-basic"),
		union(base, op, who, resolved, setOf("request_bytes")), afterBody, "credential_id", "credential_pinned", "funding", "upstream_status")
	refusal("step 9: the provider cannot be reached", nil, func(t *testing.T, h *harness) {
		closedPortTarget(h)
		h.sendAuthed(t, route, chat("fake-basic", false))
	}, union(base, op, who, resolved, credential, setOf("request_bytes", "upstream_error")), afterBody, "upstream_status")
}

// TestRequestLogIsNotLeaky checks that nothing of the caller's content is in the log of a good call.
func TestRequestLogIsNotLeaky(t *testing.T) {
	h := newHarness(t, nil)
	h.sendAuthed(t, route+"?q=query-value", chat("fake-basic", true))
	testutil.AssertNoCanary(t, "the logs", h.logs.String())
	testutil.AssertAbsent(t, "the logs", h.logs.String(), append(testutil.KeyForms(testKey), "query-value")...)
}

// TestCancelledBeforeForwarding: a request whose caller is gone (or whose server is being forced
// down) before the forward is not forwarded and gets no answer: the handler aborts the
// connection instead of letting net/http send an empty 200.
func TestCancelledBeforeForwarding(t *testing.T) {
	for name, tc := range map[string]struct {
		block   bool
		cause   error
		outcome string
	}{
		"in resolution, forced shutdown": {true, gateway.ErrShutdown, "shutdown_aborted"},
		"in resolution, caller gone":     {true, nil, "client_cancelled"},
		"before the forward, shutdown":   {false, gateway.ErrShutdown, "shutdown_aborted"},
		"before the forward, caller":     {false, nil, "client_cancelled"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			if tc.block {
				h.cat.Block = make(chan struct{})
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(tc.cause)
			req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(chat("fake-basic", false))).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+testKey)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			var got any
			func() {
				defer func() { got = recover() }()
				h.handler.ServeHTTP(rec, req)
			}()
			if got != http.ErrAbortHandler {
				t.Errorf("the handler ended with %v (status %d), want a panic of http.ErrAbortHandler", got, rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("the caller got %q", rec.Body.String())
			}
			h.awaitOutcome(t, tc.outcome)
			if n := len(h.up.Requests()); n != 0 {
				t.Errorf("the provider received %d requests", n)
			}
		})
	}
}
