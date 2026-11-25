package vault_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/internal/vault/transittest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

var reTransitID = regexp.MustCompile(`^transit-[0-9a-f]{16}-v[0-9]+$`)

// reKEKVersionFormat is the check of kek_version.id (migration 0003).
var reKEKVersionFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,126}$`)

// bodyOf decodes the JSON body of a request the double saw.
func bodyOf(t *testing.T, r transittest.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("the body of %s %s is not JSON: %v", r.Method, r.Path, err)
	}
	return m
}

func keysOf(m map[string]any) []string {
	var k []string
	for name := range m {
		k = append(k, name)
	}
	sort.Strings(k)
	return k
}

func testTenant(n byte) tenant.ID {
	var id tenant.ID
	for i := range id {
		id[i] = n + byte(i)
	}
	id[6] = 0x70 // a version 7 UUID, as the real ones
	id[8] = 0x80
	return id
}

// lastOf returns the requests of one operation of the double, in order.
func requestsOf(s *transittest.Server, method, pathSub string) []transittest.Request {
	var out []transittest.Request
	for _, r := range s.Requests() {
		if r.Method == method && strings.Contains(r.Path, pathSub) {
			out = append(out, r)
		}
	}
	return out
}

// TestTransitWrapUnwrap is T-1057: a data key wraps and unwraps to the same bytes, the requests are
// the ones of the design and no other, the key version is pinned, the format and the answers are
// checked.
func TestTransitWrapUnwrap(t *testing.T) {
	t.Parallel()
	tid := testTenant(1)
	aad := vault.DataAAD(tid, 1)
	dek := dataKeyBytes(3)

	t.Run("a round trip and its requests", func(t *testing.T) {
		r := readyRig(t)
		r.s.Reset()
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		if !reTransitID.MatchString(version) {
			t.Errorf("the version %q is not a transit identifier", version)
		}
		if len(wrapped) != 1+12+32+16 || wrapped[0] != 0x02 {
			t.Fatalf("the wrapped key is %d bytes starting with %#x; want 61 bytes starting with 0x02", len(wrapped), wrapped[0])
		}
		payloads := r.s.Payloads()
		if len(payloads) != 1 || !bytes.Equal(payloads[0], wrapped[1:]) {
			t.Errorf("the wrapped key is not 0x02 followed by the payload of the service")
		}
		reqs := r.s.Requests()
		if len(reqs) != 1 {
			t.Fatalf("Wrap sent %d requests, want 1", len(reqs))
		}
		w := reqs[0]
		if w.Method != http.MethodPost || w.Path != "/v1/transit/encrypt/pandokos" {
			t.Errorf("Wrap sent %s %s", w.Method, w.Path)
		}
		if got := w.Header.Get("X-Vault-Token"); got != r.f.Token {
			t.Errorf("X-Vault-Token is not the token of the file")
		}
		if got := w.Header.Get("X-Vault-Request"); got != "true" {
			t.Errorf("X-Vault-Request = %q", got)
		}
		if got := w.Header.Get("User-Agent"); got != "pandokos/test" {
			t.Errorf("User-Agent = %q", got)
		}
		if got := w.Header.Get("X-Vault-Namespace"); got != "" {
			t.Errorf("X-Vault-Namespace = %q without a namespace", got)
		}
		body := bodyOf(t, w)
		if got := keysOf(body); strings.Join(got, ",") != "associated_data,key_version,plaintext" {
			t.Errorf("the fields of the encrypt body are %v", got)
		}
		if body["key_version"] != float64(1) {
			t.Errorf("key_version = %v, want 1", body["key_version"])
		}
		if pt, _ := base64.StdEncoding.DecodeString(fmt.Sprint(body["plaintext"])); !bytes.Equal(pt, dek) {
			t.Errorf("plaintext is not the base64 of the data key")
		}

		r.s.Reset()
		back, err := r.p.Unwrap(ctx(t), version, wrapped, aad)
		if err != nil || !bytes.Equal(back, dek) {
			t.Fatalf("Unwrap = %x, %v; want the data key", back, err)
		}
		reqs = r.s.Requests()
		if len(reqs) != 1 {
			t.Fatalf("Unwrap sent %d requests, want 1: %v", len(reqs), reqs)
		}
		u := reqs[0]
		if u.Method != http.MethodPost || u.Path != "/v1/transit/decrypt/pandokos" {
			t.Errorf("Unwrap sent %s %s", u.Method, u.Path)
		}
		ub := bodyOf(t, u)
		if got := keysOf(ub); strings.Join(got, ",") != "associated_data,ciphertext" {
			t.Errorf("the fields of the decrypt body are %v", got)
		}
		if want := "vault:v1:" + base64.StdEncoding.EncodeToString(wrapped[1:]); ub["ciphertext"] != want {
			t.Errorf("ciphertext = %v", ub["ciphertext"])
		}
	})

	t.Run("a mount and a key name that need escaping", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{Mount: "team-a/transit_b", Key: "k.e-y_1"})
		r.start(t, false)
		r.s.Reset()
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.p.Unwrap(ctx(t), version, wrapped, aad); err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, q := range r.s.Requests() {
			paths = append(paths, q.Method+" "+q.Path)
		}
		want := []string{"POST /v1/team-a/transit_b/encrypt/k.e-y_1", "POST /v1/team-a/transit_b/decrypt/k.e-y_1"}
		if strings.Join(paths, "|") != strings.Join(want, "|") {
			t.Errorf("the requests are %v, want %v", paths, want)
		}
	})

	t.Run("the version is the one of the start, also after a rotation", func(t *testing.T) {
		r := readyRig(t)
		v1, w1, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		r.s.Namespace("").Rotate("transit", "pandokos")
		r.s.Reset()
		v2, w2, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		if v2 != v1 {
			t.Errorf("after a rotation Wrap answers %s, want the version of the start %s", v2, v1)
		}
		reqs := requestsOf(r.s, http.MethodPost, "/encrypt/")
		if len(reqs) != 1 || bodyOf(t, reqs[0])["key_version"] != float64(1) {
			t.Errorf("after a rotation the encrypt request does not pin key_version 1: %v", reqs)
		}
		if !strings.HasPrefix(string(r.s.Requests()[0].Body), "{") {
			t.Errorf("the body is not JSON")
		}
		for _, w := range [][]byte{w1, w2} {
			if back, err := r.p.Unwrap(ctx(t), v1, w, aad); err != nil || !bytes.Equal(back, dek) {
				t.Errorf("Unwrap after a rotation: %v", err)
			}
		}
	})

	t.Run("an answer with another key version or prefix is refused", func(t *testing.T) {
		ic := newInterceptor(nil)
		r := newTransitRig(t, vaulttest.TransitSetup{}, withTransport(func(rt http.RoundTripper) http.RoundTripper { ic.inner = rt; return ic }))
		r.start(t, false)
		ic.rewrite(func(req *http.Request, resp *http.Response) *http.Response {
			if !strings.Contains(req.URL.Path, "/encrypt/") {
				return resp
			}
			return rewriteData(resp, func(d map[string]any) { d["key_version"] = 2 })
		})
		if v, w, err := r.p.Wrap(ctx(t), dek, aad); err == nil || v != "" || w != nil {
			t.Errorf("an answer with key_version 2 to a request for 1: %q %x %v", v, w, err)
		}
		ic.rewrite(func(req *http.Request, resp *http.Response) *http.Response {
			if !strings.Contains(req.URL.Path, "/encrypt/") {
				return resp
			}
			return rewriteData(resp, func(d map[string]any) {
				d["ciphertext"] = strings.Replace(fmt.Sprint(d["ciphertext"]), "vault:v1:", "vault:v2:", 1)
			})
		})
		if v, w, err := r.p.Wrap(ctx(t), dek, aad); err == nil || v != "" || w != nil {
			t.Errorf("an answer with the prefix vault:v2: to a request for 1: %q %x %v", v, w, err)
		}
	})

	t.Run("a wrapped value of another format or size is refused without a request", func(t *testing.T) {
		r := readyRig(t)
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		other := append([]byte{0x01}, wrapped[1:]...)
		cases := map[string][]byte{
			"first byte 0x01": other,
			"first byte 0x00": append([]byte{0x00}, wrapped[1:]...),
			"28 bytes":        wrapped[:28],
			"empty":           nil,
			"1025 bytes":      append([]byte{0x02}, bytes.Repeat([]byte{7}, 1024)...),
		}
		for name, w := range cases {
			r.s.Reset()
			if _, err := r.p.Unwrap(ctx(t), version, w, aad); !errors.Is(err, vault.ErrUnwrap) {
				t.Errorf("%s: %v, want ErrUnwrap", name, err)
			}
			if n := len(r.s.Requests()); n != 0 {
				t.Errorf("%s: %d requests were sent", name, n)
			}
		}
	})

	t.Run("an answer over 64 KiB is refused", func(t *testing.T) {
		r := readyRig(t)
		r.s.OversizeAnswers()
		if v, w, err := r.p.Wrap(ctx(t), dek, aad); err == nil || v != "" || w != nil {
			t.Errorf("an oversize answer to Wrap: %q %x %v", v, w, err)
		}
		r.s.Clear()
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		r.s.OversizeAnswers()
		if back, err := r.p.Unwrap(ctx(t), version, wrapped, aad); err == nil || back != nil {
			t.Errorf("an oversize answer to Unwrap: %x %v", back, err)
		}
	})
}

// TestTransitAssociatedData is T-1058: the associated data are bound through a digest and the raw
// value reaches nobody.
func TestTransitAssociatedData(t *testing.T) {
	t.Parallel()
	tid := testTenant(9)
	aad := vault.DataAAD(tid, 1)
	dek := dataKeyBytes(11)

	t.Run("the digest of the label, a zero byte and the associated data, a fixed vector", func(t *testing.T) {
		r := readyRig(t)
		check := []byte("pandokos/check/v1")
		if len(check) != 17 {
			t.Fatal("the vector is for 17 bytes")
		}
		r.s.Reset()
		if _, _, err := r.p.Wrap(ctx(t), dek, check); err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprint(bodyOf(t, requestsOf(r.s, http.MethodPost, "/encrypt/")[0])["associated_data"])
		const vector = "o6w256dJ035gKI9gdwGFa89SdQNDk8zNg+jxJsCIDPg="
		if got != vector {
			t.Errorf("associated_data = %s, want %s", got, vector)
		}
		sum := sha256.Sum256(append(append([]byte("pandokos/transit-aad/v1"), 0), check...))
		if hex.EncodeToString(sum[:]) != "a3ac36e7a749d37e60288f607701856bcf5275034393cccd83e8f126c0880cf8" ||
			base64.StdEncoding.EncodeToString(sum[:]) != vector {
			t.Errorf("the vector is not the SHA-256 of the label, a zero byte and the data")
		}
	})

	t.Run("the raw associated data and the tenant appear in no request", func(t *testing.T) {
		r := readyRig(t)
		r.s.Reset()
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.p.Unwrap(ctx(t), version, wrapped, aad); err != nil {
			t.Fatal(err)
		}
		forbidden := []string{tid.String(), hex.EncodeToString(tid[:]), base64.StdEncoding.EncodeToString(tid[:]),
			base64.StdEncoding.EncodeToString(aad), hex.EncodeToString(aad), "pandokos/dek/v1", string(aad)}
		for _, q := range r.s.Requests() {
			text := q.Path + "\n" + fmt.Sprint(q.Header) + "\n" + string(q.Body)
			for _, f := range forbidden {
				if strings.Contains(text, f) {
					t.Errorf("a request to %s holds %q", q.Path, f)
				}
			}
		}
	})

	t.Run("other associated data, none, a flipped bit and another tenant's row", func(t *testing.T) {
		r := readyRig(t)
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		flipped := append([]byte(nil), wrapped...)
		flipped[20] ^= 0x01
		_, otherRow, err := r.p.Wrap(ctx(t), dek, vault.DataAAD(testTenant(40), 1))
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name    string
			wrapped []byte
			aad     []byte
		}{
			{"other associated data", wrapped, vault.DataAAD(testTenant(40), 1)},
			{"another version of the data key", wrapped, vault.DataAAD(tid, 2)},
			{"no associated data", wrapped, nil},
			{"a flipped bit", flipped, aad},
			{"the payload of another tenant's row", otherRow, aad},
		}
		for _, c := range cases {
			if back, err := r.p.Unwrap(ctx(t), version, c.wrapped, c.aad); !errors.Is(err, vault.ErrUnwrap) || back != nil {
				t.Errorf("%s: %x, %v; want ErrUnwrap", c.name, back, err)
			}
		}
	})

	t.Run("nil associated data are accepted and bound", func(t *testing.T) {
		r := readyRig(t)
		version, wrapped, err := r.p.Wrap(ctx(t), dek, nil)
		if err != nil {
			t.Fatalf("Wrap with nil associated data: %v", err)
		}
		if back, err := r.p.Unwrap(ctx(t), version, wrapped, nil); err != nil || !bytes.Equal(back, dek) {
			t.Fatalf("Unwrap with nil associated data: %v", err)
		}
		if _, err := r.p.Unwrap(ctx(t), version, wrapped, []byte("x")); !errors.Is(err, vault.ErrUnwrap) {
			t.Errorf("a value wrapped under nil associated data opens under others: %v", err)
		}
		for _, q := range requestsOf(r.s, http.MethodPost, "/encrypt/") {
			if bodyOf(t, q)["associated_data"] == "" || bodyOf(t, q)["associated_data"] == nil {
				t.Errorf("an encrypt request without associated_data: nil data must still be bound")
			}
		}
	})
}

// idFor is the identifier the design computes for a version of the key of the double.
func idFor(s *transittest.Server, mount, key string, version int) string {
	m := hmac.New(sha256.New, s.Namespace("").HMACKey(mount, key, version))
	m.Write([]byte("pandokos kek id v1"))
	return fmt.Sprintf("transit-%s-v%d", hex.EncodeToString(m.Sum(nil)[:8]), version)
}

// TestTransitVersionIdentifier is T-1059.
func TestTransitVersionIdentifier(t *testing.T) {
	t.Parallel()
	aad := vault.DataAAD(testTenant(2), 1)
	dek := dataKeyBytes(5)

	t.Run("the format, the value and its stability", func(t *testing.T) {
		r := readyRig(t)
		want := idFor(r.s, "transit", "pandokos", 1)
		version, _, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		if version != want || r.p.Info().Version != want {
			t.Errorf("the identifier is %s (Info %s), want %s", version, r.p.Info().Version, want)
		}
		if !reTransitID.MatchString(version) || !reKEKVersionFormat.MatchString(version) {
			t.Errorf("the identifier %q does not match the formats", version)
		}
		// A restart of the provider.
		again := newTransitRigOn(t, r.s, vaulttest.TransitSetup{Mount: "transit", Key: "pandokos"})
		again.start(t, false)
		if got := again.p.Info().Version; got != want {
			t.Errorf("after a restart the identifier is %s, want %s", got, want)
		}
	})

	t.Run("another mount, name, namespace or address for the same key", func(t *testing.T) {
		r := readyRig(t)
		want := r.p.Info().Version
		ns := r.s.Namespace("team/b")
		r.s.Namespace("").Alias("transit", "pandokos", ns, "other-mount", "other.name")
		tok := ns.Token(transittest.TokenOptions{Mount: "other-mount", Key: "other.name"})
		f := r.f
		f.Namespace, f.Mount, f.Key = "team/b", "other-mount", "other.name"
		f.TokenFile = r.f.TokenFile + ".other"
		writeFile(t, f.TokenFile, tok+"\n")
		p := vaulttest.TransitProvider(t, r.s, f)
		if got := p.Info().Version; got != want {
			t.Errorf("by another mount, name and namespace the identifier is %s, want %s", got, want)
		}
		// Another address: localhost instead of 127.0.0.1.
		viaName := vaulttest.TransitProvider(t, r.s, r.f, func(o *vault.TransitOptions) {
			u := *o.Address
			u.Host = "localhost:" + u.Port()
			o.Address = &u
		})
		if got := viaName.Info().Version; got != want {
			t.Errorf("by another address the identifier is %s, want %s", got, want)
		}
	})

	t.Run("another version, and a key of the same name created again", func(t *testing.T) {
		r := readyRig(t)
		v1 := r.p.Info().Version
		r.s.Namespace("").Rotate("transit", "pandokos")
		next := newTransitRigOn(t, r.s, vaulttest.TransitSetup{})
		next.start(t, false)
		v2 := next.p.Info().Version
		if v2 == v1 || !strings.HasSuffix(v2, "-v2") {
			t.Errorf("version 2 is %s after version 1 %s", v2, v1)
		}
		if want := idFor(r.s, "transit", "pandokos", 2); v2 != want {
			t.Errorf("the identifier of version 2 is %s, want %s", v2, want)
		}
		r.s.Namespace("").DeleteKey("transit", "pandokos")
		r.s.Namespace("").CreateKey("transit", "pandokos", transittest.KeyOptions{})
		again := newTransitRigOn(t, r.s, vaulttest.TransitSetup{})
		again.start(t, false)
		if got := again.p.Info().Version; got == v1 || !strings.HasSuffix(got, "-v1") {
			t.Errorf("a key created again has the identifier %s, the old one was %s", got, v1)
		}
	})

	t.Run("an identifier that is not the key's is unknown and no decrypt is sent", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{}, withClock(&tClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}))
		r.start(t, false)
		v1 := r.p.Info().Version
		_, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		fp := strings.Split(v1, "-")[1]
		table := []string{
			"", "transit", "transit-", "transit-v1", "transit-" + fp, "transit-" + fp + "-v", "transit-" + fp + "-v0",
			"transit-" + fp + "-v-1", "transit-" + fp + "-v1x", "transit-" + fp + "-V1", "transit-" + strings.ToUpper(fp) + "-v1",
			"transit-" + fp[:15] + "-v1", "transit-" + fp + "0-v1", "transit-" + fp + "-v99999999999999999999",
			"local-" + fp, "TRANSIT-" + fp + "-v1", " transit-" + fp + "-v1", "transit-" + fp + "-v1 ",
			"transit-0000000000000000-v1", // a wrong fingerprint with a valid number
			"transit-" + fp + "-v9",       // a number above the latest
		}
		r.s.Reset()
		for _, id := range table {
			if err := r.p.Available(ctx(t), id); !errors.Is(err, vault.ErrKeyVersionUnknown) {
				t.Errorf("Available(%q) = %v, want ErrKeyVersionUnknown", id, err)
			}
			if _, err := r.p.Unwrap(ctx(t), id, wrapped, aad); !errors.Is(err, vault.ErrKeyVersionUnknown) {
				t.Errorf("Unwrap(%q) = %v, want ErrKeyVersionUnknown", id, err)
			}
		}
		if n := r.s.Count(transittest.OpUnwrap); n != 0 {
			t.Errorf("%d decrypt requests were sent for identifiers that are not the key's", n)
		}
	})

	t.Run("a version below min_decryption_version", func(t *testing.T) {
		c := &tClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
		r := newTransitRig(t, vaulttest.TransitSetup{}, withClock(c))
		r.start(t, false)
		v1 := r.p.Info().Version
		_, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		sc := r.s.Namespace("")
		sc.Rotate("transit", "pandokos")
		sc.Configure("transit", "pandokos", 2, 0)
		c.advance(11 * time.Second) // the description is read again
		r.s.Reset()
		if err := r.p.Available(ctx(t), v1); !errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("Available of a version below min_decryption_version: %v", err)
		}
		if _, err := r.p.Unwrap(ctx(t), v1, wrapped, aad); !errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("Unwrap of a version below min_decryption_version: %v", err)
		}
		if n := r.s.Count(transittest.OpUnwrap); n != 0 {
			t.Errorf("%d decrypt requests for a version the description puts out of range", n)
		}
		if n := r.s.Count(transittest.OpDescribe); n == 0 {
			t.Errorf("the description older than 10 s was not read again")
		}
	})

	t.Run("a version the service refuses to identify", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{})
		sc := r.s.Namespace("")
		sc.Rotate("transit", "pandokos")
		sc.Configure("transit", "pandokos", 1, 2) // version 1 can no longer be used to encrypt or to compute an HMAC
		r.start(t, false)
		if got := r.p.Info().Version; !strings.HasSuffix(got, "-v2") {
			t.Fatalf("the start found %s, want version 2", got)
		}
		old := idFor(r.s, "transit", "pandokos", 1)
		r.s.Reset()
		if err := r.p.Available(ctx(t), old); !errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("Available of a version the service refuses to identify: %v", err)
		}
		if n := r.s.Count(transittest.OpUnwrap); n != 0 {
			t.Errorf("%d decrypt requests", n)
		}
	})

	t.Run("a fingerprint is verified by one identify and kept", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{})
		_, wrapped1, err := func() (string, []byte, error) {
			first := newTransitRigOn(t, r.s, vaulttest.TransitSetup{})
			first.start(t, false)
			return first.p.Wrap(ctx(t), dek, aad)
		}()
		if err != nil {
			t.Fatal(err)
		}
		r.s.Namespace("").Rotate("transit", "pandokos")
		r.start(t, false) // latest is 2 now
		old := idFor(r.s, "transit", "pandokos", 1)
		r.s.Reset()
		for i := 0; i < 3; i++ {
			if err := r.p.Available(ctx(t), old); err != nil {
				t.Fatalf("Available of the first version: %v", err)
			}
		}
		if _, err := r.p.Unwrap(ctx(t), old, wrapped1, aad); err != nil {
			t.Fatalf("Unwrap of the first version: %v", err)
		}
		if n := r.s.Count(transittest.OpIdentify); n != 1 {
			t.Errorf("%d identify requests for four uses of one identifier, want 1", n)
		}
	})
}

// TestTransitKeyRequirements is T-1060.
func TestTransitKeyRequirements(t *testing.T) {
	t.Parallel()
	const keyName = "very-secret-key-name"
	fail := func(t *testing.T, r *transitRig) {
		t.Helper()
		err := r.p.Start(ctx(t))
		if err == nil {
			t.Fatal("Start succeeded")
		}
		if !strings.Contains(err.Error(), "vault.kek.transit.") {
			t.Errorf("the error does not name a setting: %v", err)
		}
		if strings.Contains(err.Error(), keyName) {
			t.Errorf("the error quotes the key name: %v", err)
		}
	}
	cases := []struct {
		name string
		opts transittest.KeyOptions
	}{
		{"chacha20-poly1305", transittest.KeyOptions{Type: "chacha20-poly1305"}},
		{"rsa-2048", transittest.KeyOptions{Type: "rsa-2048"}},
		{"a derived key", transittest.KeyOptions{Derived: true}},
		{"a key that does not support decryption", transittest.KeyOptions{NoDecrypt: true}},
		{"an exportable key", transittest.KeyOptions{Exportable: true}}, // sprint 12.2: refused unless allowed
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fail(t, newTransitRig(t, vaulttest.TransitSetup{Key: keyName, KeyOptions: c.opts}))
		})
	}
	t.Run("a missing key", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{Key: keyName})
		r.s.Namespace("").DeleteKey("transit", keyName)
		fail(t, r)
	})
	t.Run("a soft-deleted key", func(t *testing.T) {
		ic := newInterceptor(nil)
		r := newTransitRig(t, vaulttest.TransitSetup{Key: keyName}, withTransport(func(rt http.RoundTripper) http.RoundTripper { ic.inner = rt; return ic }))
		ic.rewrite(func(req *http.Request, resp *http.Response) *http.Response {
			if req.Method != http.MethodGet || !strings.Contains(req.URL.Path, "/keys/") {
				return resp
			}
			return rewriteData(resp, func(d map[string]any) { d["latest_version"] = 0 })
		})
		fail(t, r)
	})

	t.Run("each weak setting is accepted and reported once", func(t *testing.T) {
		weak := []struct {
			setting string
			opts    transittest.KeyOptions
			root    bool
		}{
			{"exportable", transittest.KeyOptions{Exportable: true}, false},
			{"plaintext_backup", transittest.KeyOptions{PlaintextBackup: true}, false},
			{"deletion_allowed", transittest.KeyOptions{DeletionAllowed: true}, false},
			{"auto_rotate", transittest.KeyOptions{AutoRotate: 24 * time.Hour}, false},
			{"root_token", transittest.KeyOptions{}, true},
		}
		for _, w := range weak {
			t.Run(w.setting, func(t *testing.T) {
				var adjust []func(*vault.TransitOptions)
				if w.setting == "exportable" { // refused unless allowed (sprint 12.2, T-1100)
					adjust = append(adjust, func(o *vault.TransitOptions) { o.AllowExportable = true })
				}
				r := newTransitRig(t, vaulttest.TransitSetup{KeyOptions: w.opts}, adjust...)
				if w.root {
					root := r.s.Namespace("").Token(transittest.TokenOptions{Root: true})
					writeFile(t, r.f.TokenFile, root+"\n")
				}
				r.start(t, false)
				if got := r.p.Info().Weak; len(got) != 1 || got[0] != w.setting {
					t.Errorf("Info().Weak = %v, want [%s]", got, w.setting)
				}
				lines := r.logs.Find(t, "key provider configuration weak")
				if len(lines) != 1 || lines[0]["setting"] != w.setting || lines[0]["level"] != "WARN" {
					t.Errorf("the log lines are %v, want one warning with setting %s", lines, w.setting)
				}
			})
		}
		t.Run("all of them at once, and none for a sound key", func(t *testing.T) {
			r := newTransitRig(t, vaulttest.TransitSetup{KeyOptions: transittest.KeyOptions{
				Exportable: true, PlaintextBackup: true, DeletionAllowed: true, AutoRotate: time.Hour}},
				func(o *vault.TransitOptions) { o.AllowExportable = true })
			root := r.s.Namespace("").Token(transittest.TokenOptions{Root: true})
			writeFile(t, r.f.TokenFile, root+"\n")
			r.start(t, false)
			got := append([]string(nil), r.p.Info().Weak...)
			sort.Strings(got)
			if strings.Join(got, ",") != "auto_rotate,deletion_allowed,exportable,plaintext_backup,root_token" {
				t.Errorf("Info().Weak = %v", got)
			}
			if n := len(r.logs.Find(t, "key provider configuration weak")); n != 5 {
				t.Errorf("%d weak lines, want one per setting (5)", n)
			}
			sound := readyRig(t)
			if len(sound.p.Info().Weak) != 0 || len(sound.logs.Find(t, "key provider configuration weak")) != 0 {
				t.Errorf("a sound key is reported weak: %v", sound.p.Info().Weak)
			}
			if len(sound.logs.Find(t, "key provider ready")) != 1 {
				t.Errorf("no line key provider ready")
			}
		})
	})
}

// TestTransitExportable is T-1100: a key created exportable is refused at start with a fixed text
// that names the setting and the key that allows it, as a cause only a person can repair, before
// any data key is wrapped; with the option it starts and reports exportable among the weak
// settings; the four other weak settings still start without the option.
func TestTransitExportable(t *testing.T) {
	const keyName = "very-secret-key-name"
	exportable := vaulttest.TransitSetup{Key: keyName, KeyOptions: transittest.KeyOptions{Exportable: true}}

	t.Run("refused without the option", func(t *testing.T) {
		r := newTransitRig(t, exportable)
		err := r.p.Start(ctx(t))
		if err == nil {
			t.Fatal("Start succeeded with an exportable key")
		}
		for _, want := range []string{"exportable", "vault.kek.transit.allow_exportable_key"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not name %q: %v", want, err)
			}
		}
		for _, leak := range []string{keyName, "127.0.0.1", r.f.Token} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("the error holds %q: %v", leak, err)
			}
		}
		if errors.Is(err, vault.ErrProviderUnavailable) {
			t.Errorf("a cause only a person can repair is reported as unavailable: %v", err)
		}
		var te *vault.TransitError
		if errors.As(err, &te) {
			t.Errorf("the refusal is a failure of an operation of the service: %v", te)
		}
		if n := r.s.Count(transittest.OpWrap) + r.s.Count(transittest.OpUnwrap); n != 0 {
			t.Errorf("%d wrap or unwrap requests were sent before the refusal", n)
		}
		if got := r.p.Info().Weak; len(got) != 0 {
			t.Errorf("a refused key is reported weak: %v", got)
		}
		if n := len(r.logs.Find(t, "key provider ready")); n != 0 {
			t.Errorf("%d lines key provider ready for a refused key", n)
		}
		testutil.AssertAbsent(t, "the logs", r.logs.String(), keyName)
	})

	t.Run("accepted with the option, and reported", func(t *testing.T) {
		r := newTransitRig(t, exportable, func(o *vault.TransitOptions) { o.AllowExportable = true })
		r.start(t, false)
		if got := r.p.Info().Weak; len(got) != 1 || got[0] != "exportable" {
			t.Errorf("Info().Weak = %v, want [exportable]", got)
		}
		lines := r.logs.Find(t, "key provider configuration weak")
		if len(lines) != 1 || lines[0]["setting"] != "exportable" || lines[0]["level"] != "WARN" {
			t.Errorf("the log lines are %v, want one warning with setting exportable", lines)
		}
		// The key works: a data key wraps and unwraps.
		aad := vault.DataAAD(testTenant(3), 1)
		id, wrapped, err := r.p.Wrap(ctx(t), dataKeyBytes(1), aad)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := r.p.Unwrap(ctx(t), id, wrapped, aad); err != nil || !bytes.Equal(got, dataKeyBytes(1)) {
			t.Errorf("Unwrap: %v", err)
		}
	})

	t.Run("the other weak settings still start without the option", func(t *testing.T) {
		for name, opts := range map[string]transittest.KeyOptions{
			"plaintext_backup": {PlaintextBackup: true},
			"deletion_allowed": {DeletionAllowed: true},
			"auto_rotate":      {AutoRotate: 24 * time.Hour},
		} {
			r := newTransitRig(t, vaulttest.TransitSetup{KeyOptions: opts})
			r.start(t, false)
			if got := r.p.Info().Weak; len(got) != 1 || got[0] != name {
				t.Errorf("%s: Info().Weak = %v", name, got)
			}
		}
		r := newTransitRig(t, vaulttest.TransitSetup{})
		root := r.s.Namespace("").Token(transittest.TokenOptions{Root: true})
		writeFile(t, r.f.TokenFile, root+"\n")
		r.start(t, false)
		if got := r.p.Info().Weak; len(got) != 1 || got[0] != "root_token" {
			t.Errorf("root_token: Info().Weak = %v", got)
		}
	})
}

// reTransitText is the fixed text of a *TransitError.
var reTransitText = regexp.MustCompile(`^vault: key service: (wrap|unwrap|describe|identify|lookup|renew|login): (unavailable|denied|not_found|rejected|refused)$`)

// fixedTexts are the texts a failure of the provider may have besides a *TransitError.
func fixedText(err error) bool {
	switch err.Error() {
	case vault.ErrUnwrap.Error(), vault.ErrKeyVersionUnknown.Error(), vault.ErrUnavailable.Error(), vault.ErrProviderUnavailable.Error():
		return true
	}
	return reTransitText.MatchString(err.Error())
}

// TestTransitErrorClasses is T-1061: the table of the design, per operation.
func TestTransitErrorClasses(t *testing.T) {
	t.Parallel()
	aad := vault.DataAAD(testTenant(3), 1)
	dek := dataKeyBytes(9)

	type row struct {
		status      int
		class       string
		unavailable bool // ErrProviderUnavailable
	}
	rows := []row{
		{http.StatusBadRequest, "rejected", false}, {http.StatusForbidden, "denied", false}, {http.StatusNotFound, "not_found", false},
		{http.StatusTooManyRequests, "unavailable", true}, {http.StatusInternalServerError, "unavailable", true},
		{http.StatusBadGateway, "unavailable", true}, {http.StatusServiceUnavailable, "unavailable", true},
	}
	check := func(t *testing.T, err error, op string, want row, canaries []string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s %d: no error", op, want.status)
		}
		te, ok := transitErr(err)
		if !ok {
			t.Fatalf("%s %d: %v is not a *TransitError", op, want.status, err)
		}
		if te.Operation != op || te.Class != want.class {
			t.Errorf("%s %d: operation %q class %q, want %q %q", op, want.status, te.Operation, te.Class, op, want.class)
		}
		if !errors.Is(err, vault.ErrUnavailable) {
			t.Errorf("%s %d: not an ErrUnavailable", op, want.status)
		}
		if got := errors.Is(err, vault.ErrProviderUnavailable); got != want.unavailable {
			t.Errorf("%s %d: ErrProviderUnavailable = %v, want %v", op, want.status, got, want.unavailable)
		}
		if errors.Is(err, vault.ErrUnwrap) || errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("%s %d: it is also ErrUnwrap or ErrKeyVersionUnknown", op, want.status)
		}
		// A key that is not found has the text of its own (§5.3, C2 item 8): it names the setting.
		missingKey := op == "describe" && want.class == "not_found" && err.Error() == "vault: the transit key (vault.kek.transit.key_name) does not exist"
		if !reTransitText.MatchString(err.Error()) && !missingKey {
			t.Errorf("%s %d: the text %q is not the fixed one", op, want.status, err.Error())
		}
		for _, c := range canaries {
			if c != "" && strings.Contains(err.Error(), c) {
				t.Errorf("%s %d: the text holds %q", op, want.status, c)
			}
		}
	}

	t.Run("wrap and unwrap against the faults of the double", func(t *testing.T) {
		for _, w := range rows {
			t.Run(fmt.Sprint(w.status), func(t *testing.T) {
				r := newTransitRig(t, vaulttest.TransitSetup{Namespace: "team/a", Key: "canary-key"}, func(o *vault.TransitOptions) { o.Namespace = "team/a" })
				r.start(t, false)
				version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
				if err != nil {
					t.Fatal(err)
				}
				canaries := []string{r.f.Token, "canary-key", "team/a", strings.TrimPrefix(r.s.URL(), "http://"), "associated_data"}
				r.s.Status(w.status)
				r.s.EchoInErrors("canary-extra-text")
				canaries = append(canaries, "canary-extra-text")
				_, _, err = r.p.Wrap(ctx(t), dek, aad)
				check(t, err, "wrap", w, canaries)
				// A 400 to decrypt for an available version is the wrong associated data or damage.
				_, err = r.p.Unwrap(ctx(t), version, wrapped, aad)
				if w.status == http.StatusBadRequest {
					if !errors.Is(err, vault.ErrUnwrap) || errors.Is(err, vault.ErrUnavailable) {
						t.Errorf("unwrap 400: %v, want ErrUnwrap only", err)
					} else if !fixedText(err) {
						t.Errorf("unwrap 400: text %q", err.Error())
					}
					return
				}
				check(t, err, "unwrap", w, canaries)
			})
		}
	})

	t.Run("400 to decrypt for a version a fresh description puts out of range", func(t *testing.T) {
		r := readyRig(t)
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		sc := r.s.Namespace("")
		sc.Rotate("transit", "pandokos")
		sc.Configure("transit", "pandokos", 2, 0)
		_, err = r.p.Unwrap(ctx(t), version, wrapped, aad)
		if !errors.Is(err, vault.ErrKeyVersionUnknown) || errors.Is(err, vault.ErrUnavailable) || !fixedText(err) {
			t.Errorf("a version out of range in a fresh description: %v", err)
		}
	})

	t.Run("a timeout, a refused connection, a drop and a redirect are unavailable", func(t *testing.T) {
		faults := map[string]func(r *transitRig){
			"timeout":  func(r *transitRig) { r.s.Hang() },
			"drop":     func(r *transitRig) { r.s.Drop() },
			"stopped":  func(r *transitRig) { r.s.Stop() },
			"redirect": func(r *transitRig) { r.s.Redirect("http://127.0.0.1:1/v1/transit/encrypt/pandokos") },
		}
		for name, fault := range faults {
			t.Run(name, func(t *testing.T) {
				r := newTransitRig(t, vaulttest.TransitSetup{}, withTimeout(300*time.Millisecond))
				r.start(t, false)
				fault(r)
				_, _, err := r.p.Wrap(ctx(t), dek, aad)
				check(t, err, "wrap", row{0, "unavailable", true}, []string{r.f.Token})
			})
		}
	})

	t.Run("a malformed 200 is rejected", func(t *testing.T) {
		ic := newInterceptor(nil)
		r := newTransitRig(t, vaulttest.TransitSetup{}, withTransport(func(rt http.RoundTripper) http.RoundTripper { ic.inner = rt; return ic }))
		r.start(t, false)
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range []string{`{}`, `{"data":{}}`, `not json`, `{"data":{"ciphertext":7}}`, `{"data":{"ciphertext":"vault:v1:@@@@"}}`} {
			ic.set("/encrypt/", answer(200, body))
			_, _, err := r.p.Wrap(ctx(t), dek, aad)
			check(t, err, "wrap", row{200, "rejected", false}, []string{body})
		}
		for _, body := range []string{`{}`, `{"data":{}}`, `{"data":{"plaintext":"@@@"}}`, `{"data":{"plaintext":"AAAA"}}`} {
			ic.set("/decrypt/", answer(200, body))
			_, err := r.p.Unwrap(ctx(t), version, wrapped, aad)
			check(t, err, "unwrap", row{200, "rejected", false}, nil)
		}
	})

	t.Run("describe, identify, lookup and login at the start", func(t *testing.T) {
		starts := []struct {
			op, sub string
			setup   vaulttest.TransitSetup
		}{
			{"describe", "GET /v1/transit/keys/", vaulttest.TransitSetup{}},
			{"identify", "/hmac/", vaulttest.TransitSetup{}},
			{"lookup", "lookup-self", vaulttest.TransitSetup{}},
			{"login", "/login", vaulttest.TransitSetup{Method: "approle"}},
		}
		for _, st := range starts {
			for _, w := range rows {
				t.Run(fmt.Sprintf("%s %d", st.op, w.status), func(t *testing.T) {
					ic := newInterceptor(nil)
					r := newTransitRig(t, st.setup, withTransport(func(rt http.RoundTripper) http.RoundTripper { ic.inner = rt; return ic }))
					ic.set(st.sub, answer(w.status, `{"errors":["canary-body-text `+r.f.Token+`"]}`))
					err := r.p.Start(ctx(t))
					check(t, err, st.op, w, []string{"canary-body-text", r.f.Token})
				})
			}
		}
	})

	t.Run("not sent: the refusal and no slot", func(t *testing.T) {
		c := &tClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
		r := newTransitRig(t, vaulttest.TransitSetup{}, withClock(c))
		r.start(t, false)
		r.s.Status(http.StatusServiceUnavailable)
		for i := 0; i < 5; i++ {
			_, _, _ = r.p.Wrap(ctx(t), dek, aad)
		}
		_, _, err := r.p.Wrap(ctx(t), dek, aad)
		check(t, err, "wrap", row{0, "refused", true}, nil)
	})

	t.Run("every error of the provider has a fixed text", func(t *testing.T) {
		r := readyRig(t)
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range []error{
			func() error { _, err := r.p.Unwrap(ctx(t), "nope", wrapped, aad); return err }(),
			func() error { _, err := r.p.Unwrap(ctx(t), version, wrapped, nil); return err }(),
			r.p.Available(ctx(t), "transit-0000000000000000-v1"),
		} {
			if e == nil || !fixedText(e) {
				t.Errorf("the error %v is not one of the fixed texts", e)
			}
		}
	})
}

// callWithin runs f and reports how long it took.
func callWithin(f func()) time.Duration {
	start := time.Now()
	f()
	return time.Since(start)
}

// bufferSpy is a transport that keeps what the provider handed to it and read from it: the body
// of the request through GetBody (which shares the buffer) and every slice the provider read the
// answer into.
type bufferSpy struct {
	inner    http.RoundTripper
	mu       sync.Mutex
	bodies   []func() (readCloser, error)
	answered [][]byte
}

type readCloser interface {
	Read([]byte) (int, error)
	Close() error
}

type spyBody struct {
	inner readCloser
	spy   *bufferSpy
}

func (b *spyBody) Read(p []byte) (int, error) {
	b.spy.mu.Lock()
	b.spy.answered = append(b.spy.answered, p)
	b.spy.mu.Unlock()
	return b.inner.Read(p)
}
func (b *spyBody) Close() error { return b.inner.Close() }

func (b *bufferSpy) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.GetBody != nil && strings.Contains(req.URL.Path, "/encrypt/") || req.GetBody != nil && strings.Contains(req.URL.Path, "/decrypt/") {
		gb := req.GetBody
		b.mu.Lock()
		b.bodies = append(b.bodies, func() (readCloser, error) { return gb() })
		b.mu.Unlock()
	}
	resp, err := b.inner.RoundTrip(req)
	if err == nil && (strings.Contains(req.URL.Path, "/encrypt/") || strings.Contains(req.URL.Path, "/decrypt/")) {
		resp.Body = &spyBody{inner: resp.Body, spy: b}
	}
	return resp, err
}

// TestTransitOneAttempt is T-1062.
func TestTransitOneAttempt(t *testing.T) {
	t.Parallel()
	aad := vault.DataAAD(testTenant(4), 1)
	dek := dataKeyBytes(13)

	t.Run("a failing service gets one request per call", func(t *testing.T) {
		for _, code := range []int{500, 503, 429} {
			r := readyRig(t)
			r.s.Status(code)
			r.s.Reset()
			for i := 0; i < 3; i++ {
				if _, _, err := r.p.Wrap(ctx(t), dek, aad); err == nil {
					t.Fatal("Wrap succeeded")
				}
			}
			if n := r.s.Count(transittest.OpWrap); n != 3 {
				t.Errorf("status %d: %d requests for 3 calls", code, n)
			}
		}
	})

	t.Run("a hanging service: the call returns within the timeout and earlier when the caller's context ends", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{}, withTimeout(300*time.Millisecond))
		r.start(t, false)
		r.s.Hang()
		r.s.Reset()
		var err error
		if d := callWithin(func() { _, _, err = r.p.Wrap(ctx(t), dek, aad) }); d > 500*time.Millisecond || err == nil {
			t.Errorf("a hanging service: %v after %v, want an error within 300 ms + 200 ms", err, d)
		}
		if !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Errorf("a timeout is %v, want ErrProviderUnavailable", err)
		}
		if n := r.s.Count(transittest.OpWrap); n != 1 {
			t.Errorf("%d requests for one call", n)
		}
		c, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if d := callWithin(func() { _, _, err = r.p.Wrap(c, dek, aad) }); d > 200*time.Millisecond || err == nil {
			t.Errorf("a caller's deadline of 50 ms: %v after %v", err, d)
		}
	})

	t.Run("a redirect is not followed", func(t *testing.T) {
		target := transittest.New(t)
		r := readyRig(t)
		r.s.Redirect(target.URL() + "/v1/transit/encrypt/pandokos")
		_, _, err := r.p.Wrap(ctx(t), dek, aad)
		if !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Errorf("a redirect: %v, want ErrProviderUnavailable", err)
		}
		if n := len(target.Requests()); n != 0 {
			t.Errorf("the redirect was followed: %d requests", n)
		}
	})

	t.Run("the buffers that held a data key are zeroed", func(t *testing.T) {
		spy := &bufferSpy{}
		r := newTransitRig(t, vaulttest.TransitSetup{}, withTransport(func(rt http.RoundTripper) http.RoundTripper { spy.inner = rt; return spy }))
		r.start(t, false)
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		back, err := r.p.Unwrap(ctx(t), version, wrapped, aad)
		if err != nil || !bytes.Equal(back, dek) {
			t.Fatalf("Unwrap: %v", err)
		}
		forms := byteForms(dek)
		spy.mu.Lock()
		defer spy.mu.Unlock()
		if len(spy.bodies) == 0 {
			t.Fatal("the request body has no GetBody: the buffer cannot be inspected; build it with bytes.NewReader so that it can")
		}
		for i, get := range spy.bodies {
			rc, err := get()
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(readerOf{rc})
			for _, f := range forms {
				if len(f) >= 20 && strings.Contains(buf.String(), f) {
					t.Errorf("the request buffer %d still holds the data key after the call", i)
				}
			}
		}
		for i, b := range spy.answered {
			for _, f := range forms {
				if len(f) >= 20 && bytes.Contains(b, []byte(f)) {
					t.Errorf("a buffer the answer %d was read into still holds the data key after the call", i)
				}
			}
		}
	})
}

type readerOf struct{ rc readCloser }

func (r readerOf) Read(p []byte) (int, error) { return r.rc.Read(p) }

// TestTransitRefusal is T-1063.
func TestTransitRefusal(t *testing.T) {
	t.Parallel()
	aad := vault.DataAAD(testTenant(5), 1)
	dek := dataKeyBytes(17)
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	t.Run("five unavailable answers start the refusal and it ends after 5 s", func(t *testing.T) {
		c := &tClock{t: start}
		r := newTransitRig(t, vaulttest.TransitSetup{}, withClock(c))
		r.start(t, false)
		r.s.Status(http.StatusServiceUnavailable)
		r.s.Reset()
		for i := 0; i < 5; i++ {
			if _, _, err := r.p.Wrap(ctx(t), dek, aad); !errors.Is(err, vault.ErrProviderUnavailable) {
				t.Fatalf("call %d: %v", i, err)
			}
		}
		if n := r.s.Count(transittest.OpWrap); n != 5 {
			t.Fatalf("%d requests for 5 calls", n)
		}
		if r.p.Stats().Up {
			t.Errorf("Stats().Up is true during the refusal")
		}
		for i := 0; i < 3; i++ {
			_, _, err := r.p.Wrap(ctx(t), dek, aad)
			te, ok := transitErr(err)
			if !ok || te.Class != "refused" || !errors.Is(err, vault.ErrProviderUnavailable) {
				t.Errorf("a call in the refusal: %v", err)
			}
		}
		if n := r.s.Count(transittest.OpWrap); n != 5 {
			t.Errorf("the refusal sent %d requests", n-5)
		}
		c.advance(4900 * time.Millisecond)
		_, _, _ = r.p.Wrap(ctx(t), dek, aad)
		if n := r.s.Count(transittest.OpWrap); n != 5 {
			t.Errorf("a request passed before the 5 s were over")
		}
		c.advance(200 * time.Millisecond)
		_, _, _ = r.p.Wrap(ctx(t), dek, aad) // one request passes; it fails and renews the refusal
		if n := r.s.Count(transittest.OpWrap); n != 6 {
			t.Fatalf("after 5 s %d requests, want one more", n)
		}
		_, _, _ = r.p.Wrap(ctx(t), dek, aad)
		if n := r.s.Count(transittest.OpWrap); n != 6 {
			t.Errorf("a failure of the request that passed did not renew the refusal")
		}
		// The service is back: after the next 5 s one request passes, succeeds and ends the refusal.
		r.s.Clear()
		c.advance(5100 * time.Millisecond)
		if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
			t.Fatalf("the request that passes: %v", err)
		}
		if !r.p.Stats().Up {
			t.Errorf("Stats().Up is false after a success")
		}
		for i := 0; i < 3; i++ {
			if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
				t.Errorf("after the refusal ended: %v", err)
			}
		}
		st := r.p.Stats().Requests
		if got := st[vault.TransitCount{Operation: "wrap", Outcome: "unavailable"}]; got != 6 {
			t.Errorf("wrap unavailable = %d, want 6", got)
		}
		if got := st[vault.TransitCount{Operation: "wrap", Outcome: "refused"}]; got != 5 {
			t.Errorf("wrap refused = %d, want 5", got)
		}
		if got := st[vault.TransitCount{Operation: "wrap", Outcome: "ok"}]; got != 4 {
			t.Errorf("wrap ok = %d, want 4", got)
		}
	})

	t.Run("400, 403 and 404 never start it", func(t *testing.T) {
		for _, code := range []int{400, 403, 404} {
			c := &tClock{t: start}
			r := newTransitRig(t, vaulttest.TransitSetup{}, withClock(c))
			r.start(t, false)
			r.s.Status(code)
			r.s.Reset()
			for i := 0; i < 12; i++ {
				_, _, err := r.p.Wrap(ctx(t), dek, aad)
				if te, ok := transitErr(err); ok && te.Class == "refused" {
					t.Fatalf("status %d: call %d was refused", code, i)
				}
			}
			if n := r.s.Count(transittest.OpWrap); n < 12 {
				t.Errorf("status %d: only %d of 12 calls reached the service", code, n)
			}
			if !r.p.Stats().Up {
				t.Errorf("status %d: Stats().Up is false", code)
			}
		}
	})

	t.Run("with every slot busy a further call waits and fails as refused at its deadline", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{}, withTimeout(400*time.Millisecond), func(o *vault.TransitOptions) { o.MaxConcurrent = 2 })
		r.start(t, false)
		r.s.Hang()
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, _, _ = r.p.Wrap(ctx(t), dek, aad) }()
		}
		testutil.Eventually(t, time.Second, "two requests in flight", func() bool { return r.s.Count(transittest.OpWrap) == 2 })
		var err error
		d := callWithin(func() { _, _, err = r.p.Wrap(ctx(t), dek, aad) })
		te, ok := transitErr(err)
		if !ok || te.Class != "refused" || !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Errorf("a call with no slot: %v", err)
		}
		if d < 200*time.Millisecond || d > 700*time.Millisecond {
			t.Errorf("the call with no slot returned after %v, want about its deadline of 400 ms", d)
		}
		if n := r.s.Count(transittest.OpWrap); n != 2 {
			t.Errorf("%d requests were sent, the third call had no slot", n)
		}
		wg.Wait()
	})

	t.Run("50 concurrent identical unwraps make one request and each gets a copy", func(t *testing.T) {
		r := readyRig(t)
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		r.s.Delay(150 * time.Millisecond)
		r.s.Reset()
		results := make([][]byte, 50)
		errs := make([]error, 50)
		var wg sync.WaitGroup
		for i := range results {
			wg.Add(1)
			go func() { defer wg.Done(); results[i], errs[i] = r.p.Unwrap(ctx(t), version, wrapped, aad) }()
		}
		wg.Wait()
		if n := r.s.Count(transittest.OpUnwrap); n != 1 {
			t.Errorf("%d decrypt requests for 50 identical calls, want 1", n)
		}
		for i := range results {
			if errs[i] != nil || !bytes.Equal(results[i], dek) {
				t.Fatalf("call %d: %v", i, errs[i])
			}
		}
		for i := 0; i < len(results)-1; i++ {
			clear(results[i])
		}
		if !bytes.Equal(results[len(results)-1], dek) {
			t.Errorf("a result changed when the others were cleared: they share a buffer")
		}
	})

	t.Run("Stats counts the outcomes of the design", func(t *testing.T) {
		r := readyRig(t)
		version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = r.p.Unwrap(ctx(t), version, wrapped, nil) // rejected
		r.s.Status(404)
		_, _, _ = r.p.Wrap(ctx(t), dek, aad) // not_found
		r.s.Status(403)
		_, _, _ = r.p.Wrap(ctx(t), dek, aad) // denied
		r.s.Clear()
		st := r.p.Stats()
		for _, c := range []vault.TransitCount{
			{Operation: "describe", Outcome: "ok"}, {Operation: "identify", Outcome: "ok"}, {Operation: "lookup", Outcome: "ok"},
			{Operation: "wrap", Outcome: "ok"}, {Operation: "unwrap", Outcome: "rejected"},
			{Operation: "wrap", Outcome: "not_found"}, {Operation: "wrap", Outcome: "denied"},
		} {
			if st.Requests[c] < 1 {
				t.Errorf("Stats has no %s %s", c.Operation, c.Outcome)
			}
		}
	})
}

// TestTransitNamespace is T-1066.
func TestTransitNamespace(t *testing.T) {
	t.Parallel()
	aad := vault.DataAAD(testTenant(6), 1)
	dek := dataKeyBytes(19)
	nsOf := func(s *transittest.Server) (with, without int) {
		for _, q := range s.Requests() {
			if q.Header.Get("X-Vault-Namespace") != "" {
				with++
			} else {
				without++
			}
		}
		return
	}

	for _, method := range []string{"token", "approle"} {
		t.Run("with the key set every request carries the header, "+method, func(t *testing.T) {
			r := newTransitRig(t, vaulttest.TransitSetup{Namespace: "team/a", Method: method}, func(o *vault.TransitOptions) { o.Namespace = "team/a" })
			r.start(t, false)
			version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.p.Unwrap(ctx(t), version, wrapped, aad); err != nil {
				t.Fatal(err)
			}
			if err := r.p.Available(ctx(t), version); err != nil {
				t.Fatal(err)
			}
			with, without := nsOf(r.s)
			if with == 0 || without != 0 {
				t.Errorf("%d requests with X-Vault-Namespace and %d without", with, without)
			}
			for _, q := range r.s.Requests() {
				if got := q.Header.Get("X-Vault-Namespace"); got != "team/a" {
					t.Errorf("%s %s carries the namespace %q", q.Method, q.Path, got)
				}
			}
			if method == "approle" && r.s.Count(transittest.OpLogin) == 0 {
				t.Errorf("no login was made")
			}
		})
	}

	t.Run("without the key no request carries it", func(t *testing.T) {
		r := readyRig(t)
		if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
			t.Fatal(err)
		}
		with, without := nsOf(r.s)
		if with != 0 || without == 0 {
			t.Errorf("%d requests with X-Vault-Namespace and %d without", with, without)
		}
	})

	t.Run("a key in a namespace is not found without the key", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{Namespace: "team/a"}, func(o *vault.TransitOptions) { o.Namespace = "" })
		err := r.p.Start(ctx(t))
		if err == nil {
			t.Fatal("Start found a key of a namespace without the namespace")
		}
		if te, ok := transitErr(err); !ok || (te.Class != "not_found" && te.Class != "denied") {
			t.Errorf("Start: %v, want not_found or denied", err)
		}
	})
}

// TestTransitCanary is T-1068: nothing secret reaches a log line, an error or the statistics, and
// nothing about a tenant reaches the service.
func TestTransitCanary(t *testing.T) {
	t.Parallel()
	tid := testTenant(7)
	aad := vault.DataAAD(tid, 1)
	dek := dataKeyBytes(23)

	for _, method := range []string{"token", "approle"} {
		t.Run(method, func(t *testing.T) {
			clk := &tClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
			r := newTransitRig(t, vaulttest.TransitSetup{Method: method, Namespace: "team/canary", Key: "canary-key-name"},
				func(o *vault.TransitOptions) { o.Namespace = "team/canary" }, withTimeout(300*time.Millisecond), withClock(clk))
			r.start(t, false)
			extra := []string{r.f.Token, r.f.RoleID, r.f.SecretID}
			r.s.EchoInErrors(append(extra, "canary-extra-text")...)
			version, wrapped, err := r.p.Wrap(ctx(t), dek, aad)
			if err != nil {
				t.Fatal(err)
			}
			var errs []error
			note := func(e error) {
				if e != nil {
					errs = append(errs, e)
				}
			}
			faults := []func(){
				func() { r.s.Sealed() }, func() { r.s.Status(400) }, func() { r.s.Status(403) }, func() { r.s.Status(404) },
				func() { r.s.Status(500) }, func() { r.s.Hang() }, func() { r.s.Drop() },
				func() { r.s.Redirect("http://127.0.0.1:1/") }, func() { r.s.OversizeAnswers() }, func() { r.s.IgnoreAssociatedData() },
			}
			for _, f := range faults {
				r.s.Clear()
				r.s.EchoInErrors(append(extra, "canary-extra-text")...)
				f()
				_, _, e := r.p.Wrap(ctx(t), dek, aad)
				note(e)
				_, e = r.p.Unwrap(ctx(t), version, wrapped, aad)
				note(e)
				_, e = r.p.Unwrap(ctx(t), version, wrapped, nil)
				note(e)
				note(r.p.Available(ctx(t), version))
				note(r.p.Start(ctx(t)))
				clk.advance(6 * time.Second) // the refusal after five failures ends before the next fault
			}
			r.s.Clear()

			secrets := transittest.Secrets(r.s, append(extra, "canary-extra-text", "canary-key-name")...)
			var outputs []string
			outputs = append(outputs, "log:"+r.logs.String(), fmt.Sprintf("stats:%+v", r.p.Stats()), fmt.Sprintf("info:%+v", r.p.Info()))
			for _, e := range errs {
				outputs = append(outputs, fmt.Sprintf("%v|%+v|%#v|%s", e, e, e, e))
				for w := e; w != nil; {
					outputs = append(outputs, fmt.Sprintf("%v %+v %#v", w, w, w))
					u, ok := w.(interface{ Unwrap() error })
					if !ok {
						break
					}
					w = u.Unwrap()
				}
			}
			for _, o := range outputs {
				for _, s := range secrets {
					if len(s) >= 6 && strings.Contains(o, s) {
						t.Errorf("an output (%.12q) holds a canary, a data key or a payload (%.20q)", o, s)
					}
				}
			}
			for _, q := range r.s.Requests() {
				text := q.Path + "\n" + fmt.Sprint(q.Header) + "\n" + string(q.Body)
				for _, f := range []string{tid.String(), hex.EncodeToString(tid[:]), base64.StdEncoding.EncodeToString(tid[:]), string(aad)} {
					if strings.Contains(text, f) {
						t.Errorf("a request to %s holds the tenant %q", q.Path, f)
					}
				}
			}
		})
	}
}
