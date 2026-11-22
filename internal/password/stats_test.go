package password_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestHasherStats is T-920 (hasher part): with both slots held by running computations Stats() says
// two are active; a caller blocked for a slot is waiting until it gets one or gives up; nothing is
// active or waiting at rest.
func TestHasherStats(t *testing.T) {
	// A cost that makes one computation last long enough to be seen.
	slow := password.Params{MemoryKiB: 262144, Iterations: 6, Parallelism: 1}
	h := hasher(t, slow, 2)
	pw := secret.New("a password for the stats test")

	if st := h.Stats(); st.Active != 0 || st.Waiting != 0 {
		t.Fatalf("an idle hasher: %+v", st)
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Hash(context.Background(), pw); err != nil {
				t.Errorf("Hash: %v", err)
			}
		}()
	}
	testutil.Eventually(t, 2*time.Second, "both slots taken", func() bool { return h.Stats().Active == 2 })
	if st := h.Stats(); st.Waiting != 0 {
		t.Errorf("nobody waits yet: %+v", st)
	}

	// A third computation waits for a slot and gives up when its context ends: it is waiting while
	// it waits and never active.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	errc := make(chan error, 1)
	go func() { _, err := h.Hash(ctx, pw); errc <- err }()
	testutil.Eventually(t, 2*time.Second, "a caller waiting", func() bool { return h.Stats().Waiting == 1 })
	if st := h.Stats(); st.Active != 2 {
		t.Errorf("a waiting caller took a slot: %+v", st)
	}
	if err := <-errc; !errors.Is(err, password.ErrBusy) {
		t.Fatalf("the third computation: %v, want ErrBusy", err)
	}
	if st := h.Stats(); st.Waiting != 0 {
		t.Errorf("the caller that gave up is still waiting: %+v", st)
	}

	wg.Wait()
	if st := h.Stats(); st.Active != 0 || st.Waiting != 0 {
		t.Errorf("at rest: %+v", st)
	}
}
