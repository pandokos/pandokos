// Package requestid generates the request IDs shared by both listeners, the audit log and the
// log lines.
package requestid

import (
	"crypto/rand"
	"encoding/hex"
)

// Header is the response header that carries the request ID on both listeners.
const Header = "X-Pandokos-Request-Id"

// New returns 16 random bytes as 32 lower-case hexadecimal characters.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("requestid: no randomness available: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
