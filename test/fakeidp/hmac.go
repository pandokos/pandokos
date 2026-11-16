package fakeidp

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"hash"
)

func sha256Sum() hash.Hash { return sha256.New() }
func sha512Sum() hash.Hash { return sha512.New() }

func hmacSigner(secret []byte, h func() hash.Hash) func([]byte) []byte {
	return func(in []byte) []byte {
		m := hmac.New(h, secret)
		m.Write(in)
		return m.Sum(nil)
	}
}
