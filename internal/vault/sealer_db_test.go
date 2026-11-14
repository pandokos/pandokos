package vault_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// activeVersion is the version of the key-encryption key that the provider wraps with now.
func activeVersion(t *testing.T, p vault.KeyProvider) string {
	t.Helper()
	v, _, err := p.Wrap(ctx(t), make([]byte, 32), []byte("version probe"))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// unwrapDataKey reads the data key of a tenant as the admin role may (it holds SELECT on the
// table) and unwraps it with the provider, to check what the Sealer stored.
func unwrapDataKey(t *testing.T, w *vworld, tid tenant.ID, version int32) *vault.DataKey {
	t.Helper()
	var row queries.GetDataKeyRow
	err := w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetDataKey(c, version)
		return err
	})
	if err != nil {
		t.Fatalf("read the data key: %v", err)
	}
	raw, err := w.prov.Unwrap(ctx(t), row.KekVersion, row.WrappedKey, vault.DataAAD(tid, version))
	if err != nil {
		t.Fatalf("unwrap the data key: %v", err)
	}
	dk, err := vault.NewDataKey(raw, tid, version)
	if err != nil {
		t.Fatal(err)
	}
	return dk
}

func count(t *testing.T, w *vworld, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := w.d.Super(t).QueryRow(ctx(t), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// TestSealerDB is T-324.
func TestSealerDB(t *testing.T) {
	w := newVWorld(t)
	version := activeVersion(t, w.prov)

	t.Run("EnsureDataKey inside Provision", func(t *testing.T) {
		var tid tenant.ID
		err := w.admin.Provision(ctx(t), func(c context.Context, tx store.Tx, bind func(context.Context, tenant.ID) error) error {
			tn, err := queries.New(tx).CreateTenant(c, queries.CreateTenantParams{Name: "provisioned"})
			if err != nil {
				return err
			}
			if tid, err = tenant.ParseID(tn.ID); err != nil {
				return err
			}
			if err := bind(c, tid); err != nil {
				return err
			}
			created, kek, err := w.sealer.EnsureDataKey(c, tx, tid)
			if err != nil || !created || kek != version {
				return fmt.Errorf("EnsureDataKey: created=%v kek=%q: %w", created, kek, err)
			}
			again, kek2, err := w.sealer.EnsureDataKey(c, tx, tid)
			if err != nil || again || kek2 != version {
				return fmt.Errorf("the second EnsureDataKey: created=%v kek=%q: %w", again, kek2, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := count(t, w, `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid AND version = 1 AND kek_version = $2`, tid.String(), version); n != 1 {
			t.Errorf("%d data keys of version 1 for the new tenant", n)
		}
		var rows int
		if err := w.d.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.kek_version WHERE id = $1 AND provider = 'local'`, version).Scan(&rows); err != nil || rows != 1 {
			t.Errorf("the version %s is registered %d times: %v", version, rows, err)
		}
		// The key is the one the provider unwrapped, and is 32 bytes.
		_ = unwrapDataKey(t, w, tid, 1)
		// A transaction that fails takes the key with it.
		var failed tenant.ID
		err = w.admin.Provision(ctx(t), func(c context.Context, tx store.Tx, bind func(context.Context, tenant.ID) error) error {
			tn, err := queries.New(tx).CreateTenant(c, queries.CreateTenantParams{Name: "rolled-back"})
			if err != nil {
				return err
			}
			if failed, err = tenant.ParseID(tn.ID); err != nil {
				return err
			}
			if err := bind(c, failed); err != nil {
				return err
			}
			if _, _, err := w.sealer.EnsureDataKey(c, tx, failed); err != nil {
				return err
			}
			return errors.New("abort")
		})
		if err == nil {
			t.Fatal("the transaction did not fail")
		}
		if n := count(t, w, `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, failed.String()); n != 0 {
			t.Errorf("a rolled-back transaction left %d data keys", n)
		}
	})

	t.Run("a tenant without a data key gets one at its first Seal", func(t *testing.T) {
		tid := w.d.SeedTenant(t, "seeded")
		if n := count(t, w, `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, tid.String()); n != 0 {
			t.Fatalf("a seeded tenant has %d data keys", n)
		}
		credID, plaintext := "0192f0f0-1111-7000-8000-0000000000aa", fixtures.CanaryCredential
		var first, second vault.Sealed
		err := w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
			var err error
			if first, err = w.sealer.Seal(c, tx, tid, credID, secret.New(plaintext)); err != nil {
				return err
			}
			second, err = w.sealer.Seal(c, tx, tid, credID, secret.New(plaintext))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !first.DataKeyCreated || second.DataKeyCreated {
			t.Errorf("DataKeyCreated is %v then %v, want true then false", first.DataKeyCreated, second.DataKeyCreated)
		}
		for i, s := range []vault.Sealed{first, second} {
			if s.KeyVersion != 1 || s.KEKVersion != version || len(s.Secret) != len(plaintext)+29 || s.Secret[0] != 0x01 {
				t.Errorf("seal %d: version %d, kek %q, %d bytes", i, s.KeyVersion, s.KEKVersion, len(s.Secret))
			}
			if bytes.Contains(s.Secret, []byte(plaintext[:12])) {
				t.Errorf("seal %d holds the plaintext", i)
			}
		}
		if bytes.Equal(first.Secret, second.Secret) {
			t.Error("two seals are equal")
		}
		dk := unwrapDataKey(t, w, tid, 1)
		for i, s := range []vault.Sealed{first, second} {
			got, err := dk.Open(credID, s.Secret)
			if err != nil || string(got) != plaintext {
				t.Errorf("seal %d does not open with the stored data key: %v", i, err)
			}
			if s.Fingerprint != dk.Fingerprint([]byte(plaintext)) || s.Hint != vault.Hint(plaintext) {
				t.Errorf("seal %d: fingerprint or hint differ from the ones of the data key", i)
			}
		}
		if first.Fingerprint != second.Fingerprint {
			t.Error("one secret, two fingerprints")
		}
		if n := count(t, w, `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, tid.String()); n != 1 {
			t.Errorf("%d data keys after two seals", n)
		}
	})

	t.Run("twenty concurrent first seals leave one data key and twenty credentials that open", func(t *testing.T) {
		tid := w.d.SeedTenant(t, "concurrent")
		const n = 20
		var wg sync.WaitGroup
		ids := make([]string, n)
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// One provider per credential: this test is about the data key, not the default.
				provider := fmt.Sprintf("provider-%02d", i)
				errs[i] = w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
					q := queries.New(tx)
					id, err := q.NewCredentialID(c)
					if err != nil {
						return err
					}
					sealed, err := w.sealer.Seal(c, tx, tid, id, secret.New(fmt.Sprintf("synthetic-concurrent-secret-%02d", i)))
					if err != nil {
						return err
					}
					ids[i] = id
					_, err = q.CreateCredential(c, queries.CreateCredentialParams{TenantID: tid.String(), ID: id, Provider: provider, Label: "main",
						KeyVersion: sealed.KeyVersion, Secret: sealed.Secret, Fingerprint: sealed.Fingerprint, Hint: sealed.Hint})
					return err
				})
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("seal %d: %v", i, err)
			}
		}
		if got := count(t, w, `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, tid.String()); got != 1 {
			t.Errorf("%d data keys, want 1", got)
		}
		if got := count(t, w, `SELECT count(*) FROM pandokos.provider_credential WHERE tenant_id = $1::uuid`, tid.String()); got != n {
			t.Errorf("%d credentials, want %d", got, n)
		}
		dk := unwrapDataKey(t, w, tid, 1)
		for i, id := range ids {
			var sealed []byte
			if err := w.d.Super(t).QueryRow(ctx(t), `SELECT secret FROM pandokos.provider_credential WHERE id = $1::uuid`, id).Scan(&sealed); err != nil {
				t.Fatal(err)
			}
			got, err := dk.Open(id, sealed)
			if err != nil || string(got) != fmt.Sprintf("synthetic-concurrent-secret-%02d", i) {
				t.Errorf("credential %d does not open with the one data key: %v", i, err)
			}
		}
	})

	t.Run("the version of the key-encryption key is registered once", func(t *testing.T) {
		if got := count(t, w, `SELECT count(*) FROM pandokos.kek_version`); got != 1 {
			t.Errorf("%d versions are registered, want 1", got)
		}
		if got := count(t, w, `SELECT count(*) FROM pandokos.kek_version WHERE id = $1 AND provider = 'local'`, version); got != 1 {
			t.Errorf("the version %s is registered %d times", version, got)
		}
	})

	t.Run("the admin role cannot read the sealed secret, so the Sealer never asked", func(t *testing.T) {
		tid := w.d.SeedTenant(t, "no-read")
		w.createCredential(t, w.sealer, tid, "openai", "main", fixtures.CanaryCredential)
		// The control: the same role is refused the column. Everything the Sealer did above ran
		// as that role, in that session, and succeeded; no statement of it can have read `secret`.
		err := w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
			var b []byte
			return tx.QueryRow(c, `SELECT secret FROM pandokos.provider_credential LIMIT 1`).Scan(&b)
		})
		if store.SQLState(err) != "42501" {
			t.Errorf("the admin role read the sealed secret: %v", err)
		}
	})

	t.Run("a failing provider is unavailable, not a half-written key", func(t *testing.T) {
		bad := &failingProvider{KeyProvider: w.prov}
		sealer, err := vault.NewSealer(bad)
		if err != nil {
			t.Fatal(err)
		}
		tid := w.d.SeedTenant(t, "failing")
		err = w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
			_, err := sealer.Seal(c, tx, tid, "0192f0f0-1111-7000-8000-0000000000bb", secret.New("synthetic-secret-0001"))
			return err
		})
		if err == nil {
			t.Fatal("Seal succeeded with a provider that cannot wrap")
		}
		if n := count(t, w, `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, tid.String()); n != 0 {
			t.Errorf("%d data keys after a failed wrap", n)
		}
		// Another tenant whose data key exists, with a provider that cannot unwrap it.
		tid2 := w.d.SeedTenant(t, "failing-unwrap")
		w.createCredential(t, w.sealer, tid2, "openai", "main", "synthetic-secret-0002")
		bad.unwrapFails = true
		err = w.admin.WithTenant(ctx(t), tid2, func(c context.Context, tx store.Tx) error {
			_, err := sealer.Seal(c, tx, tid2, "0192f0f0-1111-7000-8000-0000000000cc", secret.New("synthetic-secret-0003"))
			return err
		})
		if !errors.Is(err, vault.ErrUnavailable) {
			t.Errorf("a data key that does not unwrap: %v, want ErrUnavailable", err)
		}
		var se *vault.SealError
		if !errors.As(err, &se) || se.Cause != "data_key_unwrap" || se.TenantID != tid2 || se.DataKeyVersion != 1 || se.KEKVersion == "" ||
			se.CredentialID != "0192f0f0-1111-7000-8000-0000000000cc" {
			t.Errorf("a provider that fails to unwrap: %+v", se)
		}
		// A KEK version the provider does not hold is another class.
		bad.unwrapFails, bad.unwrapErr = true, vault.ErrKeyVersionUnknown
		err = w.admin.WithTenant(ctx(t), tid2, func(c context.Context, tx store.Tx) error {
			_, err := sealer.Seal(c, tx, tid2, "0192f0f0-1111-7000-8000-0000000000dd", secret.New("synthetic-secret-0004"))
			return err
		})
		if !errors.Is(err, vault.ErrUnavailable) || !errors.As(err, &se) || se.Cause != "kek_unavailable" {
			t.Errorf("a KEK version that is unknown: %v (%+v)", err, se)
		}
		if err != nil {
			testutil.AssertAbsent(t, "the error", err.Error(), testutil.SecretForms("synthetic-secret-0003")...)
		}
	})
}

// failingProvider is a provider that works through the real one until it is told to fail wrapping
// (the default) or unwrapping.
type failingProvider struct {
	vault.KeyProvider
	unwrapFails bool
	// unwrapErr, when set, is the error of Unwrap while unwrapFails; the default is ErrUnwrap.
	unwrapErr error
}

func (f *failingProvider) Wrap(c context.Context, plaintext, aad []byte) (string, []byte, error) {
	if !f.unwrapFails {
		return "", nil, errors.New("the provider is out of reach")
	}
	return f.KeyProvider.Wrap(c, plaintext, aad)
}

func (f *failingProvider) Unwrap(c context.Context, version string, wrapped, aad []byte) ([]byte, error) {
	if f.unwrapFails {
		if f.unwrapErr != nil {
			return nil, f.unwrapErr
		}
		return nil, vault.ErrUnwrap
	}
	return f.KeyProvider.Unwrap(c, version, wrapped, aad)
}

// TestCheck is T-328.
func TestCheck(t *testing.T) {
	w := newVWorld(t)
	version := activeVersion(t, w.prov)

	t.Run("an empty database passes and names the active version", func(t *testing.T) {
		got, err := vault.Check(ctx(t), w.admin, w.prov)
		if err != nil || got != version {
			t.Errorf("Check = %q, %v; want %q", got, err, version)
		}
		if n := count(t, w, `SELECT count(*) FROM pandokos.kek_version`); n != 0 {
			t.Errorf("Check registered %d versions: it must only read", n)
		}
	})

	t.Run("a registered version of this provider passes", func(t *testing.T) {
		w := newVWorld(t)
		tid := w.d.SeedTenant(t, "registered")
		w.createCredential(t, w.sealer, tid, "openai", "main", "synthetic-secret-0001")
		if got, err := vault.Check(ctx(t), w.admin, w.prov); err != nil || got != activeVersion(t, w.prov) {
			t.Errorf("Check = %q, %v", got, err)
		}
	})

	t.Run("a registered version of another key fails and names the version", func(t *testing.T) {
		w := newVWorld(t)
		key, err := vault.GenerateLocalKey()
		if err != nil {
			t.Fatal(err)
		}
		other, err := vault.NewLocalProvider(key)
		if err != nil {
			t.Fatal(err)
		}
		otherSealer, err := vault.NewSealer(other)
		if err != nil {
			t.Fatal(err)
		}
		tid := w.d.SeedTenant(t, "other-key")
		w.createCredential(t, otherSealer, tid, "openai", "main", "synthetic-secret-0001")
		otherVersion := activeVersion(t, other)
		_, err = vault.Check(ctx(t), w.admin, w.prov)
		if err == nil {
			t.Fatal("Check passed with a database wrapped by another key")
		}
		want := "the database holds data keys wrapped by key-encryption key version " + otherVersion +
			" which this configuration does not provide; see docs/operations/vault.md"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error is %q\nwant it to contain %q", err.Error(), want)
		}
		// No byte of any key is in the message.
		for _, k := range [][]string{testutil.SecretForms(key.Reveal())} {
			testutil.AssertAbsent(t, "the error of Check", err.Error(), k...)
		}
	})

	t.Run("a registered version of another provider fails", func(t *testing.T) {
		w := newVWorld(t)
		if _, err := w.d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.kek_version (id, provider) VALUES ($1, 'vault-transit')`, version); err != nil {
			t.Fatal(err)
		}
		_, err := vault.Check(ctx(t), w.admin, w.prov)
		if err == nil || !strings.Contains(err.Error(), version) {
			t.Errorf("a version registered by another provider: %v", err)
		}
	})

	t.Run("one bad version among good ones fails", func(t *testing.T) {
		w := newVWorld(t)
		if _, err := w.d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.kek_version (id, provider) VALUES ($1, 'local'), ('local-0000000000000000', 'local')`, version); err != nil {
			t.Fatal(err)
		}
		_, err := vault.Check(ctx(t), w.admin, w.prov)
		if err == nil || !strings.Contains(err.Error(), "local-0000000000000000") {
			t.Errorf("one version the provider cannot serve: %v", err)
		}
	})
}

var rePsqlVar = regexp.MustCompile(`(^|[^:]):('?)[A-Za-z_][A-Za-z0-9_]*('?)`)

// TestLostKeyRecovery is T-329.
func TestLostKeyRecovery(t *testing.T) {
	w := newVWorld(t) // the provider "lost"
	lostVersion := activeVersion(t, w.prov)
	kept := vaulttest.NewKeyProvider(t)
	keptSealer, err := vault.NewSealer(kept)
	if err != nil {
		t.Fatal(err)
	}
	keptVersion := activeVersion(t, kept)
	if keptVersion == lostVersion {
		t.Fatal("two generated keys have one version")
	}

	// Two tenants sealed under the lost version (one with an active and one with a disabled
	// credential), one under the version that is kept.
	lost1, lost2, ok1 := w.d.SeedTenant(t, "lost-one"), w.d.SeedTenant(t, "lost-two"), w.d.SeedTenant(t, "kept-one")
	cLost1 := w.createCredential(t, w.sealer, lost1, "openai", "main", "synthetic-secret-lost-1")
	cLost2 := w.createCredential(t, w.sealer, lost2, "openai", "main", "synthetic-secret-lost-2")
	w.setState(t, lost2, cLost2, "disabled")
	cKept := w.createCredential(t, keptSealer, ok1, "openai", "main", "synthetic-secret-kept-1")
	if n := count(t, w, `SELECT count(*) FROM pandokos.kek_version`); n != 2 {
		t.Fatalf("%d versions registered before the recovery, want 2", n)
	}

	// The script, with its one variable replaced by a quoted literal.
	script, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "deploy", "postgres", "recover-lost-kek.sql"))
	if err != nil {
		t.Fatalf("the recovery script: %v", err)
	}
	src := string(script)
	if !strings.Contains(src, ":'kek_version'") {
		t.Fatal("the script does not use the variable :'kek_version'")
	}
	for _, m := range rePsqlVar.FindAllStringSubmatch(strings.ReplaceAll(src, ":'kek_version'", "''"), -1) {
		t.Errorf("the script uses a psql variable besides kek_version: %q", m[0])
	}
	if _, err := w.d.Super(t).Exec(ctx(t), strings.ReplaceAll(src, ":'kek_version'", "'"+lostVersion+"'")); err != nil {
		t.Fatalf("the script failed: %v", err)
	}

	type credRow struct {
		state, by, reason string
		secretNull        bool
	}
	read := func(id string) credRow {
		var r credRow
		if err := w.d.Super(t).QueryRow(ctx(t), `SELECT state, state_changed_by, state_reason, secret IS NULL FROM pandokos.provider_credential WHERE id = $1::uuid`, id).
			Scan(&r.state, &r.by, &r.reason, &r.secretNull); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for name, id := range map[string]string{"an active credential": cLost1, "a disabled credential": cLost2} {
		r := read(id)
		if r.state != "revoked" || r.by != "platform" || r.reason == "" || !r.secretNull {
			t.Errorf("%s sealed under the lost version: %+v, want revoked by the platform with a reason and no secret", name, r)
		}
	}
	if r := read(cKept); r.state != "active" || r.secretNull {
		t.Errorf("the credential of the other version was touched: %+v", r)
	}
	if n := count(t, w, `SELECT count(*) FROM pandokos.tenant_data_key WHERE kek_version = $1`, lostVersion); n != 0 {
		t.Errorf("%d data keys of the lost version remain", n)
	}
	if n := count(t, w, `SELECT count(*) FROM pandokos.kek_version WHERE id = $1`, lostVersion); n != 0 {
		t.Error("the lost version is still registered")
	}
	if n := count(t, w, `SELECT count(*) FROM pandokos.tenant_data_key WHERE kek_version = $1`, keptVersion); n != 1 {
		t.Errorf("%d data keys of the kept version, want 1", n)
	}
	if n := count(t, w, `SELECT count(*) FROM pandokos.kek_version WHERE id = $1`, keptVersion); n != 1 {
		t.Error("the kept version is not registered")
	}

	// The tenants of the other version still open their credentials.
	logs := &testutil.LogBuffer{}
	opener := w.opener(t, logs, kept, 30*time.Second, 5*time.Minute, nil)
	got, err := open(t, opener, principalFor(ok1, ""), "openai")
	if err != nil || got.Secret.Reveal() != "synthetic-secret-kept-1" || got.ID != cKept {
		t.Errorf("the tenant of the other version: %+v, %v", got, err)
	}
	// An affected tenant cannot open the revoked one, and can register a credential again: the
	// revoked row is deleted, the new key-encryption key wraps a new data key.
	if _, err := open(t, opener, principalFor(lost1, ""), "openai"); err == nil {
		t.Error("a revoked credential opened")
	} else {
		var nu *vault.NotUsableError
		if !errors.As(err, &nu) || nu.State != "revoked" {
			t.Errorf("the revoked credential: %v, want NotUsableError{revoked}", err)
		}
	}
	err = w.admin.WithTenant(ctx(t), lost1, func(c context.Context, tx store.Tx) error {
		_, err := queries.New(tx).DeleteCredential(c, cLost1)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	cNew := w.createCredential(t, keptSealer, lost1, "openai", "main", "synthetic-secret-new-1")
	opener.Flush()
	got, err = open(t, opener, principalFor(lost1, ""), "openai")
	if err != nil || got.Secret.Reveal() != "synthetic-secret-new-1" || got.ID != cNew {
		t.Errorf("the affected tenant after registering again: %+v, %v", got, err)
	}
	if n := count(t, w, `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid AND kek_version = $2`, lost1.String(), keptVersion); n != 1 {
		t.Errorf("the affected tenant has %d data keys under the new version, want 1", n)
	}
	// A start-up check now passes for the key that is left.
	if v, err := vault.Check(ctx(t), w.admin, kept); err != nil || v != keptVersion {
		t.Errorf("Check after the recovery: %q, %v", v, err)
	}
}
