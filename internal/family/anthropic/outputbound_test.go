package anthropic_test

import (
	"bytes"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
)

type boundCase struct {
	name, op, in string
	want         family.OutputBound
}

var boundCases = []boundCase{
	{"messages: nothing set", anthropic.OperationMessages, `{"model":"m"}`, family.OutputBound{Choices: 1}},
	{"messages: max_tokens", anthropic.OperationMessages, `{"model":"m","max_tokens":1024}`, family.OutputBound{Tokens: 1024, Choices: 1}},
	{"messages: n is not read", anthropic.OperationMessages, `{"model":"m","max_tokens":5,"n":4}`, family.OutputBound{Tokens: 5, Choices: 1}},
	{"messages: the last occurrence decides", anthropic.OperationMessages, `{"model":"m","max_tokens":5,"max_tokens":9}`, family.OutputBound{Tokens: 9, Choices: 1}},
	{"messages: the last occurrence decides, even when it is not a number", anthropic.OperationMessages, `{"model":"m","max_tokens":9,"max_tokens":null}`, family.OutputBound{Choices: 1}},
	{"messages: 12 digits", anthropic.OperationMessages, `{"model":"m","max_tokens":999999999999}`, family.OutputBound{Tokens: 999999999999, Choices: 1}},
	{"messages: 13 digits is not set", anthropic.OperationMessages, `{"model":"m","max_tokens":1000000000000}`, family.OutputBound{Choices: 1}},
	{"messages: negative is not set", anthropic.OperationMessages, `{"model":"m","max_tokens":-1}`, family.OutputBound{Choices: 1}},
	{"messages: zero is not set", anthropic.OperationMessages, `{"model":"m","max_tokens":0}`, family.OutputBound{Choices: 1}},
	{"messages: a fraction is not set", anthropic.OperationMessages, `{"model":"m","max_tokens":10.0}`, family.OutputBound{Choices: 1}},
	{"messages: an exponent is not set", anthropic.OperationMessages, `{"model":"m","max_tokens":1E2}`, family.OutputBound{Choices: 1}},
	{"messages: a string is not set", anthropic.OperationMessages, `{"model":"m","max_tokens":"10"}`, family.OutputBound{Choices: 1}},
	{"messages: null is not set", anthropic.OperationMessages, `{"model":"m","max_tokens":null}`, family.OutputBound{Choices: 1}},
	{"messages: a nested member of the same name is not read", anthropic.OperationMessages, `{"model":"m","metadata":{"max_tokens":77}}`, family.OutputBound{Choices: 1}},
	{"messages: a nested member does not hide the top-level one", anthropic.OperationMessages, `{"model":"m","x":[{"max_tokens":77}],"max_tokens":6}`, family.OutputBound{Tokens: 6, Choices: 1}},
	{"count_tokens: no output", anthropic.OperationCountTokens, `{"model":"m","messages":[]}`, family.OutputBound{None: true, Choices: 1}},
	{"count_tokens: max_tokens is ignored", anthropic.OperationCountTokens, `{"model":"m","max_tokens":5}`, family.OutputBound{None: true, Choices: 1}},
}

func operation(t testing.TB, name string) family.Operation {
	t.Helper()
	for _, op := range anthropic.Adapter().Operations() {
		if op.Name == name {
			return op
		}
	}
	t.Fatalf("no operation %q", name)
	return family.Operation{}
}

// TestOutputBound is T-627 for the anthropic family.
func TestOutputBound(t *testing.T) {
	for _, c := range boundCases {
		t.Run(c.name, func(t *testing.T) {
			body := []byte(c.in)
			orig := append([]byte(nil), body...)
			r, err := anthropic.Adapter().ParseRequest(operation(t, c.op), body)
			if err != nil {
				t.Fatalf("%s: %v", c.in, err)
			}
			got := r.OutputBound()
			if !bytes.Equal(body, orig) {
				t.Fatalf("%s: the body was changed", c.in)
			}
			if c.want.None {
				if !got.None || got.Choices < 1 {
					t.Errorf("%+v, want no output", got)
				}
				return
			}
			if got != c.want {
				t.Errorf("%s: %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

// FuzzOutputBound is T-627: no panic, Choices at least 1, Tokens never negative, the body unchanged.
func FuzzOutputBound(f *testing.F) {
	for i, c := range boundCases {
		f.Add([]byte(c.in), uint8(i))
	}
	f.Add([]byte(`{"model":"m","max_tokens":99999999999999999999}`), uint8(0))
	f.Fuzz(func(t *testing.T, in []byte, which uint8) {
		ops := anthropic.Adapter().Operations()
		orig := append([]byte(nil), in...)
		r, err := anthropic.Adapter().ParseRequest(ops[int(which)%len(ops)], in)
		if err != nil {
			return
		}
		b := r.OutputBound()
		if !bytes.Equal(in, orig) {
			t.Fatal("the body was modified")
		}
		if b.Choices < 1 || b.Choices > 128 || b.Tokens < 0 || b.Tokens > 999999999999 {
			t.Fatalf("out of range: %+v for %q", b, in)
		}
	})
}
