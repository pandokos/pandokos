package secret_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/test/fixtures"
)

const value = fixtures.CanaryProviderKey

func TestSecretNeverPrints(t *testing.T) {
	s := secret.New(value)
	ps := &s
	holder := struct {
		Name string
		S    secret.Secret
	}{"n", s}
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T%v"}
	operands := map[string]any{"value": s, "pointer": ps, "struct": holder, "pointer to struct": &holder}
	for _, verb := range verbs {
		for name, op := range operands {
			out := fmt.Sprintf(verb, op)
			if strings.Contains(out, value) {
				t.Errorf("%s of %s leaks the value", verb, name)
			}
			if verb != "%T%v" && !strings.Contains(out, secret.Redacted) && name == "value" {
				t.Errorf("%s of %s does not print %s: %q", verb, name, secret.Redacted, out)
			}
		}
	}
	if got := fmt.Sprintf("%+v", holder); !strings.Contains(got, secret.Redacted) {
		t.Errorf("struct print lacks %s: %q", secret.Redacted, got)
	}

	b, err := json.Marshal(holder)
	if err != nil || bytes.Contains(b, []byte(value)) || !bytes.Contains(b, []byte(secret.Redacted)) {
		t.Errorf("json.Marshal: %s %v", b, err)
	}
	if s.String() != secret.Redacted || s.GoString() != secret.Redacted {
		t.Error("String or GoString leaks")
	}
	if mt, _ := s.MarshalText(); string(mt) != secret.Redacted {
		t.Error("MarshalText leaks")
	}

	for name, mk := range map[string]func(*bytes.Buffer) slog.Handler{
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		var buf bytes.Buffer
		slog.New(mk(&buf)).Info("m", slog.Any("k", s), slog.Any("h", holder))
		if strings.Contains(buf.String(), value) || !strings.Contains(buf.String(), secret.Redacted) {
			t.Errorf("slog %s handler: %s", name, buf.String())
		}
	}
}

func TestRevealAndEqual(t *testing.T) {
	s := secret.New(value)
	if s.Reveal() != value {
		t.Error("Reveal")
	}
	if !s.Equal(value) || s.Equal(value+"x") || s.Equal("") || s.Equal(value[:len(value)-1]) {
		t.Error("Equal")
	}
	if s.IsZero() || !(secret.Secret{}).IsZero() || !secret.New("").IsZero() {
		t.Error("IsZero")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, content []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("trims", func(t *testing.T) {
		s, err := secret.ReadFile(write("ok", []byte("  \t"+value+"\r\n\n")))
		if err != nil || s.Reveal() != value {
			t.Fatalf("got %v", err)
		}
	})
	cases := map[string][]byte{
		"empty":         {},
		"blank":         []byte(" \n\t\n"),
		"too big":       bytes.Repeat([]byte("a"), 8193),
		"newline":       []byte("ab\ncd"),
		"control":       []byte("ab\x01cd"),
		"del":           []byte("ab\x7fcd"),
		"nul":           []byte("ab\x00cd"),
		"tab inside":    []byte("ab\tcd"),
		"canary inside": []byte(value + "\x02"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := secret.ReadFile(write(strings.ReplaceAll(name, " ", "_"), content))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "abcd") {
				t.Errorf("error contains content: %v", err)
			}
		})
	}
	t.Run("exactly 8192 bytes is accepted", func(t *testing.T) {
		if _, err := secret.ReadFile(write("max", bytes.Repeat([]byte("a"), 8192))); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("empty path", func(t *testing.T) {
		if _, err := secret.ReadFile(""); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, err := secret.ReadFile(filepath.Join(dir, "missing"))
		if err == nil || !os.IsNotExist(unwrapAll(err)) && !strings.Contains(err.Error(), "missing") {
			t.Fatalf("got %v", err)
		}
	})
}

func unwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}
