//go:build drill

package drill

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testutil"
)

// worldTenants are the four tenants of every world, in order.
var worldTenants = []string{TenantA, TenantB, TenantC, TenantD}

// assertRestored is what is asserted after every restore (sprint.md 5.3), as the guide's step 4.6
// corrected: with the tenants that should work, against the backup the restore was made from.
//
//  1. vault ready names the expected kek_version and, when the case says which, the activation;
//  2. both chains verify against the heads kept at the backup's time (G-1, the right form);
//  3. a call with each tenant's inference key reaches the provider with the tenant's own credential;
//  4. kek-version list shows the versions of the backup;
//  5. the quota of each tenant refuses at the count that the restored ledger gives;
//  6. a destination receives a new event, signed with the secret issued before the backup;
//  7. nothing was rotated before these passed (4.7): the list of versions is the same after.
func assertRestored(t *testing.T, r *Restored, b *Backup, wantActivation string, working ...string) {
	t.Helper()
	w := r.W
	if r.Proc == nil {
		t.Fatalf("serve did not start (exit %d):\n%s", r.ServeCode, r.ServeLog)
	}
	before := r.KekVersions()

	// 1.
	version, activation := r.VaultReady()
	if version != b.KEKVersion || (wantActivation != "" && activation != wantActivation) {
		t.Errorf("vault ready says kek_version %q and kek_activation %q, want %q and %q (empty: any)", version, activation, b.KEKVersion, wantActivation)
	}

	// 2. The right form of step 4.6, for the platform's chain and every tenant's.
	if code, out := r.AuditVerify("", b.Heads.Platform); code != 0 {
		t.Errorf("the platform's chain against the head kept at %s: exit %d\n%s", b.Point, code, out)
	}
	for _, name := range worldTenants {
		if code, out := r.AuditVerify(name, b.Heads.Tenants[name]); code != 0 {
			t.Errorf("the chain of tenant %s against the head kept at %s: exit %d\n%s", name, b.Point, code, out)
		}
	}
	// G-1: serve wrote rows at its first start after the restore, so the form the guide gave before
	// (the whole log against a head kept earlier) reports a mismatch where the right form verifies.
	if code, _ := r.AuditVerifyWhole("", b.Heads.Platform); code == 0 {
		t.Error("the platform's whole log verifies against the head kept at the backup although serve wrote rows after it (G-1): the wrong form is not wrong")
	}

	// 3., 5. and 6.
	w.Receiver.SetSecrets(secretsOf(w, working)...)
	for _, name := range working {
		tn := w.Tenants[name]
		status, seen := r.Call(name)
		if status != 200 || seen != Fingerprint(tn.Credential) {
			t.Errorf("tenant %s: a call with its inference key gives %d and reaches the provider with credential %q, want 200 and this tenant's own (the exit criterion)", name, status, seen)
		}
		if tn.Quota > 0 {
			rows := r.LedgerRows(name)
			if admitted, want := r.AdmittedBeforeRefusal(name), max(0, tn.Quota-int(rows)-1); admitted != want {
				t.Errorf("tenant %s: the quota admits %d more calls, want %d (the limit %d less the %d rows of the restored ledger and the call above)", name, admitted, want, tn.Quota, rows)
			}
		}
		id := r.EmitEvent(name)
		testutil.Eventually(t, 60*time.Second, "tenant "+name+"'s destination to receive the new event, signed with the secret issued before the backup", func() bool {
			for _, got := range w.Receiver.IDs() {
				if got == id {
					return true
				}
			}
			return false
		})
	}

	// 4. and 7.
	out := r.KekVersions()
	if !strings.Contains(out, b.KEKVersion) {
		t.Errorf("kek-version list does not show the version of the backup (%s):\n%s", b.KEKVersion, out)
	}
	if out != before {
		t.Errorf("the key versions changed while the checks ran (step 4.7: rotate nothing until they pass):\n%s\n%s", before, out)
	}
}

func secretsOf(w *World, tenants []string) []string {
	var out []string
	for _, name := range tenants {
		if s := w.Tenants[name].Secret; s != "" {
			out = append(out, s)
		}
	}
	return out
}

// scanBackups searches every file under the backup directory for every secret of the world and
// every byte sequence of a key file, in the files themselves and, for the base backup, inside its
// compressed archive. It returns the number of files and bytes looked at.
func scanBackups(t *testing.T, w *World) (files int, bytesRead int64) {
	t.Helper()
	needles := append([]string(nil), w.KeyMaterial()...)
	for _, s := range w.Secrets() {
		needles = append(needles, testutil.SecretForms(s)...)
	}
	needles = append(needles, w.P.SuperuserPassword(t))
	search := func(name string, data []byte) {
		files++
		bytesRead += int64(len(data))
		for i, n := range needles {
			if n != "" && bytes.Contains(data, []byte(n)) {
				t.Errorf("secret %d is in %s", i, name)
			}
		}
	}
	err := filepath.Walk(w.P.Backup, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(w.P.Backup, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		search(rel, data)
		if strings.HasSuffix(path, ".gz") {
			zr, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Logf("%s: not a gzip archive (%v): searched as it is", rel, err)
				return nil
			}
			tr := tar.NewReader(zr)
			for {
				h, err := tr.Next()
				if err != nil {
					if err != io.EOF {
						t.Errorf("%s: the archive is cut: %v", rel, err)
					}
					break
				}
				member, _ := io.ReadAll(tr)
				search(rel+":"+h.Name, member)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files, bytesRead
}
