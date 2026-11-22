package telemetry_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

// spanStates reads pandokos.telemetry.spans by state; an absent point is zero.
func spanStates(t *testing.T, s *snapshot) map[string]float64 {
	t.Helper()
	out := map[string]float64{"ended": 0, "exported": 0, "failed": 0}
	for _, p := range s.Series["pandokos.telemetry.spans"].pointsOrNil() {
		out[p.Attrs["pandokos.telemetry.state"]] += p.Value
	}
	return out
}

func failures(s *snapshot, signal string) float64 {
	var n float64
	for _, p := range s.Series["pandokos.telemetry.export_failures"].pointsOrNil() {
		if p.Attrs["pandokos.telemetry.signal"] == signal {
			n += p.Value
		}
	}
	return n
}

func (s *series) pointsOrNil() []point {
	if s == nil {
		return nil
	}
	return s.Points
}

// TestSelfObservation is T-923: spans ended, exported and failed are counted against otlptest with
// Fail(n); export failures are counted by signal and class; and, with the collector stalled and the
// queue full, ended - exported - failed equals the spans the batcher dropped.
func TestSelfObservation(t *testing.T) {
	observer := newRig(t, nil) // reads the process-wide counters of telemetry
	base := observer.collect()
	b := spanStates(t, base)
	bFail := failures(base, "traces")

	t.Run("exported and failed spans, failures by signal and class", func(t *testing.T) {
		failing := otlptest.New(t)
		failing.Fail(1) // the first batch is refused
		tr1, _ := newTraces(t, failing.URL(), nil)
		for i := 0; i < 3; i++ {
			emit(tr1, fmt.Sprintf("failed-%d", i))
		}
		shutdown(t, tr1)

		healthy := otlptest.New(t)
		tr2, _ := newTraces(t, healthy.URL(), nil)
		for i := 0; i < 4; i++ {
			emit(tr2, fmt.Sprintf("exported-%d", i))
		}
		shutdown(t, tr2)
		testutil.Eventually(t, 5*time.Second, "the four spans", func() bool { return len(healthy.Spans()) == 4 })

		s := spanStates(t, observer.collectT(t))
		if d := s["ended"] - b["ended"]; d != 7 {
			t.Errorf("ended +%v, want +7", d)
		}
		if d := s["exported"] - b["exported"]; d != 4 {
			t.Errorf("exported +%v, want +4", d)
		}
		if d := s["failed"] - b["failed"]; d != 3 {
			t.Errorf("failed +%v, want +3", d)
		}
		snap := observer.collectT(t)
		if got := failures(snap, "traces") - bFail; got != 1 {
			t.Errorf("trace export failures +%v, want +1", got)
		}
		for _, p := range snap.find(t, "pandokos.telemetry.export_failures", attrs("pandokos.telemetry.signal", "traces")) {
			if !in(p.Attrs["error.type"], []string{"timeout", "status", "connection", "other"}) {
				t.Errorf("error.type %q is not a class", p.Attrs["error.type"])
			}
		}
	})

	t.Run("a failing metrics export is counted with signal metrics", func(t *testing.T) {
		c := otlptest.New(t)
		c.Fail(1)
		r := newRigOn(t, c, nil)
		r.m.ObserveRequest(chatRequest("model-a"))
		testutil.Eventually(t, 5*time.Second, "the failure line", func() bool { return len(r.logs.Find(t, "metric export failing")) >= 1 })
		snap := observer.collectT(t)
		if got := failures(snap, "metrics"); got < 1 {
			t.Errorf("metric export failures = %v, want at least 1", got)
		}
	})

	t.Run("with the collector stalled and the queue full, dropped = ended - exported - failed", func(t *testing.T) {
		before := spanStates(t, observer.collectT(t))
		c := otlptest.New(t)
		c.Stall()
		tr, _ := newTraces(t, c.URL(), func(o *telemetry.TracesOptions) { o.Timeout = 3 * time.Second })
		const emitted = 3000 // more than the queue of 2048 and the batch in flight
		for i := 0; i < emitted; i++ {
			emit(tr, "x")
		}
		time.Sleep(200 * time.Millisecond)
		c.Release() // the batch in flight completes; the queue is flushed by Shutdown
		shutdown(t, tr)
		testutil.Eventually(t, 5*time.Second, "the collector to decode what it got", func() bool {
			n := len(c.Spans())
			time.Sleep(50 * time.Millisecond)
			return n == len(c.Spans()) && n > 0
		})
		received := len(c.Spans())

		after := spanStates(t, observer.collectT(t))
		ended, exported, failed := after["ended"]-before["ended"], after["exported"]-before["exported"], after["failed"]-before["failed"]
		if ended != emitted {
			t.Fatalf("ended +%v, want +%d", ended, emitted)
		}
		dropped := float64(emitted - received)
		if dropped <= 0 {
			t.Fatalf("the batcher dropped nothing (%d received): the test did not fill the queue", received)
		}
		if got := ended - exported - failed; got != dropped {
			t.Errorf("ended - exported - failed = %v, want the %v spans the collector did not receive (ended %v, exported %v, failed %v)", got, dropped, ended, exported, failed)
		}
	})
}
