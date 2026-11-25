//go:build drill

package drill

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/examples/webhook-receiver/receiver"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/instance"
)

// Point is a time point of a case's world (sprint.md 5.1).
type Point string

// The time points. B1 a backup; ROT a rotation of the key (re-wrap complete, the old version
// retired); B2 a backup; DEL the deletion of tenant c completes and the key of tenant d is revoked,
// more usage; X the disaster: the source service and its volumes are removed.
const (
	B1  Point = "B1"
	ROT Point = "ROT"
	B2  Point = "B2"
	DEL Point = "DEL"
	X   Point = "X"
)

// The four tenants of every world. C is deleted at DEL; D has a key revoked at DEL.
const (
	TenantA = "a"
	TenantB = "b"
	TenantC = "c"
	TenantD = "d"
)

// Options of a world.
type Options struct {
	Service     bool   // the key-encryption key is in a key service (OpenBao), not in a file
	GracePeriod string // tenant_deletion.grace_period, "0s" by default
	LedgerRows  int    // the figure case: ledger rows written by SQL before B1 (0: none)
	AuditRows   int    // the figure case: audit rows made by administration calls before B1 (0: none)
}

// Tenant is what a world keeps of a tenant of the drill.
type Tenant struct {
	Name, ID     string
	AdminKeyFile string // the key file of the tenant's administrator
	InferenceKey string // sent to the gateway; never printed or recorded
	KeyID        string
	Credential   string // the provider credential: a canary value
	Quota        int    // requests per day of its quota
	Destination  string // the destination's name
	Secret       string // the destination's signing secret, a canary
	// AfterRotation: the provider credential of this tenant is written after ROT, so that its data key
	// is wrapped by the new version of the key and no case of the key that loses the old one touches it.
	AfterRotation bool
}

// HeadRef is the head of an audit chain as it is kept outside the database: the hash and the seq.
type HeadRef struct {
	Seq  int64
	Hash string
}

// Heads are the heads of the platform's chain and of every tenant's, by tenant name.
type Heads struct {
	Platform HeadRef
	Tenants  map[string]HeadRef
}

// Outside is what the guide's section 1 says to keep outside the database, in a directory that
// survives the disaster: the heads with their seq, the list of completed deletions, the platform's
// events pulled so far, and a copy of each key file.
type Outside struct {
	Dir               string
	Heads             map[Point]Heads
	CompletedDeletion []string // the tenants whose deletion completed, as kept at X
	PulledDeletions   []string // the tenant IDs of the tenant.deleted events pulled before X
	KeyFiles          map[string]string
	KEKVersions       []string         // the identifiers `kek-version list` printed, in order
	EventHeads        map[string]int64 // the newest sequence number of each tenant's log, by tenant name, as kept at X
}

// EventHead is the newest sequence number of a tenant's log as kept outside at X.
func (o *Outside) EventHead(tenant string) int64 { return o.EventHeads[tenant] }

// Backup is a backup of the source server at a time point.
type Backup struct {
	Point        Point
	Logical      string // the dump, in the custom format
	Base         string // the base backup of a physical case
	WAL          string // the archive directory
	RestorePoint string // the name of the restore point made at this point, physical case
	Schema       string // the schema-only dump of the source at this point
	Heads        Heads
	KEKVersion   string
	Ledger       map[string]int64 // rows of the ledger by tenant at this point
}

// KeyCase is what the operator still holds of the key when the restore starts (sprint.md 5.5).
type KeyCase string

// The four cases of the key for a key file (K) and for a key service (S).
const (
	K1  KeyCase = "K1"  // the backup and its key
	K2  KeyCase = "K2"  // a backup older than a rotation, the old key kept
	K3  KeyCase = "K3"  // the same backup, without the old key
	K4  KeyCase = "K4"  // the key lost entirely
	S1  KeyCase = "S1"  // the service restored from its snapshot into an empty server
	S2  KeyCase = "S2"  // the old version still in range in the service
	S3  KeyCase = "S3"  // the old version trimmed
	S4a KeyCase = "S4a" // the key deleted in the service
	S4b KeyCase = "S4b" // the service lost with no snapshot
	S4c KeyCase = "S4c" // a snapshot older than a rotation, a database backup taken after the re-wrap
)

// RestoreSpec says what is restored: from which backup, into which server, with which key.
type RestoreSpec struct {
	Backup   *Backup
	Physical bool   // a base backup with WAL, not the dump
	Until    string // physical: the restore point to stop at; empty: the end of the archive
	On       string // Target (default) or TargetNext
	Keys     KeyCase
	Reading  string // K2: "current" (the current key as key_file) or "old" (the old key as key_file)
}

// World is the world of one case: the Compose project, the installation on the source server, four
// tenants, the provider and the receiver of the test, and what is kept outside.
type World struct {
	T        *testing.T
	Opts     Options
	P        *Project
	Canaries *instance.Canaries
	Bin      instance.Binary
	Upstream *fakeprovider.Server
	Receiver *Receiver
	Tenants  map[string]*Tenant
	Out      *Outside
	Timer    StepTimer
	Case     CaseReport
}

// Step runs fn as a step of the guide and records its duration for the report.
func (w *World) Step(s Step, fn func()) { w.Timer.Time(s, fn) }

// Receiver is the receiver of the destinations of the drill: the example receiver in front of a
// store that keeps each delivery once and counts what it saw again.
type Receiver struct {
	Srv   *httptest.Server
	Store *store
	h     *receiver.Handler
}

type store struct {
	mu   sync.Mutex
	got  map[string]receiver.Event
	dups int
	log  []string
}

func (s *store) Put(id string, e receiver.Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, id)
	if _, seen := s.got[id]; seen {
		s.dups++
		return false, nil
	}
	s.got[id] = e
	return true, nil
}

// NewReceiver starts a receiver verifying with the given secrets; it is closed with the test.
func NewReceiver(t testing.TB, secrets ...string) *Receiver {
	t.Helper()
	r := &Receiver{Store: &store{got: map[string]receiver.Event{}}}
	r.h = &receiver.Handler{Store: r.Store, Secrets: secrets}
	r.Srv = httptest.NewServer(http.HandlerFunc(r.h.ServeHTTP))
	t.Cleanup(r.Srv.Close)
	return r
}

// SetSecrets changes the secrets the receiver verifies with.
func (r *Receiver) SetSecrets(secrets ...string) { r.h.Secrets = secrets }

// IDs returns every delivery's event identifier, in the order received, repeats included.
func (r *Receiver) IDs() []string {
	r.Store.mu.Lock()
	defer r.Store.mu.Unlock()
	return append([]string(nil), r.Store.log...)
}

// Duplicates is the number of deliveries of an identifier already seen.
func (r *Receiver) Duplicates() int {
	r.Store.mu.Lock()
	defer r.Store.mu.Unlock()
	return r.Store.dups
}

// ---- what part C5 to C7 implement

// notImplemented ends a test that needs a part of the drill that sprint 12.3 builds in part C: the
// scaffolding compiles and the tests of part B fail here, loudly, never skip.
func (w *World) notImplemented(what, part string, _ ...any) {
	w.T.Helper()
	fail(w.T, fmt.Sprintf("drill: %s is not implemented yet (sprint 12.3 part %s)", what, part))
}

// fail ends the test. It is a variable so that the stubs above do not look, to a static analysis, as
// if they never returned: the code of the tests that follows them is the code that part C runs.
var fail = func(t testing.TB, msg string) {
	t.Helper()
	t.Fatal(msg)
}

// NewWorld starts the source server (and the key service when asked), installs Pandokos on it as
// the guides say (roles, login users, migrate, init, serve, then the command line), and makes the
// four tenants of the world. Part C5.
func NewWorld(t *testing.T, o Options) *World {
	t.Helper()
	w := &World{T: t, Opts: o, P: NewProject(t), Canaries: &instance.Canaries{}, Bin: instance.Build(t), Tenants: map[string]*Tenant{}}
	w.Bin = w.Bin.WithCanaries(w.Canaries)
	w.Upstream = fakeprovider.New(t)
	w.Receiver = NewReceiver(t)
	w.notImplemented("NewWorld", "C5")
	return w
}

// Rotate rotates the key and waits for the re-wrap to complete and the old version to retire (ROT).
func (w *World) Rotate() { w.notImplemented("Rotate", "C5") }

// BackupLogical takes the logical backup of the point: a dump by a superuser, the schema-only dump,
// the heads kept outside (step 1.a).
func (w *World) BackupLogical(p Point) *Backup {
	w.notImplemented("BackupLogical", "C5", p)
	return nil
}

// BackupPhysical takes the base backup once, makes a restore point and switches the WAL at the
// point (step 1.b).
func (w *World) BackupPhysical(p Point) *Backup {
	w.notImplemented("BackupPhysical", "C5", p)
	return nil
}

// DeleteTenant completes the deletion of a tenant, revokes the key of tenant d and records more
// usage (DEL).
func (w *World) DeleteTenant(name string) { w.notImplemented("DeleteTenant", "C5", name) }

// Disaster removes the source service and its volumes (X), after keeping outside what the guide's
// section 1 says.
func (w *World) Disaster() { w.notImplemented("Disaster", "C5") }

// Secrets returns every secret of the world: provider credentials, signing secrets, key file
// contents, the database passwords, the unseal share and the key service's tokens.
func (w *World) Secrets() []string {
	var out []string
	for _, tn := range w.Tenants {
		out = append(out, tn.Credential, tn.Secret, tn.InferenceKey)
	}
	return out
}

// Restore executes the guide's steps 4.1 to 4.5 on an empty server for the spec and returns the
// restored installation, started (or, when serve refuses, with its refusal).
func (w *World) Restore(s RestoreSpec) *Restored {
	w.notImplemented("Restore", "C5", s)
	return nil
}

// Restored is the installation after a restore: its process, or the refusal of serve.
type Restored struct {
	W         *World
	Proc      *instance.Process
	ServeCode int    // non-zero when serve refused to start
	ServeLog  string // what serve printed when it refused
	NotRun    string // why the binary was not run on this server (a later major), empty otherwise
}

// VaultReady returns the kek_version and kek_activation of the "vault ready" line of serve.
func (r *Restored) VaultReady() (version, activation string) {
	r.W.notImplemented("Restored.VaultReady", "C5")
	return "", ""
}

// KekVersions is the output of "pandokos kek-version list".
func (r *Restored) KekVersions() string {
	r.W.notImplemented("Restored.KekVersions", "C5")
	return ""
}

// Call sends a call with the inference key of a tenant and returns the status and the fingerprint of
// the credential the provider received (empty when it never got the call).
func (r *Restored) Call(tenant string) (status int, credentialSeen string) {
	r.W.notImplemented("Restored.Call", "C5", tenant)
	return 0, ""
}

// CLI runs a command of the line as the administrator of a tenant ("" for the platform's key).
func (r *Restored) CLI(tenant string, args ...string) (code int, stdout, stderr string) {
	r.W.notImplemented("Restored.CLI", "C5", tenant, args)
	return 0, "", ""
}

// AuditVerify runs the guide's step 4.6 for a chain ("" for the platform's) against a head kept
// outside: audit export --to-seq, then audit verify --file with --expect-head (the corrected form).
func (r *Restored) AuditVerify(tenant string, h HeadRef) (code int, out string) {
	r.W.notImplemented("Restored.AuditVerify", "C5", tenant, h)
	return 0, ""
}

// AuditVerifyWhole runs audit verify --expect-head on the whole log, the form the guide gave
// before sprint 12.3 (G-1).
func (r *Restored) AuditVerifyWhole(tenant string, h HeadRef) (code int, out string) {
	r.W.notImplemented("Restored.AuditVerifyWhole", "C5", tenant, h)
	return 0, ""
}

// LedgerRows is the number of usage records the restored ledger holds for a tenant.
func (r *Restored) LedgerRows(tenant string) int64 {
	r.W.notImplemented("Restored.LedgerRows", "C5", tenant)
	return 0
}

// AdmittedBeforeRefusal sends calls until the tenant's quota refuses and returns how many were admitted.
func (r *Restored) AdmittedBeforeRefusal(tenant string) int {
	r.W.notImplemented("Restored.AdmittedBeforeRefusal", "C5", tenant)
	return 0
}

// SchemaDump is the schema-only dump of the restored database, to compare with Backup.Schema.
func (r *Restored) SchemaDump() string {
	r.W.notImplemented("Restored.SchemaDump", "C5")
	return ""
}

// EmitEvent makes an event of the tenant that its destination receives and returns its identifier.
func (r *Restored) EmitEvent(tenant string) string {
	r.W.notImplemented("Restored.EmitEvent", "C5", tenant)
	return ""
}

// ReplayDeletion runs step 5.a: the completed deletions kept outside against the restored list, and
// "tenant delete" again for the difference; it returns the tenants it requested.
func (r *Restored) ReplayDeletion() []string {
	r.W.notImplemented("Restored.ReplayDeletion", "C5")
	return nil
}

// PullDeletionEvents runs step 5.b: the platform's tenant.deleted events, as tenant IDs with their
// external reference.
func (r *Restored) PullDeletionEvents() []string {
	r.W.notImplemented("Restored.PullDeletionEvents", "C5")
	return nil
}

// RecoverLostKey runs step 6.3 or 6.4: the recovery script for the version, a new key, and starts
// serve on it; it returns the script's output.
func (r *Restored) RecoverLostKey(version string) string {
	r.W.notImplemented("Restored.RecoverLostKey", "C6", version)
	return ""
}

// Fingerprint is the identifier of a credential that the test compares with what the provider saw:
// the first 8 bytes of its SHA-256 in hexadecimal. The provider's record and the test's expectations
// use it, never the value.
func Fingerprint(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:8])
}

// KeyMaterial returns the content of every key file the world kept outside (each line, and the raw
// bytes that its base64 stands for), for the search of a backup for what must not be in it.
func (w *World) KeyMaterial() []string {
	var out []string
	for _, path := range w.Out.KeyFiles {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(b))
		out = append(out, line)
		if raw, err := base64.StdEncoding.DecodeString(line); err == nil {
			out = append(out, string(raw))
		}
	}
	return out
}

// DumpAs takes the dump of the database as a role (a login of the database): the output of pg_dump and
// its error. A role bound by row-level security must be refused.
func (w *World) DumpAs(role string) (string, error) {
	w.notImplemented("DumpAs", "C5", role)
	return "", nil
}

// TryRestore is Restore for a restore that may be refused (a base backup on another major): it
// returns what the server said and the error instead of ending the test.
func (w *World) TryRestore(s RestoreSpec) (*Restored, string, error) {
	w.notImplemented("TryRestore", "C5", s)
	return nil, "", nil
}

// Digests are, for each audit table, a digest over seq and the text of every jsonb column of its
// rows: what the canonical encoding of the chain depends on (sprint.md 5.3, a later major).
func (w *World) Digests() map[string]string {
	w.notImplemented("Digests", "C5")
	return nil
}

// RequestDeletion requests the deletion of a tenant and returns without waiting for it (the grace
// period of the world applies).
func (w *World) RequestDeletion(name string) { w.notImplemented("RequestDeletion", "C5", name) }

// ServiceSnapshot takes the snapshot of the key service (GET sys/storage/raft/snapshot) and returns
// the file; the unseal share and a token are kept with it.
func (w *World) ServiceSnapshot(label string) string {
	w.notImplemented("ServiceSnapshot", "C6", label)
	return ""
}

// ServiceRotate rotates the key in the service (a new version).
func (w *World) ServiceRotate() { w.notImplemented("ServiceRotate", "C6") }

// ServiceTrim raises min_decryption_version and trims, with the root token.
func (w *World) ServiceTrim(minVersion int) { w.notImplemented("ServiceTrim", "C6", minVersion) }

// ServiceTrimAsPandokos tries the same with the token of Pandokos's policy and returns the status:
// the policy cannot.
func (w *World) ServiceTrimAsPandokos(minVersion int) int {
	w.notImplemented("ServiceTrimAsPandokos", "C6", minVersion)
	return 0
}

// ServiceDeleteKey deletes the key in the service.
func (w *World) ServiceDeleteKey() { w.notImplemented("ServiceDeleteKey", "C6") }

// ServiceLose removes the service and its volumes, with no snapshot taken.
func (w *World) ServiceLose() { w.notImplemented("ServiceLose", "C6") }

// MigrateStatus runs "pandokos migrate status" on the restored database (step 4.3).
func (r *Restored) MigrateStatus() (int, string) {
	r.W.notImplemented("Restored.MigrateStatus", "C5")
	return 0, ""
}

// Roles lists the login roles of the restored server.
func (r *Restored) Roles() []string {
	r.W.notImplemented("Restored.Roles", "C5")
	return nil
}

// Digests is World.Digests on the restored database.
func (r *Restored) Digests() map[string]string {
	r.W.notImplemented("Restored.Digests", "C5")
	return nil
}

// DataKeyRows is the number of data key rows of a tenant in the restored database.
func (r *Restored) DataKeyRows(tenant string) int {
	r.W.notImplemented("Restored.DataKeyRows", "C5", tenant)
	return 0
}

// DeletionCompleted says whether a completion record of the tenant's deletion exists.
func (r *Restored) DeletionCompleted(tenant string) bool {
	r.W.notImplemented("Restored.DeletionCompleted", "C5", tenant)
	return false
}

// WaitDeleted waits for the deletion of a tenant to complete and reports whether it did.
func (r *Restored) WaitDeleted(tenant string, within time.Duration) bool {
	r.W.notImplemented("Restored.WaitDeleted", "C5", tenant, within)
	return false
}

// EventExport is "pandokos event export" of a tenant from a sequence (a cursor): the status of the
// answer and its body.
func (r *Restored) EventExport(tenant string, from int64) (int, string) {
	r.W.notImplemented("Restored.EventExport", "C5", tenant, from)
	return 0, ""
}

// EventHead is the newest sequence number of a tenant's log in the restored database.
func (r *Restored) EventHead(tenant string) int64 {
	r.W.notImplemented("Restored.EventHead", "C5", tenant)
	return 0
}

// CredentialState is the state of a tenant's provider credential ("active", "revoked").
func (r *Restored) CredentialState(tenant string) string {
	r.W.notImplemented("Restored.CredentialState", "C6", tenant)
	return ""
}

// EnterCredential makes the tenant enter a provider credential again (a new canary, accepted by the
// provider) and returns its value.
func (r *Restored) EnterCredential(tenant string) string {
	r.W.notImplemented("Restored.EnterCredential", "C6", tenant)
	return ""
}

// RotateDestinationSecret runs event-destination rotate-secret for the tenant's destination and
// returns the new secret.
func (r *Restored) RotateDestinationSecret(tenant string) string {
	r.W.notImplemented("Restored.RotateDestinationSecret", "C6", tenant)
	return ""
}

// KekCheck is "pandokos kek check": its exit code and output.
func (r *Restored) KekCheck() (int, string) {
	r.W.notImplemented("Restored.KekCheck", "C6")
	return 0, ""
}

// Finish records the case id in the report of the run: outcome, the steps since the last case of this
// world, versions, sizes. A world that holds several cases (the two physical ones) finishes each.
func (w *World) Finish(id string) { w.notImplemented("Finish", "C7", id) }
