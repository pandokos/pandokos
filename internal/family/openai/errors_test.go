package openai

import (
	"net/http/httptest"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/test/familycodes"
)

func TestWriteError(t *testing.T) {
	cases := []struct {
		code   family.ErrorCode
		status int
		msg    string
	}{
		{family.CodeUnauthenticated, 401, "Pandokos: missing or invalid credential."},
		{family.CodeEndpointNotSupported, 404, "Pandokos: this endpoint is not supported."},
		{family.CodeRequestTooLarge, 413, "Pandokos: the request body exceeds the configured limit."},
		{family.CodeUpstreamUnreachable, 502, "Pandokos: the provider could not be reached."},
		{family.CodeUpstreamTimeout, 504, "Pandokos: the provider did not answer in time."},
		{family.CodeInternal, 500, "Pandokos: internal error."},
		{family.CodeForbidden, 403, "Pandokos: the credential does not allow this operation."},
		{family.CodeTenantSuspended, 403, "Pandokos: the tenant is not active."},
		{family.CodeUnavailable, 503, "Pandokos: the request cannot be verified right now."},
		{family.CodeModelNotAllowed, 403, "Pandokos: the model is not available to this credential."},
		{family.CodeNoCredential, 409, "Pandokos: no usable provider credential for this provider."},
		{family.CodeInvalidRequest, 400, `Pandokos: the request body must be a JSON object with one string member \"model\".`},
		{family.CodeRequestTimeout, 408, "Pandokos: the request body was not received in time."},
	}
	for _, c := range cases {
		t.Run(string(c.code), func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, c.code)
			if rec.Code != c.status {
				t.Errorf("status %d", rec.Code)
			}
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("content type %q", rec.Header().Get("Content-Type"))
			}
			if rec.Header().Get(family.HeaderError) != string(c.code) {
				t.Errorf("header %q", rec.Header().Get(family.HeaderError))
			}
			want := `{"error":{"message":"` + c.msg + `","type":"pandokos_error","param":null,"code":"` + string(c.code) + `"}}`
			if rec.Body.String() != want {
				t.Errorf("body %q\nwant %q", rec.Body.String(), want)
			}
		})
	}
}

// TestWriteErrorEveryCode ranges over the codes the family package declares: a new code without
// a test row above fails here.
func TestWriteErrorEveryCode(t *testing.T) {
	for _, name := range familycodes.All(t) {
		code := family.ErrorCode(name)
		rec := httptest.NewRecorder()
		WriteError(rec, code)
		if rec.Code != code.Status() || rec.Header().Get(family.HeaderError) != name {
			t.Errorf("%s: status %d, header %q", name, rec.Code, rec.Header().Get(family.HeaderError))
		}
		if code.Message() == "" || rec.Body.Len() == 0 {
			t.Errorf("%s: no message or body", name)
		}
	}
}
