package anthropic_test

import (
	"encoding/json"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
	"github.com/pandokos/pandokos/test/familycodes"
)

// errorTypes is the table of section 5.2 owned by the test: every code of the family package
// with the provider's error type for its status. TestWriteErrorEveryCode checks it for completeness.
var errorTypes = map[family.ErrorCode]string{
	family.CodeInvalidRequest:       "invalid_request_error",
	family.CodeNoCredential:         "invalid_request_error",
	family.CodeUnauthenticated:      "authentication_error",
	family.CodeForbidden:            "permission_error",
	family.CodeTenantSuspended:      "permission_error",
	family.CodeModelNotAllowed:      "permission_error",
	family.CodeEndpointNotSupported: "not_found_error",
	family.CodeRequestTimeout:       "timeout_error",
	family.CodeUpstreamTimeout:      "timeout_error",
	family.CodeRequestTooLarge:      "request_too_large",
	family.CodeInternal:             "api_error",
	family.CodeUpstreamUnreachable:  "api_error",
	family.CodeUnavailable:          "api_error",
	family.CodeRateLimited:          "rate_limit_error",
	family.CodeQuotaExceeded:        "rate_limit_error",
	family.CodeNoPrice:              "invalid_request_error",
}

// TestWriteErrorEveryCode keeps the test's table complete against family.go.
func TestWriteErrorEveryCode(t *testing.T) {
	names := familycodes.All(t)
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
		if _, ok := errorTypes[family.ErrorCode(n)]; !ok {
			t.Errorf("the code %q of family.go has no row in the test's table: add its error.type of section 5.2", n)
		}
	}
	var stale []string
	for c := range errorTypes {
		if !seen[string(c)] {
			stale = append(stale, string(c))
		}
	}
	sort.Strings(stale)
	if len(stale) != 0 {
		t.Errorf("rows for codes that family.go does not declare: %v", stale)
	}
}

// TestWriteError is T-525: for every code, the status, the headers, and a body that decodes to
// the shape with the error.type of the table and the code's message, with no other member.
func TestWriteError(t *testing.T) {
	for code, typ := range errorTypes {
		t.Run(string(code), func(t *testing.T) {
			rec := httptest.NewRecorder()
			anthropic.WriteError(rec, code)
			if rec.Code != code.Status() {
				t.Errorf("status %d, want %d", rec.Code, code.Status())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("content type %q", got)
			}
			if got := rec.Header().Get(family.HeaderError); got != string(code) {
				t.Errorf("%s %q", family.HeaderError, got)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatalf("the body is not JSON: %v: %s", err, rec.Body.Bytes())
			}
			if len(doc) != 2 || string(doc["type"]) != `"error"` || doc["error"] == nil {
				t.Fatalf("members of the body: %s", rec.Body.Bytes())
			}
			var inner map[string]string
			if err := json.Unmarshal(doc["error"], &inner); err != nil {
				t.Fatalf("error member: %v", err)
			}
			if len(inner) != 2 || inner["type"] != typ || inner["message"] != code.Message() {
				t.Errorf("error member %v, want type %q message %q", inner, typ, code.Message())
			}
			if got := anthropic.ErrorType(code); got != typ {
				t.Errorf("ErrorType = %q, want %q", got, typ)
			}
		})
	}
	t.Run("the shape for one code, byte for byte", func(t *testing.T) {
		rec := httptest.NewRecorder()
		anthropic.WriteError(rec, family.CodeEndpointNotSupported)
		want := `{"type":"error","error":{"type":"not_found_error","message":"Pandokos: this endpoint is not supported."}}`
		if rec.Body.String() != want {
			t.Errorf("body %q\nwant %q", rec.Body.String(), want)
		}
	})
	t.Run("through the adapter", func(t *testing.T) {
		rec := httptest.NewRecorder()
		anthropic.Adapter().WriteError(rec, family.CodeUnauthenticated)
		if rec.Code != 401 || rec.Header().Get(family.HeaderError) != "unauthenticated" {
			t.Errorf("%d %v", rec.Code, rec.Header())
		}
	})
	t.Run("an unknown code is an internal error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		anthropic.WriteError(rec, family.ErrorCode("synthetic_unknown"))
		if rec.Code != 500 {
			t.Errorf("status %d", rec.Code)
		}
		var doc struct {
			Error struct{ Type string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc.Error.Type != "api_error" {
			t.Errorf("%v %s", err, rec.Body.Bytes())
		}
	})
}
