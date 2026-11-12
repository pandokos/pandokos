package telemetry

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not a JSON object: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func TestLoggerShape(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf, "info")
	l.Debug("hidden")
	l.Info("one", "a", 1)
	l.Warn("two")
	l.Error("three")
	lines := decode(t, &buf)
	if len(lines) != 3 {
		t.Fatalf("got %d lines: %s", len(lines), buf.String())
	}
	for i, level := range []string{"INFO", "WARN", "ERROR"} {
		m := lines[i]
		if m["level"] != level {
			t.Errorf("line %d level %v", i, m["level"])
		}
		ts, _ := m["time"].(string)
		parsed, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil || !strings.HasSuffix(ts, "Z") || parsed.Location() != time.UTC {
			t.Errorf("time %q is not UTC RFC 3339: %v", ts, err)
		}
		if _, ok := m["source"]; ok {
			t.Error("source must not be present")
		}
	}
	if lines[0]["msg"] != "one" || lines[0]["a"] != float64(1) {
		t.Errorf("fields: %v", lines[0])
	}
	buf.Reset()
	NewLogger(&buf, "debug").Debug("shown")
	if len(decode(t, &buf)) != 1 {
		t.Error("debug level not honoured")
	}
	buf.Reset()
	NewLogger(&buf, "error").Warn("hidden")
	if buf.Len() != 0 {
		t.Error("error level not honoured")
	}
	buf.Reset()
	NewLogger(&buf, "bogus").Debug("hidden")
	NewLogger(&buf, "bogus").Info("shown")
	if len(decode(t, &buf)) != 1 {
		t.Error("unknown level must fall back to info")
	}
}

func TestRedaction(t *testing.T) {
	redacted := []string{
		"authorization", "Authorization", "proxy-authorization", "cookie", "Set-Cookie", "x-api-key",
		"api-key", "apikey", "password", "passwd", "secret", "token", "credential", "client_secret",
		"db_password", "access_token", "provider_key", "api_key", "X-API-KEY",
	}
	kept := []string{"input_tokens", "output_tokens", "cache_read_tokens", "request_id", "status", "key_id", "tokens"}
	for _, k := range redacted {
		if !IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = false", k)
		}
	}
	for _, k := range kept {
		if IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = true", k)
		}
	}
	const v = "very-secret-value"
	for _, k := range redacted {
		var buf bytes.Buffer
		l := NewLogger(&buf, "info")
		l.Info("m", k, v)
		l.Info("m", slog.Group("g", k, v))
		l.Info("m", slog.Group("g", slog.Group("h", k, v)))
		if strings.Contains(buf.String(), v) {
			t.Errorf("key %q leaked: %s", k, buf.String())
		}
		for _, m := range decode(t, &buf) {
			if !strings.Contains(toJSON(m), "[REDACTED]") {
				t.Errorf("key %q not replaced by [REDACTED]: %v", k, m)
			}
		}
	}
	for _, k := range kept {
		var buf bytes.Buffer
		NewLogger(&buf, "info").Info("m", k, v, slog.Group("g", k, v))
		if strings.Count(buf.String(), v) != 2 {
			t.Errorf("key %q must not be redacted: %s", k, buf.String())
		}
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
