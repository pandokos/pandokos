// Package secret provides a string type that cannot be printed, and a reader for secret files.
package secret

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Redacted is what every formatting path of a Secret produces.
const Redacted = "[REDACTED]"

const maxFileBytes = 8192

// Secret holds a sensitive string. Every formatting, logging and marshalling path yields Redacted.
type Secret struct{ v string }

// New returns a Secret holding v.
func New(v string) Secret { return Secret{v: v} }

// Reveal returns the value. It is the only way to obtain it.
func (s Secret) Reveal() string { return s.v }

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return s.v == "" }

// Equal compares the secret with candidate in constant time (it compares SHA-256 digests).
func (s Secret) Equal(candidate string) bool {
	a := sha256.Sum256([]byte(s.v))
	b := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// String returns Redacted.
func (s Secret) String() string { return Redacted }

// GoString returns Redacted.
func (s Secret) GoString() string { return Redacted }

// Format writes Redacted for every verb.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }

// LogValue returns Redacted as a log value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(Redacted) }

// MarshalText returns Redacted.
func (s Secret) MarshalText() ([]byte, error) { return []byte(Redacted), nil }

// MarshalJSON returns Redacted as a JSON string.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }

// ReadFile reads a secret from a file. Leading and trailing ASCII white space is removed.
// Errors never contain file content.
func ReadFile(path string) (Secret, error) {
	if path == "" {
		return Secret{}, errors.New("no file path given")
	}
	f, err := os.Open(path) //nolint:gosec // the path is operator configuration
	if err != nil {
		return Secret{}, fmt.Errorf("cannot read secret file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return Secret{}, fmt.Errorf("cannot read secret file: %w", err)
	}
	if len(b) > maxFileBytes {
		return Secret{}, fmt.Errorf("secret file is larger than %d bytes", maxFileBytes)
	}
	v := strings.Trim(string(b), " \t\r\n\v\f")
	if v == "" {
		return Secret{}, errors.New("secret file is empty")
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return Secret{}, errors.New("secret file contains a control character")
		}
	}
	return Secret{v: v}, nil
}
