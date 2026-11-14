package admin

import (
	"encoding/json"
	"errors"
	"net/http"

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
}

// The errors of the handlers and of the hooks; the Error hook turns each into a problem.
var (
	errUnauthenticated = errors.New("admin: unauthenticated")
	errNotFound        = errors.New("admin: not found")
	errConflict        = errors.New("admin: conflict")
	errInvalidState    = errors.New("admin: invalid state")
	errForbidden       = errors.New("admin: forbidden")
	errTenantState     = errors.New("admin: tenant not active")

	// errCredentialRejected: the provider answered 401 or 403 to a credential presented with validate.
	errCredentialRejected = errors.New("admin: the provider rejected the credential")
	// errValidationUnavailable: the provider could not confirm the credential.
	errValidationUnavailable = errors.New("admin: the provider could not confirm the credential")
)

// invalidError is an invalid_request with the members that are at fault.
type invalidError struct{ fields []adminapi.FieldError }

func (e *invalidError) Error() string { return "admin: invalid request" }

func invalid(field, message string) error {
	return &invalidError{fields: []adminapi.FieldError{{Field: field, Message: message}}}
}

// statusRecorder remembers the status of a response.
type statusRecorder struct {
	http.ResponseWriter
	status int
	state  *reqState
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
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
		r.warn()
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
