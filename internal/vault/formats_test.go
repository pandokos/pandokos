package vault

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/tenant"
)

// The synthetic inputs of T-301. They are public byte sequences, not key material: tenant
// 00 01 .. 0f, credential 10 11 .. 1f, data key 20 21 .. 3f, key-encryption key 40 41 .. 5f, nonce
// 60 61 .. 6b, version 1, plaintext "synthetic-plaintext". The vectors below were computed from
// section 5.2 of the design by a throwaway program that uses only the standard library, not by
// the code under test.
const (
	vecTenant     = "00010203-0405-0607-0809-0a0b0c0d0e0f"
	vecCredential = "10111213-1415-1617-1819-1a1b1c1d1e1f"
	vecKEKFile    = "QEFCQ0RFRkdISUpLTE1OT1BRUlNUVVZXWFlaW1xdXl8=" // standard base64 of 40 41 .. 5f
	vecKEKVersion = "local-e3b1845a211fbdd5"
	vecPlaintext  = "synthetic-plaintext"

	// "pandokos/dek/v1", 0x00, tenant, be32(1): 15 + 1 + 16 + 4 = 36 bytes.
	vecDataAAD = "70616e646f6b6f732f64656b2f7631" + "00" + "000102030405060708090a0b0c0d0e0f" + "00000001"
	// "pandokos/credential/v1", 0x00, tenant, credential, be32(1): 22 + 1 + 16 + 16 + 4 = 59 bytes.
	vecCredentialAAD = "70616e646f6b6f732f63726564656e7469616c2f7631" + "00" + "000102030405060708090a0b0c0d0e0f" +
		"101112131415161718191a1b1c1d1e1f" + "00000001"

	// 0x01, nonce 60..6b, AES-256-GCM of the data key under the key-encryption key: 61 bytes.
	vecWrapped = "01606162636465666768696a6b8c092b2623b1b68121a8531d29ab72243f1ddc22230ba7393643f18397ff39c47a00bcfbf391099adb7fcfc928641eda"
	// 0x01, nonce 60..6b, AES-256-GCM of the plaintext under the encryption subkey: 48 bytes.
	vecSealed = "01606162636465666768696a6bad5b861b972453a9b6a55d51f3508584eb2ca448f33703b159248cafa603f3a8e44ccc"
	// First 16 bytes of HMAC-SHA-256 of the plaintext under the fingerprint subkey.
	vecFingerprint = "5e934b2cc0443aaf51b4d6aa690ba5f3"
)

const (
	infoEncryption  = "pandokos/vault/v1/credential-encryption"
	infoFingerprint = "pandokos/vault/v1/credential-fingerprint"
)

func seq(from, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(from + i)
	}
	return b
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("the literal vector is not filled in or is not hexadecimal: %v", err)
	}
	return b
}

func mustTenant(t *testing.T) tenant.ID {
	t.Helper()
	id, err := tenant.ParseID(vecTenant)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func gcmOf(t *testing.T, key []byte) cipher.AEAD {
	t.Helper()
	blk, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestFormatsAreFrozen is T-301: what is stored today must open in every later version. The
// associated data, the two HKDF info strings, the sealed value and the wrapped key are literals.
func TestFormatsAreFrozen(t *testing.T) {
	tid := mustTenant(t)

	t.Run("associated data", func(t *testing.T) {
		if got := DataAAD(tid, 1); !bytes.Equal(got, mustHex(t, vecDataAAD)) || len(got) != 36 {
			t.Errorf("DataAAD = %x (%d bytes)\nwant %s", got, len(got), vecDataAAD)
		}
		got, err := credentialAAD(tid, vecCredential, 1)
		if err != nil || !bytes.Equal(got, mustHex(t, vecCredentialAAD)) || len(got) != 59 {
			t.Errorf("credentialAAD = %x (%d bytes), %v\nwant %s", got, len(got), err, vecCredentialAAD)
		}
		// The version is part of both, as four big-endian bytes.
		if got := DataAAD(tid, 0x01020304); !strings.HasSuffix(hex.EncodeToString(got), "01020304") {
			t.Errorf("the version is not four big-endian bytes at the end: %x", got)
		}
		// Different inputs encode differently.
		a, _ := credentialAAD(tid, vecCredential, 1)
		b, _ := credentialAAD(tid, vecCredential, 2)
		c, _ := credentialAAD(tid, "10111213-1415-1617-1819-1a1b1c1d1e1e", 1)
		var other tenant.ID
		copy(other[:], tid[:])
		other[15] ^= 1
		d, _ := credentialAAD(other, vecCredential, 1)
		for i, x := range [][]byte{b, c, d, DataAAD(tid, 1)} {
			if bytes.Equal(a, x) {
				t.Errorf("variant %d encodes like the original", i)
			}
		}
		// A credential ID is the canonical lower-case form of a UUID, and nothing else.
		for _, bad := range []string{"", "x", strings.ToUpper(vecCredential), strings.ReplaceAll(vecCredential, "-", ""), vecCredential + "0",
			"10111213-1415-1617-1819-1a1b1c1d1e1g", "{" + vecCredential + "}", " " + vecCredential, "00000000-0000-0000-0000-000000000000x"} {
			if got, err := credentialAAD(tid, bad, 1); err == nil || got != nil {
				t.Errorf("credentialAAD accepted %q", bad)
			}
		}
	})

	t.Run("the wrapped data key opens", func(t *testing.T) {
		p, err := NewLocalProvider(secret.New(vecKEKFile))
		if err != nil {
			t.Fatal(err)
		}
		wrapped := mustHex(t, vecWrapped)
		if len(wrapped) != 61 || wrapped[0] != 0x01 {
			t.Fatalf("the literal is %d bytes starting 0x%02x", len(wrapped), wrapped[0])
		}
		if err := p.Available(context.Background(), vecKEKVersion); err != nil {
			t.Fatalf("the version of the synthetic key is not %s: %v", vecKEKVersion, err)
		}
		got, err := p.Unwrap(context.Background(), vecKEKVersion, wrapped, DataAAD(tid, 1))
		if err != nil || !bytes.Equal(got, seq(0x20, 32)) {
			t.Fatalf("the literal wrapped key does not unwrap to the synthetic data key: %v", err)
		}
	})

	t.Run("the sealed credential opens and the fingerprint is stable", func(t *testing.T) {
		dk, err := NewDataKey(seq(0x20, 32), tid, 1)
		if err != nil {
			t.Fatal(err)
		}
		got, err := dk.Open(vecCredential, mustHex(t, vecSealed))
		if err != nil || string(got) != vecPlaintext {
			t.Fatalf("the literal sealed value does not open: %v", err)
		}
		if fp := dk.Fingerprint([]byte(vecPlaintext)); fp != vecFingerprint {
			t.Errorf("fingerprint %s, want %s", fp, vecFingerprint)
		}
	})

	t.Run("the HKDF info strings, with the associated data, by an independent derivation", func(t *testing.T) {
		raw := seq(0x20, 32)
		enc, err := hkdf.Key(sha256.New, raw, nil, infoEncryption, 32)
		if err != nil {
			t.Fatal(err)
		}
		sealed := mustHex(t, vecSealed)
		if sealed[0] != 0x01 || len(sealed) != 1+12+len(vecPlaintext)+16 {
			t.Fatalf("the sealed literal is %d bytes starting 0x%02x", len(sealed), sealed[0])
		}
		plain, err := gcmOf(t, enc).Open(nil, sealed[1:13], sealed[13:], mustHex(t, vecCredentialAAD))
		if err != nil || string(plain) != vecPlaintext {
			t.Fatalf("the literal does not open under the encryption subkey %q with the literal associated data: %v", infoEncryption, err)
		}
		fpk, err := hkdf.Key(sha256.New, raw, nil, infoFingerprint, 32)
		if err != nil {
			t.Fatal(err)
		}
		m := hmac.New(sha256.New, fpk)
		m.Write([]byte(vecPlaintext))
		if got := hex.EncodeToString(m.Sum(nil)[:16]); got != vecFingerprint {
			t.Errorf("the independent fingerprint %s is not the literal %s", got, vecFingerprint)
		}
		// The key-encryption key wraps with the key itself, no derivation.
		wrapped := mustHex(t, vecWrapped)
		dek, err := gcmOf(t, seq(0x40, 32)).Open(nil, wrapped[1:13], wrapped[13:], mustHex(t, vecDataAAD))
		if err != nil || !bytes.Equal(dek, raw) {
			t.Errorf("the wrapped key does not open under the key-encryption key itself: %v", err)
		}
	})

	t.Run("what the code produces opens in the independent derivation", func(t *testing.T) {
		raw := seq(0x20, 32)
		dk, err := NewDataKey(raw, tid, 1)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := dk.Seal(vecCredential, []byte(vecPlaintext))
		if err != nil {
			t.Fatal(err)
		}
		if len(sealed) != 1+12+len(vecPlaintext)+16 || sealed[0] != 0x01 {
			t.Fatalf("sealed is %d bytes starting 0x%02x", len(sealed), sealed[0])
		}
		enc, _ := hkdf.Key(sha256.New, raw, nil, infoEncryption, 32)
		plain, err := gcmOf(t, enc).Open(nil, sealed[1:13], sealed[13:], mustHex(t, vecCredentialAAD))
		if err != nil || string(plain) != vecPlaintext {
			t.Errorf("a value sealed by the code does not open in the independent derivation: %v", err)
		}
	})
}
