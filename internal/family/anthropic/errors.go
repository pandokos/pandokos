package anthropic

import (
	"net/http"

	"github.com/pandokos/pandokos/internal/family"
)

// ErrorType returns the provider's error type for the status of a Pandokos error code. The stub
// returns api_error for every code.
func ErrorType(_ family.ErrorCode) string { return "api_error" }

// WriteError writes a Pandokos error. The stub sets the status and X-Pandokos-Error only.
func WriteError(w http.ResponseWriter, code family.ErrorCode) {
	w.Header().Set(family.HeaderError, string(code))
	w.WriteHeader(code.Status())
}
