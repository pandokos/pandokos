package usage

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewID returns a UUID version 7: 48 bits of t in milliseconds and 74 random bits, in the
// canonical lower-case form. It panics if the system's random source fails, which it does not on
// supported platforms.
func NewID(t time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[6:]); err != nil {
		panic("usage: no random source: " + err.Error())
	}
	ms := uint64(t.UnixMilli()) //nolint:gosec // the low 48 bits of the time are what the ID carries
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}
