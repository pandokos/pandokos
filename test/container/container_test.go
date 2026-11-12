//go:build container

// Package container tests a built image with the docker CLI.
package container

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	for name, v := range map[string]string{"access_token": fixtures.CanaryAccessToken, "openai_api_key": fixtures.CanaryProviderKey} {
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
	hardened := []string{"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--network", "host", "-v", dir + ":/run/secrets:ro"}

	// The image creates the schema with its migrate command.
	migrateArgs := append(append([]string{"run", "--rm"}, hardened...), dbEnv...)
	if out := docker(t, append(migrateArgs, image, "migrate")...); !strings.Contains(out, "schema is at version 1") {
		t.Fatalf("migrate output: %q", out)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()

	serveArgs := append(append([]string{"run", "-d"}, hardened...), dbEnv...)
	serveArgs = append(serveArgs,
		"-e", "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:"+port,
		"-e", "PANDOKOS_SKELETON_ACCESS_TOKEN_FILE=/run/secrets/access_token",
		"-e", "PANDOKOS_SKELETON_OPENAI_API_KEY_FILE=/run/secrets/openai_api_key",
		"-e", "PANDOKOS_SKELETON_OPENAI_BASE_URL="+up.BaseURL(),
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"fake-basic","stream":true,"messages":[{"role":"user","content":"`+fixtures.CanaryPrompt+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+fixtures.CanaryAccessToken)
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
	testutil.AssertNoCanary(t, "container logs", logs)
	testutil.AssertAbsent(t, "container logs", logs, d.Passwords()...)

	if out := docker(t, "run", "--rm", image, "version"); !strings.HasPrefix(out, "pandokos ") {
		t.Errorf("version output: %q", out)
	}
}
