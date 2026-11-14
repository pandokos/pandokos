package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/vault"
)

// TestFailSealLog checks the log line of a write that cannot be sealed (design 5.4, 5.10): the
// class of the cause, the identifiers known at write time, and nothing of the provider error.
func TestFailSealLog(t *testing.T) {
	const canary = "synthetic-canary-/etc/pandokos/kek-0001.key"
	for _, tc := range []struct {
		name  string
		cause string
		err   error
	}{
		{"a data key that does not unwrap", "data_key_unwrap", &vault.SealError{
			Cause: "data_key_unwrap", CredentialID: "cred-1", KEKVersion: "kek-1", DataKeyVersion: 1}},
		{"a key-encryption key that is not available", "kek_unavailable", &vault.SealError{
			Cause: "kek_unavailable", CredentialID: "cred-1", KEKVersion: "kek-1", DataKeyVersion: 1}},
		{"a failure that carries no detail", "kek_unavailable", vault.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := &server{log: slog.New(slog.NewJSONHandler(&buf, nil))}
			err := errors.Join(tc.err, errors.New(canary))
			w := httptest.NewRecorder()
			s.fail(w, httptest.NewRequest("POST", "/x", nil), nil, err)
			if w.Code != 503 {
				t.Errorf("status %d, want 503", w.Code)
			}
			if strings.Contains(buf.String()+w.Body.String(), canary) {
				t.Errorf("the provider error leaked: %s", buf.String())
			}
			var line map[string]any
			if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
				t.Fatalf("log %q: %v", buf.String(), err)
			}
			if line["msg"] != "credential cannot be sealed" || line["cause"] != tc.cause {
				t.Errorf("line %v, want cause %s", line, tc.cause)
			}
			var se *vault.SealError
			if errors.As(tc.err, &se) {
				for k, want := range map[string]any{"credential_id": "cred-1", "kek_version": "kek-1", "data_key_version": float64(1)} {
					if line[k] != want {
						t.Errorf("%s = %v, want %v", k, line[k], want)
					}
				}
				if _, ok := line["tenant_id"]; !ok {
					t.Errorf("no tenant_id in %v", line)
				}
			}
		})
	}
}
