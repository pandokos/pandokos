package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestTenantExportHoldsNoSecretOrForeignData is T-1004: the export of a tenant holds what the tenant
// may read and nothing else: no canary, no secret of a key or a credential, no password hash, and
// nothing of another tenant; and a key that may not read everything stops it, naming the permission.
func TestTenantExportHoldsNoSecretOrForeignData(t *testing.T) {
	t.Parallel()
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, fixtures.CanaryLabel("tenant-a"), fixtures.CanaryLabel("sa-a"))
	b := newLifeTenant(t, p, boot, fixtures.CanaryLabel("tenant-b"), fixtures.CanaryLabel("sa-b"))
	// A member with a password (a hash is stored for it) and a custom role, in tenant A.
	p.mustCLI(t, a.adminFile, "role", "create", "--tenant", a.id, "--permission", "usage:read", "reader-a")
	passwordFile := filepath.Join(t.TempDir(), "member.password")
	p.mustCLI(t, a.adminFile, "member", "add", "--tenant", a.id, "--new", "--username", "member-a", "--role", "read-only", "--password-out", passwordFile)
	memberPassword := readKeyFile(t, passwordFile)
	// Something of tenant B that would be a leak anywhere in A's export: its key IDs and its metadata.
	p.mustCLI(t, b.adminFile, "role", "create", "--tenant", b.id, "--permission", "usage:read", "reader-"+fixtures.CanaryLabel("b"))

	dir := filepath.Join(t.TempDir(), "export")
	p.mustCLI(t, a.adminFile, "tenant-export", "--tenant", a.id, "--out", dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteString("\n")
	}
	text := all.String()
	if len(entries) < 17 {
		t.Errorf("%d files in the export", len(entries))
	}
	testutil.AssertNoCanary(t, "the export", text)
	// No secret: the inference key and the keys of the administrators, the platform key, the provider key, any hash of a password.
	for name, secret := range map[string]string{"the inference key": a.inference, "the administrator's key": a.adminKey, "the platform key": readKeyFile(t, boot), "the provider key": fixtures.CanaryProviderKey} {
		for _, form := range testutil.KeyForms(secret) {
			if strings.Contains(text, form) {
				t.Errorf("the export holds %s", name)
			}
		}
	}
	if strings.Contains(text, memberPassword) {
		t.Error("the export holds the initial password of a member")
	}
	for _, marker := range []string{"$argon2id$", "password_hash", "secret_hash", "token_hash", "wrapped_key", "kek_wrapped", "unwrap"} { // the audit detail of a credential names the key version that sealed it (kek_version): an identifier, not key material
		if strings.Contains(text, marker) {
			t.Errorf("the export holds %q", marker)
		}
	}
	// Tenant A's own data is there: its name, its service account, its role.
	for _, want := range []string{a.saName, "reader-a", "member-a"} {
		if !strings.Contains(text, want) {
			t.Errorf("the export of the tenant does not hold %q", want)
		}
	}
	// Nothing of tenant B.
	for what, v := range map[string]string{"the ID of tenant B": b.id, "the name of tenant B": b.name, "the service account of B": b.saName, "the key of B": b.inference} {
		if strings.Contains(text, v) {
			t.Errorf("the export of tenant A holds %s", what)
		}
	}
	// The directory is private.
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm()&0o077 != 0 {
		t.Errorf("the export directory: %v %v", st.Mode(), err)
	}

	// A key that may not read everything stops the export and names what it lacks; nothing is written.
	viewer := p.keyWithRole(t, a.adminFile, a.id, authz.RoleUsageViewer)
	viewerFile := filepath.Join(t.TempDir(), "viewer.key")
	if err := os.WriteFile(viewerFile, []byte(viewer+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(t.TempDir(), "denied")
	code, stdout, stderr := p.cli(t, viewerFile, "tenant-export", "--tenant", a.id, "--out", denied)
	if code == 0 {
		t.Fatalf("the export with a usage-viewer key ended well:\n%s", stdout)
	}
	named := false
	for _, perm := range []string{"service_account:read", "virtual_key:read", "credential:read", "token_issuer:read", "subject_mapping:read", "member:read", "role:read", "support_action:read", "audit:read"} {
		named = named || strings.Contains(stderr, perm)
	}
	if !named {
		t.Errorf("the refusal names no missing permission:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(denied, "manifest.json")); err == nil {
		t.Error("a manifest was written by a refused export")
	}
	if _, err := os.Stat(denied); err == nil {
		t.Error("the directory of a refused export exists")
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}
