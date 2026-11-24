package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

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

//nolint:unused // used from step C4
var _ = [...]any{transitTrip, transitCooldown}

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
type TransitProvider struct {
	opts   TransitOptions
	client *http.Client
	sem    chan struct{}
	polled bool // the clock is injected: the lifecycle polls it

	tokMu     sync.RWMutex
	token     secret.Secret
	policies  []string
	granted   time.Duration // the lifetime of the latest grant
	first     time.Duration // the lifetime of the first grant of this token
	obtained  time.Time
	renewable bool
	expires   bool
	gen       uint64 // changes with every grant

	wake chan struct{}

	authMu   sync.Mutex // the new authentication after a 403
	reauthCh chan struct{}
	reauthAt time.Time

	lmu          sync.Mutex // the rate limits of the log lines
	lastExpiring time.Time
	lastAuthLog  time.Time
	authFails    int

	mu         sync.Mutex
	desc       transitDescription
	descAt     time.Time
	descOnFail time.Time // the last description read because of a 400 to decrypt
	version    string
	pinned     int
	weak       []string
	verified   map[string]int
	counts     map[TransitCount]int64
	lastClass  string
}

var reTransitID = regexp.MustCompile(`^transit-([0-9a-f]{16})-v([1-9][0-9]{0,8})$`)

// Fixed texts of the requirements on the key; they name a setting and never the key.
var (
	errTransitType      = errors.New("vault: the transit key (vault.kek.transit.key_name) must be of type aes256-gcm96")
	errTransitDerived   = errors.New("vault: the transit key (vault.kek.transit.key_name) must not be derived")
	errTransitMissing   = errors.New("vault: the transit key (vault.kek.transit.key_name) does not exist")
	errTransitOperation = errors.New("vault: the transit key (vault.kek.transit.key_name) does not support encryption and decryption")
	errTransitNotReady  = errors.New("vault: the transit key provider has not started")
)

// NewTransitProvider checks the options and contacts nothing.
func NewTransitProvider(o TransitOptions) (*TransitProvider, error) {
	switch {
	case o.Transport == nil:
		return nil, errors.New("vault: the transit provider needs a transport")
	case o.Logger == nil:
		return nil, errors.New("vault: the transit provider needs a logger")
	case o.Address == nil || o.Address.Host == "" || o.Address.User != nil:
		return nil, errors.New("vault: vault.kek.transit.address must be a URL with a host and no user information")
	case o.Address.Scheme != "https" && (o.Address.Scheme != "http" || !transitLoopback(o.Address.Hostname())):
		return nil, errors.New("vault: vault.kek.transit.address must be https, or http for a loopback host")
	case !transitPathOK(o.Mount):
		return nil, errors.New("vault: vault.kek.transit.mount is not a valid mount path")
	case !transitPathOK(o.KeyName) || strings.Contains(o.KeyName, "/"):
		return nil, errors.New("vault: vault.kek.transit.key_name is not a valid key name")
	}
	switch o.AuthMethod {
	case "token":
		if o.TokenFile == "" {
			return nil, errors.New("vault: vault.kek.transit.auth.token_file is required with the method token")
		}
	case "approle":
		if o.RoleIDFile == "" || o.SecretIDFile == "" || !transitPathOK(o.AppRoleMount) {
			return nil, errors.New("vault: the AppRole mount and files are required with the method approle")
		}
	default:
		return nil, errors.New("vault: vault.kek.transit.auth.method must be token or approle")
	}
	if o.RequestTimeout == 0 {
		o.RequestTimeout = 2 * time.Second
	}
	if o.RequestTimeout < 100*time.Millisecond || o.RequestTimeout > 30*time.Second {
		return nil, errors.New("vault: vault.kek.transit.request_timeout must be from 100ms to 30s")
	}
	if o.MaxConcurrent == 0 {
		o.MaxConcurrent = 32
	}
	if o.MaxConcurrent < 1 || o.MaxConcurrent > 1024 {
		return nil, errors.New("vault: vault.kek.transit.max_concurrent_requests must be from 1 to 1024")
	}
	if o.UserAgent == "" {
		o.UserAgent = "pandokos"
	}
	polled := o.Now != nil
	if o.Now == nil {
		o.Now = time.Now
	}
	a := *o.Address
	o.Address = &a
	return &TransitProvider{
		opts: o,
		client: &http.Client{
			Transport:     o.Transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		sem:      make(chan struct{}, o.MaxConcurrent),
		polled:   polled,
		wake:     make(chan struct{}, 1),
		verified: map[string]int{},
		counts:   map[TransitCount]int64{},
	}, nil
}

func transitLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// transitPathOK reports whether every segment of a mount or a name is usable in a URL path.
func transitPathOK(s string) bool {
	if s == "" {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return !strings.ContainsAny(s, "\\?#%\x00") && strings.TrimSpace(s) == s
}

// Start authenticates, describes the key and identifies its latest version. It may be called
// again after a failure; it resets the refusal.
func (p *TransitProvider) Start(ctx context.Context) error {
	if err := p.authenticate(ctx); err != nil {
		return err
	}
	d, err := p.describe(ctx)
	if err != nil {
		return err
	}
	if err := p.checkKey(d); err != nil {
		return err
	}
	id, err := p.identify(ctx, d.newest)
	if err != nil {
		return err
	}
	weak := p.weakSettings(d)
	p.mu.Lock()
	p.version, p.pinned = id, d.newest
	p.verified[id] = d.newest
	p.weak = weak
	p.mu.Unlock()
	for _, w := range weak {
		p.opts.Logger.Warn("key provider configuration weak", "kek_provider", transitName, "setting", w)
	}
	p.opts.Logger.Info("key provider ready", "kek_provider", transitName, "kek_version", id,
		"auth_method", p.opts.AuthMethod, "oldest_version", d.oldest, "newest_version", d.newest)
	return nil
}

// Name returns "transit".
func (p *TransitProvider) Name() string { return transitName }

// aadField is the associated data as the service gets them: the base64 of a digest (§5.2).
func aadField(aad []byte) string {
	h := sha256.New()
	h.Write([]byte(transitAADLabel))
	h.Write([]byte{0})
	h.Write(aad)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// Wrap encrypts plaintext under the pinned version of the key and binds aad.
func (p *TransitProvider) Wrap(ctx context.Context, plaintext, aad []byte) (string, []byte, error) {
	p.mu.Lock()
	version, n := p.version, p.pinned
	p.mu.Unlock()
	if version == "" {
		return "", nil, errTransitNotReady
	}
	// The body is built by hand in one buffer, so that the base64 of the data key is never a string
	// and the buffer can be cleared.
	a := aadField(aad)
	buf := make([]byte, 0, len(`{"plaintext":"","associated_data":"","key_version":}`)+base64.StdEncoding.EncodedLen(len(plaintext))+len(a)+10)
	buf = append(buf, `{"plaintext":"`...)
	buf = buf[:len(buf)+base64.StdEncoding.EncodedLen(len(plaintext))]
	base64.StdEncoding.Encode(buf[len(buf)-base64.StdEncoding.EncodedLen(len(plaintext)):], plaintext)
	buf = append(buf, `","associated_data":"`...)
	buf = append(buf, a...)
	buf = append(buf, `","key_version":`...)
	buf = strconv.AppendInt(buf, int64(n), 10)
	buf = append(buf, '}')
	defer clear(buf)

	var out struct {
		Ciphertext json.RawMessage `json:"ciphertext"`
		KeyVersion *int            `json:"key_version"`
	}
	if _, err := p.send(ctx, "wrap", http.MethodPost, p.keyPath("encrypt"), buf, &out); err != nil {
		return "", nil, err
	}
	if out.KeyVersion == nil || *out.KeyVersion != n {
		return "", nil, &TransitError{Operation: "wrap", Class: "rejected"}
	}
	payload, ok := transitCiphertext(out.Ciphertext, n)
	if !ok || len(payload) < 28 || len(payload) > 1023 {
		return "", nil, &TransitError{Operation: "wrap", Class: "rejected"}
	}
	return version, append([]byte{transitWrapFormat}, payload...), nil
}

// transitCiphertext decodes the quoted ciphertext "vault:v<n>:<base64>" of an answer.
func transitCiphertext(raw json.RawMessage, n int) ([]byte, bool) {
	inner, ok := transitUnquote(raw)
	if !ok {
		return nil, false
	}
	prefix := "vault:v" + strconv.Itoa(n) + ":"
	if len(inner) <= len(prefix) || string(inner[:len(prefix)]) != prefix {
		return nil, false
	}
	return transitDecode(inner[len(prefix):])
}

// transitUnquote returns the inside of a JSON string that holds base64 text: no escape can appear.
func transitUnquote(raw []byte) ([]byte, bool) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' || bytes.IndexByte(raw, '\\') >= 0 {
		return nil, false
	}
	return raw[1 : len(raw)-1], true
}

// transitDecode decodes standard base64 into a fresh slice.
func transitDecode(b []byte) ([]byte, bool) {
	out := make([]byte, base64.StdEncoding.DecodedLen(len(b)))
	n, err := base64.StdEncoding.Decode(out, b)
	if err != nil {
		clear(out)
		return nil, false
	}
	return out[:n], true
}

// Unwrap decrypts a value made by Wrap under version.
func (p *TransitProvider) Unwrap(ctx context.Context, version string, wrapped, aad []byte) ([]byte, error) {
	if len(wrapped) < 29 || len(wrapped) > 1024 || wrapped[0] != transitWrapFormat {
		return nil, ErrUnwrap
	}
	n, err := p.resolve(ctx, version)
	if err != nil {
		return nil, err
	}
	in := struct {
		Ciphertext string `json:"ciphertext"`
		AAD        string `json:"associated_data"`
	}{"vault:v" + strconv.Itoa(n) + ":" + base64.StdEncoding.EncodeToString(wrapped[1:]), aadField(aad)}
	var out struct {
		Plaintext json.RawMessage `json:"plaintext"`
	}
	status, err := p.send(ctx, "unwrap", http.MethodPost, p.keyPath("decrypt"), in, &out)
	defer func() { clear(out.Plaintext) }()
	if err != nil {
		if status == http.StatusBadRequest {
			return nil, p.afterBadDecrypt(ctx, n)
		}
		return nil, err
	}
	inner, ok := transitUnquote(out.Plaintext)
	if !ok {
		return nil, &TransitError{Operation: "unwrap", Class: "rejected"}
	}
	dk, ok := transitDecode(inner)
	if !ok || len(dk) != dataKeyLen {
		clear(dk)
		return nil, &TransitError{Operation: "unwrap", Class: "rejected"}
	}
	return dk, nil
}

// afterBadDecrypt answers a 400 to decrypt: a version that a fresh description puts out of range is
// unknown; anything else is the wrong associated data or damage. The description is read again at
// most once per transitDescribeMaxAge for this purpose.
func (p *TransitProvider) afterBadDecrypt(ctx context.Context, n int) error {
	p.mu.Lock()
	now := p.opts.Now()
	again := p.descOnFail.IsZero() || now.Sub(p.descOnFail) >= transitDescribeMaxAge
	if again {
		p.descOnFail = now
	}
	p.mu.Unlock()
	if !again {
		return ErrUnwrap
	}
	d, err := p.describe(ctx)
	if err != nil {
		return ErrUnwrap
	}
	if n < d.oldest || n > d.newest {
		return ErrKeyVersionUnknown
	}
	return ErrUnwrap
}

// Available reports whether Unwrap can serve version now.
func (p *TransitProvider) Available(ctx context.Context, version string) error {
	_, err := p.resolve(ctx, version)
	return err
}

// resolve turns an identifier into the key version it names, after checking that the service holds
// that version and that its fingerprint is the identifier's.
func (p *TransitProvider) resolve(ctx context.Context, version string) (int, error) {
	m := reTransitID.FindStringSubmatch(version)
	if m == nil {
		return 0, ErrKeyVersionUnknown
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, ErrKeyVersionUnknown
	}
	d, err := p.currentDescription(ctx)
	if err != nil {
		var te *TransitError
		if errors.As(err, &te) && te.Class == "not_found" {
			return 0, ErrKeyVersionUnknown
		}
		return 0, err
	}
	if n < d.oldest || n > d.newest {
		return 0, ErrKeyVersionUnknown
	}
	p.mu.Lock()
	_, known := p.verified[version]
	p.mu.Unlock()
	if known {
		return n, nil
	}
	id, se := p.identifyStatus(ctx, n)
	if se != nil {
		if se.status == http.StatusBadRequest {
			return 0, ErrKeyVersionUnknown
		}
		return 0, se.err
	}
	if subtle.ConstantTimeCompare([]byte(id), []byte(version)) != 1 {
		return 0, ErrKeyVersionUnknown
	}
	p.mu.Lock()
	p.verified[version] = n
	p.mu.Unlock()
	return n, nil
}

// Info returns what Start found.
func (p *TransitProvider) Info() TransitInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return TransitInfo{
		Version: p.version, Oldest: p.desc.oldest, Newest: p.desc.newest, AuthMethod: p.opts.AuthMethod,
		Weak: append([]string(nil), p.weak...),
	}
}

// Stats returns a snapshot of the counters and of the token's lifetime.
func (p *TransitProvider) Stats() TransitStats {
	p.mu.Lock()
	m := make(map[TransitCount]int64, len(p.counts))
	for k, v := range p.counts {
		m[k] = v
	}
	p.mu.Unlock()
	st := TransitStats{Requests: m, Up: true}
	st.TokenTTL, st.TokenExpires = p.tokenLeft()
	return st
}

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
