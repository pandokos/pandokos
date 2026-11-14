package openai

import (
	"net/http"
	"strings"
)

// Names of the family and of its only operation.
const (
	Name                     = "openai"
	OperationChatCompletions = "chat.completions"
	PathChatCompletions      = "/v1/chat/completions" // the native path, appended to the endpoint base URL
)

// Match reports the operation for a method and a native path ("/v1/...").
// Only ("POST", "/v1/chat/completions") matches.
func Match(method, path string) (operation string, ok bool) {
	if method == http.MethodPost && path == PathChatCompletions {
		return OperationChatCompletions, true
	}
	return "", false
}

// InboundCredential returns the credential a caller presented: the value of
// "Authorization: Bearer <value>". The scheme is matched case-insensitively.
func InboundCredential(h http.Header) (string, bool) {
	v := h.Get("Authorization")
	scheme, value, found := strings.Cut(v, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || value == "" {
		return "", false
	}
	return value, true
}

// AttachCredential sets "Authorization: Bearer <key>" on an outbound header.
func AttachCredential(h http.Header, key string) {
	h.Set("Authorization", "Bearer "+key)
}

// RequestHeaders is the allow-list of caller headers forwarded to the provider.
var RequestHeaders = []string{"Accept", "Content-Type", "User-Agent", "OpenAI-Organization", "OpenAI-Project", "OpenAI-Beta"}

// AllowResponseHeader reports whether a provider response header is passed to the caller:
// Content-Type, Content-Length, Retry-After, X-Request-Id, and any header whose canonical
// name starts with "X-Ratelimit-" or "Openai-".
func AllowResponseHeader(canonicalName string) bool {
	switch canonicalName {
	case "Content-Type", "Content-Length", "Retry-After", "X-Request-Id":
		return true
	}
	return strings.HasPrefix(canonicalName, "X-Ratelimit-") || strings.HasPrefix(canonicalName, "Openai-")
}
