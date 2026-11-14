package sse_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family/sse"
)

// events returns the events Split reports for the chunks, the bytes it returned as overflow and
// the bytes still held (Take).
func run(max int, chunks ...[]byte) (evs []string, overflow, held []byte, f *sse.Framing) {
	f = &sse.Framing{Max: max}
	for _, c := range chunks {
		o := f.Split(c, func(ev []byte) { evs = append(evs, string(ev)) })
		overflow = append(overflow, o...)
	}
	return evs, overflow, f.Take(), f
}

// TestFraming is T-515: events come out whole whatever the chunking, with LF or CRLF, bounded.
func TestFraming(t *testing.T) {
	lf := "event: a\ndata: {\"n\":1}\n\ndata: two\n\n: comment\n\nevent: c\ndata: x\ndata: y\n\n"
	wantLF := []string{"event: a\ndata: {\"n\":1}\n\n", "data: two\n\n", ": comment\n\n", "event: c\ndata: x\ndata: y\n\n"}
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	wantCRLF := make([]string, len(wantLF))
	for i, w := range wantLF {
		wantCRLF[i] = strings.ReplaceAll(w, "\n", "\r\n")
	}

	for name, c := range map[string]struct {
		in   string
		want []string
	}{"LF": {lf, wantLF}, "CRLF": {crlf, wantCRLF}} {
		t.Run(name+" at every split offset", func(t *testing.T) {
			for cut := 0; cut <= len(c.in); cut++ {
				evs, over, held, f := run(1<<20, []byte(c.in[:cut]), []byte(c.in[cut:]))
				if strings.Join(evs, "|") != strings.Join(c.want, "|") || len(over) != 0 || len(held) != 0 || f.Failed() {
					t.Fatalf("cut at %d: events %q overflow %q held %q failed %v", cut, evs, over, held, f.Failed())
				}
			}
		})
		t.Run(name+" one byte at a time", func(t *testing.T) {
			chunks := make([][]byte, len(c.in))
			for i := range c.in {
				chunks[i] = []byte{c.in[i]}
			}
			evs, over, held, _ := run(1<<20, chunks...)
			if strings.Join(evs, "|") != strings.Join(c.want, "|") || len(over) != 0 || len(held) != 0 {
				t.Fatalf("events %q overflow %q held %q", evs, over, held)
			}
		})
	}

	t.Run("an incomplete event is held and Take returns it", func(t *testing.T) {
		evs, over, held, _ := run(1<<20, []byte("data: one\n\ndata: tw"))
		if len(evs) != 1 || len(over) != 0 || string(held) != "data: tw" {
			t.Fatalf("events %q overflow %q held %q", evs, over, held)
		}
	})
	t.Run("Take forgets what it returned", func(t *testing.T) {
		f := &sse.Framing{Max: 100}
		f.Split([]byte("data: x"), func([]byte) { t.Fatal("no event yet") })
		if got := f.Take(); string(got) != "data: x" {
			t.Fatalf("Take %q", got)
		}
		if got := f.Take(); len(got) != 0 {
			t.Fatalf("second Take %q", got)
		}
		var n int
		f.Split([]byte("data: y\n\n"), func([]byte) { n++ })
		if n != 1 {
			t.Fatalf("%d events after Take", n)
		}
	})
	t.Run("an event of exactly Max bytes passes, one more gives up", func(t *testing.T) {
		ev := "data: " + strings.Repeat("a", 20-len("data: ")-2) + "\n\n"
		if len(ev) != 20 {
			t.Fatalf("test event is %d bytes", len(ev))
		}
		evs, over, _, f := run(20, []byte(ev))
		if len(evs) != 1 || len(over) != 0 || f.Failed() {
			t.Fatalf("at the bound: %q %q %v", evs, over, f.Failed())
		}
		evs, over, held, f := run(20, []byte("x"+ev))
		if len(evs) != 0 || string(over) != "x"+ev || len(held) != 0 || !f.Failed() {
			t.Fatalf("over the bound: %q overflow %q held %q failed %v", evs, over, held, f.Failed())
		}
	})
	t.Run("the overflow is every byte held, so nothing is lost", func(t *testing.T) {
		in := "data: ok\n\n" + strings.Repeat("b", 50)
		var got []string
		f := &sse.Framing{Max: 40}
		over := f.Split([]byte(in), func(ev []byte) { got = append(got, string(ev)) })
		if len(got) != 1 || string(over) != strings.Repeat("b", 50) || !f.Failed() {
			t.Fatalf("events %q overflow %q failed %v", got, over, f.Failed())
		}
		if rest := f.Take(); len(rest) != 0 {
			t.Errorf("held %q after giving up", rest)
		}
	})
	t.Run("an incomplete event that grows over Max across chunks gives up", func(t *testing.T) {
		f := &sse.Framing{Max: 16}
		var over []byte
		for i := 0; i < 10 && !f.Failed(); i++ {
			over = append(over, f.Split([]byte("data: aaaa"), func([]byte) {})...)
		}
		if !f.Failed() || len(over) < 16 {
			t.Fatalf("failed %v overflow %d bytes", f.Failed(), len(over))
		}
	})
}

// TestData: the data lines of an event, joined with a newline.
func TestData(t *testing.T) {
	for _, c := range []struct{ name, ev, want string }{
		{"one line", "data: {\"a\":1}\n\n", `{"a":1}`},
		{"event line ignored", "event: x\ndata: v\n\n", "v"},
		{"two lines", "data: a\ndata: b\n\n", "a\nb"},
		{"no space after the colon", "data:v\n\n", "v"},
		{"only one leading space is removed", "data:  v\n\n", " v"},
		{"CRLF", "event: x\r\ndata: v\r\n\r\n", "v"},
		{"comment", ": ping\n\n", ""},
		{"empty data", "data:\n\n", ""},
	} {
		if got := sse.Data([]byte(c.ev)); string(got) != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// FuzzFraming: no panic, and the events, overflow and rest are the same whatever the chunking.
func FuzzFraming(f *testing.F) {
	f.Add([]byte("data: a\n\ndata: b\r\n\r\nevent: x\n"), uint8(3), uint16(64))
	f.Add([]byte(strings.Repeat("a", 200)), uint8(7), uint16(32))
	f.Add([]byte("\n\n\r\n\r\n: x\n\n"), uint8(1), uint16(8))
	f.Fuzz(func(t *testing.T, in []byte, chunk uint8, max uint16) {
		m := int(max%512) + 1
		whole, wo, wh, wf := run(m, in)
		step := int(chunk%16) + 1
		var chunks [][]byte
		for i := 0; i < len(in); i += step {
			chunks = append(chunks, in[i:min(i+step, len(in))])
		}
		parts, po, ph, pf := run(m, chunks...)
		// A failed framing forwards what it held when the bound was passed; where that happens
		// depends on the chunking, so only the forwarded total is compared then.
		if wf.Failed() || pf.Failed() {
			if wf.Failed() != pf.Failed() && len(in) > 0 {
				// The bound is on one event: both runs must give up on the same input.
				t.Fatalf("failed differs: whole %v chunked %v", wf.Failed(), pf.Failed())
			}
			return
		}
		if strings.Join(whole, "|") != strings.Join(parts, "|") || !bytes.Equal(wo, po) || !bytes.Equal(wh, ph) {
			t.Fatalf("chunking changed the output: %q vs %q", whole, parts)
		}
	})
}
