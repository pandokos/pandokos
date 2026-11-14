package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
)

type prepareCase struct {
	in, out       string // out == "" means unchanged
	stream, strip bool
}

var prepareCases = []prepareCase{
	{`{"model":"m","messages":[]}`, "", false, false},
	{`{"model":"m","stream":false}`, "", false, false},
	{`{"model":"m", "stream": true }`, `{"model":"m", "stream": true ,"stream_options":{"include_usage":true}}`, true, true},
	{`{"stream":true,"stream_options":{"include_usage":true},"x":1}`, "", true, false},
	{`{"stream":true,"stream_options":{"include_usage": false},"x":1}`, `{"stream":true,"stream_options":{"include_usage": true},"x":1}`, true, true},
	{`{"stream":true,"stream_options": {"include_obfuscation":false},"x":1}`, `{"stream":true,"stream_options": {"include_usage":true,"include_obfuscation":false},"x":1}`, true, true},
	{`{"stream":true,"stream_options":{ }}`, `{"stream":true,"stream_options":{"include_usage":true }}`, true, true},
	{`{"stream":true,"stream_options":null,"x":"é<>"}`, `{"stream":true,"stream_options":{"include_usage":true},"x":"é<>"}`, true, true},
	{`{"stream":true,"stream_options":7}`, "", true, false},
	{`{"stream":true,"stream":false}`, "", false, false},
	{`{"stream":true,"stream_options":{"include_usage":true},"stream_options":{"a":1}}`, `{"stream":true,"stream_options":{"include_usage":true},"stream_options":{"include_usage":true,"a":1}}`, true, true},
	{`{"stream":"yes"}`, "", false, false},
	{`[1,2]`, "", false, false},
	{`{"stream":true`, "", false, false},
	{`{"stream":true} x`, "", false, false},
	{"{\n \"stream\": true,\n \"n\": 1.0e3\n}\n", "{\n \"stream\": true,\n \"n\": 1.0e3\n,\"stream_options\":{\"include_usage\":true}}\n", true, true},
	{``, "", false, false},
}

func TestPrepareRequest(t *testing.T) {
	for _, c := range prepareCases {
		t.Run(c.in, func(t *testing.T) {
			in := []byte(c.in)
			out, stream, strip := PrepareRequest(in)
			if stream != c.stream || strip != c.strip {
				t.Errorf("stream=%v strip=%v, want %v %v", stream, strip, c.stream, c.strip)
			}
			if c.out == "" {
				if !bytes.Equal(out, in) {
					t.Fatalf("changed: %q", out)
				}
				if len(in) > 0 && &out[0] != &in[0] {
					t.Error("unchanged result is not the input slice")
				}
				return
			}
			if string(out) != c.out {
				t.Errorf("got  %q\nwant %q", out, c.out)
			}
		})
	}
}

func FuzzPrepareRequest(f *testing.F) {
	for _, c := range prepareCases {
		f.Add([]byte(c.in))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		orig := append([]byte(nil), in...)
		out, _, strip := PrepareRequest(in)
		if !bytes.Equal(in, orig) {
			t.Fatal("input was modified")
		}
		if !strip {
			if !bytes.Equal(out, orig) {
				t.Fatal("output differs from input without strip")
			}
			return
		}
		var got, was map[string]json.RawMessage
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("output is not a JSON object: %v", err)
		}
		if err := json.Unmarshal(orig, &was); err != nil {
			t.Fatalf("input was not a JSON object but strip is set: %v", err)
		}
		var so struct {
			IncludeUsage bool `json:"include_usage"`
		}
		if err := json.Unmarshal(got["stream_options"], &so); err != nil || !so.IncludeUsage {
			t.Fatalf("stream_options.include_usage is not true: %s", got["stream_options"])
		}
		for k, v := range was {
			if k == "stream_options" {
				continue
			}
			if !bytes.Equal(got[k], v) {
				t.Fatalf("member %q changed", k)
			}
		}
	})
}

// parseCase is one row of the table of sprint 3.1, section 5.8 ("Parsing").
type parseCase struct {
	name, in string
	reason   string // "" means accepted
	model    string
	stream   bool
}

var parseCases = []parseCase{
	{"minimal", `{"model":"gpt-x"}`, "", "gpt-x", false},
	{"with messages", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, "", "gpt-x", false},
	{"stream true", `{"model":"m","stream":true}`, "", "m", true},
	{"stream false", `{"model":"m","stream":false}`, "", "m", false},
	{"the last stream member counts", `{"stream":true,"model":"m","stream":false}`, "", "m", false},
	{"the last stream member counts, true", `{"stream":false,"model":"m","stream":true}`, "", "m", true},
	{"stream as a string is not true", `{"model":"m","stream":"true"}`, "", "m", false},
	{"stream as a number is not true", `{"model":"m","stream":1}`, "", "m", false},
	{"stream null", `{"model":"m","stream":null}`, "", "m", false},
	{"white space", " \n{ \"model\" :\t\"m\" , \"stream\" : true }\n ", "", "m", true},
	{"an escaped key is the key", `{"model":"escaped"}`, "", "escaped", false},
	{"nested model members are ignored", `{"model":"outer","messages":[{"model":"inner"}],"tools":{"model":"x"}}`, "", "outer", false},
	{"escapes in the value are decoded", `{"model":"a\"b\\cé\nA"}`, "", "a\"b\\cé\nA", false},
	{"a slash in the name", `{"model":"meta/llama-3"}`, "", "meta/llama-3", false},

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
	{"a key that is not a string", `{1:"m"}`, "not_object", "", false},

	{"an empty object", `{}`, "model_missing", "", false},
	{"other members only", `{"messages":[],"stream":true}`, "model_missing", "", false},
	{"model only nested", `{"messages":[{"model":"m"}]}`, "model_missing", "", false},

	{"two model members", `{"model":"a","model":"b"}`, "model_duplicate", "", false},
	{"two equal model members", `{"model":"a","x":1,"model":"a"}`, "model_duplicate", "", false},
	{"the duplicate is an escaped key", `{"model":"a","model":"b"}`, "model_duplicate", "", false},
	{"both keys escaped", `{"model":"a","model":"b"}`, "model_duplicate", "", false},
	{"a duplicate whose second value is not a string", `{"model":"a","model":1}`, "model_duplicate", "", false},

	{"a number", `{"model":1}`, "model_not_string", "", false},
	{"null", `{"model":null}`, "model_not_string", "", false},
	{"a boolean", `{"model":true}`, "model_not_string", "", false},
	{"an array", `{"model":["a"]}`, "model_not_string", "", false},
	{"an object", `{"model":{"name":"a"}}`, "model_not_string", "", false},
	{"the empty string", `{"model":""}`, "model_not_string", "", false},
}

// TestParse is T-421: every row of the table of section 5.8.
func TestParse(t *testing.T) {
	for _, c := range parseCases {
		t.Run(c.name, func(t *testing.T) {
			in := []byte(c.in)
			orig := append([]byte(nil), in...)
			r, err := Parse(in)
			if !bytes.Equal(in, orig) {
				t.Fatal("Parse modified its input")
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
				if strings.Contains(err.Error(), "gpt") || (len(c.in) > 12 && strings.Contains(err.Error(), c.in)) {
					t.Errorf("the error quotes the body: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Model() != c.model || r.Stream() != c.stream {
				t.Errorf("model %q stream %v, want %q %v", r.Model(), r.Stream(), c.model, c.stream)
			}
		})
	}
	t.Run("a body with a canary is never in the error", func(t *testing.T) {
		const canary = "synthetic-canary-prompt-text"
		_, err := Parse([]byte(`{"messages":[{"content":"` + canary + `"}]}`))
		if err == nil || strings.Contains(err.Error(), canary) {
			t.Errorf("%v", err)
		}
	})
}

type prepareModelCase struct {
	name, in, upstream, out string // out == "" means the same slice
	strip                   bool
}

var prepareModelCases = []prepareModelCase{
	{"the model alone", `{"model":"nick","messages":[]}`, "upstream-id-1", `{"model":"upstream-id-1","messages":[]}`, false},
	{"white space around the value", `{"model" :  "nick" ,"x":1}`, "upstream-id-1", `{"model" :  "upstream-id-1" ,"x":1}`, false},
	{"the model last", `{"messages":[{"model":"nested"}],"model":"nick"}`, "u", `{"messages":[{"model":"nested"}],"model":"u"}`, false},
	{"an escaped value", `{"model":"nick"}`, "u", `{"model":"u"}`, false},
	{"an escaped key", `{"model":"nick"}`, "u", `{"model":"u"}`, false},
	{"the same name", `{"model":"nick"}`, "nick", "", false},
	{"an empty upstream identifier", `{"model":"nick"}`, "", "", false},
	{"the same name with an escaped value", `{"model":"nick"}`, "nick", "", false},
	{"a quote and a backslash", `{"model":"nick"}`, `we"ird\id`, `{"model":"we\"ird\\id"}`, false},
	{"HTML characters are not escaped", `{"model":"nick"}`, `a<b>&c`, `{"model":"a<b>&c"}`, false},
	{"non-ASCII characters are not escaped", `{"model":"nick"}`, "modèle-é", `{"model":"modèle-é"}`, false},
	{"a control character", `{"model":"nick"}`, "a\nb", `{"model":"a\nb"}`, false},

	{"usage added and the model replaced", `{"model":"nick","stream":true}`, "u", `{"model":"u","stream":true,"stream_options":{"include_usage":true}}`, true},
	{"usage added, the model after the stream options", `{"stream":true,"stream_options":{"include_obfuscation":false},"model":"nick"}`, "u", `{"stream":true,"stream_options":{"include_usage":true,"include_obfuscation":false},"model":"u"}`, true},
	{"usage added, the model before the stream options", `{"model":"nick","stream":true,"stream_options":{"include_obfuscation":false}}`, "u", `{"model":"u","stream":true,"stream_options":{"include_usage":true,"include_obfuscation":false}}`, true},
	{"stream options null", `{"model":"nick","stream":true,"stream_options":null}`, "u", `{"model":"u","stream":true,"stream_options":{"include_usage":true}}`, true},
	{"include_usage false", `{"model":"nick","stream":true,"stream_options":{"include_usage": false}}`, "u", `{"model":"u","stream":true,"stream_options":{"include_usage": true}}`, true},
	{"empty stream options", `{"stream":true,"stream_options":{ },"model":"nick"}`, "u", `{"stream":true,"stream_options":{"include_usage":true },"model":"u"}`, true},
	{"usage already requested", `{"model":"nick","stream":true,"stream_options":{"include_usage":true}}`, "u", `{"model":"u","stream":true,"stream_options":{"include_usage":true}}`, false},
	{"usage splice alone, same name", `{"model":"nick","stream":true}`, "nick", `{"model":"nick","stream":true,"stream_options":{"include_usage":true}}`, true},
	{"usage splice alone, no upstream", `{"model":"nick","stream":true}`, "", `{"model":"nick","stream":true,"stream_options":{"include_usage":true}}`, true},
	{"no stream, no change at all", `{"model":"nick","stream":false}`, "nick", "", false},
}

// TestPrepare is T-422: exact output bytes.
func TestPrepare(t *testing.T) {
	for _, c := range prepareModelCases {
		t.Run(c.name, func(t *testing.T) {
			in := []byte(c.in)
			orig := append([]byte(nil), in...)
			r, err := Parse(in)
			if err != nil {
				t.Fatal(err)
			}
			out, strip := r.Prepare(family.Preparation{UpstreamModel: c.upstream})
			if !bytes.Equal(in, orig) {
				t.Fatal("Prepare modified the caller's bytes")
			}
			if strip != c.strip {
				t.Errorf("strip = %v, want %v", strip, c.strip)
			}
			if c.out == "" {
				if !bytes.Equal(out, in) {
					t.Fatalf("changed: %q", out)
				}
				if &out[0] != &in[0] {
					t.Error("an unchanged result is not the input slice")
				}
				return
			}
			if string(out) != c.out {
				t.Errorf("got  %q\nwant %q", out, c.out)
			}
			var m struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("the output is not JSON: %v", err)
			}
			if c.upstream != "" && m.Model != c.upstream && c.upstream != r.Model() {
				t.Errorf("the output decodes to model %q, want %q", m.Model, c.upstream)
			}
		})
	}
	t.Run("Prepare may be called twice with the same result", func(t *testing.T) {
		r, err := Parse([]byte(`{"model":"nick","stream":true}`))
		if err != nil {
			t.Fatal(err)
		}
		a, _ := r.Prepare(family.Preparation{UpstreamModel: "u"})
		b, _ := r.Prepare(family.Preparation{UpstreamModel: "u"})
		if !bytes.Equal(a, b) {
			t.Errorf("%q then %q", a, b)
		}
	})
	t.Run("PrepareRequest keeps the usage splice when the model is the problem", func(t *testing.T) {
		out, stream, strip := PrepareRequest([]byte(`{"stream":true}`))
		if string(out) != `{"stream":true,"stream_options":{"include_usage":true}}` || !stream || !strip {
			t.Errorf("%q %v %v", out, stream, strip)
		}
	})
}

// FuzzParseRequest is T-423: Parse never panics; what it accepts, encoding/json reads the same;
// the output of Prepare decodes to the upstream model and to the same other members.
func FuzzParseRequest(f *testing.F) {
	for i, c := range prepareCases {
		f.Add([]byte(c.in), uint8(i))
	}
	for i, c := range parseCases {
		f.Add([]byte(c.in), uint8(i))
	}
	for i, c := range prepareModelCases {
		f.Add([]byte(c.in), uint8(i))
	}
	f.Add([]byte(`{"model":"m","background":true}`), uint8(1))
	f.Add([]byte(`{"model":"m","stream":true}`), uint8(2))
	f.Fuzz(func(t *testing.T, in []byte, which uint8) {
		op := []string{OperationChatCompletions, OperationResponses, OperationEmbeddings}[int(which)%3]
		orig := append([]byte(nil), in...)
		r, err := ParseOperation(op, in)
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
			t.Fatalf("Parse accepted what encoding/json refuses: %v", err)
		}
		var model string
		if err := json.Unmarshal(members["model"], &model); err != nil || model != r.Model() || model == "" {
			t.Fatalf("encoding/json reads model %q (%v), Parse read %q", model, err, r.Model())
		}
		const upstream = `synthetic"up\stream`
		out, _ := r.Prepare(family.Preparation{UpstreamModel: upstream})
		var got map[string]json.RawMessage
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("the output is not a JSON object: %v", err)
		}
		var gotModel string
		if err := json.Unmarshal(got["model"], &gotModel); err != nil || gotModel != upstream {
			t.Fatalf("the output decodes to model %q (%v)", gotModel, err)
		}
		for k, v := range members {
			if k == "model" || k == "stream_options" {
				continue
			}
			if !bytes.Equal(got[k], v) {
				t.Fatalf("member %q changed", k)
			}
		}
		if len(got) != len(members) && (len(got) != len(members)+1 || members["stream_options"] != nil) {
			t.Fatalf("the output has %d members, the input %d", len(got), len(members))
		}
	})
}

// TestParseOperation is T-457: the request rules of the three operations and what Prepare does.
func TestParseOperation(t *testing.T) {
	all := []string{OperationChatCompletions, OperationResponses, OperationEmbeddings}

	t.Run("the model rules apply to every operation", func(t *testing.T) {
		for _, op := range all {
			for _, c := range parseCases {
				_, err := ParseOperation(op, []byte(c.in))
				if c.reason == "" {
					if err != nil {
						t.Errorf("%s %s: %v", op, c.name, err)
					}
					continue
				}
				if !errors.Is(err, family.ErrInvalidRequest) || family.InvalidReason(err) != c.reason {
					t.Errorf("%s %s: %v (reason %q), want %q", op, c.name, err, family.InvalidReason(err), c.reason)
				}
			}
		}
	})

	t.Run("responses and background", func(t *testing.T) {
		for _, c := range []struct {
			name, in, reason string
		}{
			{"true", `{"model":"m","background":true}`, "background_not_supported"},
			{"true with spaces", `{"model":"m","background" :  true }`, "background_not_supported"},
			{"false", `{"model":"m","background":false}`, ""},
			{"null", `{"model":"m","background":null}`, ""},
			{"absent", `{"model":"m","input":"x"}`, ""},
			{"the last occurrence counts: true last", `{"model":"m","background":false,"background":true}`, "background_not_supported"},
			{"the last occurrence counts: false last", `{"model":"m","background":true,"background":false}`, ""},
			{"nested is not top level", `{"model":"m","metadata":{"background":true}}`, ""},
			{"the string true is not true", `{"model":"m","background":"true"}`, ""},
			{"the model rules come first", `{"background":true}`, "model_missing"},
			{"a duplicate model comes first", `{"model":"a","model":"b","background":true}`, "model_duplicate"},
		} {
			r, err := ParseOperation(OperationResponses, []byte(c.in))
			if c.reason != "" {
				if r != nil || !errors.Is(err, family.ErrInvalidRequest) || family.InvalidReason(err) != c.reason {
					t.Errorf("%s: %v %v, want reason %q", c.name, r, err, c.reason)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
		}
		for _, op := range []string{OperationChatCompletions, OperationEmbeddings} {
			if _, err := ParseOperation(op, []byte(`{"model":"m","background":true}`)); err != nil {
				t.Errorf("%s refuses background: %v", op, err)
			}
		}
	})

	t.Run("responses streams as chat does", func(t *testing.T) {
		for in, want := range map[string]bool{
			`{"model":"m","stream":true}`: true, `{"model":"m","stream":false}`: false, `{"model":"m"}`: false,
			`{"model":"m","stream":true,"stream":false}`: false, `{"model":"m","stream":"yes"}`: false,
		} {
			r, err := ParseOperation(OperationResponses, []byte(in))
			if err != nil || r.Stream() != want {
				t.Errorf("%s: stream=%v %v, want %v", in, r != nil && r.Stream(), err, want)
			}
		}
	})

	t.Run("embeddings never stream", func(t *testing.T) {
		r, err := ParseOperation(OperationEmbeddings, []byte(`{"model":"m","stream":true,"input":"x"}`))
		if err != nil || r.Stream() {
			t.Fatalf("stream=%v %v", r != nil && r.Stream(), err)
		}
		if out, strip := r.Prepare(family.Preparation{UpstreamModel: "m"}); strip || string(out) != `{"model":"m","stream":true,"input":"x"}` {
			t.Errorf("Prepare: %q %v", out, strip)
		}
	})

	t.Run("Prepare splices the usage option for chat only and replaces the model for all", func(t *testing.T) {
		const in = `{"model":"nick","stream":true,"input":"x"}`
		want := map[string]struct {
			out   string
			strip bool
		}{
			OperationChatCompletions: {`{"model":"up","stream":true,"input":"x","stream_options":{"include_usage":true}}`, true},
			OperationResponses:       {`{"model":"up","stream":true,"input":"x"}`, false},
			OperationEmbeddings:      {`{"model":"up","stream":true,"input":"x"}`, false},
		}
		for _, op := range all {
			body := []byte(in)
			r, err := ParseOperation(op, body)
			if err != nil {
				t.Fatal(err)
			}
			out, strip := r.Prepare(family.Preparation{UpstreamModel: "up"})
			if string(out) != want[op].out || strip != want[op].strip {
				t.Errorf("%s: %q %v, want %q %v", op, out, strip, want[op].out, want[op].strip)
			}
			if string(body) != in {
				t.Errorf("%s: Prepare modified the caller's bytes", op)
			}
			// The same name: no change at all for the operations that splice nothing.
			if op != OperationChatCompletions {
				if out, strip := r.Prepare(family.Preparation{UpstreamModel: "nick"}); strip || &out[0] != &body[0] {
					t.Errorf("%s: a request that needs no change is not the input slice", op)
				}
			}
		}
	})

	t.Run("Parse stays chat", func(t *testing.T) {
		r, err := Parse([]byte(`{"model":"m","background":true,"stream":true}`))
		if err != nil || !r.Stream() {
			t.Fatalf("%v %v", r, err)
		}
	})
}
