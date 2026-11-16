package authn

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
)

// checkTokens validates the options of an authenticator that accepts KindToken: it needs a key
// source and the pool of the gateway role. The validation of a token itself arrives with the
// checks of section 5.4; until then Authenticate answers malformed for anything that is not a key.
func checkTokens(o Options) error {
	switch {
	case o.Tokens.Keys == nil:
		return errors.New("authn: tokens need Tokens.Keys")
	case o.DB.Role() != store.RoleGateway:
		return errors.New("authn: tokens need the pool of the gateway role")
	}
	return nil
}

// token authenticates a credential of the shape of a JWT. The selection of the registration and
// the principal arrive with the next step; until then every token is refused as of an unknown issuer.
func (a *Authenticator) token(_ context.Context, presented string) (authz.Principal, error) {
	_, _, iss, aud, err := peek(presented)
	if err != nil {
		return authz.Principal{}, err
	}
	if iss == "" || len(aud) == 0 {
		return authz.Principal{}, &Failure{Reason: "unknown_issuer"}
	}
	return authz.Principal{}, &Failure{Reason: "unknown_issuer"}
}
