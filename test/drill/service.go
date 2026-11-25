//go:build drill

package drill

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pandokos/pandokos/test/testutil"
)

// The key service of the drill: OpenBao (the pinned image of the transit suite), started as a server
// with integrated storage, initialised with one unseal share, unsealed, with the transit engine, a
// key created with the service's defaults (so not exportable: vault.kek.transit.allow_exportable_key
// is never set here), the policy of deploy/transit/pandokos-policy.hcl and a token of that policy.
// Everything is done by HTTP with the root token, as an operator of the service would, and the
// service's backup is its storage snapshot (sprint.md 5.5).

const (
	transitMount = "transit"
	transitKey   = "pandokos"
)

type transitConf struct {
	address, tokenFile string
}

func (c *transitConf) env() []string {
	return []string{
		"PANDOKOS_VAULT_KEK_PROVIDER=transit",
		"PANDOKOS_VAULT_KEK_TRANSIT_ADDRESS=" + c.address,
		"PANDOKOS_VAULT_KEK_TRANSIT_MOUNT=" + transitMount,
		"PANDOKOS_VAULT_KEK_TRANSIT_KEY_NAME=" + transitKey,
		"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_METHOD=token",
		"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_TOKEN_FILE=" + c.tokenFile,
		"PANDOKOS_VAULT_KEK_TRANSIT_REQUEST_TIMEOUT=5s",
		"PANDOKOS_VAULT_KEK_TRANSIT_START_TIMEOUT=30s",
	}
}

// service is one server of the key service: the one the installation uses, or the one an operator
// starts to restore it.
type service struct {
	w         *World
	name      string // the Compose service
	addr      string // http://127.0.0.1:port
	root      string // the root token of this server, never printed
	share     string // the unseal share of the service's data (the first server's, after a restore)
	tokenFile string // the token of Pandokos's policy
	version   string
	snaps     map[string]string
	client    *http.Client
}

func (s *service) conf() *transitConf { return &transitConf{address: s.addr, tokenFile: s.tokenFile} }

// startService starts the key service of the case, initialises it and sets it up.
func (w *World) startService() {
	w.T.Helper()
	s := w.newService(OpenBao)
	s.initialize()
	s.setup()
	w.service = s
}

func (w *World) newService(name string) *service {
	w.T.Helper()
	w.P.Up(w.T, nil, name)
	s := &service{w: w, name: name, addr: "http://" + w.P.Port(w.T, name, "8200"), snaps: map[string]string{}, client: &http.Client{Timeout: 30 * time.Second}}
	testutil.Eventually(w.T, time.Minute, "the key service to answer", func() bool {
		status, _ := s.call(http.MethodGet, "sys/health", "", nil)
		return status != 0
	})
	return s
}

// call sends a request to the service's API and returns the status and the body (0 when the service
// did not answer). The token is sent in the header X-Vault-Token and appears nowhere else.
func (s *service) call(method, path, token string, body any) (int, []byte) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			s.w.T.Fatal(err)
		}
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, s.addr+"/v1/"+path, rd)
	if err != nil {
		s.w.T.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (s *service) must(method, path, token string, body any) []byte {
	s.w.T.Helper()
	status, out := s.call(method, path, token, body)
	if status < 200 || status > 299 {
		s.w.T.Fatalf("key service %s %s answered %d: %s", method, path, status, s.w.redact(tail(string(out))))
	}
	return out
}

// initialize is "PUT sys/init" with one share and one threshold, and "PUT sys/unseal" with it.
func (s *service) initialize() {
	s.w.T.Helper()
	out := s.must(http.MethodPut, "sys/init", "", map[string]int{"secret_shares": 1, "secret_threshold": 1})
	var doc struct {
		Shares []string `json:"keys_base64"`
		Root   string   `json:"root_token"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc.Shares) != 1 || doc.Root == "" {
		s.w.T.Fatalf("the initialisation of the key service answered something else than a share and a root token")
	}
	s.share, s.root = s.w.reg(doc.Shares[0]), s.w.reg(doc.Root)
	s.unseal(s.share)
}

func (s *service) unseal(share string) {
	s.w.T.Helper()
	s.must(http.MethodPut, "sys/unseal", "", map[string]string{"key": share})
	testutil.Eventually(s.w.T, time.Minute, "the key service to be active", func() bool {
		status, out := s.call(http.MethodGet, "sys/health", "", nil)
		if status == http.StatusOK {
			var h struct {
				Version string `json:"version"`
			}
			_ = json.Unmarshal(out, &h)
			s.version = "OpenBao " + h.Version
			return true
		}
		return false
	})
}

// setup mounts the transit engine, creates the key with the defaults, writes the policy of Pandokos
// from the repository and makes a token of it, in a file.
func (s *service) setup() {
	s.w.T.Helper()
	s.must(http.MethodPost, "sys/mounts/"+transitMount, s.root, map[string]string{"type": "transit"})
	s.createKey()
	policy, err := os.ReadFile(filepath.Join(testutil.RepoRoot(s.w.T), "deploy", "transit", "pandokos-policy.hcl"))
	if err != nil {
		s.w.T.Fatal(err)
	}
	s.must(http.MethodPut, "sys/policies/acl/pandokos", s.root, map[string]string{"policy": string(policy)})
	out := s.must(http.MethodPost, "auth/token/create", s.root, map[string]any{"policies": []string{"pandokos"}, "no_default_policy": true, "no_parent": true, "period": "24h"})
	var doc struct {
		Auth struct {
			Token string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || doc.Auth.Token == "" {
		s.w.T.Fatalf("the token of Pandokos's policy was not issued")
	}
	s.tokenFile = filepath.Join(s.w.dir, "secrets", "transit-token-"+s.name)
	if err := os.WriteFile(s.tokenFile, []byte(s.w.reg(doc.Auth.Token)+"\n"), 0o600); err != nil {
		s.w.T.Fatal(err)
	}
}

// createKey creates the key of Pandokos with the defaults of the service: type aes256-gcm96, not
// exportable, no plaintext backup, no deletion, no automatic rotation.
func (s *service) createKey() {
	s.w.T.Helper()
	s.must(http.MethodPost, "transit/keys/"+transitKey, s.root, map[string]string{"type": "aes256-gcm96"})
}

func (s *service) keyExists() bool {
	status, _ := s.call(http.MethodGet, "transit/keys/"+transitKey, s.root, nil)
	return status == http.StatusOK
}

// pandokosToken is the token of Pandokos's policy, read from its file.
func (s *service) pandokosToken() string { return readTrim(s.tokenFile) }

// snapshot is "GET sys/storage/raft/snapshot": the service's own backup, kept in the outside directory
// with the unseal share and a token held by the case (the guide says to keep them with it).
func (s *service) snapshot(label string) string {
	s.w.T.Helper()
	status, body := s.call(http.MethodGet, "sys/storage/raft/snapshot", s.root, nil)
	if status != http.StatusOK || len(body) == 0 {
		s.w.T.Fatalf("the snapshot of the key service answered %d", status)
	}
	dir := filepath.Join(s.w.Out.Dir, "service")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.w.T.Fatal(err)
	}
	file := filepath.Join(dir, label+".snap")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		s.w.T.Fatal(err)
	}
	s.snaps[label] = file
	return file
}

// restoreSnapshot is the guide's restore of the service into an empty server: initialise it, unseal
// it with its own share, "POST sys/storage/raft/snapshot-force" with its own root token, then unseal
// with the share of the service the snapshot was taken from. The token of Pandokos works again.
func (s *service) restoreSnapshot(file, originalShare, originalRoot, originalTokenFile string) {
	s.w.T.Helper()
	body, err := os.ReadFile(file)
	if err != nil {
		s.w.T.Fatal(err)
	}
	s.initialize()
	s.must(http.MethodPost, "sys/storage/raft/snapshot-force", s.root, body)
	// The restored service is sealed with the original barrier: its own share no longer opens it.
	s.unseal(originalShare)
	// The tokens of the original service are in its storage: its root token and Pandokos's work again, and
	// the ones this server was initialised with are gone.
	s.share, s.root, s.tokenFile = originalShare, originalRoot, originalTokenFile
}

func (s *service) rotate() {
	s.w.T.Helper()
	s.must(http.MethodPost, "transit/keys/"+transitKey+"/rotate", s.root, nil)
}

// trim is the erasure procedure of the external key guide: min_decryption_version raised, then trim.
func (s *service) trim(minVersion int) {
	s.w.T.Helper()
	s.must(http.MethodPost, "transit/keys/"+transitKey+"/config", s.root, map[string]int{"min_decryption_version": minVersion})
	s.must(http.MethodPost, "transit/keys/"+transitKey+"/config", s.root, map[string]int{"min_decryption_version": minVersion, "min_encryption_version": minVersion})
	s.must(http.MethodPost, "transit/keys/"+transitKey+"/trim", s.root, map[string]int{"min_available_version": minVersion})
}

// trimAs is the same with the token of Pandokos's policy: the answer's status.
func (s *service) trimAs(minVersion int) int {
	status, _ := s.call(http.MethodPost, "transit/keys/"+transitKey+"/config", s.pandokosToken(), map[string]int{"min_decryption_version": minVersion})
	if status == http.StatusOK || status == http.StatusNoContent {
		return status
	}
	trimStatus, _ := s.call(http.MethodPost, "transit/keys/"+transitKey+"/trim", s.pandokosToken(), map[string]int{"min_available_version": minVersion})
	if trimStatus != http.StatusForbidden {
		return trimStatus
	}
	return status
}

// deleteKey deletes the key: the service refuses until deletion is allowed on the key, which only
// the operator of the service can do.
func (s *service) deleteKey() {
	s.w.T.Helper()
	s.must(http.MethodPost, "transit/keys/"+transitKey+"/config", s.root, map[string]bool{"deletion_allowed": true})
	s.must(http.MethodDelete, "transit/keys/"+transitKey, s.root, nil)
}

// restoreConf is step 4.4 for a key service: what the operator reaches the service by when the
// restore starts, which depends on what is left of it (sprint.md 5.5).
func (s *service) restoreConf(w *World, r *Restored, spec RestoreSpec) keyConf {
	w.T.Helper()
	switch spec.Keys {
	case S1, S4c:
		label := "after-rotation"
		if spec.Keys == S4c || spec.Backup.Point == B1 {
			label = "before-rotation"
		}
		file, ok := s.snaps[label]
		if !ok {
			w.T.Fatalf("no snapshot %q of the key service was taken", label)
		}
		t := w.newService(OpenBaoTarget)
		t.restoreSnapshot(file, s.share, s.root, s.tokenFile)
		r.svc = t
		return keyConf{transit: t.conf()}
	case S4b:
		// The service is gone and nothing of it was kept: a new service, a new key, a new policy and token.
		t := w.newService(OpenBaoTarget)
		t.initialize()
		t.setup()
		r.svc = t
		return keyConf{transit: t.conf()}
	case S2, S3, S4a:
		r.svc = s
		return keyConf{transit: s.conf()}
	}
	w.T.Fatalf("the key case %s is not one of a key service", spec.Keys)
	return keyConf{}
}

// beforeDisaster is called when the source is lost: the key service is not part of the source.
func (s *service) beforeDisaster() {}

// rotateService is the rotation of a key in a key service (the external key guide's section 7): a new
// version in the service, a restart of Pandokos, then the same wait for the re-wrap as with a file.
func (w *World) rotateService() {
	w.T.Helper()
	old := w.activeVersion(w.proc)
	w.service.rotate()
	w.restartSource(w.conf)
	w.refreshVersions()
	w.waitRotation(old)
}

// ServiceSnapshot takes the snapshot of the key service (GET sys/storage/raft/snapshot) and returns
// the file; the unseal share and a token are kept with it.
func (w *World) ServiceSnapshot(label string) string {
	w.T.Helper()
	return w.service.snapshot(label)
}

// ServiceRotate rotates the key in the service (a new version).
func (w *World) ServiceRotate() { w.service.rotate() }

// ServiceTrim raises min_decryption_version and trims, with the root token.
func (w *World) ServiceTrim(minVersion int) { w.service.trim(minVersion) }

// ServiceTrimAsPandokos tries the same with the token of Pandokos's policy and returns the status:
// the policy cannot.
func (w *World) ServiceTrimAsPandokos(minVersion int) int { return w.service.trimAs(minVersion) }

// ServiceDeleteKey deletes the key in the service.
func (w *World) ServiceDeleteKey() { w.service.deleteKey() }

// ServiceLose removes the service and its volumes, with no snapshot taken.
func (w *World) ServiceLose() {
	w.T.Helper()
	w.P.Remove(w.T, w.service.name)
}
