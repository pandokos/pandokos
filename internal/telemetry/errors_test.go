package telemetry_test

import (
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

const errorCanary = "synthetic-error-text-canary-8e11"

// TestSDKErrorLine is T-909: an error given to otel.Handle is logged as `telemetry error` (at most
// once a minute, with `count` and `error_class`, never the error's text), an export failure of
// spans as `trace export failing` and of metrics as `metric export failing`, each from the handler
// of its own provider.
func TestSDKErrorLine(t *testing.T) {
	t.Run("the process-wide handler logs `telemetry error`, rate-limited", func(t *testing.T) {
		r := newRig(t, nil) // the last provider built owns the process-wide handler
		for i := 0; i < 5; i++ {
			otel.Handle(errors.New("sdk problem " + errorCanary))
		}
		testutil.Eventually(t, 5*time.Second, "`telemetry error`", func() bool { return len(r.logs.Find(t, "telemetry error")) >= 1 })
		time.Sleep(100 * time.Millisecond)
		lines := r.logs.Find(t, "telemetry error")
		if len(lines) != 1 {
			t.Fatalf("%d `telemetry error` lines for five errors in a row, want 1", len(lines))
		}
		if lines[0]["count"] == nil || lines[0]["error_class"] == nil || lines[0]["level"] != "WARN" {
			t.Errorf("the line: %v", lines[0])
		}
		testutil.AssertAbsent(t, "the logs", r.logs.String(), errorCanary)
	})

	t.Run("the traces built last own it too", func(t *testing.T) {
		_, logs := newTraces(t, nil, nil)
		otel.Handle(errors.New("sdk problem " + errorCanary))
		testutil.Eventually(t, 5*time.Second, "`telemetry error`", func() bool { return len(logs.Find(t, "telemetry error")) >= 1 })
		testutil.AssertAbsent(t, "the logs", logs.String(), errorCanary)
	})

	t.Run("export failures are logged by their own provider's handler", func(t *testing.T) {
		c := otlptest.New(t)
		c.Fail(1 << 30)
		rm := newRigOn(t, c, nil)
		tr, logsT := newTraces(t, c.URL(), nil)
		emit(tr, "x")
		shutdown(t, tr)
		rm.m.ObserveRequest(chatRequest("model-a"))

		testutil.Eventually(t, 5*time.Second, "`trace export failing`", func() bool { return len(logsT.Find(t, "trace export failing")) >= 1 })
		testutil.Eventually(t, 5*time.Second, "`metric export failing`", func() bool { return len(rm.logs.Find(t, "metric export failing")) >= 1 })
		if n := len(rm.logs.Find(t, "trace export failing")); n != 0 {
			t.Errorf("the metrics' logger got %d `trace export failing` lines", n)
		}
		if n := len(logsT.Find(t, "metric export failing")); n != 0 {
			t.Errorf("the traces' logger got %d `metric export failing` lines", n)
		}
		for _, l := range append(logsT.Find(t, "trace export failing"), rm.logs.Find(t, "metric export failing")...) {
			if l["count"] == nil || l["error_class"] == nil {
				t.Errorf("the line has no count or class: %v", l)
			}
		}
		testutil.AssertAbsent(t, "the logs", logsT.String()+rm.logs.String(), headerCanary)
	})
}
