package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/ledger"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestServeContinuesWhenPartitionsFail: the partitions are ensured at start and a failure is a
// warning, not a reason to stop (sprint 3.2 section 5.6): the process listens, the keeper retries.
func TestServeContinuesWhenPartitionsFail(t *testing.T) {
	d := dbtest.NewMigrated(t)
	secretCanary := "synthetic-secret-in-the-error"
	calls := 0
	old := ensurePartitions
	ensurePartitions = func(ctx context.Context, _ *ledger.Partitioner) error {
		calls++
		if _, ok := ctx.Deadline(); ok && calls == 1 {
			t.Error("the context of the call carries a deadline of its own")
		}
		return errors.New("ledger: ensure usage partitions: " + secretCanary)
	}
	t.Cleanup(func() { ensurePartitions = old })

	logs := &testutil.LogBuffer{}
	env := append(d.Env(t), "PATH=x", "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0", "PANDOKOS_LISTENERS_ADMIN_ADDRESS=127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- runServe(ctx, nil, logs, logs, env) }()

	testutil.Eventually(t, 20*time.Second, "the process listens", func() bool {
		return strings.Contains(logs.String(), `"msg":"listening"`)
	})
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d: %s", code, logs.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not return")
	}
	warn := logs.Find(t, "usage partitions not ensured")
	if len(warn) != 1 || warn[0]["level"] != "WARN" || warn[0]["error_class"] == nil {
		t.Fatalf("warnings: %v", warn)
	}
	if len(logs.Find(t, "startup failed")) != 0 {
		t.Errorf("startup failed was logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), secretCanary) {
		t.Errorf("the error text is in the log")
	}
}
