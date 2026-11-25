//go:build drill

package drill

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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
	if want := r.ExpectedVersion(b); version != want || (wantActivation != "" && activation != wantActivation) {
		t.Errorf("vault ready says kek_version %q and kek_activation %q, want %q and %q (empty: any)", version, activation, want, wantActivation)
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
	// G-1, as the drill observed it: the form the guide gave before (the whole log against a head kept
	// earlier) reports a mismatch exactly when rows follow the kept head in the restored log, and the
	// right form (above) verifies in both cases. serve itself writes no platform audit row when it
	// starts on a restored database and the activation is unchanged, so what the assertion holds is
	// the log, and what it records is whether serve moved the head here.
	head := r.W.readHead(r.Proc, w.platform, "platform-audit", "head")
	whole, _ := r.AuditVerifyWhole("", b.Heads.Platform)
	rowsFollow := head.Seq > b.Heads.Platform.Seq
	if rowsFollow != (whole != 0) {
		t.Errorf("the whole log against the head kept at %s: exit %d, and the restored platform log is at seq %d, the kept head at %d (G-1): the wrong form must fail exactly when rows follow the kept head", b.Point, whole, head.Seq, b.Heads.Platform.Seq)
	}
	t.Logf("G-1: after the start with kek_activation %q the platform log is at seq %d and the head kept at %s at %d; the whole-log form exits %d", activation, head.Seq, b.Point, b.Heads.Platform.Seq, whole)

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

// scanBackups searches every artefact under the backup directory for every secret of the world and
// every byte sequence of a key file, in the form in which a secret could be read: the plain text of each
// dump (a custom-format dump compresses its table data, so its bytes prove nothing: pg_restore writes it
// out), the members of the base backup, and the files of the WAL archive. The same search must find a
// value that is not a secret, the name of a tenant, in every kind of artefact: a search that finds that
// in none of the data would prove nothing about the secrets. It returns the number of artefacts searched
// and the bytes looked at.
func scanBackups(t *testing.T, w *World) (files int, bytesRead int64) {
	t.Helper()
	needles := append([]string(nil), w.KeyMaterial()...)
	for _, s := range w.Secrets() {
		needles = append(needles, testutil.SecretForms(s)...)
	}
	needles = append(needles, w.P.SuperuserPassword(t))
	control := w.Tenants[TenantA].Name
	found := map[string]bool{}
	search := func(kind, name string, data []byte) {
		files++
		bytesRead += int64(len(data))
		for i, n := range needles {
			if n != "" && bytes.Contains(data, []byte(n)) {
				t.Errorf("secret %d is in %s", i, name)
			}
		}
		if bytes.Contains(data, []byte(control)) {
			found[kind] = true
			found[name] = true
		}
	}
	var dumps []string
	err := filepath.Walk(w.P.Backup, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(w.P.Backup, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(rel, "dumps"+string(filepath.Separator)):
			dumps = append(dumps, rel)
			search("dump bytes", rel, data)
		case strings.HasSuffix(path, ".gz"):
			search("base", rel, data)
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
				search("base", rel+":"+h.Name, member)
			}
		case strings.HasPrefix(rel, "wal"+string(filepath.Separator)):
			search("wal", rel, data)
		default:
			search("other", rel, data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dumps {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		text, err := w.sh(ctx, Source, "", "pg_restore -f - /backup/"+filepath.ToSlash(d))
		cancel()
		if err != nil {
			t.Fatalf("pg_restore of %s to plain text: %v", d, err)
		}
		search("dump", d+" as plain text", []byte(text))
		if !found[d+" as plain text"] {
			t.Errorf("the search finds the name of a tenant (a value that is no secret) in no part of %s as plain text: it would prove nothing about the secrets", d)
		}
	}
	for _, kind := range []string{"base", "wal"} {
		if !found[kind] {
			t.Errorf("the search finds the name of a tenant (a value that is no secret) in no member of the %s artefacts: it would prove nothing about the secrets", kind)
		}
	}
	if len(dumps) == 0 {
		t.Error("no dump was found under the backup directory")
	}
	return files, bytesRead
}
