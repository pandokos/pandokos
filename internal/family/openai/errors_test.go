package openai

import (
	"net/http/httptest"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
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
