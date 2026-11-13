// Package family holds what is common to all API families: the normalized usage and the
// error codes of the inference side.
package family

// Usage is the normalized form of ADR 0010. InputTokens includes CacheReadTokens and
// CacheWriteTokens; OutputTokens includes ReasoningTokens.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	Extra            map[string]int64 // provider-specific categories, non-zero entries only; nil if none
}

// ErrorCode is a stable code of ADR 0020.
type ErrorCode string

// The codes the skeleton can raise.
const (
	CodeUnauthenticated      ErrorCode = "unauthenticated"        // 401
	CodeEndpointNotSupported ErrorCode = "endpoint_not_supported" // 404
	CodeRequestTooLarge      ErrorCode = "request_too_large"      // 413
	CodeUpstreamUnreachable  ErrorCode = "upstream_unreachable"   // 502
	CodeUpstreamTimeout      ErrorCode = "upstream_timeout"       // 504
	CodeInternal             ErrorCode = "internal"               // 500
	CodeForbidden            ErrorCode = "forbidden"              // 403
	CodeTenantSuspended      ErrorCode = "tenant_suspended"       // 403
	CodeUnavailable          ErrorCode = "unavailable"            // 503
)

// Status returns the HTTP status of the code.
func (c ErrorCode) Status() int {
	switch c {
	case CodeUnauthenticated:
		return 401
	case CodeEndpointNotSupported:
		return 404
	case CodeRequestTooLarge:
		return 413
	case CodeUpstreamUnreachable:
		return 502
	case CodeUpstreamTimeout:
		return 504
	case CodeForbidden, CodeTenantSuspended:
		return 403
	case CodeUnavailable:
		return 503
	default:
		return 500
	}
}

// Message returns the fixed message of the code. It never contains detail of the failure.
func (c ErrorCode) Message() string {
	switch c {
	case CodeUnauthenticated:
		return "Pandokos: missing or invalid credential."
	case CodeEndpointNotSupported:
		return "Pandokos: this endpoint is not supported."
	case CodeRequestTooLarge:
		return "Pandokos: the request body exceeds the configured limit."
	case CodeUpstreamUnreachable:
		return "Pandokos: the provider could not be reached."
	case CodeUpstreamTimeout:
		return "Pandokos: the provider did not answer in time."
	case CodeForbidden:
		return "Pandokos: the credential does not allow this operation."
	case CodeTenantSuspended:
		return "Pandokos: the tenant is not active."
	case CodeUnavailable:
		return "Pandokos: the request cannot be verified right now."
	default:
		return "Pandokos: internal error."
	}
}

// Headers set on every error raised by Pandokos.
const (
	HeaderError     = "X-Pandokos-Error"
	HeaderRequestID = "X-Pandokos-Request-Id"
)
