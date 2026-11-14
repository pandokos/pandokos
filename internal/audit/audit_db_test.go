package audit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
)

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// TestRecordAuthFailure is T-449: the query writes the row through the function of migration
// 0005, and the function's format check refuses what does not look like its arguments.
func TestRecordAuthFailure(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	gw := d.Open(t, store.RoleGateway)
	super := d.Super(t)
	const (
		keyID     = "tabcdefghijklmnop"
		requestID = "0123456789abcdef0123456789abcdef"
	)
	write := func(key, reason, request string) error {
		return gw.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			return audit.RecordAuthFailure(c, tx, key, reason, request)
		})
	}

	if err := write(keyID, "wrong_secret", requestID); err != nil {
		t.Fatal(err)
	}
	var action, actor, resType, resID, outcome, request, detail string
	if err := super.QueryRow(ctx(t), `SELECT action, actor_type, resource_type, resource_id, outcome, request_id, detail::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid`, a.String()).
		Scan(&action, &actor, &resType, &resID, &outcome, &request, &detail); err != nil {
		t.Fatal(err)
	}
	if action != "auth.failure" || actor != "anonymous" || resType != "virtual_key" || resID != keyID || outcome != "failure" || request != requestID {
		t.Errorf("the row: %s %s %s %s %s %s", action, actor, resType, resID, outcome, request)
	}
	if !strings.Contains(detail, `"listener": "inference"`) || !strings.Contains(detail, `"reason": "wrong_secret"`) {
		t.Errorf("detail %s", detail)
	}

	// Values that look like what must never be written are refused by the function's format check.
	for name, c := range map[string]struct{ key, reason, request string }{
		"a whole key as the key ID":   {"pdk_tabcdefghijklmnop_" + strings.Repeat("a", 52), "wrong_secret", requestID},
		"a secret as the reason":      {keyID, "pdk_" + strings.Repeat("a", 52), requestID},
		"a reason with a bearer":      {keyID, "bearer pdk_abc", requestID},
		"a prompt as the request ID":  {keyID, "wrong_secret", "synthetic prompt text of a caller"},
		"a request ID with a newline": {keyID, "wrong_secret", requestID[:31] + "\n"},
	} {
		err := write(c.key, c.reason, c.request)
		if store.SQLState(err) != "23514" {
			t.Errorf("%s: %v, want SQLSTATE 23514", name, err)
		}
	}
	var n int
	if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid`, a.String()).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d rows after the refused calls, want the one of the first: %v", n, err)
	}
}
