package vault_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/internal/vault/transittest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// transitService is a double with a key and the files to reach it; providers on it are made with
// provider, so that a restart of the process is a new provider on the same service.
type transitService struct {
	s *transittest.Server
	f vaulttest.TransitFiles
}

func newTransitService(t *testing.T) *transitService {
	t.Helper()
	s := transittest.New(t)
	return &transitService{s: s, f: vaulttest.Transit(t, s, vaulttest.TransitSetup{})}
}

// provider returns a started provider on the service; it reads the clock c when it is not nil.
func (ts *transitService) provider(t *testing.T, c *tClock, adjust ...func(*vault.TransitOptions)) *vault.TransitProvider {
	t.Helper()
	if c != nil {
		adjust = append(adjust, withClock(c))
	}
	return vaulttest.TransitProvider(t, ts.s, ts.f, adjust...)
}

// router returns a router that wraps with active and unwraps with the others too.
func router(t *testing.T, active vault.KeyProvider, others ...vault.KeyProvider) *vault.Router {
	t.Helper()
	r, err := vault.NewRouter(active, others...)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

// otherAADDigest is what the provider sends as associated_data for the other data of the negative
// probe of Check.
func otherAADDigest() string {
	sum := sha256.Sum256(append(append([]byte("pandokos/transit-aad/v1"), 0), []byte("pandokos/check/v1/other")...))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// failNegativeProbe is a transport wrapper that answers 503 to the decrypt request of the
// negative probe of Check and passes everything else on.
func failNegativeProbe(rt http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/decrypt/") && req.GetBody != nil {
			rc, err := req.GetBody()
			if err == nil {
				b, _ := io.ReadAll(rc)
				_ = rc.Close()
				if bytes.Contains(b, []byte(otherAADDigest())) {
					return answer(http.StatusServiceUnavailable, `{"errors":["sealed"]}`)(req), nil
				}
			}
		}
		return rt.RoundTrip(req)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestCheckAssociatedDataBound is T-1071.
func TestCheckAssociatedDataBound(t *testing.T) {
	t.Run("a service that ignores associated data is refused before any write", func(t *testing.T) {
		w := newActivationWorld(t)
		ts := newTransitService(t)
		p := ts.provider(t, nil)
		ts.s.IgnoreAssociatedData()
		if _, err := vault.Check(ctx(t), w.admin, p); !errors.Is(err, vault.ErrAADNotBound) {
			t.Errorf("Check: %v, want ErrAADNotBound", err)
		}
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, p); !errors.Is(err, vault.ErrAADNotBound) {
			t.Errorf("Activate: %v, want ErrAADNotBound", err)
		}
		if got := w.states(t); got != "" {
			t.Errorf("a version was written before the refusal: %s", got)
		}
		if n := w.auditRows(t); n != 0 {
			t.Errorf("%d audit rows were written", n)
		}
	})

	t.Run("it passes for the local provider and for the double", func(t *testing.T) {
		w := newActivationWorld(t)
		k := vaulttest.Keys(t, 1)
		if _, err := vault.Check(ctx(t), w.admin, vaulttest.Provider(t, k[0])); err != nil {
			t.Errorf("Check with the local provider: %v", err)
		}
		ts := newTransitService(t)
		if v, err := vault.Check(ctx(t), w.admin, ts.provider(t, nil)); err != nil || !strings.HasPrefix(v, "transit-") {
			t.Errorf("Check with the double: %q, %v", v, err)
		}
		if v, _, err := vault.Activate(ctx(t), w.admin, w.worker, ts.provider(t, nil)); err != nil || !strings.HasPrefix(v, "transit-") {
			t.Errorf("Activate with the double: %q, %v", v, err)
		}
	})

	t.Run("a provider unavailable during the negative probe is not a pass", func(t *testing.T) {
		w := newActivationWorld(t)
		ts := newTransitService(t)
		p := ts.provider(t, nil, withTransport(failNegativeProbe))
		_, err := vault.Check(ctx(t), w.admin, p)
		if !errors.Is(err, vault.ErrProviderUnavailable) || errors.Is(err, vault.ErrAADNotBound) {
			t.Errorf("Check: %v, want ErrProviderUnavailable and not ErrAADNotBound", err)
		}
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, p); !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Errorf("Activate: %v, want ErrProviderUnavailable", err)
		}
		if got := w.states(t); got != "" {
			t.Errorf("a version was written: %s", got)
		}
	})
}

// TestCheckSeveralProviders is T-1072.
func TestCheckSeveralProviders(t *testing.T) {
	// setup makes a database in which versions of both providers are registered and a data key
	// is wrapped under each: local (k0) first and the service after, or the other way round.
	type setup struct {
		w      *activationWorld
		ts     *transitService
		k      []vaulttest.Key
		local  *vault.LocalProvider
		transp *vault.TransitProvider
		lv, tv string // the versions
	}
	build := func(t *testing.T, transitFirst bool) *setup {
		t.Helper()
		s := &setup{w: newActivationWorld(t), ts: newTransitService(t), k: vaulttest.Keys(t, 2)}
		s.local = vaulttest.Provider(t, s.k[0])
		s.transp = s.ts.provider(t, nil)
		s.lv, s.tv = s.k[0].Version, s.transp.Info().Version
		first, second := vault.KeyProvider(s.local), vault.KeyProvider(s.transp)
		if transitFirst {
			first, second = second, first
		}
		if _, _, err := vault.Activate(ctx(t), s.w.admin, s.w.worker, first); err != nil {
			t.Fatal(err)
		}
		s.w.dataKeyFor(t, first, "first")
		both := router(t, second, first)
		if _, _, err := vault.Activate(ctx(t), s.w.admin, s.w.worker, both); err != nil {
			t.Fatal(err)
		}
		s.w.dataKeyFor(t, both, "second")
		return s
	}

	for _, transitFirst := range []bool{false, true} {
		name := "the key file then the service"
		if transitFirst {
			name = "the service then the key file"
		}
		t.Run(name, func(t *testing.T) {
			s := build(t, transitFirst)
			active, retiring := s.tv, s.lv
			var both *vault.Router
			var onlyActive, onlyOther vault.KeyProvider
			if transitFirst {
				active, retiring = s.lv, s.tv
				both = router(t, s.local, s.transp)
				onlyActive, onlyOther = s.local, s.transp
			} else {
				both = router(t, s.transp, s.local)
				onlyActive, onlyOther = s.transp, s.local
			}
			if got := s.w.states(t); !strings.Contains(got, active+"=active") || !strings.Contains(got, retiring+"=retiring") {
				t.Fatalf("the states are %s", got)
			}
			if v, err := vault.Check(ctx(t), s.w.admin, both); err != nil || v != active {
				t.Errorf("the router with both: %q, %v", v, err)
			}
			// Without the provider of the retiring version: the error names it and no key.
			_, err := vault.Check(ctx(t), s.w.admin, onlyActive)
			if err == nil || !strings.Contains(err.Error(), retiring) {
				t.Errorf("without the provider of %s: %v", retiring, err)
			}
			// Without the provider of the active version: the version of the provider that is
			// configured was replaced, or the active one is not provided; both are named.
			_, err = vault.Check(ctx(t), s.w.admin, onlyOther)
			if err == nil || !strings.Contains(err.Error(), retiring) {
				t.Errorf("without the provider of %s: %v", active, err)
			}
			for _, e := range []error{err} {
				if e != nil {
					for _, k := range byteForms([]byte(s.k[0].Secret.Reveal())) {
						if len(k) > 8 && strings.Contains(e.Error(), k) {
							t.Errorf("the error holds the key")
						}
					}
					if strings.Contains(e.Error(), s.ts.f.Token) {
						t.Errorf("the error holds the token")
					}
				}
			}
		})
	}

	t.Run("a row whose provider is not the owner of its identifier fails", func(t *testing.T) {
		s := build(t, false)
		if _, err := s.w.d.Super(t).Exec(ctx(t), `UPDATE pandokos.kek_version SET provider = 'local' WHERE id = $1`, s.tv); err != nil {
			t.Fatal(err)
		}
		_, err := vault.Check(ctx(t), s.w.admin, router(t, s.transp, s.local))
		if err == nil || !strings.Contains(err.Error(), s.tv) {
			t.Errorf("a version registered by another provider than the one its identifier names: %v", err)
		}
	})

	t.Run("a retired version of an unconfigured provider does not block", func(t *testing.T) {
		w := newActivationWorld(t)
		ts := newTransitService(t)
		k := vaulttest.Keys(t, 1)
		tp := ts.provider(t, nil)
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, tp); err != nil { // no data key: it will retire
			t.Fatal(err)
		}
		lp := vaulttest.Provider(t, k[0])
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, router(t, lp, tp)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, lp); err != nil { // the unused version retires
			t.Fatalf("a retired version of an unconfigured provider blocks: %v", err)
		}
		if got := w.states(t); !strings.Contains(got, tp.Info().Version+"=retired") {
			t.Fatalf("the service's version is not retired: %s", got)
		}
		if v, err := vault.Check(ctx(t), w.admin, lp); err != nil || v != k[0].Version {
			t.Errorf("Check without the service: %q, %v", v, err)
		}
	})
}

// activationProvider returns the provider recorded in the audit row of the activation of a version.
func activationProvider(t *testing.T, d *dbtest.Database, version string) string {
	t.Helper()
	var provider string
	err := d.Super(t).QueryRow(ctx(t), `SELECT COALESCE(detail->>'provider', '') FROM pandokos.platform_audit_event WHERE action = 'kek.activate' AND resource_id = $1 ORDER BY id DESC LIMIT 1`, version).Scan(&provider)
	if err != nil {
		t.Fatalf("the audit row of the activation of %s: %v", version, err)
	}
	return provider
}

// TestActivateTransit is T-1073.
func TestActivateTransit(t *testing.T) {
	t.Run("an empty database, then the same again", func(t *testing.T) {
		w := newActivationWorld(t)
		ts := newTransitService(t)
		p := ts.provider(t, nil)
		v, res, err := vault.Activate(ctx(t), w.admin, w.worker, p)
		if err != nil || res != "activated" || v != p.Info().Version {
			t.Fatalf("Activate = %q %q %v", v, res, err)
		}
		if got := w.states(t); got != v+"=active" {
			t.Errorf("the states: %s", got)
		}
		if got := w.d.Count(t, `SELECT count(*)::int FROM pandokos.kek_version WHERE id = $1 AND provider = 'transit'`, v); got != 1 {
			t.Errorf("the version is not registered by the provider transit")
		}
		if got := activationProvider(t, w.d, v); got != "transit" {
			t.Errorf("the audit detail says provider %q, want transit", got)
		}
		rows := w.auditRows(t)
		if v2, res, err := vault.Activate(ctx(t), w.admin, w.worker, ts.provider(t, nil)); err != nil || v2 != v || res != "unchanged" {
			t.Errorf("again: %q %q %v, want unchanged", v2, res, err)
		}
		if w.auditRows(t) != rows {
			t.Errorf("an unchanged activation wrote an audit row")
		}
	})

	t.Run("after a rotation in the service the new version is active and the previous one retiring", func(t *testing.T) {
		w := newActivationWorld(t)
		ts := newTransitService(t)
		p1 := ts.provider(t, nil)
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, p1); err != nil {
			t.Fatal(err)
		}
		w.dataKeyFor(t, p1, "tenant-one")
		ts.s.Namespace("").Rotate("transit", "pandokos")
		p2 := ts.provider(t, nil)
		v2, res, err := vault.Activate(ctx(t), w.admin, w.worker, p2)
		if err != nil || res != "activated" || v2 == p1.Info().Version || !strings.HasSuffix(v2, "-v2") {
			t.Fatalf("Activate after the rotation: %q %q %v", v2, res, err)
		}
		if got := w.states(t); !strings.Contains(got, p1.Info().Version+"=retiring") || !strings.Contains(got, v2+"=active") {
			t.Errorf("the states after the rotation: %s", got)
		}
	})

	t.Run("a local active version and a transit provider: the local one becomes retiring", func(t *testing.T) {
		w := newActivationWorld(t)
		ts := newTransitService(t)
		k := vaulttest.Keys(t, 1)
		lp := vaulttest.Provider(t, k[0])
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, lp); err != nil {
			t.Fatal(err)
		}
		w.dataKeyFor(t, lp, "tenant-one")
		tp := ts.provider(t, nil)
		if _, res, err := vault.Activate(ctx(t), w.admin, w.worker, router(t, tp, lp)); err != nil || res != "activated" {
			t.Fatalf("Activate: %q %v", res, err)
		}
		if got := w.states(t); !strings.Contains(got, k[0].Version+"=retiring") || !strings.Contains(got, tp.Info().Version+"=active") {
			t.Errorf("the states: %s", got)
		}
	})

	t.Run("a replaced version is ErrKeyReplaced, naming both versions and the right provider's settings", func(t *testing.T) {
		// The key file was replaced by the service.
		w := newActivationWorld(t)
		ts := newTransitService(t)
		k := vaulttest.Keys(t, 2)
		lp := vaulttest.Provider(t, k[0])
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, lp); err != nil {
			t.Fatal(err)
		}
		w.dataKeyFor(t, lp, "tenant-one")
		tp := ts.provider(t, nil)
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, router(t, tp, lp)); err != nil {
			t.Fatal(err)
		}
		_, _, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[0], k[1]))
		if !errors.Is(err, vault.ErrKeyReplaced) {
			t.Fatalf("the old key file again: %v, want ErrKeyReplaced", err)
		}
		for _, v := range []string{k[0].Version, tp.Info().Version} {
			if !strings.Contains(err.Error(), v) {
				t.Errorf("the error does not name %s: %v", v, err)
			}
		}
		if strings.Contains(err.Error(), "vault.kek.local.key_file") {
			t.Errorf("the error tells to set vault.kek.local.key_file while the active version is the service's: %v", err)
		}

		// The service was replaced by a new key file.
		w2 := newActivationWorld(t)
		ts2 := newTransitService(t)
		tp2 := ts2.provider(t, nil)
		if _, _, err := vault.Activate(ctx(t), w2.admin, w2.worker, tp2); err != nil {
			t.Fatal(err)
		}
		w2.dataKeyFor(t, tp2, "tenant-one")
		lp2 := vaulttest.Provider(t, k[1])
		if _, _, err := vault.Activate(ctx(t), w2.admin, w2.worker, router(t, lp2, tp2)); err != nil {
			t.Fatal(err)
		}
		_, _, err = vault.Activate(ctx(t), w2.admin, w2.worker, tp2)
		if !errors.Is(err, vault.ErrKeyReplaced) {
			t.Fatalf("the service after the key file: %v, want ErrKeyReplaced", err)
		}
		for _, v := range []string{tp2.Info().Version, k[1].Version} {
			if !strings.Contains(err.Error(), v) {
				t.Errorf("the error does not name %s: %v", v, err)
			}
		}
		if strings.Contains(err.Error(), "vault.kek.transit.") {
			t.Errorf("the error names a setting of the service while the active version is local: %v", err)
		}
	})
}

// providerCuts is a key provider that stops the service after n wraps: an outage in the middle
// of a pass of the re-wrap job.
type providerCuts struct {
	vault.KeyProvider
	mu    sync.Mutex
	left  int
	onCut func()
}

func (p *providerCuts) Wrap(ctx context.Context, plain, aad []byte) (string, []byte, error) {
	p.mu.Lock()
	cut, f := p.left == 0 && p.onCut != nil, p.onCut
	if cut {
		p.onCut = nil
	} else if p.left > 0 {
		p.left--
	}
	p.mu.Unlock()
	if cut {
		f()
	}
	return p.KeyProvider.Wrap(ctx, plain, aad)
}

// openAll opens the credential of every tenant of the world through p and compares it.
func (w *rotWorld) openAll(t *testing.T, p vault.KeyProvider, when string) {
	t.Helper()
	logs := &testutil.LogBuffer{}
	o := w.opener(t, logs, p, time.Second, time.Second, nil)
	for _, tid := range w.tenants {
		got, err := open(t, o, principalFor(tid, ""), "openai")
		if err != nil || got.Secret.Reveal() != w.plain[tid] {
			t.Fatalf("%s: the credential of %s does not open: %v", when, tid, err)
		}
	}
}

// wrappedFormats counts the data keys by the format byte of their wrapped key.
func (w *rotWorld) wrappedFormats(t *testing.T) map[int]int {
	t.Helper()
	rows, err := w.d.Super(t).Query(ctx(t), `SELECT get_byte(wrapped_key, 0), kek_version FROM pandokos.tenant_data_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var b int
		var v string
		if err := rows.Scan(&b, &v); err != nil {
			t.Fatal(err)
		}
		want := 0x01
		if strings.HasPrefix(v, "transit-") {
			want = 0x02
		}
		if b != want {
			t.Errorf("a data key under %s has the format byte %#x, want %#x", v, b, want)
		}
		out[b]++
	}
	return out
}

// TestRewrapAcrossProviders is T-1074: moves between a key file and the service and between
// versions of the service, with every credential opening before, during and after.
func TestRewrapAcrossProviders(t *testing.T) {
	const n = 6
	w := newRotWorld(t, n)
	ts := newTransitService(t)
	clk := &tClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	local0 := vaulttest.Provider(t, w.k[0])
	credDigest := w.digest(t, `SELECT md5(string_agg(to_jsonb(c)::text, ',' ORDER BY c.id)) FROM pandokos.provider_credential c`)
	w.openAll(t, local0, "before")

	pass := func(t *testing.T, p vault.KeyProvider, logs *testutil.LogBuffer) error {
		t.Helper()
		rw := &vault.Rewrapper{DB: w.worker, Provider: p, Logger: newLogger(logs)}
		return rw.Pass(ctx(t))
	}
	state := func(id string) string {
		return w.digest(t, `SELECT state FROM pandokos.kek_version WHERE id = $1`, id)
	}

	// 1. Key file to service.
	tp1 := ts.provider(t, clk)
	v1 := tp1.Info().Version
	r1 := router(t, tp1, local0)
	if _, res, err := vault.Activate(ctx(t), w.admin, w.worker, r1); err != nil || res != "activated" {
		t.Fatalf("Activate the service: %q %v", res, err)
	}
	w.openAll(t, r1, "during the move to the service")
	logs := &testutil.LogBuffer{}
	if err := pass(t, r1, logs); err != nil {
		t.Fatalf("the pass to the service: %v", err)
	}
	for _, d := range w.dataKeys(t) {
		if d[2] != v1 {
			t.Errorf("a data key is under %s, want %s", d[2], v1)
		}
	}
	if f := w.wrappedFormats(t); f[0x02] != n || f[0x01] != 0 {
		t.Errorf("formats after the move to the service: %v", f)
	}
	if state(w.k[0].Version) != "retired" {
		t.Errorf("the key file's version is %s, want retired", state(w.k[0].Version))
	}
	if l := logs.Find(t, "key rotation complete"); len(l) != 1 || l[0]["kek_version"] != w.k[0].Version {
		t.Errorf("key rotation complete: %v, want one line naming %s", l, w.k[0].Version)
	}
	w.openAll(t, tp1, "after the move to the service")

	// 2. Version 1 to version 2 of the service, with the service away in the middle of the pass.
	ts.s.Namespace("").Rotate("transit", "pandokos")
	tp2 := ts.provider(t, clk)
	v2 := tp2.Info().Version
	r2 := router(t, tp2) // the router of one provider: version 1 is the same key of the service
	if v2 == v1 {
		t.Fatalf("the rotation did not change the identifier")
	}
	if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, r2); err != nil {
		t.Fatalf("Activate version 2: %v", err)
	}
	w.openAll(t, r2, "during the rotation")
	cut := &providerCuts{KeyProvider: r2, left: 2, onCut: ts.s.Stop}
	logs = &testutil.LogBuffer{}
	err := pass(t, cut, logs)
	if err == nil {
		t.Fatal("a pass with the service away succeeded")
	}
	if l := logs.Find(t, "key rewrap failing"); len(l) == 0 || l[0]["cause"] != "kek_provider_unavailable" {
		t.Errorf("key rewrap failing: %v, want cause kek_provider_unavailable", l)
	}
	if state(v1) == "retired" {
		t.Errorf("version 1 was retired by a pass that failed")
	}
	// No row is half written: each data key is under one version, with the format of its provider
	// (both are of the service), and it is one of the two.
	under1, under2 := 0, 0
	for _, d := range w.dataKeys(t) {
		switch d[2] {
		case v1:
			under1++
		case v2:
			under2++
		default:
			t.Errorf("a data key is under %s", d[2])
		}
	}
	if under1+under2 != n || under1 == 0 {
		t.Errorf("after the cut: %d under version 1, %d under version 2", under1, under2)
	}
	w.wrappedFormats(t)
	ts.s.Start()
	clk.advance(6 * time.Second) // the refusal of the provider ends
	logs = &testutil.LogBuffer{}
	if err := pass(t, r2, logs); err != nil {
		t.Fatalf("the next pass: %v", err)
	}
	if state(v1) != "retired" || state(v2) != "active" {
		t.Errorf("versions: %s %s, want retired and active", state(v1), state(v2))
	}
	if l := logs.Find(t, "key rotation complete"); len(l) != 1 || l[0]["kek_version"] != v1 {
		t.Errorf("key rotation complete: %v, want one line naming %s", l, v1)
	}
	w.openAll(t, tp2, "after the rotation")

	// 3. Service to a new key file.
	local1 := vaulttest.Provider(t, w.k[1])
	r3 := router(t, local1, tp2)
	if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, r3); err != nil {
		t.Fatalf("Activate the new key file: %v", err)
	}
	w.openAll(t, r3, "during the move back to a key file")
	logs = &testutil.LogBuffer{}
	if err := pass(t, r3, logs); err != nil {
		t.Fatalf("the pass back to a key file: %v", err)
	}
	if f := w.wrappedFormats(t); f[0x01] != n || f[0x02] != 0 {
		t.Errorf("formats after the move back: %v", f)
	}
	if state(v2) != "retired" {
		t.Errorf("the service's version is %s, want retired", state(v2))
	}
	if l := logs.Find(t, "key rotation complete"); len(l) != 1 || l[0]["kek_version"] != v2 {
		t.Errorf("key rotation complete: %v, want one line naming %s", l, v2)
	}
	w.openAll(t, local1, "after the move back")

	if got := w.digest(t, `SELECT md5(string_agg(to_jsonb(c)::text, ',' ORDER BY c.id)) FROM pandokos.provider_credential c`); got != credDigest {
		t.Errorf("a sealed credential changed during the moves")
	}
}

// TestOpenerWithTransit is T-1075: the gateway's caches in front of the service.
func TestOpenerWithTransit(t *testing.T) {
	d := dbtest.NewMigrated(t)
	if _, err := d.Super(t).Exec(ctx(t), `DELETE FROM pandokos.kek_version`); err != nil {
		t.Fatal(err)
	}
	ts := newTransitService(t)
	clk := &tClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	p := ts.provider(t, clk, withTimeout(500*time.Millisecond))
	admin, worker := d.Open(t, store.RoleAdmin), d.Open(t, store.RoleWorker)
	if _, _, err := vault.Activate(ctx(t), admin, worker, p); err != nil {
		t.Fatal(err)
	}
	sealer, err := vault.NewSealer(p)
	if err != nil {
		t.Fatal(err)
	}
	w := &vworld{d: d, admin: admin, gw: d.Open(t, store.RoleGateway), prov: p, sealer: sealer}
	mk := func(name, plain string) (tenant.ID, string) {
		tid := d.SeedTenant(t, name)
		w.createCredential(t, sealer, tid, "openai", "main", plain)
		return tid, plain
	}
	tenantA, plainA := mk("transit-a", "synthetic-secret-a")
	tenantB, plainB := mk("transit-b", "synthetic-secret-b")
	tenantC, plainC := mk("transit-c", "synthetic-secret-c")

	logs := &testutil.LogBuffer{}
	o := w.opener(t, logs, p, 30*time.Second, time.Minute, clk.now)
	openOf := func(tid tenant.ID) (string, error) {
		got, err := open(t, o, principalFor(tid, ""), "openai")
		return got.Secret.Reveal(), err
	}

	t.Run("one unwrap per tenant and lifetime", func(t *testing.T) {
		ts.s.Reset()
		for i := 0; i < 3; i++ {
			if got, err := openOf(tenantA); err != nil || got != plainA {
				t.Fatalf("open: %q %v", got, err)
			}
		}
		if n := ts.s.Count(transittest.OpUnwrap); n != 1 {
			t.Errorf("%d unwraps for three opens of one tenant", n)
		}
		clk.advance(31 * time.Second) // the credential expires, the data key stays
		if got, err := openOf(tenantA); err != nil || got != plainA {
			t.Fatalf("open: %q %v", got, err)
		}
		if n := ts.s.Count(transittest.OpUnwrap); n != 1 {
			t.Errorf("%d unwraps after the credential's lifetime, want 1: the data key is cached", n)
		}
		clk.advance(61 * time.Second)
		if got, err := openOf(tenantA); err != nil || got != plainA {
			t.Fatalf("open: %q %v", got, err)
		}
		if n := ts.s.Count(transittest.OpUnwrap); n != 2 {
			t.Errorf("%d unwraps after the data key's lifetime, want 2", n)
		}
	})

	t.Run("50 concurrent first calls of a tenant make one unwrap", func(t *testing.T) {
		ts.s.Reset()
		ts.s.Delay(150 * time.Millisecond)
		defer ts.s.Clear()
		var wg sync.WaitGroup
		errs := make([]error, 50)
		for i := range errs {
			wg.Add(1)
			go func() { defer wg.Done(); _, errs[i] = openOf(tenantB) }()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("call %d: %v", i, err)
			}
		}
		if n := ts.s.Count(transittest.OpUnwrap); n != 1 {
			t.Errorf("%d unwraps for 50 concurrent first calls, want 1", n)
		}
	})

	t.Run("with the service away", func(t *testing.T) {
		if _, err := openOf(tenantA); err != nil {
			t.Fatal(err) // cached now
		}
		ts.s.Stop()
		clk.advance(29 * time.Second)
		if got, err := openOf(tenantA); err != nil || got != plainA {
			t.Errorf("a cached tenant with the service away: %q %v", got, err)
		}
		clk.advance(31 * time.Second) // the credential was read again; the data key is still cached
		if got, err := openOf(tenantA); err != nil || got != plainA {
			t.Errorf("a cached data key with the service away: %q %v", got, err)
		}
		clk.advance(31 * time.Second) // the data key's lifetime is over
		logs.Reset()
		if _, err := openOf(tenantA); !errors.Is(err, vault.ErrUnavailable) {
			t.Errorf("after the lifetime: %v, want ErrUnavailable", err)
		}
		if l := logs.Find(t, "credential cannot be opened"); len(l) != 1 || l[0]["cause"] != "kek_provider_unavailable" {
			t.Errorf("the line credential cannot be opened: %v, want cause kek_provider_unavailable", l)
		}
		// A tenant that is not cached.
		logs.Reset()
		if _, err := openOf(tenantC); !errors.Is(err, vault.ErrUnavailable) {
			t.Errorf("a tenant not cached: %v, want ErrUnavailable", err)
		}
		if l := logs.Find(t, "credential cannot be opened"); len(l) != 1 || l[0]["cause"] != "kek_provider_unavailable" {
			t.Errorf("the line for a tenant not cached: %v", l)
		}
		// The service is back.
		ts.s.Start()
		clk.advance(6 * time.Second) // the refusal ends
		if got, err := openOf(tenantC); err != nil || got != plainC {
			t.Errorf("when the service is back: %q %v", got, err)
		}
	})

	t.Run("a data key erased in the database is not opened from any cache after the lifetimes", func(t *testing.T) {
		if got, err := openOf(tenantB); err != nil || got != plainB {
			t.Fatalf("open: %q %v", got, err)
		}
		if _, err := d.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, tenantB.String()); err != nil {
			t.Fatal(err)
		}
		clk.advance(61 * time.Second) // both lifetimes
		if _, err := openOf(tenantB); !errors.Is(err, vault.ErrUndecryptable) {
			t.Errorf("an erased data key: %v, want ErrUndecryptable", err)
		}
	})
}
