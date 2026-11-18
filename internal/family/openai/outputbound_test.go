package openai

import (
	"bytes"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
)

type boundCase struct {
	name, op, in string
	want         family.OutputBound
}

var boundCases = []boundCase{
	// chat.completions
	{"chat: nothing set", OperationChatCompletions, `{"model":"m"}`, family.OutputBound{Choices: 1}},
	{"chat: max_tokens", OperationChatCompletions, `{"model":"m","max_tokens":100}`, family.OutputBound{Tokens: 100, Choices: 1}},
	{"chat: max_completion_tokens", OperationChatCompletions, `{"model":"m","max_completion_tokens":200}`, family.OutputBound{Tokens: 200, Choices: 1}},
	{"chat: max_completion_tokens wins over max_tokens", OperationChatCompletions, `{"model":"m","max_tokens":50,"max_completion_tokens":200}`, family.OutputBound{Tokens: 200, Choices: 1}},
	{"chat: max_completion_tokens wins in either order", OperationChatCompletions, `{"model":"m","max_completion_tokens":200,"max_tokens":50}`, family.OutputBound{Tokens: 200, Choices: 1}},
	{"chat: n", OperationChatCompletions, `{"model":"m","max_tokens":10,"n":3}`, family.OutputBound{Tokens: 10, Choices: 3}},
	{"chat: n alone", OperationChatCompletions, `{"model":"m","n":2}`, family.OutputBound{Choices: 2}},
	{"chat: n at 128", OperationChatCompletions, `{"model":"m","n":128}`, family.OutputBound{Choices: 128}},
	{"chat: n above 128 is not set", OperationChatCompletions, `{"model":"m","n":129}`, family.OutputBound{Choices: 1}},
	{"chat: n zero is not set", OperationChatCompletions, `{"model":"m","n":0}`, family.OutputBound{Choices: 1}},
	{"chat: the last occurrence decides", OperationChatCompletions, `{"model":"m","max_tokens":5,"max_tokens":9}`, family.OutputBound{Tokens: 9, Choices: 1}},
	{"chat: the last occurrence decides, even when it is not a number", OperationChatCompletions, `{"model":"m","max_tokens":9,"max_tokens":"x"}`, family.OutputBound{Choices: 1}},
	{"chat: the last occurrence of n decides", OperationChatCompletions, `{"model":"m","n":4,"n":2}`, family.OutputBound{Choices: 2}},
	{"chat: 12 digits", OperationChatCompletions, `{"model":"m","max_tokens":999999999999}`, family.OutputBound{Tokens: 999999999999, Choices: 1}},
	{"chat: 13 digits is not set", OperationChatCompletions, `{"model":"m","max_tokens":1000000000000}`, family.OutputBound{Choices: 1}},
	{"chat: negative is not set", OperationChatCompletions, `{"model":"m","max_tokens":-5}`, family.OutputBound{Choices: 1}},
	{"chat: zero is not set", OperationChatCompletions, `{"model":"m","max_tokens":0}`, family.OutputBound{Choices: 1}},
	{"chat: a fraction is not set", OperationChatCompletions, `{"model":"m","max_tokens":5.0}`, family.OutputBound{Choices: 1}},
	{"chat: an exponent is not set", OperationChatCompletions, `{"model":"m","max_tokens":1e3}`, family.OutputBound{Choices: 1}},
	{"chat: a string is not set", OperationChatCompletions, `{"model":"m","max_tokens":"5"}`, family.OutputBound{Choices: 1}},
	{"chat: null is not set", OperationChatCompletions, `{"model":"m","max_tokens":null}`, family.OutputBound{Choices: 1}},
	{"chat: a boolean is not set", OperationChatCompletions, `{"model":"m","max_tokens":true}`, family.OutputBound{Choices: 1}},
	{"chat: a nested member of the same name is not read", OperationChatCompletions, `{"model":"m","metadata":{"max_tokens":77,"n":5}}`, family.OutputBound{Choices: 1}},
	{"chat: a nested member does not hide the top-level one", OperationChatCompletions, `{"model":"m","x":{"max_tokens":77},"max_tokens":6}`, family.OutputBound{Tokens: 6, Choices: 1}},
	{"chat: max_output_tokens is not a member of this operation", OperationChatCompletions, `{"model":"m","max_output_tokens":6}`, family.OutputBound{Choices: 1}},
	// responses
	{"responses: nothing set", OperationResponses, `{"model":"m"}`, family.OutputBound{Choices: 1}},
	{"responses: max_output_tokens", OperationResponses, `{"model":"m","max_output_tokens":300}`, family.OutputBound{Tokens: 300, Choices: 1}},
	{"responses: n is not read", OperationResponses, `{"model":"m","max_output_tokens":300,"n":4}`, family.OutputBound{Tokens: 300, Choices: 1}},
	{"responses: max_tokens is not read", OperationResponses, `{"model":"m","max_tokens":300}`, family.OutputBound{Choices: 1}},
	{"responses: a fraction is not set", OperationResponses, `{"model":"m","max_output_tokens":3.5}`, family.OutputBound{Choices: 1}},
	// embeddings
	{"embeddings: no output", OperationEmbeddings, `{"model":"m","input":"x"}`, family.OutputBound{None: true, Choices: 1}},
}

func boundOf(t *testing.T, op, in string) family.OutputBound {
	t.Helper()
	body := []byte(in)
	orig := append([]byte(nil), body...)
	r, err := ParseOperation(op, body)
	if err != nil {
		t.Fatalf("%s: %v", in, err)
	}
	if !bytes.Equal(body, orig) {
		t.Fatalf("%s: the body was changed", in)
	}
	got := r.OutputBound()
	if !bytes.Equal(body, orig) {
		t.Fatalf("%s: OutputBound changed the body", in)
	}
	return got
}

// TestOutputBound is T-627 for the openai family.
func TestOutputBound(t *testing.T) {
	for _, c := range boundCases {
		t.Run(c.name, func(t *testing.T) {
			got := boundOf(t, c.op, c.in)
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
	f.Add([]byte(`{"model":"m","max_tokens":99999999999999999999,"n":-1}`), uint8(0))
	f.Fuzz(func(t *testing.T, in []byte, which uint8) {
		op := []string{OperationChatCompletions, OperationResponses, OperationEmbeddings}[int(which)%3]
		orig := append([]byte(nil), in...)
		r, err := ParseOperation(op, in)
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
