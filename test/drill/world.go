//go:build drill

package drill

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
	Destination  string // the destination's ID (the command line takes a destination by its ID)
	Secret       string // the destination's signing secret, a canary

	serviceAccount string // the ID of the inference service account
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
	Logical      string // the dump, in the custom format (its path in the servers, /backup/...)
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

	// What the installation of the source is made of (install.go).
	db             string
	dir            string // the case's directory on the host: key files, configuration, the outside
	zone           string
	logins         map[string]login
	srcAddr        string
	platform       string // the key file of the platform key
	proc           *instance.Process
	conf           keyConf // what the source's serve reads to reach its key
	keys           []*keyInfo
	service        *service // the key service, when the case has one
	accepted       []string // the credentials the provider accepts
	known          []string // every secret of the world, for the canaries and for redaction
	knownMu        sync.Mutex
	sent           map[string]int // calls that were answered 200, by tenant
	deleted        map[string]bool
	currentVersion string          // the active version of the source's key-encryption key now
	requested      map[string]bool // deletions requested: the tenant is suspended
	baseTaken      bool
	restored       *Restored
	reported       int
	finalScans     int
}

// Step runs fn as a step of the guide and records its duration for the report.
func (w *World) Step(s Step, fn func()) { w.Timer.Time(s, fn) }

// Receiver is the receiver of the destinations of the drill: the example receiver in front of a
// store that keeps each delivery once and counts what it saw again.
type Receiver struct {
	Srv   *httptest.Server
	Store *store

	mu   sync.Mutex
	h    *receiver.Handler
	hold atomic.Bool
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
	r.Srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Srv.Close)
	return r
}

func (r *Receiver) serve(w http.ResponseWriter, req *http.Request) {
	if r.hold.Load() {
		http.Error(w, "the receiver is not accepting deliveries", http.StatusServiceUnavailable)
		return
	}
	r.mu.Lock()
	h := r.h
	r.mu.Unlock()
	h.ServeHTTP(w, req)
}

// SetSecrets changes the secrets the receiver verifies with.
func (r *Receiver) SetSecrets(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.h = &receiver.Handler{Store: r.Store, Secrets: append([]string(nil), secrets...)}
}

// Hold makes the receiver answer 503 to every delivery (true) or accept them again (false): events
// are then pending in the log, which is what a backup taken in the middle of deliveries holds.
func (r *Receiver) Hold(on bool) { r.hold.Store(on) }

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

// Secrets returns every secret of the world: provider credentials, signing secrets, inference keys,
// key file contents, the database passwords, the unseal share and the key service's tokens: whatever
// the world registered with the canaries.
func (w *World) Secrets() []string {
	w.knownMu.Lock()
	defer w.knownMu.Unlock()
	return append([]string(nil), w.known...)
}

func (w *World) knownSecrets() []string { return w.Secrets() }

// reg registers a secret with the canaries and for redaction, and returns it.
func (w *World) reg(secret string) string {
	if secret == "" {
		return secret
	}
	w.knownMu.Lock()
	w.known = append(w.known, secret)
	w.knownMu.Unlock()
	w.Canaries.Add(secret)
	return secret
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
