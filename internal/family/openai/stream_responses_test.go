package openai

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/test/fixtures"
)

// The tests of this file are the tests of both stream readers, through the operation-keyed
// constructor: T-459 to T-462. The splitting into events is the test's own, not the product's.

// splitEv splits a stream into events: each ends after the first empty line (LF or CRLF); a last
// event may be incomplete.
func splitEv(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); {
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			break
		}
		line := b[i : i+j]
		i += j + 1
		if len(line) == 0 || string(line) == "\r" {
			out = append(out, b[start:i])
			start = i
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// usageOnly reports whether an event is the usage-only chunk of a chat stream: a chunk with an
// empty choices array and a usage object. It is the test's own criterion, valid for the fixtures.
func usageOnly(ev []byte) bool {
	return bytes.Contains(ev, []byte(`"choices":[]`)) && bytes.Contains(ev, []byte(`"usage":{`))
}

// feed gives data to a reader in pieces (sizes, then the rest at once) and returns what it forwards.
func feed(r family.StreamReader, data []byte, sizes []int) []byte {
	var out []byte
	for _, n := range sizes {
		n = min(n, len(data))
		out = append(out, r.Filter(data[:n])...)
		data = data[n:]
	}
	if len(data) > 0 {
		out = append(out, r.Filter(data)...)
	}
	return append(out, r.Flush()...)
}

// splitAt returns the sizes that cut data at one offset.
func splitAt(k int) []int { return []int{k} }

type readerKind struct {
	name  string
	op    string
	strip bool
}

var readerKinds = []readerKind{
	{"chat observe", OperationChatCompletions, false},
	{"chat strip", OperationChatCompletions, true},
	{"responses", OperationResponses, false},
	{"responses strip flag", OperationResponses, true},
}

func (k readerKind) stripsUsageOnly() bool { return k.op == OperationChatCompletions && k.strip }

// TestResponsesStreamReader is T-459.
func TestResponsesStreamReader(t *testing.T) {
	newReader := func() family.StreamReader { return NewStreamReaderFor(OperationResponses, false) }
	t.Run("usage from response.completed", func(t *testing.T) {
		data := fixtures.Read("responses_stream.sse")
		r := newReader()
		if out := feed(r, data, nil); !bytes.Equal(out, data) {
			t.Fatal("the reader changed the stream")
		}
		want := family.Usage{InputTokens: 20, OutputTokens: 15, CacheReadTokens: 5, ReasoningTokens: 4,
			Reported: map[string]int64{"input_tokens": 20, "input_tokens_details.cached_tokens": 5,
				"output_tokens": 15, "output_tokens_details.reasoning_tokens": 4, "total_tokens": 35}}
		if u, ok := r.Usage(); !ok || !reflect.DeepEqual(u, want) {
			t.Errorf("usage %+v %v, want %+v", u, ok, want)
		}
		if r.Deltas() != 2 || r.Failed() {
			t.Errorf("deltas %d failed %v", r.Deltas(), r.Failed())
		}
	})
	t.Run("usage from response.incomplete", func(t *testing.T) {
		r := newReader()
		feed(r, fixtures.Read("responses_stream_incomplete.sse"), nil)
		u, ok := r.Usage()
		if !ok || u.InputTokens != 20 || u.OutputTokens != 6 || u.Reported["total_tokens"] != 26 {
			t.Errorf("%+v %v", u, ok)
		}
		if r.Deltas() != 1 {
			t.Errorf("deltas %d", r.Deltas())
		}
	})
	t.Run("response.failed with null usage gives none", func(t *testing.T) {
		data := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n" +
			"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"usage\":null,\"error\":{\"code\":\"synthetic\"}}}\n\n")
		r := newReader()
		feed(r, data, nil)
		if u, ok := r.Usage(); ok {
			t.Errorf("usage %+v", u)
		}
		if r.Deltas() != 1 {
			t.Errorf("deltas %d", r.Deltas())
		}
	})
	t.Run("response.failed with usage", func(t *testing.T) {
		data := []byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n")
		r := newReader()
		feed(r, data, nil)
		if u, ok := r.Usage(); !ok || u.InputTokens != 2 || u.OutputTokens != 0 {
			t.Errorf("%+v %v", u, ok)
		}
	})
	t.Run("usage on an event that is not terminal is ignored", func(t *testing.T) {
		data := []byte("data: {\"type\":\"response.created\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}}\n\n")
		r := newReader()
		feed(r, data, nil)
		if u, ok := r.Usage(); ok {
			t.Errorf("usage %+v from response.created", u)
		}
	})
	t.Run("deltas count events whose type ends in .delta", func(t *testing.T) {
		var sb strings.Builder
		for _, typ := range []string{"response.output_text.delta", "response.function_call_arguments.delta", "response.reasoning_summary_text.delta",
			"response.output_text.done", "response.output_item.added", "response.delta.extra", "delta"} {
			fmt.Fprintf(&sb, "event: %s\ndata: {\"type\":%q}\n\n", typ, typ)
		}
		r := newReader()
		feed(r, []byte(sb.String()), nil)
		if r.Deltas() != 3 {
			t.Errorf("deltas %d, want 3", r.Deltas())
		}
	})
	t.Run("Filter returns its input and Flush nothing", func(t *testing.T) {
		for _, strip := range []bool{false, true} {
			r := NewStreamReaderFor(OperationResponses, strip)
			p := []byte("event: response.comp")
			if got := r.Filter(p); !bytes.Equal(got, p) {
				t.Errorf("strip=%v: Filter held bytes: %q", strip, got)
			}
			if got := r.Flush(); len(got) != 0 {
				t.Errorf("strip=%v: Flush returned %q", strip, got)
			}
		}
	})
	t.Run("a usage-only looking event is never removed", func(t *testing.T) {
		data := []byte("data: {\"type\":\"x\",\"choices\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n")
		r := NewStreamReaderFor(OperationResponses, true)
		if out := feed(r, data, nil); !bytes.Equal(out, data) {
			t.Errorf("%q", out)
		}
	})
}

// edgeCase is one stream and what every reader must make of it.
type edgeCase struct {
	name string
	op   string
	data []byte
	// usage is what the reader must report: nil for none.
	usage *family.Usage
	// deltas is the count of content events.
	deltas int64
}

func ev(lines ...string) string { return strings.Join(lines, "\n") + "\n\n" }

func edgeCases() []edgeCase {
	chatU := &family.Usage{InputTokens: 9, OutputTokens: 12,
		Reported: map[string]int64{"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21}}
	toolsU := &family.Usage{InputTokens: 30, OutputTokens: 12,
		Reported: map[string]int64{"prompt_tokens": 30, "completion_tokens": 12, "total_tokens": 42}}
	respU := &family.Usage{InputTokens: 20, OutputTokens: 15, CacheReadTokens: 5, ReasoningTokens: 4,
		Reported: map[string]int64{"input_tokens": 20, "input_tokens_details.cached_tokens": 5,
			"output_tokens": 15, "output_tokens_details.reasoning_tokens": 4, "total_tokens": 35}}
	chat, resp := OperationChatCompletions, OperationResponses
	chatUsage := fixtures.Read("chat_stream_usage.sse")
	respStream := fixtures.Read("responses_stream.sse")
	utf8Chunk := `data: {"choices":[{"index":0,"delta":{"content":"héllo é ✓ 日本 😀"},"finish_reason":null}],"usage":null}` + "\n\n"
	usageEvent := `data: {"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}` + "\n\n"
	respUsageEvent := `event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":5},"output_tokens":15,"output_tokens_details":{"reasoning_tokens":4},"total_tokens":35}}}` + "\n\n"
	respDelta := ev("event: response.output_text.delta", `data: {"type":"response.output_text.delta","delta":"é ✓ 日本 😀"}`)
	crlf := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n")) }

	return []edgeCase{
		{"chat usage stream", chat, chatUsage, chatU, 4},
		{"chat keep-alive comments, id and event lines, CRLF", chat, fixtures.Read("chat_stream_keepalive_crlf.sse"), chatU, 3},
		{"chat tool-call deltas", chat, fixtures.Read("chat_stream_tools.sse"), toolsU, 4},
		{"chat error event in the middle, no [DONE]", chat, fixtures.Read("chat_stream_error.sse"), nil, 2},
		{"chat CRLF everywhere", chat, crlf(chatUsage), chatU, 4},
		{"chat comment events only between events", chat, []byte(": ping\n\n" + string(chatUsage) + ": ping\n\n"), chatU, 4},
		{"chat multi-line data", chat, []byte(utf8Chunk + `data: {"choices":[],` + "\n" + `data: "usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}` + "\n\ndata: [DONE]\n\n"), chatU, 1},
		{"chat UTF-8 content", chat, []byte(utf8Chunk + usageEvent + "data: [DONE]\n\n"), chatU, 1},
		{"chat no final blank line", chat, bytes.TrimSuffix(chatUsage, []byte("\n")), chatU, 4},
		{"chat only [DONE]", chat, []byte("data: [DONE]\n\n"), nil, 0},
		{"chat without usage", chat, fixtures.Read("chat_stream.sse"), nil, 4},
		{"chat empty", chat, nil, nil, 0},

		{"responses stream", resp, respStream, respU, 2},
		{"responses CRLF", resp, crlf(respStream), respU, 2},
		{"responses comments", resp, []byte(": ping\n\n" + strings.ReplaceAll(string(respStream), "\n\n", "\n\n: ping\n\n")), respU, 2},
		{"responses error event in the middle", resp, []byte(respDelta + ev("event: error", `data: {"type":"error","code":"synthetic","message":"synthetic upstream failure"}`)), nil, 1},
		{"responses tool-call deltas", resp, []byte(strings.ReplaceAll(string(respStream), "response.output_text.delta", "response.function_call_arguments.delta")), respU, 2},
		{"responses multi-line data", resp, []byte(respDelta + "event: response.completed\n" + `data: {"type":"response.completed",` + "\n" + `data: "response":{"usage":{"input_tokens":20,"output_tokens":15}}}` + "\n\n"),
			&family.Usage{InputTokens: 20, OutputTokens: 15, Reported: map[string]int64{"input_tokens": 20, "output_tokens": 15}}, 1},
		{"responses UTF-8 content", resp, []byte(respDelta + respUsageEvent), respU, 1},
		{"responses no final blank line", resp, bytes.TrimSuffix(respStream, []byte("\n")), respU, 2},
		{"responses without usage", resp, []byte(respDelta), nil, 1},
		{"responses empty", resp, nil, nil, 0},
	}
}

// TestStreamEdgeCases is T-460.
func TestStreamEdgeCases(t *testing.T) {
	for _, k := range readerKinds {
		for _, c := range edgeCases() {
			if c.op != k.op {
				continue
			}
			t.Run(k.name+"/"+c.name, func(t *testing.T) {
				want := c.data
				if k.stripsUsageOnly() {
					want = nil
					for _, e := range splitEv(c.data) {
						if !usageOnly(e) {
							want = append(want, e...)
						}
					}
				}
				check := func(label string, sizes []int) {
					t.Helper()
					r := NewStreamReaderFor(k.op, k.strip)
					out := feed(r, c.data, sizes)
					if !bytes.Equal(out, want) {
						t.Fatalf("%s: the output differs (%d bytes, want %d)", label, len(out), len(want))
					}
					u, ok := r.Usage()
					switch {
					case c.usage == nil && ok:
						t.Fatalf("%s: usage %+v from a stream without any", label, u)
					case c.usage != nil && (!ok || !reflect.DeepEqual(u, *c.usage)):
						t.Fatalf("%s: usage %+v %v, want %+v", label, u, ok, *c.usage)
					}
					if r.Deltas() != c.deltas {
						t.Fatalf("%s: %d deltas, want %d", label, r.Deltas(), c.deltas)
					}
					if r.Failed() {
						t.Fatalf("%s: the reader gave up", label)
					}
				}
				check("whole", nil)
				ones := make([]int, len(c.data))
				for i := range ones {
					ones[i] = 1
				}
				check("one byte at a time", ones)
				// Every split of the byte stream in two, including inside a UTF-8 sequence and
				// inside a CRLF.
				for k := 0; k <= len(c.data); k++ {
					check(fmt.Sprintf("split at %d", k), splitAt(k))
				}
			})
		}
	}

	t.Run("in strip mode only the usage-only event is missing", func(t *testing.T) {
		data := fixtures.Read("chat_stream_usage.sse")
		r := NewStreamReaderFor(OperationChatCompletions, true)
		out := feed(r, data, nil)
		in, got := splitEv(data), splitEv(out)
		if len(got) != len(in)-1 {
			t.Fatalf("%d events, want %d", len(got), len(in)-1)
		}
		for i, j := 0, 0; i < len(in); i++ {
			if usageOnly(in[i]) {
				continue
			}
			if !bytes.Equal(in[i], got[j]) {
				t.Errorf("event %d changed", i)
			}
			j++
		}
	})
}

// TestStreamReaderBounds is T-461: an event over the bound makes the reader give up; every byte
// is still forwarded in order, in both modes, and the reader holds nothing afterwards.
func TestStreamReaderBounds(t *testing.T) {
	for _, k := range readerKinds {
		bound := 1 << 20
		if k.op == OperationResponses {
			bound = 4 << 20
		}
		t.Run(k.name, func(t *testing.T) {
			big := append([]byte("data: "), bytes.Repeat([]byte("a"), bound+10)...)
			r := NewStreamReaderFor(k.op, k.strip)
			var out []byte
			for rest := big; len(rest) > 0; {
				n := min(4096, len(rest))
				out = append(out, r.Filter(rest[:n])...)
				rest = rest[n:]
			}
			out = append(out, r.Flush()...)
			if !bytes.Equal(out, big) {
				t.Fatalf("output of %d bytes, input of %d", len(out), len(big))
			}
			if !r.Failed() {
				t.Error("Failed() is false")
			}
			if _, ok := r.Usage(); ok {
				t.Error("usage reported")
			}
			if got := r.Flush(); len(got) != 0 {
				t.Errorf("the reader still held %d bytes", len(got))
			}
			p := []byte("data: [DONE]\n\n")
			if got := r.Filter(p); !bytes.Equal(got, p) {
				t.Errorf("after giving up Filter returned %q", got)
			}
		})
	}
	t.Run("an event under the bound is read", func(t *testing.T) {
		for _, op := range []string{OperationChatCompletions, OperationResponses} {
			pad := strings.Repeat("a", 512<<10)
			data := []byte(`data: {"type":"response.output_text.delta","delta":"` + pad + `"}` + "\n\n")
			r := NewStreamReaderFor(op, false)
			feed(r, data, []int{4096})
			if r.Failed() {
				t.Errorf("%s: gave up on an event of %d bytes", op, len(data))
			}
		}
	})
	t.Run("usage read before the overflow is kept", func(t *testing.T) {
		in := append(append([]byte(nil), fixtures.Read("chat_stream_usage_on_last_chunk.sse")...), "data: "...)
		in = append(in, bytes.Repeat([]byte("a"), 1<<20+10)...)
		r := NewStreamReaderFor(OperationChatCompletions, false)
		feed(r, in, []int{1000})
		if u, ok := r.Usage(); !ok || u.InputTokens != 9 || !r.Failed() {
			t.Fatalf("usage %+v %v failed=%v", u, ok, r.Failed())
		}
	})
}

// cutSizes turns the bytes of a fuzz input into chunk sizes.
func cutSizes(cuts []byte) []int {
	sizes := make([]int, 0, len(cuts))
	for _, c := range cuts {
		sizes = append(sizes, int(c)%97)
	}
	return sizes
}

// fuzzReader holds the properties of T-462 for one reader.
func fuzzReader(t *testing.T, op string, data, cuts []byte, strip bool) {
	t.Helper()
	bound := 1 << 20
	if op == OperationResponses {
		bound = 4 << 20
	}
	if len(data) >= bound {
		t.Skip()
	}
	a := NewStreamReaderFor(op, strip)
	outA := feed(a, data, cutSizes(cuts))
	b := NewStreamReaderFor(op, strip)
	outB := feed(b, data, nil)

	if !bytes.Equal(outA, outB) {
		t.Fatal("the output depends on the chunking")
	}
	ua, oka := a.Usage()
	ub, okb := b.Usage()
	if oka != okb || !reflect.DeepEqual(ua, ub) {
		t.Fatal("the usage depends on the chunking")
	}
	if a.Deltas() != b.Deltas() || a.Failed() != b.Failed() {
		t.Fatal("deltas or failure depend on the chunking")
	}

	if !strip || op != OperationChatCompletions {
		if !bytes.Equal(outA, data) {
			t.Fatal("a reader that does not strip changed the stream")
		}
		return
	}
	// Strip mode: the output is the input with zero or more complete events removed.
	in, out := splitEv(data), splitEv(outA)
	j := 0
	for _, e := range in {
		if j < len(out) && bytes.Equal(e, out[j]) {
			j++
			continue
		}
		if !bytes.Contains(e, []byte("usage")) {
			t.Fatalf("an event without usage was removed: %q", e)
		}
		if !bytes.HasSuffix(e, []byte("\n\n")) && !bytes.HasSuffix(e, []byte("\r\n\r\n")) && !bytes.HasSuffix(e, []byte("\n\r\n")) {
			t.Fatalf("an incomplete event was removed: %q", e)
		}
	}
	if j != len(out) {
		t.Fatalf("the output is not a subsequence of the input events")
	}
	if !bytes.Contains(data, []byte("usage")) && !bytes.Equal(outA, data) {
		t.Fatal("the stream changed though it has no usage")
	}
}

func seedStreams(f *testing.F, names ...string) {
	for _, n := range names {
		f.Add(fixtures.Read(n), []byte{3, 1, 40, 7}, true)
		f.Add(fixtures.Read(n), []byte{1}, false)
	}
	f.Add([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1}}\r\n\r\n"), []byte{2}, true)
	f.Add([]byte(": ping\r\n\r\ndata: x\r\n\r\n"), []byte{0, 5}, true)
}

// FuzzStreamReader is T-462 for the chat reader.
func FuzzStreamReader(f *testing.F) {
	seedStreams(f, "chat_stream_usage.sse", "chat_stream.sse", "chat_stream_usage_on_last_chunk.sse",
		"chat_stream_tools.sse", "chat_stream_error.sse", "chat_stream_keepalive_crlf.sse")
	f.Fuzz(func(t *testing.T, data, cuts []byte, strip bool) {
		fuzzReader(t, OperationChatCompletions, data, cuts, strip)
	})
}

// FuzzResponsesStreamReader is T-462 for the responses reader.
func FuzzResponsesStreamReader(f *testing.F) {
	seedStreams(f, "responses_stream.sse", "responses_stream_incomplete.sse")
	f.Fuzz(func(t *testing.T, data, cuts []byte, strip bool) {
		fuzzReader(t, OperationResponses, data, cuts, strip)
	})
}

func BenchmarkResponsesStreamReader(b *testing.B) {
	data := fixtures.Read("responses_stream.sse")
	evs := splitEv(data)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := NewStreamReaderFor(OperationResponses, false)
		for _, e := range evs {
			r.Filter(e)
		}
		r.Flush()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(evs)), "ns/event")
}
