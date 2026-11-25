package vault_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// faultProvider is a provider with faults for the tests of the re-wrap: it fails to unwrap the data
// key of one tenant, calls a function when the new value is checked, and counts what it wraps and
// unwraps.
type faultProvider struct {
	vault.KeyProvider
	failFor  atomic.Pointer[tenant.ID]
	onVerify func() // called when an unwrap is asked for the version in force at wrapping (the check after Wrap)
	active   string
	wraps    atomic.Int64
	mu       sync.Mutex
	unwraps  map[string]int
}

func newFaultProvider(p vault.KeyProvider, active string) *faultProvider {
	return &faultProvider{KeyProvider: p, active: active, unwraps: map[string]int{}}
}

func (f *faultProvider) Wrap(ctx context.Context, plain, aad []byte) (string, []byte, error) {
	f.wraps.Add(1)
	return f.KeyProvider.Wrap(ctx, plain, aad)
}

func (f *faultProvider) Unwrap(ctx context.Context, version string, wrapped, aad []byte) ([]byte, error) {
	f.mu.Lock()
	f.unwraps[string(aad)]++
	f.mu.Unlock()
	if id := f.failFor.Load(); id != nil && bytes.Equal(aad, vault.DataAAD(*id, 1)) {
		return nil, vault.ErrUnwrap
	}
	if version == f.active && f.onVerify != nil {
		f.onVerify()
	}
	return f.KeyProvider.Unwrap(ctx, version, wrapped, aad)
}

func (f *faultProvider) unwrapsFor(id tenant.ID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unwraps[string(vault.DataAAD(id, 1))]
}

// rotWorld is a database whose installation started with the key k[0] and holds n tenants, each
// with a credential sealed under it.
type rotWorld struct {
	*vworld
	worker  *store.DB
	k       []vaulttest.Key
	tenants []tenant.ID
	plain   map[tenant.ID]string
}

func newRotWorld(t *testing.T, n int) *rotWorld {
	t.Helper()
	d := dbtest.NewMigrated(t)
	k := vaulttest.Keys(t, 3)
	d.UseKEK(t, k[0].Version)
	p0 := vaulttest.Provider(t, k[0])
	s0, err := vault.NewSealer(p0)
	if err != nil {
		t.Fatal(err)
	}
	w := &rotWorld{
		vworld: &vworld{d: d, admin: d.Open(t, store.RoleAdmin), gw: d.Open(t, store.RoleGateway), prov: p0, sealer: s0},
		worker: d.Open(t, store.RoleWorker), k: k, plain: map[tenant.ID]string{},
	}
	for i := 0; i < n; i++ {
		tid := d.SeedTenant(t, fmt.Sprintf("rot-%03d", i))
		w.plain[tid] = fmt.Sprintf("synthetic-secret-%03d", i)
		w.createCredential(t, s0, tid, "openai", "main", w.plain[tid])
		w.tenants = append(w.tenants, tid)
	}
	return w
}

// dataKeys returns, by tenant, the identifier, the version and the key-encryption key version of its
// data key, as the superuser reads them.
func (w *rotWorld) dataKeys(t *testing.T) map[tenant.ID][3]string {
	t.Helper()
	rows, err := w.d.Super(t).Query(ctx(t), `SELECT tenant_id::text, id::text, version::text, kek_version FROM pandokos.tenant_data_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[tenant.ID][3]string{}
	for rows.Next() {
		var tid, id, ver, kek string
		if err := rows.Scan(&tid, &id, &ver, &kek); err != nil {
			t.Fatal(err)
		}
		parsed, err := tenant.ParseID(tid)
		if err != nil {
			t.Fatal(err)
		}
		out[parsed] = [3]string{id, ver, kek}
	}
	return out
}

func (w *rotWorld) digest(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := w.d.Super(t).QueryRow(ctx(t), sql, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// closeToDeletion moves a tenant to "deleting": it is suspended, a deletion is requested and the
// first step ran. Its data key and credentials are still there.
func (w *rotWorld) closeToDeletion(t *testing.T, tid tenant.ID) {
	t.Helper()
	w.d.RequestDeletion(t, tid, time.Now().Add(-time.Minute))
	err := w.worker.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
		next, err := queries.New(tx).TenantErasureStep(c, 100)
		if err == nil && next != "access" {
			err = fmt.Errorf("the first step answered %q", next)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRewrap is T-989, the exit criterion in process: a rotation re-wraps every data key of every
// tenant, active, suspended or being deleted, retires the old version, and no credential changes.
func TestRewrap(t *testing.T) {
	t.Parallel()
	w := newRotWorld(t, 40)
	for _, tid := range w.tenants[36:39] {
		if _, err := w.d.Super(t).Exec(ctx(t), `UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid`, tid.String()); err != nil {
			t.Fatal(err)
		}
	}
	w.closeToDeletion(t, w.tenants[39])

	credDigest := w.digest(t, `SELECT md5(string_agg(to_jsonb(c)::text, ',' ORDER BY c.id)) FROM pandokos.provider_credential c`)
	before := w.dataKeys(t)
	if len(before) != 40 {
		t.Fatalf("%d data keys before, want 40", len(before))
	}
	wrappedBefore := w.digest(t, `SELECT md5(string_agg(wrapped_key::text, ',' ORDER BY tenant_id)) FROM pandokos.tenant_data_key`)

	// Version 2 becomes the active one.
	if got := w.d.ActivateKEK(t, w.k[1].Version); got != "activated" {
		t.Fatal(got)
	}
	logs := &testutil.LogBuffer{}
	rw := &vault.Rewrapper{DB: w.worker, Provider: vaulttest.Provider(t, w.k[1], w.k[0]), Logger: newLogger(logs)}
	if err := rw.Pass(ctx(t)); err != nil {
		t.Fatalf("Pass: %v", err)
	}

	count := func(version string) int {
		return w.d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE kek_version = $1`, version)
	}
	if n1, n2 := count(w.k[0].Version), count(w.k[1].Version); n1 != 0 || n2 != 40 {
		t.Errorf("data keys under version 1: %d, under version 2: %d; want 0 and 40", n1, n2)
	}
	after := w.dataKeys(t)
	for tid, was := range before {
		now := after[tid]
		if now[0] != was[0] || now[1] != was[1] {
			t.Errorf("the id or the version of a data key changed: %v -> %v", was, now)
		}
		if now[2] != w.k[1].Version {
			t.Errorf("a data key is under %s, want %s", now[2], w.k[1].Version)
		}
	}
	if got := w.digest(t, `SELECT md5(string_agg(to_jsonb(c)::text, ',' ORDER BY c.id)) FROM pandokos.provider_credential c`); got != credDigest {
		t.Error("a provider_credential row changed: a credential is never read or written by a re-wrap")
	}
	if got := w.digest(t, `SELECT state FROM pandokos.kek_version WHERE id = $1`, w.k[0].Version); got != "retired" {
		t.Errorf("version 1 is %s, want retired", got)
	}
	if got := w.digest(t, `SELECT state FROM pandokos.kek_version WHERE id = $1`, w.k[1].Version); got != "active" {
		t.Errorf("version 2 is %s, want active", got)
	}
	for action, version := range map[string]string{"kek.activate": w.k[1].Version, "kek.retire": w.k[0].Version} {
		if n := w.d.Count(t, `SELECT count(*)::int FROM pandokos.platform_audit_event WHERE action = $1 AND resource_id = $2`, action, version); n != 1 {
			t.Errorf("%d %s rows for %s, want 1", n, action, version)
		}
	}
	if complete := logs.Find(t, "key rotation complete"); len(complete) != 1 || complete[0]["kek_version"] != w.k[0].Version || complete[0]["rewrapped"] != float64(40) {
		t.Errorf("the lines key rotation complete: %v, want one for %s with rewrapped 40", complete, w.k[0].Version)
	}
	st := rw.Stats()
	if st.OK != 40 || st.Failed != 0 || st.Versions["active"] != 1 || st.Versions["retired"] != 1 || st.Versions["retiring"] != 0 {
		t.Errorf("Stats %+v, want 40 re-wrapped, none failed, one version active and one retired", st)
	}

	// A second pass changes nothing.
	if err := rw.Pass(ctx(t)); err != nil {
		t.Fatalf("the second pass: %v", err)
	}
	if got := w.digest(t, `SELECT md5(string_agg(wrapped_key::text, ',' ORDER BY tenant_id)) FROM pandokos.tenant_data_key`); got == wrappedBefore {
		t.Error("the data keys were not re-wrapped")
	}
	if st2 := rw.Stats(); st2.OK != 40 {
		t.Errorf("a second pass re-wrapped %d keys more", st2.OK-40)
	}
	if n := w.d.Count(t, `SELECT count(*)::int FROM pandokos.platform_audit_event WHERE action = 'kek.retire'`); n != 1 {
		t.Errorf("%d kek.retire rows after the second pass, want 1", n)
	}

	// A provider that holds only version 2 opens every credential, to the plaintext it had.
	only2 := w.opener(t, logs, vaulttest.Provider(t, w.k[1]), 5*time.Second, time.Minute, nil)
	for _, tid := range w.tenants {
		got, err := open(t, only2, principalFor(tid, ""), "openai")
		if err != nil || got.Secret.Reveal() != w.plain[tid] {
			t.Errorf("the credential of %s after the rotation: %v", tid, err)
		}
	}
}

// TestRewrapFailureAndResume is T-990: a tenant whose key does not unwrap keeps its version and
// the version stays retiring; the rest go on; a pass that is cut short changes nothing; two jobs
// never wrap a key twice; and the database out of reach is retried.
func TestRewrapFailureAndResume(t *testing.T) {
	t.Parallel()
	t.Run("a failing tenant", func(t *testing.T) {
		w := newRotWorld(t, 6)
		w.d.ActivateKEK(t, w.k[1].Version)
		bad := w.tenants[2]
		fp := newFaultProvider(vaulttest.Provider(t, w.k[1], w.k[0]), w.k[1].Version)
		fp.failFor.Store(&bad)
		logs := &testutil.LogBuffer{}
		rw := &vault.Rewrapper{DB: w.worker, Provider: fp, Logger: newLogger(logs)}
		_ = rw.Pass(ctx(t))
		after := w.dataKeys(t)
		for _, tid := range w.tenants {
			want := w.k[1].Version
			if tid == bad {
				want = w.k[0].Version
			}
			if after[tid][2] != want {
				t.Errorf("a tenant's data key is under %s, want %s", after[tid][2], want)
			}
		}
		if got := w.digest(t, `SELECT state FROM pandokos.kek_version WHERE id = $1`, w.k[0].Version); got != "retiring" {
			t.Errorf("version 1 is %s, want retiring while a tenant still has it", got)
		}
		if lines := logs.Find(t, "key rewrap failing"); len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["cause"] != "data_key_unwrap" || lines[0]["failures"] != float64(1) {
			t.Errorf("the lines key rewrap failing: %v, want one warning with cause data_key_unwrap and one failure", lines)
		}
		if strings.Contains(logs.String(), bad.String()) {
			t.Error("a log line names the tenant")
		}
		if st := rw.Stats(); st.OK != 5 || st.Failed != 1 {
			t.Errorf("Stats %+v, want 5 re-wrapped and 1 failed (M49)", st)
		}
		if len(logs.Find(t, "key rotation complete")) != 0 {
			t.Error("key rotation complete was logged with a tenant left")
		}

		// The next pass, with the fault removed, completes.
		fp.failFor.Store(nil)
		if err := rw.Pass(ctx(t)); err != nil {
			t.Fatalf("the pass with the fault removed: %v", err)
		}
		if n := w.d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE kek_version = $1`, w.k[0].Version); n != 0 {
			t.Errorf("%d data keys still under version 1", n)
		}
		if got := w.digest(t, `SELECT state FROM pandokos.kek_version WHERE id = $1`, w.k[0].Version); got != "retired" {
			t.Errorf("version 1 is %s after the pass that completed, want retired", got)
		}
		if len(logs.Find(t, "key rotation complete")) != 1 {
			t.Error("no key rotation complete line after the pass that completed")
		}
	})

	t.Run("a pass cut short between the wrap and the update", func(t *testing.T) {
		w := newRotWorld(t, 1)
		w.d.ActivateKEK(t, w.k[1].Version)
		before := w.digest(t, `SELECT md5(kek_version || encode(wrapped_key, 'hex') || updated_at::text) FROM pandokos.tenant_data_key`)
		pass, cancel := context.WithCancel(ctx(t))
		defer cancel()
		fp := newFaultProvider(vaulttest.Provider(t, w.k[1], w.k[0]), w.k[1].Version)
		fp.onVerify = cancel // the check of the new value is the last thing before the update
		rw := &vault.Rewrapper{DB: w.worker, Provider: fp}
		if err := rw.Pass(pass); err == nil {
			t.Log("Pass returned no error when its context ended")
		}
		if got := w.digest(t, `SELECT md5(kek_version || encode(wrapped_key, 'hex') || updated_at::text) FROM pandokos.tenant_data_key`); got != before {
			t.Error("a pass cut short changed the data key")
		}
		if got := w.digest(t, `SELECT state FROM pandokos.kek_version WHERE id = $1`, w.k[0].Version); got != "retiring" {
			t.Errorf("version 1 is %s after a pass that did not finish", got)
		}
		fp.onVerify = nil
		if err := (&vault.Rewrapper{DB: w.worker, Provider: fp}).Pass(ctx(t)); err != nil {
			t.Fatal(err)
		}
		if n := w.d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE kek_version = $1`, w.k[1].Version); n != 1 {
			t.Errorf("%d data keys under version 2 after the next pass, want 1", n)
		}
	})

	t.Run("two jobs at once wrap a key once", func(t *testing.T) {
		w := newRotWorld(t, 12)
		w.d.ActivateKEK(t, w.k[1].Version)
		fp := newFaultProvider(vaulttest.Provider(t, w.k[1], w.k[0]), w.k[1].Version)
		jobs := []*vault.Rewrapper{{DB: w.worker, Provider: fp}, {DB: w.d.Open(t, store.RoleWorker), Provider: fp}}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, j := range jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := j.Pass(ctx(t)); err != nil {
					t.Errorf("Pass: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if got := fp.wraps.Load(); got != 12 {
			t.Errorf("%d wraps for 12 data keys: a key was wrapped twice (or not at all)", got)
		}
		if ok := jobs[0].Stats().OK + jobs[1].Stats().OK; ok != 12 {
			t.Errorf("the two jobs re-wrapped %d keys between them, want 12", ok)
		}
		if n := w.d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE kek_version = $1`, w.k[1].Version); n != 12 {
			t.Errorf("%d data keys under version 2, want 12", n)
		}
	})

	t.Run("the database out of reach is retried at the next tick", func(t *testing.T) {
		w := newRotWorld(t, 3)
		w.d.ActivateKEK(t, w.k[1].Version)
		rw := &vault.Rewrapper{DB: w.worker, Provider: vaulttest.Provider(t, w.k[1], w.k[0]), Logger: newLogger(&testutil.LogBuffer{})}
		w.d.SetAcceptingConnections(t, false)
		down, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := rw.Pass(down)
		cancel()
		if err == nil {
			t.Error("a pass with the database out of reach returned no error")
		}
		w.d.SetAcceptingConnections(t, true)
		var last error
		testutil.Eventually(t, 10*time.Second, "the next pass to complete", func() bool {
			last = rw.Pass(ctx(t))
			return last == nil
		})
		if n := w.d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE kek_version = $1`, w.k[1].Version); n != 3 {
			t.Errorf("%d data keys under version 2, want 3 (%v)", n, last)
		}
	})
}

// TestOpenDuringRewrap is T-991: while a rotation runs, credentials open for every tenant, at every
// moment; and the data key a gateway has cached for a tenant is dropped by the notification of
// that tenant.
func TestOpenDuringRewrap(t *testing.T) {
	t.Parallel()
	w := newRotWorld(t, 20)
	gwProvider := newFaultProvider(vaulttest.Provider(t, w.k[1], w.k[0]), w.k[1].Version)
	logs := &testutil.LogBuffer{}
	o := w.opener(t, logs, gwProvider, time.Second, 5*time.Minute, nil)
	listening(t, w.d.ConnOptions("gateway"), o, o.Flush, func(l *testutil.LogBuffer) bool {
		return len(l.Find(t, "cache listener connected")) > 0
	}, 10*time.Second)

	var failures atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := g; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				tid := w.tenants[i%len(w.tenants)]
				got, err := o.Open(ctx(t), vault.Use{Principal: principalFor(tid, ""), RequestID: "req-db-0001", Provider: "openai"})
				if err != nil || got.Secret.Reveal() != w.plain[tid] {
					failures.Add(1)
					t.Errorf("Open during the rotation: %v", err)
					return
				}
			}
		}()
	}
	time.Sleep(200 * time.Millisecond) // the openers are running before the rotation starts
	w.d.ActivateKEK(t, w.k[1].Version)
	rw := &vault.Rewrapper{DB: w.worker, Provider: gwProvider, Logger: newLogger(logs)}
	passErr := rw.Pass(ctx(t))
	close(stop)
	wg.Wait()
	if passErr != nil {
		t.Fatalf("Pass: %v", passErr)
	}
	if failures.Load() != 0 {
		t.Fatal("an Open failed during the rotation")
	}

	// The notification of a tenant drops its cached data key: the next Open unwraps again.
	probe := w.tenants[0]
	if _, err := open(t, o, principalFor(probe, ""), "openai"); err != nil {
		t.Fatal(err)
	}
	before := gwProvider.unwrapsFor(probe)
	o.Invalidate("vault:" + probe.String())
	if _, err := open(t, o, principalFor(probe, ""), "openai"); err != nil {
		t.Fatal(err)
	}
	if got := gwProvider.unwrapsFor(probe); got != before+1 {
		t.Errorf("after vault:<tenant> the data key was unwrapped %d times more, want 1", got-before)
	}
	// Another tenant's cache is not touched by it.
	other := w.tenants[1]
	if _, err := open(t, o, principalFor(other, ""), "openai"); err != nil {
		t.Fatal(err)
	}
	beforeOther := gwProvider.unwrapsFor(other)
	o.Invalidate("vault:" + probe.String())
	if _, err := open(t, o, principalFor(other, ""), "openai"); err != nil {
		t.Fatal(err)
	}
	if got := gwProvider.unwrapsFor(other); got != beforeOther {
		t.Errorf("the notification of one tenant made another unwrap its data key (%d times)", got-beforeOther)
	}
}
