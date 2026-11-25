//go:build drill

package drill

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pandokos/pandokos/test/instance"
	"github.com/pandokos/pandokos/test/testutil"
)

// ---- what the guide's section 1 says to keep, and the backups

// readHead reads the head of a chain: "audit head" for a tenant, "platform-audit head" for the
// platform's, as JSON.
func (w *World) readHead(p *instance.Process, key string, args ...string) HeadRef {
	w.T.Helper()
	out := w.mustCLIOn(p, key, append(args, "-o", "json")...)
	var h struct {
		Seq  int64  `json:"seq"`
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal([]byte(out), &h); err != nil || h.Hash == "" {
		w.T.Fatalf("the head of a chain: %v\n%s", err, tail(out))
	}
	return HeadRef{Seq: h.Seq, Hash: h.Hash}
}

// readHeads reads the heads of the platform's chain and of every tenant's that still exists.
func (w *World) readHeads(p *instance.Process) Heads {
	w.T.Helper()
	h := Heads{Platform: w.readHead(p, w.platform, "platform-audit", "head"), Tenants: map[string]HeadRef{}}
	for _, name := range worldTenants {
		if w.deleted[name] {
			continue
		}
		tn := w.Tenants[name]
		if w.requested[name] {
			// A tenant whose deletion is requested is suspended: its own administrator is refused, and the
			// platform reads its chain.
			code, out, _ := p.CLI(w.T, w.platform, "audit", "head", "--tenant", tn.ID, "-o", "json")
			var hd struct {
				Seq  int64  `json:"seq"`
				Hash string `json:"hash"`
			}
			if code == 0 && json.Unmarshal([]byte(out), &hd) == nil && hd.Hash != "" {
				h.Tenants[name] = HeadRef{Seq: hd.Seq, Hash: hd.Hash}
			}
			continue
		}
		h.Tenants[name] = w.readHead(p, tn.AdminKeyFile, "audit", "head", "--tenant", tn.ID)
	}
	return h
}

// completedDeletions is "pandokos tenant deletion list --state completed -o json" (the guide's step 5.a):
// the IDs of the tenants whose deletion completed.
func (w *World) completedDeletions(p *instance.Process) []string {
	w.T.Helper()
	out := w.mustCLIOn(p, w.platform, "tenant", "deletion", "list", "--state", "completed", "--all", "-o", "json")
	var doc struct {
		Items []struct {
			TenantID string `json:"tenant_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		w.T.Fatalf("tenant deletion list: %v\n%s", err, tail(out))
	}
	var ids []string
	for _, i := range doc.Items {
		ids = append(ids, i.TenantID)
	}
	sort.Strings(ids)
	return ids
}

// deletionEvents is "pandokos platform-event export --type io.pandokos.tenant.deleted.v1" (the
// guide's step 5.b): the tenant IDs of the events, sorted.
func (w *World) deletionEvents(p *instance.Process) []string {
	w.T.Helper()
	out := w.mustCLIOn(p, w.platform, "platform-event", "export", "--type", "io.pandokos.tenant.deleted.v1")
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e struct {
			TenantID  string `json:"tenantid"`
			TenantRef string `json:"tenantref"`
			Type      string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &e) != nil || e.Type != "io.pandokos.tenant.deleted.v1" {
			continue
		}
		if e.TenantRef == "" {
			w.T.Errorf("a tenant.deleted event carries no external reference (the guide says it does)")
		}
		ids = append(ids, e.TenantID)
	}
	sort.Strings(ids)
	return ids
}

// newBackup collects what a backup at a point is described by: the heads kept outside, the active
// version, the rows of the ledger, the schema-only dump. The things kept are read before the data is
// copied, so that the copy holds at least what they describe.
func (w *World) newBackup(pt Point) *Backup {
	w.T.Helper()
	w.settle()
	b := &Backup{Point: pt, Heads: w.readHeads(w.proc), KEKVersion: w.activeVersion(w.proc), Ledger: map[string]int64{}}
	for _, name := range worldTenants {
		if !w.deleted[name] {
			b.Ledger[name] = w.rowsToday(Source, w.Tenants[name].ID)
		}
	}
	b.Schema = w.schemaDump(Source)
	w.Out.Heads[pt] = b.Heads
	return b
}

// pending makes the events of a backup in flight: the receiver does not accept deliveries, a call of
// every tenant records an event, and the relay keeps them pending. The backup then holds destinations
// whose cursor is behind the log, as one taken while a destination is slow does.
func (w *World) pending() {
	w.T.Helper()
	w.Receiver.Hold(true)
	for _, name := range worldTenants {
		if !w.deleted[name] {
			w.call(w.proc, name)
		}
	}
	w.settle()
	testutil.Eventually(w.T, 30*time.Second, "the events to be recorded and refused by the receiver", func() bool {
		return w.count(Source, w.db, `SELECT count(*) FROM pandokos.event_destination WHERE failures > 0`) > 0
	})
}

// BackupLogical takes the logical backup of the point: a dump by a superuser, the schema-only dump,
// the heads kept outside (step 1.a).
func (w *World) BackupLogical(p Point) *Backup {
	w.T.Helper()
	defer w.phase("BackupLogical " + string(p))()
	w.pending()
	defer w.Receiver.Hold(false)
	b := w.newBackup(p)
	w.Step(Step1a, func() {
		w.run(Source, "mkdir -p /backup/dumps\n"+withDatabase(Cmd1a, w.db)+"\nmv /backup/pandokos.dump /backup/dumps/"+string(p)+".dump")
	})
	b.Logical = "/backup/dumps/" + string(p) + ".dump"
	w.shareBackup()
	return b
}

// BackupPhysical takes the base backup once, makes a restore point and switches the WAL at the
// point (step 1.b).
func (w *World) BackupPhysical(p Point) *Backup {
	w.T.Helper()
	defer w.phase("BackupPhysical " + string(p))()
	w.pending()
	defer w.Receiver.Hold(false)
	b := w.newBackup(p)
	if !w.baseTaken {
		w.Step(Step1b, func() { w.run(Source, withDatabase(Cmd1b, w.db)) })
		w.baseTaken = true
	}
	b.RestorePoint = "drill_" + strings.ToLower(string(p))
	w.switchAndArchive(b.RestorePoint)
	b.Base, b.WAL = "/backup/base/base.tar.gz", "/backup/wal"
	w.shareBackup()
	return b
}

func (w *World) shareBackup() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	w.P.shareBackup(ctx)
}

// ---- what happens to the source after the backups

// DeleteTenant completes the deletion of a tenant, revokes the key of tenant d, administers tenant a
// once more and records more usage (DEL): everything the backups made before it do not hold.
func (w *World) DeleteTenant(name string) {
	w.T.Helper()
	defer w.phase("DeleteTenant")()
	w.RequestDeletion(name)
	tn := w.Tenants[name]
	testutil.Eventually(w.T, 3*time.Minute, "the deletion of tenant "+name+" to complete", func() bool {
		w.ageSettling(Source, tn.ID)
		return w.deletionState(w.proc, tn.ID) == "completed"
	})
	w.deleted[name] = true

	d := w.Tenants[TenantD]
	w.mustCLI(d.AdminKeyFile, "key", "revoke", "--tenant", d.ID, "--reason", "the drill revokes it at DEL", d.KeyID)
	a := w.Tenants[TenantA]
	w.mustCLI(a.AdminKeyFile, "key", "create", "--tenant", a.ID, "--service-account", a.serviceAccount, "--key-out", filepath.Join(w.dir, "secrets", "a-second-inference.key"))
	for _, n := range []string{TenantA, TenantB} {
		for range 3 {
			w.call(w.proc, n)
		}
	}
	w.settle()
}

// ageSettling ends the settling wait of a deletion that is under way: after its first step the worker
// waits two minutes for transactions that began before the deletion to end, and no transaction of a
// drill does. The end-to-end suite does the same (AgeDeletion in test/dbtest). The grace period is not
// touched: it is the operator's.
func (w *World) ageSettling(service, tenantID string) {
	w.T.Helper()
	w.sql(service, w.db, fmt.Sprintf(`UPDATE pandokos.tenant_deletion SET settle_at = least(settle_at, now()) WHERE tenant_id = '%s'::uuid AND settle_at IS NOT NULL AND state <> 'completed'`, tenantID))
}

// RequestDeletion requests the deletion of a tenant and returns without waiting for it (the grace
// period of the world applies).
func (w *World) RequestDeletion(name string) {
	w.T.Helper()
	tn := w.Tenants[name]
	w.requested[name] = true
	w.mustCLI(w.platform, "tenant", "delete", tn.ID, "--reason", "the drill deletes the tenant", "--confirm-name", tn.Name)
}

// deletionState is the state of a tenant's deletion, "" when there is none.
func (w *World) deletionState(p *instance.Process, tenantID string) string {
	code, out, _ := p.CLI(w.T, w.platform, "tenant", "deletion", "get", tenantID, "-o", "json")
	if code != 0 {
		return ""
	}
	var d struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal([]byte(out), &d)
	return d.State
}

// Disaster removes the source service and its volumes (X), after keeping outside what the guide's
// section 1 says: the heads, the list of completed deletions, the platform's events pulled so far,
// the newest sequence number of each log (kept for the record of what a restore reissues).
func (w *World) Disaster() {
	w.T.Helper()
	defer w.phase("Disaster")()
	w.settle()
	w.Out.Heads[X] = w.readHeads(w.proc)
	w.Out.CompletedDeletion = w.completedDeletions(w.proc)
	w.Out.PulledDeletions = w.deletionEvents(w.proc)
	for _, name := range worldTenants {
		if !w.deleted[name] {
			w.Out.EventHeads[name] = w.count(Source, w.db, fmt.Sprintf(`SELECT COALESCE(max(last_seq), 0) FROM pandokos.outbox_stream WHERE tenant_id = '%s'::uuid`, w.Tenants[name].ID))
		}
	}
	// Wait until every pending delivery of the world is made, so that the receiver has seen every event
	// of the source: what a restore then delivers again is what it has already seen.
	testutil.Eventually(w.T, 60*time.Second, "the relay to deliver every event of the source", func() bool {
		return w.count(Source, w.db, `SELECT count(*) FROM pandokos.event_destination d JOIN pandokos.outbox_stream s ON s.tenant_id = d.tenant_id WHERE d.enabled AND d.cursor_seq < s.last_seq`) == 0
	})
	if code := w.proc.Stop(w.T); code != 0 {
		w.T.Fatalf("serve of the source exited with %d", code)
	}
	if w.baseTaken {
		w.switchAndArchive("drill_x")
	}
	w.shareBackup()
	if w.service != nil {
		w.service.beforeDisaster()
	}
	w.P.Remove(w.T, Source)
}

// ---- the schema and the digests

var reRestrict = func(line string) bool {
	return strings.HasPrefix(line, `\restrict `) || strings.HasPrefix(line, `\unrestrict `)
}

// schemaDump is "pg_dump --schema-only --no-comments" of the database of a service, with the ACL of
// the database appended. pg_dump writes a random token in the lines \restrict and \unrestrict of a
// plain dump (the security releases of 2025): they are the one thing that differs between two dumps
// of the same schema, and they are dropped (finding G-9 of the drill), and the lines of CHECK constraints are compared without their parentheses (G-10).
func (w *World) schemaDump(service string) string {
	w.T.Helper()
	out := w.run(service, fmt.Sprintf("pg_dump -U postgres -d %s --schema-only --no-comments", w.db))
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case reRestrict(line):
		case strings.Contains(line, " CHECK ("):
			// G-10: a CHECK constraint that the migrations wrote with BETWEEN inside an AND is stored as a
			// nested AND, and the dump writes it with its parentheses; the restore reads it back as one flat
			// AND, and the next dump writes that. The constraint is the same; the nesting is not preserved.
			// The two forms differ by parentheses only, so the line is compared without them.
			keep = append(keep, strings.NewReplacer("(", "", ")", "").Replace(line))
		default:
			keep = append(keep, line)
		}
	}
	acl := w.sql(service, "postgres", fmt.Sprintf(`SELECT datname, pg_get_userbyid(datdba), COALESCE(datacl::text, '') FROM pg_database WHERE datname = '%s'`, w.db))
	return strings.Join(keep, "\n") + "\n-- database: " + acl + "\n"
}

// DumpAs takes the dump of the database as a role (a login of the database): the output of pg_dump and
// its error. A role bound by row-level security must be refused.
func (w *World) DumpAs(role string) (string, error) {
	w.T.Helper()
	user := strings.TrimPrefix(role, "pandokos_")
	if l, ok := w.logins[user]; ok {
		user = l.User
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := w.sh(ctx, Source, fmt.Sprintf("pg_dump -U %s -d %s -Fc -f /dev/null", user, w.db))
	return out, err
}

// auditTables are the tables of the audit chains, with the jsonb columns that the canonical encoding
// of a row's hash reads (migrations 0002 and 0015): the text of a jsonb value is what PostgreSQL
// writes for it, and the chain depends on that text.
const digestSQL = `SELECT 'audit_event', COALESCE(md5(string_agg(seq::text || ':' || detail::text, '|' ORDER BY tenant_id, seq)), '') FROM pandokos.audit_event
UNION ALL
SELECT 'platform_audit_event', COALESCE(md5(string_agg(seq::text || ':' || detail::text, '|' ORDER BY seq)), '') FROM pandokos.platform_audit_event`

func (w *World) digests(service string) map[string]string {
	w.T.Helper()
	out := w.sql(service, w.db, digestSQL)
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, sum, ok := strings.Cut(line, "|"); ok {
			m[name] = sum
		}
	}
	return m
}

// Digests are, for each audit table, a digest over seq and the text of every jsonb column of its
// rows: what the canonical encoding of the chain depends on (sprint.md 5.3, a later major).
func (w *World) Digests() map[string]string {
	w.T.Helper()
	w.settle()
	return w.digests(Source)
}
