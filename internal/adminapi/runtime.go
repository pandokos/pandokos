// Package adminapi is the administration API as Go: the types, the server interface, the router
// and the client. adminapi.gen.go is generated from api/openapi/admin.v1.yaml by
// internal/apigen; this file is the hand-written part the generated code relies on. The package
// imports only the standard library.
package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Operation describes one operation of the document.
type Operation struct {
	ID         string // operationId
	Method     string
	Path       string // as in the document, with {name} segments
	Permission string // x-pandokos-permission
	Status     int    // status of the successful response
	HasBody    bool
	PathParams []PathParam
}

// PathParam is one {name} segment and the pattern its value must match.
type PathParam struct {
	Name    string
	Pattern *regexp.Regexp
}

// Nullable is a member that can be absent, null or a value (ServiceAccountUpdate.valid_until).
type Nullable[T any] struct {
	Set   bool // the member was present
	Null  bool // it was null
	Value T
}

// Null returns a member that is present and null.
func Null[T any]() Nullable[T] { return Nullable[T]{Set: true, Null: true} }

// Value returns a member that is present with a value.
func Value[T any](v T) Nullable[T] { return Nullable[T]{Set: true, Value: v} }

// IsZero makes the omitzero tag leave an absent member out.
func (n Nullable[T]) IsZero() bool { return !n.Set }

// MarshalJSON writes null or the value.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if n.Null || !n.Set {
		return []byte("null"), nil
	}
	return json.Marshal(n.Value)
}

// UnmarshalJSON records that the member was present, and whether it was null.
func (n *Nullable[T]) UnmarshalJSON(b []byte) error {
	n.Set, n.Null = true, false
	if string(bytes.TrimSpace(b)) == "null" {
		n.Null = true
		return nil
	}
	return json.Unmarshal(b, &n.Value)
}

// RequestError is a request that the router refuses before the Server sees it.
type RequestError struct {
	Status int          // 400, 404, 413 or 415
	Code   ProblemCode  // invalid_request, not_found, request_too_large, unsupported_media_type
	Fields []FieldError // for invalid_request, when single members are at fault
}

func (e *RequestError) Error() string { return "adminapi: " + string(e.Code) }

// ErrNoRoute is passed to Hooks.Error when no operation matches the method and the path.
var ErrNoRoute = &RequestError{Status: http.StatusNotFound, Code: ProblemCodeNotFound}

func invalid(fields []FieldError) *RequestError {
	return &RequestError{Status: http.StatusBadRequest, Code: ProblemCodeInvalidRequest, Fields: fields}
}

func invalidField(field, message string) *RequestError {
	return invalid([]FieldError{{Field: field, Message: message}})
}

// Hooks connect the generated router to the code that authenticates, authorizes and reports.
type Hooks struct {
	// Before runs when an operation matched, before the query and the body are read. It
	// authenticates and authorizes; the context it returns is the one the Server receives.
	Before func(ctx context.Context, r *http.Request, op *Operation, params map[string]string) (context.Context, error)
	// Error writes the response for err: ErrNoRoute (op is nil), an error of Before, a
	// *RequestError, or an error of the Server.
	Error func(w http.ResponseWriter, r *http.Request, op *Operation, err error)
	// MaxBodyBytes limits request bodies.
	MaxBodyBytes int64
}

type handler struct {
	server Server
	hooks  Hooks
}

// NewHandler returns the router of the administration API. All three hooks are required.
func NewHandler(s Server, h Hooks) http.Handler {
	if s == nil || h.Before == nil || h.Error == nil || h.MaxBodyBytes <= 0 {
		panic("adminapi: NewHandler needs a Server and all Hooks")
	}
	return &handler{server: s, hooks: h}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, op *Operation, err error) {
	h.hooks.Error(w, r, op, err)
}

// Match finds the operation for a method and a path. A path parameter that does not match its
// pattern is not a match: a malformed identifier is the same as an unknown resource.
func Match(method, path string) (*Operation, map[string]string) {
	if !strings.HasPrefix(path, "/") {
		return nil, nil
	}
	segs := strings.Split(path[1:], "/")
next:
	for i := range Operations {
		op := &Operations[i]
		want := strings.Split(op.Path[1:], "/")
		if op.Method != method || len(want) != len(segs) {
			continue
		}
		params := map[string]string{}
		for j, w := range want {
			if strings.HasPrefix(w, "{") {
				name := w[1 : len(w)-1]
				for _, p := range op.PathParams {
					if p.Name == name && !p.Pattern.MatchString(segs[j]) {
						continue next
					}
				}
				params[name] = segs[j]
			} else if w != segs[j] {
				continue next
			}
		}
		return op, params
	}
	return nil, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op, params := Match(r.Method, r.URL.Path)
	if op == nil {
		h.fail(w, r, nil, ErrNoRoute)
		return
	}
	ctx, err := h.hooks.Before(r.Context(), r, op, params)
	if err != nil {
		h.fail(w, r, op, err)
		return
	}
	h.serve(op, w, r.WithContext(ctx), params)
}

// parseQuery returns the query parameters. A parameter that the operation does not declare, or
// that is given twice, is an error: nothing a caller adds is silently ignored.
func parseQuery(r *http.Request, allowed ...string) (map[string]string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalidField("query", "is not a valid query string")
	}
	out := map[string]string{}
	var errs []FieldError
	for name, vs := range values {
		known := false
		for _, a := range allowed {
			known = known || a == name
		}
		switch {
		case !known:
			errs = append(errs, FieldError{Field: "query", Message: "has a parameter this operation does not take"})
		case len(vs) != 1:
			errs = append(errs, FieldError{Field: name, Message: "must be given once"})
		default:
			out[name] = vs[0]
		}
	}
	if len(errs) > 0 {
		return nil, invalid(errs[:1])
	}
	return out, nil
}

// decodeBody reads a JSON object into dst. Unknown members, missing required members, a second
// JSON value and a body over the limit are errors. A body that is not required may be empty.
func decodeBody(r *http.Request, limit int64, bodyRequired bool, required []string, dst any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &RequestError{Status: http.StatusRequestEntityTooLarge, Code: ProblemCodeRequestTooLarge}
		}
		return invalidField("body", "could not be read")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if bodyRequired {
			return invalidField("body", "is required")
		}
		return nil
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return &RequestError{Status: http.StatusUnsupportedMediaType, Code: ProblemCodeUnsupportedMediaType}
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return invalidField("body", "must be a JSON object")
	}
	var errs []FieldError
	for _, name := range required {
		if v, ok := members[name]; !ok || string(v) == "null" {
			errs = append(errs, FieldError{Field: name, Message: "is required"})
		}
	}
	if len(errs) > 0 {
		return invalid(errs)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return invalidField(te.Field, "has the wrong type")
		}
		if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			return invalidField(strings.Trim(name, `"`), "is not a member of this body")
		}
		if name := wrongMember(members, dst); name != "" {
			return invalidField(name, "is not a valid value")
		}
		return invalidField("body", "does not match the schema")
	}
	return nil
}

// wrongMember names the member of the body that does not decode into its field, for the errors
// that encoding/json reports without a field name (a malformed date-time, for one). It returns
// "" when no single member is at fault.
func wrongMember(members map[string]json.RawMessage, dst any) string {
	t := reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return ""
	}
	st := t.Elem()
	for i := range st.NumField() {
		f := st.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		raw, ok := members[name]
		if !ok || name == "" || name == "-" {
			continue
		}
		if json.Unmarshal(raw, reflect.New(f.Type).Interface()) != nil {
			return name
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("adminapi: response of type %T cannot be encoded", v))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func runeCount(s string) int { return utf8.RuneCountInString(s) }

func unique(items []string) bool {
	seen := map[string]bool{}
	for _, s := range items {
		if seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}

// ProblemError is an error response of the API, as the client returns it.
type ProblemError struct {
	Problem Problem
}

func (e *ProblemError) Error() string {
	return string(e.Problem.Code) + ": " + e.Problem.Detail + " (request " + e.Problem.RequestID + ")"
}

// Client calls the administration API. The zero value is not usable: set BaseURL and Key.
type Client struct {
	BaseURL    string               // for example https://127.0.0.1:8081, without a trailing slash
	HTTP       *http.Client         // http.DefaultClient when nil
	Key        func() string        // returns the key for the Authorization header
	OnResponse func(*http.Response) // optional: sees every response before its body is read
	UserAgent  string
}

const maxResponseBytes = 8 << 20

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, want int, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("adminapi: encode the request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	u := strings.TrimSuffix(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return fmt.Errorf("adminapi: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Key != nil {
		req.Header.Set("Authorization", "Bearer "+c.Key())
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is not repeated in the message
		}
		return fmt.Errorf("adminapi: the server could not be reached: %w", err)
	}
	defer resp.Body.Close()
	if c.OnResponse != nil {
		c.OnResponse(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("adminapi: read the response: %w", err)
	}
	if resp.StatusCode != want {
		var p Problem
		if json.Unmarshal(raw, &p) != nil || p.Code == "" {
			return fmt.Errorf("adminapi: unexpected response with status %d", resp.StatusCode)
		}
		return &ProblemError{Problem: p}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("adminapi: decode the response: %w", err)
	}
	return nil
}
