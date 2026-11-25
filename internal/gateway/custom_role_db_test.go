package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestCustomRoleInvokes is T-858: a key whose service account holds a custom role calls a model
// with the permissions of the role, is refused in the shape of the family with one of the two, and
// is refused at the next request after the role is deactivated.
func TestCustomRoleInvokes(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "custom-invoke")
	logs := &testutil.LogBuffer{}
	auth, err := authn.New(authn.Options{DB: d.Open(t, store.RoleGateway), Logger: telemetry.NewLogger(logs, "debug"), Kinds: []authn.Kind{authn.KindTenant}, CacheTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	l, err := store.NewListener(store.ListenerOptions{Conn: d.ConnOptions("gateway"), Role: store.RoleGateway, Logger: telemetry.NewLogger(logs, "debug"),
		OnNotify: auth.Invalidate, OnReset: auth.Flush})
	if err != nil {
		t.Fatal(err)
	}
	lctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(lctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	testutil.Eventually(t, 10*time.Second, "the cache listener to connect", func() bool {
		return strings.Contains(logs.String(), `"msg":"cache listener connected"`)
	})

	d.CustomRole(t, tid, "caller", "model:invoke", "credential:use")
	d.CustomRole(t, tid, "invoke-only", "model:invoke")
	d.CustomRole(t, tid, "use-only", "credential:use")
	keyOf := func(role string) string {
		t.Helper()
		sa := d.SeedServiceAccount(t, tid, "sa-"+role)
		super := d.Super(t)
		if _, err := super.Exec(context.Background(), `INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, $3)`, tid.String(), sa, role); err != nil {
			t.Fatal(err)
		}
		key, keyID, hash, err := authn.Generate(authn.KindTenant)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := super.Exec(context.Background(), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, $4)`, tid.String(), sa, keyID, hash[:]); err != nil {
			t.Fatal(err)
		}
		return key.Reveal()
	}
	caller, invokeOnly, useOnly := keyOf("caller"), keyOf("invoke-only"), keyOf("use-only")

	h := newHarness(t, func(o *gateway.Options) { o.Authenticator = auth })
	ask := func(key string) (int, string, string) {
		t.Helper()
		resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat("fake-basic", false), http.Header{"Authorization": {"Bearer " + key}, "Content-Type": {"application/json"}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("X-Pandokos-Error"), string(b)
	}

	if status, _, body := ask(caller); status != 200 {
		t.Fatalf("a key whose account holds model:invoke and credential:use through a custom role: %d %s", status, body)
	}
	for name, key := range map[string]string{"model:invoke alone": invokeOnly, "credential:use alone": useOnly} {
		status, code, body := ask(key)
		var shape struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if status != 403 || code != "forbidden" || json.Unmarshal([]byte(body), &shape) != nil || shape.Error.Message != "Pandokos: the credential does not allow this operation." {
			t.Errorf("%s: %d %q %s, want 403 forbidden in the OpenAI shape", name, status, code, body)
		}
	}
	if n := len(h.up.Requests()); n != 1 {
		t.Errorf("the provider was contacted %d times, want once (the call that was allowed)", n)
	}

	// A change of the role reaches the next call past the notification.
	if _, err := d.Super(t).Exec(context.Background(), `UPDATE pandokos.tenant_role SET state = 'inactive' WHERE tenant_id = $1::uuid AND name = 'caller'`, tid.String()); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 3*time.Second, "the call to be refused after deactivateRole", func() bool {
		status, code, _ := ask(caller)
		return status == 403 && code == "forbidden"
	})
	if _, err := d.Super(t).Exec(context.Background(), `UPDATE pandokos.tenant_role SET state = 'active' WHERE tenant_id = $1::uuid AND name = 'caller'`, tid.String()); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 3*time.Second, "the call to work again after activateRole", func() bool {
		status, _, _ := ask(caller)
		return status == 200
	})
}
