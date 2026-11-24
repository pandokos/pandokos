package vault

import (
	"context"
	"net/http"
	"slices"

	"github.com/pandokos/pandokos/internal/secret"
)

// tokenValue returns the token for the header of a request. It is revealed here and nowhere else.
func (p *TransitProvider) tokenValue() string {
	p.tokMu.RLock()
	defer p.tokMu.RUnlock()
	return p.token.Reveal()
}

// rootToken reports whether the policies of the token include root.
func (p *TransitProvider) rootToken() bool {
	p.tokMu.RLock()
	defer p.tokMu.RUnlock()
	return slices.Contains(p.policies, "root")
}

// authenticate obtains the token: it reads it from its file and looks it up, or logs in with the
// AppRole. The lifecycle (renewal, a new login, the second attempt after a 403) is step C3.
func (p *TransitProvider) authenticate(ctx context.Context) error {
	if p.opts.AuthMethod == "approle" {
		return p.login(ctx)
	}
	tok, err := secret.ReadFile(p.opts.TokenFile)
	if err != nil {
		return err
	}
	p.tokMu.Lock()
	p.token = tok
	p.tokMu.Unlock()
	var out struct {
		Policies []string `json:"policies"`
	}
	if err := p.do(ctx, "lookup", http.MethodGet, "auth/token/lookup-self", nil, &out); err != nil {
		return err
	}
	p.tokMu.Lock()
	p.policies = out.Policies
	p.tokMu.Unlock()
	return nil
}

// login makes the AppRole login and keeps the token it grants.
func (p *TransitProvider) login(ctx context.Context) error {
	roleID, err := secret.ReadFile(p.opts.RoleIDFile)
	if err != nil {
		return err
	}
	secretID, err := secret.ReadFile(p.opts.SecretIDFile)
	if err != nil {
		return err
	}
	in := struct {
		RoleID   string `json:"role_id"`
		SecretID string `json:"secret_id"`
	}{roleID.Reveal(), secretID.Reveal()}
	var out struct {
		Raw      rawToken `json:"client_token"`
		Policies []string `json:"policies"`
	}
	if err := p.do(ctx, "login", http.MethodPost, "auth/"+escapePath(p.opts.AppRoleMount)+"/login", in, &out); err != nil {
		return err
	}
	defer clear(out.Raw)
	if len(out.Raw) == 0 {
		return &TransitError{Operation: "login", Class: "rejected"}
	}
	p.tokMu.Lock()
	p.token = secret.New(string(out.Raw))
	p.policies = out.Policies
	p.tokMu.Unlock()
	return nil
}

// rawToken receives the text of a token without making a string of it on the way.
type rawToken []byte

// UnmarshalJSON keeps the bytes of a JSON string, without escapes, in a slice that can be cleared.
func (r *rawToken) UnmarshalJSON(b []byte) error {
	in, ok := transitUnquote(b)
	if !ok {
		return &TransitError{Operation: "login", Class: "rejected"}
	}
	*r = append((*r)[:0], in...)
	return nil
}
