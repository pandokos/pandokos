package family

import (
	"errors"
	"testing"

	"github.com/pandokos/pandokos/test/familycodes"
)

// codeTable is the contract of sprint 3.1, section 5.8: every code with its status and message.
var codeTable = []struct {
	code    ErrorCode
	name    string
	status  int
	message string
}{
	{CodeUnauthenticated, "unauthenticated", 401, "Pandokos: missing or invalid credential."},
	{CodeTenantSuspended, "tenant_suspended", 403, "Pandokos: the tenant is not active."},
	{CodeForbidden, "forbidden", 403, "Pandokos: the credential does not allow this operation."},
	{CodeModelNotAllowed, "model_not_allowed", 403, "Pandokos: the model is not available to this credential."},
	{CodeEndpointNotSupported, "endpoint_not_supported", 404, "Pandokos: this endpoint is not supported."},
	{CodeNoCredential, "no_credential", 409, "Pandokos: no usable provider credential for this provider."},
	{CodeInvalidRequest, "invalid_request", 400, `Pandokos: the request body is not accepted: it must be a JSON object with one string member "model", and must not ask for background processing.`},
	{CodeRequestTimeout, "request_timeout", 408, "Pandokos: the request body was not received in time."},
	{CodeRequestTooLarge, "request_too_large", 413, "Pandokos: the request body exceeds the configured limit."},
	{CodeUpstreamUnreachable, "upstream_unreachable", 502, "Pandokos: the provider could not be reached."},
	{CodeUpstreamTimeout, "upstream_timeout", 504, "Pandokos: the provider did not answer in time."},
	{CodeUnavailable, "unavailable", 503, "Pandokos: the request cannot be verified right now."},
	{CodeInternal, "internal", 500, "Pandokos: internal error."},
}

// TestErrorCodes is T-22: the thirteen codes, their statuses and their constant messages. The
// sweep ranges over the constants of family.go and fails on a code the table does not know.
func TestErrorCodes(t *testing.T) {
	known := map[string]bool{}
	for _, c := range codeTable {
		known[c.name] = true
		if string(c.code) != c.name {
			t.Errorf("%s: code string %q", c.name, c.code)
		}
		if c.code.Status() != c.status {
			t.Errorf("%s: status %d, want %d", c.name, c.code.Status(), c.status)
		}
		if c.code.Message() != c.message {
			t.Errorf("%s: message %q", c.name, c.code.Message())
		}
	}
	for _, name := range familycodes.All(t) {
		if !known[name] {
			t.Errorf("the code %q is declared in family.go and is not in the table of the contract", name)
		}
	}
	if got := len(familycodes.All(t)); got != len(codeTable) {
		t.Errorf("family.go declares %d codes, the contract has %d", got, len(codeTable))
	}
	if HeaderError != "X-Pandokos-Error" || HeaderRequestID != "X-Pandokos-Request-Id" {
		t.Error("header names")
	}
}

func TestInvalidReason(t *testing.T) {
	err := Invalid("model_missing")
	if !errors.Is(err, ErrInvalidRequest) || InvalidReason(err) != "model_missing" {
		t.Errorf("Invalid: %v %q", err, InvalidReason(err))
	}
	if InvalidReason(errors.New("other")) != "" || InvalidReason(nil) != "" {
		t.Error("a reason for an error that is not a refusal")
	}
	wrapped := errors.Join(errors.New("context"), err)
	if InvalidReason(wrapped) != "model_missing" {
		t.Error("the reason is lost when the refusal is wrapped")
	}
}
