package gateway

import (
	"crypto/rand"
	"encoding/hex"
)

// NewRequestID returns a request ID: 16 random bytes in lower-case hexadecimal.
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("gateway: no randomness available: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
