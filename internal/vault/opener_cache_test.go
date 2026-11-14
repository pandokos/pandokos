package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
)

// This file is in package vault, not vault_test: the Opener reads through the unexported
// interface source, and the test injects a fake (plan, B3). The tests of this package that are
// in package vault must not import test/testutil, which imports the vault.
//
// What the test expects of the production code (internal seam of C4):
//
//	type source interface {
//		credentialByID(ctx context.Context, id tenant.ID, credentialID string) (queries.GetCredentialForUseRow, error)
//		defaultCredential(ctx context.Context, id tenant.ID, provider string) (queries.GetDefaultCredentialForUseRow, error)
//		dataKey(ctx context.Context, id tenant.ID, version int32) (queries.GetDataKeyRow, error)
//	}
//	func newOpener(o OpenerOptions, src source) (*Opener, error)
//
// A row that does not exist is store.ErrNoRows. NewOpener validates the options and calls newOpener
// with the database implementation. The constants maxCredentialEntries and maxDataKeyEntries
// are the bounds of the two caches.

const (
	ttlCredential = 30 * time.Second
	ttlDataKey    = 5 * time.Minute
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeSource is the database of the unit test: rows by tenant and key, and a count of every read.
type fakeSource struct {
	mu       sync.Mutex
	creds    map[string]queries.GetCredentialForUseRow
	defaults map[string]queries.GetDefaultCredentialForUseRow
	deks     map[string]queries.GetDataKeyRow
	fail     map[string]error // by kind: "cred", "def", "dek"
	reads    map[string]int   // "cred:<tenant>", "def:<tenant>", "dek:<tenant>"
	onRead   func()           // called by every read, outside the lock: the latency of the database
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		creds: map[string]queries.GetCredentialForUseRow{}, defaults: map[string]queries.GetDefaultCredentialForUseRow{},
		deks: map[string]queries.GetDataKeyRow{}, fail: map[string]error{}, reads: map[string]int{},
	}
}

func (f *fakeSource) credentialByID(_ context.Context, id tenant.ID, credentialID string) (queries.GetCredentialForUseRow, error) {
	f.mu.Lock()
	hook := f.onRead
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads["cred:"+id.String()]++
	if err := f.fail["cred"]; err != nil {
		return queries.GetCredentialForUseRow{}, err
	}
	r, ok := f.creds[id.String()+"/"+credentialID]
	if !ok {
		return queries.GetCredentialForUseRow{}, store.ErrNoRows
	}
	return r, nil
}

func (f *fakeSource) defaultCredential(_ context.Context, id tenant.ID, provider string) (queries.GetDefaultCredentialForUseRow, error) {
	f.mu.Lock()
	hook := f.onRead
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads["def:"+id.String()]++
	if err := f.fail["def"]; err != nil {
		return queries.GetDefaultCredentialForUseRow{}, err
	}
	r, ok := f.defaults[id.String()+"/"+provider]
	if !ok {
		return queries.GetDefaultCredentialForUseRow{}, store.ErrNoRows
	}
	return r, nil
}

func (f *fakeSource) dataKey(_ context.Context, id tenant.ID, version int32) (queries.GetDataKeyRow, error) {
	f.mu.Lock()
	hook := f.onRead
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads["dek:"+id.String()]++
	if err := f.fail["dek"]; err != nil {
		return queries.GetDataKeyRow{}, err
	}
	r, ok := f.deks[fmt.Sprintf("%s/%d", id, version)]
	if !ok {
		return queries.GetDataKeyRow{}, store.ErrNoRows
	}
	return r, nil
}

func (f *fakeSource) count(kind string, id tenant.ID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[kind+":"+id.String()]
}

func (f *fakeSource) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.reads {
		n += c
	}
	return n
}

// uworld is one Opener over a fake source, with a real local provider and real sealing.
type uworld struct {
	t    *testing.T
	prov *LocalProvider
	src  *fakeSource
	clk  *testClock
	logs *bytes.Buffer
	o    *Opener
	deks map[tenant.ID][]byte // the raw data key of each tenant, to seal with

	saved map[string]queries.GetDataKeyRow // the data key rows, put aside while a case breaks them
}

func newUWorld(t *testing.T) *uworld {
	t.Helper()
	key, err := GenerateLocalKey()
	if err != nil {
		t.Fatal(err)
	}
	prov, err := NewLocalProvider(key)
	if err != nil {
		t.Fatal(err)
	}
	w := &uworld{t: t, prov: prov, src: newFakeSource(), clk: &testClock{t: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)},
		logs: &bytes.Buffer{}, deks: map[tenant.ID][]byte{}}
	w.o, err = newOpener(OpenerOptions{Provider: prov, Logger: telemetry.NewLogger(w.logs, "debug"), Now: w.clk.now,
		CredentialTTL: ttlCredential, DataKeyTTL: ttlDataKey}, w.src)
	if err != nil {
		t.Fatalf("newOpener: %v", err)
	}
	return w
}

func uid(n int) tenant.ID {
	id, err := tenant.ParseID(fmt.Sprintf("0192f0f0-0000-7000-8000-%012x", n))
	if err != nil {
		panic(err)
	}
	return id
}

func cuid(n int) string { return fmt.Sprintf("0192f0f0-1111-7000-8000-%012x", n) }

// addTenant creates the data key of a tenant.
func (w *uworld) addTenant(tid tenant.ID) {
	w.t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		w.t.Fatal(err)
	}
	version, wrapped, err := w.prov.Wrap(context.Background(), raw, DataAAD(tid, 1))
	if err != nil {
		w.t.Fatal(err)
	}
	w.deks[tid] = raw
	w.src.deks[fmt.Sprintf("%s/%d", tid, 1)] = queries.GetDataKeyRow{Version: 1, KekVersion: version, WrappedKey: wrapped}
}

// addCredential seals plaintext for the credential and stores the row; the state decides what
// the row holds ("revoked" has no secret).
func (w *uworld) addCredential(tid tenant.ID, id, provider, label, state, plaintext string, isDefault bool) {
	w.t.Helper()
	dk, err := NewDataKey(w.deks[tid], tid, 1)
	if err != nil {
		w.t.Fatal(err)
	}
	var sealed []byte
	if state != "revoked" {
		if sealed, err = dk.Seal(id, []byte(plaintext)); err != nil {
			w.t.Fatal(err)
		}
	}
	fp := dk.Fingerprint([]byte(plaintext))
	w.src.creds[tid.String()+"/"+id] = queries.GetCredentialForUseRow{ID: id, Provider: provider, Label: label, State: state, KeyVersion: 1, Secret: sealed, Fingerprint: fp}
	if isDefault {
		w.src.defaults[tid.String()+"/"+provider] = queries.GetDefaultCredentialForUseRow{ID: id, Provider: provider, Label: label, State: state, KeyVersion: 1, Secret: sealed, Fingerprint: fp}
	}
}

func principalOf(tid tenant.ID, pin string) authz.Principal {
	return authz.Principal{Kind: authz.KindServiceAccount, ID: "0192f0f0-2222-7000-8000-000000000001", Name: "caller", KeyID: "tabcdefghijklmnop",
		TenantID: tid, TenantName: "t", TenantState: tenant.StateActive, Roles: []string{authz.RoleInference}, CredentialID: pin}
}

func (w *uworld) open(p authz.Principal, provider string) (Opened, error) {
	w.t.Helper()
	return w.o.Open(context.Background(), Use{Principal: p, RequestID: "req-0001", Provider: provider})
}

func (w *uworld) mustOpen(p authz.Principal, provider, wantSecret string) Opened {
	w.t.Helper()
	got, err := w.open(p, provider)
	if err != nil {
		w.t.Fatalf("Open: %v", err)
	}
	if got.Secret.Reveal() != wantSecret {
		w.t.Fatalf("Open returned another secret (%d bytes, want %d)", len(got.Secret.Reveal()), len(wantSecret))
	}
	return got
}

// TestOpenerCache is T-305.
func TestOpenerCache(t *testing.T) {
	a, b := uid(1), uid(2)

	t.Run("an entry lives for the lifetime from its creation however often it is read", func(t *testing.T) {
		w := newUWorld(t)
		w.addTenant(a)
		w.addCredential(a, cuid(1), "openai", "main", "active", "plaintext-one-0001", true)
		p := principalOf(a, "")
		w.mustOpen(p, "openai", "plaintext-one-0001")
		if w.src.count("def", a) != 1 || w.src.count("dek", a) != 1 {
			t.Fatalf("a miss reads the default and the data key once: %d, %d", w.src.count("def", a), w.src.count("dek", a))
		}
		for i := 0; i < 29; i++ {
			w.clk.advance(time.Second)
			w.mustOpen(p, "openai", "plaintext-one-0001")
		}
		w.clk.advance(ttlCredential - 29*time.Second - time.Nanosecond)
		w.mustOpen(p, "openai", "plaintext-one-0001")
		if n := w.src.count("def", a); n != 1 {
			t.Fatalf("the entry was read again %d times within its lifetime", n-1)
		}
		w.clk.advance(time.Nanosecond) // exactly the lifetime: use does not extend it
		w.mustOpen(p, "openai", "plaintext-one-0001")
		if n := w.src.count("def", a); n != 2 {
			t.Errorf("the entry outlived its lifetime: %d reads of the default", n)
		}
		if n := w.src.count("dek", a); n != 1 {
			t.Errorf("the data key was read again after the lifetime of the credential: %d reads", n)
		}
	})

	t.Run("a change in the database is seen after the lifetime and not before", func(t *testing.T) {
		w := newUWorld(t)
		w.addTenant(a)
		w.addCredential(a, cuid(1), "openai", "main", "active", "plaintext-one-0001", true)
		p := principalOf(a, "")
		w.mustOpen(p, "openai", "plaintext-one-0001")
		w.addCredential(a, cuid(1), "openai", "main", "disabled", "plaintext-one-0001", true)
		w.clk.advance(ttlCredential - time.Nanosecond)
		w.mustOpen(p, "openai", "plaintext-one-0001") // still the cached one
		w.clk.advance(time.Nanosecond)
		_, err := w.open(p, "openai")
		var nu *NotUsableError
		if !errors.As(err, &nu) || nu.State != "disabled" {
			t.Errorf("after the lifetime: %v, want NotUsableError{disabled}", err)
		}
	})

	t.Run("the lifetime counts from the start of Open, not from the end of its reads", func(t *testing.T) {
		w := newUWorld(t)
		w.addTenant(a)
		w.addCredential(a, cuid(1), "openai", "main", "active", "plaintext-one-0001", true)
		p := principalOf(a, "")
		const latency = 10 * time.Second
		w.src.onRead = func() { w.clk.advance(latency) } // the default and the data key: 20s in all
		w.mustOpen(p, "openai", "plaintext-one-0001")
		w.src.onRead = nil
		// 20s have passed; the entry was stamped at the start, so it dies 10s from now.
		w.clk.advance(ttlCredential - 2*latency - time.Nanosecond)
		w.mustOpen(p, "openai", "plaintext-one-0001")
		if n := w.src.count("def", a); n != 1 {
			t.Fatalf("read again %d times before the lifetime from the start", n-1)
		}
		w.clk.advance(time.Nanosecond)
		w.mustOpen(p, "openai", "plaintext-one-0001")
		if n := w.src.count("def", a); n != 2 {
			t.Fatalf("%d reads of the default, want 2: the lifetime counted from the end of the reads", n)
		}
		// The data key read by the first open dies at its start plus the data key lifetime too: the
		// clock is at 30s, the second open found the key cached.
		w.clk.advance(ttlDataKey - ttlCredential)
		w.mustOpen(p, "openai", "plaintext-one-0001")
		if n := w.src.count("dek", a); n != 2 {
			t.Fatalf("%d reads of the data key, want 2: its lifetime counted from the end of the reads", n)
		}
	})

	t.Run("the default alias and the entry of the credential are one cache", func(t *testing.T) {
		w := newUWorld(t)
		w.addTenant(a)
		w.addCredential(a, cuid(1), "openai", "main", "active", "plaintext-one-0001", true)
		w.mustOpen(principalOf(a, ""), "openai", "plaintext-one-0001")
		// A key pinned to the same credential finds it without reading it.
		got := w.mustOpen(principalOf(a, cuid(1)), "openai", "plaintext-one-0001")
		if !got.Pinned {
			t.Error("Pinned is false for a pinned key")
		}
		if n := w.src.count("cred", a); n != 0 {
			t.Errorf("the credential entry made by the default alias was not used: %d reads by ID", n)
		}
		// The alias expires with the entry: after the lifetime the default is read again, and it
		// is the new default that is returned.
		w.addCredential(a, cuid(2), "openai", "second", "active", "plaintext-two-0002", true)
		w.clk.advance(ttlCredential)
		got = w.mustOpen(principalOf(a, ""), "openai", "plaintext-two-0002")
		if got.ID != cuid(2) || got.Label != "second" || got.Pinned {
			t.Errorf("after the default moved: %+v", got)
		}
	})

	t.Run("what Open returns", func(t *testing.T) {
		w := newUWorld(t)
		w.addTenant(a)
		w.addCredential(a, cuid(1), "openai", "main", "active", "plaintext-one-0001", true)
		got := w.mustOpen(principalOf(a, ""), "openai", "plaintext-one-0001")
		row := w.src.creds[a.String()+"/"+cuid(1)]
		if got.ID != cuid(1) || got.Provider != "openai" || got.Label != "main" || got.Fingerprint != row.Fingerprint || got.Pinned {
			t.Errorf("Opened = %+v", got)
		}
		if fmt.Sprint(got) == "" || strings.Contains(fmt.Sprintf("%+v", got), "plaintext-one-0001") {
			t.Error("the secret is printed")
		}
	})

	t.Run("the data key lifetime is independent of the credential lifetime", func(t *testing.T) {
		w := newUWorld(t)
		w.addTenant(a)
		w.addCredential(a, cuid(1), "openai", "main", "active", "plaintext-one-0001", true)
		w.addCredential(a, cuid(2), "openai", "second", "active", "plaintext-two-0002", false)
		w.mustOpen(principalOf(a, ""), "openai", "plaintext-one-0001")
		w.clk.advance(time.Second)
		w.mustOpen(principalOf(a, cuid(2)), "openai", "plaintext-two-0002")
		if n := w.src.count("dek", a); n != 1 {
			t.Errorf("a second credential of the tenant read the data key again: %d reads", n)
		}
		w.clk.advance(ttlCredential) // both credential entries are gone, the data key is not
		w.mustOpen(principalOf(a, ""), "openai", "plaintext-one-0001")
		if n := w.src.count("dek", a); n != 1 {
			t.Errorf("the data key was read again after %s: %d reads", ttlCredential, n)
		}
		w.clk.advance(ttlDataKey - ttlCredential - time.Second - time.Nanosecond) // just before the data key lifetime
		w.mustOpen(principalOf(a, cuid(2)), "openai", "plaintext-two-0002")
		if n := w.src.count("dek", a); n != 1 {
			t.Errorf("the data key expired early: %d reads", n)
		}
		w.clk.advance(time.Second + time.Nanosecond)
		w.mustOpen(principalOf(a, ""), "openai", "plaintext-one-0001")
		if n := w.src.count("dek", a); n != 2 {
			t.Errorf("the data key outlived its lifetime: %d reads", n)
		}
	})

	t.Run("resolution", func(t *testing.T) {
		type tc struct {
			name      string
			setup     func(w *uworld)
			pin       string
			provider  string
			wantID    string // "" when an error is wanted
			pinned    bool
			wantErr   error  // errors.Is
			wantState string // a NotUsableError with this state
			defReads  int    // reads of the default, -1: do not check
		}
		cases := []tc{
			{name: "unpinned: the default of the provider", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
				w.addCredential(a, cuid(2), "openai", "other", "active", "plain-other-0002", false)
			}, provider: "openai", wantID: cuid(1), defReads: -1},
			{name: "unpinned: no default is ErrNoCredential", setup: func(w *uworld) {
				w.addCredential(a, cuid(2), "openai", "other", "active", "plain-other-0002", false)
			}, provider: "openai", wantErr: ErrNoCredential, defReads: -1},
			{name: "unpinned: a default of another provider is not this one's", setup: func(w *uworld) {
				w.addCredential(a, cuid(3), "anthropic", "claude", "active", "plain-claude-0003", true)
			}, provider: "openai", wantErr: ErrNoCredential, defReads: -1},
			{name: "unpinned: a disabled default fails, it does not fall back", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "disabled", "plain-default-0001", true)
				w.addCredential(a, cuid(2), "openai", "other", "active", "plain-other-0002", false)
			}, provider: "openai", wantState: "disabled", defReads: -1},
			{name: "unpinned: a revoked default fails, it does not fall back", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "revoked", "plain-default-0001", true)
				w.addCredential(a, cuid(2), "openai", "other", "active", "plain-other-0002", false)
			}, provider: "openai", wantState: "revoked", defReads: -1},
			{name: "pinned: the pin and nothing else", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
				w.addCredential(a, cuid(2), "openai", "other", "active", "plain-other-0002", false)
			}, pin: cuid(2), provider: "openai", wantID: cuid(2), pinned: true, defReads: 0},
			{name: "pinned and disabled: fails, the default is not read", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
				w.addCredential(a, cuid(2), "openai", "other", "disabled", "plain-other-0002", false)
			}, pin: cuid(2), provider: "openai", wantState: "disabled", defReads: 0},
			{name: "pinned and revoked: fails, the default is not read", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
				w.addCredential(a, cuid(2), "openai", "other", "revoked", "plain-other-0002", false)
			}, pin: cuid(2), provider: "openai", wantState: "revoked", defReads: 0},
			{name: "pinned to a credential that is not there: ErrNoCredential, no fallback", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
			}, pin: cuid(9), provider: "openai", wantErr: ErrNoCredential, defReads: 0},
			{name: "pinned to a credential of another provider: the default of the called provider", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
				w.addCredential(a, cuid(3), "anthropic", "claude", "active", "plain-claude-0003", true)
			}, pin: cuid(3), provider: "openai", wantID: cuid(1), pinned: false, defReads: -1},
			{name: "pinned to a disabled credential of another provider: the pin is not in play", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
				w.addCredential(a, cuid(3), "anthropic", "claude", "disabled", "plain-claude-0003", true)
			}, pin: cuid(3), provider: "openai", wantID: cuid(1), pinned: false, defReads: -1},
			{name: "pinned to another provider and no default for the called one", setup: func(w *uworld) {
				w.addCredential(a, cuid(3), "anthropic", "claude", "active", "plain-claude-0003", true)
			}, pin: cuid(3), provider: "openai", wantErr: ErrNoCredential, defReads: -1},
			{name: "pinned, called for the provider of the pin", setup: func(w *uworld) {
				w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
				w.addCredential(a, cuid(3), "anthropic", "claude", "active", "plain-claude-0003", true)
			}, pin: cuid(3), provider: "anthropic", wantID: cuid(3), pinned: true, defReads: 0},
		}
		plains := map[string]string{cuid(1): "plain-default-0001", cuid(2): "plain-other-0002", cuid(3): "plain-claude-0003"}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				w := newUWorld(t)
				w.addTenant(a)
				c.setup(w)
				got, err := w.open(principalOf(a, c.pin), c.provider)
				switch {
				case c.wantID != "":
					if err != nil {
						t.Fatalf("Open: %v", err)
					}
					if got.ID != c.wantID || got.Pinned != c.pinned || got.Secret.Reveal() != plains[c.wantID] || got.Provider != c.provider {
						t.Errorf("Opened = %+v", got)
					}
				default:
					if err == nil {
						t.Fatalf("Open returned %+v, want an error", got)
					}
					if c.wantState != "" {
						var nu *NotUsableError
						if !errors.As(err, &nu) || nu.State != c.wantState {
							t.Errorf("error %v, want NotUsableError{%s}", err, c.wantState)
						}
					} else if !errors.Is(err, c.wantErr) {
						t.Errorf("error %v, want %v", err, c.wantErr)
					}
					if !got.Secret.IsZero() || got.ID != "" {
						t.Errorf("an error came with a result: %+v", got)
					}
				}
				if c.defReads >= 0 && w.src.count("def", a) != c.defReads {
					t.Errorf("%d reads of the default, want %d", w.src.count("def", a), c.defReads)
				}
			})
		}
	})

	t.Run("denied before anything is read", func(t *testing.T) {
		w := newUWorld(t)
		w.addTenant(a)
		w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
		good := principalOf(a, "")
		denied := map[string]authz.Principal{}
		// A platform principal holds no tenant permission.
		denied["a platform principal"] = authz.Principal{Kind: authz.KindPlatform, ID: "p", Roles: []string{authz.RolePlatformAdmin}, TenantID: a}
		p := good
		p.Roles = nil
		denied["no role"] = p
		p = good
		p.Roles = []string{authz.RoleInference}
		p.Scopes = []string{authz.ModelInvoke}
		denied["a key scoped without credential:use"] = p
		p = good
		p.Scopes = []string{}
		denied["a key scoped to nothing"] = p
		for _, st := range []tenant.State{tenant.StateSuspended, tenant.StateProvisioning, tenant.StateDeleting, tenant.StateDeleted} {
			p = good
			p.TenantState = st
			denied["a tenant that is "+string(st)] = p
		}
		p = good
		p.TenantID = tenant.ID{}
		denied["no tenant"] = p
		p = good
		p.Roles = []string{authz.RolePlatformAdmin}
		denied["a platform role on a service account"] = p
		for name, pr := range denied {
			got, err := w.open(pr, "openai")
			if !errors.Is(err, ErrDenied) {
				t.Errorf("%s: %v, want ErrDenied", name, err)
			}
			if !got.Secret.IsZero() {
				t.Errorf("%s: a secret was returned", name)
			}
		}
		if n := w.src.total(); n != 0 {
			t.Errorf("a denied call read the database %d times", n)
		}
		// The request ID and the provider are required, also before any read.
		for name, u := range map[string]Use{
			"no request ID": {Principal: good, Provider: "openai"},
			"no provider":   {Principal: good, RequestID: "req-1"},
			"nothing":       {},
		} {
			got, err := w.o.Open(context.Background(), u)
			if err == nil || !got.Secret.IsZero() {
				t.Errorf("%s: %v", name, err)
			}
		}
		if n := w.src.total(); n != 0 {
			t.Errorf("a call without a request ID or a provider read the database %d times", n)
		}
		// And the good principal works, so the denials above prove something.
		w.mustOpen(good, "openai", "plain-default-0001")
		// A denied call does not use what an allowed one cached.
		if _, err := w.open(denied["no role"], "openai"); !errors.Is(err, ErrDenied) {
			t.Errorf("a denied principal got through the cache: %v", err)
		}
		suspended := good
		suspended.TenantState = tenant.StateSuspended
		if _, err := w.open(suspended, "openai"); !errors.Is(err, ErrDenied) {
			t.Errorf("a suspended tenant got through the cache: %v", err)
		}
	})

	t.Run("invalidation", func(t *testing.T) {
		type tc struct {
			payload string
			dropA   bool
			dropB   bool
		}
		for _, c := range []tc{
			{payload: "vault:" + a.String(), dropA: true},
			{payload: "tenant:" + a.String(), dropA: true},
			{payload: "vault:" + b.String(), dropB: true},
			{payload: "tenant:" + b.String(), dropB: true},
			{payload: "platform"},
			{payload: "catalog"},
			{payload: "catalog:" + a.String()},
			{payload: "catalog:" + b.String()},
			{payload: "catalog:"},
			{payload: "probe:0123456789abcdef"},
			{payload: "probe:"},
			{payload: "something:else", dropA: true, dropB: true},
			{payload: "", dropA: true, dropB: true},
			{payload: "vault:", dropA: true, dropB: true},
			{payload: "tenant:", dropA: true, dropB: true},
			{payload: "FLUSH", dropA: true, dropB: true},
		} {
			t.Run(c.payload, func(t *testing.T) {
				w := newUWorld(t)
				for i, tid := range []tenant.ID{a, b} {
					w.addTenant(tid)
					w.addCredential(tid, cuid(i+1), "openai", "main", "active", fmt.Sprintf("plain-of-tenant-%04d", i), true)
					w.mustOpen(principalOf(tid, ""), "openai", fmt.Sprintf("plain-of-tenant-%04d", i))
				}
				if c.payload == "FLUSH" {
					w.o.Flush()
				} else {
					w.o.Invalidate(c.payload)
				}
				for i, tid := range []tenant.ID{a, b} {
					defBefore, dekBefore := w.src.count("def", tid), w.src.count("dek", tid)
					w.mustOpen(principalOf(tid, ""), "openai", fmt.Sprintf("plain-of-tenant-%04d", i))
					dropped := (i == 0 && c.dropA) || (i == 1 && c.dropB)
					defNow, dekNow := w.src.count("def", tid), w.src.count("dek", tid)
					switch {
					case dropped && (defNow != defBefore+1 || dekNow != dekBefore+1):
						t.Errorf("tenant %d: the credential and the data key were not both dropped (default reads %d→%d, data key reads %d→%d)", i, defBefore, defNow, dekBefore, dekNow)
					case !dropped && (defNow != defBefore || dekNow != dekBefore):
						t.Errorf("tenant %d: an entry was dropped by %q", i, c.payload)
					}
				}
			})
		}
		t.Run("a payload of a tenant that has no entry changes nothing", func(t *testing.T) {
			w := newUWorld(t)
			w.addTenant(a)
			w.addCredential(a, cuid(1), "openai", "main", "active", "plain-of-tenant-0000", true)
			w.mustOpen(principalOf(a, ""), "openai", "plain-of-tenant-0000")
			w.o.Invalidate("vault:" + uid(99).String())
			w.mustOpen(principalOf(a, ""), "openai", "plain-of-tenant-0000")
			if n := w.src.count("def", a); n != 1 {
				t.Errorf("%d reads of the default", n)
			}
		})
	})

	t.Run("a full map is emptied", func(t *testing.T) {
		t.Run("credentials", func(t *testing.T) {
			w := newUWorld(t)
			w.addTenant(a)
			for i := 0; i <= maxCredentialEntries; i++ {
				w.addCredential(a, cuid(i), "openai", fmt.Sprintf("c%d", i), "active", fmt.Sprintf("plain-secret-%06d", i), false)
			}
			for i := 0; i < maxCredentialEntries; i++ {
				w.mustOpen(principalOf(a, cuid(i)), "openai", fmt.Sprintf("plain-secret-%06d", i))
			}
			reads := w.src.count("cred", a)
			if reads != maxCredentialEntries {
				t.Fatalf("%d reads for %d credentials", reads, maxCredentialEntries)
			}
			w.mustOpen(principalOf(a, cuid(0)), "openai", "plain-secret-000000")
			w.mustOpen(principalOf(a, cuid(maxCredentialEntries-1)), "openai", fmt.Sprintf("plain-secret-%06d", maxCredentialEntries-1))
			if n := w.src.count("cred", a); n != reads {
				t.Fatalf("a full cache did not serve its entries: %d new reads", n-reads)
			}
			w.mustOpen(principalOf(a, cuid(maxCredentialEntries)), "openai", fmt.Sprintf("plain-secret-%06d", maxCredentialEntries)) // one more: the map is emptied
			w.mustOpen(principalOf(a, cuid(maxCredentialEntries)), "openai", fmt.Sprintf("plain-secret-%06d", maxCredentialEntries))
			if n := w.src.count("cred", a); n != reads+1 {
				t.Errorf("the new entry was not cached after the flush: %d reads", n-reads)
			}
			w.mustOpen(principalOf(a, cuid(0)), "openai", "plain-secret-000000")
			if n := w.src.count("cred", a); n != reads+2 {
				t.Errorf("an old entry survived the emptying of a full map: %d reads", n-reads)
			}
		})
		t.Run("data keys", func(t *testing.T) {
			w := newUWorld(t)
			for i := 1; i <= maxDataKeyEntries+1; i++ {
				w.addTenant(uid(i))
				w.addCredential(uid(i), cuid(i), "openai", "main", "active", fmt.Sprintf("plain-secret-%06d", i), true)
			}
			for i := 1; i <= maxDataKeyEntries; i++ {
				w.mustOpen(principalOf(uid(i), ""), "openai", fmt.Sprintf("plain-secret-%06d", i))
			}
			// Let the credential entries expire; the data keys have not.
			w.clk.advance(ttlCredential)
			w.mustOpen(principalOf(uid(1), ""), "openai", "plain-secret-000001")
			if n := w.src.count("dek", uid(1)); n != 1 {
				t.Fatalf("a data key of a full cache was read again: %d reads", n)
			}
			w.mustOpen(principalOf(uid(maxDataKeyEntries+1), ""), "openai", fmt.Sprintf("plain-secret-%06d", maxDataKeyEntries+1))
			w.mustOpen(principalOf(uid(2), ""), "openai", "plain-secret-000002")
			if n := w.src.count("dek", uid(2)); n != 2 {
				t.Errorf("a data key survived the emptying of a full map: %d reads", n)
			}
		})
	})

	t.Run("failures are not cached", func(t *testing.T) {
		boom := errors.New("connection lost")
		cases := []struct {
			name    string
			breakIt func(w *uworld)
			fix     func(w *uworld)
			wantErr func(error) bool
			pin     string
		}{
			{name: "the database is out of reach",
				breakIt: func(w *uworld) { w.src.fail["def"] = boom },
				fix:     func(w *uworld) { delete(w.src.fail, "def") },
				wantErr: func(err error) bool { return errors.Is(err, ErrUnavailable) }},
			{name: "the database is out of reach for a pinned credential", pin: cuid(1),
				breakIt: func(w *uworld) { w.src.fail["cred"] = boom },
				fix:     func(w *uworld) { delete(w.src.fail, "cred") },
				wantErr: func(err error) bool { return errors.Is(err, ErrUnavailable) }},
			{name: "there is no default yet",
				breakIt: func(w *uworld) {
					w.src.creds = map[string]queries.GetCredentialForUseRow{}
					w.src.defaults = map[string]queries.GetDefaultCredentialForUseRow{}
				},
				fix:     func(w *uworld) { w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true) },
				wantErr: func(err error) bool { return errors.Is(err, ErrNoCredential) }},
			{name: "the default is disabled",
				breakIt: func(w *uworld) { w.addCredential(a, cuid(1), "openai", "main", "disabled", "plain-default-0001", true) },
				fix:     func(w *uworld) { w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true) },
				wantErr: func(err error) bool { var nu *NotUsableError; return errors.As(err, &nu) }},
			{name: "the data key is missing",
				breakIt: func(w *uworld) {
					w.src.mu.Lock()
					w.saved = w.src.deks
					w.src.deks = map[string]queries.GetDataKeyRow{}
					w.src.mu.Unlock()
				},
				fix: func(w *uworld) {
					w.src.mu.Lock()
					w.src.deks = w.saved
					w.src.mu.Unlock()
				},
				wantErr: func(err error) bool { return errors.Is(err, ErrUndecryptable) || errors.Is(err, ErrUnavailable) }},
			{name: "the key-encryption key version is not served",
				breakIt: func(w *uworld) {
					w.src.mu.Lock()
					w.saved = w.src.deks
					bad := map[string]queries.GetDataKeyRow{}
					for k, r := range w.src.deks {
						r.KekVersion = "local-0000000000000000"
						bad[k] = r
					}
					w.src.deks = bad
					w.src.mu.Unlock()
				},
				fix: func(w *uworld) {
					w.src.mu.Lock()
					w.src.deks = w.saved
					w.src.mu.Unlock()
				},
				wantErr: func(err error) bool { return errors.Is(err, ErrUnavailable) }},
			{name: "the wrapped data key is damaged",
				breakIt: func(w *uworld) {
					w.src.mu.Lock()
					w.saved = w.src.deks
					bad := map[string]queries.GetDataKeyRow{}
					for k, r := range w.src.deks {
						r.WrappedKey = append([]byte(nil), r.WrappedKey...)
						r.WrappedKey[20] ^= 1
						bad[k] = r
					}
					w.src.deks = bad
					w.src.mu.Unlock()
				},
				fix: func(w *uworld) {
					w.src.mu.Lock()
					w.src.deks = w.saved
					w.src.mu.Unlock()
				},
				wantErr: func(err error) bool { return errors.Is(err, ErrUndecryptable) }},
			{name: "the sealed secret does not authenticate",
				breakIt: func(w *uworld) {
					w.src.mu.Lock()
					defer w.src.mu.Unlock()
					k := a.String() + "/" + cuid(1)
					r := w.src.creds[k]
					r.Secret = append([]byte(nil), r.Secret...)
					r.Secret[len(r.Secret)-1] ^= 1
					w.src.creds[k] = r
					d := w.src.defaults[a.String()+"/openai"]
					d.Secret = r.Secret
					w.src.defaults[a.String()+"/openai"] = d
				},
				fix:     func(w *uworld) { w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true) },
				wantErr: func(err error) bool { return errors.Is(err, ErrUndecryptable) }},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				w := newUWorld(t)
				w.addTenant(a)
				w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
				c.breakIt(w)
				pr := principalOf(a, c.pin)
				got, err := w.open(pr, "openai")
				if err == nil || !c.wantErr(err) || !got.Secret.IsZero() {
					t.Fatalf("Open under the failure: %+v, %v", got, err)
				}
				// A second call fails again: nothing was cached from the failure.
				if _, err := w.open(pr, "openai"); err == nil || !c.wantErr(err) {
					t.Errorf("the second call: %v", err)
				}
				// The cause goes away and the next call works, with no waiting for any lifetime.
				c.fix(w)
				w.mustOpen(pr, "openai", "plain-default-0001")
			})
		}
	})

	t.Run("the log lines", func(t *testing.T) {
		w := newUWorld(t)
		w.addTenant(a)
		w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
		pr := principalOf(a, "")
		w.mustOpen(pr, "openai", "plain-default-0001")
		w.mustOpen(pr, "openai", "plain-default-0001")
		lines := logLines(t, w.logs, "credential opened")
		if len(lines) != 2 {
			t.Fatalf("%d lines `credential opened`, want 2", len(lines))
		}
		for i, wantCache := range []string{"miss", "hit"} {
			l := lines[i]
			if l["level"] != "DEBUG" || l["request_id"] != "req-0001" || l["tenant_id"] != a.String() || l["principal_id"] != pr.ID ||
				l["key_id"] != pr.KeyID || l["credential_id"] != cuid(1) || l["provider"] != "openai" || l["pinned"] != false || l["cache"] != wantCache {
				t.Errorf("line %d: %v", i, l)
			}
		}
		w.mustOpen(principalOf(a, cuid(1)), "openai", "plain-default-0001")
		if l := logLines(t, w.logs, "credential opened")[2]; l["pinned"] != true || l["cache"] != "hit" {
			t.Errorf("the pinned call: %v", l)
		}
		if strings.Contains(w.logs.String(), "plain-default-0001") {
			t.Error("the secret is in the log")
		}

		// A failure is one error line with the cause class and the identifiers, never a byte of a key.
		w2 := newUWorld(t)
		w2.addTenant(a)
		w2.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
		k := a.String() + "/" + cuid(1)
		r := w2.src.creds[k]
		r.Secret[len(r.Secret)-1] ^= 1
		w2.src.creds[k] = r
		d := w2.src.defaults[a.String()+"/openai"]
		d.Secret = r.Secret
		w2.src.defaults[a.String()+"/openai"] = d
		if _, err := w2.open(principalOf(a, ""), "openai"); !errors.Is(err, ErrUndecryptable) {
			t.Fatalf("%v", err)
		}
		errs := logLines(t, w2.logs, "credential cannot be opened")
		if len(errs) != 1 {
			t.Fatalf("%d lines `credential cannot be opened`, want 1", len(errs))
		}
		e := errs[0]
		if e["level"] != "ERROR" || e["request_id"] != "req-0001" || e["tenant_id"] != a.String() || e["credential_id"] != cuid(1) ||
			e["cause"] != "authentication" || e["data_key_version"] != float64(1) || e["kek_version"] != w2.src.deks[a.String()+"/1"].KekVersion {
			t.Errorf("error line: %v", e)
		}
		for _, bad := range [][]byte{w2.deks[a], r.Secret, w2.src.deks[a.String()+"/1"].WrappedKey} {
			for _, f := range byteFormsInternal(bad) {
				if strings.Contains(w2.logs.String(), f) {
					t.Error("key material or sealed bytes are in the log")
				}
			}
		}
		// The other causes.
		for _, c := range []struct {
			name  string
			setup func(w *uworld)
			cause string
		}{
			{"no data key", func(w *uworld) { w.src.deks = map[string]queries.GetDataKeyRow{} }, "data_key_missing"},
			{"a data key that does not unwrap", func(w *uworld) {
				r := w.src.deks[a.String()+"/1"]
				r.WrappedKey = append([]byte(nil), r.WrappedKey...)
				r.WrappedKey[30] ^= 1
				w.src.deks[a.String()+"/1"] = r
			}, "data_key_unwrap"},
			{"a version of the key-encryption key that is not served", func(w *uworld) {
				r := w.src.deks[a.String()+"/1"]
				r.KekVersion = "local-0000000000000000"
				w.src.deks[a.String()+"/1"] = r
			}, "kek_unavailable"},
		} {
			w3 := newUWorld(t)
			w3.addTenant(a)
			w3.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
			c.setup(w3)
			if _, err := w3.open(principalOf(a, ""), "openai"); err == nil {
				t.Fatalf("%s: no error", c.name)
			}
			ls := logLines(t, w3.logs, "credential cannot be opened")
			if len(ls) != 1 || ls[0]["cause"] != c.cause || ls[0]["credential_id"] != cuid(1) || ls[0]["tenant_id"] != a.String() || ls[0]["request_id"] != "req-0001" {
				t.Errorf("%s: %v, want cause %s", c.name, ls, c.cause)
			}
		}
	})

	t.Run("the options", func(t *testing.T) {
		key, _ := GenerateLocalKey()
		prov, _ := NewLocalProvider(key)
		logger := telemetry.NewLogger(&bytes.Buffer{}, "debug")
		good := OpenerOptions{Provider: prov, Logger: logger, CredentialTTL: 30 * time.Second, DataKeyTTL: 5 * time.Minute}
		if _, err := newOpener(good, newFakeSource()); err != nil {
			t.Fatalf("the good options: %v", err)
		}
		for _, ttl := range []time.Duration{time.Second, 30 * time.Second, 60 * time.Second} {
			o := good
			o.CredentialTTL = ttl
			if _, err := newOpener(o, newFakeSource()); err != nil {
				t.Errorf("credential lifetime %s refused: %v", ttl, err)
			}
		}
		for _, ttl := range []time.Duration{0, -time.Second, time.Second - time.Nanosecond, 60*time.Second + time.Nanosecond, 2 * time.Minute} {
			o := good
			o.CredentialTTL = ttl
			if _, err := newOpener(o, newFakeSource()); err == nil {
				t.Errorf("credential lifetime %s was accepted", ttl)
			}
		}
		for _, ttl := range []time.Duration{time.Second, 5 * time.Minute, 15 * time.Minute} {
			o := good
			o.DataKeyTTL = ttl
			if _, err := newOpener(o, newFakeSource()); err != nil {
				t.Errorf("data key lifetime %s refused: %v", ttl, err)
			}
		}
		for _, ttl := range []time.Duration{0, -time.Second, time.Second - time.Nanosecond, 15*time.Minute + time.Nanosecond, time.Hour} {
			o := good
			o.DataKeyTTL = ttl
			if _, err := newOpener(o, newFakeSource()); err == nil {
				t.Errorf("data key lifetime %s was accepted", ttl)
			}
		}
		o := good
		o.Provider = nil
		if _, err := newOpener(o, newFakeSource()); err == nil {
			t.Error("a nil provider was accepted")
		}
		o = good
		o.Logger = nil
		if _, err := newOpener(o, newFakeSource()); err == nil {
			t.Error("a nil logger was accepted")
		}
		// NewOpener takes the database: a nil one is refused before anything else.
		if _, err := NewOpener(good); err == nil {
			t.Error("a nil database was accepted")
		}
		// Without a clock the opener uses the real one.
		w := newUWorld(t)
		w.addTenant(a)
		w.addCredential(a, cuid(1), "openai", "main", "active", "plain-default-0001", true)
		real, err := newOpener(OpenerOptions{Provider: w.prov, Logger: logger, CredentialTTL: time.Second, DataKeyTTL: time.Minute}, w.src)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := real.Open(context.Background(), Use{Principal: principalOf(a, ""), RequestID: "r", Provider: "openai"}); err != nil || got.Secret.Reveal() != "plain-default-0001" {
			t.Errorf("an opener without an injected clock: %v", err)
		}
	})

	t.Run("concurrent use is safe", func(t *testing.T) {
		w := newUWorld(t)
		for i := 1; i <= 4; i++ {
			w.addTenant(uid(i))
			w.addCredential(uid(i), cuid(i), "openai", "main", "active", fmt.Sprintf("plain-secret-%06d", i), true)
		}
		var wg sync.WaitGroup
		stop, stopped := make(chan struct{}), make(chan struct{})
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 300; i++ {
					n := i%4 + 1
					got, err := w.o.Open(context.Background(), Use{Principal: principalOf(uid(n), ""), RequestID: "r", Provider: "openai"})
					if err != nil || got.Secret.Reveal() != fmt.Sprintf("plain-secret-%06d", n) {
						t.Errorf("concurrent Open of tenant %d: %v", n, err)
						return
					}
				}
			}()
		}
		go func() {
			defer close(stopped)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				switch i % 3 {
				case 0:
					w.o.Invalidate("vault:" + uid(i%4+1).String())
				case 1:
					w.o.Flush()
				default:
					w.clk.advance(time.Second)
				}
			}
		}()
		wg.Wait()
		close(stop)
		<-stopped
	})
}

// logLines decodes the lines of the JSON log with the message.
func logLines(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a log line is not JSON: %q", line)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func byteFormsInternal(b []byte) []string {
	return []string{fmt.Sprintf("%x", b), fmt.Sprintf("%v", b), string(b)}
}
