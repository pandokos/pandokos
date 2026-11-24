package family

import (
	"errors"
	"strings"
	"testing"
)

// TestScanObject is T-516: the scanner moved from the openai family reports the members of one
// object with the byte ranges of their values, and refuses everything else.
func TestScanObject(t *testing.T) {
	t.Run("members in order with their value ranges", func(t *testing.T) {
		in := ` {"a" : 1 ,"b":"two","a":[3,{"x":4}], "c":{"d":null}} `
		ms, closeIdx, err := ScanObject([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		var keys, vals []string
		for _, m := range ms {
			keys = append(keys, m.Key)
			vals = append(vals, in[m.Start:m.End])
		}
		if strings.Join(keys, ",") != "a,b,a,c" || strings.Join(vals, "|") != `1|"two"|[3,{"x":4}]|{"d":null}` {
			t.Errorf("keys %v values %q", keys, vals)
		}
		if in[closeIdx] != '}' || strings.TrimSpace(in[closeIdx+1:]) != "" {
			t.Errorf("closing brace at %d", closeIdx)
		}
	})
	t.Run("an escaped key is the decoded key", func(t *testing.T) {
		ms, _, err := ScanObject([]byte(`{"model":"m"}`))
		if err != nil || len(ms) != 1 || ms[0].Key != "model" {
			t.Fatalf("%v %v", ms, err)
		}
	})
	t.Run("an empty object", func(t *testing.T) {
		ms, closeIdx, err := ScanObject([]byte(`{}`))
		if err != nil || len(ms) != 0 || closeIdx != 1 {
			t.Fatalf("%v %d %v", ms, closeIdx, err)
		}
	})
	for _, in := range []string{``, ` `, `[]`, `"x"`, `7`, `null`, `{"a":1} x`, `{"a":1}{"a":2}`, `{"a":1`, `{a:1}`, `{1:2}`, `{"a":}`, `{"a" 1}`} {
		if ms, _, err := ScanObject([]byte(in)); err == nil {
			t.Errorf("%q accepted: %v", in, ms)
		}
	}
}

func TestLastMember(t *testing.T) {
	body := []byte(`{"stream":true,"model":"m","stream":false}`)
	ms, _, err := ScanObject(body)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := LastMember(ms, "stream")
	if !ok || string(body[m.Start:m.End]) != "false" {
		t.Errorf("last stream member %v %v", m, ok)
	}
	if _, ok := LastMember(ms, "absent"); ok {
		t.Error("a member that is not there was found")
	}
	if _, ok := LastMember(nil, "model"); ok {
		t.Error("a member of no members")
	}
}

// TestParseModel is T-516: the table of sprint 3.1 section 5.8 with its reasons.
func TestParseModel(t *testing.T) {
	for _, c := range []struct {
		name, in, model, reason string
	}{
		{"minimal", `{"model":"gpt-x"}`, "gpt-x", ""},
		{"with other members", `{"messages":[{"model":"inner"}],"model":"outer","stream":true}`, "outer", ""},
		{"escapes are decoded", `{"model":"a\"b\\cé\nA"}`, "a\"b\\cé\nA", ""},
		{"an escaped key", `{"model":"m"}`, "m", ""},
		{"missing", `{}`, "", "model_missing"},
		{"only nested", `{"messages":[{"model":"m"}]}`, "", "model_missing"},
		{"duplicate", `{"model":"a","model":"b"}`, "", "model_duplicate"},
		{"duplicate with another type", `{"model":"a","model":1}`, "", "model_duplicate"},
		{"duplicate through an escaped key", `{"model":"a","model":"b"}`, "", "model_duplicate"},
		{"a number", `{"model":1}`, "", "model_not_string"},
		{"null", `{"model":null}`, "", "model_not_string"},
		{"a boolean", `{"model":true}`, "", "model_not_string"},
		{"an array", `{"model":["a"]}`, "", "model_not_string"},
		{"an object", `{"model":{"a":1}}`, "", "model_not_string"},
		{"the empty string", `{"model":""}`, "", "model_not_string"},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := []byte(c.in)
			ms, _, err := ScanObject(body)
			if err != nil {
				t.Fatal(err)
			}
			model, m, err := ParseModel(body, ms)
			if c.reason != "" {
				if !errors.Is(err, ErrInvalidRequest) || InvalidReason(err) != c.reason {
					t.Fatalf("error %v reason %q, want %q", err, InvalidReason(err), c.reason)
				}
				if strings.Contains(err.Error(), c.in) {
					t.Errorf("the error quotes the body: %v", err)
				}
				return
			}
			if err != nil || model != c.model {
				t.Fatalf("model %q error %v, want %q", model, err, c.model)
			}
			if body[m.Start] != '"' {
				t.Errorf("the member range starts at %q", body[m.Start])
			}
		})
	}
}

func TestIsLiteralSpliceEncodeString(t *testing.T) {
	body := []byte(`{"model":"m","stream":true,"x":"true"}`)
	ms, _, _ := ScanObject(body)
	stream, _ := LastMember(ms, "stream")
	x, _ := LastMember(ms, "x")
	if !IsLiteral(body, stream, "true") || IsLiteral(body, stream, "false") || IsLiteral(body, x, "true") {
		t.Error("IsLiteral is not exact")
	}
	orig := append([]byte(nil), body...)
	model, _ := LastMember(ms, "model")
	out := Splice(body, model.Start, model.End, EncodeString("a<b>&\"c\\é\n"))
	if string(out) != `{"model":"a<b>&\"c\\é\n","stream":true,"x":"true"}` {
		t.Errorf("splice: %s", out)
	}
	if string(body) != string(orig) {
		t.Error("Splice changed its input")
	}
	if got := Splice([]byte("abc"), 1, 1, "XY"); string(got) != "aXYbc" {
		t.Errorf("an insertion: %s", got)
	}
	if got := Splice([]byte("abc"), 0, 3, ""); string(got) != "" {
		t.Errorf("a deletion: %q", got)
	}
	if got := EncodeString("plain"); got != `"plain"` {
		t.Errorf("EncodeString: %s", got)
	}
}

// TestCaseConflict is T-1095: a member that equals a name of the read set when case is ignored and
// is not spelled exactly so is a conflict; exact names, names outside the set and nested members
// are not.
func TestCaseConflict(t *testing.T) {
	read := []string{"model", "stream", "max_tokens"}
	for _, c := range []struct {
		name, in string
		want     bool
	}{
		{"the exact names", `{"model":"m","stream":true,"max_tokens":1}`, false},
		{"an empty object", `{}`, false},
		{"a capital first letter", `{"Model":"m"}`, true},
		{"all capitals", `{"MODEL":"m"}`, true},
		{"a letter in the middle", `{"sTream":true}`, true},
		{"beside the exact name", `{"model":"m","Model":"n"}`, true},
		{"before the exact name", `{"MODEL":"n","model":"m"}`, true},
		{"a name with an underscore", `{"Max_Tokens":1}`, true},
		{"a letter that folds to an ASCII one (long s)", "{\"ſtream\":true}", true},
		{"the Kelvin sign inside a name of the set", "{\"max_toKens\":1}", true},
		{"an escaped spelling of the exact name", `{"model":"m"}`, false},
		{"an escaped spelling of another case", `{"Model":"m"}`, true},
		{"a name outside the set", `{"Messages":[],"Temperature":1}`, false},
		{"a name that only contains one of the set", `{"models":1,"model_x":2,"streams":3}`, false},
		{"a nested member in another case", `{"model":"m","messages":[{"Model":"x","Stream":true}],"x":{"MODEL":1}}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ms, _, err := ScanObject([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if got := CaseConflict(ms, read...); got != c.want {
				t.Errorf("CaseConflict(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
	t.Run("an empty read set", func(t *testing.T) {
		ms, _, _ := ScanObject([]byte(`{"Model":"m"}`))
		if CaseConflict(ms) {
			t.Error("a conflict with nothing to read")
		}
	})
	t.Run("no members", func(t *testing.T) {
		if CaseConflict(nil, read...) {
			t.Error("a conflict in no members")
		}
	})
}
