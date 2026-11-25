package auditchain_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/pandokos/pandokos/internal/auditchain"
)

// FuzzCanonical is T-1116: Canonical never panics and is deterministic; an input that is not one
// JSON value fails; the output of an accepted input does not depend on insignificant white space
// (the compact and the indented forms of the same value give the same bytes).
func FuzzCanonical(f *testing.F) {
	for _, s := range []string{
		`{}`, `[]`, `null`, `true`, `false`, `0`, `-1.50e+3`, `"s"`, `"éé😀"`, `"\ud800"`,
		`{"b":1,"a":[1,2,{"z":null,"y":"x"}],"c":{"d":true}}`, ` { "b" : 1 ,"a" : [ 1 , 2 ] } `,
		"{\n\t\"action\": \"tenant.create\",\n\t\"detail\": {\"name\": \"synthetic\", \"n\": 12345678901234567890}\n}\n",
		`{"a":1,"a":2}`, `{"a":1}{"a":2}`, `{"a":1} x`, `{"a":`, `{"a"}`, `{1:2}`, `[1,]`, `01`, `1.`, `.5`, `+1`, `NaN`, `Infinity`, `'a'`, ``, ` `,
		`{"é":1,"e":2,"é":3}`, `{"a":1,"a":2}`, `[[[[[[[[[[1]]]]]]]]]]`, `1e400`, `-0`, `0.0000000000000000000000000001`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		if len(in) > 1<<20 {
			t.Skip()
		}
		got, err := auditchain.Canonical(in)
		again, err2 := auditchain.Canonical(in)
		if (err == nil) != (err2 == nil) || !bytes.Equal(got, again) {
			t.Fatal("Canonical is not deterministic")
		}
		if err != nil {
			if got != nil {
				t.Fatalf("an encoding and an error: %v", err)
			}
			return
		}
		if len(bytes.TrimSpace(in)) == 0 || !json.Valid(in) {
			t.Fatalf("an input that is not one JSON value was accepted: %.60q", in)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, in); err != nil {
			t.Fatalf("Compact refuses what Canonical accepted: %v", err)
		}
		var indented bytes.Buffer
		if err := json.Indent(&indented, in, "\t", "  "); err != nil {
			t.Fatalf("Indent refuses what Canonical accepted: %v", err)
		}
		for name, variant := range map[string][]byte{
			"compact": compact.Bytes(), "indented": indented.Bytes(), "padded": append(append([]byte(" \n\t"), in...), " \r\n"...),
		} {
			v, err := auditchain.Canonical(variant)
			if err != nil || !bytes.Equal(v, got) {
				t.Fatalf("the %s form of the value encodes differently (%v)", name, err)
			}
		}
		if d1, err := auditchain.DetailDigest(in); err != nil {
			t.Fatalf("DetailDigest refuses what Canonical accepted: %v", err)
		} else if d2, _ := auditchain.DetailDigest(compact.Bytes()); d1 != d2 {
			t.Fatal("the digest depends on white space")
		}
	})
}
