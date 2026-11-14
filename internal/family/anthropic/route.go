package anthropic

import "net/http"

// Names of the family, its operations and their native paths (appended to the endpoint base URL).
const (
	Name                 = "anthropic"
	OperationMessages    = "messages"
	PathMessages         = "/v1/messages"
	OperationCountTokens = "messages.count_tokens"
	PathCountTokens      = "/v1/messages/count_tokens" //nolint:gosec // a URL path, not a credential
	HeaderVersion        = "Anthropic-Version"
)

// Match reports the operation for a method and a native path. The stub matches nothing.
func Match(_, _ string) (operation string, ok bool) { return "", false }

// InboundCredential returns the credential a caller presented. The stub finds none.
func InboundCredential(_ http.Header) (string, bool) { return "", false }

// AttachCredential sets "X-Api-Key: <key>" on an outbound header.
func AttachCredential(h http.Header, key string) { h.Set("X-Api-Key", key) }

// RequestHeaders is the allow-list of caller headers forwarded to the provider.
var RequestHeaders = []string{"Accept", "Content-Type", "User-Agent", HeaderVersion, "Anthropic-Beta"}

// AllowResponseHeader reports whether a provider response header is passed to the caller.
func AllowResponseHeader(_ string) bool { return false }
