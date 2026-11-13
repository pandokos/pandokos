package vault_test

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// byteForms returns what a leak of b would look like in an output: the bytes as they come out of
// a careless fmt verb or encoder.
func byteForms(b []byte) []string {
	return []string{
		string(b),
		hex.EncodeToString(b),
		strings.ToUpper(hex.EncodeToString(b)),
		base64.StdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b),
		base64.RawStdEncoding.EncodeToString(b),
		base64.RawURLEncoding.EncodeToString(b),
		decimalList(b),                     // [1 2 3]
		fmt.Sprintf("%x", b),               // hexadecimal
		fmt.Sprintf("%#v", b),              // []byte{0x1, 0x2}
		strings.Trim(decimalList(b), "[]"), // 1 2 3
	}
}

// TestNothingPrints is T-304: a data key, a provider, a sealed value, an opened credential and a
// use cannot be made to show a key, a secret or a sealed byte by any formatting path.
func TestNothingPrints(t *testing.T) {
	kek := rawKey(t)
	dek := rawKey(t)
	tid := tenantID(t, 7)
	prov := provider(t, kek)
	dk := dataKey(t, dek, tid, 1)
	plain := []byte(fixtures.CanaryCredential)
	sealedBytes, err := dk.Seal(credA, plain)
	if err != nil {
		t.Fatal(err)
	}
	_, wrapped, err := prov.Wrap(t.Context(), dek, vault.DataAAD(tid, 1))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := hkdf.Key(sha256.New, dek, nil, "pandokos/vault/v1/credential-encryption", 32)
	if err != nil {
		t.Fatal(err)
	}
	fpk, err := hkdf.Key(sha256.New, dek, nil, "pandokos/vault/v1/credential-fingerprint", 32)
	if err != nil {
		t.Fatal(err)
	}
	kekLine := base64.StdEncoding.EncodeToString(kek)

	sealed := vault.Sealed{KeyVersion: 1, Secret: sealedBytes, Fingerprint: dk.Fingerprint(plain), Hint: vault.Hint(string(plain)), DataKeyCreated: true, KEKVersion: "local-0123456789abcdef"}
	opened := vault.Opened{ID: credA, Provider: "openai", Label: "main", Fingerprint: sealed.Fingerprint, Pinned: true, Secret: secret.New(string(plain))}
	use := vault.Use{Principal: authz.Principal{Kind: authz.KindServiceAccount, ID: "sa", KeyID: "tabcdefghijklmnop", TenantID: tid, Roles: []string{authz.RoleInference}, CredentialID: credA}, RequestID: "req-1", Provider: "openai"}

	// What must never appear, per value.
	keyMaterial := [][]string{byteForms(kek), {kekLine}, byteForms(dek), byteForms(enc), byteForms(fpk), byteForms(wrapped)}
	var allForms []string
	for _, f := range keyMaterial {
		allForms = append(allForms, f...)
	}
	secretForms := append(testutil.SecretForms(string(plain)), byteForms(sealedBytes)...)

	values := []struct {
		name   string
		v      any
		forbid []string
	}{
		{"*DataKey", dk, append(append([]string{}, allForms...), secretForms...)},
		{"DataKey", *dk, append(append([]string{}, allForms...), secretForms...)},
		{"*LocalProvider", prov, allForms},
		{"LocalProvider", *prov, allForms},
		{"KeyProvider", vault.KeyProvider(prov), allForms},
		{"Sealed", sealed, secretForms},
		{"*Sealed", &sealed, secretForms},
		{"Opened", opened, secretForms},
		{"*Opened", &opened, secretForms},
		{"Use", use, secretForms},
		{"*Use", &use, secretForms},
	}
	for _, c := range values {
		outputs := map[string]string{}
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%T"} {
			outputs["fmt "+verb] = fmt.Sprintf(verb, c.v)
		}
		outputs["fmt.Sprint"] = fmt.Sprint(c.v)
		outputs["fmt.Sprintln"] = fmt.Sprintln(c.v)
		outputs["wrapped in a struct, %+v"] = fmt.Sprintf("%+v", struct{ V any }{c.v})
		outputs["wrapped in a slice, %v"] = fmt.Sprintf("%v", []any{c.v})
		outputs["wrapped in a map, %v"] = fmt.Sprintf("%v", map[string]any{"v": c.v})
		var buf bytes.Buffer
		for _, h := range []slog.Handler{slog.NewJSONHandler(&buf, nil), slog.NewTextHandler(&buf, nil)} {
			l := slog.New(h)
			l.Info("value", "v", c.v)
			l.Info("value", slog.Any("v", c.v), slog.Group("g", slog.Any("w", c.v)))
			l.With("v", c.v).Error("with")
		}
		outputs["slog"] = buf.String()
		if j, err := json.Marshal(c.v); err == nil {
			outputs["json.Marshal"] = string(j)
		}
		if j, err := json.MarshalIndent(map[string]any{"v": c.v}, "", " "); err == nil {
			outputs["json.MarshalIndent"] = string(j)
		}
		for where, out := range outputs {
			for i, f := range c.forbid {
				if f != "" && len(f) >= 6 && strings.Contains(out, f) {
					t.Errorf("%s through %s: form %d of a secret or a key is printed", c.name, where, i)
				}
			}
		}
	}

	// The types that hold a secret say so.
	for _, c := range []struct {
		name string
		v    any
	}{{"*DataKey", dk}, {"*LocalProvider", prov}} {
		if got := fmt.Sprintf("%v", c.v); got != secret.Redacted {
			t.Errorf("%s prints %q, want %q", c.name, got, secret.Redacted)
		}
	}
}

// decimalList is what fmt prints for a byte slice with %v: "[1 2 3]".
func decimalList(b []byte) string {
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = strconv.Itoa(int(c))
	}
	return "[" + strings.Join(parts, " ") + "]"
}
