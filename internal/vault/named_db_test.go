package vault_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
)

// unwrapDown is a provider whose Unwrap fails with the error given: a key-encryption key out of reach.
type unwrapDown struct {
	vault.KeyProvider
	err error
}

func (u unwrapDown) Unwrap(context.Context, string, []byte, []byte) ([]byte, error) {
	return nil, u.err
}

// TestSealNamed is T-1039 (sprint 6.4, ADR 0071): a named secret round trips through the database;
// the associated data binds the kind, the tenant and the object, so that a credential's ciphertext,
// another destination's and another tenant's do not open; printing gives the marker.
func TestSealNamed(t *testing.T) {
	w := newVWorld(t)
	a, b := w.d.SeedTenant(t, "named-a"), w.d.SeedTenant(t, "named-b")
	const plaintext = "whsec_CANARYsecretCANARYsecretCANARYsecret12="
	const dest, other = "0192f0f0-7000-7000-8000-000000000001", "0192f0f0-7000-7000-8000-000000000002"
	worker := w.d.Open(t, store.RoleWorker)

	sealIn := func(t *testing.T, tid tenant.ID, kind vault.SecretKind, object, text string) vault.Sealed {
		t.Helper()
		var sealed vault.Sealed
		err := w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
			var err error
			sealed, err = w.sealer.SealNamed(c, tx, tid, kind, object, secret.New(text))
			return err
		})
		if err != nil {
			t.Fatalf("SealNamed: %v", err)
		}
		return sealed
	}
	openIn := func(t *testing.T, tid tenant.ID, prov vault.KeyProvider, kind vault.SecretKind, object string, version int32, sealed []byte) (secret.Secret, error) {
		t.Helper()
		var got secret.Secret
		err := worker.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
			var err error
			got, err = vault.OpenNamed(c, tx, prov, tid, kind, object, version, sealed)
			return err
		})
		return got, err
	}

	t.Run("it round trips through the database", func(t *testing.T) {
		sealed := sealIn(t, a, vault.KindWebhookSecret, dest, plaintext)
		if sealed.KeyVersion != 1 || sealed.Fingerprint != "" || sealed.Hint != "" {
			t.Errorf("the sealed secret: version %d, fingerprint %q, hint %q: a named secret has no fingerprint and no hint", sealed.KeyVersion, sealed.Fingerprint, sealed.Hint)
		}
		if len(sealed.Secret) != 1+12+len(plaintext)+16 || sealed.Secret[0] != 0x01 || bytes.Contains(sealed.Secret, []byte("CANARY")) {
			t.Errorf("the sealed bytes: %d long, first byte %#x, want 0x01, a nonce, the ciphertext and a tag; no plaintext", len(sealed.Secret), sealed.Secret[0])
		}
		// Stored as the administration stores it, read back by the worker that signs.
		err := w.admin.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			_, err := queries.New(tx).CreateEventDestination(c, queries.CreateEventDestinationParams{ID: dest, Name: "named", Url: "https://hooks.example.test/in", EventTypes: []string{}, Enabled: true,
				Secret: sealed.Secret, SecretKeyVersion: sealed.KeyVersion})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		var stored []byte
		var version int32
		if err := w.d.Super(t).QueryRow(ctx(t), `SELECT secret, secret_key_version FROM pandokos.event_destination WHERE id = $1::uuid`, dest).Scan(&stored, &version); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(stored, []byte("CANARY")) || !bytes.Equal(stored, sealed.Secret) {
			t.Error("the column does not hold the sealed bytes, or holds the plaintext")
		}
		got, err := openIn(t, a, w.prov, vault.KindWebhookSecret, dest, version, stored)
		if err != nil || got.Reveal() != plaintext {
			t.Fatalf("OpenNamed: %v", err)
		}
		// Sealing twice gives two ciphertexts (a nonce each) that open to the same text.
		again := sealIn(t, a, vault.KindWebhookSecret, dest, plaintext)
		if bytes.Equal(again.Secret, sealed.Secret) {
			t.Error("two sealings gave the same bytes")
		}
	})

	t.Run("the associated data binds the kind, the tenant and the object", func(t *testing.T) {
		sealed := sealIn(t, a, vault.KindWebhookSecret, dest, plaintext)
		// A credential's ciphertext is not a signing secret, whatever object it is opened as.
		credID := w.createCredential(t, w.sealer, a, "openai", "named-cred", plaintext)
		var credBytes []byte
		var credVersion int32
		if err := w.d.Super(t).QueryRow(ctx(t), `SELECT secret, key_version FROM pandokos.provider_credential WHERE id = $1::uuid`, credID).Scan(&credBytes, &credVersion); err != nil {
			t.Fatal(err)
		}
		for what, c := range map[string]struct {
			tid     tenant.ID
			kind    vault.SecretKind
			object  string
			version int32
			sealed  []byte
		}{
			"a credential's ciphertext as the destination's secret": {a, vault.KindWebhookSecret, dest, credVersion, credBytes},
			"a credential's ciphertext as its own ID":               {a, vault.KindWebhookSecret, credID, credVersion, credBytes},
			"another destination's ID":                              {a, vault.KindWebhookSecret, other, sealed.KeyVersion, sealed.Secret},
			"another kind":                                          {a, vault.SecretKind("pandokos/other-secret/v1"), dest, sealed.KeyVersion, sealed.Secret},
			"another tenant":                                        {b, vault.KindWebhookSecret, dest, sealed.KeyVersion, sealed.Secret},
			"another key version":                                   {a, vault.KindWebhookSecret, dest, sealed.KeyVersion + 1, sealed.Secret},
			"a truncated ciphertext":                                {a, vault.KindWebhookSecret, dest, sealed.KeyVersion, sealed.Secret[:len(sealed.Secret)-1]},
			"an empty ciphertext":                                   {a, vault.KindWebhookSecret, dest, sealed.KeyVersion, nil},
		} {
			// The other tenant has no data key yet: give it one, so that the refusal is the binding's.
			if c.tid == b {
				sealIn(t, b, vault.KindWebhookSecret, other, "whsec_otherotherotherotherotherotherother12=")
			}
			if got, err := openIn(t, c.tid, w.prov, c.kind, c.object, c.version, c.sealed); err == nil || got.Reveal() != "" {
				t.Errorf("%s: opened (%v)", what, err)
			} else if strings.Contains(err.Error(), "CANARY") || strings.Contains(err.Error(), plaintext) {
				t.Errorf("%s: the error quotes the secret: %v", what, err)
			}
		}
		// A flipped bit.
		bad := append([]byte(nil), sealed.Secret...)
		bad[len(bad)/2] ^= 1
		if _, err := openIn(t, a, w.prov, vault.KindWebhookSecret, dest, sealed.KeyVersion, bad); !errors.Is(err, vault.ErrUndecryptable) {
			t.Errorf("a flipped bit: %v, want ErrUndecryptable", err)
		}
		// And the credential's own path still works: a named secret does not disturb it.
		if got, err := openIn(t, a, w.prov, vault.KindWebhookSecret, dest, sealed.KeyVersion, sealed.Secret); err != nil || got.Reveal() != plaintext {
			t.Errorf("the right call after the refused ones: %v", err)
		}
	})

	t.Run("a key-encryption key out of reach is ErrUnavailable", func(t *testing.T) {
		sealed := sealIn(t, a, vault.KindWebhookSecret, dest, plaintext)
		for _, cause := range []error{vault.ErrUnwrap, vault.ErrKeyVersionUnknown, errors.New("the provider is down")} {
			_, err := openIn(t, a, unwrapDown{w.prov, cause}, vault.KindWebhookSecret, dest, sealed.KeyVersion, sealed.Secret)
			if err == nil || !errors.Is(err, vault.ErrUnavailable) {
				t.Errorf("a provider that fails with %v: %v, want ErrUnavailable", cause, err)
			}
		}
		// Sealing needs the key too: the same error, nothing sealed.
		sealer, err := vault.NewSealer(unwrapDown{w.prov, vault.ErrUnwrap})
		if err != nil {
			t.Fatal(err)
		}
		err = w.admin.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			_, err := sealer.SealNamed(c, tx, a, vault.KindWebhookSecret, dest, secret.New(plaintext))
			return err
		})
		var se *vault.SealError
		if err == nil || !errors.As(err, &se) {
			t.Errorf("SealNamed with a key that does not unwrap: %v, want a SealError", err)
		} else if strings.Contains(err.Error(), "CANARY") {
			t.Errorf("the error quotes the secret: %v", err)
		}
	})

	t.Run("printing gives the marker", func(t *testing.T) {
		sealed := sealIn(t, a, vault.KindWebhookSecret, dest, plaintext)
		got, err := openIn(t, a, w.prov, vault.KindWebhookSecret, dest, sealed.KeyVersion, sealed.Secret)
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range []string{fmt.Sprint(sealed), fmt.Sprintf("%+v", sealed), fmt.Sprintf("%#v", sealed), fmt.Sprint(got), fmt.Sprintf("%+v", got), fmt.Sprintf("%#v", got), fmt.Sprintf("%v", []vault.Sealed{sealed})} {
			if !strings.Contains(out, secret.Redacted) || strings.Contains(out, "CANARY") || strings.Contains(out, fmt.Sprintf("%x", sealed.Secret[:8])) {
				t.Errorf("a formatting path prints more than the marker: %q", out)
			}
		}
	})

	t.Run("the kind and an empty object are refused before anything is sealed", func(t *testing.T) {
		for what, c := range map[string]struct {
			kind   vault.SecretKind
			object string
		}{"an empty kind": {"", dest}, "an empty object": {vault.KindWebhookSecret, ""}, "an object that is not a UUID": {vault.KindWebhookSecret, "not-a-uuid"}} {
			err := w.admin.WithTenant(ctx(t), a, func(c2 context.Context, tx store.Tx) error {
				_, err := w.sealer.SealNamed(c2, tx, a, c.kind, c.object, secret.New(plaintext))
				return err
			})
			if err == nil {
				t.Errorf("%s: sealed", what)
			}
		}
	})
}
