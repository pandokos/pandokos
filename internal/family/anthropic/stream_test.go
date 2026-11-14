package anthropic_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
	"github.com/pandokos/pandokos/test/fixtures"
)

// splitEvents is the test's own splitter: events end with a blank line, LF or CRLF.
func splitEvents(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		lf, crlf := bytes.Index(b, []byte("\n\n")), bytes.Index(b, []byte("\r\n\r\n"))
		end := -1
		switch {
		case lf >= 0 && (crlf < 0 || lf < crlf):
			end = lf + 2
		case crlf >= 0:
			end = crlf + 4
		}
		if end < 0 {
			out = append(out, b)
			break
		}
		out = append(out, b[:end])
		b = b[end:]
	}
	return out
}

type reading struct {
	usage  family.Usage
	parts  family.Parts
	deltas int64
	failed bool
}

func snapshot(r *anthropic.StreamReader) reading {
	u, p := r.Usage()
	return reading{usage: u, parts: p, deltas: r.Deltas(), failed: r.Failed()}
}

// feed gives the reader the chunks, checks that it forwards every byte unchanged and nothing is
// held at the end, and returns what it read.
func feed(t testing.TB, chunks ...[]byte) reading {
	t.Helper()
	r := anthropic.NewStreamReader()
	var in, out []byte
	for _, c := range chunks {
		in = append(in, c...)
		got := r.Filter(c)
		if !bytes.Equal(got, c) {
			t.Fatalf("Filter changed %q into %q", c, got)
		}
		if len(c) > 0 && &got[0] != &c[0] {
			t.Fatal("Filter did not return its input")
		}
		out = append(out, got...)
	}
	out = append(out, r.Flush()...)
	if !bytes.Equal(in, out) {
		t.Fatalf("the reader forwarded %d bytes of %d", len(out), len(in))
	}
	return snapshot(r)
}

func usageOf(in, out, cacheRead, cacheWrite int64, extra map[string]int64, reported map[string]int64) family.Usage {
	return family.Usage{InputTokens: in, OutputTokens: out, CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite, Extra: extra, Reported: reported}
}

// TestStreamReader is T-522: the parts and figures of each stream fixture.
func TestStreamReader(t *testing.T) {
	t.Run("message_stream.sse event by event", func(t *testing.T) {
		evs := splitEvents(fixtures.ReadAnthropic("message_stream.sse"))
		if len(evs) != 9 {
			t.Fatalf("%d events in the fixture", len(evs))
		}
		r := anthropic.NewStreamReader()
		if u, p := r.Usage(); p != 0 || !reflect.DeepEqual(u, family.Usage{}) || r.Deltas() != 0 || r.Failed() {
			t.Fatalf("a new reader reports %+v %v", u, p)
		}
		type step struct {
			parts  family.Parts
			deltas int64
		}
		want := []step{
			{family.PartInput, 0},                                               // message_start
			{family.PartInput, 0},                                               // content_block_start
			{family.PartInput, 0},                                               // ping
			{family.PartInput, 1}, {family.PartInput, 2}, {family.PartInput, 3}, // content_block_delta x3
			{family.PartInput, 3}, // content_block_stop
			{family.PartsAll, 3},  // message_delta
			{family.PartsAll, 3},  // message_stop
		}
		for i, ev := range evs {
			r.Filter(ev)
			u, p := r.Usage()
			if p != want[i].parts || r.Deltas() != want[i].deltas {
				t.Fatalf("after event %d: parts %v deltas %d, want %v %d", i, p, r.Deltas(), want[i].parts, want[i].deltas)
			}
			if u.InputTokens != 12 || u.CacheReadTokens != 0 || u.CacheWriteTokens != 0 {
				t.Errorf("after event %d: input side %+v", i, u)
			}
			if p&family.PartOutput != 0 && u.OutputTokens != 7 {
				t.Errorf("after event %d: output %d, want 7", i, u.OutputTokens)
			}
		}
		u, p := r.Usage()
		wantU := usageOf(12, 7, 0, 0, nil, map[string]int64{"input_tokens": 12, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 7})
		if p != family.PartsAll || !reflect.DeepEqual(u, wantU) {
			t.Errorf("final %+v %v\nwant %+v", u, p, wantU)
		}
	})

	t.Run("the cumulative message_delta replaces what it repeats and keeps the rest", func(t *testing.T) {
		evs := splitEvents(fixtures.ReadAnthropic("message_stream_cumulative.sse"))
		r := anthropic.NewStreamReader()
		r.Filter(evs[0])
		u, p := r.Usage()
		extra := map[string]int64{"cache_write_1h_tokens": 200}
		if p != family.PartInput || u.InputTokens != 2310 || u.CacheReadTokens != 2000 || u.CacheWriteTokens != 300 || !reflect.DeepEqual(u.Extra, extra) {
			t.Errorf("after message_start: %+v %v", u, p)
		}
		for _, ev := range evs[1:] {
			r.Filter(ev)
		}
		u, p = r.Usage()
		want := usageOf(2310, 50, 2000, 300, extra, map[string]int64{
			"input_tokens": 10, "cache_creation_input_tokens": 300, "cache_read_input_tokens": 2000,
			"cache_creation.ephemeral_5m_input_tokens": 100, "cache_creation.ephemeral_1h_input_tokens": 200, "output_tokens": 50,
		})
		if p != family.PartsAll || !reflect.DeepEqual(u, want) {
			t.Errorf("final %+v %v\nwant %+v", u, p, want)
		}
		if r.Deltas() != 3 {
			t.Errorf("deltas %d", r.Deltas())
		}
	})

	t.Run("a nested object in message_delta replaces the nested object whole", func(t *testing.T) {
		in := sseEvent("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200},"output_tokens":1}}}`) +
			sseEvent("message_delta", `{"type":"message_delta","delta":{},"usage":{"cache_creation":{"ephemeral_5m_input_tokens":300},"output_tokens":9}}`)
		got := feed(t, []byte(in))
		if got.parts != family.PartsAll || got.usage.OutputTokens != 9 || got.usage.CacheWriteTokens != 300 || len(got.usage.Extra) != 0 {
			t.Errorf("%+v", got)
		}
		if _, ok := got.usage.Reported["cache_creation.ephemeral_1h_input_tokens"]; ok {
			t.Errorf("the replaced nested member survived: %v", got.usage.Reported)
		}
	})

	t.Run("a message_delta without output_tokens does not report the output part", func(t *testing.T) {
		in := sseEvent("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}`) +
			sseEvent("message_delta", `{"type":"message_delta","delta":{},"usage":{"input_tokens":15}}`)
		got := feed(t, []byte(in))
		if got.parts != family.PartInput || got.usage.InputTokens != 15 {
			t.Errorf("%+v", got)
		}
	})

	t.Run("a stream cut after message_start reports the input part", func(t *testing.T) {
		evs := splitEvents(fixtures.ReadAnthropic("message_stream.sse"))
		got := feed(t, evs[0])
		if got.parts != family.PartInput || got.usage.InputTokens != 12 || got.deltas != 0 || got.failed {
			t.Errorf("%+v", got)
		}
		cache := splitEvents(fixtures.ReadAnthropic("message_stream_cumulative.sse"))
		got = feed(t, cache[0], cache[1], cache[2], cache[3])
		if got.parts != family.PartInput || got.usage.InputTokens != 2310 || got.usage.CacheReadTokens != 2000 || got.deltas != 1 {
			t.Errorf("%+v", got)
		}
	})

	t.Run("a message_start cut in the middle reports nothing", func(t *testing.T) {
		first := splitEvents(fixtures.ReadAnthropic("message_stream.sse"))[0]
		got := feed(t, first[:len(first)-2]) // without the blank line
		if got.parts != 0 || !reflect.DeepEqual(got.usage, family.Usage{}) {
			t.Errorf("%+v", got)
		}
	})

	t.Run("the tool-use stream reports its usage and counts its deltas", func(t *testing.T) {
		got := feed(t, fixtures.ReadAnthropic("message_stream_tools.sse"))
		if got.parts != family.PartsAll || got.usage.InputTokens != 12 || got.usage.OutputTokens != 20 || got.deltas != 2 {
			t.Errorf("%+v", got)
		}
	})

	t.Run("an error event in a 200 stream changes nothing", func(t *testing.T) {
		got := feed(t, fixtures.ReadAnthropic("message_stream_error.sse"))
		if got.parts != family.PartInput || got.usage.InputTokens != 12 || got.deltas != 2 || got.failed {
			t.Errorf("%+v", got)
		}
		// An error event after a complete stream leaves everything as it was.
		full := fixtures.ReadAnthropic("message_stream.sse")
		plain := feed(t, full)
		withErr := feed(t, append(append([]byte(nil), full...), sseEvent("error", `{"type":"error","error":{"type":"api_error","message":"synthetic"}}`)...))
		if !reflect.DeepEqual(plain, withErr) {
			t.Errorf("%+v vs %+v", plain, withErr)
		}
	})

	t.Run("pings and CRLF", func(t *testing.T) {
		lf := feed(t, fixtures.ReadAnthropic("message_stream.sse"))
		crlf := feed(t, fixtures.ReadAnthropic("message_stream_ping_crlf.sse"))
		if !reflect.DeepEqual(lf, crlf) {
			t.Errorf("LF %+v\nCRLF %+v", lf, crlf)
		}
		if crlf.parts != family.PartsAll || crlf.deltas != 3 {
			t.Errorf("%+v", crlf)
		}
	})

	t.Run("the type of the data decides, not the event line", func(t *testing.T) {
		in := sseEvent("ping", `{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`) +
			sseEvent("message_start", `{"type":"ping"}`) +
			sseEvent("content_block_stop", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`) +
			sseEvent("content_block_delta", `{"type":"content_block_stop","index":0}`) +
			sseEvent("message_stop", `{"type":"message_delta","delta":{},"usage":{"output_tokens":4}}`)
		got := feed(t, []byte(in))
		if got.parts != family.PartsAll || got.usage.InputTokens != 5 || got.usage.OutputTokens != 4 || got.deltas != 1 {
			t.Errorf("%+v", got)
		}
	})

	t.Run("data without an event line, comments, unknown types and data that is not JSON", func(t *testing.T) {
		in := ": keep-alive\n\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n" +
			"event: mystery\ndata: {\"type\":\"something_new\",\"usage\":{\"input_tokens\":999}}\n\n" +
			"data: not json\n\n" +
			"data: [DONE]\n\n" +
			"data: {\"no\":\"type\"}\n\n" +
			"data: 7\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n"
		got := feed(t, []byte(in))
		if got.parts != family.PartsAll || got.usage.InputTokens != 5 || got.usage.OutputTokens != 4 || got.deltas != 0 {
			t.Errorf("%+v", got)
		}
	})

	t.Run("data over two lines is one JSON document", func(t *testing.T) {
		in := "event: message_start\ndata: {\"type\":\"message_start\",\ndata: \"message\":{\"usage\":{\"input_tokens\":5}}}\n\n"
		got := feed(t, []byte(in))
		if got.parts != family.PartInput || got.usage.InputTokens != 5 {
			t.Errorf("%+v", got)
		}
	})

	t.Run("no usage anywhere reports nothing", func(t *testing.T) {
		in := sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_synthetic-1"}}`) +
			sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`) +
			sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
		got := feed(t, []byte(in))
		if got.parts != 0 || got.deltas != 1 {
			t.Errorf("%+v", got)
		}
	})

	t.Run("a second message_start starts over", func(t *testing.T) {
		in := sseEvent("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`) +
			sseEvent("message_delta", `{"type":"message_delta","delta":{},"usage":{"output_tokens":4}}`) +
			sseEvent("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":1}}}`)
		got := feed(t, []byte(in))
		if got.usage.InputTokens != 9 || got.parts != family.PartInput {
			t.Errorf("%+v", got)
		}
	})

	t.Run("every stream fixture at every split offset gives the same usage", func(t *testing.T) {
		for _, name := range []string{
			"message_stream.sse", "message_stream_cumulative.sse", "message_stream_tools.sse", "message_stream_error.sse", "message_stream_ping_crlf.sse",
		} {
			data := fixtures.ReadAnthropic(name)
			whole := feed(t, data)
			for cut := 0; cut <= len(data); cut++ {
				got := feed(t, data[:cut], data[cut:])
				if !reflect.DeepEqual(got, whole) {
					t.Fatalf("%s cut at %d:\n%+v\nvs whole\n%+v", name, cut, got, whole)
				}
			}
			one := make([][]byte, len(data))
			for i := range data {
				one[i] = data[i : i+1]
			}
			if got := feed(t, one...); !reflect.DeepEqual(got, whole) {
				t.Fatalf("%s byte by byte:\n%+v\nvs whole\n%+v", name, got, whole)
			}
		}
	})

	t.Run("Flush holds nothing and Filter forwards a partial event at once", func(t *testing.T) {
		r := anthropic.NewStreamReader()
		part := []byte("event: message_start\ndata: {\"type\":\"mess")
		if got := r.Filter(part); !bytes.Equal(got, part) {
			t.Errorf("Filter %q", got)
		}
		if got := r.Flush(); len(got) != 0 {
			t.Errorf("Flush %q", got)
		}
		if got := r.Filter(nil); len(got) != 0 {
			t.Errorf("Filter(nil) %q", got)
		}
	})

	t.Run("the reader implements family.StreamReader", func(*testing.T) {
		var _ family.StreamReader = anthropic.NewStreamReader()
	})
}

func sseEvent(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

// TestStreamReaderBounds is T-523: an event over 1 MiB makes the reader fail, every byte is
// still forwarded and nothing is held.
func TestStreamReaderBounds(t *testing.T) {
	const max = 1 << 20
	pad := func(n int) string { return strings.Repeat("a", n) }
	delta := func(size int) []byte { // one content_block_delta event of exactly size bytes
		head := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\""
		tail := "\"}}\n\n"
		return []byte(head + pad(size-len(head)-len(tail)) + tail)
	}
	start := []byte(sseEvent("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}`))

	t.Run("an event of exactly 1 MiB is read", func(t *testing.T) {
		got := feed(t, start, delta(max), []byte(sseEvent("message_delta", `{"type":"message_delta","delta":{},"usage":{"output_tokens":7}}`)))
		if got.failed || got.parts != family.PartsAll || got.deltas != 1 {
			t.Errorf("%+v", got)
		}
	})
	t.Run("an event one byte over makes the reader fail", func(t *testing.T) {
		got := feed(t, start, delta(max+1))
		if !got.failed {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a complete event far over the bound, in one chunk", func(t *testing.T) {
		got := feed(t, start, delta(max+4096), []byte(sseEvent("message_delta", `{"type":"message_delta","delta":{},"usage":{"output_tokens":7}}`)))
		if !got.failed {
			t.Errorf("%+v", got)
		}
	})
	t.Run("an incomplete event that grows over the bound across chunks", func(t *testing.T) {
		var chunks [][]byte
		chunks = append(chunks, start, []byte("event: content_block_delta\ndata: {\"text\":\""))
		for i := 0; i < 9; i++ {
			chunks = append(chunks, []byte(pad(1<<17)))
		}
		got := feed(t, chunks...)
		if !got.failed {
			t.Errorf("%+v", got)
		}
	})
	t.Run("after failing every later byte is still forwarded", func(t *testing.T) {
		r := anthropic.NewStreamReader()
		var chunks [][]byte
		chunks = append(chunks, delta(max+10), []byte(sseEvent("ping", `{"type":"ping"}`)), []byte(pad(100)))
		for _, c := range chunks {
			if got := r.Filter(c); !bytes.Equal(got, c) {
				t.Fatalf("Filter changed %d bytes", len(c))
			}
		}
		if !r.Failed() {
			t.Error("not failed")
		}
		if got := r.Flush(); len(got) != 0 {
			t.Errorf("Flush returned %d bytes", len(got))
		}
	})
	t.Run("a stream of many small events is never over the bound", func(t *testing.T) {
		r := anthropic.NewStreamReader()
		for i := 0; i < 5000; i++ {
			r.Filter(delta(300))
		}
		if r.Failed() || r.Deltas() != 5000 {
			t.Errorf("failed %v deltas %d", r.Failed(), r.Deltas())
		}
	})
}

// FuzzAnthropicStreamReader is T-524: no panic; every byte is forwarded; the usage read does not
// depend on how the stream was cut; Reported stays within bounds.
func FuzzAnthropicStreamReader(f *testing.F) {
	for _, name := range []string{"message_stream.sse", "message_stream_cumulative.sse", "message_stream_tools.sse", "message_stream_error.sse", "message_stream_ping_crlf.sse"} {
		f.Add(fixtures.ReadAnthropic(name), uint8(7))
	}
	f.Add([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":-1}}}\n\n"), uint8(3))
	f.Add([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1.5}}\n\n"), uint8(1))
	f.Fuzz(func(t *testing.T, in []byte, step uint8) {
		whole := feed(t, in)
		n := int(step%32) + 1
		var chunks [][]byte
		for i := 0; i < len(in); i += n {
			chunks = append(chunks, in[i:min(i+n, len(in))])
		}
		parts := feed(t, chunks...)
		if !reflect.DeepEqual(whole, parts) {
			t.Fatalf("the usage depends on the chunking:\n%+v\n%+v", whole, parts)
		}
		u := whole.usage
		if len(u.Reported) > 32 {
			t.Fatalf("%d reported entries", len(u.Reported))
		}
		for k, v := range u.Reported {
			if v < 0 || len(k) == 0 || len(k) > 64 {
				t.Fatalf("reported %q=%d", k, v)
			}
		}
		if whole.parts&^family.PartsAll != 0 {
			t.Fatalf("unknown parts %v", whole.parts)
		}
		if whole.deltas < 0 {
			t.Fatal("negative deltas")
		}
	})
}
