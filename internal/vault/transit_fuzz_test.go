package vault

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// reFixedTransit is the fixed text of a *TransitError (sprint 12.1, section 5.8).
var reFixedTransit = regexp.MustCompile(`^vault: key service: (wrap|unwrap|describe|identify|lookup|renew|login): (unavailable|denied|not_found|rejected|refused)$`)

// fixedTransitErrors are the other texts a check of a key description may give.
var fixedTransitErrors = []error{errTransitMissing, errTransitType, errTransitDerived, errTransitOperation, errTransitExportable}

// noInputEcho fails when text holds a substring of input longer than 8 bytes that is not part of
// the fixed words: an error never repeats what the other side said. Inputs of 8 bytes or fewer
// cannot show it and are skipped.
func noInputEcho(t *testing.T, text string, input []byte, fixed ...string) {
	t.Helper()
	if len(input) < 9 {
		return
	}
	for _, f := range fixed {
		text = strings.ReplaceAll(text, f, "\x00")
	}
	for _, part := range strings.Split(text, "\x00") {
		for i := 0; i+9 <= len(part); i++ {
			if bytes.Contains(input, []byte(part[i:i+9])) {
				t.Fatalf("the error repeats %q of the input: %q", part[i:i+9], text)
			}
		}
	}
}

// transitFixedWords are the words of the fixed texts, which an input may happen to hold.
var transitFixedWords = []string{
	"vault: key service: ", "vault: ", "wrap", "unwrap", "describe", "identify", "lookup", "renew", "login",
	"unavailable", "denied", "not_found", "rejected", "refused",
	"the transit key (vault.kek.transit.key_name) ", "must be of type aes256-gcm96", "must not be derived",
	"does not exist", "does not support encryption and decryption",
	"is exportable; create it with exportable false, or accept it with vault.kek.transit.allow_exportable_key", "exportable",
}

// seedTransitAnswers returns well-formed answers of the key service, one list per operation, with
// the synthetic values of the fixtures, and malformed variants.
func seedTransitAnswers() [][]byte {
	b64 := base64.StdEncoding.EncodeToString
	payload := make([]byte, 40)
	key := make([]byte, 32)
	mac := make([]byte, 32)
	for i := range payload {
		payload[i] = byte(i * 3)
	}
	for i := range key {
		key[i] = byte(0xa0 + i)
		mac[i] = byte(i * 7)
	}
	return [][]byte{
		[]byte(`{"data":{"ciphertext":"vault:v1:` + b64(payload) + `","key_version":1}}`),
		[]byte(`{"data":{"ciphertext":"vault:v2:` + b64(payload) + `","key_version":2}}`),
		[]byte(`{"data":{"ciphertext":"vault:v1:` + b64(payload) + `","key_version":2}}`),
		[]byte(`{"data":{"ciphertext":"vault:v1:@@@@","key_version":1}}`),
		[]byte(`{"data":{"ciphertext":"vault:v1:AAAA","key_version":1}}`),
		[]byte(`{"data":{"plaintext":"` + b64(key) + `"}}`),
		[]byte(`{"data":{"plaintext":"` + b64(key[:31]) + `"}}`),
		[]byte(`{"data":{"plaintext":null}}`),
		[]byte(`{"data":{"hmac":"vault:v1:` + b64(mac) + `"}}`),
		[]byte(`{"data":{"hmac":"vault:v1:` + b64(mac[:16]) + `"}}`),
		[]byte(`{"data":{"type":"aes256-gcm96","derived":false,"exportable":true,"allow_plaintext_backup":false,"deletion_allowed":false,"auto_rotate_period":"0s","soft_deleted":false,"supports_encryption":true,"supports_decryption":true,"latest_version":3,"min_decryption_version":1}}`),
		[]byte(`{"data":{"type":"rsa-2048","derived":true,"supports_encryption":false,"supports_decryption":true,"latest_version":0,"min_decryption_version":0,"auto_rotate_period":"24h"}}`),
		[]byte(`{"data":{"type":"aes256-gcm96"}}`),
		[]byte(`{"data":{"policies":["default","pandokos-transit"],"ttl":3600,"renewable":true}}`),
		[]byte(`{"data":{"policies":null,"ttl":0,"renewable":false}}`),
		[]byte(`{"data":{"policies":["root"],"ttl":-1}}`),
		[]byte(`{"auth":{"lease_duration":1800,"renewable":true}}`),
		[]byte(`{"auth":{"lease_duration":1e400}}`),
		[]byte(`{"auth":{"client_token":"synthetic-token-0001","policies":["default"],"lease_duration":2764800,"renewable":true}}`),
		[]byte(`{"auth":{"client_token":"synthetic\"token","lease_duration":1}}`),
		[]byte(`{"auth":{"client_token":"","lease_duration":1}}`),
		[]byte(`{"auth":null}`),
		[]byte(`{"data":null}`),
		[]byte(`[]`),
		[]byte(``),
		[]byte(`{"data":{"ciphertext":` + `"vault:v1:` + strings.Repeat("A", 2000) + `"}}`),
	}
}

// FuzzTransitAnswer is T-1105: the answers of the key service are parsed the way the client parses
// them, for each operation (the second argument selects it and the key version in play): what is
// accepted has the shape the client relies on, and what is refused is one of the fixed texts of
// sprint 12.1 with no piece of the input in it.
func FuzzTransitAnswer(f *testing.F) {
	for i, seed := range seedTransitAnswers() {
		for op := 0; op < 7; op++ {
			f.Add(seed, uint8(op+7*(i%3)))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, sel uint8) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		const operations = 7
		operation := [operations]string{"wrap", "unwrap", "describe", "identify", "lookup", "renew", "login"}[int(sel)%operations]
		n := 1 + int(sel/operations)%3

		// The client turns an answer it cannot decode into a rejection of the operation.
		fixedFailure := func(err error) {
			t.Helper()
			var te *TransitError
			if !errors.As(err, &te) || te.Operation != operation || !reFixedTransit.MatchString(err.Error()) {
				t.Fatalf("%s: the failure is not a fixed text of the client: %v", operation, err)
			}
			noInputEcho(t, err.Error(), data, transitFixedWords...)
		}
		decode := func(out any) bool {
			err := decodeEnvelope(data, operation, out)
			if err == nil {
				return true
			}
			// This branch is vacuous: the target checks an error it builds itself, because in
			// production exchange discards the decoding error and returns the fixed "rejected"
			// one, so nothing of the decoder reaches a caller. What can be said is that the
			// function returned an error value, which is what brought us here, and that no data
			// is taken from a body that did not decode: the caller returns at once.
			fixedFailure(&TransitError{Operation: operation, Class: "rejected"})
			return false
		}

		switch operation {
		case "wrap":
			var out encryptData
			if !decode(&out) {
				return
			}
			payload, err := wrapAnswer(out, n)
			if err != nil {
				fixedFailure(err)
				return
			}
			if len(payload) < 28 || len(payload) > 1023 {
				t.Fatalf("an accepted ciphertext of %d bytes", len(payload))
			}
			if want := fmt.Sprintf(`"vault:v%d:`, n); !strings.HasPrefix(string(out.Ciphertext), want) {
				t.Fatalf("an accepted ciphertext without the prefix of the pinned version %d: %.30q", n, out.Ciphertext)
			}
		case "unwrap":
			var out decryptData
			if !decode(&out) {
				return
			}
			dk, err := unwrapAnswer(out)
			if err != nil {
				fixedFailure(err)
				return
			}
			if len(dk) != 32 {
				t.Fatalf("an accepted plaintext of %d bytes", len(dk))
			}
			clear(dk)
		case "describe":
			var k transitKeyInfo
			if !decode(&k) {
				return
			}
			d, err := describeAnswer(k)
			if err != nil {
				fixedFailure(err)
				return
			}
			if d.oldest < 1 {
				t.Fatalf("a description whose oldest version is %d", d.oldest)
			}
			p := &TransitProvider{}
			if err := p.checkKey(d); err != nil {
				ok := false
				for _, fixed := range fixedTransitErrors {
					ok = ok || errors.Is(err, fixed)
				}
				if !ok {
					t.Fatalf("checkKey: not a fixed text: %v", err)
				}
				noInputEcho(t, err.Error(), data, transitFixedWords...)
			}
			_ = p.weakSettings(d)
		case "identify":
			var out hmacData
			if !decode(&out) {
				return
			}
			id, err := identifyAnswer(out, n)
			if err != nil {
				fixedFailure(err)
				return
			}
			if !reTransitID.MatchString(id) || !strings.HasSuffix(id, fmt.Sprintf("-v%d", n)) {
				t.Fatalf("an accepted identifier %q for version %d", id, n)
			}
		case "lookup":
			var out lookupData
			if !decode(&out) {
				return
			}
			g, err := lookupGrant(out)
			if err != nil {
				fixedFailure(err)
				return
			}
			if g.lifetime < 0 || g.policies == nil {
				t.Fatalf("an accepted grant: %+v", g)
			}
		case "renew":
			var out renewData
			if !decode(&out) {
				return
			}
			g, err := renewGrant(out)
			if err != nil {
				fixedFailure(err)
				return
			}
			if g.lifetime < 0 {
				t.Fatalf("an accepted grant with the lifetime %v", g.lifetime)
			}
		case "login":
			var out loginData
			if !decode(&out) {
				return
			}
			defer clear(out.Raw)
			g, err := loginGrant(out)
			if err != nil {
				fixedFailure(err)
				return
			}
			if g.lifetime < 0 || g.policies == nil || len(out.Raw) == 0 || bytes.IndexByte(out.Raw, '\\') >= 0 {
				t.Fatalf("an accepted login: %+v, a token of %d bytes", g, len(out.Raw))
			}
			var probe struct {
				Auth struct {
					Token string `json:"client_token"`
				} `json:"auth"`
			}
			// A token that is not valid UTF-8 is kept as the service sent it; encoding/json would
			// replace its bytes, so only a valid one is compared.
			if utf8.Valid(out.Raw) && (json.Unmarshal(data, &probe) != nil || probe.Auth.Token != string(out.Raw)) {
				t.Fatalf("the token read is not the one encoding/json reads")
			}
		}
	})
}
