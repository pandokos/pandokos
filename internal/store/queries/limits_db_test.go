package queries_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// limitStatements covers the two statements of sprint 5.1: the limit rules of a tenant and the
// platform ceilings a tenant's session sees (its own and the defaults), read in the session of the
// tenant by the gateway role. The rows are written by the admin role and removed again.
func limitStatements(c context.Context, t *testing.T, admin, gateway *store.DB, tid tenant.ID, tenantID, keyID string, called map[string]bool) {
	t.Helper()
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("limit rules written", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		// A rule of a key names the key by foreign key (migration 0011): keyID is a live virtual key of the tenant.
		for _, r := range []struct {
			kind, metric string
			value        int64
		}{
			{"tenant", "tokens_per_minute", 1000000},
			{"key", "requests_per_minute", 60},
			{"key", "concurrent_requests", 4},
		} {
			if _, err := tx.Exec(c, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, virtual_key_id, metric, limit_value)
				VALUES ($1::uuid, $2, (SELECT id FROM pandokos.virtual_key WHERE key_id = CASE WHEN $2 = 'key' THEN $5 END), $3, $4)`,
				tenantID, r.kind, r.metric, r.value, keyID); err != nil {
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
		// The identifiers are generated (and the statement orders by them, so by insertion): the want carries those that were read. Since sprint 5.2 a
		// rule also has a scope, a currency, an enforcement and thresholds; the rows above leave
		// them at their defaults.
		want := []queries.ListLimitRulesRow{
			{SubjectKind: "tenant", SubjectID: "", Metric: "tokens_per_minute", LimitValue: 1000000, Enforce: true, Thresholds: []int16{}},
			{SubjectKind: "key", SubjectID: keyID, Metric: "requests_per_minute", LimitValue: 60, Enforce: true, Thresholds: []int16{}},
			{SubjectKind: "key", SubjectID: keyID, Metric: "concurrent_requests", LimitValue: 4, Enforce: true, Thresholds: []int16{}},
		}
		for i := range want {
			if i < len(rules) {
				want[i].ID = rules[i].ID
			}
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
			{IsDefault: true, Metric: "concurrent_requests", LimitValue: 100, Enforce: true, Thresholds: []int16{}},
			{IsDefault: false, Metric: "requests_per_minute", LimitValue: 500, Enforce: true, Thresholds: []int16{}},
		}
		for i := range wantPlatform {
			if i < len(platform) {
				wantPlatform[i].ID = platform[i].ID
			}
		}
		if !reflect.DeepEqual(platform, wantPlatform) {
			t.Errorf("ListPlatformLimitRules: %+v, want %+v", platform, wantPlatform)
		}
		return nil
	}))
}

// noID is the identifier a paged statement is given when no cursor is: the parameter is a uuid and
// is cast even when the cursor is not used.
const noID = "00000000-0000-0000-0000-000000000000"

// quotaStatements covers the statements of sprint 5.2: the administration of limit rules and of
// platform ceilings, the limit events, and the ledger aggregate the counters are rebuilt from.
func quotaStatements(c context.Context, t *testing.T, admin, gateway *store.DB, tid tenant.ID, called map[string]bool) {
	t.Helper()
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	fail := func(format string, args ...any) { t.Helper(); t.Errorf(format, args...) }
	var rule queries.CreateLimitRuleRow
	var before int64

	must("tenant rules administered", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		called["LockLimitRules"] = true
		if err := q.LockLimitRules(c); err != nil {
			return err
		}
		called["CountLimitRules"] = true
		var err error
		if before, err = q.CountLimitRules(c); err != nil {
			return err
		}
		called["ResolveLimitSubject"] = true
		if r, err := q.ResolveLimitSubject(c, queries.ResolveLimitSubjectParams{SubjectKind: "tenant"}); err != nil || r != (queries.ResolveLimitSubjectRow{}) {
			fail("the tenant subject: %+v %v", r, err)
		}
		if r, err := q.ResolveLimitSubject(c, queries.ResolveLimitSubjectParams{SubjectKind: "service_account", SubjectID: "0192f0f0-0000-7000-8000-000000000001"}); err != nil || r != (queries.ResolveLimitSubjectRow{}) {
			fail("a missing service account: %+v %v", r, err)
		}
		if r, err := q.ResolveLimitSubject(c, queries.ResolveLimitSubjectParams{SubjectKind: "key", SubjectID: "tmissingkeyid00000"}); err != nil || r != (queries.ResolveLimitSubjectRow{}) {
			fail("a missing key: %+v %v", r, err)
		}
		called["ResolveLimitScope"] = true
		if _, err := q.ResolveLimitScope(c, queries.ResolveLimitScopeParams{Provider: "no-such-provider", Model: ""}); !errors.Is(err, store.ErrNoRows) {
			fail("a missing provider: %v", err)
		}
		called["CreateLimitRule"] = true
		rule, err = q.CreateLimitRule(c, queries.CreateLimitRuleParams{SubjectKind: "tenant", Metric: "cost_per_day", LimitValue: 250500000,
			Currency: "EUR", Enforce: false, Thresholds: []int16{80, 100}})
		if err != nil {
			return err
		}
		if rule.ID == "" || rule.Metric != "cost_per_day" || rule.LimitValue != 250500000 || rule.Currency != "EUR" || rule.Enforce || !reflect.DeepEqual(rule.Thresholds, []int16{80, 100}) {
			fail("CreateLimitRule: %+v", rule)
		}
		called["GetLimitRule"] = true
		got, err := q.GetLimitRule(c, rule.ID)
		if err != nil || got.ID != rule.ID || got.Currency != "EUR" {
			fail("GetLimitRule: %+v %v", got, err)
		}
		called["LockLimitRule"] = true
		if l, err := q.LockLimitRule(c, rule.ID); err != nil || l.ID != rule.ID {
			fail("LockLimitRule: %+v %v", l, err)
		}
		called["ListLimitRulesPage"] = true
		page, err := q.ListLimitRulesPage(c, queries.ListLimitRulesPageParams{Metric: "cost_per_day", Sort: "created_at", AfterID: noID, PageSize: 10})
		if err != nil || len(page) != 1 || page[0].ID != rule.ID {
			fail("ListLimitRulesPage: %+v %v", page, err)
		}
		called["UpdateLimitRule"] = true
		up, err := q.UpdateLimitRule(c, queries.UpdateLimitRuleParams{ID: rule.ID, LimitValue: 300000000, Enforce: true, Thresholds: []int16{}})
		if err != nil || up.LimitValue != 300000000 || !up.Enforce || len(up.Thresholds) != 0 {
			fail("UpdateLimitRule: %+v %v", up, err)
		}
		if n, err := q.CountLimitRules(c); err != nil || n != before+1 {
			fail("CountLimitRules after the insert: %d %v, want %d", n, err, before+1)
		}
		called["DeleteLimitRule"] = true
		del, err := q.DeleteLimitRule(c, rule.ID)
		if err != nil || del.ID != rule.ID || del.LimitValue != 300000000 {
			fail("DeleteLimitRule: %+v %v", del, err)
		}
		if n, err := q.CountLimitRules(c); err != nil || n != before {
			fail("CountLimitRules after the delete: %d %v, want %d", n, err, before)
		}
		return nil
	}))

	var platform queries.CreatePlatformLimitRuleRow
	must("platform ceilings administered", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		called["CreatePlatformLimitRule"] = true
		var err error
		platform, err = q.CreatePlatformLimitRule(c, queries.CreatePlatformLimitRuleParams{Metric: "tokens_per_month", LimitValue: 5000000, Enforce: true, Thresholds: []int16{90}})
		if err != nil {
			return err
		}
		if platform.ID == "" || platform.TenantID != nil || platform.Metric != "tokens_per_month" || !reflect.DeepEqual(platform.Thresholds, []int16{90}) {
			fail("CreatePlatformLimitRule: %+v", platform)
		}
		called["GetPlatformLimitRule"] = true
		if g, err := q.GetPlatformLimitRule(c, platform.ID); err != nil || g.ID != platform.ID {
			fail("GetPlatformLimitRule: %+v %v", g, err)
		}
		called["LockPlatformLimitRule"] = true
		if l, err := q.LockPlatformLimitRule(c, platform.ID); err != nil || l.ID != platform.ID {
			fail("LockPlatformLimitRule: %+v %v", l, err)
		}
		called["ListPlatformLimitRulesPage"] = true
		page, err := q.ListPlatformLimitRulesPage(c, queries.ListPlatformLimitRulesPageParams{AppliesTo: "default", Metric: "tokens_per_month", Sort: "-created_at", AfterID: noID, PageSize: 10})
		if err != nil || len(page) != 1 || page[0].ID != platform.ID {
			fail("ListPlatformLimitRulesPage: %+v %v", page, err)
		}
		return nil
	}))

	must("a tenant sees the ceiling and records an event", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		called["InsertLimitEvent"] = true
		start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		ev := queries.InsertLimitEventParams{Kind: "threshold_reached", RuleID: platform.ID, RuleLevel: "platform", SubjectKind: "tenant", Metric: "tokens_per_month",
			PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0), Percent: 90, LimitValue: 5000000, Observed: "4500000"}
		for i, want := range []int64{1, 0} { // the second insert of the same event is left alone
			if n, err := q.InsertLimitEvent(c, ev); err != nil || n != want {
				fail("InsertLimitEvent %d: %d %v, want %d", i, n, err, want)
			}
		}
		return nil
	}))
	must("the ceiling and the event are read", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		called["ListTenantPlatformLimitRules"] = true
		seen, err := q.ListTenantPlatformLimitRules(c, queries.ListTenantPlatformLimitRulesParams{AfterID: noID, PageSize: 50})
		if err != nil {
			return err
		}
		found := false
		for _, r := range seen {
			found = found || (r.ID == platform.ID && r.IsDefault)
		}
		if !found {
			fail("ListTenantPlatformLimitRules does not show the default: %+v", seen)
		}
		called["ListLimitEvents"] = true
		evs, err := q.ListLimitEvents(c, queries.ListLimitEventsParams{Kind: "threshold_reached", RuleID: platform.ID, AfterID: noID, PageSize: 10})
		if err != nil || len(evs) != 1 || evs[0].Percent != 90 || evs[0].Observed != "4500000.000000000000000000" {
			fail("ListLimitEvents: %+v %v", evs, err)
		}
		called["AggregateUsageForLimits"] = true
		now := time.Now().UTC()
		if _, err := q.AggregateUsageForLimits(c, queries.AggregateUsageForLimitsParams{LiveFrom: now.Add(-time.Hour), FromTime: now.Add(-24 * time.Hour), ToTime: now}); err != nil {
			return err
		}
		return nil
	}))

	must("the ceiling is changed and removed", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		called["UpdatePlatformLimitRule"] = true
		up, err := q.UpdatePlatformLimitRule(c, queries.UpdatePlatformLimitRuleParams{ID: platform.ID, LimitValue: 6000000, Enforce: false, Thresholds: []int16{50}})
		if err != nil || up.LimitValue != 6000000 || up.Enforce {
			fail("UpdatePlatformLimitRule: %+v %v", up, err)
		}
		called["DeletePlatformLimitRule"] = true
		del, err := q.DeletePlatformLimitRule(c, platform.ID)
		if err != nil || del.ID != platform.ID {
			fail("DeletePlatformLimitRule: %+v %v", del, err)
		}
		return nil
	}))
}

// integrityStatements covers the statements of sprint 5.3: the time zone of a tenant, the rules of
// a subject that goes away, the unlimited-traffic record and the currencies of a scope. Each is
// called once on rows that do not exist or on the tenant itself; what they do is the subject of
// the tests of the sprint.
func integrityStatements(c context.Context, t *testing.T, admin, gateway *store.DB, tid tenant.ID, called map[string]bool) {
	t.Helper()
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("time zone and rules of a subject", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		called["GetTenantTimeZone"] = true
		z, err := q.GetTenantTimeZone(c)
		if err != nil || z.TimeZone != "" || z.TimeZonePrevious != "" || z.TimeZoneEffectiveFrom != nil {
			t.Errorf("GetTenantTimeZone: %+v %v", z, err)
		}
		called["LockTenantTimeZone"] = true
		lock, err := q.LockTenantTimeZone(c, tid.String())
		if err != nil || lock.ID != tid.String() || lock.DbNow.IsZero() {
			t.Errorf("LockTenantTimeZone: %+v %v", lock, err)
		}
		called["SetTenantTimeZone"] = true
		set, err := q.SetTenantTimeZone(c, queries.SetTenantTimeZoneParams{ID: tid.String(), TimeZone: "", TimeZonePrevious: ""})
		if err != nil || set.TimeZone != "" {
			t.Errorf("SetTenantTimeZone: %+v %v", set, err)
		}
		called["DeleteLimitRulesOfSubject"] = true
		gone, err := q.DeleteLimitRulesOfSubject(c, queries.DeleteLimitRulesOfSubjectParams{KeyID: "tnosuchkeyid000000"})
		if err != nil || len(gone) != 0 {
			t.Errorf("DeleteLimitRulesOfSubject: %+v %v", gone, err)
		}
		called["ListPriceCurrenciesForScope"] = true
		if _, err := q.ListPriceCurrenciesForScope(c, queries.ListPriceCurrenciesForScopeParams{VisibleOnly: true}); err != nil {
			t.Errorf("ListPriceCurrenciesForScope: %v", err)
		}
		return nil
	}))
	must("platform issuer rules", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		called["DeletePlatformIssuerLimitRules"] = true
		n, err := queries.New(tx).DeletePlatformIssuerLimitRules(c, queries.DeletePlatformIssuerLimitRulesParams{
			IssuerID: "0192f0f0-0000-7000-8000-0000000000ff", ActorID: "a", ActorName: "a", KeyID: "k", RequestID: strings.Repeat("0", 32)})
		if err != nil || n != 0 {
			t.Errorf("DeletePlatformIssuerLimitRules: %d %v", n, err)
		}
		return nil
	}))
	must("unlimited traffic", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		called["RecordUnlimitedTraffic"] = true
		day := pgtype.Date{Time: time.Now().UTC().Truncate(24 * time.Hour), Valid: true}
		first, err := queries.New(tx).RecordUnlimitedTraffic(c, queries.RecordUnlimitedTrafficParams{
			Day: day, TimeZone: "UTC", Provider: "openai", Model: "gpt-4o", RequestID: strings.Repeat("1", 32)})
		if err != nil || !first {
			t.Errorf("RecordUnlimitedTraffic: %v %v", first, err)
		}
		return nil
	}))
}
