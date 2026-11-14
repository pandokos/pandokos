package catalog_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// TestStoredReportFitsTheColumn: the CHECK of price_import.report bounds the jsonb value, which is
// larger than the JSON text; a report of any number of models is cut to fit and the import row is
// written (finding 1 of the review).
func TestStoredReportFitsTheColumn(t *testing.T) {
	e := newEnv(t)
	e.sql(`INSERT INTO pandokos.model (provider_id, name, upstream_id)
	       SELECT p.id, 'synthetic-unmatched-model-with-a-rather-long-name-' || g, 'x-' || g
	         FROM pandokos.provider p, generate_series(1, 1500) g WHERE p.name = 'synthetic'`)
	for i := 0; i < 2; i++ {
		rep, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: fixtureFile(t, "base.json")}, catalog.ImportOptions{Actor: cliActor})
		if err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
		if rep.Unmatched < 1500 {
			t.Fatalf("unmatched: %d", rep.Unmatched)
		}
		var doc string
		var size int
		if err := e.scan(`SELECT report::text, pg_column_size(report) FROM pandokos.price_import WHERE id = $1::uuid`, rep.ImportID)(&doc, &size); err != nil {
			t.Fatal(err)
		}
		var r struct {
			Items     []json.RawMessage `json:"items"`
			Truncated int               `json:"truncated"`
		}
		if err := json.Unmarshal([]byte(doc), &r); err != nil {
			t.Fatal(err)
		}
		if r.Truncated == 0 || len(r.Items) == 0 || len(r.Items)+r.Truncated != len(rep.Items) {
			t.Errorf("import %d: %d items stored, %d truncated, %d in the report", i, len(r.Items), r.Truncated, len(rep.Items))
		}
		if size > 65536 {
			t.Errorf("the stored report is %d bytes", size)
		}
		if !strings.Contains(doc, `"truncated"`) {
			t.Error("no truncated count")
		}
	}
}

// TestImportVersionStartsAfterTheLock: a transaction that started before another one committed a
// version, but took the import lock after it, must not insert a version that starts before it: the
// clock of the new version is read after the lock (finding 2 of the review).
func TestImportVersionStartsAfterTheLock(t *testing.T) {
	e := newEnv(t, "synthetic-chat")
	var modelID string
	if err := e.scan(`SELECT id::text FROM pandokos.model WHERE name = 'synthetic-chat'`)(&modelID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	err := e.admin.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if err := q.LockPriceImports(c); err != nil {
			return err
		}
		go func() {
			_, err := catalog.Import(context.Background(), e.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: fixtureFile(t, "base.json")}, catalog.ImportOptions{Actor: cliActor})
			done <- err
		}()
		// The import's transaction starts now and waits for the lock.
		time.Sleep(500 * time.Millisecond)
		v, err := q.CreatePriceVersion(c, queries.CreatePriceVersionParams{ModelID: modelID, Currency: "USD", Source: "override", ValidFrom: ptr(time.Now())})
		if err != nil {
			return err
		}
		_, err = q.CreatePriceRates(c, queries.CreatePriceRatesParams{PriceVersionID: v.ID, Categories: []string{"input"}, Thresholds: []int64{0}, Rates: []string{"9"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	vs := e.versions(t, "synthetic", "synthetic-chat")
	if len(vs) != 2 || vs[0].Source != "override" || vs[1].Source != "import" {
		t.Fatalf("versions: %+v (the version of the import must start after the one that committed first)", vs)
	}
	var source string
	if err := e.scan(`SELECT (SELECT w.source FROM pandokos.price_version w WHERE w.model_id = $1::uuid AND w.valid_from <= now() ORDER BY w.valid_from DESC LIMIT 1)`, modelID)(&source); err != nil || source != "import" {
		t.Errorf("the version in force is %q (%v), want the import's", source, err)
	}
}

func ptr[T any](v T) *T { return &v }
