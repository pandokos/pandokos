//go:build browser

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

var binary string

func TestMain(m *testing.M) {
	os.Exit(func() int {
		dir, err := os.MkdirTemp("", "pandokos-browser-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(dir)
		rootOut, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "go list:", err)
			return 1
		}
		binary = filepath.Join(dir, "pandokos")
		build := exec.Command("go", "build", "-o", binary, "github.com/pandokos/pandokos/cmd/pandokos")
		build.Dir = strings.TrimSpace(string(rootOut))
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "go build:", err)
			return 1
		}
		return m.Run()
	}())
}

// chrome returns the browser to run. The tests behind the tag browser never skip: without Chrome they fail.
func chrome(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("PANDOKOS_TEST_CHROME"); p != "" {
		return p
	}
	p, err := exec.LookPath("google-chrome")
	if err != nil {
		t.Fatalf("google-chrome is not installed and PANDOKOS_TEST_CHROME is not set: the tests behind the tag browser never skip (make test-browser)")
	}
	return p
}

// world is the binary, a reverse proxy that serves the page and the API on one origin, and a second
// server on another site that serves the page of an attacker.
type world struct {
	d        *dbtest.Database
	origin   string // the origin of the page and of the API: http://127.0.0.1:port
	attacker string // another site: http://localhost:port
	root     string // the password of the administrator
}

func run(t *testing.T, env []string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

func startWorld(t *testing.T) *world {
	t.Helper()
	w := &world{d: dbtest.NewMigrated(t)}
	env := w.d.Env(t)
	out, errs, err := run(t, env, "init", "--admin-username", "root.admin")
	if err != nil {
		t.Fatalf("init: %v\n%s\n%s", err, out, errs)
	}
	m := regexp.MustCompile(`(?m)^password: ([a-z2-7]{24})$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no password in the output of init:\n%s", out)
	}
	// The password has to be changed before the account can do anything: the page signs in with the new one.
	w.root = m[1]

	// The addresses: the proxy (the origin of the page) first, because auth.public_origin names it.
	proxyLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	adminLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	adminAddr := adminLn.Addr().String()
	adminLn.Close()
	w.origin = "http://" + proxyLn.Addr().String()

	e := append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	e = append(e, "PANDOKOS_LISTENERS_ADMIN_ADDRESS="+adminAddr, "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0", "PANDOKOS_AUTH_PUBLIC_ORIGIN="+w.origin)
	cmd := exec.Command(binary, "serve")
	cmd.Env = e
	logs := &testutil.LogBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	testutil.Eventually(t, 15*time.Second, "the administration listener", func() bool {
		resp, err := http.Get("http://" + adminAddr + "/readyz") //nolint:noctx // a loopback probe
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})

	target, _ := url.Parse("http://" + adminAddr)
	proxy := httputil.NewSingleHostReverseProxy(target) // the Host header of the browser is kept: the origin is the proxy's
	index, err := os.ReadFile("page/index.html")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = rw.Write(index)
	})
	mux.Handle("/v1/", proxy)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(proxyLn) }()
	t.Cleanup(func() { _ = srv.Close() })

	attackerPage, err := os.ReadFile("page/attacker.html")
	if err != nil {
		t.Fatal(err)
	}
	// The origin of the API is written in the page by the server, not passed in its address.
	attackerPage = []byte(strings.Replace(string(attackerPage), "{{API}}", html.EscapeString(w.origin), 1))
	attLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(attLn.Addr().String())
	w.attacker = "http://localhost:" + port
	att := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = rw.Write(attackerPage)
	})}
	go func() { _ = att.Serve(attLn) }()
	t.Cleanup(func() { _ = att.Close() })

	// The first password is changed with the API, not in a page: a page is not what is under test here.
	w.changePassword(t, w.root, &w.root)
	return w
}

// changePassword signs in with the generated password, sets a new one and stores it in *into.
func (w *world) changePassword(t *testing.T, current string, into *string) {
	t.Helper()
	next := dbtest.NewPassword(t)
	steps := []struct {
		path string
		body map[string]any
	}{
		{"/v1/auth/sign-in", map[string]any{"username": "root.admin", "password": current}},
		{"/v1/auth/password", map[string]any{"current_password": current, "new_password": next}},
	}
	jar := newJar()
	for _, s := range steps {
		status, body := jar.post(t, w.origin+s.path, w.origin, s.body)
		if status != 200 {
			t.Fatalf("%s: %d %s", s.path, status, body)
		}
	}
	*into = next
}

// A cookie jar that keeps Secure cookies of a loopback origin, as Chrome does.
type jar struct{ cookies map[string]string }

func newJar() *jar { return &jar{cookies: map[string]string{}} }

func (j *jar) post(t *testing.T, target, origin string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", target, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pandokos-Csrf", "1")
	req.Header.Set("Origin", origin)
	for n, v := range j.cookies {
		req.AddCookie(&http.Cookie{Name: n, Value: v})
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		j.cookies[c.Name] = c.Value
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(buf)
}

// dump runs Chrome headless on the address and returns the JSON that the page wrote to its result element.
func dump(t *testing.T, profile, address string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome(t), "--headless", "--disable-gpu", "--no-sandbox", "--user-data-dir="+profile,
		"--virtual-time-budget=10000", "--dump-dom", address)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("chrome: %v", err)
	}
	m := regexp.MustCompile(`(?s)<pre id="result">(.*?)</pre>`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("no result in the DOM of %s:\n%s", address, out)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &res); err != nil {
		t.Fatalf("the result of the page %q: %v", m[1], err)
	}
	return res
}

func status(res map[string]any, name string) float64 {
	m, _ := res[name].(map[string]any)
	s, _ := m["status"].(float64)
	return s
}

// TestMinimalPage is T-844: the page on the origin of the listener signs in with fetch, the browser keeps
// the cookie, the script cannot read it, getMe and a POST with the header succeed, sign-out ends it.
func TestMinimalPage(t *testing.T) {
	w := startWorld(t)
	profile := t.TempDir()
	res := dump(t, profile, w.origin+"/#username=root.admin&password="+url.QueryEscape(w.root)+"&create=page.created")
	if status(res, "signIn") != 200 {
		t.Fatalf("signIn: %v", res["signIn"])
	}
	if res["cookieVisible"] != false {
		t.Error("the script reads the session cookie")
	}
	me, _ := res["me"].(map[string]any)
	body, _ := me["body"].(map[string]any)
	if status(res, "me") != 200 || body["principal_type"] != "user" {
		t.Errorf("getMe: %v", res["me"])
	}
	if status(res, "create") != 201 {
		t.Errorf("a POST with the header: %v", res["create"])
	}
	if s := status(res, "noHeader"); s != 403 {
		t.Errorf("a POST without the header: %v", res["noHeader"])
	}
	if status(res, "signOut") != 200 || status(res, "meAfter") != 401 {
		t.Errorf("sign-out: %v then %v", res["signOut"], res["meAfter"])
	}
	var n int
	if err := w.d.Super(t).QueryRow(context.Background(), "SELECT count(*) FROM pandokos.local_account WHERE username IN ('page.created', 'page.noheader')").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d accounts made by the page, want exactly the one with the header (%v)", n, err)
	}
}

// TestCrossSite is T-845: a page of another site, with the victim signed in, changes nothing.
func TestCrossSite(t *testing.T) {
	w := startWorld(t)
	profile := t.TempDir() // the victim's browser: the same profile for both pages
	// The victim signs in on the page of the origin and stays signed in (signout=0).
	login := dump(t, profile, w.origin+"/#username=root.admin&password="+url.QueryEscape(w.root)+"&create=victim.session&signout=0")
	if status(login, "signIn") != 200 {
		t.Fatalf("the victim could not sign in: %v", login["signIn"])
	}
	users := w.d.Count(t, "SELECT count(*) FROM pandokos.app_user")
	sessions := w.d.Count(t, "SELECT count(*) FROM pandokos.user_session")

	res := dump(t, profile, w.attacker+"/")
	for _, name := range []string{"json", "simple", "read"} {
		r, _ := res[name].(map[string]any)
		// No answer a script of another site can read: an error, or an opaque response.
		if s, _ := r["status"].(float64); s == 200 || s == 201 {
			t.Errorf("%s: the attacker's script got %v", name, r)
		}
	}
	time.Sleep(time.Second) // the form posts into a hidden frame
	if n := w.d.Count(t, "SELECT count(*) FROM pandokos.app_user"); n != users {
		t.Errorf("%d users were created by the other site", n-users)
	}
	if n := w.d.Count(t, "SELECT count(*) FROM pandokos.local_account WHERE username LIKE 'attacker.%'"); n != 0 {
		t.Errorf("%d accounts of the attacker", n)
	}
	if n := w.d.Count(t, "SELECT count(*) FROM pandokos.user_session"); n < sessions-1 {
		t.Errorf("sessions went from %d to %d", sessions, n)
	}
	// The listener sent no CORS header: nothing says that another origin may read an answer.
	req, _ := http.NewRequest("OPTIONS", w.origin+"/v1/users", nil)
	req.Header.Set("Origin", w.attacker)
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for h := range resp.Header {
		if strings.HasPrefix(strings.ToLower(h), "access-control-") {
			t.Errorf("the listener answers a preflight with %s", h)
		}
	}
}
