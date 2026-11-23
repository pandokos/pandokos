package fakeidp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func codeFlow(t *testing.T, s *Server, verifier string) (code, redirect string) {
	t.Helper()
	sum := sha256.Sum256([]byte(verifier))
	redirect = "https://app.example.test/v1/auth/oidc/callback"
	s.RegisterRedirectURI(redirect)
	q := url.Values{
		"response_type": {"code"}, "client_id": {s.ClientID()}, "redirect_uri": {redirect}, "scope": {"openid"},
		"state": {"st"}, "nonce": {"no"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	got, loc := s.Authorize(s.AuthorizeURL() + "?" + q.Encode())
	if !strings.HasPrefix(loc, redirect+"?") || got.Get("state") != "st" || got.Get("code") == "" {
		t.Fatalf("the redirect: %s", loc)
	}
	return got.Get("code"), redirect
}

func postToken(t *testing.T, s *Server, form url.Values, user, pass string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, s.TokenURL(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := (&http.Client{Transport: s.Transport()}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func TestAuthorizationCodeFlow(t *testing.T) {
	s := Start(t, Options{Users: map[string]Claims{"ada": {"name": "Ada", "groups": []string{"admins"}}}})
	s.SignInAs("ada")
	verifier := strings.Repeat("v", 43)

	code, redirect := codeFlow(t, s, verifier)
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
	if st, _ := postToken(t, s, form, s.ClientID(), "wrong"); st != http.StatusUnauthorized {
		t.Errorf("a wrong client secret: %d", st)
	}
	st, body := postToken(t, s, form, s.ClientID(), s.ClientSecret())
	if st != http.StatusOK || body["token_type"] != "Bearer" || body["access_token"] != s.AccessToken() || body["refresh_token"] != s.RefreshToken() {
		t.Fatalf("token response %d %v", st, body)
	}
	idToken, _ := body["id_token"].(string)
	if parts := strings.Split(idToken, "."); len(parts) != 3 {
		t.Fatalf("id_token %q", idToken)
	}
	if st, _ := postToken(t, s, form, s.ClientID(), s.ClientSecret()); st != http.StatusBadRequest {
		t.Errorf("a code used twice: %d", st)
	}

	// Client authentication in the body, a wrong verifier, a wrong redirect URI.
	code, redirect = codeFlow(t, s, verifier)
	post := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {"other" + verifier}, "client_id": {s.ClientID()}, "client_secret": {s.ClientSecret()}}
	if st, _ := postToken(t, s, post, "", ""); st != http.StatusBadRequest {
		t.Errorf("a wrong verifier: %d", st)
	}
	code, redirect = codeFlow(t, s, verifier)
	post.Set("code", code)
	post.Set("code_verifier", verifier)
	post.Set("redirect_uri", redirect+"x")
	if st, _ := postToken(t, s, post, "", ""); st != http.StatusBadRequest {
		t.Errorf("a wrong redirect URI: %d", st)
	}
	code, redirect = codeFlow(t, s, verifier)
	post.Set("code", code)
	post.Set("redirect_uri", redirect)
	if st, _ := postToken(t, s, post, "", ""); st != http.StatusOK {
		t.Errorf("client_secret_post: %d", st)
	}
	if s.TokenRequests() != 6 {
		t.Errorf("%d token requests, want 6", s.TokenRequests())
	}
}

// The authorization endpoint redirects only to a registered redirect URI, compared exactly.
func TestAuthorizeRedirectURIRegistered(t *testing.T) {
	s := Start(t, Options{Users: map[string]Claims{"ada": {"name": "Ada"}}})
	s.RegisterRedirectURI("https://app.example.test/v1/auth/oidc/callback")
	sum := sha256.Sum256([]byte(strings.Repeat("v", 43)))
	for _, uri := range []string{
		"https://evil.example.test/v1/auth/oidc/callback",
		"https://app.example.test/v1/auth/oidc/callback/",
		"https://app.example.test/v1/auth/oidc/callback?next=https://evil.example.test",
		"",
	} {
		q := url.Values{
			"response_type": {"code"}, "client_id": {s.ClientID()}, "redirect_uri": {uri}, "scope": {"openid"},
			"state": {"st"}, "nonce": {"no"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		}
		client := &http.Client{Transport: s.Transport(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Get(s.AuthorizeURL() + "?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("redirect_uri %q: %d, Location %q", uri, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}
