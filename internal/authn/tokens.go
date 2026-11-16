package authn

import (
	"errors"

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
