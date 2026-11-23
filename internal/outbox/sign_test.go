package outbox

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

// secretText is a signing secret made for the tests: "whsec_" and the base64 of 32 bytes.
func secretText(fill byte) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func mustKey(t *testing.T, text string) Key {
	t.Helper()
	k, err := ParseKey(secret.New(text))
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	return k
}

// reference is the signature of Standard Webhooks, written here from the specification and not
// from sign.go: v1, and the base64 of the HMAC-SHA256, under the 32 bytes of the key, of
// "<id>.<timestamp>.<body>".
func reference(fill byte, id string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, bytes.Repeat([]byte{fill}, 32))
	m.Write([]byte(id + "." + strconv.FormatInt(ts, 10) + "."))
	m.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// TestSign is T-1027: the three headers, the signed text, one and two keys, the form of a key and
// the redaction of every formatting path.
func TestSign(t *testing.T) {
	when := time.Date(2026, 11, 23, 17, 20, 4, 999999000, time.UTC) // seconds since the epoch: the microseconds are cut
	body := []byte(`{"specversion":"1.0","id":"` + testEvent + `","data":{"é": 1}}`)
	k1, k2 := mustKey(t, secretText(1)), mustKey(t, secretText(2))

	t.Run("the timestamp is seconds since the epoch and the signature is Standard Webhooks'", func(t *testing.T) {
		ts, sig := Sign(testEvent, when, body, k1)
		if ts != strconv.FormatInt(when.Unix(), 10) {
			t.Errorf("the timestamp is %q, want %d", ts, when.Unix())
		}
		if want := reference(1, testEvent, when.Unix(), body); sig != want {
			t.Errorf("the signature is %q, want %q", sig, want)
		}
	})
	t.Run("two keys give two signatures separated by a space, the first key first", func(t *testing.T) {
		_, sig := Sign(testEvent, when, body, k1, k2)
		want := reference(1, testEvent, when.Unix(), body) + " " + reference(2, testEvent, when.Unix(), body)
		if sig != want {
			t.Errorf("the signatures are %q, want %q", sig, want)
		}
	})
	t.Run("the signed text is id, timestamp and body, nothing else", func(t *testing.T) {
		_, a := Sign("id-a", when, body, k1)
		_, b := Sign("id-b", when, body, k1)
		_, c := Sign("id-a", when.Add(time.Second), body, k1)
		_, d := Sign("id-a", when, append(append([]byte(nil), body...), ' '), k1)
		_, same := Sign("id-a", when.Add(-500*time.Millisecond), body, k1)
		if a == b || a == c || a == d {
			t.Errorf("the id, the timestamp and the body do not all change the signature: %s %s %s %s", a, b, c, d)
		}
		if a != same {
			t.Error("a time within the same second gives another signature")
		}
	})
	t.Run("a key made by NewKey round trips through its text", func(t *testing.T) {
		k, err := NewKey()
		if err != nil {
			t.Fatal(err)
		}
		text := k.Text().Reveal()
		if !strings.HasPrefix(text, "whsec_") || len(text) != len("whsec_")+44 {
			t.Fatalf("the text of a key has %d characters: %q", len(text), text[:6])
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(text, "whsec_"))
		if err != nil || len(raw) != 32 {
			t.Fatalf("the text is not the base64 of 32 bytes: %v", err)
		}
		again, err := ParseKey(secret.New(text))
		if err != nil {
			t.Fatal(err)
		}
		_, s1 := Sign("x", when, body, k)
		_, s2 := Sign("x", when, body, again)
		if s1 != s2 {
			t.Error("the parsed key signs differently")
		}
		other, _ := NewKey()
		if other.Text().Reveal() == text {
			t.Error("two keys are the same")
		}
	})
	t.Run("a text that is not a key is refused with an error that quotes nothing", func(t *testing.T) {
		const canary = "CANARY-signing-secret-value"
		short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 31))
		long := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 33))
		for what, text := range map[string]string{
			"empty": "", "no prefix": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "another prefix": "whsek_" + short,
			"31 bytes": "whsec_" + short, "33 bytes": "whsec_" + long, "not base64": "whsec_" + canary + "!!", "unpadded": "whsec_" + strings.TrimRight(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "="),
			"url alphabet": "whsec_" + strings.NewReplacer("+", "-", "/", "_").Replace(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32))),
			"the canary":   canary, "prefix only": "whsec_", "with a newline": secretText(3) + "\n",
		} {
			_, err := ParseKey(secret.New(text))
			if err == nil {
				t.Errorf("%s: accepted", what)
				continue
			}
			if text != "" && (strings.Contains(err.Error(), text) || strings.Contains(err.Error(), canary) || (strings.TrimPrefix(text, "whsec_") != "" && strings.Contains(err.Error(), strings.TrimPrefix(text, "whsec_")))) {
				t.Errorf("%s: the error quotes the text: %v", what, err)
			}
		}
	})
	t.Run("every formatting path of a key prints the redaction marker", func(t *testing.T) {
		text := secretText(9)
		k := mustKey(t, text)
		raw := strings.TrimPrefix(text, "whsec_")
		holder := struct {
			K  Key
			P  *Key
			KS []Key
		}{k, &k, []Key{k, k}}
		var logged bytes.Buffer
		slog.New(slog.NewTextHandler(&logged, nil)).Info("x", "key", k, "ptr", &k, "keys", []Key{k})
		slog.New(slog.NewJSONHandler(&logged, nil)).Info("x", "key", k, "ptr", &k)
		js, err := json.Marshal(holder)
		if err != nil {
			t.Fatal(err)
		}
		mt, _ := k.MarshalText()
		outs := []string{
			fmt.Sprint(k), fmt.Sprintf("%v", k), fmt.Sprintf("%+v", k), fmt.Sprintf("%#v", k), fmt.Sprintf("%s", k), fmt.Sprintf("%q", k), fmt.Sprintf("%x", k), fmt.Sprintf("%X", k),
			fmt.Sprintf("%d", k), fmt.Sprintf("%v", &k), fmt.Sprintf("%+v", holder), fmt.Sprintf("%#v", holder), fmt.Sprintf("%v", []Key{k}), fmt.Sprintf("%v", map[string]Key{"k": k}),
			fmt.Sprintf("%v", k.Text()), logged.String(), string(js), string(mt), k.String(), k.GoString(),
		}
		for i, out := range outs {
			if strings.Contains(out, raw) || strings.Contains(out, text) {
				t.Errorf("formatting path %d prints the key: %q", i, out)
			}
			if !strings.Contains(out, secret.Redacted) {
				t.Errorf("formatting path %d does not print %s: %q", i, secret.Redacted, out)
			}
		}
	})
}
