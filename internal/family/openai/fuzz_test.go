package openai

import (
	"net/http"
	"strings"
	"testing"
)

// FuzzInboundCredential is T-1117: InboundCredential never panics; a credential it returns is a
// substring of a header value of the request, is the whole of what follows the scheme, and only
// "Bearer <value>" gives one: no credential for an empty or malformed header.
func FuzzInboundCredential(f *testing.F) {
	for _, s := range []string{
		"Bearer synthetic-credential-0001", "bearer synthetic-credential-0001", "BEARER synthetic-credential-0001",
		"Bearer ", "Bearer", "", " ", "Basic c3ludGhldGljOnN5bnRoZXRpYw==", "Bearer  two-spaces", "Bearer a b c",
		"Bearer\tsynthetic", "Bearer\nsynthetic", " Bearer synthetic", "Token synthetic-credential-0001", "Bearer synthetic\r\nX-Injected: 1",
		"Bearer \x00", "Béarer synthetic", "Bearer é", strings.Repeat("A", 70000),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, auth string) {
		if len(auth) > 1<<20 {
			t.Skip()
		}
		h := http.Header{"Authorization": {auth}}
		cred, ok := InboundCredential(h)
		if !ok {
			if cred != "" {
				t.Fatalf("a credential %.20q with ok false", cred)
			}
			return
		}
		if cred == "" || !strings.Contains(auth, cred) {
			t.Fatalf("the credential %.20q is not in the header value", cred)
		}
		scheme, value, found := strings.Cut(auth, " ")
		if !found || !strings.EqualFold(scheme, "Bearer") || value != cred {
			t.Fatalf("a credential from a header that is not \"Bearer <value>\": %.40q", auth)
		}
		// A request without the header has none, whatever else it carries.
		if c, ok := InboundCredential(http.Header{"X-Api-Key": {auth}, "Cookie": {auth}}); ok || c != "" {
			t.Fatalf("a credential from another header: %.20q", c)
		}
	})
}
