//go:build transit

package transit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// syncBuffer is a log buffer that is safe to read while the process writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lines returns the complete JSON lines with the message.
func (s *syncBuffer) lines(msg string) []map[string]any {
	text := s.String()
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	} else {
		return nil
	}
	var out []map[string]any
	for _, l := range strings.Split(text, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

var binaryPath struct {
	once sync.Once
	path string
	err  error
}

// buildBinary builds cmd/pandokos once for the tests of this package.
func buildBinary(t *testing.T) string {
	t.Helper()
	binaryPath.once.Do(func() {
		dir, err := os.MkdirTemp("", "pandokos-transit-")
		if err != nil {
			binaryPath.err = err
			return
		}
		binaryPath.path = filepath.Join(dir, "pandokos")
		build := exec.Command("go", "build", "-o", binaryPath.path, "github.com/pandokos/pandokos/cmd/pandokos")
		build.Dir = testutil.RepoRoot(t)
		if out, err := build.CombinedOutput(); err != nil {
			binaryPath.err = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if binaryPath.err != nil {
		t.Fatal(binaryPath.err)
	}
	return binaryPath.path
}

func freePort(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// server is a process of the binary.
type server struct {
	t         *testing.T
	cmd       *exec.Cmd
	stdout    *syncBuffer
	stderr    *syncBuffer
	exited    chan struct{}
	code      int
	inference string
	admin     string
	env       []string
}

// installation is a database, a platform key and the tenants made on it.
type installation struct {
	t        *testing.T
	d        *dbtest.Database
	up       *fakeprovider.Server
	bin      string
	platform string
	servers  []*server
}

func newInstallation(t *testing.T, emptyKEK bool) *installation {
	t.Helper()
	d := dbtest.NewMigrated(t)
	if emptyKEK {
		if _, err := d.Super(t).Exec(context.Background(), `DELETE FROM pandokos.kek_version`); err != nil {
			t.Fatal(err)
		}
	}
	return &installation{t: t, d: d, up: fakeprovider.New(t), bin: buildBinary(t)}
}

// serve starts the binary with extra environment variables and no key file unless one is given.
func (in *installation) serve(extra ...string) *server {
	t := in.t
	t.Helper()
	env := []string{"PATH=" + os.Getenv("PATH")}
	for _, kv := range in.d.Env(t) {
		if !strings.HasPrefix(kv, "PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE=") {
			env = append(env, kv)
		}
	}
	s := &server{t: t, stdout: &syncBuffer{}, stderr: &syncBuffer{}, exited: make(chan struct{}), inference: freePort(t), admin: freePort(t)}
	env = append(env, "PANDOKOS_LISTENERS_INFERENCE_ADDRESS="+s.inference, "PANDOKOS_LISTENERS_ADMIN_ADDRESS="+s.admin, "PANDOKOS_LOG_LEVEL=debug")
	s.env = append(env, extra...)
	s.cmd = exec.Command(in.bin, "serve")
	s.cmd.Env = s.env
	s.cmd.Stdout, s.cmd.Stderr = s.stdout, s.stderr
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := s.cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			s.code = ee.ExitCode()
		}
		close(s.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-s.exited:
		default:
			_ = s.cmd.Process.Kill()
			<-s.exited
		}
	})
	in.servers = append(in.servers, s)
	return s
}

func (s *server) ready() {
	s.t.Helper()
	for _, addr := range []string{s.inference, s.admin} {
		testutil.Eventually(s.t, 30*time.Second, "readiness of "+addr, func() bool {
			resp, err := http.Get("http://" + addr + "/readyz")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == 200
		})
	}
}

func (s *server) stop() int {
	s.t.Helper()
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.exited:
	case <-time.After(15 * time.Second):
		s.t.Fatal("the process did not exit within 15 s of SIGTERM")
	}
	return s.code
}

// cli runs a client command against the administration listener of the server.
func (in *installation) cli(s *server, keyFile string, args ...string) string {
	t := in.t
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, in.bin, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PANDOKOS_CLI_SERVER=http://" + s.admin, "PANDOKOS_CLI_KEY_FILE=" + keyFile}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("pandokos %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out.String(), errb.String())
	}
	return out.String()
}

var reID = regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`)

// tenant is a tenant with an inference key.
type tenant struct{ id, admin, key string }

// bootstrap makes the platform key of the installation (init) once.
func (in *installation) bootstrap(s *server) {
	t := in.t
	t.Helper()
	in.platform = filepath.Join(t.TempDir(), "platform.key")
	cmd := exec.Command(in.bin, "init", "--key-out", in.platform)
	cmd.Env = s.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	in.cli(s, in.platform, "endpoint", "update", "--base-url", in.up.URL(), "--private", "openai", "default")
	in.cli(s, in.platform, "model", "create", "--availability", "all", "openai/"+fakeprovider.ModelBasic)
	in.up.Accept(fixtures.CanaryCredential)
}

// newTenant makes a tenant with a credential and an inference key.
func (in *installation) newTenant(s *server, name string) tenant {
	t := in.t
	t.Helper()
	adminFile := filepath.Join(t.TempDir(), name+".key")
	out := in.cli(s, in.platform, "tenant", "create", "--admin-key-out", adminFile, name)
	m := reID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no tenant ID in %s", out)
	}
	id := m[1]
	in.cli(s, adminFile, "service-account", "create", "--tenant", id, "--role", "inference", "caller")
	var sas struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(in.cli(s, adminFile, "service-account", "list", "--tenant", id, "-o", "json")), &sas); err != nil || len(sas.Items) == 0 {
		t.Fatalf("no service account: %v", err)
	}
	keyFile := filepath.Join(t.TempDir(), name+".inference.key")
	in.cli(s, adminFile, "key", "create", "--tenant", id, "--service-account", sas.Items[0].ID, "--key-out", keyFile)
	secretFile := testutil.WriteSecret(t, "provider-secret", fixtures.CanaryCredential)
	in.cli(s, adminFile, "credential", "create", "--provider", "openai", "--secret-file", secretFile, name+"-credential")
	b, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return tenant{id: id, admin: adminFile, key: strings.TrimSpace(string(b))}
}

// call makes one chat call and returns the status, -1 when nobody answers.
func (s *server) call(key string) int {
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, fakeprovider.ModelBasic)
	req, err := http.NewRequestWithContext(context.Background(), "POST", "http://"+s.inference+"/openai/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return -1
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (in *installation) kekStates(s *server) map[string]string {
	t := in.t
	t.Helper()
	var list struct {
		Items []struct{ ID, State string } `json:"items"`
	}
	if err := json.Unmarshal([]byte(in.cli(s, in.platform, "kek-version", "list", "-o", "json")), &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, it := range list.Items {
		out[it.ID] = it.State
	}
	return out
}

// transitEnv is the configuration of the binary for a service reached through an address.
func transitEnv(address, mount, key, method, tokenFile, appRoleMount, roleFile, secretFile string, extra ...string) []string {
	env := []string{"PANDOKOS_VAULT_KEK_PROVIDER=transit", "PANDOKOS_VAULT_KEK_TRANSIT_ADDRESS=" + address, "PANDOKOS_VAULT_KEK_TRANSIT_MOUNT=" + mount,
		"PANDOKOS_VAULT_KEK_TRANSIT_KEY_NAME=" + key, "PANDOKOS_VAULT_KEK_TRANSIT_AUTH_METHOD=" + method, "PANDOKOS_VAULT_KEK_TRANSIT_REQUEST_TIMEOUT=1s"}
	if method == "token" {
		env = append(env, "PANDOKOS_VAULT_KEK_TRANSIT_AUTH_TOKEN_FILE="+tokenFile)
	} else {
		env = append(env, "PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_MOUNT="+appRoleMount, "PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_ROLE_ID_FILE="+roleFile,
			"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_SECRET_ID_FILE="+secretFile)
	}
	return append(env, extra...)
}

// TestBinaryExit is T-1094: the three exit criteria of the sprint with the built binary against the
// real service, through the relay of the test.
func TestBinaryExit(t *testing.T) {
	target := newRealTarget(t)
	u, err := url.Parse(target.Address())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	secrets := []string{target.root} // the secrets of the service: no output of the binary holds one

	newService := func() (relay *Relay, env func(extra ...string) []string, mount, key string) {
		mount, key = NewKey(t, target)
		am, roleID, secretID := AppRole(t, target, mount, key, "", "")
		secrets = append(secrets, roleID, secretID)
		roleFile := writeSecret(t, dir, "role-"+mount, roleID)
		secretFile := writeSecret(t, dir, "secret-"+mount, secretID)
		relay = Listen(t, u.Host)
		address := "http://" + relay.Addr()
		return relay, func(extra ...string) []string {
			return transitEnv(address, mount, key, "approle", "", am, roleFile, secretFile, extra...)
		}, mount, key
	}

	t.Run("a fresh install with no key file", func(t *testing.T) {
		_, env, _, _ := newService()
		in := newInstallation(t, true)
		s := in.serve(env()...)
		s.ready()
		if l := s.stdout.lines("vault ready"); len(l) != 1 || l[0]["kek_provider"] != "transit" || l[0]["kek_activation"] != "activated" {
			t.Fatalf("vault ready: %v", l)
		}
		in.bootstrap(s)
		a := in.newTenant(s, "fresh")
		if status := s.call(a.key); status != 200 {
			t.Errorf("a call of the first tenant: %d", status)
		}
		if code := s.stop(); code != 0 {
			t.Errorf("exit code %d", code)
		}
		assertNoSecrets(t, in, secrets)
	})

	t.Run("a move from a key file to the service and then a start without a key file", func(t *testing.T) {
		_, env, _, _ := newService()
		d := dbtest.NewMigrated(t)
		keys := vaulttest.Keys(t, 1)
		d.UseKEK(t, keys[0].Version)
		in := &installation{t: t, d: d, up: fakeprovider.New(t), bin: buildBinary(t)}
		first := in.serve("PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE=" + keys[0].File)
		first.ready()
		in.bootstrap(first)
		a, b := in.newTenant(first, "move-a"), in.newTenant(first, "move-b")
		if code := first.stop(); code != 0 {
			t.Fatalf("exit code %d", code)
		}
		second := in.serve(env("PANDOKOS_VAULT_KEK_LOCAL_UNWRAP_KEY_FILES=" + keys[0].File)...)
		second.ready()
		deadline := time.Now().Add(90 * time.Second)
		for in.kekStates(second)[keys[0].Version] != "retired" {
			for _, x := range []tenant{a, b} {
				if status := second.call(x.key); status != 200 {
					t.Fatalf("a call failed while the worker re-wrapped: %d", status)
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("the key file's version is not retired: %v", in.kekStates(second))
			}
			time.Sleep(300 * time.Millisecond)
		}
		if code := second.stop(); code != 0 {
			t.Fatalf("exit code %d", code)
		}
		last := in.serve(env()...)
		last.ready()
		for _, x := range []tenant{a, b} {
			if status := last.call(x.key); status != 200 {
				t.Errorf("a call without a key file: %d", status)
			}
		}
		if code := last.stop(); code != 0 {
			t.Errorf("exit code %d", code)
		}
		assertNoSecrets(t, in, secrets)
	})

	t.Run("an outage and a restart during it and the recovery", func(t *testing.T) {
		relay, env, _, _ := newService()
		in := newInstallation(t, true)
		s := in.serve(env("PANDOKOS_CACHE_CREDENTIAL_TTL=1s", "PANDOKOS_CACHE_DATA_KEY_TTL=2s")...)
		s.ready()
		in.bootstrap(s)
		a := in.newTenant(s, "outage")
		if status := s.call(a.key); status != 200 {
			t.Fatalf("a call before the outage: %d", status)
		}
		relay.Refuse()
		testutil.Eventually(t, 15*time.Second, "503 once the caches lapse", func() bool { return s.call(a.key) == 503 })
		if code := s.stop(); code != 0 {
			t.Fatalf("exit code %d", code)
		}
		// A restart during the outage listens on nothing and does not exit.
		again := in.serve(env()...)
		testutil.Eventually(t, 20*time.Second, "the line key provider unavailable", func() bool { return len(again.stdout.lines("key provider unavailable")) > 0 })
		select {
		case <-again.exited:
			t.Fatalf("the process exited during the outage: %d", again.code)
		case <-time.After(2 * time.Second):
		}
		for _, addr := range []string{again.inference, again.admin} {
			if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
				_ = c.Close()
				t.Errorf("%s accepts connections during the outage", addr)
			}
		}
		relay.Open()
		again.ready()
		if status := again.call(a.key); status != 200 {
			t.Errorf("a call after the recovery: %d", status)
		}
		if len(again.stdout.lines("key provider recovered")) == 0 {
			t.Errorf("no line key provider recovered")
		}
		if code := again.stop(); code != 0 {
			t.Errorf("exit code %d", code)
		}
		assertNoSecrets(t, in, secrets)
	})
}

// assertNoSecrets searches the output of every process of the installation for the secrets of the service.
func assertNoSecrets(t *testing.T, in *installation, secrets []string) {
	t.Helper()
	for i, s := range in.servers {
		testutil.AssertAbsent(t, fmt.Sprintf("the stdout of process %d", i), s.stdout.String(), secrets...)
		testutil.AssertAbsent(t, fmt.Sprintf("the stderr of process %d", i), s.stderr.String(), secrets...)
	}
}
