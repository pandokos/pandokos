package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/pandokos/pandokos/internal/secret"
)

// redactions counts the values the log handler rewrote because they had the shape of a secret
// (M41). Any non-zero value means a secret reached a logger.
var redactions atomic.Int64

// Redactions returns how many values the log handler has rewritten by pattern since the process
// started.
func Redactions() int64 { return redactions.Load() }

// The shapes of a secret in a value (section 5.8). They are the product's own shapes, repeated
// here because this package imports no product package: authn.Pattern for a key and
// session.TokenPrefix with 32 bytes in base64url for a session token. A test checks that the
// generators of the product produce text they match.
var (
	// A bearer value looks like a credential: at least 16 characters of the token68 set, or
	// dot-separated (a JWS). An ordinary sentence ("the bearer token was refused") is kept.
	bearerPattern  = regexp.MustCompile(`(?i)\bbearer\s+(?:[A-Za-z0-9._~+/=-]{16,}|[A-Za-z0-9_-]+\.[A-Za-z0-9._~+/=-]+)`)
	keyPattern     = regexp.MustCompile(`pdk_[tp][a-z2-7]{16}_[a-z2-7]{59}`)
	sessionPattern = regexp.MustCompile(`pds_[A-Za-z0-9_-]{43}`)
	jwsPattern     = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
)

const bearerRedacted = "Bearer " + secret.Redacted

// mayHoldSecret is the prefilter: one scan of the string for the text every pattern needs, so that
// a line without any costs no regular expression.
func mayHoldSecret(s string) bool {
	if strings.Contains(s, "pdk_") || strings.Contains(s, "pds_") || strings.Contains(s, "eyJ") {
		return true
	}
	for i := 0; i+6 <= len(s); i++ {
		if (s[i] == 'b' || s[i] == 'B') && strings.EqualFold(s[i:i+6], "bearer") {
			return true
		}
	}
	return false
}

// redactValue replaces every secret shape in s by [REDACTED], keeping the rest of the text, and
// reports whether it replaced any.
func redactValue(s string) (string, bool) {
	if !mayHoldSecret(s) {
		return s, false
	}
	out := bearerPattern.ReplaceAllLiteralString(s, bearerRedacted)
	out = keyPattern.ReplaceAllLiteralString(out, secret.Redacted)
	out = sessionPattern.ReplaceAllLiteralString(out, secret.Redacted)
	out = jwsPattern.ReplaceAllLiteralString(out, secret.Redacted)
	return out, out != s
}

// redactString is redactValue that counts a rewrite on M41.
func redactString(s string) (string, bool) {
	out, changed := redactValue(s)
	if changed {
		redactions.Add(1)
	}
	return out, changed
}

// textOf returns the text of an error or a fmt.Stringer; a panicking method yields none.
func textOf(v any) (text string, ok bool) {
	defer func() {
		if recover() != nil {
			text, ok = "", false
		}
	}()
	switch x := v.(type) {
	case error:
		return x.Error(), true
	case fmt.Stringer:
		return x.String(), true
	}
	return "", false
}

// redactAttr rewrites the value of an attribute, through groups. An attribute with a sensitive key
// is left to the key rule of the JSON handler. A value of kind Any that is an error or a Stringer
// is turned into its text before matching, and replaced by the rewritten text only when a pattern
// matched.
func redactAttr(a slog.Attr) (slog.Attr, bool) {
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindGroup:
		group := a.Value.Group()
		out := make([]slog.Attr, len(group))
		changed := false
		for i, g := range group {
			var c bool
			out[i], c = redactAttr(g)
			changed = changed || c
		}
		if !changed {
			return a, false
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}, true
	case slog.KindString:
		if IsSensitiveKey(a.Key) {
			return a, false
		}
		if s, ok := redactString(a.Value.String()); ok {
			return slog.String(a.Key, s), true
		}
	case slog.KindAny:
		if IsSensitiveKey(a.Key) {
			return a, false
		}
		if text, ok := textOf(a.Value.Any()); ok {
			if s, ok := redactString(text); ok {
				return slog.String(a.Key, s), true
			}
		}
	}
	return a, false
}

// redactingHandler wraps the JSON handler and rewrites every secret shape in the message and in
// the attributes before they are written (section 5.8). The redaction by key stays the JSON
// handler's.
type redactingHandler struct{ next slog.Handler }

// Enabled implements slog.Handler.
func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	msg, changed := redactString(r.Message)
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		na, c := redactAttr(a)
		attrs = append(attrs, na)
		changed = changed || c
		return true
	})
	if !changed {
		return h.next.Handle(ctx, r)
	}
	nr := slog.NewRecord(r.Time, r.Level, msg, r.PC)
	nr.AddAttrs(attrs...)
	return h.next.Handle(ctx, nr)
}

// WithAttrs implements slog.Handler: the attributes are rewritten once, when given.
func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i], _ = redactAttr(a)
	}
	return redactingHandler{next: h.next.WithAttrs(out)}
}

// WithGroup implements slog.Handler.
func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(name)}
}
