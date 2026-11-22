package idem_test

import (
	"errors"
	"testing"

	"github.com/pandokos/pandokos/internal/idem"
)

// TestReplayStats is T-920 (idempotency part): Stats().Replays counts the stored responses returned
// for a key that completed, and nothing else: not a new claim, not a request in progress, not a key
// reused with another request.
func TestReplayStats(t *testing.T) {
	r := newRig(t)
	user := idem.Scope{TenantID: r.tid, PrincipalKind: "user", PrincipalID: "u1"}
	body := []byte(`{"name":"one"}`)
	const path = "/v1/tenants/x/members"

	if got := r.s.Stats().Replays; got != 0 {
		t.Fatalf("a new store has counted %d replays", got)
	}
	c1, replay, err := r.s.Begin(ctx(t), user, "createMember", path, "key-1", body)
	if err != nil || replay != nil {
		t.Fatalf("a new key: replay=%v err=%v", replay, err)
	}
	if _, _, err := r.s.Begin(ctx(t), user, "createMember", path, "key-1", body); !errors.Is(err, idem.ErrInProgress) {
		t.Fatalf("while locked: %v", err)
	}
	if _, _, err := r.s.Begin(ctx(t), user, "createMember", path, "key-1", []byte(`{"name":"two"}`)); !errors.Is(err, idem.ErrKeyReused) {
		t.Fatalf("another body: %v", err)
	}
	if got := r.s.Stats().Replays; got != 0 {
		t.Errorf("%d replays counted before any response was stored", got)
	}

	if err := r.commit(c1, t, user, "user", "0192f0f0-0000-7000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Finish(c1, 201, []byte(`{"user":{"id":"0192f0f0-0000-7000-8000-000000000001"}}`)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		_, replay, err := r.s.Begin(ctx(t), user, "createMember", path, "key-1", body)
		if err != nil || replay == nil {
			t.Fatalf("replay %d: %+v %v", i, replay, err)
		}
		if got := r.s.Stats().Replays; got != int64(i) {
			t.Errorf("after %d replays the count is %d", i, got)
		}
	}
	// A key reused after completion with another body is refused, not replayed.
	if _, _, err := r.s.Begin(ctx(t), user, "createMember", path, "key-1", []byte(`{"name":"two"}`)); !errors.Is(err, idem.ErrKeyReused) {
		t.Fatalf("another body after completion: %v", err)
	}
	if got := r.s.Stats().Replays; got != 3 {
		t.Errorf("a refusal was counted as a replay: %d", got)
	}
}
