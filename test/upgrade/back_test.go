//go:build upgrade

package upgrade

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/instance"
)

// quietUpgrade upgrades the world without traffic, by the procedure of the guide, and returns the
// procedure, the dump it took before migrate and the new process.
func quietUpgrade(t *testing.T, w *world) (*Procedure, string, *instance.Process) {
	t.Helper()
	p := &Procedure{Old: w.inst.Bin, New: instance.Build(t), DB: w.db, Env: w.env, Canaries: w.inst.Canaries}
	p.Check(t)
	dump := p.Backup(t)
	p.Stop(t, w.proc)
	p.Migrate(t)
	return p, dump, p.Start(t)
}

// fingerprint is the number of rows of every table of the schema, read as the superuser: what the
// going-back test compares to say that the upgraded database was not touched.
func fingerprint(t *testing.T, d *dbtest.Database) string {
	t.Helper()
	conn := d.Super(t)
	rows, err := conn.Query(testCtx(), `SELECT table_name FROM information_schema.tables WHERE table_schema = 'pandokos' AND table_type = 'BASE TABLE' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	sort.Strings(tables)
	var sb strings.Builder
	for _, name := range tables {
		var n int
		if err := conn.QueryRow(testCtx(), fmt.Sprintf(`SELECT count(*)::int FROM pandokos.%q`, name)).Scan(&n); err != nil {
			continue
		}
		fmt.Fprintf(&sb, "%s=%d\n", name, n)
	}
	return sb.String()
}

// TestGoingBackIsARestore is T-1132: going back from a minor is a restore of the backup taken before
// migrate, run by the old binary. The dump taken before migrate, restored into an empty database, is
// served by the previous binary and the credentials of its tenants open; and the upgraded database is
// unchanged by it. The schema it is restored to is the one the previous binary needs, derived from it
// (the one before and after the upgrade alike when no migration is crossed).
func TestGoingBackIsARestore(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	prev := neededVersion(t, w.inst.Bin)
	_, dump, newProc := quietUpgrade(t, w)
	if status := w.call(t, newProc, "c"); status != 200 {
		t.Fatalf("the upgraded installation: %d", status)
	}
	// The upgraded installation stops (it drains and flushes its ledger), so that its database is at
	// rest and the comparison after the restore means something.
	if code := newProc.Stop(t); code != 0 {
		t.Errorf("the upgraded instance exited %d", code)
	}
	upgradedBefore := fingerprint(t, w.db)

	// An empty database, the dump restored into it, and the previous binary on it with the key of the
	// installation (the key file is not in any dump).
	back := dbtest.New(t)
	Restore(t, dump, back)
	env := append(back.Env(t), "PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE="+w.db.KEKFile(t), "PANDOKOS_LOG_LEVEL=info", "PANDOKOS_LIMITS_TIME_ZONE="+w.zone)
	old := w.inst.Bin
	if code, out, errs := old.Run(t, env, "migrate", "status"); code != 0 || !strings.Contains(out, fmt.Sprintf("schema is at version %d;", prev)) {
		t.Fatalf("migrate status of the restored database by the previous binary, want schema version %d: exit %d\n%s%s", prev, code, out, errs)
	}
	proc := old.Serve(t, env)
	proc.Ready(t)

	// Every tenant's credential opens: the provider receives each tenant's own canary.
	for name, tn := range w.tenants {
		before := len(w.up.Requests())
		status, text := proc.Call(t, tn.InferenceKey, fakeprovider.ModelBasic)
		if status != 200 {
			t.Errorf("tenant %s on the restored installation: %d\n%s", name, status, text)
			continue
		}
		reqs := w.up.Requests()
		if len(reqs) != before+1 || reqs[len(reqs)-1].Header.Get("Authorization") != "Bearer "+tn.Credential {
			t.Errorf("tenant %s: the provider did not receive this tenant's credential", name)
		}
	}
	if got := fingerprint(t, w.db); got != upgradedBefore {
		t.Errorf("the upgraded database changed while the restored one was served:\n%s\nwas:\n%s", got, upgradedBefore)
	}
	if code := proc.Stop(t); code != 0 {
		t.Errorf("the previous binary exited %d", code)
	}
}

// call sends one inference call to a process with the key of a tenant of the world and returns the
// status.
func (w *world) call(t *testing.T, p *instance.Process, tenant string) int {
	t.Helper()
	status, _ := p.Call(t, w.tenants[tenant].InferenceKey, fakeprovider.ModelBasic)
	return status
}
