//go:build drill

package drill

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// dbService is the server that holds the database of the case now: the restored one, or the source.
func (w *World) dbService() string {
	if w.restored != nil {
		return w.restored.service
	}
	return Source
}

// Finish records the case id in the report of the run: outcome, the steps since the last case of this
// world, versions, sizes. A world that holds several cases (the two physical ones) finishes each.
func (w *World) Finish(id string) {
	w.T.Helper()
	all := w.Timer.Steps()
	c := CaseReport{ID: id, Outcome: Pass, NotRun: w.Case.NotRun, Steps: append([]StepReport(nil), all[w.reported:]...)}
	w.reported = len(all)
	if w.T.Failed() {
		c.Outcome = Fail
	}
	svc := w.dbService()
	c.Versions = Versions{Binary: w.Bin.Version, PostgreSQL: w.sql(svc, "postgres", `SHOW server_version`)}
	if w.service != nil {
		c.Versions.KeyService = w.service.version
	}
	c.Sizes = Sizes{
		LedgerRows:  w.count(svc, w.db, `SELECT count(*) FROM pandokos.usage_record`),
		AuditRows:   w.count(svc, w.db, `SELECT (SELECT count(*) FROM pandokos.audit_event) + (SELECT count(*) FROM pandokos.platform_audit_event)`),
		BackupBytes: w.backupBytes(),
	}
	w.Case.NotRun = ""
	Collect(c)
}

// finalCheck is the last check of every case, run before its containers go: no secret is in what the
// servers logged. Every other output of the case was scanned as it was produced.
func (w *World) finalCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := w.P.compose(ctx, "--profile", "*", "logs", "--no-color")
	if err != nil && !strings.Contains(out, "no such") {
		w.T.Logf("the logs of the servers could not be read for the final check: %v", err)
	}
	w.Canaries.Scan(w.T, "the logs of the servers of the case", []byte(out))
	w.finalScans++
	_ = fmt.Sprint
}

// phase logs the time a phase of the case took, for the people who tune the suite.
func (w *World) phase(name string) func() {
	start := time.Now()
	return func() { w.T.Logf("phase %s: %s", name, time.Since(start).Round(100*time.Millisecond)) }
}

// backupBytes is the size of what the case backed up: the dumps, and the base backup with the WAL
// archive when the case took one. The archive of a source that is not backed up physically is not a
// backup.
func (w *World) backupBytes() int64 {
	n := dirSize(filepath.Join(w.P.Backup, "dumps"))
	if w.baseTaken {
		n += dirSize(filepath.Join(w.P.Backup, "base")) + dirSize(filepath.Join(w.P.Backup, "wal"))
	}
	return n
}
