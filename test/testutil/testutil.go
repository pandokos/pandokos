// Package testutil holds helpers shared by the tests of every level.
package testutil

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/fixtures"
)

// LogBuffer is a concurrency-safe io.Writer that collects log output.
type LogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer.
func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *LogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Lines decodes every line as a JSON object; it fails the test on a line that is not one.
func (b *LogBuffer) Lines(tb testing.TB) []map[string]any {
	tb.Helper()
	var out []map[string]any
	for _, line := range strings.Split(b.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			tb.Fatalf("log line is not a JSON object: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// Find returns the lines whose "msg" equals msg.
func (b *LogBuffer) Find(tb testing.TB, msg string) []map[string]any {
	tb.Helper()
	var out []map[string]any
	for _, m := range b.Lines(tb) {
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// Eventually polls cond every 10 ms until it returns true or d elapses; it fails the test on timeout.
func Eventually(tb testing.TB, d time.Duration, what string, cond func() bool) {
	tb.Helper()
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// WriteSecret writes value plus a newline to a new file in tb.TempDir() and returns its path.
func WriteSecret(tb testing.TB, name, value string) string {
	tb.Helper()
	p := filepath.Join(tb.TempDir(), name)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
	return p
}

// ReadEvent reads one server-sent event (up to and including the empty line) from r.
func ReadEvent(r *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		line, err := r.ReadString('\n')
		sb.WriteString(line)
		if err != nil {
			return sb.String(), err
		}
		if line == "\n" || line == "\r\n" {
			return sb.String(), nil
		}
	}
}

// AssertNoCanary fails the test if text contains any of fixtures.Canaries(); it reports which
// canary and in which named output, never the surrounding text.
func AssertNoCanary(tb testing.TB, where, text string) {
	tb.Helper()
	for i, c := range fixtures.Canaries() {
		if strings.Contains(text, c) {
			tb.Errorf("canary %d (%s...) found in %s", i, c[:len("pandokos-canary-")+4], where)
		}
	}
}

// RepoRoot returns the directory of the product module's go.mod, found by walking up from the
// working directory.
func RepoRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(b), "module github.com/pandokos/pandokos\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatal("repository root not found")
		}
		dir = parent
	}
}

// WriteCA writes a new self-signed CA certificate (ECDSA P-256, valid one hour) as PEM to a
// file in tb.TempDir() and returns its path.
func WriteCA(tb testing.TB) string {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "pandokos test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatal(err)
	}
	p := filepath.Join(tb.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		tb.Fatal(err)
	}
	return p
}

// AssertAbsent fails the test if text contains any of the given secrets; it reports the index
// of the secret and the named output, never the secret or the surrounding text.
func AssertAbsent(tb testing.TB, where, text string, secrets ...string) {
	tb.Helper()
	for i, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			tb.Errorf("secret %d found in %s", i, where)
		}
	}
}

// Static is a gateway.Authenticator for in-process tests. It is a concrete type, not
// gateway.Authenticator itself, because the tests of package gateway import this package.
type Static struct{ principals map[string]authz.Principal }

// StaticAuthenticator returns an authenticator for in-process tests: the map key is the
// presented key. Any other credential is authz.ErrUnauthenticated.
func StaticAuthenticator(principals map[string]authz.Principal) *Static {
	return &Static{principals: principals}
}

// Authenticate implements gateway.Authenticator.
func (s *Static) Authenticate(_ context.Context, presented string) (authz.Principal, error) {
	if p, ok := s.principals[presented]; ok {
		return p, nil
	}
	return authz.Principal{}, authz.ErrUnauthenticated
}

// InferencePrincipal returns an active service-account principal of a random tenant with the
// role inference, for tests that only need a caller that is allowed.
func InferencePrincipal() authz.Principal {
	var id tenant.ID
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	id[6] = id[6]&0x0f | 0x70
	id[8] = id[8]&0x3f | 0x80
	return authz.Principal{
		Kind:        authz.KindServiceAccount,
		ID:          id.String(),
		Name:        "inference-test",
		KeyID:       "t" + strings.Repeat("a", 16),
		TenantID:    id,
		TenantName:  "tenant-test",
		TenantState: tenant.StateActive,
		Roles:       []string{authz.RoleInference},
	}
}

// NewKey returns a new virtual key (authn.Generate, kind tenant) as a string.
func NewKey(tb testing.TB) string {
	tb.Helper()
	key, _, _, err := authn.Generate(authn.KindTenant)
	if err != nil {
		tb.Fatal(err)
	}
	return key.Reveal()
}

// KeyForms returns the forms of a key that must never be found in an output: the key, the 59
// characters after the second underscore, the 52-character secret, and the SHA-256 of the 32
// secret bytes in lower-case hexadecimal, standard base64 and URL-safe base64.
func KeyForms(key string) []string {
	parts := strings.SplitN(key, "_", 3)
	if len(parts) != 3 || len(parts[2]) != 59 {
		return []string{key}
	}
	raw, err := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding).DecodeString(parts[2][:52])
	if err != nil {
		return []string{key, parts[2], parts[2][:52]}
	}
	sum := sha256.Sum256(raw)
	return []string{
		key, parts[2], parts[2][:52],
		hex.EncodeToString(sum[:]),
		base64.StdEncoding.EncodeToString(sum[:]),
		base64.URLEncoding.EncodeToString(sum[:]),
	}
}

// SecretForms returns the forms of a secret that must never be found in an output: the value
// itself, its standard base64, its URL-safe base64 and its lower-case hexadecimal encoding.
func SecretForms(s string) []string {
	return []string{
		s,
		base64.StdEncoding.EncodeToString([]byte(s)),
		base64.URLEncoding.EncodeToString([]byte(s)),
		hex.EncodeToString([]byte(s)),
	}
}

// WriteKEK writes a new key-encryption key as one line to a file of mode 0600 in tb.TempDir()
// and returns its path. The key is 32 bytes from crypto/rand in standard base64 with padding,
// the content that vault.GenerateLocalKey produces and the local provider reads; it exists for
// this test only. (It does not call vault.GenerateLocalKey: this package must not import the
// vault, so that the tests of every other package build while the vault is being written;
// the provider on such a key is test/vaulttest.NewKeyProvider.)
func WriteKEK(tb testing.TB) string {
	tb.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		tb.Fatal(err)
	}
	p := filepath.Join(tb.TempDir(), "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(raw)+"\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
	return p
}
