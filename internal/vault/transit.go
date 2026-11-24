package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

//nolint:unused // scaffolding: the constants are used from step C2
const (
	transitName           = "transit"
	transitWrapFormat     = 0x02
	transitIDLabel        = "pandokos kek id v1"
	transitAADLabel       = "pandokos/transit-aad/v1"
	transitMaxAnswer      = 64 << 10
	transitTrip           = 5               // consecutive unavailable answers that start the refusal
	transitCooldown       = 5 * time.Second // length of the refusal
	transitWarnEvery      = 30 * time.Second
	transitDescribeMaxAge = 10 * time.Second
)

// TransitOptions configures a TransitProvider. Every formatting path prints the redaction marker.
type TransitOptions struct {
	Transport      http.RoundTripper // egress.Guard.KeyService; required
	Address        *url.URL          // required
	Namespace      string
	Mount, KeyName string // required
	AuthMethod     string // "token" or "approle"
	TokenFile      string
	AppRoleMount   string
	RoleIDFile     string
	SecretIDFile   string
	RequestTimeout time.Duration // 100 ms to 30 s
	MaxConcurrent  int           // 1 to 1024
	UserAgent      string
	Logger         *slog.Logger // required
	Now            func() time.Time
	OnRequest      func(operation string, d time.Duration) // M60; may be nil
}

// TransitProvider wraps data keys with a transit key of OpenBao or HashiCorp Vault. It holds the
// token; every formatting path prints the redaction marker.
//
//nolint:unused // scaffolding: the field is used from step C2
type TransitProvider struct {
	opts TransitOptions
}

var errNotImplemented = errors.New("not implemented")

// NewTransitProvider checks the options and contacts nothing.
func NewTransitProvider(_ TransitOptions) (*TransitProvider, error) {
	return nil, errNotImplemented
}

// Start authenticates, describes the key and identifies its latest version. It may be called
// again after a failure; it resets the refusal.
func (p *TransitProvider) Start(_ context.Context) error { return errNotImplemented }

// Run keeps the token alive until ctx ends (§5.5).
func (p *TransitProvider) Run(_ context.Context) {}

// Name returns "transit".
func (p *TransitProvider) Name() string { return transitName }

// Wrap encrypts plaintext under the pinned version of the key and binds aad.
func (p *TransitProvider) Wrap(_ context.Context, _, _ []byte) (string, []byte, error) {
	return "", nil, errNotImplemented
}

// Unwrap decrypts a value made by Wrap under version.
func (p *TransitProvider) Unwrap(_ context.Context, _ string, _, _ []byte) ([]byte, error) {
	return nil, errNotImplemented
}

// Available reports whether Unwrap can serve version now.
func (p *TransitProvider) Available(_ context.Context, _ string) error {
	return errNotImplemented
}

// Info returns what Start found.
func (p *TransitProvider) Info() TransitInfo { return TransitInfo{} }

// Stats returns a snapshot of the counters and of the token's lifetime.
func (p *TransitProvider) Stats() TransitStats { return TransitStats{} }

// TransitInfo describes the key and the authentication.
type TransitInfo struct {
	Version        string // the identifier Wrap returns
	Oldest, Newest int    // min_decryption_version and latest_version
	AuthMethod     string
	Weak           []string // from: exportable, plaintext_backup, deletion_allowed, auto_rotate, root_token
}

// TransitStats is a snapshot of the instruments of the provider.
type TransitStats struct {
	Requests     map[TransitCount]int64 // by operation and outcome (§5.14)
	Up           bool
	TokenExpires bool
	TokenTTL     time.Duration
}

// TransitCount is the key of a counter: an operation and its outcome.
type TransitCount struct{ Operation, Outcome string }

// TransitError is a failure of a request to the key service. Its text is
// "vault: key service: <operation>: <class>" and nothing else.
type TransitError struct{ Operation, Class string }

// Error returns the constant text.
func (e *TransitError) Error() string {
	return "vault: key service: " + e.Operation + ": " + e.Class
}

// Is reports ErrUnavailable always and ErrProviderUnavailable for the classes unavailable and
// refused.
func (e *TransitError) Is(target error) bool {
	switch target {
	case ErrUnavailable:
		return true
	case ErrProviderUnavailable:
		return e.Class == "unavailable" || e.Class == "refused"
	}
	return false
}

// String returns the redaction marker.
func (*TransitProvider) String() string { return secret.Redacted }

// GoString returns the redaction marker.
func (*TransitProvider) GoString() string { return secret.Redacted }

// Format writes the redaction marker for every verb.
func (*TransitProvider) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, secret.Redacted) }

// LogValue returns the redaction marker as a log value.
func (*TransitProvider) LogValue() slog.Value { return slog.StringValue(secret.Redacted) }

// MarshalJSON returns the redaction marker as a JSON string.
func (*TransitProvider) MarshalJSON() ([]byte, error) {
	return []byte(`"` + secret.Redacted + `"`), nil
}

// MarshalText returns the redaction marker.
func (*TransitProvider) MarshalText() ([]byte, error) { return []byte(secret.Redacted), nil }

// String returns the redaction marker.
func (TransitOptions) String() string { return secret.Redacted }

// GoString returns the redaction marker.
func (TransitOptions) GoString() string { return secret.Redacted }

// Format writes the redaction marker for every verb.
func (TransitOptions) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, secret.Redacted) }

// LogValue returns the redaction marker as a log value.
func (TransitOptions) LogValue() slog.Value { return slog.StringValue(secret.Redacted) }

// MarshalJSON returns the redaction marker as a JSON string.
func (TransitOptions) MarshalJSON() ([]byte, error) {
	return []byte(`"` + secret.Redacted + `"`), nil
}

// MarshalText returns the redaction marker.
func (TransitOptions) MarshalText() ([]byte, error) { return []byte(secret.Redacted), nil }
