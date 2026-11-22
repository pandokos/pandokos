package telemetry_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/testutil"
)

// text is a fmt.Stringer over a string, so that the JSON encoder alone would print the secret.
type text string

func (t text) String() string { return string(t) }

// redactions reads pandokos.log.redactions (M41) now.
func redactions(t *testing.T, r *rig) float64 {
	t.Helper()
	return r.collectT(t).total(t, "pandokos.log.redactions", nil)
}

// secretShapes returns the four shapes of section 5.8, made by the generators of the product.
func secretShapes(t *testing.T) map[string]string {
	t.Helper()
	key, _, _, err := authn.Generate(authn.KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	pkey, _, _, err := authn.Generate(authn.KindPlatform)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := session.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	idp := fakeidp.Start(t, fakeidp.Options{})
	jws := idp.Sign("RS256", fakeidp.Claims{"iss": idp.Issuer(), "sub": "synthetic-subject", "aud": "pandokos"})
	if strings.Count(jws, ".") != 2 || !strings.HasPrefix(jws, "eyJ") {
		t.Fatalf("the fake identity provider made %q, not a compact JWS", jws)
	}
	return map[string]string{
		"a virtual key":   key.Reveal(),
		"a platform key":  pkey.Reveal(),
		"a session token": tok.Reveal(),
		"a bearer value":  "Bearer abc.def",
		"a dotted bearer value with the whole token68 set": "Bearer ab.c+d/e=",
		"a compact JWS":      jws,
		"a bearer JWS value": "bearer " + jws,
	}
}

// TestRedactingHandler is T-907: a secret of each shape, placed in the message, in a string value, in
// a group two levels deep, in an error value and in a fmt.Stringer, is replaced by [REDACTED] with the
// rest of the text kept; the redactions are counted by M41; the key-based redaction still holds.
func TestRedactingHandler(t *testing.T) {
	r := newRig(t, nil)
	for name, secretValue := range secretShapes(t) {
		t.Run(name, func(t *testing.T) {
			bufs := &testutil.LogBuffer{}
			log := telemetry.NewLogger(bufs, "debug")
			before := redactions(t, r)

			in := "before " + secretValue + " after"
			bearer := strings.HasPrefix(strings.ToLower(secretValue), "bearer ")
			wantText := "before [REDACTED] after"
			if bearer {
				wantText = "before Bearer [REDACTED] after"
			}
			var calls = []struct {
				where string
				emit  func()
				read  func(line map[string]any) string
			}{
				{"the message", func() { log.Info(in) }, func(l map[string]any) string { s, _ := l["msg"].(string); return s }},
				{"a string value", func() { log.Info("m", "detail", in) }, func(l map[string]any) string { s, _ := l["detail"].(string); return s }},
				{"a group two levels deep", func() { log.Info("m", slog.Group("outer", slog.Group("inner", slog.String("detail", in)))) }, func(l map[string]any) string {
					o, _ := l["outer"].(map[string]any)
					i, _ := o["inner"].(map[string]any)
					s, _ := i["detail"].(string)
					return s
				}},
				{"an error value", func() { log.Info("m", "err", errors.New(in)) }, func(l map[string]any) string { s, _ := l["err"].(string); return s }},
				{"a fmt.Stringer", func() { log.Info("m", "detail", text(in)) }, func(l map[string]any) string { s, _ := l["detail"].(string); return s }},
			}
			for _, c := range calls {
				bufs.Reset()
				c.emit()
				lines := bufs.Lines(t)
				if len(lines) != 1 {
					t.Fatalf("%s: %d lines", c.where, len(lines))
				}
				got := c.read(lines[0])
				if !strings.Contains(got, "[REDACTED]") {
					t.Errorf("%s: no [REDACTED] in %q", c.where, got)
				}
				testutil.AssertAbsent(t, c.where, bufs.String(), secretValue, strings.TrimPrefix(strings.TrimPrefix(secretValue, "Bearer "), "bearer "))
				if got != wantText {
					t.Errorf("%s: %q, want %q (the rest of the text is kept)", c.where, got, wantText)
				}
			}
			if got := redactions(t, r) - before; got != float64(len(calls)) {
				t.Errorf("M41 counted %v rewritten values, want %d", got, len(calls))
			}
		})
	}

	t.Run("attributes given through With are rewritten", func(t *testing.T) {
		bufs := &testutil.LogBuffer{}
		key, _, _, _ := authn.Generate(authn.KindTenant)
		log := telemetry.NewLogger(bufs, "debug").With("detail", "x "+key.Reveal()).WithGroup("g")
		log.Info("m", "more", "y "+key.Reveal())
		testutil.AssertAbsent(t, "the log", bufs.String(), key.Reveal())
		if !strings.Contains(bufs.String(), "[REDACTED]") {
			t.Errorf("no redaction in %s", bufs.String())
		}
	})

	t.Run("the key-based redaction holds and is not counted as a shape", func(t *testing.T) {
		bufs := &testutil.LogBuffer{}
		log := telemetry.NewLogger(bufs, "debug")
		before := redactions(t, r)
		log.Info("m", "password", "plain-synthetic-value", "api_key", "another", slog.Group("g", slog.String("token", "nested")))
		testutil.AssertAbsent(t, "the log", bufs.String(), "plain-synthetic-value", "another", "nested")
		if got := redactions(t, r) - before; got != 0 {
			t.Errorf("a redaction by key moved M41 by %v", got)
		}
	})
}

// TestRedactionKeepsOperationalValues is T-908: request IDs, 32-hex trace IDs, UUIDs, pdk_ alone, eyJ
// alone, the word "bearer" in a sentence without a value, durations and SQLSTATEs are not changed
// and not counted.
func TestRedactionKeepsOperationalValues(t *testing.T) {
	r := newRig(t, nil)
	bufs := &testutil.LogBuffer{}
	log := telemetry.NewLogger(bufs, "debug")
	before := redactions(t, r)

	values := []string{
		"4bf92f3577b34da6a3ce929d0e0e4736", // a trace ID and a request ID: 32 hexadecimal characters
		"0af7651916cd43dd8448eb211c80319c",
		testutil.RandomUUID(),
		"pdk_",
		"pdk_t",
		"pds_",
		"eyJ",
		"eyJhbGciOiJ",
		"the scheme is bearer.",
		"bearer: not accepted here",
		"Bearer",
		"the bearer token was refused",
		"Bearer authentication failed for this request",
		"1.5s",
		"250ms",
		"23505",
		"40001",
		"sqlstate 23505 on unique_violation",
		"a message with eyJ in the middle",
	}
	for _, v := range values {
		bufs.Reset()
		log.Info(v, "detail", v, "request_id", v)
		lines := bufs.Lines(t)
		if len(lines) != 1 {
			t.Fatalf("%q: %d lines", v, len(lines))
		}
		for _, k := range []string{"msg", "detail", "request_id"} {
			if got := lines[0][k]; got != v {
				t.Errorf("%s: %q was changed to %q", k, v, got)
			}
		}
		if strings.Contains(bufs.String(), "[REDACTED]") {
			t.Errorf("%q was redacted", v)
		}
	}
	if got := redactions(t, r) - before; got != 0 {
		t.Errorf("operational values moved M41 by %v", got)
	}
}
