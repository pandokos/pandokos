// Package instance is a small process harness for the suites that drive a built binary by its path:
// the restore drill (test/drill) and the upgrade suite (test/upgrade). It builds this tree once per
// test binary, finds the previous release built by "make previous-binary", runs commands to
// completion, starts and stops a server process, runs the command line against it, and passes every
// output it collected to a set of canaries (planted secrets that no output may hold).
//
// It exists because the helpers of test/e2e are private to that package. It takes the binary's
// path, so it drives the previous release too. It touches no database or address of its own: the
// caller gives the environment.
package instance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testutil"
)

// Binary is a built pandokos executable. Canaries, when set, receives every output of every Run,
// Serve and CLI made through the value.
type Binary struct {
	Path, Version string
	Canaries      *Canaries
}

var built struct {
	once sync.Once
	b    Binary
	err  error
}

// TryBuild builds this tree's cmd/pandokos once per test binary and returns it. A TestMain calls it
// to fail early with a clear message.
func TryBuild() (Binary, error) {
	built.once.Do(func() {
		root, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
		if err != nil {
			built.err = fmt.Errorf("go list: %w", err)
			return
		}
		dir, err := os.MkdirTemp("", "pandokos-instance-")
		if err != nil {
			built.err = err
			return
		}
		path := filepath.Join(dir, "pandokos")
		cmd := exec.Command("go", "build", "-o", path, "github.com/pandokos/pandokos/cmd/pandokos")
		cmd.Dir = strings.TrimSpace(string(root))
		if out, err := cmd.CombinedOutput(); err != nil {
			built.err = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		built.b = Binary{Path: path, Version: "dev"}
	})
	return built.b, built.err
}

// Build is TryBuild that fails the test.
func Build(tb testing.TB) Binary {
	tb.Helper()
	b, err := TryBuild()
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// Previous returns the previous release built by "make previous-binary" (local/previous/pandokos).
// It fails, never skips, when the file is absent.
func Previous(tb testing.TB) Binary {
	tb.Helper()
	path := filepath.Join(testutil.RepoRoot(tb), "local", "previous", "pandokos")
	if _, err := os.Stat(path); err != nil {
		tb.Fatalf("the previous release is not built (%v): run make previous-binary", err)
	}
	b := Binary{Path: path}
	code, out, errs := b.Run(tb, nil, "version")
	if code != 0 {
		tb.Fatalf("the previous binary's version command exited %d: %s", code, errs)
	}
	if m := reVersion.FindStringSubmatch(out); m != nil {
		b.Version = m[1]
	}
	return b
}

var reVersion = regexp.MustCompile(`^pandokos (\S+) \(commit `)

// WithCanaries returns the binary with its outputs scanned by c.
func (b Binary) WithCanaries(c *Canaries) Binary {
	b.Canaries = c
	return b
}

// Run runs the binary to completion with exactly the environment env (PATH is added) and a 60 s
// deadline, and returns its exit code and outputs.
func (b Binary) Run(tb testing.TB, env []string, args ...string) (code int, stdout, stderr string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.Path, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		tb.Fatalf("pandokos %s did not exit within 60 s", firstWord(args))
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		tb.Fatal(err)
	}
	b.Canaries.Scan(tb, "output of pandokos "+firstWord(args), out.Bytes(), errb.Bytes())
	return code, out.String(), errb.String()
}

func firstWord(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// Process is a running "pandokos serve".
type Process struct {
	Bin              Binary
	Inference, Admin string // listen addresses, read from the log
	Scheme           string // "http" unless the caller sets "https" and Client
	Client           *http.Client
	CAFile           string // given to the command line as PANDOKOS_CLI_CA_FILE when set

	Stdout, Stderr testutil.LogBuffer
	cmd            *exec.Cmd
	exited         chan struct{}
	code           int
}

// Serve starts "pandokos serve" with listeners on 127.0.0.1:0 (the environment may override them)
// and exactly the environment env. The process is killed when the test ends if it still runs.
func (b Binary) Serve(tb testing.TB, env []string, args ...string) *Process {
	tb.Helper()
	p := &Process{Bin: b, Scheme: "http", Client: &http.Client{Timeout: 10 * time.Second}, exited: make(chan struct{})}
	full := append([]string{"PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0", "PANDOKOS_LISTENERS_ADMIN_ADDRESS=127.0.0.1:0"}, env...)
	p.cmd = exec.Command(b.Path, append([]string{"serve"}, args...)...)
	p.cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, full...)
	p.cmd.Stdout, p.cmd.Stderr = &p.Stdout, &p.Stderr
	if err := p.cmd.Start(); err != nil {
		tb.Fatal(err)
	}
	go func() {
		err := p.cmd.Wait()
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			p.code = ee.ExitCode()
		case err != nil:
			p.code = -1
		}
		close(p.exited)
	}()
	tb.Cleanup(func() {
		select {
		case <-p.exited:
		default:
			_ = p.cmd.Process.Kill()
			<-p.exited
		}
		p.scan(tb)
	})
	return p
}

func (p *Process) scan(tb testing.TB) {
	tb.Helper()
	p.Bin.Canaries.Scan(tb, "log of pandokos serve", []byte(p.Stdout.String()), []byte(p.Stderr.String()))
}

// Ready waits for the listening log lines and for /readyz of both listeners to answer 200.
// A complete line is read: a trailing fragment is never parsed.
func (p *Process) Ready(tb testing.TB) {
	tb.Helper()
	testutil.Eventually(tb, 30*time.Second, "the listening log lines", func() bool {
		for _, l := range completeLines(p.Stdout.String(), "listening") {
			addr, _ := l["address"].(string)
			switch l["listener"] {
			case "inference":
				p.Inference = addr
			case "admin":
				p.Admin = addr
			}
		}
		return p.Inference != "" && p.Admin != ""
	})
	for _, addr := range []string{p.Inference, p.Admin} {
		testutil.Eventually(tb, 30*time.Second, "readiness of "+addr, func() bool {
			resp, err := p.Client.Get(p.Scheme + "://" + addr + "/readyz")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		})
	}
}

// Exited is closed when the process has exited.
func (p *Process) Exited() <-chan struct{} { return p.exited }

// Code is the exit code; valid after Exited is closed.
func (p *Process) Code() int { return p.code }

// Stop sends SIGTERM, waits for the exit (30 s at most) and returns the exit code.
func (p *Process) Stop(tb testing.TB) (code int) {
	tb.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		select {
		case <-p.exited:
			return p.code
		default:
			tb.Fatal(err)
		}
	}
	select {
	case <-p.exited:
	case <-time.After(30 * time.Second):
		tb.Fatal("the process did not exit within 30 s of SIGTERM")
	}
	p.scan(tb)
	return p.code
}

// CLI runs an API command of the binary against the administration listener of the process with
// the key file of an administrator.
func (p *Process) CLI(tb testing.TB, keyFile string, args ...string) (int, string, string) {
	tb.Helper()
	env := []string{"PANDOKOS_CLI_SERVER=" + p.Scheme + "://" + p.Admin, "PANDOKOS_CLI_KEY_FILE=" + keyFile}
	if p.CAFile != "" {
		env = append(env, "PANDOKOS_CLI_CA_FILE="+p.CAFile)
	}
	return p.Bin.Run(tb, env, args...)
}

// completeLines decodes the complete JSON lines of text whose msg is msg.
func completeLines(text, msg string) []map[string]any {
	i := strings.LastIndexByte(text, '\n')
	if i < 0 {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(text[:i], "\n") {
		if m := decodeLine(line); m != nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// Installation is one installation as a process sees it: a binary, the environment that defines it
// (configuration, secret files), the canaries of its outputs and, once initialised, the key file of
// its first platform key.
type Installation struct {
	Bin         Binary
	Env         []string
	Canaries    *Canaries
	PlatformKey string // the key file written by Init
}

// NewInstallation returns an installation of the binary with the environment env; every output of
// its commands and processes is scanned by its Canaries.
func NewInstallation(bin Binary, env []string) *Installation {
	c := &Canaries{}
	return &Installation{Bin: bin.WithCanaries(c), Env: env, Canaries: c}
}

// Run runs a command with the installation's environment and the extra variables.
func (in *Installation) Run(tb testing.TB, extra []string, args ...string) (int, string, string) {
	tb.Helper()
	return in.Bin.Run(tb, append(append([]string(nil), in.Env...), extra...), args...)
}

// Init runs "pandokos init --key-out" and records the key file.
func (in *Installation) Init(tb testing.TB) string {
	tb.Helper()
	file := filepath.Join(tb.TempDir(), "platform.key")
	code, out, errs := in.Run(tb, nil, "init", "--key-out", file)
	if code != 0 {
		tb.Fatalf("init exited %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	in.PlatformKey = file
	return file
}

// Serve starts a server with the installation's environment and the extra variables.
func (in *Installation) Serve(tb testing.TB, extra ...string) *Process {
	tb.Helper()
	return in.Bin.Serve(tb, append(append([]string(nil), in.Env...), extra...))
}
