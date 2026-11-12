package openai

import (
	"net/http"
	"testing"
)

func TestMatch(t *testing.T) {
	op, ok := Match("POST", "/chat/completions")
	if !ok || op != "chat.completions" {
		t.Fatalf("got %q %v", op, ok)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/chat/completions"},
		{"PUT", "/chat/completions"},
		{"post", "/chat/completions"},
		{"POST", "/chat/completions/"},
		{"POST", "/chat/completion"},
		{"POST", "/embeddings"},
		{"POST", "/"},
		{"POST", ""},
		{"POST", "chat/completions"},
		{"POST", "/Chat/Completions"},
	} {
		if op, ok := Match(c.method, c.path); ok {
			t.Errorf("%s %s matched %q", c.method, c.path, op)
		}
	}
	if Name != "openai" || PathChatCompletions != "/chat/completions" {
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
	if len(RequestHeaders) != 3 || RequestHeaders[0] != "Accept" || RequestHeaders[1] != "Content-Type" || RequestHeaders[2] != "User-Agent" {
		t.Errorf("RequestHeaders = %v", RequestHeaders)
	}
}
