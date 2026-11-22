package apitest

import (
	"context"
	"crypto"
	"sync"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/test/fakeidp"
)

// IDPKeys is a key source backed by the keys of fake issuers, without HTTP: the source is found by
// its issuer URL. It is what the authenticator of a test needs to validate tokens.
type IDPKeys struct {
	mu   sync.Mutex
	idps map[string]*fakeidp.Server
}

var _ authn.KeySource = (*IDPKeys)(nil)

// NewIDPKeys returns a key source for the issuers.
func NewIDPKeys(idps ...*fakeidp.Server) *IDPKeys {
	k := &IDPKeys{idps: map[string]*fakeidp.Server{}}
	for _, s := range idps {
		k.idps[s.Issuer()] = s
	}
	return k
}

// Key implements authn.KeySource.
func (k *IDPKeys) Key(_ context.Context, src jwks.Source, kid, alg string) (crypto.PublicKey, error) {
	k.mu.Lock()
	idp := k.idps[src.Issuer]
	k.mu.Unlock()
	if idp == nil {
		return nil, jwks.ErrKeyNotFound
	}
	if kid == "" {
		return idp.PublicKey(alg), nil
	}
	if pub := idp.PublicKeyByID(kid); pub != nil {
		return pub, nil
	}
	return nil, jwks.ErrKeyNotFound
}
