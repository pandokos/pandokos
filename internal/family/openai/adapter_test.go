package openai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/test/familycodes"
)

// TestAdapter is T-424: the adapter of the openai family.
func TestAdapter(t *testing.T) {
	a := Adapter()

	t.Run("name and operations", func(t *testing.T) {
		if a.Name() != "openai" {
			t.Errorf("Name = %q", a.Name())
		}
		want := []family.Operation{{Name: "chat.completions", Method: "POST", Path: "/v1/chat/completions"}}
		if got := a.Operations(); !reflect.DeepEqual(got, want) {
			t.Errorf("Operations = %+v, want %+v", got, want)
		}
		ops := a.Operations()
		ops[0].Path = "/changed"
		if a.Operations()[0].Path != "/v1/chat/completions" {
			t.Error("Operations returns the adapter's own slice")
		}
	})

	t.Run("Match accepts native paths only", func(t *testing.T) {
		op, ok := a.Match("POST", "/v1/chat/completions")
		if !ok || op.Name != "chat.completions" || op.Path != "/v1/chat/completions" || op.Method != "POST" {
			t.Fatalf("got %+v %v", op, ok)
		}
		for _, c := range []struct{ method, path string }{
			{"POST", "/chat/completions"},
			{"POST", "/v1/chat/completions/"},
			{"POST", "//v1/chat/completions"},
			{"GET", "/v1/chat/completions"},
			{"post", "/v1/chat/completions"},
			{"POST", "/v1/embeddings"},
			{"POST", "/v1/responses"},
			{"POST", "/v1"},
			{"POST", "/"},
			{"POST", ""},
			{"POST", "/openai/v1/chat/completions"},
		} {
			if op, ok := a.Match(c.method, c.path); ok {
				t.Errorf("%s %s matched %+v", c.method, c.path, op)
			}
		}
	})

	t.Run("credential in and out", func(t *testing.T) {
		if v, ok := a.InboundCredential(http.Header{"Authorization": {"Bearer synthetic-key"}}); !ok || v != "synthetic-key" {
			t.Errorf("InboundCredential = %q %v", v, ok)
		}
		if _, ok := a.InboundCredential(http.Header{"X-Api-Key": {"synthetic-key"}}); ok {
			t.Error("the openai family accepts x-api-key")
		}
		h := http.Header{"Authorization": {"old"}, "X-Api-Key": {"old"}}
		a.AttachCredential(h, "synthetic-secret")
		if got := h.Values("Authorization"); len(got) != 1 || got[0] != "Bearer synthetic-secret" {
			t.Errorf("Authorization = %v", got)
		}
	})

	t.Run("header lists of section 5.3", func(t *testing.T) {
		want := []string{"Accept", "Content-Type", "User-Agent", "OpenAI-Organization", "OpenAI-Project", "OpenAI-Beta"}
		var got, canon []string
		for _, n := range a.RequestHeaders() {
			got = append(got, http.CanonicalHeaderKey(n))
			canon = append(canon, n)
		}
		var wantCanon []string
		for _, n := range want {
			wantCanon = append(wantCanon, http.CanonicalHeaderKey(n))
		}
		sort.Strings(got)
		sort.Strings(wantCanon)
		if !reflect.DeepEqual(got, wantCanon) {
			t.Errorf("RequestHeaders = %v, want %v", canon, want)
		}
		for _, n := range []string{"Authorization", "Cookie", "X-Api-Key", "Idempotency-Key", "Accept-Encoding", "Traceparent", "Connection", "Forwarded"} {
			for _, h := range a.RequestHeaders() {
				if http.CanonicalHeaderKey(h) == n {
					t.Errorf("%s is on the request allow-list", n)
				}
			}
		}
		for n, want := range map[string]bool{
			"Content-Type": true, "Content-Length": true, "Retry-After": true, "X-Request-Id": true,
			"X-Ratelimit-Remaining-Requests": true, "Openai-Processing-Ms": true,
			"Set-Cookie": false, "X-Pandokos-Error": false, "Location": false, "Www-Authenticate": false, "X-Echo-Authorization": false,
		} {
			if a.AllowResponseHeader(n) != want {
				t.Errorf("AllowResponseHeader(%s) = %v", n, !want)
			}
		}
	})

	t.Run("ParseRequest", func(t *testing.T) {
		op := a.Operations()[0]
		r, err := a.ParseRequest(op, []byte(`{"model":"m","stream":true}`))
		if err != nil || r.Model() != "m" || !r.Stream() {
			t.Fatalf("%v %v", r, err)
		}
		out, strip := r.Prepare("u")
		if string(out) != `{"model":"u","stream":true,"stream_options":{"include_usage":true}}` || !strip {
			t.Errorf("Prepare: %q %v", out, strip)
		}
		_, err = a.ParseRequest(op, []byte(`{"model":"a","model":"b"}`))
		if !errors.Is(err, family.ErrInvalidRequest) || family.InvalidReason(err) != "model_duplicate" {
			t.Errorf("a duplicate model: %v", err)
		}
	})

	t.Run("usage readers", func(t *testing.T) {
		op := a.Operations()[0]
		u, ok, err := a.ReadUsage(op, []byte(`{"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}`))
		if err != nil || !ok || u.InputTokens != 9 || u.OutputTokens != 12 {
			t.Errorf("ReadUsage: %+v %v %v", u, ok, err)
		}
		sr := a.NewStreamReader(op, false)
		if sr == nil {
			t.Fatal("no stream reader")
		}
		in := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
		out := append(sr.Filter(in), sr.Flush()...)
		if string(out) != string(in) {
			t.Errorf("a reader that does not strip changed the stream: %q", out)
		}
		if u, ok := sr.Usage(); !ok || u.InputTokens != 1 || u.OutputTokens != 2 || sr.Failed() {
			t.Errorf("stream usage: %+v %v failed=%v", u, ok, sr.Failed())
		}
	})
}

// TestAdapterErrors is the error body of every code of section 5.8, in the shape of the family.
func TestAdapterErrors(t *testing.T) {
	a := Adapter()
	want := map[string]struct {
		status int
		msg    string
	}{}
	for _, c := range []family.ErrorCode{
		family.CodeUnauthenticated, family.CodeTenantSuspended, family.CodeForbidden, family.CodeModelNotAllowed,
		family.CodeEndpointNotSupported, family.CodeNoCredential, family.CodeInvalidRequest, family.CodeRequestTimeout,
		family.CodeRequestTooLarge, family.CodeUpstreamUnreachable, family.CodeUpstreamTimeout, family.CodeUnavailable,
		family.CodeInternal,
	} {
		want[string(c)] = struct {
			status int
			msg    string
		}{c.Status(), c.Message()}
	}
	for _, name := range familycodes.All(t) {
		w, ok := want[name]
		if !ok {
			t.Errorf("the code %q is declared in family.go and this test does not know it", name)
			continue
		}
		rec := httptest.NewRecorder()
		a.WriteError(rec, family.ErrorCode(name))
		if rec.Code != w.status || rec.Header().Get(family.HeaderError) != name || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: status %d, headers %v", name, rec.Code, rec.Header())
		}
		var body struct {
			Error struct {
				Message string  `json:"message"`
				Type    string  `json:"type"`
				Param   *string `json:"param"`
				Code    string  `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if body.Error.Message != w.msg || body.Error.Type != "pandokos_error" || body.Error.Param != nil || body.Error.Code != name {
			t.Errorf("%s: body %s", name, rec.Body.String())
		}
	}
}
