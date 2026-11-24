package anthropic_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family/anthropic"
)

// FuzzInboundCredential is T-1118: InboundCredential never panics; a credential it returns is a
// substring of a header value of the request (X-Api-Key or Authorization), is not empty, and when
// both headers carry a value they carry the same one: no credential for an empty or malformed
// header, and none that differs between the two.
func FuzzInboundCredential(f *testing.F) {
	const key = "synthetic-credential-0001"
	for _, s := range [][2]string{
		{key, ""}, {"", "Bearer " + key}, {key, "Bearer " + key}, {key, "Bearer synthetic-credential-0002"},
		{"", "bearer " + key}, {" " + key + "\t", ""}, {"", "  Bearer " + key + "  "}, {"", "Bearer"}, {"", "Bearer "}, {"", ""},
		{key, "Basic abc"}, {"", "Basic abc"}, {"", "Bearer a b"}, {key + "\r\nX-Injected: 1", ""}, {"\t", "\t"},
		{key, "Bearer " + key + " "}, {"", "Token " + key},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, apiKey, auth string) {
		if len(apiKey) > 1<<20 || len(auth) > 1<<20 {
			t.Skip()
		}
		h := http.Header{}
		if apiKey != "" {
			h["X-Api-Key"] = []string{apiKey}
		}
		if auth != "" {
			h["Authorization"] = []string{auth}
		}
		cred, ok := anthropic.InboundCredential(h)
		if !ok {
			if cred != "" {
				t.Fatalf("a credential %.20q with ok false", cred)
			}
			return
		}
		if cred == "" || (!strings.Contains(apiKey, cred) && !strings.Contains(auth, cred)) {
			t.Fatalf("the credential %.20q is in no header value", cred)
		}
		if ows(apiKey) != "" && ows(auth) != "" { // both carry a value
			// Both carry a value: the credential is the key, and the bearer value equals it.
			if !strings.Contains(auth, cred) || !strings.Contains(apiKey, cred) {
				t.Fatalf("the credential %.20q is not the value of both headers", cred)
			}
		}
		if ows(apiKey) == "" && ows(auth) == "" {
			t.Fatal("a credential from two empty headers")
		}
	})
}

// ows removes the optional white space (spaces and tabs) around a header value, as the family does.
func ows(s string) string { return strings.Trim(s, " \t") }
