package admin

import "github.com/pandokos/pandokos/internal/secret"

// revealOnce returns the text of a new key for the create response. It is the one place of the
// handlers that calls Reveal.
func revealOnce(s secret.Secret) string { return s.Reveal() }
