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
	for name, v := range map[string]string{"openai_api_key": fixtures.CanaryProviderKey} {
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
	if out := docker(t, append(migrateArgs, image, "migrate")...); !strings.Contains(out, "schema is at version 3") {
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
		"-e", "PANDOKOS_SKELETON_OPENAI_API_KEY_FILE=/run/secrets/openai_api_key",
		"-e", "PANDOKOS_SKELETON_OPENAI_BASE_URL="+up.BaseURL(),
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"fake-basic","stream":true,"messages":[{"role":"user","content":"`+fixtures.CanaryPrompt+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+inferenceKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(bufio.NewReader(resp.Body))
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasSuffix(string(body), "data: [DONE]\n\n") {
		t.Fatalf("streaming call: %d %q", resp.StatusCode, body)
	}

	docker(t, "stop", "-t", "10", id)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", id); code != "0" {
		t.Errorf("exit code %s", code)
	}
	logs := docker(t, "logs", id)
	// The key was read, the version is announced, and the mode of the file is warned about (T-362).
	if !strings.Contains(logs, `"msg":"vault ready"`) || !strings.Contains(logs, `"kek_provider":"local"`) {
		t.Errorf("the container did not log `vault ready`:\n%s", logs)
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
