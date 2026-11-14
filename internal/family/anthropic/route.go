package anthropic

import (
	"net/http"
	"strings"
)

// Names of the family, its operations and their native paths (appended to the endpoint base URL).
const (
	Name                 = "anthropic"
	OperationMessages    = "messages"
	PathMessages         = "/v1/messages"
	OperationCountTokens = "messages.count_tokens"
	PathCountTokens      = "/v1/messages/count_tokens" //nolint:gosec // a URL path, not a credential
	HeaderVersion        = "Anthropic-Version"
)

// Match reports the operation for a method and a native path. Only POST to one of the two native
// paths matches, exactly.
func Match(method, path string) (operation string, ok bool) {
	if method != http.MethodPost {
		return "", false
	}
	switch path {
	case PathMessages:
		return OperationMessages, true
	case PathCountTokens:
		return OperationCountTokens, true
	}
	return "", false
}

// trimOWS removes the optional white space (spaces and tabs) around a header value.
func trimOWS(s string) string { return strings.Trim(s, " \t") }

// bearerValue returns the value of an "Authorization: Bearer <value>" header (scheme case
// insensitive) and whether the header is a bearer header with a value.
func bearerValue(auth string) (string, bool) {
	const scheme = "Bearer"
	if len(auth) <= len(scheme) || !strings.EqualFold(auth[:len(scheme)], scheme) {
		return "", false
	}
	if c := auth[len(scheme)]; c != ' ' && c != '\t' {
		return "", false
	}
	v := trimOWS(auth[len(scheme)+1:])
	return v, v != ""
}

// InboundCredential returns the credential a caller presented, by the table of sprint 3.3
// section 5.2: the value of X-Api-Key or of "Authorization: Bearer <value>". When both carry a
// value they must be equal. Nothing else is accepted, and a refused credential is never returned.
func InboundCredential(h http.Header) (string, bool) {
	key := trimOWS(h.Get("X-Api-Key"))
	auth := trimOWS(h.Get("Authorization"))
	bearer, isBearer := bearerValue(auth)
	switch {
	case key != "" && auth == "":
		return key, true
	case key != "" && isBearer && bearer == key:
		return key, true
	case key == "" && isBearer:
		return bearer, true
	}
	return "", false
}

// AttachCredential sets "X-Api-Key: <key>" on an outbound header.
func AttachCredential(h http.Header, key string) { h.Set("X-Api-Key", key) }

// RequestHeaders is the allow-list of caller headers forwarded to the provider.
var RequestHeaders = []string{"Accept", "Content-Type", "User-Agent", HeaderVersion, "Anthropic-Beta"}

// AllowResponseHeader reports whether a provider response header is passed to the caller:
// Content-Type, Content-Length, Retry-After, Request-Id, X-Should-Retry, and any header whose
// canonical name starts with "Anthropic-".
func AllowResponseHeader(canonicalName string) bool {
	switch canonicalName {
	case "Content-Type", "Content-Length", "Retry-After", "Request-Id", "X-Should-Retry":
		return true
	}
	return strings.HasPrefix(canonicalName, "Anthropic-")
}

// Recognizes reports whether a request that reached no provider looks like a call of the family:
// it matches a route, or it carries an Anthropic-Version header.
func Recognizes(method, nativePath string, h http.Header) bool {
	if _, ok := Match(method, nativePath); ok {
		return true
	}
	return h.Get(HeaderVersion) != ""
}
