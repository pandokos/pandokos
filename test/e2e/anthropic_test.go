package e2e

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	anthropicPath = "/anthropic/v1/messages"
	versionHeader = "2026-10-21"
)

// messagesBody is a Messages API request whose prompt is a canary.
func messagesBody(model string, stream bool) []byte {
	s := `{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"` + fixtures.CanaryPrompt + `"}]`
	if stream {
		s += `,"stream":true`
	}
	return []byte(s + "}")
}

// anthropicHeader authenticates as the SDKs and curl do: bearer, or x-api-key.
func anthropicHeader(key string, bearer bool) http.Header {
	h := http.Header{"Content-Type": {"application/json"}, "Anthropic-Version": {versionHeader}}
	if bearer {
		h.Set("Authorization", "Bearer "+key)
	} else {
		h.Set("X-Api-Key", key)
	}
	return h
}

// assertAnthropicPandokosError checks a response raised by Pandokos on an anthropic route.
func assertAnthropicPandokosError(t *testing.T, what string, status int, text string, wantStatus int, code, errType string) {
	t.Helper()
	if status != wantStatus {
		t.Errorf("%s: status %d, want %d\n%s", what, status, wantStatus, text)
	}
	for _, want := range []string{"X-Pandokos-Error: " + code, "X-Pandokos-Request-Id: ", `"type":"error"`, `"type":"` + errType + `"`} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: the response lacks %q\n%s", what, want, text)
		}
	}
	if strings.Contains(text, `"pandokos_error"`) {
		t.Errorf("%s: the OpenAI shape on an anthropic route\n%s", what, text)
	}
}

// useAnthropicFake points the seeded provider anthropic at the fake, with the models of the
// tests, and returns nothing: every later call of the test goes through /anthropic.
func (p *proc) useAnthropicFake(t *testing.T, boot string, up *fakeprovider.Server, models ...string) {
	t.Helper()
	p.useFakeProvider(t, boot, "anthropic", up)
	for _, m := range models {
		p.mustCLI(t, boot, "model", "create", "--availability", "all", "anthropic/"+m)
	}
}

// capabilityOf runs `capability check` and decodes its answer.
func (p *proc) capabilityOf(t *testing.T, adminFile, tenantID, provider, model string) adminapi.Capability {
	t.Helper()
	out := p.mustCLI(t, adminFile, "capability", "check", "-o", "json", "--tenant", tenantID, "--provider", provider, "--model", model)
	var c adminapi.Capability
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("capability check: %v\n%s", err, out)
	}
	return c
}

// usageList runs `usage list` and decodes its items.
func (p *proc) usageList(t *testing.T, adminFile, tenantID string, extra ...string) []adminapi.UsageRecord {
	t.Helper()
	args := append([]string{"usage", "list", "-o", "json", "--all", "--tenant", tenantID}, extra...)
	out := p.mustCLI(t, adminFile, args...)
	var doc struct {
		Items []adminapi.UsageRecord `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("usage list: %v\n%s", err, out)
	}
	return doc.Items
}

// TestAnthropicEndToEnd is T-552: the binary, the database and the fake as the Anthropic provider.
func TestAnthropicEndToEnd(t *testing.T) {
	t.Parallel()
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	p.useAnthropicFake(t, boot, up, fakeprovider.ModelBasic, fakeprovider.ModelCache)
	p.upReady = true
	p.mustCLI(t, boot, "price", "override", "--currency", "USD", "--rate", "input=3", "--rate", "output=6", "anthropic/fake-basic")

	const secret = "synthetic-credential-anthropic-0301"
	up.Accept(secret)
	id, admin := p.newTenant(t, boot, "anthropic-tenant")
	_, key := p.newInferenceKey(t, admin, id)
	p.addCredential(t, admin, "anthropic", secret)

	t.Run("a streamed call with X-Api-Key and one with a bearer token", func(t *testing.T) {
		before := len(up.Requests())
		for _, bearer := range []bool{false, true} {
			status, text := exchangeWith(t, p.client, "POST", p.url(anthropicPath), anthropicHeader(key, bearer), messagesBody(fakeprovider.ModelBasic, true))
			if status != 200 || !strings.HasSuffix(text, string(fixtures.ReadAnthropic("message_stream.sse"))) {
				t.Fatalf("bearer=%v: %d\n%s", bearer, status, text)
			}
		}
		reqs := up.Requests()[before:]
		if len(reqs) != 2 {
			t.Fatalf("the provider saw %d requests", len(reqs))
		}
		for _, r := range reqs {
			if r.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != secret || r.Header.Get("Authorization") != "" || r.Header.Get("Anthropic-Version") != versionHeader {
				t.Errorf("the provider saw %s with X-Api-Key %q Authorization %q version %q", r.Path, r.Header.Get("X-Api-Key"), r.Header.Get("Authorization"), r.Header.Get("Anthropic-Version"))
			}
			testutil.AssertAbsent(t, "a header the provider received", fmt.Sprint(r.Header), testutil.KeyForms(key)...)
			if strings.Contains(string(r.Body), "stream_options") {
				t.Errorf("a stream option reached the provider: %s", r.Body)
			}
		}
	})

	t.Run("one row each, with provider input and output and a cost", func(t *testing.T) {
		rows := waitRows(t, p.db, id, 2)
		for i, r := range rows {
			if r["provider"] != "anthropic" || r["api_family"] != "anthropic" || r["operation"] != "messages" || r["outcome"] != "ok" || r["stream"] != true {
				t.Errorf("row %d: %v", i, r)
			}
			if r["input_source"] != "provider" || r["output_source"] != "provider" || num(r["input_tokens"]) != 12 || num(r["output_tokens"]) != 7 {
				t.Errorf("row %d: sources and tokens: %v", i, r)
			}
			if r["cost_status"] != "known" || math.Abs(num(r["cost"])-0.000078) > 1e-12 || r["currency"] != "USD" {
				t.Errorf("row %d: cost %v %v %v", i, r["cost_status"], r["cost"], r["currency"])
			}
			if r["estimated_input_tokens"] != nil || r["estimated_output_tokens"] != nil {
				t.Errorf("row %d: an estimate beside the provider's figures", i)
			}
		}
	})

	t.Run("a call with cache usage, and token counting without usage", func(t *testing.T) {
		if status, text := exchangeWith(t, p.client, "POST", p.url(anthropicPath), anthropicHeader(key, true), messagesBody(fakeprovider.ModelCache, false)); status != 200 {
			t.Fatalf("%d\n%s", status, text)
		}
		if status, text := exchangeWith(t, p.client, "POST", p.url(anthropicPath+"/count_tokens"), anthropicHeader(key, true), messagesBody(fakeprovider.ModelBasic, false)); status != 200 || !strings.HasSuffix(text, `{"input_tokens":42}`+"\n") {
			t.Fatalf("count_tokens: %d\n%s", status, text)
		}
		rows := waitRows(t, p.db, id, 4)
		cache, counting := rows[2], rows[3]
		if num(cache["input_tokens"]) != 2310 || num(cache["cache_read_tokens"]) != 2000 || num(cache["cache_write_tokens"]) != 300 || num(cache["output_tokens"]) != 50 {
			t.Errorf("the cache row: %v", cache)
		}
		if units, _ := cache["other_units"].(map[string]any); num(units["cache_write_1h_tokens"]) != 200 || num(units["web_search_requests"]) != 2 {
			t.Errorf("other_units of the cache row: %v", cache["other_units"])
		}
		if counting["operation"] != "messages.count_tokens" || counting["input_source"] != "none" || counting["output_source"] != "none" || counting["cost_status"] != "none" {
			t.Errorf("the counting row: %v", counting)
		}
	})

	t.Run("usage list shows them", func(t *testing.T) {
		items := p.usageList(t, admin, id, "--provider", "anthropic")
		if len(items) != 4 {
			t.Fatalf("%d items", len(items))
		}
		streamed := 0
		for _, it := range items {
			if it.Provider != "anthropic" || it.ApiFamily != "anthropic" {
				t.Errorf("item %+v", it)
			}
			if it.Operation == "messages" && it.Stream && it.InputSource == "provider" && it.OutputSource == "provider" && it.InputTokens == 12 && it.OutputTokens == 7 && it.CostStatus == "known" {
				streamed++
			}
		}
		if streamed != 2 {
			t.Errorf("%d streamed items with provider figures, want 2: %+v", streamed, items)
		}
		if other := p.usageList(t, admin, id, "--provider", "openai"); len(other) != 0 {
			t.Errorf("%d items for another provider", len(other))
		}
	})

	t.Run("usage list --all pages over the default window", func(t *testing.T) {
		// No --from and no --to: every page must keep the window of the first one.
		items := p.usageList(t, admin, id, "--limit", "1")
		if len(items) < 4 {
			t.Fatalf("%d items over one-record pages, want at least 4", len(items))
		}
	})

	t.Run("capability check says callable", func(t *testing.T) {
		got := p.capabilityOf(t, admin, id, "anthropic", fakeprovider.ModelBasic)
		if !got.Callable || len(got.Reasons) != 0 || got.Checks.Credential != adminapi.CredentialAvailabilityActive {
			t.Errorf("%+v", got)
		}
		if out := p.mustCLI(t, admin, "capability", "check", "--tenant", id, "--provider", "anthropic", "--model", "no-such-model"); !strings.Contains(out, "model_not_allowed") {
			t.Errorf("an unknown model:\n%s", out)
		}
	})

	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	for _, o := range []struct{ what, text string }{{"stdout", p.stdout.String()}, {"stderr", p.stderr.String()}} {
		testutil.AssertNoCanary(t, o.what, o.text)
		testutil.AssertAbsent(t, o.what, o.text, append(testutil.KeyForms(key), testutil.SecretForms(secret)...)...)
	}
}

// TestUnsupportedAnthropicPaths is T-554: the paths of the Messages API that are not served are
// 404 endpoint_not_supported in the Anthropic shape, and an unauthenticated caller leaves no record.
func TestUnsupportedAnthropicPaths(t *testing.T) {
	t.Parallel()
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	p.useAnthropicFake(t, boot, up, fakeprovider.ModelBasic)
	p.upReady = true
	const secret = "synthetic-credential-anthropic-0302"
	up.Accept(secret)
	id, admin := p.newTenant(t, boot, "paths-tenant")
	_, key := p.newInferenceKey(t, admin, id)
	p.addCredential(t, admin, "anthropic", secret)
	body := messagesBody(fakeprovider.ModelBasic, false)

	for _, c := range []struct {
		what, method, path string
	}{
		{"batches", "POST", "/anthropic/v1/messages/batches"},
		{"the model list", "POST", "/anthropic/v1/models"},
		{"the model list by GET", "GET", "/anthropic/v1/models"},
		{"messages by GET", "GET", "/anthropic/v1/messages"},
		{"a trailing slash", "POST", "/anthropic/v1/messages/"},
		{"the legacy completion path", "POST", "/anthropic/v1/complete"},
		{"an openai path on the anthropic provider", "POST", "/anthropic/v1/chat/completions"},
	} {
		for _, auth := range []struct {
			name string
			hdr  http.Header
		}{{"unauthenticated", http.Header{"Content-Type": {"application/json"}, "Anthropic-Version": {versionHeader}}}, {"authenticated", anthropicHeader(key, true)}} {
			status, text := exchangeWith(t, p.client, c.method, p.url(c.path), auth.hdr, body)
			assertAnthropicPandokosError(t, c.what+", "+auth.name, status, text, 404, "endpoint_not_supported", "not_found_error")
		}
	}
	// The same unauthenticated request to an unknown provider: the Anthropic shape too, by its path.
	status, text := exchangeWith(t, p.client, "POST", p.url("/nope/v1/messages"), http.Header{"Content-Type": {"application/json"}}, body)
	assertAnthropicPandokosError(t, "an unknown provider on the messages path", status, text, 404, "endpoint_not_supported", "not_found_error")
	status, text = exchangeWith(t, p.client, "POST", p.url("/nope/v1/chat/completions"), http.Header{"Content-Type": {"application/json"}}, body)
	assertPandokosError(t, "an unknown provider on an openai path", status, text, 404, "endpoint_not_supported")

	if n := len(up.Requests()); n != 1 && n != 0 { // the credential check of createCredential is not a call
		for _, r := range up.Requests() {
			if r.Path != "/v1/models" {
				t.Errorf("the provider saw %s %s", r.Method, r.Path)
			}
		}
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if n := countRows(t, p.db); n != 0 {
		t.Errorf("%d usage rows: a request that reached no route must leave none", n)
	}
	testutil.AssertNoCanary(t, "stdout", p.stdout.String())
}
