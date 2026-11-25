//go:build container

// Package container tests a built image with the docker CLI.
package container

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestImage(t *testing.T) {
	image := os.Getenv("PANDOKOS_TEST_IMAGE")
	if image == "" {
		t.Skip("PANDOKOS_TEST_IMAGE is not set: build the image and set it to its name")
	}

	if got := docker(t, "image", "inspect", "-f", "{{.Config.User}}|{{json .Config.Entrypoint}}|{{json .Config.Cmd}}", image); got != `65532:65532|["/pandokos"]|["serve"]` {
		t.Fatalf("image configuration: %s", got)
	}

	// The database: one of the test database, with its four users. The container uses host
	// networking, so the address is the one the tests use.
	d := dbtest.New(t)
	up := fakeprovider.New(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The secret of the tenant's credential, read by the CLI inside the container.
	up.Accept(fixtures.CanaryCredential)
	for name, v := range map[string]string{"provider_secret": fixtures.CanaryCredential} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dbEnv := []string{
		"-e", "PANDOKOS_DATABASE_ADDRESS=" + net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		"-e", "PANDOKOS_DATABASE_NAME=" + d.Name,
		"-e", "PANDOKOS_DATABASE_TLS_MODE=disable",
	}
	for _, role := range []string{"owner", "gateway", "admin", "worker"} {
		pw := d.ConnOptions(role).Password.Reveal()
		if err := os.WriteFile(filepath.Join(dir, "db_"+role), []byte(pw+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dbEnv = append(dbEnv,
			"-e", "PANDOKOS_DATABASE_"+strings.ToUpper(role)+"_USER="+d.User(role),
			"-e", "PANDOKOS_DATABASE_"+strings.ToUpper(role)+"_PASSWORD_FILE=/run/secrets/db_"+role,
		)
	}
	// The key-encryption key: a file of mode 0444, so that the user of the image can read it
	// through the read-only mount; the warning about the mode is expected and asserted.
	kekBytes, err := os.ReadFile(testutil.WriteKEK(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pandokos_kek"), kekBytes, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "pandokos_kek"), 0o444); err != nil {
		t.Fatal(err)
	}
	hardened := []string{"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--network", "host", "-v", dir + ":/run/secrets:ro"}

	// The image creates the schema with its migrate command.
	migrateArgs := append(append([]string{"run", "--rm"}, hardened...), dbEnv...)
	if out := docker(t, append(migrateArgs, image, "migrate")...); !strings.Contains(out, "schema is at version 17") {
		t.Fatalf("migrate output: %q", out)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()

	// init issues the bootstrap key; the image's own CLI does the rest through docker exec.
	initOut := docker(t, append(append(append([]string{"run", "--rm"}, hardened...), dbEnv...), image, "init")...)
	reKey := regexp.MustCompile(`(?m)^key: +(pdk_p[a-z2-7]{16}_[a-z2-7]{59})$`)
	m := reKey.FindStringSubmatch(initOut)
	if m == nil {
		t.Fatalf("init output has no key line: %q", initOut)
	}
	if err := os.WriteFile(filepath.Join(dir, "platform.key"), []byte(m[1]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	adminAddr := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().String()
	}()

	serveArgs := append(append([]string{"run", "-d"}, hardened...), dbEnv...)
	serveArgs = append(serveArgs,
		"-e", "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:"+port,
		"-e", "PANDOKOS_LISTENERS_ADMIN_ADDRESS="+adminAddr,
		"-e", "PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE=/run/secrets/pandokos_kek",
		image)
	id := docker(t, serveArgs...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	base := "http://127.0.0.1:" + port
	testutil.Eventually(t, 20*time.Second, "readiness of the container", func() bool {
		resp, err := http.Get(base + "/readyz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})

	// A tenant, a service account and a key, made with the CLI of the image.
	cli := func(keyFile string, args ...string) string {
		return docker(t, append([]string{"exec", "-e", "PANDOKOS_CLI_SERVER=http://" + adminAddr, "-e", "PANDOKOS_CLI_KEY_FILE=/run/secrets/" + keyFile, id, "/pandokos"}, args...)...)
	}
	reTenantKey := regexp.MustCompile(`(?m)^key: +(pdk_t[a-z2-7]{16}_[a-z2-7]{59})$`)
	out := cli("platform.key", "tenant", "create", "acme")
	tm := reTenantKey.FindStringSubmatch(out)
	tid := regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`).FindStringSubmatch(out)
	if tm == nil || tid == nil {
		t.Fatalf("tenant create output: %q", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin.key"), []byte(tm[1]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli("admin.key", "service-account", "create", "--tenant", tid[1], "--role", "inference", "caller")
	out = cli("admin.key", "key", "create", "--tenant", tid[1], "--service-account", "caller")
	km := reTenantKey.FindStringSubmatch(out)
	if km == nil {
		t.Fatalf("key create output: %q", out)
	}
	inferenceKey := km[1]

	// The catalogue route: the default endpoint of openai is the fake provider, as seen from the
	// container (host networking), marked private; the model exists; the tenant has a credential.
	cli("platform.key", "endpoint", "update", "--base-url", up.URL(), "--private", "openai", "default")
	cli("platform.key", "model", "create", "--availability", "all", "openai/"+fakeprovider.ModelBasic)
	cli("admin.key", "credential", "create", "--provider", "openai", "--secret-file", "/run/secrets/provider_secret", "container-credential")

	call := func() (int, string, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/openai/v1/chat/completions",
			strings.NewReader(`{"model":"fake-basic","stream":true,"messages":[{"role":"user","content":"`+fixtures.CanaryPrompt+`"}]}`))
		req.Header.Set("Authorization", "Bearer "+inferenceKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(bufio.NewReader(resp.Body))
		return resp.StatusCode, resp.Header.Get("X-Pandokos-Error"), string(body)
	}
	// The server learns of the endpoint change through a notification.
	var status int
	var code, body string
	testutil.Eventually(t, 20*time.Second, "the streaming call through the catalogue route", func() bool {
		status, code, body = call()
		return status == 200
	})
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("streaming call: %d %q", status, body)
	}

	// T-72: the streamed call left one usage row for the tenant, read from the provider.
	super := d.Super(t)
	testutil.Eventually(t, 20*time.Second, "the usage row of the streamed call", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var n int
		err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.usage_record WHERE tenant_id = $1::uuid AND outcome = 'ok' AND input_source = 'provider'`, tid[1]).Scan(&n)
		return err == nil && n == 1
	})

	// The same fake, now through a public endpoint: the admin API refuses an http URL or a
	// loopback literal there, so the endpoint is https://localhost:<port>. It passes the
	// validation, and the guard refuses the resolved loopback address when connecting, before
	// any TLS handshake.
	upURL, err := url.Parse(up.URL())
	if err != nil {
		t.Fatal(err)
	}
	cli("platform.key", "endpoint", "update", "--base-url", "https://localhost:"+upURL.Port(), "--private=false", "openai", "default")
	testutil.Eventually(t, 20*time.Second, "the refusal of a private address on a public endpoint", func() bool {
		status, code, body = call()
		return status != 200
	})
	if status != 502 || code != "upstream_unreachable" {
		t.Fatalf("call to a private address through a public endpoint: %d %q %q", status, code, body)
	}

	docker(t, "stop", "-t", "10", id)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", id); code != "0" {
		t.Errorf("exit code %s", code)
	}
	logs := docker(t, "logs", id)
	if !strings.Contains(logs, "egress blocked") {
		t.Errorf("the container did not log the refusal of the private address:\n%s", logs)
	}
	// The key was read, the version is announced, and the mode of the file is warned about (T-362).
	if !strings.Contains(logs, `"msg":"vault ready"`) || !strings.Contains(logs, `"kek_provider":"local"`) {
		t.Errorf("the container did not log `vault ready`:\n%s", logs)
	}
	// T-412: the image imports the embedded snapshot at its first start; no network is needed.
	if !regexp.MustCompile(`"msg":"prices imported"[^\n]*"source":"embedded"|"source":"embedded"[^\n]*"msg":"prices imported"`).MatchString(logs) {
		t.Errorf("the container did not log `prices imported` for the embedded snapshot:\n%s", logs)
	}
	if strings.Contains(logs, `"msg":"price import failed"`) {
		t.Errorf("the start-up price import failed in the container:\n%s", logs)
	}
	if !strings.Contains(logs, `"msg":"configuration warning"`) || !strings.Contains(logs, `"key":"vault.kek.local.key_file"`) {
		t.Errorf("no warning about the mode of the key file in the container logs:\n%s", logs)
	}
	line := strings.TrimSpace(string(kekBytes))
	testutil.AssertAbsent(t, "container logs", logs, testutil.SecretForms(line)...)
	if raw, err := base64.StdEncoding.DecodeString(line); err == nil {
		testutil.AssertAbsent(t, "container logs", logs, testutil.SecretForms(string(raw))...)
	}
	testutil.AssertNoCanary(t, "container logs", logs)
	testutil.AssertAbsent(t, "container logs", logs, d.Passwords()...)
	for _, k := range []string{m[1], tm[1], inferenceKey} {
		testutil.AssertAbsent(t, "container logs", logs, testutil.KeyForms(k)...)
	}

	if out := docker(t, "run", "--rm", image, "version"); !strings.HasPrefix(out, "pandokos ") {
		t.Errorf("version output: %q", out)
	}
}

// previousPin reads test/upgrade/PREVIOUS: the version, commit and image (by digest) of the release
// before the one in development.
func previousPin(t *testing.T) (version, commit, image string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "test", "upgrade", "PREVIOUS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "version":
			version = v
		case "commit":
			commit = v
		case "image":
			image = v
		}
	}
	if version == "" || commit == "" || !regexp.MustCompile(`^[^@]+@sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatalf("test/upgrade/PREVIOUS is not version, commit and image by digest: %q", b)
	}
	return version, commit, image
}

// TestUpgradeFromPreviousImage is T-1134: the artefact that operators run. The previous release's
// image, pulled by the digest of test/upgrade/PREVIOUS, creates the schema, serves a tenant with a
// credential and a call; the image of this tree then migrates the same database (one migration) and
// serves the same call with the same key; and the digest of the image that ran is the pinned one.
func TestUpgradeFromPreviousImage(t *testing.T) {
	image := os.Getenv("PANDOKOS_TEST_IMAGE")
	if image == "" {
		t.Skip("PANDOKOS_TEST_IMAGE is not set: build the image and set it to its name")
	}
	version, _, previous := previousPin(t)
	docker(t, "pull", "--quiet", previous)
	wantDigest := previous[strings.Index(previous, "@")+1:]
	if got := docker(t, "image", "inspect", "-f", "{{json .RepoDigests}}", previous); !strings.Contains(got, wantDigest) {
		t.Fatalf("the image pulled is %s, want the digest of test/upgrade/PREVIOUS (%s)", got, wantDigest)
	}
	if out := docker(t, "run", "--rm", previous, "version"); !strings.HasPrefix(out, "pandokos "+strings.TrimPrefix(version, "v")+" ") {
		t.Fatalf("the previous image says %q, want it to be %s", out, version)
	}

	d := dbtest.New(t)
	up := fakeprovider.New(t)
	up.Accept(fixtures.CanaryCredential)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("provider_secret", fixtures.CanaryCredential, 0o644)
	kek, err := os.ReadFile(testutil.WriteKEK(t))
	if err != nil {
		t.Fatal(err)
	}
	write("pandokos_kek", strings.TrimSpace(string(kek)), 0o444)
	dbEnv := []string{
		"-e", "PANDOKOS_DATABASE_ADDRESS=" + net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		"-e", "PANDOKOS_DATABASE_NAME=" + d.Name,
		"-e", "PANDOKOS_DATABASE_TLS_MODE=disable",
		"-e", "PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE=/run/secrets/pandokos_kek",
	}
	for _, role := range []string{"owner", "gateway", "admin", "worker"} {
		write("db_"+role, d.ConnOptions(role).Password.Reveal(), 0o644)
		dbEnv = append(dbEnv, "-e", "PANDOKOS_DATABASE_"+strings.ToUpper(role)+"_USER="+d.User(role),
			"-e", "PANDOKOS_DATABASE_"+strings.ToUpper(role)+"_PASSWORD_FILE=/run/secrets/db_"+role)
	}
	hardened := []string{"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--network", "host", "-v", dir + ":/run/secrets:ro"}
	run := func(img string, extra []string, args ...string) string {
		t.Helper()
		return docker(t, append(append(append(append([]string{"run"}, hardened...), dbEnv...), extra...), append([]string{img}, args...)...)...)
	}
	freeAddr := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().String()
	}

	// The previous image: the schema, the bootstrap key, a server.
	if out := run(previous, []string{"--rm"}, "migrate"); !strings.Contains(out, "schema is at version 16") {
		t.Fatalf("the previous image's migrate: %q", out)
	}
	initOut := run(previous, []string{"--rm"}, "init")
	m := regexp.MustCompile(`(?m)^key: +(pdk_p[a-z2-7]{16}_[a-z2-7]{59})$`).FindStringSubmatch(initOut)
	if m == nil {
		t.Fatalf("init output has no key line: %q", initOut)
	}
	write("platform.key", m[1], 0o644)
	inference, admin := freeAddr(), freeAddr()
	serve := func(img string) (id string) {
		t.Helper()
		id = run(img, []string{"-d", "-e", "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=" + inference, "-e", "PANDOKOS_LISTENERS_ADMIN_ADDRESS=" + admin})
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
		testutil.Eventually(t, 30*time.Second, "readiness of the container", func() bool {
			resp, err := http.Get("http://" + inference + "/readyz")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == 200
		})
		return id
	}
	id := serve(previous)
	cli := func(keyFile string, args ...string) string {
		return docker(t, append([]string{"exec", "-e", "PANDOKOS_CLI_SERVER=http://" + admin, "-e", "PANDOKOS_CLI_KEY_FILE=/run/secrets/" + keyFile, id, "/pandokos"}, args...)...)
	}
	reTenantKey := regexp.MustCompile(`(?m)^key: +(pdk_t[a-z2-7]{16}_[a-z2-7]{59})$`)
	out := cli("platform.key", "tenant", "create", "acme")
	tm, tid := reTenantKey.FindStringSubmatch(out), regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`).FindStringSubmatch(out)
	if tm == nil || tid == nil {
		t.Fatalf("tenant create output: %q", out)
	}
	write("admin.key", tm[1], 0o644)
	cli("admin.key", "service-account", "create", "--tenant", tid[1], "--role", "inference", "caller")
	km := reTenantKey.FindStringSubmatch(cli("admin.key", "key", "create", "--tenant", tid[1], "--service-account", "caller"))
	if km == nil {
		t.Fatal("key create printed no key")
	}
	cli("platform.key", "endpoint", "update", "--base-url", up.URL(), "--private", "openai", "default")
	cli("platform.key", "model", "create", "--availability", "all", "openai/"+fakeprovider.ModelBasic)
	cli("admin.key", "credential", "create", "--provider", "openai", "--secret-file", "/run/secrets/provider_secret", "container-credential")

	call := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+inference+"/openai/v1/chat/completions",
			strings.NewReader(`{"model":"fake-basic","messages":[{"role":"user","content":"upgrade"}]}`))
		req.Header.Set("Authorization", "Bearer "+km[1])
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	testutil.Eventually(t, 30*time.Second, "the call on the previous image", func() bool { return call() == 200 })
	docker(t, "stop", "-t", "20", id)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", id); code != "0" {
		t.Errorf("the previous image exited %s on SIGTERM", code)
	}

	// The image of this tree: one migration, the same key and credential.
	if out := run(image, []string{"--rm"}, "migrate"); !strings.Contains(out, "applied 0017_events") || !strings.Contains(out, "schema is at version 17") {
		t.Fatalf("the migrate of this image on the database of the previous one: %q", out)
	}
	id = serve(image)
	testutil.Eventually(t, 30*time.Second, "the same call on the image of this tree", func() bool { return call() == 200 })
	reqs := up.Requests()
	var last string
	for _, r := range reqs {
		if r.Method == http.MethodPost {
			last = r.Header.Get("Authorization")
		}
	}
	if last != "Bearer "+fixtures.CanaryCredential {
		t.Errorf("the provider did not receive the tenant's credential after the upgrade")
	}
	logs := docker(t, "logs", id)
	testutil.AssertAbsent(t, "container logs", logs, append(testutil.SecretForms(fixtures.CanaryCredential), testutil.KeyForms(km[1])...)...)
	testutil.AssertNoCanary(t, "container logs", logs)
	docker(t, "stop", "-t", "20", id)
}
