package openai

import (
	"bytes"
	"encoding/json"
	"testing"
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
