package gateway_test

import (
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeprovider"
)

// TestObservationCallTimes is T-912: with a provider that answers the headers at once, the first
// byte of the body 200 ms later and the end 400 ms later, Call is at least 400 ms and less than the
// whole request's Duration; FirstChunk is at least 200 ms for a stream and 0 for a plain answer; a
// request refused before step 9 has Forwarded false and zero times.
func TestObservationCallTimes(t *testing.T) {
	t.Parallel()
	t.Run("a plain answer", func(t *testing.T) {
		ob := &observer{}
		h := newHarness(t, observed(ob, nil))
		if c := h.sendAuthed(t, route, chat(fakeprovider.ModelTimed, false)); c.status != 200 {
			t.Fatalf("status %d", c.status)
		}
		o := ob.wait(t, 1)[0]
		if !o.Forwarded || o.Stream {
			t.Fatalf("forwarded %v stream %v", o.Forwarded, o.Stream)
		}
		if o.Call < fakeprovider.TimedEnd || o.Call >= o.Duration {
			t.Errorf("call %s, duration %s: want the call at least %s and under the duration", o.Call, o.Duration, fakeprovider.TimedEnd)
		}
		if o.FirstChunk != 0 {
			t.Errorf("a plain answer has a first chunk: %s", o.FirstChunk)
		}
	})
	t.Run("a stream", func(t *testing.T) {
		ob := &observer{}
		h := newHarness(t, observed(ob, nil))
		if c := h.sendAuthed(t, route, chat(fakeprovider.ModelTimed, true)); c.status != 200 {
			t.Fatalf("status %d", c.status)
		}
		o := ob.wait(t, 1)[0]
		if !o.Forwarded || !o.Stream {
			t.Fatalf("forwarded %v stream %v", o.Forwarded, o.Stream)
		}
		if o.Call < fakeprovider.TimedEnd || o.Call >= o.Duration {
			t.Errorf("call %s, duration %s", o.Call, o.Duration)
		}
		if o.FirstChunk < fakeprovider.TimedFirstByte || o.FirstChunk >= fakeprovider.TimedEnd {
			t.Errorf("first chunk %s, want from %s (the first body byte) and before %s", o.FirstChunk, fakeprovider.TimedFirstByte, fakeprovider.TimedEnd)
		}
	})
	t.Run("a request refused before step 9", func(t *testing.T) {
		ob := &observer{}
		h := newHarness(t, observed(ob, nil))
		h.sendAuthed(t, route, chat("nope", false))
		o := ob.wait(t, 1)[0]
		if o.Forwarded || o.Call != 0 || o.FirstChunk != 0 {
			t.Errorf("forwarded %v call %s first chunk %s", o.Forwarded, o.Call, o.FirstChunk)
		}
		if o.Duration <= 0 || o.Duration > time.Second {
			t.Errorf("duration %s", o.Duration)
		}
	})
}
