// Package testutil holds helpers shared by the tests of every level.
package testutil

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
