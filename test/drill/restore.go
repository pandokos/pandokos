//go:build drill

package drill

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/test/instance"
	"github.com/pandokos/pandokos/test/testutil"
)

// The commands the suite runs for the steps of a restore that the guide does not show as one block
// under an anchor: the unpacking of a base backup and the settings of its recovery (the physical
// form of step 4.2). They are named without the prefix Cmd, so that test/repo T-R100 does not compare
// them with a block of the guide: the guide shows them under step 4.2 after the logical form.
const (
	PhysicalUnpack = `tar -xzf /backup/base/base.tar.gz -C "$PGDATA"`
	// PhysicalRecovery is appended to postgresql.auto.conf of the unpacked data directory; the second
	// line is present for a restore to a restore point only. An empty recovery target recovers to the
	// end of the archive.
	PhysicalRecovery = "restore_command = 'cp /backup/wal/%f %p'\nrecovery_target_name = '<restore point>'\nrecovery_target_action = 'promote'\n"
	PhysicalSignal   = `touch "$PGDATA/recovery.signal"`
)

// Restored is the installation after a restore: its process, or the refusal of serve.
type Restored struct {
	W         *World
	Proc      *instance.Process
	ServeCode int    // non-zero when serve refused to start
	ServeLog  string // what serve printed when it refused
	NotRun    string // why the binary was not run on this server (a later major), empty otherwise

	service string
	addr    string
	spec    RestoreSpec
	conf    keyConf
	newKey  *keyInfo // the key made by the recovery of a lost one
	svc     *service // the key service the restore reaches, for the cases of a service
	cutoff  string   // the time of the server just before serve started: what was recorded before it is the restored ledger
}

// teardown stops the process of a restore and destroys its server: the next restore of the same
// world starts on an empty server again.
func (r *Restored) teardown() {
	t := r.W.T
	if r.Proc != nil {
		select {
		case <-r.Proc.Exited():
		default:
			r.Proc.Stop(t)
		}
	}
	r.W.shareBackup()
	r.W.P.Remove(t, r.service)
}

// Restore executes the guide's steps 4.1 to 4.5 on an empty server for the spec and returns the
// restored installation, started (or, when serve refuses, with its refusal).
func (w *World) Restore(s RestoreSpec) *Restored {
	w.T.Helper()
	r, said, err := w.TryRestore(s)
	if err != nil {
		w.T.Fatalf("the restore failed: %v\n%s", err, w.redact(tail(said)))
	}
	return r
}

// TryRestore is Restore for a restore that may be refused (a base backup on another major): it
// returns what the server said and the error instead of ending the test.
func (w *World) TryRestore(s RestoreSpec) (*Restored, string, error) {
	w.T.Helper()
	defer w.phase("Restore")()
	if w.restored != nil {
		w.restored.teardown()
		w.restored = nil
	}
	service := s.On
	if service == "" {
		service = Target
	}
	r := &Restored{W: w, service: service, spec: s}
	w.restored = r

	// The server comes up empty. A base backup is unpacked before the server starts, so that one is
	// held at its entrypoint until the marker file is written.
	if s.Physical {
		w.P.Up(w.T, []string{"PANDOKOS_DRILL_HOLD_TARGET=1"}, service)
		if said, err := w.recoverPhysical(service, s); err != nil {
			return r, said, err
		}
	} else {
		w.P.Up(w.T, nil, service)
	}
	r.addr = w.P.Port(w.T, service, "5432")

	if s.Physical {
		// A base backup carries the roles and the passwords of the login users (G-3): step 4.1 is for a dump.
		if got := w.sql(service, "postgres", `SELECT count(*) FROM pg_roles WHERE rolname LIKE 'pandokos\_%'`); got != "4" {
			return r, got, fmt.Errorf("the base backup holds %s of the four privilege roles", got)
		}
	} else {
		w.Step(Step41, func() {
			w.setupRoles(service)
			w.createLoginsOn(service)
		})
		w.Step(Step42, func() {
			w.run(service, "cp "+s.Backup.Logical+" /backup/pandokos.dump\n"+withDatabase(Cmd42, w.db))
		})
	}

	w.Step(Step44, func() { r.conf = w.restoreConf(r, s) })
	env := w.env(r.addr, r.conf)

	var code int
	var out string
	w.Step(Step43, func() {
		code, out, _ = w.installation(env).Run(w.T, nil, "migrate", "status")
	})
	if code != 0 {
		if service == TargetNext {
			r.NotRun = "migrate status on the later major exited " + fmt.Sprint(code) + ": " + firstLine(w.redact(out))
			return r, "", nil
		}
		return r, out, fmt.Errorf("migrate status of the restored database: exit %d", code)
	}

	r.cutoff = w.sql(service, "postgres", `SELECT now()`)
	w.Step(Step45, func() { r.Proc, r.ServeCode, r.ServeLog = w.startServe(env) })
	if r.Proc == nil {
		w.T.Logf("serve refused to start (exit %d): %s", r.ServeCode, w.redact(startupError(r.ServeLog)))
	}
	if r.Proc == nil && service == TargetNext {
		r.NotRun = fmt.Sprintf("serve exited %d on the later major: %s", r.ServeCode, firstLine(w.redact(r.ServeLog)))
	}
	w.T.Logf("restored on PostgreSQL %s", w.sql(service, "postgres", `SHOW server_version`))
	for _, name := range worldTenants {
		if !w.deleted[name] && w.Out.EventHeads[name] > 0 && r.Proc != nil {
			w.T.Logf("O-3: tenant %s: the event log of the restored database is at %d, it was at %d when the source was lost", name, r.EventHead(name), w.Out.EventHead(name))
		}
	}
	w.Step(Step47, func() {
		if r.Proc != nil {
			_ = w.versions(r.Proc) // nothing is rotated until the checks pass: the versions are read, not changed
		}
	})
	return r, "", nil
}

// recoverPhysical unpacks the base backup into the held server, writes the recovery settings and
// the signal file, lets the server start and waits until recovery has ended and it was promoted.
func (w *World) recoverPhysical(service string, s RestoreSpec) (string, error) {
	w.T.Helper()
	conf := "restore_command = 'cp /backup/wal/%f %p'\nrecovery_target_action = 'promote'\n"
	if s.Until != "" {
		conf += "recovery_target_name = '" + s.Until + "'\n"
	}
	var failed error
	var said string
	w.Step(Step42, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		script := "mkdir -p \"$PGDATA\"\n" + PhysicalUnpack + "\ncat >> \"$PGDATA/postgresql.auto.conf\"\n" + PhysicalSignal +
			"\nchown -R postgres:postgres \"$PGDATA\"\nchmod 0700 \"$PGDATA\"\ntouch /backup/target.go\n"
		said, failed = w.sh(ctx, service, conf, script)
	})
	if failed != nil {
		return said, failed
	}
	if service == TargetNext {
		// A server of another major does not start on these files: its own message says so.
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			logs, _ := w.P.compose(ctx, "--profile", service, "logs", "--no-color", service)
			cancel()
			if strings.Contains(logs, "incompatible") || strings.Contains(logs, "not compatible") {
				return logs, fmt.Errorf("%s refused the base backup", service)
			}
			if w.waitPromoted(service, 3*time.Second) == nil {
				return "", nil
			}
		}
		return "", fmt.Errorf("%s neither refused the base backup nor started", service)
	}
	if err := w.waitPromoted(service, 5*time.Minute); err != nil {
		return "", err
	}
	// The marker is for the next physical restore on a fresh server.
	_, _ = w.sh(context.Background(), service, "", "rm -f /backup/target.go")
	return "", nil
}

// restoreConf is step 4.4: the key files the operator holds for the case, as the configuration.
func (w *World) restoreConf(r *Restored, s RestoreSpec) keyConf {
	w.T.Helper()
	if w.service != nil {
		return w.service.restoreConf(w, r, s)
	}
	latest := w.keys[len(w.keys)-1]
	needed := w.keyOf(s.Backup.KEKVersion)
	switch s.Keys {
	case K1:
		return keyConf{files: []string{needed.File}}
	case K2:
		if s.Reading == "stale" {
			// The first, oldest key as key_file whatever the backup: with a backup newer than the rotation serve
			// refuses, naming both versions (asserted by K2).
			return keyConf{files: []string{w.keys[0].File, latest.File}}
		}
		if s.Reading == "old" {
			return keyConf{files: []string{needed.File, latest.File}}
		}
		return keyConf{files: []string{latest.File, needed.File}}
	case K3:
		// The operator holds the current key and not the old one.
		return keyConf{files: []string{latest.File}}
	case K4:
		// Nothing of the keys: the operator starts with a key made now.
		r.newKey = w.newKey(fmt.Sprintf("kek-new-%d", len(w.keys)+1))
		return keyConf{files: []string{r.newKey.File}}
	}
	w.T.Fatalf("the key case %s is not one of a key file", s.Keys)
	return keyConf{}
}

// keyOf is the key file that belongs to a version.
func (w *World) keyOf(version string) *keyInfo {
	for _, k := range w.keys {
		if k.Version == version {
			return k
		}
	}
	w.T.Fatalf("no key file of the case belongs to the version %s", version)
	return nil
}

// ---- what the restored installation answers

// VaultReady returns the kek_version and kek_activation of the "vault ready" line of serve.
func (r *Restored) VaultReady() (version, activation string) {
	r.W.T.Helper()
	lines := logLines(r.Proc.Stdout.String(), "vault ready")
	if len(lines) == 0 {
		r.W.T.Fatalf("serve printed no \"vault ready\" line")
	}
	l := lines[len(lines)-1]
	version, _ = l["kek_version"].(string)
	activation, _ = l["kek_activation"].(string)
	return version, activation
}

// ExpectedVersion is the key-encryption key version that "vault ready" names after a restore of the
// backup: the backup's active version when the configuration holds that version's key as the key
// that wraps, and the current version when it holds the current key as the one that wraps (a backup
// older than a rotation, the current key as key_file or the service with its newest version): serve
// activates the version of the key that wraps, the old one is only unwrapped, and the worker
// re-wraps (G-5).
func (r *Restored) ExpectedVersion(b *Backup) string {
	if (r.spec.Keys == K2 && r.spec.Reading == "current") || r.spec.Keys == S2 {
		return r.W.currentVersion
	}
	return b.KEKVersion
}

// KekVersions is the output of "pandokos kek-version list".
func (r *Restored) KekVersions() string {
	r.W.T.Helper()
	return r.W.mustCLIOn(r.mustProc(), r.W.platform, "kek-version", "list")
}

func (r *Restored) mustProc() *instance.Process {
	r.W.T.Helper()
	if r.Proc == nil {
		r.W.T.Fatalf("serve is not running (it exited %d)", r.ServeCode)
	}
	return r.Proc
}

// Call sends a call with the inference key of a tenant and returns the status and the fingerprint of
// the credential the provider received (empty when it never got the call).
func (r *Restored) Call(tenant string) (status int, credentialSeen string) {
	r.W.T.Helper()
	return r.W.call(r.mustProc(), tenant)
}

func (r *Restored) keyOfTenant(tenant string) string {
	if tenant == "" {
		return r.W.platform
	}
	return r.W.Tenants[tenant].AdminKeyFile
}

// CLI runs a command of the line as the administrator of a tenant ("" for the platform's key).
func (r *Restored) CLI(tenant string, args ...string) (code int, stdout, stderr string) {
	r.W.T.Helper()
	return r.mustProc().CLI(r.W.T, r.keyOfTenant(tenant), args...)
}

// The lines of Cmd46, by the verb that starts them.
func cmd46Lines() (tenantExport, tenantVerify, platformExport, platformVerify string) {
	for _, line := range strings.Split(Cmd46, "\n") {
		switch {
		case strings.HasPrefix(line, "pandokos audit export"):
			tenantExport = line
		case strings.HasPrefix(line, "pandokos audit verify"):
			tenantVerify = line
		case strings.HasPrefix(line, "pandokos platform-audit export"):
			platformExport = line
		case strings.HasPrefix(line, "pandokos platform-audit verify"):
			platformVerify = line
		}
	}
	return
}

// argsOf turns a line of the guide into the arguments of the binary, after substituting the
// placeholders: the command's own name is dropped.
func argsOf(line string, subst map[string]string) []string {
	for k, v := range subst {
		line = strings.ReplaceAll(line, k, v)
	}
	return strings.Fields(strings.TrimPrefix(line, "pandokos "))
}

// AuditVerify runs the guide's step 4.6 for a chain ("" for the platform's) against a head kept
// outside: audit export --to-seq, then audit verify --file with --expect-head (the corrected form).
func (r *Restored) AuditVerify(tenant string, h HeadRef) (code int, out string) {
	r.W.T.Helper()
	w := r.W
	proc := r.mustProc()
	if tenant != "" && r.DeletionCompleted(tenant) {
		// A tenant whose deletion completed in this database (a restore to the end of the archive that
		// holds it) has no chain any more: it was erased with the tenant. What remains is the log the
		// platform kept of the deletion, which "tenant deletion verify" checks.
		w.Step(Step46, func() {
			var o, e string
			code, o, e = proc.CLI(w.T, w.platform, "tenant", "deletion", "verify", r.tenantID(tenant))
			out = o + e
		})
		return code, out
	}
	tExport, tVerify, pExport, pVerify := cmd46Lines()
	file := filepath.Join(w.dir, "work", fmt.Sprintf("head-%d.ndjson", callSeq.Add(1)))
	subst := map[string]string{"<seq kept with the head>": fmt.Sprint(h.Seq), "<hash kept outside>": h.Hash, "tenant.ndjson": file, "platform.ndjson": file}
	var export, verify string
	key := w.platform
	if tenant == "" {
		export, verify = pExport, pVerify
	} else {
		tn := w.Tenants[tenant]
		export, verify, key = tExport, tVerify, tn.AdminKeyFile
		subst["<tenant ID>"] = tn.ID
	}
	w.Step(Step46, func() {
		c1, o1, e1 := proc.CLI(w.T, key, argsOf(export, subst)...)
		if c1 != 0 {
			code, out = c1, o1+e1
			return
		}
		code, o1, e1 = proc.CLI(w.T, key, argsOf(verify, subst)...)
		out = o1 + e1
	})
	return code, out
}

// AuditVerifyWhole runs audit verify --expect-head on the whole log, the form the guide gave
// before sprint 12.3 (G-1).
func (r *Restored) AuditVerifyWhole(tenant string, h HeadRef) (code int, out string) {
	r.W.T.Helper()
	proc := r.mustProc()
	args := []string{"platform-audit", "verify", "--expect-head", h.Hash}
	key := r.W.platform
	if tenant != "" {
		tn := r.W.Tenants[tenant]
		args, key = []string{"audit", "verify", "--tenant", tn.ID, "--expect-head", h.Hash}, tn.AdminKeyFile
	}
	code, o, e := proc.CLI(r.W.T, key, args...)
	return code, o + e
}

func (r *Restored) tenantID(tenant string) string { return r.W.Tenants[tenant].ID }

// LedgerRows is the number of usage records the restored ledger holds for a tenant (of the days the
// quota counts: today's): what was recorded before serve started on the restored database, not the
// calls the checks make afterwards.
func (r *Restored) LedgerRows(tenant string) int64 {
	r.W.T.Helper()
	return r.W.rowsToday(r.service, r.tenantID(tenant), "recorded_at < '"+r.cutoff+"'")
}

// AdmittedBeforeRefusal sends calls until the tenant's quota refuses and returns how many were admitted.
func (r *Restored) AdmittedBeforeRefusal(tenant string) int {
	r.W.T.Helper()
	admitted := 0
	for i := 0; i <= r.W.Tenants[tenant].Quota+2; i++ {
		switch status, _ := r.Call(tenant); status {
		case 200:
			admitted++
		case 429:
			return admitted
		default:
			r.W.T.Fatalf("tenant %s: a call answered %d while the quota was being reached", tenant, status)
		}
	}
	r.W.T.Fatalf("tenant %s: the quota did not refuse after %d calls", tenant, admitted)
	return admitted
}

// SchemaDump is the schema-only dump of the restored database, to compare with Backup.Schema.
func (r *Restored) SchemaDump() string {
	r.W.T.Helper()
	return r.W.schemaDump(r.service)
}

var reEventLine = regexp.MustCompile(`^\{`)

// events reads "pandokos event export" of a tenant after a sequence: id and sequence of each event.
func (r *Restored) events(tenant string, after int64) (status int, ids []string, seqs []int64, body string) {
	code, out, errs := r.CLI(tenant, "event", "export", "--tenant", r.tenantID(tenant), "--after", fmt.Sprint(after))
	if code != 0 {
		return httpStatusOf(errs), nil, nil, errs
	}
	for _, line := range strings.Split(out, "\n") {
		if !reEventLine.MatchString(line) {
			continue
		}
		var e struct {
			ID       string `json:"id"`
			Sequence string `json:"sequence"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.ID != "" {
			n, _ := strconv.ParseInt(e.Sequence, 10, 64)
			ids, seqs = append(ids, e.ID), append(seqs, n)
		}
	}
	return 200, ids, seqs, out
}

var reStatus = regexp.MustCompile(`\b([1-5][0-9][0-9])\b`)

// httpStatusOf finds the status the command line reports for a failed call in its error text.
func httpStatusOf(errs string) int {
	if m := reStatus.FindStringSubmatch(errs); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// EventExport is "pandokos event export" of a tenant from a sequence (a cursor): the status of the
// answer and its body.
func (r *Restored) EventExport(tenant string, from int64) (int, string) {
	r.W.T.Helper()
	status, ids, _, body := r.events(tenant, from)
	r.W.T.Logf("O-3: event export of tenant %s after %d answers %d with %d events (the head is %d)", tenant, from, status, len(ids), r.EventHead(tenant))
	return status, body
}

// EventHead is the newest sequence number of a tenant's log in the restored database.
func (r *Restored) EventHead(tenant string) int64 {
	r.W.T.Helper()
	return r.W.count(r.service, r.W.db, fmt.Sprintf(`SELECT COALESCE(max(last_seq), 0) FROM pandokos.outbox_stream WHERE tenant_id = '%s'::uuid`, r.tenantID(tenant)))
}

// EmitEvent makes an event of the tenant that its destination receives and returns its identifier:
// a call records a usage event.
func (r *Restored) EmitEvent(tenant string) string {
	r.W.T.Helper()
	head := r.EventHead(tenant)
	// A call that a quota refuses is recorded in the ledger too, and so is an event.
	if status, _ := r.Call(tenant); status != 200 && status != 429 {
		r.W.T.Fatalf("tenant %s: the call that makes an event answered %d", tenant, status)
	}
	var id string
	var seq int64
	testutil.Eventually(r.W.T, 30*time.Second, "the call of tenant "+tenant+" to be recorded as an event", func() bool {
		_, ids, seqs, _ := r.events(tenant, head)
		if len(ids) == 0 {
			return false
		}
		id, seq = ids[len(ids)-1], seqs[len(seqs)-1]
		return true
	})
	r.W.T.Logf("O-3: tenant %s: the log of the restored database was at %d, the new event took sequence %d; before the disaster the log was at %d (%d deliveries of an identifier seen before so far)",
		tenant, head, seq, r.W.Out.EventHead(tenant), r.W.Receiver.Duplicates())
	return id
}

// ReplayDeletion runs step 5.a: the completed deletions kept outside against the restored list, and
// "tenant delete" again for the difference; it returns the tenants it requested.
func (r *Restored) ReplayDeletion() []string {
	r.W.T.Helper()
	w := r.W
	proc := r.mustProc()
	have := map[string]bool{}
	for _, id := range w.completedDeletions(proc) {
		have[id] = true
	}
	var again []string
	w.Step(Step5a, func() {
		for _, id := range w.Out.CompletedDeletion {
			if have[id] {
				continue
			}
			again = append(again, id)
			name := ""
			for _, tn := range w.Tenants {
				if tn.ID == id {
					name = tn.Name
				}
			}
			w.mustCLIOn(proc, w.platform, "tenant", "delete", id, "--reason", "the deletion is requested again after the restore", "--confirm-name", name)
		}
	})
	sort.Strings(again)
	return again
}

// PullDeletionEvents runs step 5.b: the platform's tenant.deleted events, as tenant IDs.
func (r *Restored) PullDeletionEvents() []string {
	r.W.T.Helper()
	return r.W.deletionEvents(r.mustProc())
}

// MigrateStatus runs "pandokos migrate status" on the restored database (step 4.3).
func (r *Restored) MigrateStatus() (int, string) {
	r.W.T.Helper()
	code, out, errs := r.W.installation(r.W.env(r.addr, r.conf)).Run(r.W.T, nil, "migrate", "status")
	return code, out + errs
}

// Roles lists the roles of the restored server that are not PostgreSQL's own.
func (r *Restored) Roles() []string {
	r.W.T.Helper()
	out := r.W.sql(r.service, "postgres", `SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' ORDER BY 1`)
	return strings.Fields(out)
}

// Digests is World.Digests on the restored database.
func (r *Restored) Digests() map[string]string {
	r.W.T.Helper()
	return r.W.digests(r.service)
}

// DataKeyRows is the number of data key rows of a tenant in the restored database.
func (r *Restored) DataKeyRows(tenant string) int {
	r.W.T.Helper()
	return int(r.W.count(r.service, r.W.db, fmt.Sprintf(`SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = '%s'::uuid`, r.tenantID(tenant))))
}

// DeletionCompleted says whether a completion record of the tenant's deletion exists.
func (r *Restored) DeletionCompleted(tenant string) bool {
	r.W.T.Helper()
	return r.W.count(r.service, r.W.db, fmt.Sprintf(`SELECT count(*) FROM pandokos.tenant_deletion WHERE tenant_id = '%s'::uuid AND state = 'completed' AND completed_at IS NOT NULL`, r.tenantID(tenant))) > 0
}

// WaitDeleted waits for the deletion of a tenant to complete and reports whether it did.
func (r *Restored) WaitDeleted(tenant string, within time.Duration) bool {
	r.W.T.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		r.W.ageSettling(r.service, r.tenantID(tenant))
		if r.DeletionCompleted(tenant) {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

// startupError is the "error" of the line "startup failed" of a refused start: what serve says is wrong.
func startupError(log string) string {
	lines := logLines(log+"\n", "startup failed")
	if len(lines) == 0 {
		return firstLine(log)
	}
	e, _ := lines[len(lines)-1]["error"].(string)
	return e
}
