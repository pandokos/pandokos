package openai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/test/familycodes"
)

// opNamed returns the operation of the adapter with the given name.
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

// TestOperations is T-456: the adapter lists three operations and Match accepts the method and
// the native path of each, exactly.
func TestOperations(t *testing.T) {
	a := Adapter()
	native := map[string]string{
		"chat.completions": "/v1/chat/completions",
		"responses":        "/v1/responses",
		"embeddings":       "/v1/embeddings",
	}
	ops := a.Operations()
	if len(ops) != len(native) {
		t.Fatalf("%d operations, want %d: %+v", len(ops), len(native), ops)
	}
	for _, op := range ops {
		path, ok := native[op.Name]
		if !ok {
			t.Errorf("unknown operation %q", op.Name)
			continue
		}
		if op.Method != "POST" || op.Path != path {
			t.Errorf("%s: %s %s", op.Name, op.Method, op.Path)
		}
		got, ok := a.Match("POST", path)
		if !ok || got != op {
			t.Errorf("Match(POST, %s) = %+v %v, want %+v", path, got, ok, op)
		}
		if name, ok := Match("POST", path); !ok || name != op.Name {
			t.Errorf("openai.Match(POST, %s) = %q %v", path, name, ok)
		}
		for _, bad := range []struct{ method, path string }{
			{"GET", path}, {"PUT", path}, {"post", path}, {"POST", path + "/"}, {"POST", "/" + path},
			{"POST", strings.ToUpper(path)}, {"POST", path + "?x=1"}, {"POST", "/openai" + path},
			{"POST", strings.TrimPrefix(path, "/v1")},
		} {
			if got, ok := a.Match(bad.method, bad.path); ok {
				t.Errorf("%s %s matched %+v", bad.method, bad.path, got)
			}
		}
	}
	for _, c := range []struct{ name, want string }{
		{OperationChatCompletions, "chat.completions"}, {OperationResponses, "responses"}, {OperationEmbeddings, "embeddings"},
		{PathChatCompletions, "/v1/chat/completions"}, {PathResponses, "/v1/responses"}, {PathEmbeddings, "/v1/embeddings"},
	} {
		if c.name != c.want {
			t.Errorf("constant %q, want %q", c.name, c.want)
		}
	}
}

// TestAdapterDelegatesByOperation checks that the adapter's methods follow the operation they get.
func TestAdapterDelegatesByOperation(t *testing.T) {
	a := Adapter()
	resp, emb := opNamed(t, a, "responses"), opNamed(t, a, "embeddings")
	if _, err := a.ParseRequest(resp, []byte(`{"model":"m","background":true}`)); family.InvalidReason(err) != "background_not_supported" {
		t.Errorf("responses: %v", err)
	}
	r, err := a.ParseRequest(emb, []byte(`{"model":"m","stream":true}`))
	if err != nil || r.Stream() {
		t.Errorf("embeddings: %v stream=%v", err, err == nil && r.Stream())
	}
	u, ok, err := a.ReadUsage(emb, []byte(`{"usage":{"prompt_tokens":8,"total_tokens":8}}`))
	if err != nil || !ok || u.InputTokens != 8 || u.OutputTokens != 0 {
		t.Errorf("embeddings usage: %+v %v %v", u, ok, err)
	}
	u, ok, err = a.ReadUsage(resp, []byte(`{"usage":{"input_tokens":3,"output_tokens":4}}`))
	if err != nil || !ok || u.InputTokens != 3 || u.OutputTokens != 4 {
		t.Errorf("responses usage: %+v %v %v", u, ok, err)
	}
	if got := a.EstimateUsage(emb, family.Evidence{RequestBytes: 9, ResponseBytes: 100}); got.InputTokens != 3 || got.OutputTokens != 0 {
		t.Errorf("embeddings estimate: %+v", got)
	}
	if sr := a.NewStreamReader(resp, true); sr == nil {
		t.Error("no responses stream reader")
	}
}

// TestAdapter is T-424: the adapter of the openai family.
func TestAdapter(t *testing.T) {
	a := Adapter()

	t.Run("name and operations", func(t *testing.T) {
		if a.Name() != "openai" {
			t.Errorf("Name = %q", a.Name())
		}
		want := []family.Operation{
			{Name: "chat.completions", Method: "POST", Path: "/v1/chat/completions"},
			{Name: "responses", Method: "POST", Path: "/v1/responses"},
			{Name: "embeddings", Method: "POST", Path: "/v1/embeddings"},
		}
		got := a.Operations()
		sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
		sort.Slice(want, func(i, j int) bool { return want[i].Name < want[j].Name })
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Operations = %+v, want %+v", got, want)
		}
		ops := a.Operations()
		ops[0].Path = "/changed"
		for _, op := range a.Operations() {
			if op.Path == "/changed" {
				t.Error("Operations returns the adapter's own slice")
			}
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
		op := opNamed(t, a, "chat.completions")
		r, err := a.ParseRequest(op, []byte(`{"model":"m","stream":true}`))
		if err != nil || r.Model() != "m" || !r.Stream() {
			t.Fatalf("%v %v", r, err)
		}
		out, strip := r.Prepare(family.Preparation{UpstreamModel: "u"})
		if string(out) != `{"model":"u","stream":true,"stream_options":{"include_usage":true}}` || !strip {
			t.Errorf("Prepare: %q %v", out, strip)
		}
		_, err = a.ParseRequest(op, []byte(`{"model":"a","model":"b"}`))
		if !errors.Is(err, family.ErrInvalidRequest) || family.InvalidReason(err) != "model_duplicate" {
			t.Errorf("a duplicate model: %v", err)
		}
	})

	t.Run("usage readers", func(t *testing.T) {
		op := opNamed(t, a, "chat.completions")
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
		if u, ok := reported(sr); !ok || u.InputTokens != 1 || u.OutputTokens != 2 || sr.Failed() {
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

// reported adapts the readers' Usage to the "was anything reported" form these tests assert on.
func reported(r interface {
	Usage() (family.Usage, family.Parts)
}) (family.Usage, bool) {
	u, parts := r.Usage()
	return u, parts != 0
}
