// Package apitest gives tests a migrated database with the administration API running in front
// of it: the handler of internal/admin behind httptest, a bootstrap platform key made with the
// code path of pandokos init, and a client of the generated package.
package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// API is one running administration API.
type API struct {
	DB    *dbtest.Database
	URL   string              // of the httptest server in front of admin.New
	Logs  *testutil.LogBuffer // the handler's logger, level debug
	Admin *store.DB           // the admin pool the handler uses

	srv         *httptest.Server
	platformKey string
	mu          sync.Mutex
	secrets     []string
	super       *pgx.Conn
}

// Response is a raw response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

var reKey = regexp.MustCompile(authn.Pattern)

// New creates a migrated database and an API on it. The optional functions change the options
// of the handler before it is built (for example the clock).
func New(tb testing.TB, mutate ...func(*admin.Options)) *API {
	tb.Helper()
	return NewOn(tb, dbtest.NewMigrated(tb), mutate...)
}

// NewOn builds the same on a given, already migrated database; the database may be the pooled
// view of a Database.
func NewOn(tb testing.TB, d *dbtest.Database, mutate ...func(*admin.Options)) *API {
	tb.Helper()
	a := &API{DB: d, Logs: &testutil.LogBuffer{}}
	a.Admin = d.Open(tb, store.RoleAdmin)
	logger := telemetry.NewLogger(a.Logs, "debug")
	auth, err := authn.New(authn.Options{
		DB: a.Admin, Logger: logger, Kinds: []authn.Kind{authn.KindTenant, authn.KindPlatform},
	})
	if err != nil {
		tb.Fatal(err)
	}
	o := admin.Options{Logger: logger, DB: a.Admin, Authenticator: auth}
	for _, m := range mutate {
		m(&o)
	}
	h, err := admin.New(o)
	if err != nil {
		tb.Fatal(err)
	}
	a.srv = httptest.NewServer(a.recording(h))
	tb.Cleanup(a.srv.Close)
	a.URL = a.srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := admin.Bootstrap(ctx, a.Admin, admin.BootstrapOptions{TTL: time.Hour, Description: "apitest"})
	if err != nil {
		tb.Fatalf("bootstrap: %v", err)
	}
	a.platformKey = res.Key.Reveal()
	a.track(a.platformKey)
	return a
}

// recording notes every key that appears in a response, so that Secrets is complete whatever
// code issued the key.
func (a *API) recording(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &teeWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		for _, k := range reKey.FindAllString(rec.buf.String(), -1) {
			a.track(k)
		}
	})
}

type teeWriter struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.buf.Write(p)
	return t.ResponseWriter.Write(p)
}

func (a *API) track(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range a.secrets {
		if k == key {
			return
		}
	}
	a.secrets = append(a.secrets, key)
}

// PlatformKey returns the bootstrap platform key.
func (a *API) PlatformKey() string { return a.platformKey }

// Client returns a generated client that authenticates with key.
func (a *API) Client(key string) *adminapi.Client {
	return &adminapi.Client{BaseURL: a.URL, HTTP: a.srv.Client(), Key: func() string { return key }}
}

// Tenant creates a tenant through the API with the platform key and returns it with the key of
// its first administrator.
func (a *API) Tenant(tb testing.TB, name string) (t adminapi.Tenant, adminKey string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := a.Client(a.platformKey).CreateTenant(ctx, adminapi.CreateTenantInput{Body: adminapi.TenantCreate{Name: name}})
	if err != nil {
		tb.Fatalf("create tenant %s: %v", name, err)
	}
	a.track(out.AdminKey.Secret)
	return out.Tenant, out.AdminKey.Secret
}

// Do sends a raw request. body is nil, a string or []byte (sent as it is), or any value (sent
// as JSON). A body gets Content-Type application/json unless hdr sets one.
func (a *API) Do(tb testing.TB, method, path, key string, body any, hdr http.Header) *Response {
	tb.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		enc, err := json.Marshal(b)
		if err != nil {
			tb.Fatal(err)
		}
		rd = bytes.NewReader(enc)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, a.URL+path, rd)
	if err != nil {
		tb.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		tb.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Fatal(err)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

// Secrets returns the forms (testutil.KeyForms) of every key issued through a.
func (a *API) Secrets() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, k := range a.secrets {
		out = append(out, testutil.KeyForms(k)...)
	}
	return out
}

// Super returns one superuser connection of the database, made on the first call and kept for the
// life of the test (a new connection for every call would exhaust the server).
func (a *API) Super(tb testing.TB) *pgx.Conn {
	tb.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.super == nil {
		a.super = a.DB.Super(tb)
	}
	return a.super
}

// Tables returns the names of the tables of schema pandokos, from the catalogue.
func (a *API) Tables(tb testing.TB) []string {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := a.Super(tb).Query(ctx, `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') ORDER BY 1`)
	if err != nil {
		tb.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			tb.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}
	return names
}

// Snapshot returns every row of every table of the schema (the tables in exclude left out) as
// one JSON text, read as superuser. Two snapshots are equal when nothing changed.
func (a *API) Snapshot(tb testing.TB, exclude ...string) string {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var sb strings.Builder
	for _, name := range a.Tables(tb) {
		skip := false
		for _, e := range exclude {
			skip = skip || e == name
		}
		if skip {
			continue
		}
		var s string
		if err := a.Super(tb).QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text, '[]') FROM pandokos.`+pgxIdent(name)+` t`).Scan(&s); err != nil {
			tb.Fatalf("snapshot of %s: %v", name, err)
		}
		sb.WriteString(name + ": " + s + "\n")
	}
	return sb.String()
}

// SnapshotTenant returns the rows of one tenant, in every table that has a tenant_id and in the
// tenant table itself, as one JSON text.
func (a *API) SnapshotTenant(tb testing.TB, tenantID string) string {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var sb strings.Builder
	for _, name := range a.Tables(tb) {
		col := "tenant_id"
		if name == "tenant" {
			col = "id"
		}
		var has bool
		if err := a.Super(tb).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'pandokos' AND table_name = $1 AND column_name = $2)`, name, col).Scan(&has); err != nil {
			tb.Fatal(err)
		}
		if !has {
			continue
		}
		var s string
		if err := a.Super(tb).QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text, '[]') FROM pandokos.`+pgxIdent(name)+` t WHERE t.`+col+` = $1::uuid`, tenantID).Scan(&s); err != nil {
			tb.Fatalf("snapshot of %s: %v", name, err)
		}
		sb.WriteString(name + ": " + s + "\n")
	}
	return sb.String()
}

func pgxIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
