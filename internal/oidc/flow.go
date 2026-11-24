package oidc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/secret"
)

const maxTokenResponse = 64 << 10

var b64 = base64.RawURLEncoding

// derive is HMAC-SHA-256 keyed by the seed's bytes over the label (section 5.8), in unpadded
// base64url.
func derive(key []byte, label string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(label))
	return b64.EncodeToString(h.Sum(nil))
}

// seedKey decodes the seed of the binding cookie: 32 bytes in unpadded base64url.
func seedKey(seed secret.Secret) ([]byte, bool) {
	key, err := b64.DecodeString(seed.Reveal())
	return key, err == nil && len(key) == 32
}

func unavailable(what string) error {
	return fmt.Errorf("%w: identity provider: %s", authz.ErrUnavailable, what)
}

// Start returns the address of the authorization endpoint, the seed of the binding cookie and the
// hash of the state that the transaction is stored under. Nothing secret is kept: state, nonce and
// verifier are functions of the seed.
func (p *Provider) Start(ctx context.Context) (redirect string, seed secret.Secret, stateHash [32]byte, err error) {
	md, err := p.o.Cache.Metadata(ctx, p.o.Issuer)
	if err != nil {
		return "", secret.Secret{}, stateHash, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", secret.Secret{}, stateHash, fmt.Errorf("oidc: random seed: %w", err)
	}
	state := derive(raw, "state")
	challenge := sha256.Sum256([]byte(derive(raw, "pkce")))
	u, err := url.Parse(md.AuthorizationEndpoint)
	if err != nil {
		return "", secret.Secret{}, stateHash, unavailable("the authorization endpoint")
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.o.ClientID)
	q.Set("redirect_uri", p.o.RedirectURI)
	q.Set("scope", strings.Join(p.o.Scopes, " "))
	q.Set("state", state)
	q.Set("nonce", derive(raw, "nonce"))
	q.Set("code_challenge", b64.EncodeToString(challenge[:]))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), secret.New(b64.EncodeToString(raw)), sha256.Sum256([]byte(state)), nil
}

// StateHash checks the state of a callback against the seed of the browser's cookie, in constant
// time, and returns the hash that the transaction of the start is stored under. A refusal is a
// *Failure (state_mismatch).
func StateHash(seed secret.Secret, query url.Values) ([32]byte, error) {
	key, ok := seedKey(seed)
	if !ok {
		return [32]byte{}, &Failure{Reason: "state_mismatch"}
	}
	state := query.Get("state")
	if !hmac.Equal([]byte(state), []byte(derive(key, "state"))) {
		return [32]byte{}, &Failure{Reason: "state_mismatch"}
	}
	return sha256.Sum256([]byte(state)), nil
}

// Complete exchanges the code of the callback query and checks the ID token; a refusal is a
// *Failure, an outage an error that wraps authz.ErrUnavailable. No token is returned or logged.
func (p *Provider) Complete(ctx context.Context, seed secret.Secret, query url.Values) (Identity, error) {
	if _, err := StateHash(seed, query); err != nil {
		return Identity{}, err
	}
	key, _ := seedKey(seed)
	if query.Get("error") != "" {
		return Identity{}, &Failure{Reason: "provider_error"}
	}
	if iss, present := query["iss"]; present && (len(iss) != 1 || iss[0] != p.o.Issuer) {
		return Identity{}, &Failure{Reason: "provider_error"}
	}
	code := query.Get("code")
	if code == "" {
		return Identity{}, &Failure{Reason: "code_rejected"}
	}
	md, err := p.o.Cache.Metadata(ctx, p.o.Issuer)
	if err != nil {
		return Identity{}, err
	}
	idToken, err := p.redeem(ctx, md.TokenEndpoint, code, derive(key, "pkce"))
	if err != nil {
		return Identity{}, err
	}
	return p.identity(ctx, md.JWKSURI, idToken, derive(key, "nonce"))
}

// parseTokenAnswer reads the body of the token endpoint's answer: the ID token, when the body is a
// JSON object with a non-empty string id_token. The pure part of redeem.
func parseTokenAnswer(body []byte) (idToken string, ok bool) {
	var answer struct {
		IDToken string `json:"id_token"`
	}
	if json.Unmarshal(body, &answer) != nil || answer.IDToken == "" {
		return "", false
	}
	return answer.IDToken, true
}

// redeem sends the code to the token endpoint and returns the ID token of the answer. The access
// token and any refresh token are never read.
func (p *Provider) redeem(ctx context.Context, endpoint, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.o.RedirectURI}, "code_verifier": {verifier},
	}
	if p.o.ClientAuth == "post" {
		form.Set("client_id", p.o.ClientID)
		form.Set("client_secret", p.o.ClientSecret.Reveal())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", unavailable("the token endpoint")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if p.o.ClientAuth != "post" {
		req.SetBasicAuth(url.QueryEscape(p.o.ClientID), url.QueryEscape(p.o.ClientSecret.Reveal()))
	}
	resp, err := p.o.HTTP.Do(req)
	if err != nil {
		p.o.Logger.Warn("token endpoint not reached")
		return "", unavailable("the token endpoint is not reachable")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		p.o.Logger.Warn("token endpoint failed", "status", resp.StatusCode)
		return "", unavailable("the token endpoint failed")
	case resp.StatusCode != http.StatusOK:
		return "", &Failure{Reason: "code_rejected"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", unavailable("the token endpoint is not reachable")
		}
		return "", &Failure{Reason: "code_rejected"}
	}
	if len(body) > maxTokenResponse {
		return "", &Failure{Reason: "code_rejected"}
	}
	idToken, ok := parseTokenAnswer(body)
	if !ok {
		return "", &Failure{Reason: "code_rejected"}
	}
	return idToken, nil
}
