package openai

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/test/fixtures"
)

// run feeds data to a reader in chunks of the given size and returns everything it forwards.
func run(r *StreamReader, data []byte, chunk int) []byte {
	var out []byte
	for len(data) > 0 {
		n := min(chunk, len(data))
		out = append(out, r.Filter(data[:n])...)
		data = data[n:]
	}
	return append(out, r.Flush()...)
}

func events(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.Index(b, []byte("\n\n"))
		if i < 0 {
			out = append(out, b)
			break
		}
		out = append(out, b[:i+2])
		b = b[i+2:]
	}
	return out
}

// without returns data minus the event at index i.
func without(data []byte, i int) []byte {
	var out []byte
	for j, ev := range events(data) {
		if j != i {
			out = append(out, ev...)
		}
	}
	return out
}

var wantUsage = family.Usage{InputTokens: 9, OutputTokens: 12}

// core is the usage without the provider's own counters (Usage.Reported, added in sprint 3.2 and
// checked by its own tests): the 1.1 tests compare the normalized figures.
func core(u family.Usage) family.Usage {
	u.Reported = nil
	return u
}

func TestStreamReaderTee(t *testing.T) {
	data := fixtures.Read("chat_stream_usage.sse")
	for chunk := 1; chunk <= len(data); chunk++ {
		r := NewStreamReader(false)
		if got := run(r, data, chunk); !bytes.Equal(got, data) {
			t.Fatalf("chunk %d: output differs from input", chunk)
		}
		if u, ok := reported(r); !ok || !reflect.DeepEqual(core(u), wantUsage) {
			t.Fatalf("chunk %d: usage %+v %v", chunk, u, ok)
		}
		if r.Failed() {
			t.Fatalf("chunk %d: failed", chunk)
		}
	}
}

func TestStreamReaderTeeReturnsInputImmediately(t *testing.T) {
	r := NewStreamReader(false)
	p := []byte("data: {\"cho")
	if got := r.Filter(p); !bytes.Equal(got, p) {
		t.Fatalf("tee mode held bytes: %q", got)
	}
}

func TestStreamReaderStrip(t *testing.T) {
	data := fixtures.Read("chat_stream_usage.sse")
	want := without(data, 4)
	if len(want) >= len(data) || bytes.Contains(want, []byte(`"choices":[]`)) {
		t.Fatal("test setup: usage-only event not removed from expectation")
	}
	for chunk := 1; chunk <= len(data); chunk++ {
		r := NewStreamReader(true)
		if got := run(r, data, chunk); !bytes.Equal(got, want) {
			t.Fatalf("chunk %d: output differs", chunk)
		}
		if u, ok := reported(r); !ok || !reflect.DeepEqual(core(u), wantUsage) {
			t.Fatalf("chunk %d: usage %+v %v", chunk, u, ok)
		}
	}
}

func TestStreamReaderStripForwardsEventsAsTheyComplete(t *testing.T) {
	data := fixtures.Read("chat_stream_usage.sse")
	first := events(data)[0]
	r := NewStreamReader(true)
	got := append([]byte(nil), r.Filter(first[:len(first)-1])...)
	if len(got) != 0 {
		t.Fatalf("incomplete event forwarded: %q", got)
	}
	got = r.Filter(first[len(first)-1:])
	if !bytes.Equal(got, first) {
		t.Fatalf("complete event not forwarded: %q", got)
	}
}

func TestStreamReaderVariants(t *testing.T) {
	usageStream := fixtures.Read("chat_stream_usage.sse")
	check := func(t *testing.T, strip bool, in, want []byte, wantOK bool, wantU family.Usage) {
		t.Helper()
		for _, chunk := range []int{1, 7, len(in)} {
			r := NewStreamReader(strip)
			if got := run(r, in, chunk); !bytes.Equal(got, want) {
				t.Fatalf("chunk %d: output\n%q\nwant\n%q", chunk, got, want)
			}
			u, ok := reported(r)
			if ok != wantOK || (ok && !reflect.DeepEqual(core(u), wantU)) {
				t.Fatalf("chunk %d: usage %+v %v", chunk, u, ok)
			}
		}
	}

	t.Run("CRLF", func(t *testing.T) {
		in := bytes.ReplaceAll(usageStream, []byte("\n"), []byte("\r\n"))
		var want []byte
		for i, ev := range bytes.SplitAfter(in, []byte("\r\n\r\n")) {
			if i != 4 {
				want = append(want, ev...)
			}
		}
		check(t, true, in, want, true, wantUsage)
		check(t, false, in, in, true, wantUsage)
	})
	t.Run("comment events", func(t *testing.T) {
		in := append([]byte(": keep-alive\n\n"), usageStream...)
		want := append([]byte(": keep-alive\n\n"), without(usageStream, 4)...)
		check(t, true, in, want, true, wantUsage)
	})
	t.Run("multi-line data", func(t *testing.T) {
		ev := "data: {\"choices\":[],\ndata: \"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\n"
		in := []byte(string(events(usageStream)[0]) + ev + "data: [DONE]\n\n")
		want := []byte(string(events(usageStream)[0]) + "data: [DONE]\n\n")
		check(t, true, in, want, true, family.Usage{InputTokens: 1, OutputTokens: 2})
		check(t, false, in, in, true, family.Usage{InputTokens: 1, OutputTokens: 2})
	})
	t.Run("no usage", func(t *testing.T) {
		in := fixtures.Read("chat_stream.sse")
		check(t, true, in, in, false, family.Usage{})
		check(t, false, in, in, false, family.Usage{})
	})
	t.Run("usage on a chunk with choices is kept", func(t *testing.T) {
		in := fixtures.Read("chat_stream_usage_on_last_chunk.sse")
		check(t, true, in, in, true, wantUsage)
		check(t, false, in, in, true, wantUsage)
	})
	t.Run("truncated stream", func(t *testing.T) {
		in := usageStream[:len(usageStream)-7]
		head := usageStream[:len(usageStream)-len("data: [DONE]\n\n")]
		want := append(without(head, 4), []byte("data: [")...)
		check(t, true, in, want, true, wantUsage)
		check(t, false, in, in, true, wantUsage)
	})
	t.Run("non-JSON data and unknown fields", func(t *testing.T) {
		in := []byte("data: hello\n\nevent: ping\ndata: {\"unknown\":1}\n\ndata: [1,2]\n\ndata: {\"choices\":[],\"usage\":null}\n\n")
		check(t, true, in, in, false, family.Usage{})
	})
	t.Run("negative usage is ignored", func(t *testing.T) {
		in := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":-1,\"completion_tokens\":1}}\n\n")
		check(t, false, in, in, false, family.Usage{})
	})
	t.Run("last usage wins", func(t *testing.T) {
		in := []byte(`data: {"choices":[{}],"usage":{"prompt_tokens":1,"completion_tokens":1}}` + "\n\n" +
			`data: {"choices":[{}],"usage":{"prompt_tokens":5,"completion_tokens":6}}` + "\n\n")
		check(t, false, in, in, true, family.Usage{InputTokens: 5, OutputTokens: 6})
	})
}

func TestStreamReaderBounded(t *testing.T) {
	big := append([]byte("data: "), bytes.Repeat([]byte("a"), maxEventBytes+10)...)
	for _, strip := range []bool{false, true} {
		r := NewStreamReader(strip)
		if got := run(r, big, 4096); !bytes.Equal(got, big) {
			t.Fatalf("strip=%v: output differs from input (%d vs %d bytes)", strip, len(got), len(big))
		}
		if !r.Failed() {
			t.Errorf("strip=%v: Failed() is false", strip)
		}
		if _, ok := reported(r); ok {
			t.Errorf("strip=%v: usage reported", strip)
		}
	}
	t.Run("usage read before the overflow is kept", func(t *testing.T) {
		in := append(append([]byte(nil), fixtures.Read("chat_stream_usage_on_last_chunk.sse")...), big...)
		r := NewStreamReader(false)
		run(r, in, 1000)
		if u, ok := reported(r); !ok || !reflect.DeepEqual(core(u), wantUsage) || !r.Failed() {
			t.Fatalf("usage %+v %v failed=%v", u, ok, r.Failed())
		}
	})
	t.Run("nothing is held after a completed event", func(t *testing.T) {
		for _, strip := range []bool{false, true} {
			r := NewStreamReader(strip)
			for i, ev := range events(fixtures.Read("chat_stream_usage.sse")) {
				r.Filter(ev)
				if held := len(r.Take()); held != 0 {
					t.Fatalf("strip=%v event %d: %d bytes held", strip, i, held)
				}
			}
		}
	})
}

func BenchmarkStreamReader(b *testing.B) {
	data := fixtures.Read("chat_stream_usage.sse")
	n := len(events(data))
	for _, strip := range []bool{false, true} {
		name := "tee"
		if strip {
			name = "strip"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r := NewStreamReader(strip)
				for _, ev := range events(data) {
					r.Filter(ev)
				}
				r.Flush()
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/event")
		})
	}
}
