package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/testutil"
)

var reKEKGenerated = regexp.MustCompile(`^key-encryption key written to (.+); version (local-[0-9a-f]{16})\n$`)

// versionOfFile is the version identifier of the key in the file, by the provider.
func versionOfFile(t *testing.T, path string) string {
	t.Helper()
	k, err := secret.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := vault.NewLocalProvider(k)
	if err != nil {
		t.Fatalf("the key file is not accepted by the local provider: %v", err)
	}
	v, _, err := p.Wrap(t.Context(), make([]byte, 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestKEKGenerate is T-312.
func TestKEKGenerate(t *testing.T) {
	t.Run("a new file of mode 0600 with a key of 32 bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kek")
		code, out, errs := runCmd(t, nil, "kek", "generate", "--out", path)
		if code != 0 || errs != "" {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
		}
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
			t.Fatalf("the file: %v, %v", st, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		line := strings.TrimSuffix(string(raw), "\n")
		if strings.ContainsAny(line, "\r\n \t") || len(line) != 44 {
			t.Fatalf("the file is not one line of 44 characters")
		}
		key, err := base64.StdEncoding.Strict().DecodeString(line)
		if err != nil || len(key) != 32 {
			t.Fatalf("the content decodes to %d bytes: %v", len(key), err)
		}
		// The output names the version and not the key.
		m := reKEKGenerated.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("stdout %q is not `key-encryption key written to FILE; version local-…`", out)
		}
		if m[1] != path || m[2] != versionOfFile(t, path) {
			t.Errorf("the output names %q and %q, want the file %q and the version of its key %q", m[1], m[2], path, versionOfFile(t, path))
		}
		testutil.AssertAbsent(t, "stdout", out, testutil.SecretForms(line)...)
		testutil.AssertAbsent(t, "stdout", out, testutil.SecretForms(string(key))...)
	})

	t.Run("an existing file is refused and left alone", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kek")
		const before = "this file was here first\n"
		if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out, errs := runCmd(t, nil, "kek", "generate", "--out", path)
		if code != 1 || out != "" || !strings.HasPrefix(errs, "error: ") {
			t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
		}
		if got, _ := os.ReadFile(path); string(got) != before {
			t.Error("an existing file was changed")
		}
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
			t.Errorf("the mode of an existing file was changed to %v", st.Mode().Perm())
		}
		// A link that points nowhere is not a free name either: nothing is created behind it.
		dir := t.TempDir()
		target := filepath.Join(dir, "elsewhere")
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		code, _, errs = runCmd(t, nil, "kek", "generate", "--out", link)
		if code != 1 || errs == "" {
			t.Errorf("a dangling link: code %d, stderr %q", code, errs)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Error("the key was written through a link")
		}
	})

	t.Run("two runs differ", func(t *testing.T) {
		dir := t.TempDir()
		a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
		_, outA, _ := runCmd(t, nil, "kek", "generate", "--out", a)
		_, outB, _ := runCmd(t, nil, "kek", "generate", "--out", b)
		ra, _ := os.ReadFile(a)
		rb, _ := os.ReadFile(b)
		if len(ra) == 0 || string(ra) == string(rb) {
			t.Error("two generated keys are equal")
		}
		va, vb := reKEKGenerated.FindStringSubmatch(outA), reKEKGenerated.FindStringSubmatch(outB)
		if va == nil || vb == nil || va[2] == vb[2] {
			t.Errorf("two generated keys have one version: %q %q", outA, outB)
		}
	})

	t.Run("usage", func(t *testing.T) {
		dir := t.TempDir()
		for _, args := range [][]string{
			{"kek", "generate"}, {"kek", "generate", "--out"}, {"kek", "generate", "--out", ""},
			{"kek", "generate", "--bogus"}, {"kek", "generate", "--out", filepath.Join(dir, "x"), "extra"},
			{"kek"}, {"kek", "rotate"},
		} {
			code, out, errs := runCmd(t, nil, args...)
			if code != 2 || out != "" || errs == "" {
				t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
			}
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("a refused command created %d files", len(entries))
		}
		// There is no flag that prints the key.
		for _, flag := range []string{"--print", "--stdout", "-v", "--show"} {
			if code, out, _ := runCmd(t, nil, "kek", "generate", "--out", filepath.Join(dir, "y"), flag); code != 2 || out != "" {
				t.Errorf("%s: code %d, stdout %q", flag, code, out)
			}
		}
	})

	t.Run("a directory that does not exist", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "kek")
		code, out, errs := runCmd(t, nil, "kek", "generate", "--out", path)
		if code != 1 || out != "" || errs == "" {
			t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Error("the command created a directory")
		}
	})
}
