package adminapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// noInputEcho fails when a message holds a substring of input longer than 8 bytes that is not part
// of the fixed words: a message about a body never repeats the body. Inputs of 8 bytes or fewer
// cannot show it and are skipped.
func noInputEcho(t *testing.T, text string, input []byte, fixed []string) {
	t.Helper()
	if len(input) < 9 {
		return
	}
	for _, f := range fixed {
		text = strings.ReplaceAll(text, f, "\x00")
	}
	for _, part := range strings.Split(text, "\x00") {
		for i := 0; i+9 <= len(part); i++ {
			if bytes.Contains(input, []byte(part[i:i+9])) {
				t.Fatalf("a message repeats %q of the body: %q", part[i:i+9], text)
			}
		}
	}
}

// fixedBodyWords are the words of the messages decodeBody and Validate give and the names of the
// members of the three bodies the target reads.
var fixedBodyWords = []string{
	"is required", "must not be empty", "is not a member of this body", "has a member that is not part of this body",
	"has the wrong type", "is not a valid value", "does not match the schema", "must be a JSON object", "could not be read",
	"must match ", "length must be ", "must be one of", "must be ", "at most ", "at least ",
	"provider", "label", "secret", "description", "expires_at", "validate", "issuer", "subject", "service_account_id",
	"scope", "id", "name", "body", "null", "cannot be null",
}

// FuzzDecodeBody is T-1111: decodeBody and the Validate that follows it never panic and never run
// past the limit; for three operations whose bodies cover a member that carries a secret
// (createCredential, CredentialCreate), a Nullable member (updateCredential, CredentialUpdate) and
// a nested object (createSubjectMapping, SubjectMappingCreate), a failure is a RequestError with a
// code of the closed set, and, when the destination carries a secret, no message contains a
// substring of the body longer than 8 bytes.
func FuzzDecodeBody(f *testing.F) {
	seed := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			f.Fatal(err)
		}
		return b
	}
	bodies := [][]byte{
		seed(map[string]any{"provider": "openai", "label": "synthetic-label", "secret": "synthetic-secret-0001", "description": "synthetic", "validate": true}),
		seed(map[string]any{"provider": "openai", "label": "Bad Label", "secret": ""}),
		seed(map[string]any{"provider": "openai", "label": "synthetic-label", "secret": "synthetic-secret-0001", "synthetic-secret-in-a-name": 1}),
		seed(map[string]any{"label": "synthetic-label", "description": "synthetic", "expires_at": "2026-12-31T00:00:00Z"}),
		seed(map[string]any{"expires_at": nil}),
		seed(map[string]any{"expires_at": "not a date", "label": 7}),
		seed(map[string]any{"issuer": map[string]any{"scope": "tenant", "id": "0192f3a4-5b6c-7d8e-9fa0-b1c2d3e4f506"}, "subject": "synthetic-subject", "service_account_id": "0192f3a4-5b6c-7d8e-9fa0-b1c2d3e4f507"}),
		seed(map[string]any{"issuer": map[string]any{"scope": "tenant"}, "subject": "s", "service_account_id": "x"}),
		seed(map[string]any{"issuer": "not an object", "subject": "s", "service_account_id": "x"}),
		seed(map[string]any{"issuer": nil, "subject": nil}),
		[]byte(`{}`), []byte(`[]`), []byte(`null`), []byte(`"x"`), []byte(``), []byte(` `), []byte(`{"secret":"a"}{"secret":"b"}`),
		[]byte(`{"secret":"synthetic-secret-0002","secret":7}`), []byte(`{"Secret":"synthetic-secret-0003","PROVIDER":"p","Label":"l"}`),
	}
	for _, b := range bodies {
		for op := uint8(0); op < 3; op++ {
			f.Add(b, op, true)
		}
	}
	f.Add(bodies[0], uint8(0), false)
	f.Fuzz(func(t *testing.T, body []byte, which uint8, isJSON bool) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		r := &http.Request{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}
		if isJSON {
			r.Header.Set("Content-Type", "application/json")
		}
		const limit = 1 << 16
		var err error
		var validation []FieldError
		secret := false
		switch which % 3 {
		case 0:
			var dst CredentialCreate
			secret = true
			err = decodeBody(r, limit, true, requiredCredentialCreate, nestedCredentialCreate, &dst)
			if err == nil {
				validation = dst.Validate()
			}
		case 1:
			var dst CredentialUpdate
			err = decodeBody(r, limit, true, requiredCredentialUpdate, nestedCredentialUpdate, &dst)
			if err == nil {
				validation = dst.Validate()
			}
		default:
			var dst SubjectMappingCreate
			err = decodeBody(r, limit, true, requiredSubjectMappingCreate, nestedSubjectMappingCreate, &dst)
			if err == nil {
				validation = dst.Validate()
			}
		}
		var fields []FieldError
		if err != nil {
			var re *RequestError
			if !errors.As(err, &re) {
				t.Fatalf("a failure that is not a RequestError: %T %v", err, err)
			}
			switch re.Code {
			case ProblemCodeInvalidRequest, ProblemCodeRequestTooLarge, ProblemCodeUnsupportedMediaType:
			default:
				t.Fatalf("a failure with the code %q", re.Code)
			}
			if (re.Code == ProblemCodeRequestTooLarge) != (re.Status == http.StatusRequestEntityTooLarge) || (re.Code == ProblemCodeRequestTooLarge) != (len(body) > limit) {
				t.Fatalf("code %q, status %d for a body of %d bytes", re.Code, re.Status, len(body))
			}
			if re.Code == ProblemCodeUnsupportedMediaType && (re.Status != http.StatusUnsupportedMediaType || len(bytes.TrimSpace(body)) == 0) {
				t.Fatalf("unsupported_media_type with status %d", re.Status)
			}
			if re.Code == ProblemCodeInvalidRequest && re.Status != http.StatusBadRequest {
				t.Fatalf("invalid_request with status %d", re.Status)
			}
			if text := re.Error(); text != "adminapi: "+string(re.Code) {
				t.Fatalf("the error text %q", text)
			}
			fields = re.Fields
		} else if len(body) > limit {
			t.Fatalf("a body of %d bytes accepted over the limit of %d", len(body), limit)
		}
		fields = append(fields, validation...)
		if secret {
			for _, fe := range fields {
				noInputEcho(t, fe.Field, body, fixedBodyWords)
				noInputEcho(t, fe.Message, body, fixedBodyWords)
			}
		}
		for _, fe := range fields {
			if fe.Message == "" { // a member named "" gives an empty Field: the name is the caller's
				t.Fatalf("a field error without a message: %+v", fe)
			}
		}
	})
}

// FuzzMatch is T-1112: Match never panics; a match is an operation of the table with the method
// asked, and parameters that are non-empty, hold no slash and are exactly the parameters of the
// operation's path.
func FuzzMatch(f *testing.F) {
	for i := range Operations {
		f.Add(Operations[i].Method, concretePath(Operations[i].Path))
		f.Add(Operations[i].Method, Operations[i].Path)
	}
	for _, s := range []string{"", "/", "//", "/v1", "/v1/", "/v1/tenants//", "/v1/tenants/%2e%2e", "/v1/tenants/a/b/c/d/e/f", "v1/tenants", "/V1/TENANTS", "/v1/tenants/\x00"} {
		f.Add("GET", s)
		f.Add("POST", s)
	}
	f.Fuzz(func(t *testing.T, method, path string) {
		if len(method) > 64 || len(path) > 1<<16 {
			t.Skip()
		}
		op, params := Match(method, path)
		if op == nil {
			if params != nil {
				t.Fatalf("parameters %v without an operation", params)
			}
			return
		}
		inTable := false
		for i := range Operations {
			inTable = inTable || op == &Operations[i]
		}
		if !inTable || op.Method != method {
			t.Fatalf("the operation %s %s is not the one asked (%s)", op.Method, op.Path, method)
		}
		want := map[string]bool{}
		for _, seg := range strings.Split(op.Path[1:], "/") {
			if strings.HasPrefix(seg, "{") {
				want[seg[1:len(seg)-1]] = true
			}
		}
		if len(params) != len(want) {
			t.Fatalf("%d parameters for %d in the path %s", len(params), len(want), op.Path)
		}
		for name, v := range params {
			if !want[name] || v == "" || strings.Contains(v, "/") {
				t.Fatalf("the parameter %q = %q", name, v)
			}
		}
		if got := len(strings.Split(path[1:], "/")); got != len(strings.Split(op.Path[1:], "/")) {
			t.Fatalf("%q has %d segments, the path of the operation %d", path, got, len(strings.Split(op.Path[1:], "/")))
		}
	})
}

// concretePath fills the parameters of a path with a value that is the same for every parameter.
func concretePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") {
			segs[i] = "0192f3a4-5b6c-7d8e-9fa0-b1c2d3e4f506"
		}
	}
	return strings.Join(segs, "/")
}
