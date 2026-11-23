package gateway_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/ledger"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestInferenceRefusesAClosedTenant is the part of T-1000 that concerns the inference listener: a key
// of a tenant that is being deleted is refused (403 tenant_suspended) and no usage record of the
// tenant is written; the same listener records a call of the tenant before its deletion began.
func TestInferenceRefusesAClosedTenant(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "closed-inference")
	logs := &testutil.LogBuffer{}
	gw := d.Open(t, store.RoleGateway)
	auth, err := authn.New(authn.Options{DB: gw, Logger: telemetry.NewLogger(logs, "debug"), Kinds: []authn.Kind{authn.KindTenant}, CacheTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w, err := ledger.New(ledger.Options{DB: gw, Logger: telemetry.NewLogger(logs, "debug"), BufferSize: 64, BatchSize: 8, FlushInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(wctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		w.Close(c)
	})
	// Two keys of one service account: the second is used only after the closing, so no cache
	// holds the tenant as it was.
	sa := d.SeedServiceAccount(t, tid, "caller")
	super := d.Super(t)
	if _, err := super.Exec(ctx5(t), `INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'inference')`, tid.String(), sa); err != nil {
		t.Fatal(err)
	}
	keyOf := func() string {
		key, keyID, hash, err := authn.Generate(authn.KindTenant)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := super.Exec(ctx5(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, $4)`, tid.String(), sa, keyID, hash[:]); err != nil {
			t.Fatal(err)
		}
		return key.Reveal()
	}
	before, after := keyOf(), keyOf()
	h := newHarness(t, func(o *gateway.Options) { o.Authenticator = auth; o.Recorder = catalogueFree{w} })
	ask := func(key string) (int, string) {
		t.Helper()
		resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat("fake-basic", false), http.Header{"Authorization": {"Bearer " + key}, "Content-Type": {"application/json"}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("X-Pandokos-Error")
	}
	records := func() int {
		testutil.Eventually(t, 10*time.Second, "every record is settled", func() bool { return w.Stats().InUse == 0 })
		return d.Count(t, `SELECT count(*)::int FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, tid.String())
	}

	if status, code := ask(before); status != 200 {
		t.Fatalf("a call of the open tenant: %d %s", status, code)
	}
	if n := records(); n != 1 {
		t.Fatalf("%d records after a call of the open tenant, want 1\n%s", n, logs.String())
	}

	d.BeginDeletion(t, tid)
	if status, code := ask(after); status != 403 || code != "tenant_suspended" {
		t.Errorf("a key of a tenant being deleted: %d %s, want 403 tenant_suspended", status, code)
	}
	if n := records(); n != 1 {
		t.Errorf("%d records after the call of the tenant being deleted, want still 1", n)
	}
}

// catalogueFree hands the ledger writer the records of the harness without the identifiers of its
// static catalogue, which name no row of the database.
type catalogueFree struct{ w *ledger.Writer }

func (c catalogueFree) Admit() bool { return c.w.Admit() }

func (c catalogueFree) Record(r usage.Record) {
	r.ModelID, r.CredentialID = "", ""
	r.Cost, r.CostStatus = pricing.Cost{}, "none"
	c.w.Record(r)
}
