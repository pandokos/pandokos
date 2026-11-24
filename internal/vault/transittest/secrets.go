package transittest

import (
	"encoding/base64"
	"encoding/hex"
)

// Secrets returns every plaintext and every ciphertext payload the double handled, each in raw,
// hexadecimal, standard base64 and URL base64 form, plus extra (the token and the IDs of a test).
// A canary scan searches an output for every element.
func Secrets(s *Server, extra ...string) []string {
	var out []string
	add := func(b []byte) {
		if len(b) == 0 {
			return
		}
		out = append(out, string(b), hex.EncodeToString(b),
			base64.StdEncoding.EncodeToString(b), base64.URLEncoding.EncodeToString(b),
			base64.RawStdEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b))
	}
	for _, b := range s.Plaintexts() {
		add(b)
	}
	for _, b := range s.Payloads() {
		add(b)
	}
	for _, e := range extra {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// HMACKey returns the HMAC key of a version of a key. It exists for the test of the version
// identifier only: the provider never reads it.
func (c *Scope) HMACKey(mount, name string, version int) []byte {
	c.s.tb.Helper()
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	k := c.key(mount, name)
	if k == nil || k.vers[version] == nil {
		c.s.tb.Fatalf("transittest: no such key version")
		return nil
	}
	return append([]byte(nil), k.vers[version].hmacKey...)
}
