package gateway_test

import (
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestScopeByIdentifier is T-711: a rule that names a model limits that model, by its identifier. With
// the rule deleted, the model deleted and a model created with the same name, a gateway that still
// holds the old rule set does not limit the new model, and a new rule on the new model starts from
// the new model's usage.
func TestScopeByIdentifier(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tn := d.SeedTenant(t, "scope-id")
	m1 := d.SeedModel(t, "openai", "fake-basic")
	oa := d.ProviderID(t, "openai")
	d.SeedLimitRule(t, tn, "tenant", "", "requests_per_day", 1, dbtest.LimitOptions{Provider: "openai", Model: "fake-basic"})
	s := newQuotaStack(t, d, tn, stackOptions{ledger: true})
	target := func(model string) {
		t.Helper()
		tgt := s.h.targets["openai/fake-basic"]
		tgt.ProviderID, tgt.ModelID = oa, model
		s.h.targets["openai/fake-basic"] = tgt
	}
	target(m1)

	// The rule limits the model it names.
	s.ok(t, "a", "fake-basic")
	if c := s.call(t, "a", "openai", "fake-basic"); c.status != 429 {
		t.Fatalf("the second request under a rule of one a day: %d %s", c.status, c.body)
	}
	testutil.Eventually(t, 5*time.Second, "the record of the first request", func() bool { return s.count(t, "usage_record") >= 2 })

	// The rule is deleted, the model is deleted, a model of the same name is created. The gateway does
	// not hear of it: its cache holds the rule set of the old model until its lifetime ends.
	d.DeleteLimitRule(t, tn, "tenant", "", "requests_per_day") // the scoped rule is the only one of its subject and metric
	if _, err := d.Super(t).Exec(ctx5(t), `DELETE FROM pandokos.model WHERE id = $1::uuid`, m1); err != nil {
		t.Fatal(err)
	}
	m2 := d.SeedModel(t, "openai", "fake-basic")
	if m1 == m2 {
		t.Fatal("the model created again has the old ID")
	}
	target(m2)
	for i := 0; i < 2; i++ {
		if c := s.call(t, "a", "openai", "fake-basic"); c.status != 200 {
			t.Fatalf("request %d for the new model: %d %s: the rule of the old model, which the cache still holds, limited a model of the same name", i+1, c.status, c.body)
		}
	}

	// A rule on the new model counts the new model's records only: two of them, none of the old model's.
	testutil.Eventually(t, 5*time.Second, "the records of the new model", func() bool { return s.count(t, "usage_record") >= 4 })
	d.SeedLimitRule(t, tn, "tenant", "", "requests_per_day", 3, dbtest.LimitOptions{Provider: "openai", Model: "fake-basic"})
	s.cache.Flush()
	if c := s.call(t, "a", "openai", "fake-basic"); c.status != 200 {
		t.Fatalf("the third request of the new model under a limit of 3: %d %s: the rows of the old model were counted", c.status, c.body)
	}
	if c := s.call(t, "a", "openai", "fake-basic"); c.status != 429 {
		t.Errorf("the fourth: %d", c.status)
	}
}
