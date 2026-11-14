package catalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// TestPriceAtDB is T-395 (exit condition 2): a cost computed for a past instant uses the version
// valid then, on the database and in the gateway cache's snapshot.
func TestPriceAtDB(t *testing.T) {
	e := newEnv(t, "synthetic-timed")
	var modelID string
	if err := e.scan(`SELECT id::text FROM pandokos.model WHERE name = 'synthetic-timed'`)(&modelID); err != nil {
		t.Fatal(err)
	}

	// Three versions in sequence, in real time: each starts at the moment of its transaction.
	var starts []time.Time
	for i, rate := range []string{"1", "2", "3"} {
		if i > 0 {
			time.Sleep(25 * time.Millisecond)
		}
		if err := e.admin.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			v, err := q.CreatePriceVersion(c, queries.CreatePriceVersionParams{ModelID: modelID, Currency: "USD", Source: "override"})
			if err != nil {
				return err
			}
			if _, err := q.CreatePriceRates(c, queries.CreatePriceRatesParams{PriceVersionID: v.ID, Categories: []string{"input"}, Thresholds: []int64{0}, Rates: []string{rate}}); err != nil {
				return err
			}
			starts = append(starts, v.ValidFrom)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !starts[0].Before(starts[1]) || !starts[1].Before(starts[2]) {
		t.Fatalf("the versions do not start in order: %v", starts)
	}

	// One million input tokens cost the rate of the version in force, in USD.
	usage := family.Usage{InputTokens: 1_000_000}
	at := func(name string, when time.Time, want string) {
		t.Helper()
		var v pricing.Version
		var found bool
		err := e.admin.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
			var err error
			v, found, err = catalog.PriceAt(c, tx, modelID, when)
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cost := pricing.CostOf(usage, v, found)
		if want == "" {
			if found || cost.Known || cost.Reason != "no_price" {
				t.Errorf("%s: found %v, cost %+v, want an unknown cost", name, found, cost)
			}
			return
		}
		if !found || !cost.Known || cost.Amount.String() != want || cost.Currency != "USD" || v.Source != "override" || v.ModelID != modelID || len(v.Rates) != 1 {
			t.Errorf("%s: found %v, version %+v, cost %+v, want %s", name, found, v, cost, want)
		}
	}
	mid := func(a, b time.Time) time.Time { return a.Add(b.Sub(a) / 2) }
	us := time.Microsecond
	for _, c := range []struct {
		name string
		when time.Time
		want string
	}{
		{"a microsecond before the first version", starts[0].Add(-us), ""},
		{"long before", starts[0].Add(-48 * time.Hour), ""},
		{"on the first", starts[0], "1"},
		{"between the first and the second", mid(starts[0], starts[1]), "1"},
		{"a microsecond before the second", starts[1].Add(-us), "1"},
		{"on the second", starts[1], "2"},
		{"between the second and the third", mid(starts[1], starts[2]), "2"},
		{"a microsecond before the third", starts[2].Add(-us), "2"},
		{"on the third", starts[2], "3"},
		{"a microsecond after the third", starts[2].Add(us), "3"},
		{"an hour later", starts[2].Add(time.Hour), "3"},
		{"now", time.Now(), "3"},
		{"in another zone", starts[1].In(time.FixedZone("x", -9*3600)), "2"},
	} {
		at(c.name, c.when, c.want)
	}

	// The cache's snapshot gives the same version at every instant.
	gateway := e.d.Open(t, store.RoleGateway)
	snap, err := catalog.LoadSnapshot(ctx(t), gateway)
	if err != nil {
		t.Fatal(err)
	}
	versions := snap.Versions(modelID)
	if len(versions) != 3 {
		t.Fatalf("the snapshot holds %d versions, want 3", len(versions))
	}
	for i := 1; i < len(versions); i++ {
		if !versions[i].ValidFrom.After(versions[i-1].ValidFrom) {
			t.Errorf("the versions of the snapshot are not ordered by valid_from: %v", versions)
		}
	}
	instants := []time.Time{starts[0].Add(-us), starts[0], mid(starts[0], starts[1]), starts[1].Add(-us), starts[1], mid(starts[1], starts[2]), starts[2].Add(-us), starts[2], starts[2].Add(time.Hour), time.Now()}
	for i, when := range instants {
		var fromDB pricing.Version
		var foundDB bool
		if err := e.admin.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
			var err error
			fromDB, foundDB, err = catalog.PriceAt(c, tx, modelID, when)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		fromCache, foundCache := pricing.VersionAt(versions, when)
		if foundDB != foundCache || fromDB.ID != fromCache.ID || (foundDB && (fromDB.Currency != fromCache.Currency || !fromDB.SameRates(fromCache) || !fromDB.ValidFrom.Equal(fromCache.ValidFrom))) {
			t.Errorf("instant %d (%v): the database says %+v (%v), the cache %+v (%v)", i, when, fromDB, foundDB, fromCache, foundCache)
		}
	}

	t.Run("a model with no version, and a future version that has not started", func(t *testing.T) {
		var other string
		e.sql(`INSERT INTO pandokos.model (provider_id, name, upstream_id) SELECT id, 'synthetic-unpriced', 'synthetic-unpriced' FROM pandokos.provider WHERE name = 'synthetic'`)
		if err := e.scan(`SELECT id::text FROM pandokos.model WHERE name = 'synthetic-unpriced'`)(&other); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
			if _, found, err := catalog.PriceAt(c, tx, other, time.Now()); err != nil || found {
				t.Errorf("a model without versions: found %v, %v", found, err)
			}
			future := time.Now().Add(time.Hour)
			q := queries.New(tx)
			v, err := q.CreatePriceVersion(c, queries.CreatePriceVersionParams{ModelID: other, ValidFrom: &future, Currency: "EUR", Source: "override"})
			if err != nil {
				return err
			}
			if _, err := q.CreatePriceRates(c, queries.CreatePriceRatesParams{PriceVersionID: v.ID, Categories: []string{"input"}, Thresholds: []int64{0}, Rates: []string{"9"}}); err != nil {
				return err
			}
			if _, found, err := catalog.PriceAt(c, tx, other, time.Now()); err != nil || found {
				t.Errorf("a version that has not started is in force: found %v, %v", found, err)
			}
			if v, found, err := catalog.PriceAt(c, tx, other, future.Add(time.Minute)); err != nil || !found || v.Currency != "EUR" {
				t.Errorf("after it starts: %+v %v %v", v, found, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
