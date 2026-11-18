package anthropic

import (
	"encoding/json"
	"net/http"

	"github.com/pandokos/pandokos/internal/family"
)

// ErrorType returns the provider's error type for the status of a Pandokos error code
// (sprint 3.3 section 5.2).
func ErrorType(code family.ErrorCode) string {
	switch code {
	case family.CodeInvalidRequest, family.CodeNoCredential:
		return "invalid_request_error"
	case family.CodeUnauthenticated:
		return "authentication_error"
	case family.CodeForbidden, family.CodeTenantSuspended, family.CodeModelNotAllowed:
		return "permission_error"
	case family.CodeEndpointNotSupported:
		return "not_found_error"
	case family.CodeRequestTimeout, family.CodeUpstreamTimeout:
		return "timeout_error"
	case family.CodeRequestTooLarge:
		return "request_too_large"
	case family.CodeRateLimited:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

type errorBody struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// WriteError writes a Pandokos error in the Anthropic error shape. It sets Content-Type,
// X-Pandokos-Error and the status of the code. The request ID header is set by the gateway.
func WriteError(w http.ResponseWriter, code family.ErrorCode) {
	b := errorBody{Type: "error"}
	b.Error.Type = ErrorType(code)
	b.Error.Message = code.Message()
	body, err := json.Marshal(b)
	if err != nil { // cannot happen: the body holds constants
		body = []byte(`{"type":"error","error":{"type":"api_error","message":"Pandokos: internal error."}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(family.HeaderError, string(code))
	w.WriteHeader(code.Status())
	_, _ = w.Write(body)
}
