package openai

import (
	"net/http"
	"testing"
)

func TestMatch(t *testing.T) {
	op, ok := Match("POST", "/v1/chat/completions")
	if !ok || op != "chat.completions" {
		t.Fatalf("got %q %v", op, ok)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/chat/completions"},
		{"PUT", "/v1/chat/completions"},
		{"post", "/v1/chat/completions"},
		{"POST", "/v1/chat/completions/"},
		{"POST", "/v1/chat/completion"},
		{"POST", "/chat/completions"},
		{"POST", "/v1/embeddings"},
		{"POST", "/v1/responses"},
		{"POST", "/"},
		{"POST", ""},
		{"POST", "v1/chat/completions"},
		{"POST", "/V1/Chat/Completions"},
	} {
		if op, ok := Match(c.method, c.path); ok {
			t.Errorf("%s %s matched %q", c.method, c.path, op)
		}
	}
	if Name != "openai" || PathChatCompletions != "/v1/chat/completions" {
		t.Error("constants")
	}
}

func TestInboundCredential(t *testing.T) {
	good := map[string]string{"Bearer x": "x", "bearer x": "x", "BEARER abc.def": "abc.def"}
	for in, want := range good {
		h := http.Header{"Authorization": {in}}
		if got, ok := InboundCredential(h); !ok || got != want {
			t.Errorf("%q: got %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "Bearer", "Bearer ", "Basic abc", "Bearerx", "Token x"} {
		h := http.Header{"Authorization": {in}}
		if got, ok := InboundCredential(h); ok {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
	if _, ok := InboundCredential(http.Header{}); ok {
		t.Error("missing header accepted")
	}
}

func TestAttachCredential(t *testing.T) {
	h := http.Header{"Authorization": {"old"}}
	AttachCredential(h, "k")
	if got := h.Values("Authorization"); len(got) != 1 || got[0] != "Bearer k" {
		t.Errorf("got %v", got)
	}
}

func TestAllowResponseHeader(t *testing.T) {
	allowed := []string{
		"Content-Type", "Content-Length", "Retry-After", "X-Request-Id",
		"X-Ratelimit-Remaining-Requests", "X-Ratelimit-Reset-Tokens", "Openai-Processing-Ms", "Openai-Version",
	}
	refused := []string{
		"Set-Cookie", "Server", "X-Pandokos-Error", "X-Pandokos-Request-Id", "Content-Encoding",
		"Transfer-Encoding", "Connection", "Date", "Cf-Ray", "Alt-Svc", "X-Ratelimit", "Openai", "Location",
	}
	for _, n := range allowed {
		if !AllowResponseHeader(n) {
			t.Errorf("%s refused", n)
		}
	}
	for _, n := range refused {
		if AllowResponseHeader(n) {
			t.Errorf("%s allowed", n)
		}
	}
	want := []string{"Accept", "Content-Type", "User-Agent", "OpenAI-Organization", "OpenAI-Project", "OpenAI-Beta"}
	if len(RequestHeaders) != len(want) {
		t.Fatalf("RequestHeaders = %v", RequestHeaders)
	}
	for i := range want {
		if http.CanonicalHeaderKey(RequestHeaders[i]) != http.CanonicalHeaderKey(want[i]) {
			t.Errorf("RequestHeaders[%d] = %q, want %q", i, RequestHeaders[i], want[i])
		}
	}
}
