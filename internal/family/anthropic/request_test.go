package anthropic_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
	"github.com/pandokos/pandokos/test/fixtures"
)

type parseCase struct {
	name, in string
	reason   string // "" means accepted
	model    string
	stream   bool
}

var parseCases = []parseCase{
	{"minimal", `{"model":"claude-sonnet-4-5"}`, "", "claude-sonnet-4-5", false},
	{"a real-looking body", `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"` + fixtures.CanaryPrompt + `"}]}`, "", "claude-sonnet-4-5", false},
	{"stream true", `{"model":"m","stream":true}`, "", "m", true},
	{"stream false", `{"model":"m","stream":false}`, "", "m", false},
	{"the last stream member counts", `{"stream":true,"model":"m","stream":false}`, "", "m", false},
	{"the last stream member counts, true", `{"stream":false,"model":"m","stream":true}`, "", "m", true},
	{"stream as a string is not true", `{"model":"m","stream":"true"}`, "", "m", false},
	{"stream as a number is not true", `{"model":"m","stream":1}`, "", "m", false},
	{"stream null", `{"model":"m","stream":null}`, "", "m", false},
	{"white space", " \n{ \"model\" :\t\"m\" , \"stream\" : true }\n ", "", "m", true},
	{"nested model members are ignored", `{"model":"outer","messages":[{"model":"inner"}],"tools":{"model":"x"}}`, "", "outer", false},
	{"nested stream members are ignored", `{"model":"m","metadata":{"stream":true}}`, "", "m", false},
	{"escapes in the value are decoded", `{"model":"a\"b\\cé\nA"}`, "", "a\"b\\cé\nA", false},
	{"an escaped key is the key", `{"model":"m","stream":true}`, "", "m", true},

	{"an array", `[{"model":"m"}]`, "not_object", "", false},
	{"a string", `"model"`, "not_object", "", false},
	{"a number", `7`, "not_object", "", false},
	{"null", `null`, "not_object", "", false},
	{"empty input", ``, "not_object", "", false},
	{"white space only", " \n", "not_object", "", false},
	{"trailing data", `{"model":"m"} x`, "not_object", "", false},
	{"a second object", `{"model":"m"}{"model":"n"}`, "not_object", "", false},
	{"truncated", `{"model":"m"`, "not_object", "", false},
	{"invalid JSON", `{model:"m"}`, "not_object", "", false},

	{"an empty object", `{}`, "model_missing", "", false},
	{"other members only", `{"messages":[],"stream":true}`, "model_missing", "", false},
	{"model only nested", `{"messages":[{"model":"m"}]}`, "model_missing", "", false},

	{"two model members", `{"model":"a","model":"b"}`, "model_duplicate", "", false},
	{"two equal model members", `{"model":"a","x":1,"model":"a"}`, "model_duplicate", "", false},
	{"a duplicate whose second value is not a string", `{"model":"a","model":1}`, "model_duplicate", "", false},

	{"a number", `{"model":1}`, "model_not_string", "", false},
	{"null", `{"model":null}`, "model_not_string", "", false},
	{"a boolean", `{"model":true}`, "model_not_string", "", false},
	{"an array", `{"model":["a"]}`, "model_not_string", "", false},
	{"an object", `{"model":{"name":"a"}}`, "model_not_string", "", false},
	{"the empty string", `{"model":""}`, "model_not_string", "", false},
}

var operations = []string{anthropic.OperationMessages, anthropic.OperationCountTokens}

// TestParseRequest is T-520: the model rules and their reasons for both operations, the
// stream rule, and what Prepare does.
func TestParseRequest(t *testing.T) {
	for _, op := range operations {
		for _, c := range parseCases {
			t.Run(op+"/"+c.name, func(t *testing.T) {
				in := []byte(c.in)
				orig := append([]byte(nil), in...)
				r, err := anthropic.ParseOperation(op, in)
				if !bytes.Equal(in, orig) {
					t.Fatal("ParseOperation modified its input")
				}
				if c.reason != "" {
					if err == nil || r != nil {
						t.Fatalf("accepted: %v %v", r, err)
					}
					if !errors.Is(err, family.ErrInvalidRequest) {
						t.Errorf("the error does not wrap ErrInvalidRequest: %v", err)
					}
					if got := family.InvalidReason(err); got != c.reason {
						t.Errorf("reason %q, want %q", got, c.reason)
					}
					if len(c.in) > 12 && strings.Contains(err.Error(), c.in) {
						t.Errorf("the error quotes the body: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				wantStream := c.stream && op == anthropic.OperationMessages // counting never streams
				if r.Model() != c.model || r.Stream() != wantStream {
					t.Errorf("model %q stream %v, want %q %v", r.Model(), r.Stream(), c.model, wantStream)
				}
			})
		}
	}
	t.Run("the error never holds the body", func(t *testing.T) {
		_, err := anthropic.ParseOperation(anthropic.OperationMessages, []byte(`{"messages":[{"content":"`+fixtures.CanaryPrompt+`"}]}`))
		if err == nil || strings.Contains(err.Error(), fixtures.CanaryPrompt) {
			t.Errorf("%v", err)
		}
	})
	t.Run("a Request is a family.Request", func(*testing.T) {
		var _ family.Request = (*anthropic.Request)(nil)
	})
}

type prepareCase struct {
	name, in, upstream, out string // out == "" means the input slice itself
}

var prepareCases = []prepareCase{
	{"the model alone", `{"model":"nick","max_tokens":1}`, "claude-sonnet-4-5", `{"model":"claude-sonnet-4-5","max_tokens":1}`},
	{"white space around the value", `{"model" :  "nick" ,"x":1}`, "u", `{"model" :  "u" ,"x":1}`},
	{"the model last, nested models before", `{"messages":[{"model":"nested"}],"model":"nick"}`, "u", `{"messages":[{"model":"nested"}],"model":"u"}`},
	{"an escaped key", `{"model":"nick"}`, "u", `{"model":"u"}`},
	{"a quote and a backslash", `{"model":"nick"}`, `we"ird\id`, `{"model":"we\"ird\\id"}`},
	{"HTML characters are not escaped", `{"model":"nick"}`, `a<b>&c`, `{"model":"a<b>&c"}`},
	{"non-ASCII characters are not escaped", `{"model":"nick"}`, "modèle-é", `{"model":"modèle-é"}`},
	{"the same name", `{"model":"nick"}`, "nick", ""},
	{"an empty upstream identifier", `{"model":"nick"}`, "", ""},
	{"a stream is left alone", `{"model":"nick","stream":true}`, "nick", ""},
	{"a stream with another model", `{"model":"nick","stream":true,"max_tokens":5}`, "u", `{"model":"u","stream":true,"max_tokens":5}`},
	{"stream options are not an Anthropic member and are left alone", `{"model":"nick","stream":true,"stream_options":{"x":1}}`, "nick", ""},
}

// TestPrepare is T-520: the body is forwarded as received, only the value of model changes, and
// strip is always false.
func TestPrepare(t *testing.T) {
	for _, op := range operations {
		for _, c := range prepareCases {
			t.Run(op+"/"+c.name, func(t *testing.T) {
				in := []byte(c.in)
				orig := append([]byte(nil), in...)
				r, err := anthropic.ParseOperation(op, in)
				if err != nil {
					t.Fatal(err)
				}
				for _, streamUsage := range []bool{false, true} {
					out, strip := r.Prepare(family.Preparation{UpstreamModel: c.upstream, StreamUsage: streamUsage})
					if strip {
						t.Errorf("strip is true (StreamUsage %v)", streamUsage)
					}
					if !bytes.Equal(in, orig) {
						t.Fatal("Prepare modified the caller's bytes")
					}
					if c.out == "" {
						if !bytes.Equal(out, in) || &out[0] != &in[0] {
							t.Fatalf("an unchanged body is not the input slice: %q", out)
						}
						continue
					}
					if string(out) != c.out {
						t.Errorf("got  %q\nwant %q", out, c.out)
					}
					var m struct {
						Model string `json:"model"`
					}
					if err := json.Unmarshal(out, &m); err != nil || m.Model != c.upstream {
						t.Errorf("the output decodes to model %q (%v), want %q", m.Model, err, c.upstream)
					}
				}
			})
		}
	}
	t.Run("a body is never given an option", func(t *testing.T) {
		r, err := anthropic.ParseOperation(anthropic.OperationMessages, []byte(`{"model":"m","stream":true}`))
		if err != nil {
			t.Fatal(err)
		}
		out, strip := r.Prepare(family.Preparation{StreamUsage: true})
		if string(out) != `{"model":"m","stream":true}` || strip {
			t.Errorf("%q %v", out, strip)
		}
	})
	t.Run("Prepare may be called twice with the same result", func(t *testing.T) {
		r, err := anthropic.ParseOperation(anthropic.OperationMessages, []byte(`{"model":"nick"}`))
		if err != nil {
			t.Fatal(err)
		}
		a, _ := r.Prepare(family.Preparation{UpstreamModel: "u"})
		b, _ := r.Prepare(family.Preparation{UpstreamModel: "u"})
		if !bytes.Equal(a, b) {
			t.Errorf("%q then %q", a, b)
		}
	})
}

// FuzzAnthropicParseRequest is T-524: ParseOperation never panics; what it accepts,
// encoding/json reads the same; the output of Prepare decodes to the upstream model and to the
// same other members, and strip is false.
func FuzzAnthropicParseRequest(f *testing.F) {
	for i, c := range parseCases {
		f.Add([]byte(c.in), uint8(i))
	}
	for i, c := range prepareCases {
		f.Add([]byte(c.in), uint8(i))
	}
	f.Add([]byte(`{"model":"m","stream":true}`), uint8(1))
	f.Fuzz(func(t *testing.T, in []byte, which uint8) {
		op := operations[int(which)%len(operations)]
		orig := append([]byte(nil), in...)
		r, err := anthropic.ParseOperation(op, in)
		if !bytes.Equal(in, orig) {
			t.Fatal("input was modified")
		}
		if err != nil {
			if !errors.Is(err, family.ErrInvalidRequest) || family.InvalidReason(err) == "" {
				t.Fatalf("a refusal without a reason: %v", err)
			}
			return
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(in, &members); err != nil {
			t.Fatalf("ParseOperation accepted what encoding/json refuses: %v", err)
		}
		var model string
		if err := json.Unmarshal(members["model"], &model); err != nil || model != r.Model() || model == "" {
			t.Fatalf("encoding/json reads model %q (%v), ParseOperation read %q", model, err, r.Model())
		}
		if op == anthropic.OperationCountTokens && r.Stream() {
			t.Fatal("token counting streams")
		}
		const upstream = `synthetic"up\stream`
		out, strip := r.Prepare(family.Preparation{UpstreamModel: upstream, StreamUsage: true})
		if strip {
			t.Fatal("strip is true")
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("the output is not a JSON object: %v", err)
		}
		var gotModel string
		if err := json.Unmarshal(got["model"], &gotModel); err != nil || gotModel != upstream {
			t.Fatalf("the output decodes to model %q (%v)", gotModel, err)
		}
		for k, v := range members {
			if k != "model" && !bytes.Equal(got[k], v) {
				t.Fatalf("member %q changed", k)
			}
		}
		if len(got) != len(members) {
			t.Fatalf("the output has %d members, the input %d", len(got), len(members))
		}
	})
}
