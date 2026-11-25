package instance

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/pandokos/pandokos/test/testutil"
)

// Canaries is a set of secrets that no output may hold: credentials, keys, passwords, signing
// secrets, key-service tokens. A failure names the source and the index of the secret, never the
// secret. A nil *Canaries scans nothing.
type Canaries struct {
	mu      sync.Mutex
	secrets [][]byte // every registered secret in its forms
	owner   []int    // the index of the registered secret of each form
	n       int
	scanned int
}

// Add registers a secret with its derived forms (base64 and hexadecimal) and returns its index.
func (c *Canaries) Add(secret string) int {
	if c == nil || secret == "" {
		return -1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range testutil.SecretForms(secret) {
		c.secrets = append(c.secrets, []byte(f))
		c.owner = append(c.owner, c.n)
	}
	c.n++
	return c.n - 1
}

// Scan fails the test for each registered secret found in any of the outputs of the named source.
func (c *Canaries) Scan(tb testing.TB, source string, outputs ...[]byte) {
	tb.Helper()
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scanned++
	reported := map[int]bool{}
	for _, out := range outputs {
		for i, s := range c.secrets {
			if len(s) > 0 && bytes.Contains(out, s) && !reported[c.owner[i]] {
				reported[c.owner[i]] = true
				tb.Errorf("canary %d found in %s", c.owner[i], source)
			}
		}
	}
}

// Scanned is the number of outputs scanned so far: a test that must prove its outputs were
// registered compares it with what it expects.
func (c *Canaries) Scanned() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scanned
}

func decodeLine(line string) map[string]any {
	if !strings.HasPrefix(line, "{") {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return nil
	}
	return m
}
