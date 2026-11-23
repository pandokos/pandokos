package outbox

import (
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

// Key is a signing key of a destination: 32 bytes. It prints nothing but the redaction marker on
// every formatting path.
type Key struct{ b [32]byte } //nolint:unused // filled by NewKey and ParseKey (plan step C2)

// NewKey returns a key of 32 random bytes.
func NewKey() (Key, error) { return Key{}, errNotBuilt }

// ParseKey reads the text form of a key: "whsec_" and the standard, padded base64 of 32 bytes.
func ParseKey(text secret.Secret) (Key, error) {
	_ = text
	return Key{}, errNotBuilt
}

// Text returns the text form of the key.
func (k Key) Text() secret.Secret { return secret.Secret{} }

// String returns the redaction marker.
func (Key) String() string { return secret.Redacted }

// GoString returns the redaction marker.
func (Key) GoString() string { return secret.Redacted }

// Format writes the redaction marker for every verb.
func (Key) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, secret.Redacted) }

// LogValue returns the redaction marker.
func (Key) LogValue() slog.Value { return slog.StringValue(secret.Redacted) }

// MarshalText returns the redaction marker.
func (Key) MarshalText() ([]byte, error) { return []byte(secret.Redacted), nil }

// MarshalJSON returns the redaction marker.
func (Key) MarshalJSON() ([]byte, error) { return []byte(`"` + secret.Redacted + `"`), nil }

// Sign returns the timestamp header and the signature header of a request that carries the event
// id at the time at with the given body, signed with every key (sprint 6.4, section 5.9).
func Sign(id string, at time.Time, body []byte, keys ...Key) (timestamp, signature string) {
	_, _, _, _ = id, at, body, keys
	return "", ""
}
