package family

import "testing"

func TestErrorCodes(t *testing.T) {
	cases := []struct {
		code    ErrorCode
		name    string
		status  int
		message string
	}{
		{CodeUnauthenticated, "unauthenticated", 401, "Pandokos: missing or invalid credential."},
		{CodeEndpointNotSupported, "endpoint_not_supported", 404, "Pandokos: this endpoint is not supported."},
		{CodeRequestTooLarge, "request_too_large", 413, "Pandokos: the request body exceeds the configured limit."},
		{CodeUpstreamUnreachable, "upstream_unreachable", 502, "Pandokos: the provider could not be reached."},
		{CodeUpstreamTimeout, "upstream_timeout", 504, "Pandokos: the provider did not answer in time."},
		{CodeInternal, "internal", 500, "Pandokos: internal error."},
	}
	for _, c := range cases {
		if string(c.code) != c.name {
			t.Errorf("%s: code string %q", c.name, c.code)
		}
		if c.code.Status() != c.status {
			t.Errorf("%s: status %d", c.name, c.code.Status())
		}
		if c.code.Message() != c.message {
			t.Errorf("%s: message %q", c.name, c.code.Message())
		}
	}
	if HeaderError != "X-Pandokos-Error" || HeaderRequestID != "X-Pandokos-Request-Id" {
		t.Error("header names")
	}
}
