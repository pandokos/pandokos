//go:build drill

package drill

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/instance"
	"github.com/pandokos/pandokos/test/testutil"
)

// This file is the installation of the source: what the guides say an operator does (roles, login
// users, the database, migrate, init, serve, then the command line), and the things the cases do to
// it (calls, rotation).

type login struct{ User, PasswordFile string }

// keyInfo is a key-encryption key file of the case, in the order it was generated.
type keyInfo struct {
	Name    string
	File    string
	Version string // the identifier that the database knows it by, once it was activated
}

// keyConf is what the process reads to reach its key: a key file and unwrap files, or a key service.
type keyConf struct {
	files   []string // key_file first, then unwrap_key_files
	transit *transitConf
}

var (
	reID     = regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`)
	reKeyID  = regexp.MustCompile(`(?m)^(?:key )?id: (t[a-z2-7]{16})$`)
	reSecret = regexp.MustCompile(`whsec_[A-Za-z0-9+/]{43}=`)
)

const (
	quotaPerTenant = 25
	modelRef       = "openai/" + fakeprovider.ModelBasic
)

var callSeq atomic.Int64

// zoneNoMidnight returns a fixed-offset zone in which it is now mid-morning to mid-afternoon, so that
// no midnight (the start of a tenant's day, and with it a reset of the quota) falls within the
// minutes of a case, whatever the hour at which it runs: Etc/GMT-N is N hours east of UTC.
func zoneNoMidnight(now time.Time) string {
	for offset := -12; offset <= 12; offset++ {
		if h := ((now.UTC().Hour()+offset)%24 + 24) % 24; h >= 9 && h < 15 {
			if offset == 0 {
				return "UTC"
			}
			if offset > 0 {
				return fmt.Sprintf("Etc/GMT-%d", offset)
			}
			return fmt.Sprintf("Etc/GMT+%d", -offset)
		}
	}
	return "UTC"
}

// NewWorld starts the source server (and the key service when asked), installs Pandokos on it as
// the guides say (roles, login users, migrate, init, serve, then the command line), and makes the
// four tenants of the world. Part C5.
func NewWorld(t *testing.T, o Options) *World {
	t.Helper()
	start := time.Now()
	defer func() { t.Logf("phase NewWorld: %s", time.Since(start).Round(100*time.Millisecond)) }()
	w := &World{T: t, Opts: o, P: NewProject(t), Canaries: &instance.Canaries{}, Bin: instance.Build(t),
		Tenants: map[string]*Tenant{}, db: "pandokos", logins: map[string]login{}, sent: map[string]int{}, deleted: map[string]bool{}, requested: map[string]bool{}}
	w.Bin = w.Bin.WithCanaries(w.Canaries)
	w.Upstream = fakeprovider.New(t)
	w.Receiver = NewReceiver(t)
	w.dir = tempDir(t)
	w.zone = zoneNoMidnight(time.Now())
	w.Out = &Outside{Dir: filepath.Join(w.dir, "outside"), Heads: map[Point]Heads{}, KeyFiles: map[string]string{}, EventHeads: map[string]int64{}}
	for _, d := range []string{w.Out.Dir, filepath.Join(w.dir, "secrets"), filepath.Join(w.dir, "work")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	w.reg(w.P.SuperuserPassword(t))
	// The final check of the case, before its containers go: the logs of the servers hold no secret.
	t.Cleanup(w.finalCheck)

	switch {
	case o.Service:
		w.startService()
	}
	w.P.Up(t, nil, Source)
	w.srcAddr = w.P.Port(t, Source, "5432")

	w.setupRoles(Source)
	w.createLogins()
	w.createDatabase(Source)

	conf := w.initialKey()
	env := w.env(w.srcAddr, conf)
	inst := w.installation(env)
	if code, out, errs := inst.Run(t, nil, "migrate"); code != 0 {
		t.Fatalf("migrate: exit %d\n%s%s", code, w.redact(out), w.redact(errs))
	}
	w.platform = filepath.Join(w.dir, "secrets", "platform.key")
	if code, out, errs := inst.Run(t, nil, "init", "--key-out", w.platform); code != 0 {
		t.Fatalf("init: exit %d\n%s%s", code, w.redact(out), w.redact(errs))
	}
	w.reg(readTrim(w.platform))

	w.startSource(conf)
	w.refreshVersions()

	// The catalogue: the fake provider is the endpoint of openai, with the one model the cases use.
	w.mustCLI(w.platform, "endpoint", "update", "--base-url", w.Upstream.URL(), "--private", "openai", "default")
	w.mustCLI(w.platform, "model", "create", "--availability", "all", modelRef)

	for _, name := range worldTenants {
		w.addTenant(name)
	}
	// The dataset of the figure case is written before the destinations exist: a destination starts at
	// the head of its log and receives what is recorded after it, so the events of the bulk are not
	// delivered to the receiver of the test.
	w.dataset()
	for _, name := range worldTenants {
		w.addDestination(name)
	}
	for _, name := range worldTenants {
		for range 3 {
			w.call(w.proc, name)
		}
	}
	return w
}

// installation is the process environment of an installation as a value the harness runs commands with.
func (w *World) installation(env []string) *instance.Installation {
	return &instance.Installation{Bin: w.Bin, Env: env, Canaries: w.Canaries}
}

// setupRoles is the guide's step 4.1 on a server, and the first step of an installation: the privilege
// roles, from deploy/postgres/roles.sql.
func (w *World) setupRoles(service string) {
	w.T.Helper()
	w.run(service, Cmd41)
}

// createLogins creates the login users of the database guide's "one login user per role" with
// passwords the test generates, in files; on a server that does not have them yet.
func (w *World) createLogins() {
	w.T.Helper()
	if len(w.logins) == 0 {
		for _, r := range []string{"owner", "gateway", "admin", "worker"} {
			pw := w.reg(randomHex(w.T, 16))
			file := filepath.Join(w.dir, "secrets", "db_"+r)
			if err := os.WriteFile(file, []byte(pw), 0o600); err != nil {
				w.T.Fatal(err)
			}
			w.logins[r] = login{User: "drill_" + r, PasswordFile: file}
		}
	}
}

// createLoginsOn makes the users on a server whose roles exist: step 4.1 of a restore into an empty
// server, and the installation of the source.
func (w *World) createLoginsOn(service string) {
	w.T.Helper()
	var sb strings.Builder
	for _, r := range []string{"owner", "gateway", "admin", "worker"} {
		l := w.logins[r]
		fmt.Fprintf(&sb, "CREATE ROLE %s LOGIN INHERIT PASSWORD '%s';\nGRANT pandokos_%s TO %s;\n", l.User, readTrim(l.PasswordFile), r, l.User)
	}
	w.sqlSecret(service, "postgres", sb.String())
}

// createDatabase is the database guide's creation of the database of the installation, and the login
// users of its roles.
func (w *World) createDatabase(service string) {
	w.T.Helper()
	w.createLoginsOn(service)
	w.run(service, fmt.Sprintf(`psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE %[1]s OWNER pandokos_owner" -c "REVOKE ALL ON DATABASE %[1]s FROM PUBLIC" -c "GRANT CONNECT ON DATABASE %[1]s TO pandokos_gateway, pandokos_admin, pandokos_worker"`, w.db))
}

// env is the configuration of a process of the installation, as environment variables: the database
// at addr, the login users and their password files, the key, and the settings that make the events
// and the deletions of a case take seconds, not minutes.
func (w *World) env(addr string, k keyConf) []string {
	grace := w.Opts.GracePeriod
	if grace == "" {
		grace = "0s"
	}
	env := []string{
		"PANDOKOS_LOG_LEVEL=info",
		"PANDOKOS_DATABASE_ADDRESS=" + addr, "PANDOKOS_DATABASE_NAME=" + w.db, "PANDOKOS_DATABASE_TLS_MODE=disable",
		"PANDOKOS_PRICING_IMPORT_AT_START=false",
		"PANDOKOS_LIMITS_TIME_ZONE=" + w.zone,
		"PANDOKOS_EVENTS_RELAY_INTERVAL=100ms", "PANDOKOS_EVENTS_DELIVERY_TIMEOUT=1s", "PANDOKOS_EVENTS_DELIVERY_BACKOFF_MAX=5s",
		"PANDOKOS_EVENTS_WEBHOOK_PRIVATE_HOSTS=localhost",
		"PANDOKOS_TENANT_DELETION_GRACE_PERIOD=" + grace, "PANDOKOS_TENANT_DELETION_INTERVAL=10s",
		"PANDOKOS_SHUTDOWN_DRAIN_TIMEOUT=5s",
	}
	for _, r := range []string{"owner", "gateway", "admin", "worker"} {
		up := strings.ToUpper(r)
		env = append(env, "PANDOKOS_DATABASE_"+up+"_USER="+w.logins[r].User, "PANDOKOS_DATABASE_"+up+"_PASSWORD_FILE="+w.logins[r].PasswordFile)
	}
	if k.transit != nil {
		env = append(env, k.transit.env()...)
	} else if len(k.files) > 0 {
		env = append(env, "PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE="+k.files[0])
		if len(k.files) > 1 {
			env = append(env, "PANDOKOS_VAULT_KEK_LOCAL_UNWRAP_KEY_FILES="+strings.Join(k.files[1:], ","))
		}
	}
	return env
}

// ---- keys

// newKey generates a key file in the outside directory (the guide's section 1: a copy of the key file
// is made when it is generated) with "pandokos kek generate" and registers its content.
func (w *World) newKey(name string) *keyInfo {
	w.T.Helper()
	file := filepath.Join(w.Out.Dir, name)
	code, out, errs := w.Bin.Run(w.T, nil, "kek", "generate", "--out", file)
	if code != 0 {
		w.T.Fatalf("kek generate: exit %d\n%s%s", code, w.redact(out), w.redact(errs))
	}
	w.reg(readTrim(file))
	k := &keyInfo{Name: name, File: file}
	w.keys = append(w.keys, k)
	w.Out.KeyFiles[name] = file
	return k
}

// initialKey is the key of the installation: a key file, or the key service.
func (w *World) initialKey() keyConf {
	if w.service != nil {
		return keyConf{transit: w.service.conf()}
	}
	k := w.newKey("kek-1")
	return keyConf{files: []string{k.File}}
}

// startSource starts serve of the source with the key configuration and waits for it.
func (w *World) startSource(conf keyConf) {
	w.T.Helper()
	p, code, log := w.startServe(w.env(w.srcAddr, conf))
	if p == nil {
		w.T.Fatalf("serve of the source exited with %d:\n%s", code, w.redact(tail(log)))
	}
	w.proc = p
	w.conf = conf
}

// refreshVersions reads kek-version list and attaches a version identifier to the key files that do
// not have one yet, in order: the one that is active now belongs to the newest key.
func (w *World) refreshVersions() {
	w.T.Helper()
	items := w.versions(w.proc)
	w.Out.KEKVersions = w.Out.KEKVersions[:0]
	for _, v := range items {
		w.Out.KEKVersions = append(w.Out.KEKVersions, v.ID)
		if v.State == "active" {
			w.currentVersion = v.ID
		}
	}
	if w.service != nil {
		return
	}
	known := map[string]bool{}
	for _, k := range w.keys {
		if k.Version != "" {
			known[k.Version] = true
		}
	}
	for _, v := range items {
		if v.State != "active" || known[v.ID] {
			continue
		}
		for _, k := range w.keys {
			if k.Version == "" {
				k.Version = v.ID
				break
			}
		}
	}
}

type versionItem struct{ ID, State string }

// versions is "pandokos kek-version list -o json".
func (w *World) versions(p *instance.Process) []versionItem {
	w.T.Helper()
	out := w.mustCLIOn(p, w.platform, "kek-version", "list", "-o", "json")
	var doc struct {
		Items []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		w.T.Fatalf("kek-version list: %v\n%s", err, out)
	}
	var vs []versionItem
	for _, i := range doc.Items {
		vs = append(vs, versionItem{i.ID, i.State})
	}
	return vs
}

// activeVersion is the identifier of the active key-encryption key version.
func (w *World) activeVersion(p *instance.Process) string {
	for _, v := range w.versions(p) {
		if v.State == "active" {
			return v.ID
		}
	}
	return ""
}

// Rotate rotates the key and waits for the re-wrap to complete and the old version to retire (ROT):
// the vault guide's section 6a, with a key file: a new file, the old one as an unwrap key, a restart,
// the line "key rotation complete", the old file removed, a restart.
func (w *World) Rotate() {
	w.T.Helper()
	defer w.phase("Rotate")()
	if w.service != nil {
		w.rotateService()
		return
	}
	old := w.keys[len(w.keys)-1]
	next := w.newKey(fmt.Sprintf("kek-%d", len(w.keys)+1))
	w.restartSource(keyConf{files: []string{next.File, old.File}})
	w.refreshVersions()
	w.waitRotation(old.Version)
	// Step 6 of the guide: the old file leaves the configuration, the instance restarts.
	w.restartSource(keyConf{files: []string{next.File}})
}

// waitRotation waits for the retirement of a version: the line "key rotation complete" and the
// state retired in kek-version list.
func (w *World) waitRotation(version string) {
	w.T.Helper()
	testutil.Eventually(w.T, 3*time.Minute, "the key rotation to complete (the worker re-wraps every data key)", func() bool {
		for _, v := range w.versions(w.proc) {
			if v.ID == version {
				return v.State == "retired"
			}
		}
		return false
	})
}

// restartSource stops serve of the source and starts it again with another key configuration.
func (w *World) restartSource(conf keyConf) {
	w.T.Helper()
	if code := w.proc.Stop(w.T); code != 0 {
		w.T.Fatalf("serve of the source exited with %d at the restart", code)
	}
	w.startSource(conf)
}

// ---- serve

// startServe starts serve with the environment and waits until both listeners answer, or until the
// process exits (a refusal), which it returns as a nil process with the exit code and what it printed.
func (w *World) startServe(env []string) (*instance.Process, int, string) {
	w.T.Helper()
	p := w.Bin.Serve(w.T, env)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-p.Exited():
			return nil, p.Code(), p.Stdout.String() + p.Stderr.String()
		default:
		}
		if w.listening(p) && w.ready(p) {
			return p, 0, ""
		}
		time.Sleep(100 * time.Millisecond)
	}
	w.T.Fatalf("serve was not ready within 2 minutes:\n%s", w.redact(tail(p.Stdout.String())))
	return nil, 0, ""
}

// listening reads the addresses from the complete "listening" lines of the log.
func (w *World) listening(p *instance.Process) bool {
	for _, l := range logLines(p.Stdout.String(), "listening") {
		addr, _ := l["address"].(string)
		switch l["listener"] {
		case "inference":
			p.Inference = addr
		case "admin":
			p.Admin = addr
		}
	}
	return p.Inference != "" && p.Admin != ""
}

func (w *World) ready(p *instance.Process) bool {
	for _, addr := range []string{p.Inference, p.Admin} {
		resp, err := p.Client.Get(p.Scheme + "://" + addr + "/readyz")
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
	}
	return true
}

// logLines decodes the complete JSON lines of a log whose msg is msg: a trailing fragment is never
// parsed.
func logLines(text, msg string) []map[string]any {
	i := strings.LastIndexByte(text, '\n')
	if i < 0 {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(text[:i], "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// ---- the command line

// mustCLIOn runs a command of the line against a process as the holder of a key file and fails the
// test when it exits with a non-zero code.
func (w *World) mustCLIOn(p *instance.Process, keyFile string, args ...string) string {
	w.T.Helper()
	code, out, errs := p.CLI(w.T, keyFile, args...)
	if code != 0 {
		w.T.Fatalf("pandokos %s: exit %d\nstdout: %s\nstderr: %s", firstWords(args), code, w.redact(tail(out)), w.redact(tail(errs)))
	}
	return out
}

func (w *World) mustCLI(keyFile string, args ...string) string {
	w.T.Helper()
	return w.mustCLIOn(w.proc, keyFile, args...)
}

func firstWords(args []string) string {
	if len(args) > 3 {
		args = args[:3]
	}
	return strings.Join(args, " ")
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func idOf(t *testing.T, out string) string {
	t.Helper()
	m := reID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no ID in:\n%s", tail(out))
	}
	return m[1]
}

func keyIDOf(t *testing.T, out string) string {
	t.Helper()
	m := reKeyID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no key ID in:\n%s", tail(out))
	}
	return m[1]
}

// ---- tenants

// addTenant makes a tenant with the command line: its administrator, a provider credential that
// is a canary (the provider accepts it), an inference service account and key, a daily request
// quota, and a signed destination on the receiver of the test.
func (w *World) addTenant(name string) {
	t := w.T
	t.Helper()
	tn := &Tenant{Name: "drill-" + name, Quota: quotaPerTenant}
	tn.Credential = w.reg("synthetic-drill-credential-" + name + "-" + randomHex(t, 6))
	tn.AdminKeyFile = filepath.Join(w.dir, "secrets", name+"-admin.key")
	tn.ID = idOf(t, w.mustCLI(w.platform, "tenant", "create", "--admin-key-out", tn.AdminKeyFile, "--external-reference", "drill-ref-"+name, tn.Name))
	w.reg(readTrim(tn.AdminKeyFile))
	secretFile := filepath.Join(w.dir, "secrets", name+"-provider-secret")
	if err := os.WriteFile(secretFile, []byte(tn.Credential), 0o600); err != nil {
		t.Fatal(err)
	}
	w.mustCLI(tn.AdminKeyFile, "credential", "create", "--tenant", tn.ID, "--provider", "openai", "--secret-file", secretFile, "main")
	tn.serviceAccount = idOf(t, w.mustCLI(tn.AdminKeyFile, "service-account", "create", "--tenant", tn.ID, "--role", "inference", "workload"))
	keyFile := filepath.Join(w.dir, "secrets", name+"-inference.key")
	tn.KeyID = keyIDOf(t, w.mustCLI(tn.AdminKeyFile, "key", "create", "--tenant", tn.ID, "--service-account", tn.serviceAccount, "--key-out", keyFile))
	tn.InferenceKey = w.reg(readTrim(keyFile))
	w.mustCLI(tn.AdminKeyFile, "limit", "create", "--tenant", tn.ID, "--metric", "requests_per_day", "--limit", fmt.Sprint(tn.Quota))
	w.accepted = append(w.accepted, tn.Credential)
	w.Upstream.Accept(w.accepted...)
	w.Tenants[name] = tn
}

// addDestination makes the tenant's destination on the receiver of the test: signed, all event types.
func (w *World) addDestination(name string) {
	t := w.T
	t.Helper()
	tn := w.Tenants[name]
	out := w.mustCLI(tn.AdminKeyFile, "event-destination", "create", "--tenant", tn.ID, "--url", "http://localhost:"+portOf(w.Receiver.Srv.URL)+"/"+name, "hook")
	tn.Secret = w.reg(reSecret.FindString(out))
	if tn.Secret == "" {
		t.Fatalf("event-destination create printed no signing secret")
	}
	// The command line takes a destination by its ID: Destination is the ID.
	tn.Destination = destinationID(t, w.mustCLI(tn.AdminKeyFile, "event-destination", "list", "--tenant", tn.ID, "-o", "json"), "hook")
	var secrets []string
	for _, n := range worldTenants {
		if x := w.Tenants[n]; x != nil && x.Secret != "" {
			secrets = append(secrets, x.Secret)
		}
	}
	w.Receiver.SetSecrets(secrets...)
}

// destinationID finds a destination by its name in the output of "event-destination list -o json".
func destinationID(t *testing.T, list, name string) string {
	t.Helper()
	var doc struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(list), &doc); err != nil {
		t.Fatalf("event-destination list: %v", err)
	}
	for _, i := range doc.Items {
		if i.Name == name {
			return i.ID
		}
	}
	t.Fatalf("no destination named %s", name)
	return ""
}

func portOf(u string) string {
	if i := strings.LastIndexByte(u, ':'); i >= 0 {
		return u[i+1:]
	}
	return ""
}

// ---- calls

// call sends one chat completion with the tenant's inference key to the inference listener of the
// process and returns the status and the fingerprint of the credential the provider received for it
// (empty when it never got the call).
func (w *World) call(p *instance.Process, tenant string) (int, string) {
	w.T.Helper()
	tn := w.Tenants[tenant]
	id := fmt.Sprintf("drill-call-%d", callSeq.Add(1))
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}]}`, fakeprovider.ModelBasic, id)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, p.Scheme+"://"+p.Inference+"/openai/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		w.T.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tn.InferenceKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.Client.Do(req)
	if err != nil {
		w.T.Fatalf("a call of tenant %s: %v", tenant, err)
	}
	text, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	w.Canaries.Scan(w.T, "an answer of the gateway", text)
	if p == w.proc && resp.StatusCode == http.StatusOK {
		w.sent[tenant]++
	}
	return resp.StatusCode, w.seenBy(id)
}

// seenBy is the fingerprint of the credential that the provider received with the call whose prompt
// is id.
func (w *World) seenBy(id string) string {
	for _, r := range w.Upstream.Requests() {
		var m struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(r.Body, &m) != nil || len(m.Messages) == 0 || m.Messages[0].Content != id {
			continue
		}
		return Fingerprint(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	return ""
}

// rowsToday is the usage records of a tenant that opened a credential since its day began: what the
// limit counters hold.
func (w *World) rowsToday(service, tenantID string, and ...string) int64 {
	cond := ""
	for _, a := range and {
		cond += " AND " + a
	}
	return w.count(service, w.db, fmt.Sprintf(`SELECT count(*) FROM pandokos.usage_record WHERE tenant_id = '%s'::uuid AND credential_id IS NOT NULL
		AND started_at >= (date_trunc('day', now() AT TIME ZONE '%s') AT TIME ZONE '%s')%s`, tenantID, w.zone, w.zone, cond))
}

// settle waits until the ledger holds every call that was answered: the records are written in
// batches within a few milliseconds.
func (w *World) settle() {
	w.T.Helper()
	for name, n := range w.sent {
		if w.deleted[name] {
			continue
		}
		tn := w.Tenants[name]
		testutil.Eventually(w.T, 30*time.Second, "the ledger to hold the calls of tenant "+name, func() bool {
			return w.rowsToday(Source, tn.ID) >= int64(n)
		})
	}
}
