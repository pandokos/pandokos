package vaulttest

import (
	"context"
	"crypto/x509"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/internal/vault/transittest"
)

// TransitSetup describes what Transit makes in the double. An empty value takes the default.
type TransitSetup struct {
	Namespace    string // the namespace of the mount and the key; "" is the root
	Mount        string // default "transit"
	Key          string // default "pandokos"
	KeyOptions   transittest.KeyOptions
	Method       string        // "token" (default) or "approle"
	AppRoleMount string        // default "approle"
	TTL, MaxTTL  time.Duration // the lifetime of the token, or of the tokens of the role
	Renewable    bool          // a token only: an AppRole's tokens are always renewable
}

// TransitFiles are the files and the names Transit made. The files have mode 0600 and live in the
// temporary directory of the test; the values are what they hold.
type TransitFiles struct {
	Namespace, Mount, Key string
	Method                string
	AppRoleMount          string
	TokenFile             string // method token
	RoleIDFile            string // method approle
	SecretIDFile          string
	Token                 string
	RoleID, SecretID      string
	CAFile                string // set by the test when the double serves TLS; read by TransitProvider
}

// Transit makes, in s, the mount and the key and a token or an AppRole with the least-privilege
// paths, writes the files and returns them.
func Transit(tb testing.TB, s *transittest.Server, o TransitSetup) TransitFiles {
	tb.Helper()
	if o.Mount == "" {
		o.Mount = "transit"
	}
	if o.Key == "" {
		o.Key = "pandokos"
	}
	if o.Method == "" {
		o.Method = "token"
	}
	if o.AppRoleMount == "" {
		o.AppRoleMount = "approle"
	}
	sc := s.Namespace(o.Namespace)
	sc.CreateKey(o.Mount, o.Key, o.KeyOptions)
	f := TransitFiles{Namespace: o.Namespace, Mount: o.Mount, Key: o.Key, Method: o.Method, AppRoleMount: o.AppRoleMount}
	dir := tb.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content+"\n"), 0o600); err != nil {
			tb.Fatal(err)
		}
		return p
	}
	switch o.Method {
	case "token":
		f.Token = sc.Token(transittest.TokenOptions{TTL: o.TTL, MaxTTL: o.MaxTTL, Renewable: o.Renewable, Mount: o.Mount, Key: o.Key})
		f.TokenFile = write("token", f.Token)
	case "approle":
		f.RoleID, f.SecretID = sc.AppRole(o.AppRoleMount, transittest.RoleOptions{TokenTTL: o.TTL, TokenMaxTTL: o.MaxTTL, Mount: o.Mount, Key: o.Key})
		f.RoleIDFile = write("role_id", f.RoleID)
		f.SecretIDFile = write("secret_id", f.SecretID)
	default:
		tb.Fatalf("vaulttest: unknown method %q", o.Method)
	}
	return f
}

// TransitProvider returns a vault.TransitProvider on the files of Transit, started. It reaches
// the double through the transport of an egress guard (the private class). The options of
// vault.TransitOptions can be changed with adjust before it is built.
func TransitProvider(tb testing.TB, s *transittest.Server, f TransitFiles, adjust ...func(*vault.TransitOptions)) *vault.TransitProvider {
	tb.Helper()
	g, err := egress.New(egress.Options{ConnectTimeout: 2 * time.Second})
	if err != nil {
		tb.Fatal(err)
	}
	var roots *x509.CertPool
	if f.CAFile != "" {
		pem, err := os.ReadFile(f.CAFile)
		if err != nil {
			tb.Fatal(err)
		}
		roots = x509.NewCertPool()
		roots.AppendCertsFromPEM(pem)
	}
	u, err := url.Parse(s.URL())
	if err != nil {
		tb.Fatal(err)
	}
	o := vault.TransitOptions{
		Transport: g.KeyService(roots), Address: u, Namespace: f.Namespace, Mount: f.Mount, KeyName: f.Key,
		AuthMethod: f.Method, TokenFile: f.TokenFile, AppRoleMount: f.AppRoleMount,
		RoleIDFile: f.RoleIDFile, SecretIDFile: f.SecretIDFile,
		RequestTimeout: 2 * time.Second, MaxConcurrent: 32, UserAgent: "pandokos/test",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, a := range adjust {
		a(&o)
	}
	p, err := vault.NewTransitProvider(o)
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Start(ctx); err != nil {
		tb.Fatal(err)
	}
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(runCtx) }()
	tb.Cleanup(func() { stop(); <-done; g.CloseIdleConnections() })
	return p
}
