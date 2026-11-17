package queries_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// limitStatements covers the two statements of sprint 5.1: the limit rules of a tenant and the
// platform ceilings a tenant's session sees (its own and the defaults), read in the session of the
// tenant by the gateway role. The rows are written by the admin role and removed again.
func limitStatements(c context.Context, t *testing.T, admin, gateway *store.DB, tid tenant.ID, tenantID string, called map[string]bool) {
	t.Helper()
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("limit rules written", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		for _, r := range []struct {
			kind, id, metric string
			value            int64
		}{
			{"tenant", "", "tokens_per_minute", 1000000},
			{"key", "k1", "requests_per_minute", 60},
			{"key", "k1", "concurrent_requests", 4},
		} {
			if _, err := tx.Exec(c, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value) VALUES ($1::uuid, $2, $3, $4, $5)`,
				tenantID, r.kind, r.id, r.metric, r.value); err != nil {
				return err
			}
		}
		return nil
	}))
	must("platform rules written", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		for _, r := range []struct {
			tenant *string
			metric string
			value  int64
		}{
			{nil, "concurrent_requests", 100},
			{&tenantID, "requests_per_minute", 500},
		} {
			if _, err := tx.Exec(c, `INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES ($1::uuid, $2, $3)`, r.tenant, r.metric, r.value); err != nil {
				return err
			}
		}
		return nil
	}))
	defer func() {
		must("platform rules removed", admin.Platform(c, func(c context.Context, tx store.Tx) error {
			_, err := tx.Exec(c, `DELETE FROM pandokos.platform_limit_rule`)
			return err
		}))
	}()

	must("rules read", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		called["ListLimitRules"] = true
		rules, err := q.ListLimitRules(c)
		if err != nil {
			return err
		}
		want := []queries.ListLimitRulesRow{
			{SubjectKind: "key", SubjectID: "k1", Metric: "concurrent_requests", LimitValue: 4},
			{SubjectKind: "key", SubjectID: "k1", Metric: "requests_per_minute", LimitValue: 60},
			{SubjectKind: "tenant", SubjectID: "", Metric: "tokens_per_minute", LimitValue: 1000000},
		}
		if !reflect.DeepEqual(rules, want) {
			t.Errorf("ListLimitRules: %+v, want %+v", rules, want)
		}
		called["ListPlatformLimitRules"] = true
		platform, err := q.ListPlatformLimitRules(c)
		if err != nil {
			return err
		}
		wantPlatform := []queries.ListPlatformLimitRulesRow{
			{IsDefault: true, Metric: "concurrent_requests", LimitValue: 100},
			{IsDefault: false, Metric: "requests_per_minute", LimitValue: 500},
		}
		if !reflect.DeepEqual(platform, wantPlatform) {
			t.Errorf("ListPlatformLimitRules: %+v, want %+v", platform, wantPlatform)
		}
		return nil
	}))
}
