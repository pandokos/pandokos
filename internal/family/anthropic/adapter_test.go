package anthropic_test

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
)

func opNamed(t testing.TB, a family.Adapter, name string) family.Operation {
	t.Helper()
	for _, op := range a.Operations() {
		if op.Name == name {
			return op
		}
	}
	t.Fatalf("the adapter has no operation %q", name)
	return family.Operation{}
}

// TestOperations is T-517: two operations, Match for the exact method and path only, NoUsage on
// token counting only.
func TestOperations(t *testing.T) {
	a := anthropic.Adapter()
	if a.Name() != "anthropic" || anthropic.Name != "anthropic" {
		t.Errorf("name %q", a.Name())
	}
	want := map[string]struct {
		path    string
		noUsage bool
	}{
		"messages":              {"/v1/messages", false},
		"messages.count_tokens": {"/v1/messages/count_tokens", true},
	}
	ops := a.Operations()
	if len(ops) != len(want) {
		t.Fatalf("%d operations: %+v", len(ops), ops)
	}
	for _, op := range ops {
		w, ok := want[op.Name]
		if !ok {
			t.Errorf("unknown operation %q", op.Name)
			continue
		}
		if op.Method != "POST" || op.Path != w.path || op.NoUsage != w.noUsage {
			t.Errorf("%s: %+v, want POST %s NoUsage %v", op.Name, op, w.path, w.noUsage)
		}
		got, ok := a.Match("POST", w.path)
		if !ok || got != op {
			t.Errorf("Match(POST, %s) = %+v %v", w.path, got, ok)
		}
		if name, ok := anthropic.Match("POST", w.path); !ok || name != op.Name {
			t.Errorf("anthropic.Match(POST, %s) = %q %v", w.path, name, ok)
		}
		for _, bad := range []struct{ method, path string }{
			{"GET", w.path}, {"PUT", w.path}, {"post", w.path}, {"DELETE", w.path}, {"POST", w.path + "/"}, {"POST", "/" + w.path},
			{"POST", strings.ToUpper(w.path)}, {"POST", w.path + "?x=1"}, {"POST", "/anthropic" + w.path},
			{"POST", strings.TrimPrefix(w.path, "/v1")}, {"POST", w.path + "/batches"},
		} {
			if got, ok := a.Match(bad.method, bad.path); ok {
				t.Errorf("%s %s matched %+v", bad.method, bad.path, got)
			}
			if _, ok := anthropic.Match(bad.method, bad.path); ok {
				t.Errorf("anthropic.Match(%s, %s) matched", bad.method, bad.path)
			}
		}
	}
	for _, p := range []string{"/v1/models", "/v1/messages/batches", "/v1/complete", "/v1/chat/completions", "/"} {
		if _, ok := a.Match("POST", p); ok {
			t.Errorf("POST %s matched", p)
		}
	}
	for _, c := range []struct{ got, want string }{
		{anthropic.OperationMessages, "messages"}, {anthropic.OperationCountTokens, "messages.count_tokens"},
		{anthropic.PathMessages, "/v1/messages"}, {anthropic.PathCountTokens, "/v1/messages/count_tokens"},
		{anthropic.HeaderVersion, "Anthropic-Version"},
	} {
		if c.got != c.want {
			t.Errorf("constant %q, want %q", c.got, c.want)
		}
	}
	t.Run("Operations returns a copy", func(t *testing.T) {
		ops := a.Operations()
		ops[0].Name = "changed"
		if a.Operations()[0].Name == "changed" {
			t.Error("the adapter's list was changed through the returned slice")
		}
	})
}

// TestInboundCredential is T-518: each row of the table of section 5.2, the scheme's case,
// white space around values, and empty values.
func TestInboundCredential(t *testing.T) {
	type row struct {
		name     string
		apiKey   []string // values of X-Api-Key; nil means absent
		auth     []string
		want     string
		accepted bool
	}
	rows := []row{
		{"x-api-key alone", []string{"k"}, nil, "k", true},
		{"bearer alone", nil, []string{"Bearer v"}, "v", true},
		{"empty x-api-key and a bearer", []string{""}, []string{"Bearer v"}, "v", true},
		{"both, the same value", []string{"k"}, []string{"Bearer k"}, "k", true},
		{"x-api-key and an empty authorization", []string{"k"}, []string{""}, "k", true},
		{"both, another value", []string{"k"}, []string{"Bearer other"}, "", false},
		{"x-api-key and another scheme", []string{"k"}, []string{"Basic abc"}, "", false},
		{"x-api-key and a bare scheme", []string{"k"}, []string{"Bearer"}, "", false},
		{"x-api-key and a bearer without a value", []string{"k"}, []string{"Bearer "}, "", false},
		{"neither", nil, nil, "", false},
		{"both empty", []string{""}, []string{""}, "", false},
		{"empty x-api-key and another scheme", []string{""}, []string{"Basic abc"}, "", false},
		{"only a bare scheme", nil, []string{"Bearer"}, "", false},
		{"only a bearer without a value", nil, []string{"Bearer "}, "", false},
		{"only another scheme", nil, []string{"Token v"}, "", false},
		{"only a value without a scheme", nil, []string{"v"}, "", false},
		{"scheme in lower case", nil, []string{"bearer v"}, "v", true},
		{"scheme in upper case", nil, []string{"BEARER v"}, "v", true},
		{"scheme in mixed case, same value as x-api-key", []string{"k"}, []string{"bEaReR k"}, "k", true},
		{"a value with dots and dashes", nil, []string{"Bearer abc.def-ghi_jkl"}, "abc.def-ghi_jkl", true},
		{"a scheme glued to the value", nil, []string{"Bearerv"}, "", false},
		// White space around a value is not part of it (HTTP optional white space); a value of
		// white space only is empty.
		{"x-api-key with surrounding white space", []string{"  k\t"}, nil, "k", true},
		{"bearer with surrounding white space", nil, []string{"Bearer   v  "}, "v", true},
		{"x-api-key of white space only", []string{"   "}, nil, "", false},
		{"white space only and a bearer", []string{" \t "}, []string{"Bearer v"}, "v", true},
		{"bearer of white space only", nil, []string{"Bearer    "}, "", false},
		{"same value, white space differs", []string{" k "}, []string{"Bearer k"}, "k", true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			h := http.Header{}
			if r.apiKey != nil {
				h["X-Api-Key"] = r.apiKey
			}
			if r.auth != nil {
				h["Authorization"] = r.auth
			}
			got, ok := anthropic.InboundCredential(h)
			if ok != r.accepted || got != r.want {
				t.Fatalf("got %q %v, want %q %v", got, ok, r.want, r.accepted)
			}
			a, aok := anthropic.Adapter().InboundCredential(h)
			if a != got || aok != ok {
				t.Errorf("the adapter answers %q %v", a, aok)
			}
		})
	}
	t.Run("a refused credential is never returned", func(t *testing.T) {
		got, ok := anthropic.InboundCredential(http.Header{"X-Api-Key": {"k"}, "Authorization": {"Bearer other"}})
		if ok || got != "" {
			t.Errorf("%q %v", got, ok)
		}
	})
}

// TestHeaders is T-519: the lists of section 5.2 and the outbound credential.
func TestHeaders(t *testing.T) {
	t.Run("RequestHeaders exactly", func(t *testing.T) {
		want := []string{"Accept", "Anthropic-Beta", "Anthropic-Version", "Content-Type", "User-Agent"}
		got := append([]string(nil), anthropic.RequestHeaders...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("RequestHeaders %v, want %v", got, want)
		}
		got2 := append([]string(nil), anthropic.Adapter().RequestHeaders()...)
		sort.Strings(got2)
		if !reflect.DeepEqual(got2, want) {
			t.Errorf("the adapter's RequestHeaders %v", got2)
		}
		for _, n := range anthropic.RequestHeaders {
			if http.CanonicalHeaderKey(n) != n {
				t.Errorf("%q is not in canonical form", n)
			}
		}
	})
	t.Run("AttachCredential sets X-Api-Key only", func(t *testing.T) {
		h := http.Header{}
		anthropic.AttachCredential(h, "k")
		if len(h) != 1 || len(h.Values("X-Api-Key")) != 1 || h.Get("X-Api-Key") != "k" {
			t.Errorf("header %v", h)
		}
		if h.Get("Authorization") != "" {
			t.Error("Authorization was set")
		}
		h2 := http.Header{"X-Api-Key": {"old", "older"}}
		anthropic.AttachCredential(h2, "k")
		if got := h2.Values("X-Api-Key"); len(got) != 1 || got[0] != "k" {
			t.Errorf("replaced header %v", got)
		}
		h3 := http.Header{}
		anthropic.Adapter().AttachCredential(h3, "k")
		if h3.Get("X-Api-Key") != "k" || h3.Get("Authorization") != "" {
			t.Errorf("the adapter set %v", h3)
		}
	})
	t.Run("AllowResponseHeader", func(t *testing.T) {
		allowed := []string{
			"Content-Type", "Content-Length", "Retry-After", "Request-Id", "X-Should-Retry",
			"Anthropic-Ratelimit-Requests-Remaining", "Anthropic-Ratelimit-Input-Tokens-Reset", "Anthropic-Organization-Id", "Anthropic-Version", "Anthropic-X",
		}
		refused := []string{
			"Set-Cookie", "X-Api-Key", "Authorization", "Via", "Server", "X-Pandokos-Error", "X-Pandokos-Request-Id", "Content-Encoding",
			"Transfer-Encoding", "Connection", "Date", "Cf-Ray", "Alt-Svc", "Location", "Www-Authenticate",
			"Anthropic", "X-Anthropic-Foo", "Openai-Version", "X-Request-Id", "X-Ratelimit-Remaining-Requests", "Request-Id-2",
		}
		for _, n := range allowed {
			if !anthropic.AllowResponseHeader(n) || !anthropic.Adapter().AllowResponseHeader(n) {
				t.Errorf("%s refused", n)
			}
		}
		for _, n := range refused {
			if anthropic.AllowResponseHeader(n) || anthropic.Adapter().AllowResponseHeader(n) {
				t.Errorf("%s allowed", n)
			}
		}
	})
	t.Run("UpstreamRequestID reads Request-Id", func(t *testing.T) {
		a := anthropic.Adapter()
		if got := a.UpstreamRequestID(http.Header{"Request-Id": {"req_synthetic-1"}}); got != "req_synthetic-1" {
			t.Errorf("got %q", got)
		}
		if got := a.UpstreamRequestID(http.Header{"X-Request-Id": {"req_x"}}); got != "" {
			t.Errorf("X-Request-Id was read: %q", got)
		}
		if got := a.UpstreamRequestID(http.Header{}); got != "" {
			t.Errorf("got %q for no header", got)
		}
		if got := a.UpstreamRequestID(http.Header{"Request-Id": {"  odd value\n"}}); got != "  odd value\n" {
			t.Errorf("the value is returned unchecked, got %q", got)
		}
	})
	t.Run("Recognizes", func(t *testing.T) {
		a := anthropic.Adapter()
		for _, c := range []struct {
			name, method, path string
			h                  http.Header
			want               bool
		}{
			{"messages", "POST", "/v1/messages", nil, true},
			{"count tokens", "POST", "/v1/messages/count_tokens", nil, true},
			{"a method that does not match", "GET", "/v1/messages", nil, false},
			{"another path without the version header", "POST", "/v1/chat/completions", nil, false},
			{"the version header on another path", "POST", "/v1/anything", http.Header{"Anthropic-Version": {"2026-10-21"}}, true},
			{"the version header on another method", "GET", "/x", http.Header{"Anthropic-Version": {"2026-10-21"}}, true},
			{"an empty version header", "POST", "/v1/anything", http.Header{"Anthropic-Version": {""}}, false},
			{"the whole path", "POST", "/anthropic/v1/messages", nil, false},
			{"no path", "POST", "", nil, false},
			{"the beta header alone", "POST", "/v1/x", http.Header{"Anthropic-Beta": {"x"}}, false},
		} {
			if got := a.Recognizes(c.method, c.path, c.h); got != c.want {
				t.Errorf("%s: %v, want %v", c.name, got, c.want)
			}
		}
	})
}

// TestAdapterDelegates: the adapter's methods follow the operation they get.
func TestAdapterDelegates(t *testing.T) {
	a := anthropic.Adapter()
	msgs, count := opNamed(t, a, "messages"), opNamed(t, a, "messages.count_tokens")
	r, err := a.ParseRequest(msgs, []byte(`{"model":"m","stream":true}`))
	if err != nil || r.Model() != "m" || !r.Stream() {
		t.Errorf("messages: %v", err)
	}
	r, err = a.ParseRequest(count, []byte(`{"model":"m","stream":true}`))
	if err != nil || r.Stream() {
		t.Errorf("counting: %v stream=%v", err, err == nil && r.Stream())
	}
	if _, err := a.ParseRequest(msgs, []byte(`[]`)); family.InvalidReason(err) != "not_object" {
		t.Errorf("a body that is not an object: %v", err)
	}
	u, ok, err := a.ReadUsage(msgs, []byte(`{"usage":{"input_tokens":3,"output_tokens":4}}`))
	if err != nil || !ok || u.InputTokens != 3 || u.OutputTokens != 4 {
		t.Errorf("messages usage: %+v %v %v", u, ok, err)
	}
	if _, ok, _ := a.ReadUsage(count, []byte(`{"input_tokens":42}`)); ok {
		t.Error("counting reported usage")
	}
	if got := a.EstimateUsage(count, family.Evidence{RequestBytes: 400, ResponseBytes: 400}); got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("counting estimate: %+v", got)
	}
	for _, strip := range []bool{false, true} {
		sr := a.NewStreamReader(msgs, strip)
		if sr == nil {
			t.Fatal("no stream reader")
		}
		in := []byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")
		if got := sr.Filter(in); string(got) != string(in) {
			t.Errorf("strip=%v: the reader changed the stream", strip)
		}
	}
}
