//go:build drill

package drill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This file holds what runs inside the containers: the tools of the guide (pg_dump, pg_restore,
// pg_basebackup, psql) are run with docker compose exec in the PostgreSQL service where the data is,
// so the host needs Docker and Go only (D-4).

// sh runs a shell script in a service, in the directory /work where the repository's deploy
// directory is mounted (the paths of the guide are relative to the repository), as root, and
// returns what it printed. Every output goes through the canaries and is shown redacted.
func (w *World) sh(ctx context.Context, service, script string) (string, error) {
	return w.shAs(ctx, service, "", "", script)
}

// shAs is sh as a user of the container ("" for the container's own, root): the user is named, never
// the uid of whoever runs the tests on the host.
func (w *World) shAs(ctx context.Context, service, user, stdin, script string) (string, error) {
	out, err := w.P.Exec(ctx, service, user, stdin, "sh", "-c", "set -e\ncd /work\n"+script)
	w.Canaries.Scan(w.T, "output of "+service+": "+firstLine(script), []byte(out))
	return out, err
}

// run is sh that fails the test when the script fails.
func (w *World) run(service, script string) string {
	w.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := w.sh(ctx, service, script)
	if err != nil {
		w.T.Fatalf("%s: %v\n%s", firstLine(script), err, w.redact(tail(out)))
	}
	return out
}

// sql runs statements as the superuser in a database of a service and returns the rows as printed by
// psql -At (columns separated by |). The statements travel on the standard input, never in an
// argument, and a failure is reported without the text of the error, which may quote a statement.
func (w *World) sql(service, db, statements string) string {
	w.T.Helper()
	out, err := w.trySQL(service, db, statements)
	if err != nil {
		w.T.Fatalf("psql on %s failed: %v\n%s", service, err, w.redact(tail(out)))
	}
	return out
}

func (w *World) trySQL(service, db, statements string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := w.P.Exec(ctx, service, "", statements, "psql", "-X", "-q", "-At", "-U", "postgres", "-d", db, "-v", "ON_ERROR_STOP=1", "-f", "-")
	w.Canaries.Scan(w.T, "output of psql on "+service, []byte(out))
	return strings.TrimSpace(out), err
}

// sqlSecret is sql for a statement that holds a secret (the password of a login user): on failure
// nothing of psql's text is shown.
func (w *World) sqlSecret(service, db, statements string) {
	w.T.Helper()
	if _, err := w.trySQL(service, db, statements); err != nil {
		w.T.Fatalf("psql on %s failed (its text is withheld: the statement holds a password): %v", service, err)
	}
}

// count is a query with one integer.
func (w *World) count(service, db, query string) int64 {
	w.T.Helper()
	out := w.sql(service, db, query)
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		w.T.Fatalf("the query %q printed %q", firstLine(query), out)
	}
	return n
}

// redact replaces every secret the world knows in a text, for a message.
func (w *World) redact(s string) string {
	for _, k := range w.knownSecrets() {
		if k != "" {
			s = strings.ReplaceAll(s, k, "[secret]")
		}
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// withDatabase replaces the placeholder of the guide's commands.
func withDatabase(cmd, db string) string { return strings.ReplaceAll(cmd, "<database>", db) }

// waitPromoted waits until a service accepts connections and is not in recovery: a server recovering
// from a base backup answers pg_isready before it has been promoted.
func (w *World) waitPromoted(service string, within time.Duration) error {
	deadline := time.Now().Add(within)
	var last string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, err := w.P.Exec(ctx, service, "", "", "psql", "-X", "-At", "-h", "127.0.0.1", "-U", "postgres", "-d", "postgres", "-c", "SELECT pg_is_in_recovery()")
		cancel()
		last = strings.TrimSpace(out)
		if err == nil && last == "f" {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("%s was not promoted within %s (last answer %q)", service, within, tail(last))
}

// walName returns the name of the WAL file of the current position of the source.
func (w *World) walName() string {
	return w.sql(Source, "postgres", `SELECT pg_walfile_name(pg_current_wal_lsn())`)
}

// switchAndArchive makes a restore point, switches the WAL and waits until the file that held the
// point is in the archive: what the restore reads must be there before the server that wrote it goes.
func (w *World) switchAndArchive(name string) {
	w.T.Helper()
	w.sql(Source, "postgres", fmt.Sprintf(`SELECT pg_create_restore_point('%s')`, name))
	file := w.walName()
	w.sql(Source, "postgres", `SELECT pg_switch_wal()`)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		last := w.sql(Source, "postgres", `SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver`)
		if last >= file && !strings.HasSuffix(last, ".history") && last != "" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	w.T.Fatalf("the WAL file %s was not archived within 2 minutes", file)
}

// shareBackup makes everything under /backup readable and removable by the user who runs the
// tests: the tools write as the database's user and as root, in directories of mode 0700.
func (p *Project) shareBackup(ctx context.Context) {
	for _, s := range []string{Source, Target, TargetNext} {
		_, _ = p.Exec(ctx, s, "", "", "chmod", "-R", "a+rwX", "/backup")
	}
}

// dirSize is the number of bytes under a directory.
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}
