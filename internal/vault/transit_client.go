package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

// statusErr is the failure of one request with the status that caused it (0: none was received).
type statusErr struct {
	status int
	err    error
}

// transitDescription is what the provider keeps of the description of the key.
type transitDescription struct {
	oldest, newest int
	raw            transitKeyInfo
}

// transitKeyInfo is the part of the description of a key that the provider reads.
type transitKeyInfo struct {
	Type                 *string         `json:"type"`
	Derived              bool            `json:"derived"`
	Exportable           bool            `json:"exportable"`
	AllowPlaintextBackup bool            `json:"allow_plaintext_backup"`
	DeletionAllowed      bool            `json:"deletion_allowed"`
	AutoRotatePeriod     json.RawMessage `json:"auto_rotate_period"`
	SoftDeleted          bool            `json:"soft_deleted"`
	SupportsEncryption   *bool           `json:"supports_encryption"`
	SupportsDecryption   *bool           `json:"supports_decryption"`
	LatestVersion        *int            `json:"latest_version"`
	MinDecryptionVersion *int            `json:"min_decryption_version"`
}

// escapePath escapes every segment of a mount path.
func escapePath(s string) string {
	segs := strings.Split(s, "/")
	for i, g := range segs {
		segs[i] = url.PathEscape(g)
	}
	return strings.Join(segs, "/")
}

// keyPath is the path under /v1/ of an operation on the key.
func (p *TransitProvider) keyPath(kind string) string {
	return escapePath(p.opts.Mount) + "/" + kind + "/" + url.PathEscape(p.opts.KeyName)
}

// do sends one request and decodes the answer into out; it returns nil or a *TransitError.
func (p *TransitProvider) do(ctx context.Context, operation, method, path string, in, out any) error {
	_, err := p.send(ctx, operation, method, path, in, out)
	return err
}

// send is do that also tells the status of the answer (400 matters to its callers). A body given as
// a []byte is sent as it is; any other is encoded as JSON. The buffer of the request is the
// caller's to clear when it holds a data key; the buffer the answer is read into is cleared here.
// The request is sent once; the one exception is the new authentication after a 403 (§5.5), after
// which it is sent once more as a request of its own (a slot and a time of its own).
func (p *TransitProvider) send(ctx context.Context, operation, method, path string, in, out any) (int, error) {
	caller := ctx
	probe, ok := p.gate.admit(p.opts.Now())
	if !ok {
		p.record(operation, "refused", 0, false)
		return 0, &TransitError{Operation: operation, Class: "refused"}
	}
	for attempt := 0; ; attempt++ {
		// Each attempt is a request of its own: its own time and its own slot. The slot is not held
		// while the new authentication runs, for that makes requests that need slots too (§5.5).
		start := time.Now()
		actx, cancel := context.WithTimeout(caller, p.opts.RequestTimeout)
		if !p.acquire(actx) {
			cancel()
			if probe {
				p.gate.release()
			}
			p.record(operation, "refused", 0, false)
			return 0, &TransitError{Operation: operation, Class: "refused"}
		}
		sent, overridden := actx.Value(tokenKey{}).(secret.Secret)
		xctx := actx
		if !overridden {
			sent = p.currentToken()
			xctx = context.WithValue(actx, tokenKey{}, sent)
		}
		status, class, errClass := p.exchange(xctx, operation, method, path, in, out)
		<-p.sem
		cancel()
		p.record(operation, class, time.Since(start), true)
		p.mu.Lock()
		p.lastClass = errClass
		p.mu.Unlock()
		p.settle(operation, class, errClass, probe, errors.Is(caller.Err(), context.Canceled))
		probe = false
		if status == http.StatusForbidden && attempt == 0 && !overridden && operation != "login" && operation != "lookup" && operation != "renew" {
			rctx, rcancel := context.WithTimeout(caller, p.opts.RequestTimeout)
			again := p.reauth(rctx, sent)
			rcancel()
			if again && caller.Err() == nil {
				continue
			}
		}
		if class == "ok" {
			return status, nil
		}
		return status, &TransitError{Operation: operation, Class: class}
	}
}

// acquire waits for a slot. It fails at the deadline of ctx; a slot that frees after nine tenths of
// the time of the call are gone is as good as none, for the request would have no time left.
func (p *TransitProvider) acquire(ctx context.Context) bool {
	start := time.Now()
	select {
	case p.sem <- struct{}{}:
		return true
	default:
	}
	select {
	case p.sem <- struct{}{}:
		if dl, ok := ctx.Deadline(); ok && time.Since(start) > 9*(time.Until(dl)+time.Since(start))/10 {
			<-p.sem
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

// settle tells the refusal what a request that was sent came to (§5.8).
func (p *TransitProvider) settle(operation, class, errClass string, probe, canceled bool) {
	if canceled && class == "unavailable" {
		// The caller gave up; that says nothing about the service.
		class = "ignored"
	}
	ev := p.gate.outcome(p.opts.Now(), probe, class)
	switch {
	case ev.tripped:
		p.opts.Logger.Warn("key provider unavailable", "kek_provider", transitName, "phase", "run",
			"operation", operation, "error_class", errClass, "failures", ev.failures)
	case ev.recovered:
		p.opts.Logger.Info("key provider recovered", "kek_provider", transitName, "phase", "run",
			"outage_seconds", int64(ev.outage/time.Second))
	}
}

// exchange makes the request and returns the status, the class of the outcome and, for a failure,
// the class of the log line (timeout, connection, tls, status, response).
func (p *TransitProvider) exchange(ctx context.Context, operation, method, path string, in, out any) (status int, class, errClass string) {
	var body []byte
	switch v := in.(type) {
	case nil:
	case []byte:
		body = v
	default:
		b, err := json.Marshal(in)
		if err != nil {
			return 0, "rejected", "response"
		}
		defer clear(b)
		body = b
	}
	target := strings.TrimRight(p.opts.Address.String(), "/") + "/v1/" + path
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return 0, "rejected", "response"
	}
	req.Header.Set("X-Vault-Request", "true")
	req.Header.Set("User-Agent", p.opts.UserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.opts.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", p.opts.Namespace)
	}
	if operation != "login" {
		if tok, ok := ctx.Value(tokenKey{}).(secret.Secret); ok {
			req.Header.Set("X-Vault-Token", tok.Reveal())
		}
	}

	resp, err := p.client.Do(req)
	if err != nil {
		if reason, ok := blockedReason(err); ok {
			// A configuration error, not an outage; the line holds the reason and nothing else.
			p.opts.Logger.Warn("egress blocked", "kek_provider", transitName, "reason", reason)
			return 0, "rejected", "connection"
		}
		return 0, "unavailable", transportClass(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The body of an error answer is read to the limit and dropped: it can repeat what was sent.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, transitMaxAnswer))
		return resp.StatusCode, statusClass(resp.StatusCode), "status"
	}
	buf, over, err := readLimited(resp.Body, transitMaxAnswer)
	defer func() { clear(buf[:cap(buf)]) }()
	if err != nil {
		return resp.StatusCode, "unavailable", transportClass(err)
	}
	if over {
		return resp.StatusCode, "rejected", "response"
	}
	if out != nil && decodeEnvelope(buf, operation, out) != nil {
		return resp.StatusCode, "rejected", "response"
	}
	return resp.StatusCode, "ok", ""
}

// decodeEnvelope decodes {"data": {...}} (or {"auth": {...}} for a login) into out, ignoring
// unknown fields. The fields of out that hold a secret are raw messages, which the caller clears.
func decodeEnvelope(buf []byte, operation string, out any) error {
	if operation == "login" || operation == "renew" {
		return json.Unmarshal(buf, &struct {
			Auth any `json:"auth"`
		}{Auth: out})
	}
	return json.Unmarshal(buf, &struct {
		Data any `json:"data"`
	}{Data: out})
}

// statusClass is the class of an answer that is not 200 (§5.8).
func statusClass(code int) string {
	switch {
	case code == http.StatusBadRequest:
		return "rejected"
	case code == http.StatusForbidden || code == http.StatusUnauthorized:
		return "denied"
	case code == http.StatusNotFound:
		return "not_found"
	case code == http.StatusTooManyRequests || code >= 500 || (code >= 300 && code < 400):
		return "unavailable"
	}
	return "rejected"
}

// transportClass is the log class of a transport error.
func transportClass(err error) string {
	var ne net.Error
	var cve *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	var al tls.AlertError
	var rh tls.RecordHeaderError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "timeout"
	case errors.As(err, &cve), errors.As(err, &ua), errors.As(err, &hn), errors.As(err, &ci), errors.As(err, &al), errors.As(err, &rh):
		return "tls"
	}
	return "connection"
}

// blockedReason reports the reason of a refusal by the egress guard; this package does not import
// the guard.
func blockedReason(err error) (string, bool) {
	var b interface{ BlockedReason() string }
	if errors.As(err, &b) {
		return b.BlockedReason(), true
	}
	return "", false
}

// readLimited reads up to limit bytes; over reports more than that. The buffers it grows out of are
// cleared; the caller clears the one it returns.
func readLimited(r io.Reader, limit int) (buf []byte, over bool, err error) {
	buf = make([]byte, 0, 1024)
	for {
		if len(buf) == cap(buf) {
			if len(buf) > limit {
				return buf, true, nil
			}
			next := make([]byte, len(buf), min(2*cap(buf), limit+1))
			copy(next, buf)
			clear(buf[:cap(buf)])
			buf = next
		}
		n, rerr := r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if rerr == io.EOF {
			return buf, len(buf) > limit, nil
		}
		if rerr != nil {
			return buf, false, rerr
		}
	}
}

// record counts a request and tells the instrument.
func (p *TransitProvider) record(operation, outcome string, d time.Duration, sent bool) {
	p.mu.Lock()
	p.counts[TransitCount{Operation: operation, Outcome: outcome}]++
	p.mu.Unlock()
	if sent && p.opts.OnRequest != nil {
		p.opts.OnRequest(operation, d)
	}
}

// currentDescription returns the description, read again when it is older than
// transitDescribeMaxAge.
func (p *TransitProvider) currentDescription(ctx context.Context) (transitDescription, error) {
	p.mu.Lock()
	d, at := p.desc, p.descAt
	p.mu.Unlock()
	if !at.IsZero() && d.newest > 0 && p.opts.Now().Sub(at) < transitDescribeMaxAge {
		return d, nil
	}
	return p.describe(ctx)
}

// describe reads the description of the key and keeps it.
func (p *TransitProvider) describe(ctx context.Context) (transitDescription, error) {
	var k transitKeyInfo
	if err := p.do(ctx, "describe", http.MethodGet, p.keyPath("keys"), nil, &k); err != nil {
		var te *TransitError
		if errors.As(err, &te) && te.Class == "not_found" {
			p.mu.Lock()
			p.desc, p.descAt = transitDescription{}, time.Time{}
			p.mu.Unlock()
			return transitDescription{}, &keyError{msg: errTransitMissing, cause: te}
		}
		return transitDescription{}, err
	}
	d, err := describeAnswer(k)
	if err != nil {
		return transitDescription{}, err
	}
	p.mu.Lock()
	p.desc, p.descAt = d, p.opts.Now()
	p.mu.Unlock()
	return d, nil
}

// keyError is a failure to find the key; its text is fixed and the failed description is its cause.
type keyError struct {
	msg   error
	cause error
}

func (e *keyError) Error() string { return e.msg.Error() }
func (e *keyError) Unwrap() error { return e.cause }

// checkKey applies the requirements of §5.3 to a description.
func (p *TransitProvider) checkKey(d transitDescription) error {
	k := d.raw
	switch {
	case d.newest < 1 || k.SoftDeleted:
		return errTransitMissing
	case *k.Type != "aes256-gcm96":
		return errTransitType
	case k.Derived:
		return errTransitDerived
	case !*k.SupportsEncryption || !*k.SupportsDecryption:
		return errTransitOperation
	case k.Exportable && !p.opts.AllowExportable:
		return errTransitExportable
	}
	return nil
}

// weakSettings lists the settings of §5.3 that are accepted with a warning.
func (p *TransitProvider) weakSettings(d transitDescription) []string {
	k := d.raw
	var w []string
	if k.Exportable {
		w = append(w, "exportable")
	}
	if k.AllowPlaintextBackup {
		w = append(w, "plaintext_backup")
	}
	if k.DeletionAllowed {
		w = append(w, "deletion_allowed")
	}
	switch strings.TrimSpace(string(k.AutoRotatePeriod)) {
	case "", "null", "0", `"0"`, `"0s"`:
	default:
		w = append(w, "auto_rotate")
	}
	if p.rootToken() {
		w = append(w, "root_token")
	}
	return w
}

// identify computes the identifier of a key version: the service computes a fingerprint of a fixed
// label under that version (§5.3).
func (p *TransitProvider) identify(ctx context.Context, n int) (string, error) {
	id, e := p.identifyStatus(ctx, n)
	if e != nil {
		return "", e.err
	}
	return id, nil
}

func (p *TransitProvider) identifyStatus(ctx context.Context, n int) (string, *statusErr) {
	in := struct {
		Input      string `json:"input"`
		KeyVersion int    `json:"key_version"`
	}{base64.StdEncoding.EncodeToString([]byte(transitIDLabel)), n}
	var out hmacData
	path := escapePath(p.opts.Mount) + "/hmac/" + url.PathEscape(p.opts.KeyName) + "/sha2-256"
	status, err := p.send(ctx, "identify", http.MethodPost, path, in, &out)
	if err != nil {
		return "", &statusErr{status, err}
	}
	id, err := identifyAnswer(out, n)
	if err != nil {
		return "", &statusErr{status, err}
	}
	return id, nil
}

// The data of the answers of the key service that the client reads. The fields that hold a secret
// are raw messages, which the caller clears.
type (
	encryptData struct {
		Ciphertext json.RawMessage `json:"ciphertext"`
		KeyVersion *int            `json:"key_version"`
	}
	decryptData struct {
		Plaintext json.RawMessage `json:"plaintext"`
	}
	hmacData struct {
		HMAC *string `json:"hmac"`
	}
	lookupData struct {
		Policies  []string     `json:"policies"`
		TTL       *json.Number `json:"ttl"`
		Renewable bool         `json:"renewable"`
	}
	renewData struct {
		Lease     *json.Number `json:"lease_duration"`
		Renewable bool         `json:"renewable"`
	}
	loginData struct {
		Raw       rawToken     `json:"client_token"`
		Policies  []string     `json:"policies"`
		Lease     *json.Number `json:"lease_duration"`
		Renewable bool         `json:"renewable"`
	}
)

// wrapAnswer checks the answer to an encrypt request for key version n and returns the payload of
// the ciphertext. It is the part of Wrap that reads the answer.
func wrapAnswer(out encryptData, n int) ([]byte, error) {
	if out.KeyVersion == nil || *out.KeyVersion != n {
		return nil, &TransitError{Operation: "wrap", Class: "rejected"}
	}
	payload, ok := transitCiphertext(out.Ciphertext, n)
	if !ok || len(payload) < 28 || len(payload) > 1023 {
		return nil, &TransitError{Operation: "wrap", Class: "rejected"}
	}
	return payload, nil
}

// unwrapAnswer returns the data key of the answer to a decrypt request. It is the part of unwrapOnce
// that reads the answer.
func unwrapAnswer(out decryptData) ([]byte, error) {
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

// describeAnswer reads the description of the key. It is the part of describe that reads the answer.
func describeAnswer(k transitKeyInfo) (transitDescription, error) {
	if k.Type == nil || k.LatestVersion == nil || k.MinDecryptionVersion == nil || k.SupportsEncryption == nil || k.SupportsDecryption == nil {
		return transitDescription{}, &TransitError{Operation: "describe", Class: "rejected"}
	}
	return transitDescription{oldest: max(*k.MinDecryptionVersion, 1), newest: *k.LatestVersion, raw: k}, nil
}

// identifyAnswer returns the identifier of key version n from the answer to an HMAC request. It is
// the part of identifyStatus that reads the answer.
func identifyAnswer(out hmacData, n int) (string, error) {
	rejected := &TransitError{Operation: "identify", Class: "rejected"}
	if out.HMAC == nil {
		return "", rejected
	}
	rest, ok := strings.CutPrefix(*out.HMAC, "vault:v"+strconv.Itoa(n)+":")
	if !ok {
		return "", rejected
	}
	mac, err := base64.StdEncoding.DecodeString(rest)
	if err != nil || len(mac) != 32 {
		return "", rejected
	}
	return "transit-" + hex.EncodeToString(mac[:8]) + "-v" + strconv.Itoa(n), nil
}

// lookupGrant reads the answer to a lookup of the token in use. It is the part of lookup that reads
// the answer.
func lookupGrant(out lookupData) (grant, error) {
	life, ok := seconds(out.TTL)
	if !ok {
		return grant{}, &TransitError{Operation: "lookup", Class: "rejected"}
	}
	pol := out.Policies
	if pol == nil {
		pol = []string{}
	}
	return grant{policies: pol, lifetime: life, renewable: out.Renewable}, nil
}

// renewGrant reads the answer to a renewal. It is the part of renew that reads the answer.
func renewGrant(out renewData) (grant, error) {
	life, ok := seconds(out.Lease)
	if !ok {
		return grant{}, &TransitError{Operation: "renew", Class: "rejected"}
	}
	return grant{lifetime: life, renewable: out.Renewable}, nil
}

// loginGrant reads the answer to an AppRole login; the token itself is out.Raw, which the caller
// clears. It is the part of login that reads the answer.
func loginGrant(out loginData) (grant, error) {
	if len(out.Raw) == 0 {
		return grant{}, &TransitError{Operation: "login", Class: "rejected"}
	}
	life, ok := seconds(out.Lease)
	if !ok {
		return grant{}, &TransitError{Operation: "login", Class: "rejected"}
	}
	pol := out.Policies
	if pol == nil {
		pol = []string{}
	}
	return grant{policies: pol, lifetime: life, renewable: out.Renewable}, nil
}
