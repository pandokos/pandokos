package config

import (
	"errors"
	"strconv"
	"testing"
)

// TestParseIntWidth checks that an integer key is parsed at the width of the field it sets, so no
// value is narrowed silently: a key that sets an int refuses what does not fit an int on the
// platform (32-bit included), and a key that sets an int64 takes the whole 64-bit range everywhere.
// Run it on a 32-bit build with GOARCH=386 to cover the narrow case.
func TestParseIntWidth(t *testing.T) {
	const beyondInt32 = "3000000000" // fits an int64 and a 64-bit int, not a 32-bit int

	setKey := func(t *testing.T, name, value string) (*Config, error) {
		t.Helper()
		for _, k := range keyTable {
			if k.Name == name {
				cfg := Defaults()
				return cfg, k.set(cfg, value)
			}
		}
		t.Fatalf("%s is not a key", name)
		return nil, nil
	}

	t.Run("parseInt follows the width of int", func(t *testing.T) {
		if n, err := parseInt("2147483647"); err != nil || n != 2147483647 {
			t.Errorf("max int32: %d %v", n, err)
		}
		if n, err := parseInt("-2147483648"); err != nil || n != -2147483648 {
			t.Errorf("min int32: %d %v", n, err)
		}
		n, err := parseInt(beyondInt32)
		if strconv.IntSize == 32 {
			if !errors.Is(err, errNotInteger) {
				t.Errorf("%s on a 32-bit int: %d %v, want errNotInteger", beyondInt32, n, err)
			}
		} else if err != nil || int64(n) != 3000000000 {
			t.Errorf("%s on a 64-bit int: %d %v", beyondInt32, n, err)
		}
		for _, bad := range []string{"", "x", "1.5", "99999999999999999999"} {
			if _, err := parseInt(bad); !errors.Is(err, errNotInteger) {
				t.Errorf("%q: %v, want errNotInteger", bad, err)
			}
		}
	})

	t.Run("parseInt64 takes the whole 64-bit range on every platform", func(t *testing.T) {
		for in, want := range map[string]int64{
			beyondInt32:            3000000000,
			"9223372036854775807":  1<<63 - 1,
			"-9223372036854775808": -1 << 63,
			"0":                    0,
		} {
			if n, err := parseInt64(in); err != nil || n != want {
				t.Errorf("%s: %d %v, want %d", in, n, err, want)
			}
		}
		for _, bad := range []string{"", "x", "1.5", "9223372036854775808"} {
			if _, err := parseInt64(bad); !errors.Is(err, errNotInteger) {
				t.Errorf("%q: %v, want errNotInteger", bad, err)
			}
		}
	})

	t.Run("keys that set an int never wrap a value", func(t *testing.T) {
		for _, name := range []string{
			"listeners.inference.max_header_bytes",
			"listeners.admin.max_header_bytes",
			"database.gateway.max_connections",
			"database.admin.max_connections",
			"database.worker.max_connections",
		} {
			if _, err := setKey(t, name, "12345"); err != nil {
				t.Errorf("%s: 12345: %v", name, err)
			}
			_, err := setKey(t, name, beyondInt32)
			if strconv.IntSize == 32 && !errors.Is(err, errNotInteger) {
				t.Errorf("%s: %s on a 32-bit int: %v, want errNotInteger", name, beyondInt32, err)
			}
			if strconv.IntSize == 64 && err != nil {
				t.Errorf("%s: %s on a 64-bit int: %v", name, beyondInt32, err)
			}
		}
	})

	t.Run("the key that sets an int64 takes a value beyond 32 bits on every platform", func(t *testing.T) {
		cfg, err := setKey(t, "limits.max_request_body_bytes", beyondInt32)
		if err != nil || cfg.Limits.MaxRequestBodyBytes != 3000000000 {
			t.Errorf("limits.max_request_body_bytes: %d %v", cfg.Limits.MaxRequestBodyBytes, err)
		}
	})
}
