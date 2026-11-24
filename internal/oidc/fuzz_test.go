package oidc

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// groupPaths are the claim paths FuzzProviderAnswers reads groups at.
var groupPaths = []string{"groups", "roles.admin", "a.b.c.d", "a.b.c.d.e", "", "realm_access.roles", "G"}

// FuzzProviderAnswers is T-1108: what the relying party reads of the answers of the people's
// provider, for each reading (the second argument selects it): the token endpoint's answer, the
// audience claim, a string claim and the groups claim. An ID token string is returned only for a
// JSON object with a non-empty string id_token; audienceCount agrees with a plain decoding;
// stringClaim returns what encoding/json reads of a string; groups never exceed their bound and a
// shape that gives none is named by a JSON type, never by content.
func FuzzProviderAnswers(f *testing.F) {
	for _, s := range []string{
		`{"access_token":"synthetic-access","token_type":"Bearer","id_token":"synthetic.id.token","refresh_token":"synthetic-refresh"}`,
		`{"id_token":""}`, `{"id_token":null}`, `{"id_token":7}`, `{"id_token":["a"]}`, `{"ID_TOKEN":"synthetic.id.token"}`,
		`{"id_token":"first","id_token":"second"}`, `{}`, `[]`, `"id_token"`, `null`, ``, `{"id_token":"x"} trailing`,
		`"synthetic"`, `["a","b","c"]`, `[]`, `["a",["b"],{"c":1}]`, `7`, `true`, `null`, `{"a":1}`, `"é\\u0041\\n"`, `"\\ud800"`,
		`{"groups":["a","b"],"roles":{"admin":["x"]},"a":{"b":{"c":{"d":["deep"]}}}}`,
		`{"groups":"single","G":["upper"]}`, `{"groups":[1,2]}`, `{"groups":{"x":1}}`, `{"groups":7}`, `{"groups":null}`, `{"groups":[]}`,
		`{"groups":""}`, `{"roles":"not an object"}`, `{"realm_access":{"roles":["r1","r2"]}}`,
	} {
		for sel := 0; sel < 4; sel++ {
			f.Add([]byte(s), uint8(sel))
		}
	}
	big := make([]string, 300)
	for i := range big {
		big[i] = "g" + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
	}
	list, _ := json.Marshal(map[string]any{"groups": big})
	f.Add(list, uint8(3))

	f.Fuzz(func(t *testing.T, data []byte, sel uint8) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		switch sel % 4 {
		case 0: // the answer of the token endpoint
			token, ok := parseTokenAnswer(data)
			if !ok {
				if token != "" {
					t.Fatalf("a token %q with ok false", token)
				}
				return
			}
			if token == "" {
				t.Fatal("an empty ID token accepted")
			}
			var members map[string]json.RawMessage
			if json.Unmarshal(data, &members) != nil {
				t.Fatalf("an ID token returned for what is not a JSON object: %.40q", data)
			}
			found := false
			for name, raw := range members {
				var s string
				if strings.EqualFold(name, "id_token") && raw[0] == '"' && json.Unmarshal(raw, &s) == nil && s == token {
					found = true
				}
			}
			if !found {
				t.Fatalf("the ID token %.20q is no string member id_token of the answer", token)
			}
		case 1: // the audience claim
			var raw json.RawMessage
			if json.Unmarshal(data, &raw) != nil {
				return
			}
			n := audienceCount(raw)
			var plain any
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber() // a number out of the range of a float is still a value
			if err := dec.Decode(&plain); err != nil {
				t.Fatalf("a value RawMessage kept does not decode: %v", err)
			}
			want := 1
			if list, ok := plain.([]any); ok {
				want = len(list)
			}
			if n != want {
				t.Fatalf("audienceCount = %d, a plain decoding gives %d: %.60q", n, want, raw)
			}
		case 2: // a string claim: the name, the e-mail address
			var raw json.RawMessage
			if json.Unmarshal(data, &raw) != nil {
				return
			}
			s, ok := stringClaim(raw)
			var plain string
			plainOK := raw[0] == '"' && json.Unmarshal(raw, &plain) == nil
			if ok != plainOK || (ok && s != plain) {
				t.Fatalf("stringClaim = %q, %v; a plain decoding gives %q, %v", s, ok, plain, plainOK)
			}
		case 3: // the groups claim
			var claims map[string]json.RawMessage
			if json.Unmarshal(data, &claims) != nil {
				return
			}
			for _, path := range groupPaths {
				groups, shape := readGroups(claims, path)
				if len(groups) > maxGroups {
					t.Fatalf("%d groups, at most %d", len(groups), maxGroups)
				}
				if len(groups) > 0 && shape != "" {
					t.Fatalf("groups and a bad shape %q together", shape)
				}
				switch shape {
				case "", "object", "array", "string", "boolean", "number", "array of other values":
				default:
					t.Fatalf("the shape %q is not the name of a JSON type", shape)
				}
				if got := Groups(claims, path); len(got) != len(groups) {
					t.Fatalf("Groups and readGroups differ: %d and %d", len(got), len(groups))
				}
			}
		}
	})
}
