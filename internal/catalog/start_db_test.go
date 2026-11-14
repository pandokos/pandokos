package catalog_test

import (
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestImportAtStart is T-396: the embedded snapshot is imported once per digest, never over a
// newer file or download, a failure is reported and not returned, and two instances starting
// together import once.
func TestImportAtStart(t *testing.T) {
	logger := func() (*slog.Logger, *testutil.LogBuffer) {
		logs := &testutil.LogBuffer{}
		return telemetry.NewLogger(logs, "debug"), logs
	}

	t.Run("once per digest", func(t *testing.T) {
		e := newEnv(t)
		l, logs := logger()
		catalog.ImportAtStart(ctx(t), e.worker, l)
		imported := logs.Find(t, "prices imported")
		if len(imported) != 1 || imported[0]["source"] != "embedded" || imported[0]["import_id"] == "" || imported[0]["digest"] == "" || imported[0]["created"] != float64(17) || imported[0]["level"] != "INFO" {
			t.Fatalf("log lines: %v\n%s", imported, logs.String())
		}
		for _, f := range []string{"unchanged", "overridden", "unmatched", "skipped", "duration_ms"} {
			if _, ok := imported[0][f]; !ok {
				t.Errorf("the line lacks %s: %v", f, imported[0])
			}
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 17 {
			t.Errorf("%d versions", n)
		}
		if got := actions(e.audit(t), "price.create"); len(got) != 17 || got[0].ActorName != "price-import-at-start" || got[0].ActorType != "host" {
			t.Errorf("audit rows of the start-up import: %d, %+v", len(got), got)
		}

		// Another start: the same digest was imported, so it is skipped and says why.
		l, logs = logger()
		catalog.ImportAtStart(ctx(t), e.worker, l)
		skipped := logs.Find(t, "price import skipped")
		if len(skipped) != 1 || skipped[0]["reason"] != "digest_seen" || skipped[0]["source"] != "embedded" || len(logs.Find(t, "prices imported")) != 0 {
			t.Errorf("second start: %s", logs.String())
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_import`); n != 1 {
			t.Errorf("%d import rows after a skipped start", n)
		}
	})

	t.Run("never over a newer file import", func(t *testing.T) {
		e := newEnv(t)
		if _, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: fixtureFile(t, "base.json")}, catalog.ImportOptions{Actor: cliActor}); err != nil {
			t.Fatal(err)
		}
		l, logs := logger()
		catalog.ImportAtStart(ctx(t), e.worker, l)
		skipped := logs.Find(t, "price import skipped")
		if len(skipped) != 1 || skipped[0]["reason"] != "newer_import" || len(logs.Find(t, "prices imported")) != 0 {
			t.Errorf("start after a file import: %s", logs.String())
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 4 {
			t.Errorf("the start-up import went over the file: %d versions", n)
		}
		// A file import that is older than the snapshot does not stop it: the row is dated before the snapshot.
		e2 := newEnv(t)
		if _, err := catalog.Import(ctx(t), e2.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: fixtureFile(t, "base.json")}, catalog.ImportOptions{Actor: cliActor}); err != nil {
			t.Fatal(err)
		}
		// The tables are append-only for every role, the owner included: the date is moved by dropping the guard for this connection.
		e2.sql(`ALTER TABLE pandokos.price_import DISABLE TRIGGER price_import_append_only`)
		e2.sql(`UPDATE pandokos.price_import SET created_at = created_at - interval '30 days'`)
		e2.sql(`ALTER TABLE pandokos.price_import ENABLE TRIGGER price_import_append_only`)
		l, logs = logger()
		catalog.ImportAtStart(ctx(t), e2.worker, l)
		if len(logs.Find(t, "prices imported")) != 1 {
			t.Errorf("start after an old file import: %s", logs.String())
		}
	})

	t.Run("a download counts like a file, an embedded import of another digest does not", func(t *testing.T) {
		e := newEnv(t)
		e.sql(`INSERT INTO pandokos.price_import (started_at, source, source_ref, dataset_format, digest, outcome, actor_type)
			VALUES (now(), 'remote', 'https://prices.example.com/data.json', 'genai-prices-v2', repeat('a', 64), 'success', 'host')`)
		l, logs := logger()
		catalog.ImportAtStart(ctx(t), e.worker, l)
		if s := logs.Find(t, "price import skipped"); len(s) != 1 || s[0]["reason"] != "newer_import" {
			t.Errorf("start after a download: %s", logs.String())
		}
		e = newEnv(t)
		e.sql(`INSERT INTO pandokos.price_import (started_at, source, dataset_format, digest, outcome, actor_type)
			VALUES (now(), 'embedded', 'genai-prices-v2', repeat('b', 64), 'success', 'host')`)
		l, logs = logger()
		catalog.ImportAtStart(ctx(t), e.worker, l)
		if len(logs.Find(t, "prices imported")) != 1 {
			t.Errorf("start after an embedded import of another release: %s", logs.String())
		}
		// A failed run is not an import that happened.
		e = newEnv(t)
		e.sql(`INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, error_class, actor_type)
			VALUES (now(), 'remote', 'genai-prices-v2', 'failure', 'status', 'host')`)
		l, logs = logger()
		catalog.ImportAtStart(ctx(t), e.worker, l)
		if len(logs.Find(t, "prices imported")) != 1 {
			t.Errorf("start after a failed download: %s", logs.String())
		}
	})

	t.Run("a failure is reported and not returned", func(t *testing.T) {
		e := newEnv(t)
		e.sql(`CREATE FUNCTION pandokos.zz_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit is down'; END $$`)
		e.sql(`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.platform_audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail()`)
		l, logs := logger()
		catalog.ImportAtStart(ctx(t), e.worker, l) // returns nothing, does not panic
		failed := logs.Find(t, "price import failed")
		if len(failed) != 1 || failed[0]["level"] != "WARN" || failed[0]["source"] != "embedded" || failed[0]["error_class"] == "" || failed[0]["error_class"] == nil {
			t.Fatalf("log: %s", logs.String())
		}
		if _, ok := failed[0]["duration_ms"]; !ok {
			t.Errorf("the line lacks duration_ms: %v", failed[0])
		}
		testutil.AssertAbsent(t, "the log", logs.String(), "audit is down")
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 0 {
			t.Errorf("a failed start-up import left %d versions", n)
		}
		// The database being out of reach is a failure too.
		e.d.SetAcceptingConnections(t, false)
		l, logs = logger()
		catalog.ImportAtStart(ctx(t), e.worker, l)
		if len(logs.Find(t, "price import failed")) < 1 {
			t.Errorf("a database that is not there: %s", logs.String())
		}
		e.d.SetAcceptingConnections(t, true)
	})

	t.Run("two instances starting together import once", func(t *testing.T) {
		e := newEnv(t)
		w2 := e.d.Open(t, store.RoleWorker)
		var wg sync.WaitGroup
		bufs := make([]*testutil.LogBuffer, 2)
		start := make(chan struct{})
		for i, db := range []*store.DB{e.worker, w2} {
			wg.Add(1)
			bufs[i] = &testutil.LogBuffer{}
			go func() {
				defer wg.Done()
				<-start
				catalog.ImportAtStart(ctx(t), db, telemetry.NewLogger(bufs[i], "debug"))
			}()
		}
		close(start)
		wg.Wait()
		imported, skipped := 0, 0
		for _, b := range bufs {
			imported += len(b.Find(t, "prices imported"))
			skipped += len(b.Find(t, "price import skipped"))
		}
		if imported != 1 || skipped != 1 {
			t.Errorf("%d imports and %d skips: %s / %s", imported, skipped, bufs[0].String(), bufs[1].String())
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 17 {
			t.Errorf("%d versions", n)
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_import WHERE source = 'embedded'`); n != 1 {
			t.Errorf("%d embedded import rows", n)
		}
	})

	t.Run("a quiet logger is enough", func(t *testing.T) {
		e := newEnv(t)
		catalog.ImportAtStart(ctx(t), e.worker, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 17 {
			t.Errorf("%d versions", n)
		}
	})
}
