//go:build upgrade

package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/instance"
	"github.com/pandokos/pandokos/test/testutil"
)

// The world of the upgrade tests is made by the previous binary's own commands, and no SQL writes it
// (sprint 12.3, 5.9 step 1): three tenants with provider credentials (canaries) and inference keys,
// a daily request quota on two of them, a tenant price list, a platform-level issuer with a mapping,
// a local account with a session, a revoked key, and the heads of the chains.

var (
	reID       = regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`)
	reKeyID    = regexp.MustCompile(`(?m)^(?:key )?id: (t[a-z2-7]{16})$`)
	rePassword = regexp.MustCompile(`(?m)^password: ([a-z2-7]{24})$`)
)

// tenantW is a tenant of the world.
type tenantW struct {
	Name, ID     string
	Admin        string // key file of its administrator
	SA           string // ID of its inference service account
	InferenceKey string
	KeyID        string
	Credential   string // the provider credential, a canary
	Quota        int    // requests per day; 0 for none
}

// head is the head of a chain kept before the upgrade.
type head struct {
	Seq  int
	Hash string
}

// world is an installation of the previous release that serves, with its world in it.
type world struct {
	t     *testing.T
	db    *dbtest.Database
	env   []string
	up    *fakeprovider.Server
	idp   *fakeidp.Server
	inst  *instance.Installation
	proc  *instance.Process
	zone  string
	admin string // the key file of the bootstrap platform key

	tenants      map[string]*tenantW
	user, passwd string
	session      string // the session token of the local account
	revokedKey   string // an inference key of tenant a, revoked
	revokedKeyID string
	issuerID     string
	mappingID    string
	heads        map[string]head // by tenant ID, and "platform"

	mu        sync.Mutex
	inference string // the address clients use
}

// The quotas the traffic reaches: b's early (while the previous release serves), a's late (after the
// upgrade). c has none, and carries the issuer.
const (
	quotaA = 300
	quotaB = 25
)

// zoneNoMidnight returns a fixed-offset zone in which it is now mid-morning to mid-afternoon, so that
// no midnight (the start of a tenant's day, and with it a reset of the quota) falls within the
// minutes of a test, whatever the hour at which it runs: Etc/GMT-N is N hours east of UTC.
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

// newWorld builds the installation with the previous binary and leaves its server running.
func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, tenants: map[string]*tenantW{}, heads: map[string]head{}, user: "upgrade.admin", zone: zoneNoMidnight(time.Now())}
	w.db = dbtest.New(t) // empty: the previous binary's migrate creates the schema
	w.up = fakeprovider.New(t)
	w.idp = fakeidp.Start(t, fakeidp.Options{})
	w.env = append(w.db.Env(t),
		"PANDOKOS_LOG_LEVEL=info", "PANDOKOS_SHUTDOWN_DRAIN_TIMEOUT=20s", "PANDOKOS_LIMITS_TIME_ZONE="+w.zone,
		"PANDOKOS_TOKENS_PRIVATE_HOSTS=localhost", "PANDOKOS_UPSTREAM_TLS_CA_FILE="+w.idp.CAFile())
	old := instance.Previous(t)
	w.inst = instance.NewInstallation(old, w.env)

	if code, out, errs := w.inst.Run(t, nil, "migrate"); code != 0 {
		t.Fatalf("migrate by the previous binary: exit %d\n%s%s", code, out, errs)
	}
	w.admin = filepath.Join(t.TempDir(), "platform.key")
	code, out, errs := w.inst.Run(t, nil, "init", "--admin-username", w.user, "--key-out", w.admin)
	if code != 0 {
		t.Fatalf("init by the previous binary: exit %d\n%s%s", code, out, errs)
	}
	m := rePassword.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no password in the output of init:\n%s", out)
	}
	w.passwd = m[1]
	w.inst.Canaries.Add(w.passwd)
	w.inst.Canaries.Add(readFile(t, w.db.KEKFile(t)))
	for _, pw := range w.db.Passwords() {
		w.inst.Canaries.Add(pw)
	}
	w.inst.Canaries.Add(readFile(t, w.admin))

	w.proc = w.inst.Serve(t)
	w.proc.Ready(t)
	w.setInference(w.proc.Inference)

	// The catalogue: the fake provider is the endpoint of openai, with the two models the traffic uses.
	w.cli(w.admin, "endpoint", "update", "--base-url", w.up.URL(), "--private", "openai", "default")
	for _, model := range []string{fakeprovider.ModelBasic, fakeprovider.ModelTimed} {
		w.cli(w.admin, "model", "create", "--availability", "all", "openai/"+model)
	}
	for _, name := range []string{"a", "b", "c"} {
		w.addTenant(name)
	}
	w.tenants["a"].Quota, w.tenants["b"].Quota = quotaA, quotaB
	w.cli(w.tenants["a"].Admin, "limit", "create", "--tenant", w.tenants["a"].ID, "--metric", "requests_per_day", "--limit", fmt.Sprint(quotaA))
	w.cli(w.tenants["b"].Admin, "limit", "create", "--tenant", w.tenants["b"].ID, "--metric", "requests_per_day", "--limit", fmt.Sprint(quotaB))
	w.cli(w.tenants["b"].Admin, "price-list", "set", "--tenant", w.tenants["b"].ID, "--currency", "EUR", "--model", "openai/"+fakeprovider.ModelBasic,
		"--rate", "input=4", "--rate", "output=8", "--valid-from", time.Now().UTC().Add(24*time.Hour).Truncate(time.Second).Format(time.RFC3339))

	// A platform-level issuer on the fake identity provider that lists tenant c, and a mapping of the
	// subject "svc" to c's service account.
	c := w.tenants["c"]
	w.issuerID = idOf(t, w.cli(w.admin, "platform-issuer", "create", "--name", "shared", "--issuer", w.idp.Issuer(), "--audience", "pandokos-upgrade", "--tenant-claim", "org"))
	w.cli(w.admin, "platform-issuer", "add-tenant", "--claim-value", "org-c", w.issuerID, c.ID)
	w.mappingID = idOf(t, w.cli(c.Admin, "subject-mapping", "create", "--tenant", c.ID, "--issuer", w.issuerID, "--platform", "--subject", "svc", "--service-account", c.SA))

	// A second key of tenant a, revoked.
	a := w.tenants["a"]
	revokedFile := filepath.Join(t.TempDir(), "revoked.key")
	w.revokedKeyID = keyIDOf(t, w.cli(a.Admin, "key", "create", "--tenant", a.ID, "--service-account", a.SA, "--key-out", revokedFile))
	w.revokedKey = readFile(t, revokedFile)
	w.inst.Canaries.Add(w.revokedKey)
	w.cli(a.Admin, "key", "revoke", "--tenant", a.ID, "--reason", "upgrade world", w.revokedKeyID)

	// A local account with a session.
	br := apitest.NewBrowserAt("http://"+w.proc.Admin, http.DefaultTransport)
	resp := br.SignIn(t, w.user, w.passwd)
	if resp.Status != 200 {
		t.Fatalf("sign-in of the local account: %d\n%s", resp.Status, resp.Body)
	}
	var s adminapi.Session
	resp.JSON(t, &s)
	w.session = br.Token()
	w.inst.Canaries.Add(w.session)

	w.keepHeads()
	return w
}

func readFile(t *testing.T, path string) string {
	t.Helper()
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
		t.Fatalf("no ID in:\n%s", out)
	}
	return m[1]
}

func keyIDOf(t *testing.T, out string) string {
	t.Helper()
	m := reKeyID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no key ID in:\n%s", out)
	}
	return m[1]
}

// cli runs a command of the line against the running server and fails on a non-zero exit.
func (w *world) cli(keyFile string, args ...string) string {
	w.t.Helper()
	code, out, errs := w.proc.CLI(w.t, keyFile, args...)
	if code != 0 {
		w.t.Fatalf("%s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errs)
	}
	return out
}

// addTenant creates a tenant with its administrator, a credential (a canary that the provider
// accepts), an inference service account and a key.
func (w *world) addTenant(name string) {
	t := w.t
	t.Helper()
	tn := &tenantW{Name: "upgrade-" + name, Credential: "synthetic-upgrade-credential-" + name + "-5c0e91f3"}
	tn.Admin = filepath.Join(t.TempDir(), name+".key")
	tn.ID = idOf(t, w.cli(w.admin, "tenant", "create", "--admin-key-out", tn.Admin, tn.Name))
	w.inst.Canaries.Add(tn.Credential)
	w.inst.Canaries.Add(readFile(t, tn.Admin))
	secret := testutil.WriteSecret(t, "provider-secret-"+name, tn.Credential)
	w.cli(tn.Admin, "credential", "create", "--tenant", tn.ID, "--provider", "openai", "--secret-file", secret, "main")
	tn.SA = idOf(t, w.cli(tn.Admin, "service-account", "create", "--tenant", tn.ID, "--role", "inference", "workload"))
	keyFile := filepath.Join(t.TempDir(), name+".inference.key")
	tn.KeyID = keyIDOf(t, w.cli(tn.Admin, "key", "create", "--tenant", tn.ID, "--service-account", tn.SA, "--key-out", keyFile))
	tn.InferenceKey = readFile(t, keyFile)
	w.inst.Canaries.Add(tn.InferenceKey)
	var creds []string
	for _, x := range w.tenants {
		creds = append(creds, x.Credential)
	}
	w.up.Accept(append(creds, tn.Credential)...)
	w.tenants[name] = tn
}

// keepHeads records the heads of every chain, as the guide says to keep them outside the database.
func (w *world) keepHeads() {
	w.t.Helper()
	read := func(key string, args ...string) head {
		var h head
		out := w.cli(key, append(args, "-o", "json")...)
		if err := json.Unmarshal([]byte(out), &h); err != nil {
			// the field names of the head are seq and hash
			var raw struct {
				Seq  int    `json:"seq"`
				Hash string `json:"hash"`
			}
			if err := json.Unmarshal([]byte(out), &raw); err != nil {
				w.t.Fatalf("the head: %v\n%s", err, out)
			}
			h = head{raw.Seq, raw.Hash}
		}
		return h
	}
	w.heads["platform"] = read(w.admin, "platform-audit", "head")
	for _, tn := range w.tenants {
		w.heads[tn.ID] = read(tn.Admin, "audit", "head", "--tenant", tn.ID)
	}
}

func (w *world) setInference(addr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inference = addr
}

// url is where the clients call now.
func (w *world) url() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return "http://" + w.inference
}

// clients are the closed-loop callers of the three tenants: plain calls and streaming calls that
// last long enough to be in flight when the signal comes.
func (w *world) clients() []Client {
	var out []Client
	for _, n := range []string{"a", "b", "c"} {
		tn := w.tenants[n]
		out = append(out, Client{Tenant: n, Key: tn.InferenceKey, Model: fakeprovider.ModelBasic, Slow: fakeprovider.ModelTimed, Clients: 2, Think: 40 * time.Millisecond})
	}
	return out
}

// usageCount is the number of usage records of a tenant with an outcome, read as the superuser.
func (w *world) usageCount(tenantID, outcome string) int {
	w.t.Helper()
	var n int
	if err := w.db.Super(w.t).QueryRow(testCtx(), `SELECT count(*)::int FROM pandokos.usage_record WHERE tenant_id = $1::uuid AND outcome = $2`, tenantID, outcome).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func testCtx() context.Context { return context.Background() }
