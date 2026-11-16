package jwks

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/json"

	jose "github.com/go-jose/go-jose/v4"
)

const (
	maxKeys     = 32   // usable keys kept per source
	minRSABits  = 2048 // the smallest RSA key that verifies anything
	maxRSABits  = 8192 // a larger key costs more to verify with than any issuer needs
	maxSetItems = 4096 // members of a key set looked at, whatever their size
)

// algorithms are the signature algorithms of the product (section 5.3); a key is kept only if it
// fits one of them.
var algorithms = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}

// usableKey is a public key of an issuer that may verify a token.
type usableKey struct {
	kid string // "" when the key has none
	alg string // the alg member of the JWK; "" when absent
	pub crypto.PublicKey
}

// fits reports whether pub can verify signatures of alg: the type, the curve and the size.
func fits(pub crypto.PublicKey, alg string) bool {
	switch alg {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512":
		k, ok := pub.(*rsa.PublicKey)
		if !ok || k == nil || k.N == nil {
			return false
		}
		bits := k.N.BitLen()
		return bits >= minRSABits && bits <= maxRSABits
	case "ES256":
		return isCurve(pub, elliptic.P256())
	case "ES384":
		return isCurve(pub, elliptic.P384())
	case "ES512":
		return isCurve(pub, elliptic.P521())
	case "EdDSA":
		k, ok := pub.(ed25519.PublicKey)
		return ok && len(k) == ed25519.PublicKeySize
	}
	return false
}

func isCurve(pub crypto.PublicKey, c elliptic.Curve) bool {
	k, ok := pub.(*ecdsa.PublicKey)
	return ok && k != nil && k.Curve == c
}

// parseKeySet reads a key set and keeps its usable keys (section 5.4, check 5): public, use absent
// or sig, a type and size that fit an algorithm of the product, an alg member that the key fits.
// A member that does not parse is skipped, not a failure of the set. At most maxKeys are kept, in
// the order of the document; a key ID that appears twice is dropped with all its holders.
func parseKeySet(doc []byte) ([]usableKey, error) {
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(doc, &set); err != nil {
		return nil, err
	}
	var kept []usableKey
	for i, raw := range set.Keys {
		if i >= maxSetItems || len(kept) >= maxKeys {
			break
		}
		var k jose.JSONWebKey
		if err := k.UnmarshalJSON(raw); err != nil {
			continue
		}
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if !k.IsPublic() || !usableAny(k.Key, k.Algorithm) {
			continue
		}
		kept = append(kept, usableKey{kid: k.KeyID, alg: k.Algorithm, pub: k.Key})
	}
	count := map[string]int{}
	for _, k := range kept {
		if k.kid != "" {
			count[k.kid]++
		}
	}
	out := kept[:0]
	for _, k := range kept {
		if k.kid == "" || count[k.kid] == 1 {
			out = append(out, k)
		}
	}
	return out, nil
}

// usableAny reports whether pub fits some algorithm of the product, and the one the JWK names if
// it names one.
func usableAny(pub crypto.PublicKey, jwkAlg string) bool {
	if jwkAlg != "" {
		return fits(pub, jwkAlg) && isProductAlg(jwkAlg)
	}
	for _, a := range algorithms {
		if fits(pub, a) {
			return true
		}
	}
	return false
}

func isProductAlg(alg string) bool {
	for _, a := range algorithms {
		if a == alg {
			return true
		}
	}
	return false
}

// find returns the key for a token of algorithm alg with key ID kid: the one with that key ID; or,
// without a key ID, the only key that fits the algorithm.
func find(keys []usableKey, kid, alg string) (crypto.PublicKey, bool) {
	var found crypto.PublicKey
	n := 0
	for _, k := range keys {
		if kid != "" && k.kid != kid {
			continue
		}
		if (k.alg != "" && k.alg != alg) || !fits(k.pub, alg) {
			continue
		}
		found = k.pub
		n++
	}
	return found, n == 1
}
