package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// TestApplySecretsOut is T-1125: apply --secrets-out DIR is the only way apply creates an event
// destination, and the signing secret shown once by the API goes to a file there and nowhere else:
// the directory must exist and be private; the file is event-destination-<name>, mode 0600, created
// exclusively; an existing file fails before any destination is created; --dry-run writes nothing;
// and the secret is in no output.
func TestApplySecretsOut(t *testing.T) {
	t.Parallel()
	const secret = "whsec_SECRETSOUTSECRETSOUTSECRETSOUTSECRETSOUT0="
	const doc = "pandokos: v1\nkind: tenant\nevent_destinations:\n  - name: hooks\n    url: https://hooks.example.test/in\n    enabled: true\n"

	setup := func(t *testing.T) (*rig, string) {
		t.Helper()
		r := newRig(t)
		r.f.out["createEventDestination"] = &adminapi.EventDestinationCreated{Destination: destination("hooks"), Secret: secret}
		file := filepath.Join(t.TempDir(), "doc.yaml")
		if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return r, file
	}
	private := func(t *testing.T) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "secrets")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	noSecret := func(t *testing.T, what, text string) {
		t.Helper()
		if strings.Contains(text, secret) || strings.Contains(text, "whsec_") {
			t.Errorf("the signing secret is in %s:\n%s", what, text)
		}
	}
	refused := func(t *testing.T, r *rig, code int, out, errs string) {
		t.Helper()
		if code != 2 {
			t.Errorf("exit %d, want 2 (a usage error)\nstdout: %s\nstderr: %s", code, out, errs)
		}
		if strings.Contains(errs, "not defined") {
			t.Errorf("the flag --secrets-out does not exist: %s", errs)
		}
		if n := len(r.f.calls("createEventDestination")); n != 0 {
			t.Errorf("%d destinations were created before the refusal", n)
		}
		noSecret(t, "stdout", out)
		noSecret(t, "stderr", errs)
	}

	t.Run("a destination is created and its secret goes to the file", func(t *testing.T) {
		t.Parallel()
		r, file := setup(t)
		dir := private(t)
		code, out, errs := r.run(t, "apply", "--tenant", tid, "--file", file, "--secrets-out", dir)
		if code != 0 {
			t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errs)
		}
		if n := len(r.f.calls("createEventDestination")); n != 1 {
			t.Fatalf("%d destinations created, want 1", n)
		}
		path := filepath.Join(dir, "event-destination-hooks")
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("the secret file: %v", err)
		}
		if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
			t.Errorf("the secret file has mode %v, want a regular file of 0600", info.Mode())
		}
		got, _ := os.ReadFile(path)
		if strings.TrimSpace(string(got)) != secret {
			t.Errorf("the secret file does not hold the secret the API showed once")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Errorf("the directory holds %d entries, want only the secret file", len(entries))
		}
		noSecret(t, "stdout", out)
		noSecret(t, "stderr", errs)
		if !strings.Contains(out, dir) {
			t.Errorf("the summary does not name the directory:\n%s", out)
		}
	})

	t.Run("without the flag an absent destination is skipped", func(t *testing.T) {
		t.Parallel()
		r, file := setup(t)
		code, out, errs := r.run(t, "apply", "--tenant", tid, "--file", file)
		if code != 0 || !strings.Contains(out, "needs --secrets-out") || len(r.f.calls("createEventDestination")) != 0 {
			t.Errorf("exit %d, %d created\nstdout: %s\nstderr: %s", code, len(r.f.calls("createEventDestination")), out, errs)
		}
		noSecret(t, "stdout", out)
	})

	t.Run("the directory must exist", func(t *testing.T) {
		t.Parallel()
		r, file := setup(t)
		code, out, errs := r.run(t, "apply", "--tenant", tid, "--file", file, "--secrets-out", filepath.Join(t.TempDir(), "missing"))
		refused(t, r, code, out, errs)
	})

	t.Run("the directory must be private", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []os.FileMode{0o750, 0o705, 0o770, 0o755} {
			r, file := setup(t)
			dir := private(t)
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			code, out, errs := r.run(t, "apply", "--tenant", tid, "--file", file, "--secrets-out", dir)
			refused(t, r, code, out, errs)
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("mode %v: the directory holds %d files after the refusal", mode, len(entries))
			}
		}
	})

	t.Run("a file or a link is not a directory", func(t *testing.T) {
		t.Parallel()
		r, file := setup(t)
		code, out, errs := r.run(t, "apply", "--tenant", tid, "--file", file, "--secrets-out", file)
		refused(t, r, code, out, errs)

		r2, file2 := setup(t)
		real := private(t)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		code, out, errs = r2.run(t, "apply", "--tenant", tid, "--file", file2, "--secrets-out", link)
		refused(t, r2, code, out, errs)
	})

	t.Run("an existing file fails before any destination is created", func(t *testing.T) {
		t.Parallel()
		r, file := setup(t)
		dir := private(t)
		path := filepath.Join(dir, "event-destination-hooks")
		if err := os.WriteFile(path, []byte("an earlier secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errs := r.run(t, "apply", "--tenant", tid, "--file", file, "--secrets-out", dir)
		refused(t, r, code, out, errs)
		if got, _ := os.ReadFile(path); string(got) != "an earlier secret" {
			t.Error("the existing file was overwritten")
		}
	})

	t.Run("a dry run writes nothing", func(t *testing.T) {
		t.Parallel()
		r, file := setup(t)
		dir := private(t)
		code, out, errs := r.run(t, "apply", "--tenant", tid, "--file", file, "--secrets-out", dir, "--dry-run")
		if code != 0 {
			t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errs)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("a dry run wrote %d files", len(entries))
		}
		if n := len(r.f.calls("createEventDestination")); n != 0 {
			t.Errorf("a dry run created %d destinations", n)
		}
		noSecret(t, "stdout", out)
		noSecret(t, "stderr", errs)
	})
}
