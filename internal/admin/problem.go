package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

type problemSpec struct {
	status        int
	title, detail string
}

// The table of sprint 1.3, section 5.4: a title and a detail that are constants per code.
var problems = map[adminapi.ProblemCode]problemSpec{
	adminapi.ProblemCodeInvalidRequest:       {400, "Invalid request", "The request does not match the API description."},
	adminapi.ProblemCodeUnauthenticated:      {401, "Unauthenticated", "Missing or invalid credential."},
	adminapi.ProblemCodeForbidden:            {403, "Forbidden", "The credential does not allow this operation."},
	adminapi.ProblemCodeTenantSuspended:      {403, "Tenant not active", "The tenant is not active."},
	adminapi.ProblemCodeNotFound:             {404, "Not found", "The resource does not exist."},
	adminapi.ProblemCodeConflict:             {409, "Conflict", "The name is already in use."},
	adminapi.ProblemCodeInvalidState:         {409, "Invalid state", "The resource is in a state that does not allow this operation."},
	adminapi.ProblemCodeRequestTooLarge:      {413, "Request too large", "The request body exceeds 1048576 bytes."},
	adminapi.ProblemCodeUnsupportedMediaType: {415, "Unsupported media type", "The request body must be application/json."},
	adminapi.ProblemCodeInternal:             {500, "Internal error", "Internal error."},
	adminapi.ProblemCodeUnavailable:          {503, "Unavailable", "The request cannot be verified or recorded right now."},

	// Sprint 2.1, section 5.4: the two outcomes of a validation at write time.
	adminapi.ProblemCodeCredentialRejected:    {422, "Credential rejected", "The provider did not accept the credential. Nothing was stored."},
	adminapi.ProblemCodeValidationUnavailable: {502, "Validation unavailable", "The provider could not confirm the credential. Nothing was stored."},

	// Sprint 2.2, section 5.8: the two outcomes of a price import that fails before anything is written.
	adminapi.ProblemCodePriceSourceUnavailable: {502, "Price source unavailable", "The price dataset could not be downloaded. Nothing was imported."},
	adminapi.ProblemCodePriceDatasetInvalid:    {422, "Price dataset invalid", "The price dataset could not be read. Nothing was imported."},

	// Sprint 4.2, section 5.14: sign-in, sessions and idempotency keys.
	adminapi.ProblemCodeTooManyAttempts:         {429, "Too many attempts", "Too many attempts. Try again later."},
	adminapi.ProblemCodePasswordChangeRequired:  {403, "Password change required", "The password must be changed before anything else."},
	adminapi.ProblemCodeCsrfRejected:            {403, "Request rejected", "The request does not come from this site."},
	adminapi.ProblemCodeSignInDisabled:          {403, "Sign-in disabled", "Sign-in is not available here."},
	adminapi.ProblemCodeIdempotencyKeyReused:    {422, "Idempotency key reused", "The Idempotency-Key was used with another request."},
	adminapi.ProblemCodeIdempotencyInProgress:   {409, "Request in progress", "The first request with this Idempotency-Key is still running."},
	adminapi.ProblemCodeIdempotencyResponseLost: {409, "Response lost", "The first request with this Idempotency-Key succeeded and its response was not kept."},

	// Sprint 4.3, section 5.4 and 5.6: the rule against escalation, support actions, assigned roles.
	adminapi.ProblemCodeEscalationRefused: {403, "Escalation refused", "The credential may not give or take a permission it does not hold."},
	adminapi.ProblemCodeSupportDisabled:   {403, "Support actions disabled", "Support actions are switched off."},
	adminapi.ProblemCodeRoleInUse:         {409, "Role in use", "The role is still assigned."},
}

// The errors of the handlers and of the hooks; the Error hook turns each into a problem.
var (
	errUnauthenticated = errors.New("admin: unauthenticated")
	errNotFound        = errors.New("admin: not found")
	errConflict        = errors.New("admin: conflict")
	errInvalidState    = errors.New("admin: invalid state")
	errForbidden       = errors.New("admin: forbidden")
	errTenantState     = errors.New("admin: tenant not active")

	// The errors of sprint 4.3.
	errEscalation      = errors.New("admin: escalation refused")
	errSupportDisabled = errors.New("admin: support actions are disabled")
	errRoleInUse       = errors.New("admin: the role is in use")

	// The errors of the hook for sessions (sprint 4.2, sections 5.3 and 5.7).
	errCSRF           = errors.New("admin: request rejected as forged")
	errSignInDisabled = errors.New("admin: sign-in is not available")
	errMustChange     = errors.New("admin: the password must be changed first")

	// errCredentialRejected: the provider answered 401 or 403 to a credential presented with validate.
	errCredentialRejected = errors.New("admin: the provider rejected the credential")
	// errValidationUnavailable: the provider could not confirm the credential.
	errValidationUnavailable = errors.New("admin: the provider could not confirm the credential")
)

// retryError is a problem that tells the caller when to come back (Retry-After, in whole seconds).
type retryError struct {
	code  adminapi.ProblemCode
	after time.Duration
}

func (e *retryError) Error() string { return "admin: " + string(e.code) }

// invalidError is an invalid_request with the members that are at fault.
type invalidError struct{ fields []adminapi.FieldError }

func (e *invalidError) Error() string { return "admin: invalid request" }

// stateError is an invalid_state with the members that explain it (sprint 5.3, section 5.4).
type stateError struct{ fields []adminapi.FieldError }

func (e *stateError) Error() string { return "admin: invalid state" }

func invalidState(field, message string) error {
	return &stateError{fields: []adminapi.FieldError{{Field: field, Message: message}}}
}

func invalid(field, message string) error {
	return &invalidError{fields: []adminapi.FieldError{{Field: field, Message: message}}}
}

// statusRecorder remembers the status of a response.
type statusRecorder struct {
	http.ResponseWriter
	status int
	state  *reqState
}

// flush copies the headers and cookies that a handler or the hook added (reqState.header) into the
// response, once, before the status line.
func (r *statusRecorder) flush() {
	if r.state == nil {
		return
	}
	for k, vs := range r.state.header {
		for _, v := range vs {
			r.Header().Add(k, v)
		}
	}
	r.state.header = nil
}

// warn marks every response to the bootstrap key (IAM-14).
func (r *statusRecorder) warn() {
	if r.state != nil && r.state.principal != nil && r.state.principal.Bootstrap {
		r.Header().Set("X-Pandokos-Warning", "bootstrap-key-active")
	}
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
		r.warn()
		r.flush()
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if st := r.state; st != nil && st.capture != nil && !st.captureOverflow {
		if st.capture.Len()+len(p) > maxStoredResponse {
			st.captureOverflow = true
		} else {
			st.capture.Write(p)
		}
	}
	if r.status == 0 {
		r.status = http.StatusOK
		r.warn()
		r.flush()
	}
	return r.ResponseWriter.Write(p)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// writeProblem writes the problem of a code. It is the only function that writes an error
// response of the administration API.
func writeProblem(w http.ResponseWriter, requestID string, code adminapi.ProblemCode, fields []adminapi.FieldError) {
	spec, ok := problems[code]
	if !ok {
		code, spec = adminapi.ProblemCodeInternal, problems[adminapi.ProblemCodeInternal]
	}
	p := adminapi.Problem{
		Type: "urn:pandokos:problem:" + string(code), Title: spec.title, Status: spec.status, Code: code,
		Detail: spec.detail, RequestID: requestID, Errors: fields,
	}
	b, err := json.Marshal(p)
	if err != nil {
		b = []byte(`{"code":"internal"}`)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	if code == adminapi.ProblemCodeUnauthenticated {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.WriteHeader(spec.status)
	_, _ = w.Write(append(b, '\n'))
}
