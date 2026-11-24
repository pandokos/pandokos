package transittest

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxRequestBody = 1 << 20

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	f := s.f
	s.mu.Unlock()

	if f.drop {
		if h, ok := w.(http.Hijacker); ok {
			if c, _, err := h.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	if f.hang {
		select {
		case <-r.Context().Done():
		case <-f.releaseHng:
		}
		return
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
	}
	e := &exchange{s: s, w: w, r: r, body: body, f: f}
	switch {
	case f.sealed:
		e.fail(http.StatusServiceUnavailable, "Vault is sealed")
		return
	case f.redirect != "":
		http.Redirect(w, r, f.redirect, http.StatusTemporaryRedirect)
		return
	case f.status != 0:
		e.fail(f.status, "status "+strconv.Itoa(f.status))
		return
	case f.oversize:
		e.reply(http.StatusOK, map[string]any{"data": map[string]any{"pad": strings.Repeat("A", 70<<10)}})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e.route()
}

// exchange is one request in handling; the server's mutex is held while it runs.
type exchange struct {
	s    *Server
	w    http.ResponseWriter
	r    *http.Request
	body []byte
	f    faults
}

func (e *exchange) reply(code int, v any) {
	b, _ := json.Marshal(v)
	e.w.Header().Set("Content-Type", "application/json")
	e.w.WriteHeader(code)
	_, _ = e.w.Write(b)
}

// fail writes {"errors":[msg]}; with EchoInErrors the list repeats the token, the body and the extras.
func (e *exchange) fail(code int, msgs ...string) {
	if msgs == nil {
		msgs = []string{}
	}
	if e.f.echo {
		msgs = append(msgs, e.r.Header.Get("X-Vault-Token"), string(e.body))
		msgs = append(msgs, e.f.echoExtra...)
	}
	e.reply(code, map[string]any{"errors": msgs})
}

func (e *exchange) denied() { e.fail(http.StatusForbidden, "permission denied") }

func (e *exchange) route() {
	p := strings.TrimSuffix(strings.TrimPrefix(e.r.URL.Path, "/v1/"), "/")
	if !strings.HasPrefix(e.r.URL.Path, "/v1/") {
		e.fail(http.StatusNotFound)
		return
	}
	scope := e.s.scopes[strings.Trim(e.r.Header.Get("X-Vault-Namespace"), "/")]
	if p != "" && strings.HasPrefix(p, "auth/") && strings.HasSuffix(p, "/login") && e.r.Method != http.MethodGet {
		if scope == nil {
			e.fail(http.StatusNotFound)
			return
		}
		e.login(scope, strings.TrimSuffix(strings.TrimPrefix(p, "auth/"), "/login"))
		return
	}
	tok := e.s.tokens[e.r.Header.Get("X-Vault-Token")]
	if tok == nil || tok.revoked || (!tok.expires.IsZero() && !e.s.now().Before(tok.expires)) ||
		(scope != nil && tok.scope != scope && tok.scope.path != "") {
		e.denied()
		return
	}
	capab := "update"
	if e.r.Method == http.MethodGet {
		capab = "read"
	}
	if !tok.root {
		ok := false
		for _, pp := range tok.paths {
			if pp.Path == p && pp.Capability == capab {
				ok = true
			}
		}
		if !ok {
			e.denied()
			return
		}
	}
	if scope == nil {
		e.fail(http.StatusNotFound)
		return
	}
	switch {
	case p == "auth/token/lookup-self" && e.r.Method == http.MethodGet:
		e.lookup(tok)
	case p == "auth/token/renew-self" && e.r.Method == http.MethodPost:
		e.renew(tok)
	default:
		e.transit(scope, tok, p)
	}
}

func secs(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(math.Ceil(d.Seconds()))
}

func (e *exchange) lookup(t *token) {
	ttl := time.Duration(0)
	if !t.expires.IsZero() {
		ttl = t.expires.Sub(e.s.now())
	}
	e.reply(http.StatusOK, map[string]any{"data": map[string]any{
		"policies": t.policies, "ttl": secs(ttl), "creation_ttl": secs(t.ttl), "renewable": t.renewable, "type": "service",
	}})
}

func (e *exchange) renew(t *token) {
	if !t.renewable || t.expires.IsZero() {
		e.fail(http.StatusBadRequest, "lease is not renewable")
		return
	}
	now := e.s.now()
	exp := now.Add(t.ttl)
	var warn []string
	if t.max > 0 && exp.After(t.created.Add(t.max)) {
		exp = t.created.Add(t.max)
		warn = []string{"TTL of \"" + t.ttl.String() + "\" exceeded the effective max_ttl; TTL value is capped accordingly"}
	}
	t.expires = exp
	e.reply(http.StatusOK, map[string]any{
		"auth":     map[string]any{"client_token": t.value, "lease_duration": secs(exp.Sub(now)), "renewable": true},
		"warnings": warn,
	})
}

func (e *exchange) login(scope *Scope, mount string) {
	if !scope.amounts[mount] {
		e.denied()
		return
	}
	var in struct {
		RoleID   string `json:"role_id"`
		SecretID string `json:"secret_id"`
	}
	_ = json.Unmarshal(e.body, &in)
	ro := e.s.roles[mount+"/"+in.RoleID]
	if ro == nil || ro.scope != scope || in.SecretID == "" || ro.secretID != in.SecretID {
		e.fail(http.StatusBadRequest, "invalid role or secret ID")
		return
	}
	ttl := ro.o.TokenTTL
	if ttl == 0 {
		ttl = 32 * 24 * time.Hour
	}
	t := scope.newToken(TokenOptions{TTL: ttl, MaxTTL: ro.o.TokenMaxTTL, Renewable: true, Mount: ro.o.Mount, Key: ro.o.Key, Paths: ro.o.Paths})
	e.reply(http.StatusOK, map[string]any{"auth": map[string]any{
		"client_token": t.value, "lease_duration": secs(ttl), "renewable": true, "policies": t.policies,
	}})
}

// transit serves the four paths of the transit engine under a mount of the scope.
func (e *exchange) transit(scope *Scope, tok *token, p string) {
	mount, rest := "", ""
	for m := range scope.mounts {
		if strings.HasPrefix(p, m+"/") && len(m) > len(mount) {
			mount, rest = m, strings.TrimPrefix(p, m+"/")
		}
	}
	segs := strings.Split(rest, "/")
	if mount == "" || len(segs) < 2 {
		e.fail(http.StatusNotFound)
		return
	}
	op, name := segs[0], segs[1]
	k := scope.keys[mount+"/"+name]
	switch {
	case op == "keys" && len(segs) == 2 && e.r.Method == http.MethodGet:
		if k == nil {
			e.fail(http.StatusNotFound)
			return
		}
		e.describe(name, k)
		return
	case e.r.Method != http.MethodPost || len(segs) < 2:
		e.fail(http.StatusNotFound)
		return
	}
	if k == nil {
		if op == "encrypt" && tok.root {
			k = scope.createKey(mount, name, KeyOptions{})
		} else if op == "encrypt" {
			e.denied()
			return
		} else {
			e.fail(http.StatusBadRequest, "encryption key not found")
			return
		}
	}
	var in struct {
		Plaintext  string `json:"plaintext"`
		Ciphertext string `json:"ciphertext"`
		AAD        string `json:"associated_data"`
		Context    string `json:"context"`
		Input      string `json:"input"`
		KeyVersion int    `json:"key_version"`
	}
	if len(e.body) > 0 && json.Unmarshal(e.body, &in) != nil {
		e.fail(http.StatusBadRequest, "failed to parse request body")
		return
	}
	switch {
	case op == "encrypt" && len(segs) == 2:
		e.encrypt(k, in.Plaintext, in.AAD, in.Context, in.KeyVersion)
	case op == "decrypt" && len(segs) == 2:
		e.decrypt(k, in.Ciphertext, in.AAD, in.Context)
	case op == "hmac" && len(segs) == 3 && segs[2] == "sha2-256":
		e.hmac(k, in.Input, in.KeyVersion)
	default:
		e.fail(http.StatusNotFound)
	}
}

func (e *exchange) describe(name string, k *key) {
	keys := map[string]int64{}
	for v, kv := range k.vers {
		if v >= k.minDec {
			keys[strconv.Itoa(v)] = kv.created.Unix()
		}
	}
	e.reply(http.StatusOK, map[string]any{"data": map[string]any{
		"name": name, "type": k.opts.Type, "latest_version": k.latest,
		"min_decryption_version": k.minDec, "min_encryption_version": k.minEnc,
		"exportable": k.opts.Exportable, "allow_plaintext_backup": k.opts.PlaintextBackup,
		"deletion_allowed": k.opts.DeletionAllowed, "derived": k.opts.Derived,
		"auto_rotate_period": secs(k.opts.AutoRotate), "supports_encryption": true, "supports_decryption": !k.opts.NoDecrypt,
		"keys": keys,
	}})
}

// aad decodes associated_data: nothing when empty or when the fault says to ignore it; a refusal
// for a key that is not an AEAD.
func (e *exchange) aad(k *key, in string) (data []byte, ok bool) {
	if in == "" {
		return nil, true
	}
	if !k.aead() {
		e.fail(http.StatusBadRequest, "associated_data not supported for this key type")
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		e.fail(http.StatusBadRequest, "failed to base64-decode associated_data")
		return nil, false
	}
	if e.f.ignoreAAD {
		return nil, true
	}
	return b, true
}

func (e *exchange) context(k *key, in string) ([]byte, bool) {
	if !k.opts.Derived {
		return nil, true
	}
	if in == "" {
		e.fail(http.StatusBadRequest, "missing 'context' for key derivation; the key was marked as having derived keys")
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		e.fail(http.StatusBadRequest, "failed to base64-decode context")
		return nil, false
	}
	return b, true
}

// version checks a version asked for against the key's range; 0 is the latest.
func (e *exchange) version(k *key, v int, enc bool) (int, bool) {
	if v == 0 {
		v = k.latest
	}
	if v < 0 || v > k.latest || v < k.minDec || (enc && v < k.minEnc) {
		e.fail(http.StatusBadRequest, "invalid key version")
		return 0, false
	}
	return v, true
}

func (e *exchange) encrypt(k *key, plaintext, aadIn, ctxIn string, version int) {
	pt, err := base64.StdEncoding.DecodeString(plaintext)
	if err != nil {
		e.fail(http.StatusBadRequest, "failed to base64-decode plaintext")
		return
	}
	aad, ok := e.aad(k, aadIn)
	if !ok {
		return
	}
	ctx, ok := e.context(k, ctxIn)
	if !ok {
		return
	}
	v, ok := e.version(k, version, true)
	if !ok {
		return
	}
	g := k.gcm(k.vers[v], ctx)
	nonce := make([]byte, g.NonceSize())
	_, _ = rand.Read(nonce)
	payload := g.Seal(nonce, nonce, pt, aad)
	e.s.plains = append(e.s.plains, pt)
	e.s.payloads = append(e.s.payloads, payload)
	e.reply(http.StatusOK, map[string]any{"data": map[string]any{
		"ciphertext": fmt.Sprintf("vault:v%d:%s", v, base64.StdEncoding.EncodeToString(payload)), "key_version": v,
	}})
}

func (e *exchange) decrypt(k *key, ciphertext, aadIn, ctxIn string) {
	parts := strings.SplitN(ciphertext, ":", 3)
	if len(parts) != 3 || parts[0] != "vault" || !strings.HasPrefix(parts[1], "v") {
		e.fail(http.StatusBadRequest, "invalid ciphertext: no prefix")
		return
	}
	v, err := strconv.Atoi(parts[1][1:])
	payload, err2 := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || err2 != nil || v < 1 {
		e.fail(http.StatusBadRequest, "invalid ciphertext")
		return
	}
	e.s.payloads = append(e.s.payloads, payload)
	aad, ok := e.aad(k, aadIn)
	if !ok {
		return
	}
	ctx, ok := e.context(k, ctxIn)
	if !ok {
		return
	}
	if v > k.latest {
		e.fail(http.StatusBadRequest, "ciphertext or signature version is disallowed by policy (too new)")
		return
	}
	if v < k.minDec || k.vers[v] == nil {
		e.fail(http.StatusBadRequest, "ciphertext or signature version is disallowed by policy (too old)")
		return
	}
	g := k.gcm(k.vers[v], ctx)
	if len(payload) < g.NonceSize() {
		e.fail(http.StatusBadRequest, "invalid ciphertext")
		return
	}
	pt, err := g.Open(nil, payload[:g.NonceSize()], payload[g.NonceSize():], aad)
	if err != nil {
		e.fail(http.StatusBadRequest, "cipher: message authentication failed")
		return
	}
	e.s.plains = append(e.s.plains, pt)
	e.reply(http.StatusOK, map[string]any{"data": map[string]any{"plaintext": base64.StdEncoding.EncodeToString(pt)}})
}

func (e *exchange) hmac(k *key, input string, version int) {
	in, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		e.fail(http.StatusBadRequest, "unable to decode input as base64")
		return
	}
	v, ok := e.version(k, version, true)
	if !ok {
		return
	}
	m := hmac.New(sha256.New, k.vers[v].hmacKey)
	m.Write(in)
	e.s.plains = append(e.s.plains, in)
	e.reply(http.StatusOK, map[string]any{"data": map[string]any{
		"hmac": fmt.Sprintf("vault:v%d:%s", v, base64.StdEncoding.EncodeToString(m.Sum(nil))),
	}})
}
