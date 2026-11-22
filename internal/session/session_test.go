package session_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
)

var reToken = regexp.MustCompile(`^pds_[A-Za-z0-9_-]{43}$`)

// TestToken is T-771.
func TestToken(t *testing.T) {
	seen := map[string]bool{}
	hashes := map[[32]byte]bool{}
	for i := 0; i < 10000; i++ {
		tok, hash, err := session.NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		v := tok.Reveal()
		if !reToken.MatchString(v) || len(v) != 47 {
			t.Fatalf("token %q: want pds_ and 43 characters of unpadded base64url (47 in all)", v)
		}
		if seen[v] || hashes[hash] {
			t.Fatalf("a token or a hash was made twice")
		}
		seen[v], hashes[hash] = true, true
		if want := sha256.Sum256([]byte(v)); hash != want {
			t.Fatalf("the hash is not the SHA-256 of the whole token")
		}
		got, ok := session.ParseToken(v)
		if !ok || got != hash {
			t.Fatalf("ParseToken of a token that NewToken made: ok=%v", ok)
		}
	}
	if session.TokenPrefix != "pds_" || session.CookieName != "__Host-pandokos_session" {
		t.Error("the prefix or the cookie name changed")
	}

	tok, _, _ := session.NewToken()
	v := tok.Reveal()
	for name, bad := range map[string]string{
		"empty":           "",
		"prefix only":     "pds_",
		"no prefix":       v[4:],
		"other prefix":    "pdk_" + v[4:],
		"upper prefix":    "PDS_" + v[4:],
		"short":           v[:len(v)-1],
		"long":            v + "A",
		"padded":          v[:len(v)-1] + "=",
		"standard base64": v[:10] + "+" + v[11:],
		"space":           " " + v,
		"trailing space":  v + " ",
		"newline":         v + "\n",
		"nul":             v[:10] + "\x00" + v[11:],
		"non-ascii":       v[:10] + "é" + v[12:],
		"bearer":          "Bearer " + v,
		"two tokens":      v + " " + v,
	} {
		if _, ok := session.ParseToken(bad); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

// FuzzParseToken is the fuzz target of T-771: nothing panics, and what is accepted has the shape.
func FuzzParseToken(f *testing.F) {
	tok, _, err := session.NewToken()
	if err != nil {
		f.Fatalf("NewToken: %v", err)
	}
	for _, s := range []string{"", "pds_", tok.Reveal(), tok.Reveal() + "=", strings.Repeat("pds_", 20), "pds_\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		hash, ok := session.ParseToken(s)
		if !ok {
			return
		}
		if !reToken.MatchString(s) {
			t.Fatalf("accepted %q", s)
		}
		if hash != sha256.Sum256([]byte(s)) {
			t.Fatalf("wrong hash for %q", s)
		}
	})
}

// TestCookie is T-772: the exact Set-Cookie strings, which never vary with the listener.
func TestCookie(t *testing.T) {
	tok, _, err := session.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	want := "__Host-pandokos_session=" + tok.Reveal() + "; Path=/; HttpOnly; Secure; SameSite=Strict"
	// The attributes are the same with and without TLS on the listener, loopback included: the
	// function takes no argument that could change them.
	for _, listener := range []string{"with TLS", "without TLS"} {
		if got := session.Cookie(tok).String(); got != want {
			t.Errorf("%s: Set-Cookie %q, want %q", listener, got, want)
		}
	}
	c := session.Cookie(tok)
	if c.Domain != "" || c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Errorf("Domain, Max-Age and Expires must be absent: %+v", c)
	}
	const clear = "__Host-pandokos_session=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict"
	if got := session.ClearCookie().String(); got != clear {
		t.Errorf("clearing Set-Cookie %q, want %q", got, clear)
	}
}

// clock is the real time plus an offset that the test moves: SQL stamps rows with now(), so the
// store's clock must agree with it at the start.
type clock struct{ offset atomic.Int64 }

func (c *clock) now() time.Time          { return time.Now().Add(time.Duration(c.offset.Load())) }
func (c *clock) advance(d time.Duration) { c.offset.Add(int64(d)) }

func newStore(t *testing.T, d *dbtest.Database, clk *clock, maxPerUser int) *session.Store {
	t.Helper()
	return session.NewStore(session.Options{
		DB: d.Open(t, store.RoleAdmin), Now: clk.now, Idle: 30 * time.Minute, Absolute: 12 * time.Hour, MaxPerUser: maxPerUser,
	})
}

func create(t *testing.T, d *dbtest.Database, s *session.Store, userID, method string) (secret.Secret, session.Session) {
	t.Helper()
	ctx := context.Background()
	var tok secret.Secret
	var sess session.Session
	err := d.Open(t, store.RoleAdmin).Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		tok, sess, err = s.Create(ctx, tx, userID, method)
		return err
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return tok, sess
}

// TestStoreLifecycle covers Create, Lookup, Delete and DeleteOfUser, and the three ways a session
// stops being valid on Lookup: idle, absolute, and a user that is no longer active.
func TestStoreLifecycle(t *testing.T) {
	d := dbtest.NewMigrated(t)
	clk := &clock{}
	s := newStore(t, d, clk, 10)
	ctx := context.Background()
	u := d.PlatformUser(t, "ada.lovelace", []string{"platform-admin"})
	other := d.PlatformUser(t, "grace.hopper", []string{"platform-admin"})

	tok, sess := create(t, d, s, u.ID, "local")
	if !reToken.MatchString(tok.Reveal()) || sess.UserID != u.ID || sess.Method != "local" || sess.ID == "" {
		t.Fatalf("Create: %+v", sess)
	}
	if got := sess.ExpiresAt.Sub(sess.CreatedAt); got < 11*time.Hour+59*time.Minute || got > 12*time.Hour+time.Minute {
		t.Errorf("the session lasts %v, want the absolute lifetime of 12h", got)
	}
	got, user, err := s.Lookup(ctx, tok.Reveal())
	if err != nil || got.ID != sess.ID || user.ID != u.ID || user.Username != "ada.lovelace" || user.State != "active" {
		t.Fatalf("Lookup: %+v %+v %v", got, user, err)
	}
	for _, bad := range []string{"", "pds_", "pds_" + strings.Repeat("A", 43), "not a token"} {
		if _, _, err := s.Lookup(ctx, bad); !errors.Is(err, session.ErrNoSession) {
			t.Errorf("Lookup(%q): %v, want ErrNoSession", bad, err)
		}
	}

	// Idle: no use for longer than the idle timeout.
	clk.advance(29 * time.Minute)
	if _, _, err := s.Lookup(ctx, tok.Reveal()); err != nil {
		t.Errorf("29 minutes without use: %v", err)
	}
	clk.advance(2 * time.Minute)
	if _, _, err := s.Lookup(ctx, tok.Reveal()); !errors.Is(err, session.ErrNoSession) {
		t.Errorf("31 minutes without use: %v, want ErrNoSession", err)
	}

	// Absolute: used all along, still ends at expires_at.
	clk2 := &clock{}
	s2 := newStore(t, d, clk2, 10)
	tok2, sess2 := create(t, d, s2, u.ID, "local")
	for i := 0; i < 25; i++ { // 12.5 hours in steps of 30 minutes, each step a use
		clk2.advance(29 * time.Minute)
		if _, err := d.Super(t).Exec(ctx, "UPDATE pandokos.user_session SET last_used_at = $2 WHERE id = $1::uuid", sess2.ID, clk2.now()); err != nil {
			t.Fatal(err) // what a use at that time would have written
		}
		_, _, err := s2.Lookup(ctx, tok2.Reveal())
		if clk2.now().After(sess2.ExpiresAt) && !errors.Is(err, session.ErrNoSession) {
			t.Fatalf("past the absolute lifetime, still valid (step %d): %v", i, err)
		}
		if clk2.now().Before(sess2.ExpiresAt.Add(-time.Minute)) && err != nil {
			t.Fatalf("before the absolute lifetime, refused (step %d): %v", i, err)
		}
	}

	// A user that is not active: no session.
	clk3 := &clock{}
	s3 := newStore(t, d, clk3, 10)
	tok3, _ := create(t, d, s3, other.ID, "local")
	if _, _, err := s3.Lookup(ctx, tok3.Reveal()); err != nil {
		t.Fatalf("Lookup before disabling: %v", err)
	}
	d.Super(t).Exec(ctx, "UPDATE pandokos.app_user SET state = 'disabled' WHERE id = $1::uuid", other.ID) //nolint:errcheck // the next Lookup shows it
	if _, _, err := s3.Lookup(ctx, tok3.Reveal()); !errors.Is(err, session.ErrNoSession) {
		t.Errorf("Lookup of a session of a disabled user: %v, want ErrNoSession", err)
	}

	// Delete and DeleteOfUser end sessions by deleting rows.
	clk4 := &clock{}
	s4 := newStore(t, d, clk4, 10)
	a, sa := create(t, d, s4, u.ID, "local")
	b, sb := create(t, d, s4, u.ID, "oidc")
	c, _ := create(t, d, s4, u.ID, "local")
	db := d.Open(t, store.RoleAdmin)
	if err := db.Platform(ctx, func(ctx context.Context, tx store.Tx) error { return s4.Delete(ctx, tx, sa.ID) }); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := s4.Lookup(ctx, a.Reveal()); !errors.Is(err, session.ErrNoSession) {
		t.Errorf("a deleted session: %v", err)
	}
	var n int64
	if err := db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		n, err = s4.DeleteOfUser(ctx, tx, u.ID, sb.ID)
		return err
	}); err != nil || n != 1 {
		t.Fatalf("DeleteOfUser except one: n=%d err=%v", n, err)
	}
	if _, _, err := s4.Lookup(ctx, b.Reveal()); err != nil {
		t.Errorf("the session that was excepted: %v", err)
	}
	if _, _, err := s4.Lookup(ctx, c.Reveal()); !errors.Is(err, session.ErrNoSession) {
		t.Errorf("a session of the user that was deleted: %v", err)
	}
}

// TestStoreStoresOnlyTheHash checks that the token is not in the database.
func TestStoreStoresOnlyTheHash(t *testing.T) {
	d := dbtest.NewMigrated(t)
	s := newStore(t, d, &clock{}, 10)
	u := d.PlatformUser(t, "ada.lovelace", []string{"platform-admin"})
	tok, _ := create(t, d, s, u.ID, "local")
	if n := d.Count(t, "SELECT count(*) FROM pandokos.user_session WHERE token_hash = sha256($1::bytea)", tok.Reveal()); n != 1 {
		t.Errorf("the row of the session does not hold the SHA-256 of the token (%d rows)", n)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.user_session s WHERE s::text LIKE '%' || $1 || '%'", tok.Reveal()[4:]); n != 0 {
		t.Error("the token is in the row of the session")
	}
}

// TestMaxPerUser checks that the oldest sessions beyond the maximum are removed by Create.
func TestMaxPerUser(t *testing.T) {
	d := dbtest.NewMigrated(t)
	s := newStore(t, d, &clock{}, 3)
	u := d.PlatformUser(t, "ada.lovelace", []string{"platform-admin"})
	var tokens []secret.Secret
	for i := 0; i < 5; i++ {
		tok, _ := create(t, d, s, u.ID, "local")
		tokens = append(tokens, tok)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.user_session WHERE user_id = $1::uuid", u.ID); n != 3 {
		t.Fatalf("%d sessions, want 3", n)
	}
	for i, tok := range tokens {
		_, _, err := s.Lookup(context.Background(), tok.Reveal())
		if live := i >= 2; live != (err == nil) {
			t.Errorf("session %d: live=%v err=%v", i, live, err)
		}
	}
}
