// Package fakeidp is an OpenID Connect issuer for tests: an HTTPS server on loopback whose keys
// are generated when it starts. It never reads a key or a token from disk, and nothing it makes is
// ever written to the repository.
//
// It serves the discovery document and the key set of an issuer, signs tokens (valid ones with
// go-jose, arbitrary ones by hand with the standard library) and can misbehave on request: refuse
// connections, answer slowly, answer with too much, redirect, name another issuer, be stopped and
// started again.
package fakeidp

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/pandokos/pandokos/test/testcert"
)

// Options configure Start.
type Options struct {
	Path        string // issuer path, default "/"; the issuer is https://localhost:<port><Path>
	NoDiscovery bool   // serve only the key set (for an explicit jwks_uri)
}

// Claims is the payload of a token.
type Claims map[string]any

// With returns a copy of c with the member set.
func (c Claims) With(name string, value any) Claims {
	out := Claims{}
	for k, v := range c {
		out[k] = v
	}
	out[name] = value
	return out
}

// Without returns a copy of c without the members.
func (c Claims) Without(names ...string) Claims {
	out := Claims{}
	for k, v := range c {
		out[k] = v
	}
	for _, n := range names {
		delete(out, n)
	}
	return out
}

// Algorithms are the algorithms the fake signs with, in the order of the product's allow-list.
var Algorithms = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}

// keySet is the keys of one generation.
type keySet struct {
	rsa     *rsa.PrivateKey
	p256    *ecdsa.PrivateKey
	p384    *ecdsa.PrivateKey
	p521    *ecdsa.PrivateKey
	ed      ed25519.PrivateKey
	weakRSA *rsa.PrivateKey // 1024 bits, served only with WithWeakKeys
	oct     []byte          // a symmetric key, served only with WithWeakKeys
	kids    map[string]string
}

// Server is a running issuer.
type Server struct {
	tb     testing.TB
	opt    Options
	base   string // the path of the issuer without its trailing slash
	caFile string
	cert   tls.Certificate
	addr   string // 127.0.0.1:port

	mu          sync.Mutex
	keys        *keySet
	weak        bool
	fault       Fault
	discJWKSURI *string // overrides the jwks_uri member of the discovery document
	srv         *http.Server
	running     bool

	discoveryHits atomic.Int64
	jwksHits      atomic.Int64
	bytesSent     atomic.Int64
}

// Start starts an issuer with the certificate of a throw-away authority for "localhost" and keys
// generated now. It stops with tb.
func Start(tb testing.TB, o Options) *Server {
	tb.Helper()
	if o.Path == "" {
		o.Path = "/"
	}
	if !strings.HasPrefix(o.Path, "/") {
		tb.Fatalf("fakeidp: the path %q must start with /", o.Path)
	}
	caFile, certFile, keyFile := testcert.Files(tb, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		tb.Fatal(err)
	}
	s := &Server{tb: tb, opt: o, base: strings.TrimSuffix(o.Path, "/"), caFile: caFile, cert: cert}
	s.keys = newKeySet(tb, false)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	s.addr = ln.Addr().String()
	s.serve(ln)
	tb.Cleanup(s.Stop)
	return s
}

func (s *Server) serve(ln net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	srv := &http.Server{
		Handler:           mux,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}},
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          nil,
	}
	s.mu.Lock()
	s.srv, s.running = srv, true
	s.mu.Unlock()
	go func() { _ = srv.ServeTLS(ln, "", "") }()
}

// Stop closes the listener and every connection: requests are refused until Resume. It is also
// called when the test ends.
func (s *Server) Stop() {
	s.mu.Lock()
	srv, running := s.srv, s.running
	s.running = false
	s.mu.Unlock()
	if running {
		_ = srv.Close()
	}
}

// Resume listens again on the address Stop released, with the same keys.
func (s *Server) Resume() {
	s.tb.Helper()
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		return
	}
	var ln net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if ln, err = net.Listen("tcp4", s.addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		s.tb.Fatalf("fakeidp: cannot listen again on %s: %v", s.addr, err)
	}
	s.serve(ln)
}

// Host returns "localhost:<port>".
func (s *Server) Host() string {
	_, port, _ := net.SplitHostPort(s.addr)
	return "localhost:" + port
}

// Issuer returns the issuer URL: https://localhost:<port><Path>.
func (s *Server) Issuer() string { return "https://" + s.Host() + s.opt.Path }

// JWKSURI returns the address of the key set.
func (s *Server) JWKSURI() string { return "https://" + s.Host() + s.base + "/jwks.json" }

// DiscoveryURL returns the address of the discovery document of an issuer without a trailing slash.
func (s *Server) DiscoveryURL() string {
	return "https://" + s.Host() + s.base + "/.well-known/openid-configuration"
}

// CAFile returns the path of the PEM of the authority, for upstream.tls.ca_file.
func (s *Server) CAFile() string { return s.caFile }

// Transport returns a transport that trusts the authority of the server and nothing else. It is
// for the tests that fetch without the egress guard.
func (s *Server) Transport() *http.Transport {
	pool := x509.NewCertPool()
	pem, err := readFile(s.caFile)
	if err != nil || !pool.AppendCertsFromPEM(pem) {
		s.tb.Fatalf("fakeidp: cannot load the authority: %v", err)
	}
	return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
}

// Requests returns the number of discovery and key set requests that reached the handler.
func (s *Server) Requests() (discovery, jwks int) {
	return int(s.discoveryHits.Load()), int(s.jwksHits.Load())
}

// BytesSent returns the number of bytes of document bodies the server wrote, whatever the fault.
func (s *Server) BytesSent() int64 { return s.bytesSent.Load() }

// WithWeakKeys makes the key set include an RSA key of 1024 bits and a symmetric (oct) entry.
func (s *Server) WithWeakKeys(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.weak = on
	if on && s.keys.weakRSA == nil {
		s.keys.addWeak(s.tb)
	}
}

// SetDiscoveryJWKSURI makes the discovery document name uri as the address of the key set, whatever
// it is: the tests of malformed documents use it.
func (s *Server) SetDiscoveryJWKSURI(uri string) {
	s.mu.Lock()
	s.discJWKSURI = &uri
	s.mu.Unlock()
}

// Rotate replaces every key by a new one with a new key ID: the old keys are no longer served.
func (s *Server) Rotate() {
	ks := newKeySet(s.tb, false)
	s.mu.Lock()
	if s.weak {
		ks.addWeak(s.tb)
	}
	s.keys = ks
	s.mu.Unlock()
}

// SetFault makes the server misbehave from now on; None restores it.
func (s *Server) SetFault(f Fault) {
	s.mu.Lock()
	s.fault = f
	s.mu.Unlock()
}

func newKeySet(tb testing.TB, weak bool) *keySet {
	tb.Helper()
	var err error
	ks := &keySet{kids: map[string]string{}}
	if ks.rsa, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		tb.Fatal(err)
	}
	if ks.p256, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		tb.Fatal(err)
	}
	if ks.p384, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader); err != nil {
		tb.Fatal(err)
	}
	if ks.p521, err = ecdsa.GenerateKey(elliptic.P521(), rand.Reader); err != nil {
		tb.Fatal(err)
	}
	if _, ks.ed, err = ed25519.GenerateKey(rand.Reader); err != nil {
		tb.Fatal(err)
	}
	for _, name := range []string{"rsa", "p256", "p384", "p521", "ed25519", "rsa1024", "oct"} {
		ks.kids[name] = "kid-" + name + "-" + randomHex(tb, 6)
	}
	if weak {
		ks.addWeak(tb)
	}
	return ks
}

func (ks *keySet) addWeak(tb testing.TB) {
	tb.Helper()
	var err error
	if ks.weakRSA, err = rsa.GenerateKey(rand.Reader, 1024); err != nil {
		tb.Fatal(err)
	}
	ks.oct = make([]byte, 32)
	if _, err := rand.Read(ks.oct); err != nil {
		tb.Fatal(err)
	}
}

func randomHex(tb testing.TB, n int) string {
	tb.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// kidName returns the name under which the key of alg has its key ID.
func kidName(alg string) string {
	switch alg {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512":
		return "rsa"
	case "ES256":
		return "p256"
	case "ES384":
		return "p384"
	case "ES512":
		return "p521"
	case "EdDSA":
		return "ed25519"
	}
	return ""
}

func (s *Server) current() *keySet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys
}

// KeyID returns the key ID of the key that signs with alg ("" for an algorithm the fake has no key for).
func (s *Server) KeyID(alg string) string { return s.current().kids[kidName(alg)] }

// WeakKeyID returns the key ID of the RSA key of 1024 bits (WithWeakKeys must be on to serve it).
func (s *Server) WeakKeyID() string { return s.current().kids["rsa1024"] }

// OctKeyID returns the key ID of the symmetric entry that WithWeakKeys adds.
func (s *Server) OctKeyID() string { return s.current().kids["oct"] }

// PublicKey returns the public key that verifies tokens of alg, as the key source of a unit test
// would hand it to the validation: no network is involved.
func (s *Server) PublicKey(alg string) crypto.PublicKey {
	ks := s.current()
	switch kidName(alg) {
	case "rsa":
		return &ks.rsa.PublicKey
	case "p256":
		return &ks.p256.PublicKey
	case "p384":
		return &ks.p384.PublicKey
	case "p521":
		return &ks.p521.PublicKey
	case "ed25519":
		return ks.ed.Public()
	}
	s.tb.Fatalf("fakeidp: no key for %q", alg)
	return nil
}

// PublicKeyByID returns the public key with the key ID, or nil, among the keys now served.
func (s *Server) PublicKeyByID(kid string) crypto.PublicKey {
	ks := s.current()
	for _, alg := range []string{"RS256", "ES256", "ES384", "ES512", "EdDSA"} {
		if ks.kids[kidName(alg)] == kid {
			return s.PublicKey(alg)
		}
	}
	if ks.weakRSA != nil && ks.kids["rsa1024"] == kid {
		return &ks.weakRSA.PublicKey
	}
	return nil
}

// PublicKeyPEM returns the public key of alg as PKIX PEM, and PublicKeyDER the same as DER, for
// the test that uses them as the secret of an HMAC.
func (s *Server) PublicKeyPEM(alg string) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: s.PublicKeyDER(alg)})
}

// PublicKeyDER returns the public key of alg as PKIX DER.
func (s *Server) PublicKeyDER(alg string) []byte {
	der, err := x509.MarshalPKIXPublicKey(s.PublicKey(alg))
	if err != nil {
		s.tb.Fatal(err)
	}
	return der
}

func (s *Server) signer(alg string, weak bool) any {
	ks := s.current()
	if weak {
		if ks.weakRSA == nil {
			s.WithWeakKeys(true)
			ks = s.current()
		}
		return ks.weakRSA
	}
	switch kidName(alg) {
	case "rsa":
		return ks.rsa
	case "p256":
		return ks.p256
	case "p384":
		return ks.p384
	case "p521":
		return ks.p521
	case "ed25519":
		return ks.ed
	}
	s.tb.Fatalf("fakeidp: no key to sign with %q", alg)
	return nil
}

// Standard returns the claims of an ordinary token of the issuer now: iss, sub, aud, iat, exp.
func (s *Server) Standard(sub string, aud any, ttl time.Duration) Claims {
	return s.StandardAt(time.Now(), sub, aud, ttl)
}

// StandardAt is Standard for a given instant.
func (s *Server) StandardAt(now time.Time, sub string, aud any, ttl time.Duration) Claims {
	return Claims{"iss": s.Issuer(), "sub": sub, "aud": aud, "iat": now.Unix(), "exp": now.Add(ttl).Unix()}
}

// SignOption changes how Sign makes a token.
type SignOption func(*signConfig)

type signConfig struct {
	kid     *string
	noKID   bool
	weak    bool
	headers map[string]any
}

// WithKID sets the "kid" header to kid instead of the key ID of the signing key.
func WithKID(kid string) SignOption { return func(c *signConfig) { c.kid = &kid } }

// WithoutKID leaves out the "kid" header.
func WithoutKID() SignOption { return func(c *signConfig) { c.noKID = true } }

// WithWeakKey signs with the RSA key of 1024 bits (alg must be an RS or PS algorithm) and names it
// in "kid".
func WithWeakKey() SignOption { return func(c *signConfig) { c.weak = true } }

// WithHeader adds a member to the protected header.
func WithHeader(name string, value any) SignOption {
	return func(c *signConfig) {
		if c.headers == nil {
			c.headers = map[string]any{}
		}
		c.headers[name] = value
	}
}

// Sign returns a valid compact token of the claims, signed with the key of alg and carrying its
// key ID.
func (s *Server) Sign(alg string, c Claims, o ...SignOption) string {
	s.tb.Helper()
	var cfg signConfig
	for _, f := range o {
		f(&cfg)
	}
	key := s.signer(alg, cfg.weak)
	kid := s.KeyID(alg)
	if cfg.weak {
		kid = s.WeakKeyID()
	}
	if cfg.kid != nil {
		kid = *cfg.kid
	}
	opts := (&jose.SignerOptions{}).WithType("JWT")
	if !cfg.noKID {
		opts = opts.WithHeader("kid", kid)
	}
	for k, v := range cfg.headers {
		opts = opts.WithHeader(jose.HeaderKey(k), v)
	}
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.SignatureAlgorithm(alg), Key: key}, opts)
	if err != nil {
		s.tb.Fatalf("fakeidp: %v", err)
	}
	payload, err := json.Marshal(c)
	if err != nil {
		s.tb.Fatal(err)
	}
	jws, err := sg.Sign(payload)
	if err != nil {
		s.tb.Fatal(err)
	}
	out, err := jws.CompactSerialize()
	if err != nil {
		s.tb.Fatal(err)
	}
	return out
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Raw builds a compact token from a header and a payload as given, and the signature that sign
// returns for the signing input (the first two segments joined by a dot); a nil sign gives an empty
// signature.
func (s *Server) Raw(header, payload map[string]any, sign func(signingInput []byte) []byte) string {
	s.tb.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		s.tb.Fatal(err)
	}
	p, err := json.Marshal(payload)
	if err != nil {
		s.tb.Fatal(err)
	}
	return RawJSON(h, p, sign)
}

// RawJSON is Raw for a header and a payload that are already text: the tests of duplicate members
// and of numbers written as strings need to control every byte.
func RawJSON(header, payload []byte, sign func(signingInput []byte) []byte) string {
	in := b64(header) + "." + b64(payload)
	var sig []byte
	if sign != nil {
		sig = sign([]byte(in))
	}
	return in + "." + b64(sig)
}

// SignWith returns a signing function for Raw that signs with the key of alg of the server, whatever
// the header says: RS and PS with the RSA key, ES with the curve of the algorithm, EdDSA.
func (s *Server) SignWith(alg string) func([]byte) []byte {
	s.tb.Helper()
	key := s.signer(alg, false)
	return func(in []byte) []byte { return signStd(s.tb, alg, key, in) }
}

// SignWithWeakKey is SignWith for the RSA key of 1024 bits.
func (s *Server) SignWithWeakKey(alg string) func([]byte) []byte {
	s.tb.Helper()
	key := s.signer(alg, true)
	return func(in []byte) []byte { return signStd(s.tb, alg, key, in) }
}

// SignWithKey returns a signing function for Raw that signs with a key of the caller's, for the
// tests that sign with a key of a different type than the algorithm says.
func SignWithKey(tb testing.TB, alg string, key any) func([]byte) []byte {
	return func(in []byte) []byte { return signStd(tb, alg, key, in) }
}

// HS256 returns a signing function that computes HMAC-SHA-256 with the secret.
func HS256(secret []byte) func([]byte) []byte { return hmacSigner(secret, sha256Sum) }

// HS512 returns a signing function that computes HMAC-SHA-512 with the secret.
func HS512(secret []byte) func([]byte) []byte { return hmacSigner(secret, sha512Sum) }

func signStd(tb testing.TB, alg string, key any, in []byte) []byte {
	tb.Helper()
	var hash crypto.Hash
	switch alg {
	case "RS256", "PS256", "ES256":
		hash = crypto.SHA256
	case "RS384", "PS384", "ES384":
		hash = crypto.SHA384
	case "RS512", "PS512", "ES512":
		hash = crypto.SHA512
	}
	var digest []byte
	if hash != 0 {
		h := hash.New()
		h.Write(in)
		digest = h.Sum(nil)
	}
	switch k := key.(type) {
	case *rsa.PrivateKey:
		var sig []byte
		var err error
		if strings.HasPrefix(alg, "PS") {
			sig, err = rsa.SignPSS(rand.Reader, k, hash, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		} else {
			sig, err = rsa.SignPKCS1v15(rand.Reader, k, hash, digest)
		}
		if err != nil {
			tb.Fatal(err)
		}
		return sig
	case *ecdsa.PrivateKey:
		r, s, err := ecdsa.Sign(rand.Reader, k, digest)
		if err != nil {
			tb.Fatal(err)
		}
		size := (k.Curve.Params().BitSize + 7) / 8
		out := make([]byte, 2*size)
		r.FillBytes(out[:size])
		s.FillBytes(out[size:])
		return out
	case ed25519.PrivateKey:
		return ed25519.Sign(k, in)
	}
	tb.Fatalf("fakeidp: cannot sign with a key of type %T", key)
	return nil
}

// JWKS returns the key set as it is served now (without a fault).
func (s *Server) JWKS() []byte {
	s.mu.Lock()
	ks, weak := s.keys, s.weak
	s.mu.Unlock()
	var keys []jose.JSONWebKey
	add := func(name string, pub any, alg string) {
		keys = append(keys, jose.JSONWebKey{Key: pub, KeyID: ks.kids[name], Use: "sig", Algorithm: alg})
	}
	add("rsa", &ks.rsa.PublicKey, "")
	add("p256", &ks.p256.PublicKey, "ES256")
	add("p384", &ks.p384.PublicKey, "ES384")
	add("p521", &ks.p521.PublicKey, "ES512")
	add("ed25519", ks.ed.Public(), "EdDSA")
	if weak && ks.weakRSA != nil {
		add("rsa1024", &ks.weakRSA.PublicKey, "")
		keys = append(keys, jose.JSONWebKey{Key: ks.oct, KeyID: ks.kids["oct"], Use: "sig", Algorithm: "HS256"})
	}
	out, err := json.Marshal(jose.JSONWebKeySet{Keys: keys})
	if err != nil {
		s.tb.Fatal(err)
	}
	return out
}

// Discovery returns the discovery document as it is served now (without a fault).
func (s *Server) Discovery() []byte {
	s.mu.Lock()
	override := s.discJWKSURI
	s.mu.Unlock()
	uri := s.JWKSURI()
	if override != nil {
		uri = *override
	}
	b, err := json.Marshal(map[string]any{
		"issuer":                                s.Issuer(),
		"jwks_uri":                              uri,
		"id_token_signing_alg_values_supported": Algorithms,
	})
	if err != nil {
		s.tb.Fatal(err)
	}
	return b
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var doc []byte
	var isDiscovery bool
	switch {
	case !s.opt.NoDiscovery && r.URL.Path == s.base+"/.well-known/openid-configuration":
		s.discoveryHits.Add(1)
		doc, isDiscovery = s.Discovery(), true
	case r.URL.Path == s.base+"/jwks.json":
		s.jwksHits.Add(1)
		doc = s.JWKS()
	default:
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	f := s.fault
	s.mu.Unlock()
	f.apply(s, w, r, doc, isDiscovery)
}

// bodyWriter counts what the server sends.
func (s *Server) send(w http.ResponseWriter, p []byte) error {
	n, err := w.Write(p)
	s.bytesSent.Add(int64(n))
	return err
}

func readFile(path string) ([]byte, error) {
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
