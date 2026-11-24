package transittest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"strings"
	"time"
)

// Scope is a namespace of the double; "" is the root. The administration methods are what an
// operator does with another token: tests call them, the provider never does.
type Scope struct {
	s       *Server
	path    string
	mounts  map[string]bool
	amounts map[string]bool // auth mounts
	keys    map[string]*key // by mount and name
}

func newScope(s *Server, path string) *Scope {
	return &Scope{s: s, path: path, mounts: map[string]bool{}, amounts: map[string]bool{}, keys: map[string]*key{}}
}

// KeyOptions are the settings of a key at creation.
type KeyOptions struct {
	Type            string // default aes256-gcm96
	Derived         bool
	Exportable      bool
	PlaintextBackup bool
	DeletionAllowed bool
	AutoRotate      time.Duration
	NoDecrypt       bool // the description says the key does not support decryption
}

type keyVer struct {
	raw     []byte
	hmacKey []byte
	created time.Time
}

type key struct {
	opts    KeyOptions
	vers    map[int]*keyVer
	latest  int
	minDec  int // versions below cannot decrypt
	minEnc  int // 0 when unset
	trimmed int // versions below are gone; 0 when none
}

func (k *key) aead() bool {
	return k.opts.Type == "aes256-gcm96" || k.opts.Type == "chacha20-poly1305"
}

func (k *key) gcm(v *keyVer, context []byte) cipher.AEAD {
	raw := v.raw
	if k.opts.Derived {
		m := hmac.New(sha256.New, raw)
		m.Write(context)
		raw = m.Sum(nil)
	}
	b, err := aes.NewCipher(raw)
	if err != nil {
		panic(err)
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		panic(err)
	}
	return g
}

func (k *key) addVersion(now time.Time) {
	k.latest++
	raw, mac := make([]byte, 32), make([]byte, 32)
	_, _ = rand.Read(raw)
	_, _ = rand.Read(mac)
	k.vers[k.latest] = &keyVer{raw: raw, hmacKey: mac, created: now}
}

// Namespace returns a scope, creating it; "" is the root.
func (s *Server) Namespace(path string) *Scope {
	path = strings.Trim(path, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.scopes[path]
	if !ok {
		c = newScope(s, path)
		s.scopes[path] = c
	}
	return c
}

// Mount enables a transit engine at path.
func (c *Scope) Mount(path string) {
	c.s.mu.Lock()
	c.mounts[strings.Trim(path, "/")] = true
	c.s.mu.Unlock()
}

// CreateKey creates a key at version 1; the mount is created when it does not exist.
func (c *Scope) CreateKey(mount, name string, o KeyOptions) {
	c.s.tb.Helper()
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.createKey(mount, name, o)
}

// HasKey reports whether the key exists.
func (c *Scope) HasKey(mount, name string) bool {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	return c.keys[mount+"/"+name] != nil
}

func (c *Scope) createKey(mount, name string, o KeyOptions) *key {
	if o.Type == "" {
		o.Type = "aes256-gcm96"
	}
	c.mounts[mount] = true
	k := &key{opts: o, vers: map[int]*keyVer{}, minDec: 1}
	k.addVersion(c.s.now())
	c.keys[mount+"/"+name] = k
	return k
}

func (c *Scope) key(mount, name string) *key {
	c.s.tb.Helper()
	k := c.keys[mount+"/"+name]
	if k == nil {
		c.s.tb.Fatalf("transittest: no key %s/%s", mount, name)
	}
	return k
}

// Alias makes the key mount/name of c reachable as toMount/toName in the scope to, with the same
// versions and the same key material: one key reached by another mount, name or namespace. It
// exists for the test of the version identifier; tokens for the new path are made with Token.
func (c *Scope) Alias(mount, name string, to *Scope, toMount, toName string) {
	c.s.tb.Helper()
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	k := c.key(mount, name)
	to.mounts[strings.Trim(toMount, "/")] = true
	to.keys[strings.Trim(toMount, "/")+"/"+toName] = k
}

// Rotate adds a version and makes it the latest.
func (c *Scope) Rotate(mount, name string) {
	c.s.tb.Helper()
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.key(mount, name).addVersion(c.s.now())
}

// Configure sets min_decryption_version and min_encryption_version (0 leaves the latter unset).
func (c *Scope) Configure(mount, name string, minDecryption, minEncryption int) {
	c.s.tb.Helper()
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	k := c.key(mount, name)
	if minDecryption < 1 || minDecryption > k.latest || minEncryption < 0 || minEncryption > k.latest ||
		(minEncryption != 0 && minEncryption < minDecryption) || minDecryption < k.trimmed {
		c.s.tb.Fatalf("transittest: configure %d, %d is refused for %s/%s", minDecryption, minEncryption, mount, name)
	}
	k.minDec, k.minEnc = minDecryption, minEncryption
}

// Trim removes the versions below minAvailable for good. It needs min_encryption_version set to
// minAvailable or more, as the services do.
func (c *Scope) Trim(mount, name string, minAvailable int) {
	c.s.tb.Helper()
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	k := c.key(mount, name)
	if minAvailable < 1 || k.minEnc < minAvailable {
		c.s.tb.Fatalf("transittest: trim to %d needs min_encryption_version of %d or more", minAvailable, minAvailable)
	}
	for v := range k.vers {
		if v < minAvailable {
			delete(k.vers, v)
		}
	}
	k.trimmed = minAvailable
	if k.minDec < minAvailable {
		k.minDec = minAvailable
	}
}

// DeleteKey removes a key; CreateKey again makes a new key with the same name.
func (c *Scope) DeleteKey(mount, name string) {
	c.s.tb.Helper()
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.key(mount, name)
	delete(c.keys, mount+"/"+name)
}

// Path is a path a token may use and the capability it has there ("update" for POST, "read" for GET).
type Path struct{ Path, Capability string }

// TokenOptions describes a token. Mount and Key (default transit and pandokos) give the paths of
// the least-privilege policy; Paths, when not nil, replace them.
type TokenOptions struct {
	TTL, MaxTTL time.Duration // TTL 0: the token never expires
	Renewable   bool
	Mount, Key  string
	Root        bool // a root token: every path, and an encrypt on a missing key creates it
	NoLookup    bool // without the default policy: no lookup-self and no renew-self
	Paths       []Path
}

type token struct {
	value     string
	scope     *Scope
	root      bool
	paths     []Path
	policies  []string
	ttl, max  time.Duration
	renewable bool
	created   time.Time
	expires   time.Time // zero: never
	revoked   bool
}

func policyPaths(mount, key string, noLookup bool) []Path {
	if mount == "" {
		mount = "transit"
	}
	if key == "" {
		key = "pandokos"
	}
	p := []Path{
		{mount + "/encrypt/" + key, "update"},
		{mount + "/decrypt/" + key, "update"},
		{mount + "/hmac/" + key + "/sha2-256", "update"},
		{mount + "/keys/" + key, "read"},
	}
	if !noLookup {
		p = append(p, Path{"auth/token/lookup-self", "read"}, Path{"auth/token/renew-self", "update"})
	}
	return p
}

func (c *Scope) newToken(o TokenOptions) *token {
	t := &token{
		value: "hvs." + randomHex(16), scope: c, root: o.Root, ttl: o.TTL, max: o.MaxTTL,
		renewable: o.Renewable, created: c.s.now(), policies: []string{"default", "pandokos"},
	}
	if o.NoLookup {
		t.policies = []string{"pandokos"}
	}
	t.paths = o.Paths
	if t.paths == nil {
		t.paths = policyPaths(o.Mount, o.Key, o.NoLookup)
	}
	if o.Root {
		t.policies = []string{"root"}
	}
	if o.TTL > 0 {
		t.expires = t.created.Add(o.TTL)
	}
	c.s.tokens[t.value] = t
	return t
}

// Token creates a token and returns it.
func (c *Scope) Token(o TokenOptions) string {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	return c.newToken(o).value
}

// RevokeToken ends a token; it answers 403 from then on.
func (c *Scope) RevokeToken(tok string) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if t := c.s.tokens[tok]; t != nil {
		t.revoked = true
	}
}

// RoleOptions describe an AppRole and the tokens its login gives (renewable, with the paths of
// the least-privilege policy for Mount and Key, or Paths).
type RoleOptions struct {
	TokenTTL, TokenMaxTTL time.Duration // TokenTTL 0 is 32 days
	Mount, Key            string
	Paths                 []Path
}

type role struct {
	scope    *Scope
	mount    string
	roleID   string
	secretID string
	o        RoleOptions
}

// AppRole enables the AppRole method at mount, makes a role and returns its role ID and its first
// secret ID.
func (c *Scope) AppRole(mount string, o RoleOptions) (roleID, secretID string) {
	mount = strings.Trim(mount, "/")
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.amounts[mount] = true
	r := &role{scope: c, mount: mount, roleID: "role-" + randomHex(8), secretID: "secret-" + randomHex(8), o: o}
	c.s.roles[mount+"/"+r.roleID] = r
	return r.roleID, r.secretID
}

// NewSecretID makes a new secret ID for the role; the old one stops working.
func (c *Scope) NewSecretID(mount, roleID string) string {
	c.s.tb.Helper()
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	r := c.s.roles[strings.Trim(mount, "/")+"/"+roleID]
	if r == nil {
		c.s.tb.Fatalf("transittest: no such role")
		return ""
	}
	r.secretID = "secret-" + randomHex(8)
	return r.secretID
}
