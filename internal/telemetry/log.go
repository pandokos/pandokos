// Package telemetry provides the structured logger and its redaction, and the trace provider.
package telemetry

import (
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

var sensitiveKeys = map[string]bool{
	"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true,
	"x-api-key": true, "api-key": true, "apikey": true, "password": true, "passwd": true,
	"secret": true, "token": true, "credential": true,
}

var sensitiveSuffixes = []string{"_secret", "_password", "_token", "_key"}

// IsSensitiveKey reports whether a log attribute key must have its value redacted.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if sensitiveKeys[k] {
		return true
	}
	for _, s := range sensitiveSuffixes {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewLogger returns a JSON logger writing to w at the given level ("debug", "info", "warn",
// "error"). An unknown level means info.
func NewLogger(w io.Writer, level string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: parseLevel(level),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			return replaceAttr(len(groups), a)
		},
	}))
}

func replaceAttr(depth int, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && depth == 0 {
		if t := a.Value.Time(); !t.IsZero() {
			a.Value = slog.TimeValue(t.UTC().Truncate(time.Nanosecond))
		}
		return a
	}
	if a.Value.Kind() != slog.KindGroup && IsSensitiveKey(a.Key) {
		return slog.String(a.Key, secret.Redacted)
	}
	return a
}
