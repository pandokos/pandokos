// Package oidc is the OpenID Connect relying party of the administration listener: the redirect
// to the identity provider, the exchange of the code and the check of the ID token, and the group
// rules (sprint 4.2). It does not import go-jose; the token is verified by internal/authn. Part A
// gives the package its names and no behaviour.
package oidc

import (
	"context"
	"errors"
	"net/url"

	"github.com/pandokos/pandokos/internal/secret"
)

var errNotImplemented = errors.New("oidc: not implemented")

// Failure is a refusal of a sign-in through the identity provider; Reason is the audit reason
// (state_mismatch, provider_error, code_rejected, nonce_mismatch, unmapped_subject, ...).
type Failure struct{ Reason string }

func (f *Failure) Error() string { return "oidc: sign-in refused: " + f.Reason }

// Provider is the relying party of one identity provider.
type Provider struct{}

// Identity is what is kept of an ID token.
type Identity struct {
	Subject, Name, Email string
	Groups               []string
}

// Rule is one group rule: members of Group get Role, in the platform or in the tenant.
type Rule struct {
	Platform bool
	Tenant   string // tenant name; empty for a platform rule
	Role     string
	Group    string
}

// Start returns the address of the authorization endpoint, the seed of the binding cookie and the
// hash of the state that the transaction is stored under.
func (p *Provider) Start(ctx context.Context) (redirect string, seed secret.Secret, stateHash [32]byte, err error) {
	_, _ = ctx, p
	return "", secret.Secret{}, stateHash, errNotImplemented
}

// Complete exchanges the code of the callback query and checks the ID token; a refusal is a *Failure.
func (p *Provider) Complete(ctx context.Context, seed secret.Secret, query url.Values) (Identity, error) {
	_, _, _, _ = ctx, seed, query, p
	return Identity{}, errNotImplemented
}

// Grants returns the platform roles and the roles per tenant name that rules give to groups.
func Grants(rules []Rule, groups []string) (platformRoles []string, tenantRoles map[string][]string) {
	_, _ = rules, groups
	return nil, nil
}
