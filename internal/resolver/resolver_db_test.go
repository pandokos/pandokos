package resolver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

// world is a migrated database with the administration API (to arrange data the way an operator
// does) and the gateway pool the resolver reads through.
type world struct {
	d    *dbtest.Database
	api  *apitest.API
	gw   *store.DB
	logs *testutil.LogBuffer
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := dbtest.NewMigrated(t)
	return &world{d: d, api: apitest.NewOn(t, d), gw: d.Open(t, store.RoleGateway), logs: &testutil.LogBuffer{}}
}

func (w *world) platform(t *testing.T, method, path string, body any) []byte {
	t.Helper()
	r := w.api.Do(t, method, path, w.api.PlatformKey(), body, nil)
	if r.Status/100 != 2 {
		t.Fatalf("%s %s: %d %s", method, path, r.Status, r.Body)
	}
	return r.Body
}

func (w *world) tenantCall(t *testing.T, key, method, path string, body any) []byte {
	t.Helper()
	r := w.api.Do(t, method, path, key, body, nil)
	if r.Status/100 != 2 {
		t.Fatalf("%s %s: %d %s", method, path, r.Status, r.Body)
	}
	return r.Body
}

func (w *world) catalogue(t *testing.T) *resolver.Catalogue {
	t.Helper()
	cache, err := catalog.NewCache(catalog.CacheOptions{DB: w.gw, Logger: telemetry.NewLogger(w.logs, "debug"), TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return resolver.NewCatalogue(cache, telemetry.NewLogger(w.logs, "debug"))
}

func principal(t *testing.T, tid string, pin string) authz.Principal {
	t.Helper()
	id, err := tenant.ParseID(tid)
	if err != nil {
		t.Fatal(err)
	}
	return authz.Principal{
		Kind: authz.KindServiceAccount, ID: "0192f0f0-2222-7000-8000-000000000001", Name: "caller", KeyID: "tabcdefghijklmnop",
		TenantID: id, TenantName: "t", TenantState: tenant.StateActive, Roles: []string{authz.RoleInference}, CredentialID: pin,
	}
}

// provider creates a provider of family openai with one default endpoint and one model.
func (w *world) provider(t *testing.T, name, baseURL string, private bool, model, upstream, availability string) (modelID string) {
	t.Helper()
	w.platform(t, "POST", "/v1/providers", map[string]any{"name": name, "api_family": "openai"})
	if baseURL != "" {
		w.platform(t, "POST", "/v1/providers/"+name+"/endpoints", map[string]any{"name": "main", "base_url": baseURL, "private": private})
	}
	var m struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.platform(t, "POST", "/v1/models", map[string]any{"provider": name, "name": model, "upstream_id": upstream, "availability": availability}), &m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

// TestCatalogue is T-440: the gateway's view of the catalogue, on a migrated database.
func TestCatalogue(t *testing.T) {
	w := newWorld(t)
	tn, _ := w.api.Tenant(t, "tenant-a")
	nickID := w.provider(t, "synth-main", "http://10.1.2.3:8080/api/prefix", true, "nick", "up/nick", "all")
	w.provider(t, "synth-noep", "", false, "orphan", "orphan", "all")
	selectedID := w.provider(t, "synth-sel", "https://sel.example.test", false, "chosen", "chosen", "selected")
	w.provider(t, "synth-off", "https://off.example.test", false, "off-model", "off-model", "all")
	w.platform(t, "PATCH", "/v1/providers/synth-off", map[string]any{"enabled": false})
	_ = selectedID

	cat := w.catalogue(t)
	p := principal(t, tn.ID, "")

	t.Run("Provider", func(t *testing.T) {
		for name, want := range map[string]struct {
			family string
			ok     bool
		}{
			"openai":     {"openai", true},
			"anthropic":  {"anthropic", true},
			"synth-main": {"openai", true},
			"synth-off":  {"", false},
			"nope":       {"", false},
			"v1":         {"", false},
		} {
			fam, ok, err := cat.Provider(ctx(t), name)
			if err != nil || ok != want.ok || fam != want.family {
				t.Errorf("%s: (%q, %v, %v), want (%q, %v, nil)", name, fam, ok, err, want.family, want.ok)
			}
		}
	})

	t.Run("Resolve returns what the call needs", func(t *testing.T) {
		got, err := cat.Resolve(ctx(t), p, "synth-main", "nick")
		if err != nil {
			t.Fatal(err)
		}
		if got.Provider != "synth-main" || got.Endpoint != "main" || got.ModelID != nickID || got.Model != "nick" || got.UpstreamModel != "up/nick" || !got.Private {
			t.Errorf("target: %+v", got)
		}
		if got.BaseURL == nil || got.BaseURL.String() != "http://10.1.2.3:8080/api/prefix" {
			t.Errorf("BaseURL = %v", got.BaseURL)
		}
	})

	t.Run("both refusals map", func(t *testing.T) {
		for name, c := range map[string]struct {
			provider, model string
			want            error
		}{
			"an unknown model":                    {"synth-main", "nope", gateway.ErrModelNotAllowed},
			"another provider's model":            {"synth-main", "chosen", gateway.ErrModelNotAllowed},
			"a model that is selected, ungranted": {"synth-sel", "chosen", gateway.ErrModelNotAllowed},
			"a provider without an endpoint":      {"synth-noep", "orphan", gateway.ErrProviderUnavailable},
			"a disabled provider":                 {"synth-off", "off-model", gateway.ErrProviderUnavailable},
			"an unknown provider":                 {"nope", "nick", gateway.ErrProviderUnavailable},
		} {
			_, err := cat.Resolve(ctx(t), p, c.provider, c.model)
			if !errors.Is(err, c.want) {
				t.Errorf("%s: %v, want %v", name, err, c.want)
			}
		}
	})

	t.Run("a principal that is not a service account is refused", func(t *testing.T) {
		if _, err := cat.Resolve(ctx(t), authz.Principal{}, "synth-main", "nick"); !errors.Is(err, gateway.ErrModelNotAllowed) {
			t.Errorf("%v", err)
		}
	})

	t.Run("with the database stopped the error is neither refusal", func(t *testing.T) {
		cold := w.catalogue(t)
		w.d.SetAcceptingConnections(t, false)
		t.Cleanup(func() { w.d.SetAcceptingConnections(t, true) })
		_, err := cold.Resolve(ctx(t), p, "synth-main", "nick")
		if err == nil || errors.Is(err, gateway.ErrModelNotAllowed) || errors.Is(err, gateway.ErrProviderUnavailable) {
			t.Errorf("Resolve: %v", err)
		}
		if _, _, err := w.catalogue(t).Provider(ctx(t), "openai"); err == nil {
			t.Error("Provider returned no error with the database stopped")
		}
	})
}

// secrets of the credentials of the world, synthetic.
const (
	secretOne   = "synthetic-credential-one-0001"
	secretTwo   = "synthetic-credential-two-0002"
	secretOther = "synthetic-credential-other-0003"
)

func (w *world) credential(t *testing.T, adminKey, tid, provider, label, secretValue string) adminapi.Credential {
	t.Helper()
	var c adminapi.Credential
	if err := json.Unmarshal(w.tenantCall(t, adminKey, "POST", "/v1/tenants/"+tid+"/credentials", map[string]any{"provider": provider, "label": label, "secret": secretValue}), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func (w *world) credentials(t *testing.T) *resolver.Credentials {
	t.Helper()
	o, err := vault.NewOpener(vault.OpenerOptions{
		DB: w.gw, Provider: w.api.KeyProvider, Logger: telemetry.NewLogger(w.logs, "debug"),
		CredentialTTL: time.Second, DataKeyTTL: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver.NewCredentials(o)
}

// TestCredentials is T-441: the credential a call is made with, and tenant isolation.
func TestCredentials(t *testing.T) {
	w := newWorld(t)
	a, keyA := w.api.Tenant(t, "tenant-a")
	b, keyB := w.api.Tenant(t, "tenant-b")
	empty, _ := w.api.Tenant(t, "tenant-empty")
	creds := w.credentials(t)
	const requestID = "0123456789abcdef0123456789abcdef"
	open := func(p authz.Principal, provider string) (gateway.Credential, error) {
		return creds.Open(ctx(t), p, requestID, provider)
	}

	one := w.credential(t, keyA, a.ID, "openai", "one", secretOne) // the first is the default
	two := w.credential(t, keyA, a.ID, "openai", "two", secretTwo)
	anth := w.credential(t, keyA, a.ID, "anthropic", "anth", secretOther)
	bOne := w.credential(t, keyB, b.ID, "openai", "b-one", secretOther+"-b")

	t.Run("the default of the tenant, its ID, its secret", func(t *testing.T) {
		got, err := open(principal(t, a.ID, ""), "openai")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != one.ID || got.Pinned || got.Secret.Reveal() != secretOne {
			t.Errorf("got ID %s pinned %v (want %s, false)", got.ID, got.Pinned, one.ID)
		}
		testutil.AssertAbsent(t, "the logs", w.logs.String(), testutil.SecretForms(secretOne)...)
	})

	t.Run("a pinned key gets its pin for the pin's provider and the default for another", func(t *testing.T) {
		got, err := open(principal(t, a.ID, two.ID), "openai")
		if err != nil || got.ID != two.ID || !got.Pinned || got.Secret.Reveal() != secretTwo {
			t.Errorf("the pin's provider: %+v %v", got, err)
		}
		got, err = open(principal(t, a.ID, two.ID), "anthropic")
		if err != nil || got.ID != anth.ID || got.Pinned || got.Secret.Reveal() != secretOther {
			t.Errorf("another provider: %+v %v", got, err)
		}
	})

	t.Run("no credential, disabled, revoked", func(t *testing.T) {
		if _, err := open(principal(t, empty.ID, ""), "openai"); !errors.Is(err, gateway.ErrNoCredential) {
			t.Errorf("a tenant without credential: %v", err)
		}
		// A provider the tenant has no credential for.
		if _, err := open(principal(t, b.ID, ""), "anthropic"); !errors.Is(err, gateway.ErrNoCredential) {
			t.Errorf("a provider without a credential: %v", err)
		}
		w.tenantCall(t, keyB, "POST", "/v1/tenants/"+b.ID+"/credentials/"+bOne.ID+"/disable", map[string]any{})
		time.Sleep(1100 * time.Millisecond) // the credential cache lives one second
		if _, err := w.credentials(t).Open(ctx(t), principal(t, b.ID, ""), requestID, "openai"); !errors.Is(err, gateway.ErrNoCredential) {
			t.Errorf("a disabled credential: %v", err)
		}
		w.tenantCall(t, keyB, "POST", "/v1/tenants/"+b.ID+"/credentials/"+bOne.ID+"/enable", map[string]any{})
		w.tenantCall(t, keyB, "POST", "/v1/tenants/"+b.ID+"/credentials/"+bOne.ID+"/revoke", map[string]any{})
		if _, err := w.credentials(t).Open(ctx(t), principal(t, b.ID, ""), requestID, "openai"); !errors.Is(err, gateway.ErrNoCredential) {
			t.Errorf("a revoked credential: %v", err)
		}
	})

	t.Run("a principal without credential:use is denied", func(t *testing.T) {
		p := principal(t, a.ID, "")
		p.Roles = []string{authz.RoleTenantAdmin}
		p.Scopes = []string{authz.VirtualKeyRead}
		if _, err := open(p, "openai"); !errors.Is(err, gateway.ErrCredentialDenied) {
			t.Errorf("%v", err)
		}
		p.Roles = nil
		p.Scopes = nil
		if _, err := open(p, "openai"); !errors.Is(err, gateway.ErrCredentialDenied) {
			t.Errorf("no role: %v", err)
		}
	})

	t.Run("a key of tenant B never opens a credential of tenant A", func(t *testing.T) {
		// A forged principal of B pinned to the credential of A: nothing falls back to B's own.
		w.credential(t, keyB, b.ID, "openai", "b-two", secretOther+"-b2")
		for name, pin := range map[string]string{"by ID": one.ID, "by the second ID": two.ID} {
			got, err := w.credentials(t).Open(ctx(t), principal(t, b.ID, pin), requestID, "openai")
			if !errors.Is(err, gateway.ErrNoCredential) {
				t.Errorf("%s: %+v %v, want ErrNoCredential", name, got.ID, err)
			}
			testutil.AssertAbsent(t, name, fmt.Sprint(got.ID), one.ID, two.ID)
			if got.Secret.Reveal() != "" {
				t.Errorf("%s: a secret was returned with the refusal", name)
			}
		}
		// And by default: B's default is B's.
		got, err := w.credentials(t).Open(ctx(t), principal(t, b.ID, ""), requestID, "openai")
		if err != nil && !errors.Is(err, gateway.ErrNoCredential) {
			t.Fatal(err)
		}
		if err == nil && (got.ID == one.ID || got.ID == two.ID || got.Secret.Reveal() == secretOne || got.Secret.Reveal() == secretTwo) {
			t.Errorf("tenant B opened a credential of tenant A: %s", got.ID)
		}
	})

	t.Run("with the database stopped the error is not a refusal", func(t *testing.T) {
		cold := w.credentials(t)
		w.d.SetAcceptingConnections(t, false)
		t.Cleanup(func() { w.d.SetAcceptingConnections(t, true) })
		_, err := cold.Open(ctx(t), principal(t, a.ID, ""), requestID, "anthropic")
		if err == nil || errors.Is(err, gateway.ErrNoCredential) || errors.Is(err, gateway.ErrCredentialDenied) {
			t.Errorf("%v", err)
		}
	})
	_ = fixtures.CanaryCredential
}

// fakeClock is a clock the test moves.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func requestIDFor(i int) string { return fmt.Sprintf("%032x", i) }

// keyIDFor returns the i-th distinct key ID: "t" and 16 characters of the key alphabet.
func keyIDFor(i int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	b := []byte("taaaaaaaaaaaaaaaa")
	for pos := len(b) - 1; i > 0 && pos > 0; pos-- {
		b[pos] = alphabet[i%32]
		i /= 32
	}
	return string(b)
}

// TestFailureAuditor is T-442: the bounded record of authentication failures of the inference
// listener.
func TestFailureAuditor(t *testing.T) {
	d := dbtest.NewMigrated(t)
	gw := d.Open(t, store.RoleGateway)
	a := d.SeedTenant(t, "tenant-a")
	super := d.Super(t)
	logs := &testutil.LogBuffer{}
	clock := &fakeClock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	newAuditor := func() *resolver.FailureAuditor {
		return resolver.NewFailureAuditor(gw, telemetry.NewLogger(logs, "debug"), clock.now)
	}
	rows := func() int {
		t.Helper()
		var n int
		if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure'`, a.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	failure := func(i int, key, reason string) gateway.AuthFailure {
		return gateway.AuthFailure{RequestID: requestIDFor(i), Reason: reason, KeyID: key, TenantID: a.String()}
	}

	t.Run("one row with the listener, in the tenant's log", func(t *testing.T) {
		au := newAuditor()
		if !au.Record(ctx(t), failure(1, "tabcdefghijklmnop", "revoked")) {
			t.Fatal("not recorded")
		}
		var detail, key, request, actor string
		if err := super.QueryRow(ctx(t), `SELECT detail::text, key_id, request_id, actor_type FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure'`, a.String()).Scan(&detail, &key, &request, &actor); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(detail, `"listener": "inference"`) || !strings.Contains(detail, `"reason": "revoked"`) || key != "tabcdefghijklmnop" || request != requestIDFor(1) || actor != "anonymous" {
			t.Errorf("row: %s %s %s %s", detail, key, request, actor)
		}
	})

	t.Run("the same key and reason within the window is not recorded again", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Minute)
		if !au.Record(ctx(t), failure(2, "tbbbbbbbbbbbbbbbb", "expired")) {
			t.Fatal("the first was not recorded")
		}
		before := rows()
		clock.t = clock.t.Add(9 * time.Minute)
		if au.Record(ctx(t), failure(3, "tbbbbbbbbbbbbbbbb", "expired")) || rows() != before {
			t.Error("a repeat within ten minutes was recorded")
		}
		if !au.Record(ctx(t), failure(4, "tbbbbbbbbbbbbbbbb", "wrong_secret")) || rows() != before+1 {
			t.Error("another reason for the same key must be recorded")
		}
		if !au.Record(ctx(t), failure(5, "tcccccccccccccccc", "expired")) || rows() != before+2 {
			t.Error("another key with the same reason must be recorded")
		}
		clock.t = clock.t.Add(2 * time.Minute) // eleven minutes after the first
		if !au.Record(ctx(t), failure(6, "tbbbbbbbbbbbbbbbb", "expired")) || rows() != before+3 {
			t.Error("a repeat after the window must be recorded")
		}
	})

	t.Run("concurrent identical failures write one row", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := rows()
		const n = 20
		var wrote atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if au.Record(ctx(t), failure(400+i, "tffffffffffffffff", "revoked")) {
					wrote.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wrote.Load() != 1 || rows() != before+1 {
			t.Errorf("%d of %d concurrent identical failures were recorded, %d rows written; want 1", wrote.Load(), n, rows()-before)
		}
	})

	t.Run("a failure that is not recorded leaves no trace in the window", func(t *testing.T) {
		// Only a successful insert starts the window: a refused one (here a bad tenant) does not.
		au := newAuditor()
		bad := failure(7, "tdddddddddddddddd", "expired")
		bad.TenantID = "0192f0f0-0000-7000-8000-00000000dead" // no such tenant: the insert is refused by the policy or a constraint
		_ = au.Record(ctx(t), bad)
		before := rows()
		if !au.Record(ctx(t), failure(8, "tdddddddddddddddd", "expired")) || rows() != before+1 {
			t.Error("a failed insert started the suppression window")
		}
	})

	t.Run("an exhausted bucket writes nothing", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := rows()
		for i := 0; i < 60; i++ {
			if !au.Record(ctx(t), failure(100+i, keyIDFor(1000+i), "wrong_secret")) {
				t.Fatalf("failure %d was refused before the bucket was empty", i)
			}
		}
		if au.Record(ctx(t), failure(200, keyIDFor(2000), "wrong_secret")) || rows() != before+60 {
			t.Error("the 61st failure within the same second was recorded")
		}
		clock.t = clock.t.Add(time.Second)
		if !au.Record(ctx(t), failure(201, keyIDFor(2001), "wrong_secret")) {
			t.Error("a token is back after a second")
		}
		if au.Record(ctx(t), failure(202, keyIDFor(2002), "wrong_secret")) {
			t.Error("only one token came back")
		}
	})

	t.Run("a full map is emptied", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		const n = 4097
		for i := 0; i < n; i++ {
			clock.t = clock.t.Add(time.Second) // one token a second: the bucket never runs dry
			if !au.Record(ctx(t), failure(1000+i, keyIDFor(10000+i), "wrong_secret")) {
				t.Fatalf("failure %d was not recorded", i)
			}
		}
		// The key recorded about 97 seconds before the end is within its window of ten minutes, and
		// is no longer remembered: the map was emptied when it reached its bound.
		before := rows()
		if !au.Record(ctx(t), failure(9000, keyIDFor(10000+4000), "wrong_secret")) || rows() != before+1 {
			t.Error("a key recorded earlier is still suppressed after the map was filled and emptied")
		}
		clock.t = clock.t.Add(time.Second)
		if au.Record(ctx(t), failure(9001, keyIDFor(10000+n-1), "wrong_secret")) {
			t.Error("the newest key must still be suppressed")
		}
	})

	t.Run("without a tenant nothing is written", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := rows()
		for _, tn := range []string{"", "not-a-uuid", "0192F0F0"} {
			f := failure(300, "teeeeeeeeeeeeeeee", "unknown_key")
			f.TenantID = tn
			if au.Record(ctx(t), f) {
				t.Errorf("a failure with the tenant %q was recorded", tn)
			}
		}
		if rows() != before {
			t.Error("a row was written")
		}
	})

	t.Run("a context that was cancelled does not stop the record", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		c, cancel := context.WithCancel(context.Background())
		cancel()
		before := rows()
		if !au.Record(c, failure(400, "tffffffffffffffff", "revoked")) || rows() != before+1 {
			t.Error("the record depends on the context of the request")
		}
	})

	t.Run("with the database stopped it gives up within 3 s", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		d.SetAcceptingConnections(t, false)
		t.Cleanup(func() { d.SetAcceptingConnections(t, true) })
		start := time.Now()
		if au.Record(ctx(t), failure(500, "tggggggggggggggggg"[:17], "revoked")) {
			t.Error("recorded with the database stopped")
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("Record took %s", d)
		}
		for _, l := range logs.Find(t, "authentication failure not recorded") {
			if l["level"] != "DEBUG" || l["request_id"] == nil {
				t.Errorf("the line: %v", l)
			}
		}
	})
}

// TestTargetPrices is T-483: Resolve returns the model's price versions from the snapshot that
// resolved it; a model without a price returns none; versions changed in the database appear
// after the cache refresh.
func TestTargetPrices(t *testing.T) {
	w := newWorld(t)
	tn, _ := w.api.Tenant(t, "tenant-prices")
	priced := w.provider(t, "synth-priced", "http://10.1.2.3:8080", true, "priced", "up/priced", "all")
	bare := w.provider(t, "synth-bare", "http://10.1.2.4:8080", true, "bare", "up/bare", "all")
	_ = bare
	cat := w.catalogue(t)
	p := principal(t, tn.ID, "")

	resolve := func(provider, model string) gateway.Target {
		t.Helper()
		target, err := cat.Resolve(ctx(t), p, provider, model)
		if err != nil {
			t.Fatalf("Resolve(%s, %s): %v", provider, model, err)
		}
		return target
	}

	t.Run("a model without a price returns none", func(t *testing.T) {
		if got := resolve("synth-bare", "bare").Prices; len(got) != 0 {
			t.Errorf("%+v", got)
		}
		if got := resolve("synth-priced", "priced").Prices; len(got) != 0 {
			t.Errorf("%+v", got)
		}
	})

	rates := []map[string]any{{"category": "input", "threshold": 0, "rate": "2"}, {"category": "output", "threshold": 0, "rate": "4"}}
	w.platform(t, "POST", "/v1/models/"+priced+"/prices", map[string]any{"currency": "USD", "rates": rates})
	later := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	w.platform(t, "POST", "/v1/models/"+priced+"/prices", map[string]any{"currency": "EUR", "valid_from": later.Format(time.RFC3339),
		"rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "3"}, {"category": "output", "threshold": 0, "rate": "6"}}})

	t.Run("versions changed in the database appear after the cache refresh", func(t *testing.T) {
		var prices []pricing.Version
		testutil.Eventually(t, 10*time.Second, "both versions reach the target", func() bool {
			prices = resolve("synth-priced", "priced").Prices
			return len(prices) == 2
		})
		if prices[0].Currency != "USD" || prices[1].Currency != "EUR" || !prices[1].ValidFrom.Equal(later) {
			t.Errorf("%+v", prices)
		}
		for _, v := range prices {
			if v.ModelID != priced || v.ID == "" || len(v.Rates) != 2 {
				t.Errorf("version %+v", v)
			}
		}
		at, ok := pricing.VersionAt(prices, time.Now())
		if !ok || at.Currency != "USD" {
			t.Errorf("the version in force now: %+v %v", at, ok)
		}
		if at, ok = pricing.VersionAt(prices, later.Add(time.Minute)); !ok || at.Currency != "EUR" {
			t.Errorf("the version in force later: %+v %v", at, ok)
		}
		if got := resolve("synth-bare", "bare").Prices; len(got) != 0 {
			t.Errorf("the other model got %+v", got)
		}
	})
}
