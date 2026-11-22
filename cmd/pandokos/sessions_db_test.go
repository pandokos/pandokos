package main

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestSessionsOpen is T-920 (sessions part): after each round of the purge loop the number of
// sessions open is counted with CountOpen and kept for the metric; it equals the rows of live
// sessions; a failed count keeps the last value, logs `session count failed` at warn without
// anything of a user, and does not stop the loop. The tests use two methods of people that the
// sprint adds: purgeOnce, one round of runPurge, and sessionsOpen, the last count and whether one
// has been made.
func TestSessionsOpen(t *testing.T) {
	d := dbtest.NewMigrated(t)
	db := d.Open(t, store.RoleAdmin)
	cfg := config.Defaults()
	idle := time.Duration(cfg.Auth.Session.IdleTimeout)
	logs := &testutil.LogBuffer{}
	logger := telemetry.NewLogger(logs, "debug")
	p, err := newPeople(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := p.sessionsOpen(); ok || n != 0 {
		t.Fatalf("before the first round: %d, %v: there is no count yet", n, ok)
	}

	seed := func(name string) string {
		u := d.PlatformUser(t, name, nil)
		hash := make([]byte, 32)
		_, _ = rand.Read(hash)
		return d.Session(t, u.ID, "local", hash, time.Hour)
	}
	live1, live2 := seed("live.one"), seed("live.two")
	expired, idled := seed("expired.one"), seed("idle.one")
	d.Age(t, "user_session", "id = '"+expired+"'::uuid", 13*time.Hour, "created_at", "last_used_at", "expires_at")
	d.Age(t, "user_session", "id = '"+idled+"'::uuid", idle+time.Hour, "last_used_at")
	_, _ = live1, live2

	round := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		p.purgeOnce(ctx, logger)
	}

	round()
	rows := d.Count(t, "SELECT count(*) FROM pandokos.user_session")
	if n, ok := p.sessionsOpen(); !ok || n != 2 || int(n) != rows {
		t.Errorf("after the first round: %d open, %v; the table holds %d rows (the expired and the idle ones were purged)", n, ok, rows)
	}

	seed("live.three")
	round()
	if n, ok := p.sessionsOpen(); !ok || n != 3 || n != int64(d.Count(t, "SELECT count(*) FROM pandokos.user_session")) {
		t.Errorf("after a new session: %d open, %v", n, ok)
	}

	// A database that cannot answer: the last value is kept, a line says so, the round returns.
	logs.Reset()
	d.SetAcceptingConnections(t, false)
	round()
	d.SetAcceptingConnections(t, true)
	if n, ok := p.sessionsOpen(); !ok || n != 3 {
		t.Errorf("after a failed count: %d, %v, want the last value 3 kept", n, ok)
	}
	lines := logs.Find(t, "session count failed")
	if len(lines) != 1 || lines[0]["level"] != "WARN" {
		t.Fatalf("`session count failed`: %v", lines)
	}
	testutil.AssertAbsent(t, "the logs", logs.String(), "live.one", "live.two", "live.three")

	// And it recovers at the next round.
	round()
	if n, ok := p.sessionsOpen(); !ok || n != 3 {
		t.Errorf("after the database came back: %d, %v", n, ok)
	}
}
