package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// TestKEKRotationEndToEnd is T-1007, an exit test of the sprint: a rotation by configuration and a
// restart, with calls that keep succeeding while the job re-wraps, and the refusal of the old key as the key file.
func TestKEKRotationEndToEnd(t *testing.T) {
	up := fakeprovider.New(t)
	d := dbtest.NewMigrated(t)
	keys := vaulttest.Keys(t, 2)
	d.UseKEK(t, keys[0].Version) // the installation started with the first key
	p := startBinary(t, opts{level: "info", up: up, db: d, kekFiles: keys[:1]})
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "rotated-a", "workload-a")
	b := newLifeTenant(t, p, boot, "rotated-b", "workload-b")
	if states := kekStates(t, p, boot); states[keys[0].Version] != "active" || len(states) != 1 {
		t.Fatalf("the versions before the rotation: %v", states)
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// The new key is the key file, the old one an unwrap key: calls keep succeeding while the job works.
	p2 := restart(t, p, opts{level: "info", up: up, kekFiles: []vaulttest.Key{keys[1], keys[0]}})
	ready := completeLines(p2.stdout, "vault ready")
	if len(ready) != 1 || ready[0]["kek_version"] != keys[1].Version || ready[0]["kek_activation"] != "activated" || ready[0]["kek_unwrap_versions"] != float64(1) {
		t.Fatalf("vault ready after the rotation: %v", ready)
	}
	deadline := time.Now().Add(90 * time.Second)
	for kekStates(t, p2, boot)[keys[0].Version] != "retired" {
		for _, x := range []lifeTenant{a, b} {
			if !x.ok(t, p2) {
				t.Fatalf("a call of %s failed during the rotation", x.name)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the old version is not retired after 90 s: %v", kekStates(t, p2, boot))
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got := kekStates(t, p2, boot); got[keys[0].Version] != "retired" || got[keys[1].Version] != "active" {
		t.Errorf("the versions after the rotation: %v", got)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE kek_version = $1`, keys[0].Version); n != 0 {
		t.Errorf("%d data keys under the old version", n)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE kek_version = $1`, keys[1].Version); n < 2 {
		t.Errorf("%d data keys under the new version, want those of both tenants", n)
	}
	if lines := completeLines(p2.stdout, "key rotation complete"); len(lines) != 1 || lines[0]["kek_version"] != keys[0].Version {
		t.Errorf("the lines key rotation complete: %v", lines)
	}
	if code := p2.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// The new key only: it starts, and every credential of both tenants works.
	p3 := restart(t, p, opts{level: "info", up: up, kekFiles: keys[1:]})
	if ready := completeLines(p3.stdout, "vault ready"); len(ready) != 1 || ready[0]["kek_version"] != keys[1].Version || ready[0]["kek_activation"] != "unchanged" {
		t.Errorf("vault ready with the new key only: %v", ready)
	}
	for _, x := range []lifeTenant{a, b} {
		if !x.ok(t, p3) {
			t.Errorf("the credential of %s does not work with the new key only", x.name)
		}
	}
	if code := p3.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// The old key as the key file is refused. With the new key to unwrap, the message names both versions; alone,
	// the active version is not provided and the start is refused as before.
	for what, c := range map[string]struct {
		env  []string
		want []string
	}{
		"the old key as the key file, the new one to unwrap": {[]string{kekEnv + "=" + keys[0].File, "PANDOKOS_VAULT_KEK_LOCAL_UNWRAP_KEY_FILES=" + keys[1].File}, []string{keys[0].Version, keys[1].Version}},
		"the old key as the key file":                        {[]string{kekEnv + "=" + keys[0].File}, []string{keys[1].Version}},
	} {
		code, out, errs := runBinary(t, serveEnv(t, d, c.env...), "serve", "--config", writeConfig(t, "info"))
		failed := logLines(t, out, "startup failed")
		if code != 1 || len(failed) != 1 {
			t.Errorf("%s: exit %d, stdout %s, stderr %s", what, code, out, errs)
			continue
		}
		for _, v := range c.want {
			if !strings.Contains(out+errs, v) {
				t.Errorf("%s: the refusal does not name the version %s: %s", what, v, out)
			}
		}
		if len(logLines(t, out, "listening")) != 0 {
			t.Errorf("%s: the process listened", what)
		}
		for _, k := range keys {
			testutil.AssertAbsent(t, what, out+errs, testutil.KeyForms(k.Secret.Reveal())...)
		}
	}
}

// TestBackupBeforeDeletionYieldsNothing is T-1006, an exit test of the sprint: after a deletion and a
// rotation with the old key destroyed, a backup taken before the deletion no longer yields the tenant's
// credential. The control, with the old key, shows why the file has to be destroyed.
func TestBackupBeforeDeletionYieldsNothing(t *testing.T) {
	up := fakeprovider.New(t)
	d := dbtest.NewMigrated(t)
	keys := vaulttest.Keys(t, 2)
	d.UseKEK(t, keys[0].Version)
	p := startBinary(t, opts{level: "info", up: up, db: d, kekFiles: keys[:1], gracePeriod: "0s", deletionInterval: "10s"})
	boot := p.bootstrap(t)
	a := newLifeTenant(t, p, boot, "backed-up", "workload-a")
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// The backup: a copy of the database as it is, the credential sealed under version 1.
	backup := d.Copy(t)
	control := backup.Copy(t) // untouched, for the control at the end
	if n := backup.Count(t, `SELECT count(*)::int FROM pandokos.provider_credential WHERE tenant_id = $1::uuid`, a.id); n != 1 {
		t.Fatalf("the backup holds %d credentials of the tenant", n)
	}

	// The tenant is deleted, the key rotated, the old version retired, and the old key file destroyed (not given again).
	p2 := restart(t, p, opts{level: "info", up: up, kekFiles: keys[:1], gracePeriod: "0s", deletionInterval: "10s"})
	p2.mustCLI(t, boot, "tenant", "delete", a.id, "--reason", "closing", "--confirm-name", a.name)
	waitDeleted(t, d, a.tenantID(t))
	if code := p2.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	p3 := restart(t, p, opts{level: "info", up: up, kekFiles: []vaulttest.Key{keys[1], keys[0]}, gracePeriod: "0s", deletionInterval: "10s"})
	testutil.Eventually(t, 90*time.Second, "version 1 to be retired", func() bool { return kekStates(t, p3, boot)[keys[0].Version] == "retired" })
	deletion := cliJSON(t, p3, boot, "tenant", "deletion", "get", a.id)
	if ba, _ := deletion["backup_erasure"].(map[string]any); ba["state"] != "complete" {
		t.Errorf("backup_erasure after the retirement: %v", ba)
	}
	if code := p3.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	p4 := restart(t, p, opts{level: "info", up: up, kekFiles: keys[1:]})
	if code := p4.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	// Against the backup, with the current configuration (the key of version 2 only):
	var outputs strings.Builder
	// (a) serve refuses to start, naming version 1.
	code, out, errs := runBinary(t, serveEnv(t, backup, kekEnv+"="+keys[1].File), "serve", "--config", writeConfig(t, "info"))
	outputs.WriteString(out + errs)
	if failed := logLines(t, out, "startup failed"); code != 1 || len(failed) != 1 || !strings.Contains(out, keys[0].Version) {
		t.Errorf("serve on the backup with the current key: exit %d\nstdout: %s\nstderr: %s\nwant a refusal that names %s", code, out, errs, keys[0].Version)
	}
	// (b) the opener with the current provider cannot open the credential: its key is not here.
	logs := &testutil.LogBuffer{}
	gw := backup.Open(t, store.RoleGateway)
	opener := func(prov vault.KeyProvider) *vault.Opener {
		o, err := vault.NewOpener(vault.OpenerOptions{DB: gw, Provider: prov, Logger: telemetry.NewLogger(logs, "debug"), CredentialTTL: time.Second, DataKeyTTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	use := vault.Use{Principal: a.principal(t), RequestID: "req-backup-0001", Provider: "openai"}
	got, err := opener(vaulttest.Provider(t, keys[1])).Open(context.Background(), use)
	if err == nil || !errors.Is(err, vault.ErrUnavailable) || !got.Secret.IsZero() || !strings.Contains(logs.String(), "kek_unavailable") {
		t.Errorf("the credential of the backup with the current key: %v, %v; want the key material unavailable (kek_unavailable)", got, err)
	}
	// (c) with the row of the backup altered to claim version 2, it fails to unwrap.
	if got := backup.ActivateKEK(t, keys[1].Version); got != "activated" {
		t.Fatal(got)
	}
	if _, err := backup.Super(t).Exec(context.Background(), `UPDATE pandokos.tenant_data_key SET kek_version = $1 WHERE tenant_id = $2::uuid`, keys[1].Version, a.id); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	got, err = opener(vaulttest.Provider(t, keys[1])).Open(context.Background(), use)
	if err == nil || !errors.Is(err, vault.ErrUndecryptable) || !got.Secret.IsZero() || !strings.Contains(logs.String(), "data_key_unwrap") {
		t.Errorf("the credential of the backup with the row altered: %v, %v; want data_key_unwrap", got, err)
	}
	outputs.WriteString(logs.String())
	// The canary is in no output.
	testutil.AssertNoCanary(t, "the outputs of the checks against the backup", outputs.String())
	for _, k := range keys {
		testutil.AssertAbsent(t, "the outputs of the checks against the backup", outputs.String(), testutil.KeyForms(k.Secret.Reveal())...)
	}
	// The control: with the key of version 1 the backup does open the credential. This is why the file must be destroyed.
	o, err := vault.NewOpener(vault.OpenerOptions{DB: control.Open(t, store.RoleGateway), Provider: vaulttest.Provider(t, keys[0]),
		Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "debug"), CredentialTTL: time.Second, DataKeyTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := o.Open(context.Background(), use)
	if err != nil || opened.Secret.Reveal() != fixtures.CanaryCredential {
		t.Errorf("the control: the backup does not open the credential with the key of version 1: %v", err)
	}
}
