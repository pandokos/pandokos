package vault_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

func notUsable(t *testing.T, err error, state string) {
	t.Helper()
	var nu *vault.NotUsableError
	if !errors.As(err, &nu) || nu.State != state {
		t.Errorf("error %v, want NotUsableError{%s}", err, state)
	}
}

// TestOpenerDB is T-325.
func TestOpenerDB(t *testing.T) {
	w := newVWorld(t)
	super := w.d.Super(t)
	logs := &testutil.LogBuffer{}
	fresh := func() *vault.Opener { return w.opener(t, logs, w.prov, 30*time.Second, 5*time.Minute, nil) }
	const (
		secA1  = fixtures.CanaryCredential
		secA2  = "synthetic-provider-secret-a-0002"
		secAnt = "synthetic-provider-secret-a-claude"
		secB1  = "synthetic-provider-secret-b-0001"
	)
	a, b := w.d.SeedTenant(t, "tenant-a"), w.d.SeedTenant(t, "tenant-b")
	cA1 := w.createCredential(t, w.sealer, a, "openai", "main", secA1)
	cA2 := w.createCredential(t, w.sealer, a, "openai", "second", secA2)
	cAnt := w.createCredential(t, w.sealer, a, "anthropic", "claude", secAnt)
	cB1 := w.createCredential(t, w.sealer, b, "openai", "main", secB1)

	t.Run("sealed as the admin role, opened as the gateway role", func(t *testing.T) {
		o := fresh()
		got, err := open(t, o, principalFor(a, ""), "openai")
		if err != nil || got.Secret.Reveal() != secA1 || got.ID != cA1 || got.Provider != "openai" || got.Label != "main" || got.Pinned {
			t.Fatalf("the default of A: %+v, %v", got, err)
		}
		var fp string
		if err := super.QueryRow(ctx(t), `SELECT fingerprint FROM pandokos.provider_credential WHERE id = $1::uuid`, cA1).Scan(&fp); err != nil || got.Fingerprint != fp {
			t.Errorf("fingerprint %q, database %q: %v", got.Fingerprint, fp, err)
		}
		// Pinned, the pin is used and not the default.
		got, err = open(t, o, principalFor(a, cA2), "openai")
		if err != nil || got.Secret.Reveal() != secA2 || got.ID != cA2 || !got.Pinned {
			t.Errorf("the pin of A: %+v, %v", got, err)
		}
		// A pin to another provider's credential: the call for openai uses the default of openai.
		got, err = open(t, o, principalFor(a, cAnt), "openai")
		if err != nil || got.Secret.Reveal() != secA1 || got.Pinned {
			t.Errorf("a pin to anthropic, a call for openai: %+v, %v", got, err)
		}
		got, err = open(t, o, principalFor(a, cAnt), "anthropic")
		if err != nil || got.Secret.Reveal() != secAnt || !got.Pinned {
			t.Errorf("a pin to anthropic, a call for anthropic: %+v, %v", got, err)
		}
		got, err = open(t, o, principalFor(a, ""), "anthropic")
		if err != nil || got.Secret.Reveal() != secAnt || got.Pinned {
			t.Errorf("the default of anthropic: %+v, %v", got, err)
		}
		// No default for a provider the tenant never registered.
		if _, err := open(t, o, principalFor(b, ""), "anthropic"); !errors.Is(err, vault.ErrNoCredential) {
			t.Errorf("no credential of that provider: %v", err)
		}
		// B has its own.
		if got, err := open(t, o, principalFor(b, ""), "openai"); err != nil || got.Secret.Reveal() != secB1 || got.ID != cB1 {
			t.Errorf("the default of B: %+v, %v", got, err)
		}
	})

	t.Run("a credential of another tenant is a credential that is not there", func(t *testing.T) {
		o := fresh()
		var missing string
		if err := super.QueryRow(ctx(t), `SELECT pandokos.uuid_v7()::text`).Scan(&missing); err != nil {
			t.Fatal(err)
		}
		_, errOther := open(t, o, principalFor(b, cA1), "openai")
		_, errMissing := open(t, o, principalFor(b, missing), "openai")
		if !errors.Is(errOther, vault.ErrNoCredential) || !errors.Is(errMissing, vault.ErrNoCredential) {
			t.Fatalf("another tenant's ID: %v; a missing ID: %v; want ErrNoCredential for both", errOther, errMissing)
		}
		if errOther.Error() != errMissing.Error() {
			t.Errorf("the two errors differ: %q and %q", errOther, errMissing)
		}
		// No fallback to the tenant's own default either.
		if got, err := open(t, o, principalFor(b, cA1), "openai"); err == nil || !got.Secret.IsZero() {
			t.Errorf("a pin to A's credential opened for B: %+v, %v", got, err)
		}
	})

	t.Run("disabled and revoked", func(t *testing.T) {
		c := w.d.SeedTenant(t, "tenant-c")
		c1 := w.createCredential(t, w.sealer, c, "openai", "main", "synthetic-provider-secret-c-0001")
		c2 := w.createCredential(t, w.sealer, c, "openai", "second", "synthetic-provider-secret-c-0002")
		if _, err := open(t, fresh(), principalFor(c, c2), "openai"); err != nil {
			t.Fatal(err)
		}
		w.setState(t, c, c2, "disabled")
		_, err := open(t, fresh(), principalFor(c, c2), "openai")
		notUsable(t, err, "disabled")
		w.setState(t, c, c2, "revoked")
		_, err = open(t, fresh(), principalFor(c, c2), "openai")
		notUsable(t, err, "revoked")
		// The default that is disabled or revoked is not replaced by another credential.
		w.setState(t, c, c1, "disabled")
		_, err = open(t, fresh(), principalFor(c, ""), "openai")
		notUsable(t, err, "disabled")
		w.setState(t, c, c1, "active")
		if got, err := open(t, fresh(), principalFor(c, ""), "openai"); err != nil || got.ID != c1 {
			t.Errorf("an enabled default: %+v, %v", got, err)
		}
		w.setState(t, c, c1, "revoked")
		_, err = open(t, fresh(), principalFor(c, ""), "openai")
		notUsable(t, err, "revoked")
	})

	// What a superuser can do to the rows, and what the cryptography answers.
	type pair struct{ x, y tenant.ID }
	makePair := func(t *testing.T, name string) (pair, string, string, string) {
		x, y := w.d.SeedTenant(t, name+"-x"), w.d.SeedTenant(t, name+"-y")
		x1 := w.createCredential(t, w.sealer, x, "openai", "main", "synthetic-provider-secret-x-0001")
		x2 := w.createCredential(t, w.sealer, x, "openai", "second", "synthetic-provider-secret-x-0002")
		y1 := w.createCredential(t, w.sealer, y, "openai", "main", "synthetic-provider-secret-y-0001")
		return pair{x, y}, x1, x2, y1
	}
	exec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx(t), sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustOpenAs := func(t *testing.T, tid tenant.ID, pin, want string) {
		t.Helper()
		if got, err := open(t, fresh(), principalFor(tid, pin), "openai"); err != nil || got.Secret.Reveal() != want {
			t.Errorf("the untouched credential: %v", err)
		}
	}

	t.Run("a sealed secret copied onto another tenant's row does not open", func(t *testing.T) {
		p, x1, _, y1 := makePair(t, "copy-tenant")
		exec(t, `UPDATE pandokos.provider_credential SET secret = (SELECT secret FROM pandokos.provider_credential WHERE id = $1::uuid) WHERE id = $2::uuid`, x1, y1)
		got, err := open(t, fresh(), principalFor(p.y, ""), "openai")
		if !errors.Is(err, vault.ErrUndecryptable) || !got.Secret.IsZero() {
			t.Errorf("a secret copied onto a row of another tenant: %+v, %v", got, err)
		}
		mustOpenAs(t, p.x, "", "synthetic-provider-secret-x-0001")
	})

	t.Run("a sealed secret copied onto another credential of the same tenant does not open", func(t *testing.T) {
		p, x1, x2, _ := makePair(t, "copy-credential")
		exec(t, `UPDATE pandokos.provider_credential SET secret = (SELECT secret FROM pandokos.provider_credential WHERE id = $1::uuid) WHERE id = $2::uuid`, x1, x2)
		got, err := open(t, fresh(), principalFor(p.x, x2), "openai")
		if !errors.Is(err, vault.ErrUndecryptable) || !got.Secret.IsZero() {
			t.Errorf("a secret copied onto another credential: %+v, %v", got, err)
		}
		mustOpenAs(t, p.x, "", "synthetic-provider-secret-x-0001")
	})

	t.Run("a sealed secret kept while key_version changes does not open", func(t *testing.T) {
		// Version 2 does not exist: nothing to open with.
		p, x1, _, _ := makePair(t, "version-missing")
		exec(t, `UPDATE pandokos.provider_credential SET key_version = 2 WHERE id = $1::uuid`, x1)
		if got, err := open(t, fresh(), principalFor(p.x, ""), "openai"); err == nil || !got.Secret.IsZero() {
			t.Errorf("a credential that names a data key that does not exist: %+v, %v", got, err)
		}
		// Version 2 exists and holds the same key material, wrapped for version 2: the tag still
		// fails, because the version is part of what the secret was sealed with.
		p2, x1v, _, _ := makePair(t, "version-copied")
		var kek string
		var wrapped []byte
		if err := super.QueryRow(ctx(t), `SELECT kek_version, wrapped_key FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid AND version = 1`, p2.x.String()).Scan(&kek, &wrapped); err != nil {
			t.Fatal(err)
		}
		raw, err := w.prov.Unwrap(ctx(t), kek, wrapped, vault.DataAAD(p2.x, 1))
		if err != nil {
			t.Fatal(err)
		}
		kek2, wrapped2, err := w.prov.Wrap(ctx(t), raw, vault.DataAAD(p2.x, 2))
		clear(raw)
		if err != nil {
			t.Fatal(err)
		}
		exec(t, `INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key) VALUES ($1::uuid, 2, $2, $3)`, p2.x.String(), kek2, wrapped2)
		exec(t, `UPDATE pandokos.provider_credential SET key_version = 2 WHERE id = $1::uuid`, x1v)
		got, err := open(t, fresh(), principalFor(p2.x, ""), "openai")
		if !errors.Is(err, vault.ErrUndecryptable) || !got.Secret.IsZero() {
			t.Errorf("a credential sealed under version 1 and read as version 2: %+v, %v", got, err)
		}
	})

	t.Run("a wrapped data key copied to another tenant does not unwrap", func(t *testing.T) {
		p, _, _, _ := makePair(t, "copy-dek")
		exec(t, `UPDATE pandokos.tenant_data_key SET wrapped_key = (SELECT wrapped_key FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid), kek_version = (SELECT kek_version FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid) WHERE tenant_id = $2::uuid`, p.x.String(), p.y.String())
		got, err := open(t, fresh(), principalFor(p.y, ""), "openai")
		if !errors.Is(err, vault.ErrUndecryptable) || !got.Secret.IsZero() {
			t.Errorf("a data key copied from another tenant: %+v, %v", got, err)
		}
		mustOpenAs(t, p.x, "", "synthetic-provider-secret-x-0001")
	})

	t.Run("the log holds no byte of a secret, a sealed value or a key", func(t *testing.T) {
		if len(logs.Find(t, "credential opened")) == 0 {
			t.Error("no debug line `credential opened`")
		}
		errLines := logs.Find(t, "credential cannot be opened")
		if len(errLines) < 4 {
			t.Fatalf("%d lines `credential cannot be opened`, want at least 4 (one per failure above)", len(errLines))
		}
		causes := map[string]int{}
		for _, l := range errLines {
			cause, _ := l["cause"].(string)
			causes[cause]++
			for _, f := range []string{"request_id", "tenant_id", "credential_id", "cause"} {
				if l[f] == nil || l[f] == "" {
					t.Errorf("the error line lacks %s: %v", f, l)
				}
			}
			if l["level"] != "ERROR" {
				t.Errorf("level %v", l["level"])
			}
		}
		for _, want := range []string{"authentication", "data_key_unwrap", "data_key_missing"} {
			if causes[want] == 0 {
				t.Errorf("no failure was logged with the cause %q: %v", want, causes)
			}
		}
		// Every secret, sealed value, wrapped key and data key of the database.
		var forbidden []string
		rows, err := super.Query(ctx(t), `SELECT secret FROM pandokos.provider_credential WHERE secret IS NOT NULL`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s []byte
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			forbidden = append(forbidden, byteForms(s)...)
		}
		rows.Close()
		rows, err = super.Query(ctx(t), `SELECT wrapped_key FROM pandokos.tenant_data_key`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s []byte
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			forbidden = append(forbidden, byteForms(s)...)
		}
		rows.Close()
		for _, s := range []string{secA1, secA2, secAnt, secB1, "synthetic-provider-secret-x-0001", "synthetic-provider-secret-y-0001", "synthetic-provider-secret-c-0001"} {
			forbidden = append(forbidden, testutil.SecretForms(s)...)
		}
		out := logs.String()
		for i, f := range forbidden {
			if len(f) >= 8 && strings.Contains(out, f) {
				t.Errorf("form %d of a secret or a key is in the log", i)
			}
		}
	})

	t.Run("NewOpener refuses what it cannot work with", func(t *testing.T) {
		good := vault.OpenerOptions{DB: w.gw, Provider: w.prov, Logger: newLogger(logs), CredentialTTL: 30 * time.Second, DataKeyTTL: 5 * time.Minute}
		if _, err := vault.NewOpener(good); err != nil {
			t.Fatalf("the good options: %v", err)
		}
		mutate := map[string]func(o *vault.OpenerOptions){
			"no database":                    func(o *vault.OpenerOptions) { o.DB = nil },
			"no provider":                    func(o *vault.OpenerOptions) { o.Provider = nil },
			"no logger":                      func(o *vault.OpenerOptions) { o.Logger = nil },
			"a credential lifetime of zero":  func(o *vault.OpenerOptions) { o.CredentialTTL = 0 },
			"a credential lifetime of 61 s":  func(o *vault.OpenerOptions) { o.CredentialTTL = 61 * time.Second },
			"a credential lifetime of 500ms": func(o *vault.OpenerOptions) { o.CredentialTTL = 500 * time.Millisecond },
			"a data key lifetime of zero":    func(o *vault.OpenerOptions) { o.DataKeyTTL = 0 },
			"a data key lifetime of 16 m":    func(o *vault.OpenerOptions) { o.DataKeyTTL = 16 * time.Minute },
			"a data key lifetime of 500ms":   func(o *vault.OpenerOptions) { o.DataKeyTTL = 500 * time.Millisecond },
		}
		for name, m := range mutate {
			o := good
			m(&o)
			if got, err := vault.NewOpener(o); err == nil || got != nil {
				t.Errorf("%s was accepted", name)
			}
		}
	})
}

// listening starts a store.Listener that feeds the opener and waits until it has connected.
func listening(t *testing.T, opts store.ConnOptions, o *vault.Opener, onReset func(), until func(logs *testutil.LogBuffer) bool, within time.Duration) {
	t.Helper()
	logs := &testutil.LogBuffer{}
	l, err := store.NewListener(store.ListenerOptions{Conn: opts, Role: store.RoleGateway, Logger: newLogger(logs), OnNotify: o.Invalidate, OnReset: onReset})
	if err != nil {
		t.Fatal(err)
	}
	lctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.Run(lctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })
	testutil.Eventually(t, within, "the cache listener", func() bool { return until(logs) })
}

// A tenant with an administrator, for the API calls of T-326 and T-327. The vault does not
// import the administration API, not even in its tests: the calls are raw requests.
type apiTenant struct {
	api   *apitest.API
	id    tenant.ID
	admin string
	sid   string // the tenant ID, as text
}

func newAPITenant(t *testing.T, api *apitest.API, name string) apiTenant {
	t.Helper()
	tn, key := api.Tenant(t, name)
	id, err := tenant.ParseID(tn.ID)
	if err != nil {
		t.Fatal(err)
	}
	return apiTenant{api: api, id: id, admin: key, sid: tn.ID}
}

// call makes a request with the key of the administrator and fails the test unless the status is 200.
func (a apiTenant) call(t *testing.T, method, path string, body any) {
	t.Helper()
	r := a.api.Do(t, method, "/v1/tenants/"+a.sid+"/credentials"+path, a.admin, body, nil)
	if r.Status != 200 {
		t.Fatalf("%s %s: status %d\n%s", method, path, r.Status, r.Body)
	}
}

// TestRevocationWithNotifications is T-326: with a lifetime of 60 s and a real listener on a
// direct connection, the change made through the API reaches Open within 2 s.
func TestRevocationWithNotifications(t *testing.T) {
	api := apitest.New(t)
	gw := api.DB.Open(t, store.RoleGateway)
	logs := &testutil.LogBuffer{}
	o, err := vault.NewOpener(vault.OpenerOptions{DB: gw, Provider: api.KeyProvider, Logger: newLogger(logs), CredentialTTL: time.Minute, DataKeyTTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	listening(t, api.DB.ConnOptions("gateway"), o, o.Flush, func(l *testutil.LogBuffer) bool {
		return len(l.Find(t, "cache listener connected")) > 0
	}, 10*time.Second)

	tn := newAPITenant(t, api, "notified")
	first := api.Credential(t, tn.sid, tn.admin, "openai", "first")
	p := principalFor(tn.id, "")
	within := func(what string, cond func() bool) {
		t.Helper()
		testutil.Eventually(t, 2*time.Second, what, cond)
	}
	check := func(pin, wantSecret string) func() bool {
		return func() bool {
			got, err := open(t, o, principalFor(tn.id, pin), "openai")
			return err == nil && got.Secret.Reveal() == wantSecret
		}
	}
	state := func(pin, state string) func() bool {
		return func() bool {
			_, err := open(t, o, principalFor(tn.id, pin), "openai")
			var nu *vault.NotUsableError
			return errors.As(err, &nu) && nu.State == state
		}
	}
	if got, err := open(t, o, p, "openai"); err != nil || got.Secret.Reveal() != fixtures.CanaryCredential {
		t.Fatalf("the first open: %v", err)
	}

	// disable, enable
	tn.call(t, "POST", "/"+first.ID+"/disable", map[string]any{})
	within("a disabled credential to be refused", state("", "disabled"))
	tn.call(t, "POST", "/"+first.ID+"/enable", map[string]any{})
	within("an enabled credential to open again", check("", fixtures.CanaryCredential))

	// replace the secret
	const replaced = "synthetic-replacement-secret-0001"
	tn.call(t, "PUT", "/"+first.ID+"/secret", map[string]any{"secret": replaced})
	within("the replaced secret", check("", replaced))

	// the default moves
	second := api.Credential(t, tn.sid, tn.admin, "openai", "second")
	if got, err := open(t, o, p, "openai"); err != nil || got.ID != first.ID {
		t.Fatalf("a new credential took the default: %+v, %v", got, err)
	}
	tn.call(t, "POST", "/"+second.ID+"/set-default", map[string]any{})
	within("the new default", func() bool {
		got, err := open(t, o, p, "openai")
		return err == nil && got.ID == second.ID
	})

	// revoke
	if got, err := open(t, o, principalFor(tn.id, first.ID), "openai"); err != nil || got.Secret.Reveal() != replaced {
		t.Fatalf("the pinned open: %v", err)
	}
	tn.call(t, "POST", "/"+first.ID+"/revoke", map[string]any{})
	within("a revoked credential to be refused", state(first.ID, "revoked"))

	// delete
	third := api.Credential(t, tn.sid, tn.admin, "openai", "third")
	if _, err := open(t, o, principalFor(tn.id, third.ID), "openai"); err != nil {
		t.Fatal(err)
	}
	tn.call(t, "DELETE", "/"+third.ID, nil)
	within("a deleted credential to be gone", func() bool {
		_, err := open(t, o, principalFor(tn.id, third.ID), "openai")
		return errors.Is(err, vault.ErrNoCredential)
	})

	// Another tenant's entries are not dropped by any of this: the notifications name a tenant.
	other := newAPITenant(t, api, "bystander")
	api.Credential(t, other.sid, other.admin, "openai", "main")
	if _, err := open(t, o, principalFor(other.id, ""), "openai"); err != nil {
		t.Fatal(err)
	}
	tn.call(t, "POST", "/"+second.ID+"/disable", map[string]any{})
	within("the second credential to be refused", state("", "disabled"))
	if _, err := open(t, o, principalFor(other.id, ""), "openai"); err != nil {
		t.Errorf("the bystander: %v", err)
	}
}

// TestRevocationBound is T-327: with no listener, or with one that cannot hear (through the
// pooler), Open serves the cached result until the injected clock passes the lifetime.
func TestRevocationBound(t *testing.T) {
	run := func(t *testing.T, api *apitest.API, listener func(t *testing.T, o *vault.Opener)) {
		t.Helper()
		gw := api.DB.Open(t, store.RoleGateway)
		clk := &sharedClock{t: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
		logs := &testutil.LogBuffer{}
		o, err := vault.NewOpener(vault.OpenerOptions{DB: gw, Provider: api.KeyProvider, Logger: newLogger(logs), Now: clk.now, CredentialTTL: 30 * time.Second, DataKeyTTL: 5 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if listener != nil {
			listener(t, o)
		}

		t.Run("a disabled credential after the credential lifetime", func(t *testing.T) {
			tn := newAPITenant(t, api, "bound-state")
			c := api.Credential(t, tn.sid, tn.admin, "openai", "main")
			p := principalFor(tn.id, "")
			if _, err := open(t, o, p, "openai"); err != nil {
				t.Fatal(err)
			}
			tn.call(t, "POST", "/"+c.ID+"/disable", map[string]any{})
			time.Sleep(300 * time.Millisecond) // a notification, if any could arrive, has arrived
			for _, step := range []time.Duration{10 * time.Second, 10 * time.Second, 10*time.Second - time.Nanosecond} {
				clk.advance(step)
				if got, err := open(t, o, p, "openai"); err != nil || got.Secret.Reveal() != fixtures.CanaryCredential {
					t.Fatalf("within the lifetime the cached credential is served: %v", err)
				}
			}
			clk.advance(time.Nanosecond)
			_, err := open(t, o, p, "openai")
			notUsable(t, err, "disabled")
		})

		t.Run("a removed data key after the data key lifetime and the credential lifetime", func(t *testing.T) {
			tn := newAPITenant(t, api, "bound-key")
			api.Credential(t, tn.sid, tn.admin, "openai", "main")
			p := principalFor(tn.id, "")
			// Lifetimes of 10 s and 60 s for this part, so that the loop is short.
			o2, err := vault.NewOpener(vault.OpenerOptions{DB: gw, Provider: api.KeyProvider, Logger: newLogger(logs), Now: clk.now, CredentialTTL: 10 * time.Second, DataKeyTTL: 60 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := open(t, o2, p, "openai"); err != nil {
				t.Fatal(err)
			}
			if _, err := api.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, tn.sid); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			failedAt := time.Duration(-1)
			for at := time.Second; at <= 90*time.Second; at += time.Second {
				clk.advance(time.Second)
				if _, err := open(t, o2, p, "openai"); err != nil {
					failedAt = at
					break
				}
			}
			if failedAt < 0 {
				t.Fatal("a removed data key is still served 90 s later")
			}
			if failedAt < 60*time.Second || failedAt > 70*time.Second {
				t.Errorf("the removed data key stopped working after %s, want between the data key lifetime (60 s) and the sum of both (70 s)", failedAt)
			}
			// The failure is not cached: it is repeated, not served from a negative entry.
			if _, err := open(t, o2, p, "openai"); err == nil {
				t.Error("the failure of a removed data key was not repeated")
			}
		})
	}

	t.Run("no listener", func(t *testing.T) {
		run(t, apitest.New(t), nil)
	})

	t.Run("through the pooler, where the listener cannot hear", func(t *testing.T) {
		d := dbtest.New(t)
		p := d.Pooled(t)
		d.Migrate(t)
		api := apitest.NewOn(t, p)
		run(t, api, func(t *testing.T, o *vault.Opener) {
			listening(t, p.ConnOptions("gateway"), o, func() {}, func(l *testutil.LogBuffer) bool {
				return len(l.Find(t, "cache invalidation unavailable")) >= 1
			}, 25*time.Second)
		})
	})
}

// sharedClock is the clock of an opener that tests move.
type sharedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *sharedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *sharedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
