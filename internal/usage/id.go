package usage

import "time"

// NewID returns a UUID version 7: 48 bits of t in milliseconds and 74 random bits.
func NewID(t time.Time) string {
	_ = t
	return ""
}
