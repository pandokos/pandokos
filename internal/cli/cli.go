// Package cli implements the API commands of the pandokos binary: clients of the administration
// API that only call the generated adminapi.Client (API-03). It imports nothing that reaches the
// database, the handlers or the configuration.
package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/buildinfo"
	"github.com/pandokos/pandokos/internal/secret"
)

// usageError is a mistake of the caller: exit code 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return &usageError{fmt.Sprintf(format, args...)} }

type command struct {
	words     []string
	operation string
	run       func(a *app, args []string) error
}

// app is one run of a command.
type app struct {
	ctx            context.Context
	stdout, stderr io.Writer
	environ        []string

	server, keyFile, caFile, output string
	warnedBootstrap                 bool
	client                          *adminapi.Client
}

func (a *app) env(name string) string {
	v := ""
	for _, e := range a.environ {
		if n, val, ok := strings.Cut(e, "="); ok && n == name {
			v = val
		}
	}
	return v
}

// Run runs an API command. args start with the command words ("tenant", "list", ...). It returns
// the exit code: 0 success, 1 a problem of the API or an unreachable server, 2 a usage error.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	a := &app{ctx: ctx, stdout: stdout, stderr: stderr, environ: environ}
	err := a.dispatch(args)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	}
	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintf(stderr, "pandokos: %s\n", ue.msg)
		return 2
	}
	var pe *adminapi.ProblemError
	if errors.As(err, &pe) {
		fmt.Fprintf(stderr, "error: %s: %s (request %s)\n", pe.Problem.Code, pe.Problem.Detail, pe.Problem.RequestID)
		for _, f := range pe.Problem.Errors {
			fmt.Fprintf(stderr, "  %s: %s\n", f.Field, f.Message)
		}
		return 1
	}
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "adminapi: ")
	fmt.Fprintf(stderr, "error: %s\n", msg)
	return 1
}

func (a *app) dispatch(args []string) error {
	if len(args) == 0 {
		return usagef("a command is needed")
	}
	var matched *command
	for i := range commands {
		c := &commands[i]
		if len(args) >= len(c.words) && equalWords(args[:len(c.words)], c.words) {
			matched = c
			break
		}
	}
	if matched == nil {
		if args[0] == "whoami" {
			return usagef("whoami takes no sub-command")
		}
		return usagef("%s needs a sub-command: %s", args[0], strings.Join(subcommands(args[0]), ", "))
	}
	return matched.run(a, args[len(matched.words):])
}

func equalWords(a, b []string) bool {
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func subcommands(group string) []string {
	var out []string
	for _, c := range commands {
		if len(c.words) == 2 && c.words[0] == group {
			out = append(out, c.words[1])
		}
	}
	return out
}

// flags creates the flag set of a command with the connection flags.
func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.StringVar(&a.server, "server", a.env("PANDOKOS_CLI_SERVER"), "base URL of the administration API (PANDOKOS_CLI_SERVER)")
	fs.StringVar(&a.keyFile, "key-file", a.env("PANDOKOS_CLI_KEY_FILE"), "file with the key (PANDOKOS_CLI_KEY_FILE)")
	fs.StringVar(&a.caFile, "ca-file", a.env("PANDOKOS_CLI_CA_FILE"), "PEM file with the authorities to trust (PANDOKOS_CLI_CA_FILE)")
	fs.StringVar(&a.output, "o", "text", "output: text or json")
	return fs
}

// parse parses flags that may come before and after the positional arguments.
func parse(fs *flag.FlagSet, args []string, wantArgs int) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, &usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != wantArgs {
		return nil, usagef("%s takes %d argument(s), got %d", fs.Name(), wantArgs, len(pos))
	}
	return pos, nil
}

var reUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// connect builds the client from the connection flags.
func (a *app) connect() error {
	if a.output != "text" && a.output != "json" {
		return usagef("-o must be text or json")
	}
	if a.keyFile == "" {
		return usagef("the key file is required: use --key-file or PANDOKOS_CLI_KEY_FILE")
	}
	server := a.server
	if server == "" {
		server = "http://127.0.0.1:8081"
	}
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.Contains(server, "?") {
		return usagef("the server must be a URL with the scheme http or https and no path, query or user information")
	}
	key, err := secret.ReadFile(a.keyFile)
	if err != nil {
		return fmt.Errorf("the key file cannot be used: %w", err)
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if a.caFile != "" {
		pem, err := os.ReadFile(a.caFile) //nolint:gosec // the path is a flag of the operator
		if err != nil {
			return fmt.Errorf("cannot read the CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("the CA file contains no PEM certificate")
		}
		tlsCfg.RootCAs = pool
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		fmt.Fprintln(a.stderr, "warning: the key is sent without TLS")
	}
	a.client = &adminapi.Client{
		BaseURL:   strings.TrimSuffix(u.String(), "/"),
		HTTP:      &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: tlsCfg}},
		Key:       key.Reveal, // the one place where the key leaves its type: the Authorization header
		UserAgent: "pandokos-cli/" + buildinfo.Version,
		OnResponse: func(r *http.Response) {
			if strings.Contains(r.Header.Get("X-Pandokos-Warning"), "bootstrap-key-active") && !a.warnedBootstrap {
				a.warnedBootstrap = true
				fmt.Fprintln(a.stderr, "warning: you are using the bootstrap key; create a platform key and revoke this one")
			}
		},
	}
	return nil
}

// resolveTenant turns a tenant ID or name into an ID; an empty value is the tenant of the key.
func (a *app) resolveTenant(v string) (string, error) {
	switch {
	case v == "":
		me, err := a.client.GetMe(a.ctx, adminapi.GetMeInput{})
		if err != nil {
			return "", err
		}
		if me.Tenant == nil {
			return "", usagef("--tenant is required with a platform key")
		}
		return me.Tenant.ID, nil
	case reUUID.MatchString(v):
		return v, nil
	}
	l, err := a.client.ListTenants(a.ctx, adminapi.ListTenantsInput{Name: v})
	if err != nil {
		return "", err
	}
	for _, t := range l.Items {
		if t.Name == v {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("no tenant named %q", v)
}

func (a *app) resolveServiceAccount(tenantID, v string) (string, error) {
	if reUUID.MatchString(v) {
		return v, nil
	}
	l, err := a.client.ListServiceAccounts(a.ctx, adminapi.ListServiceAccountsInput{Tenant: tenantID, Name: v})
	if err != nil {
		return "", err
	}
	for _, s := range l.Items {
		if s.Name == v {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("no service account named %q", v)
}

// ---- output

type pair struct{ k, v string }

// fields returns the members of a JSON object in document order, scalars as text.
func fields(v any) []pair {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var out []pair
	for dec.More() {
		kt, _ := dec.Token()
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		out = append(out, pair{kt.(string), scalar(raw)})
	}
	return out
}

func scalar(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []any
	if json.Unmarshal(raw, &list) == nil {
		parts := make([]string, len(list))
		for i, e := range list {
			parts[i] = fmt.Sprint(e)
		}
		return strings.Join(parts, ",")
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		var parts []string
		for _, p := range fields(obj) {
			parts = append(parts, p.k+"="+p.v)
		}
		return strings.Join(parts, " ")
	}
	return string(raw)
}

func (a *app) printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "%s\n", b)
	return err
}

// printResource prints a resource as name: value lines, or as JSON.
func (a *app) printResource(v any) error {
	if a.output == "json" {
		return a.printJSON(v)
	}
	for _, p := range fields(v) {
		fmt.Fprintf(a.stdout, "%s: %s\n", p.k, p.v)
	}
	return nil
}

// printTable prints items as a table with a header.
func (a *app) printTable(items []any) {
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	if len(items) > 0 {
		var head []string
		for _, p := range fields(items[0]) {
			head = append(head, strings.ToUpper(p.k))
		}
		fmt.Fprintln(tw, strings.Join(head, "\t"))
	}
	for _, it := range items {
		var row []string
		for _, p := range fields(it) {
			row = append(row, p.v)
		}
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
}

// keyOut creates the file for a new key before the key exists, so that an existing file is an
// error before anything is issued.
type keyOut struct{ path string }

func openKeyOut(path string) (*keyOut, error) {
	if path == "" {
		return nil, nil //nolint:nilnil // no out-file was asked for
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is a flag of the operator
	if err != nil {
		return nil, fmt.Errorf("cannot create the key file: %w", err)
	}
	_ = f.Close()
	return &keyOut{path}, nil
}

func (k *keyOut) abandon() {
	if k != nil {
		_ = os.Remove(k.path)
	}
}

func (k *keyOut) write(key string) error {
	return os.WriteFile(k.path, []byte(key+"\n"), 0o600)
}

// printCreated prints what a create command returned: with -o json the whole response (the
// secret blanked when it went to an out-file), else the resource through text and then the key or
// a note that it was written to the out-file.
func (a *app) printCreated(text func() error, full any, secretText string, out *keyOut, blank func()) error {
	if out != nil {
		if err := out.write(secretText); err != nil {
			return err
		}
		blank()
	}
	if a.output == "json" {
		return a.printJSON(full)
	}
	if err := text(); err != nil {
		return err
	}
	if out != nil {
		fmt.Fprintf(a.stdout, "key written to %s\n", out.path)
	} else {
		fmt.Fprintf(a.stdout, "key:     %s\n", secretText)
	}
	return nil
}
