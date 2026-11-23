package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/testutil"
)

// The helpers of the end-to-end tests of sprint 6.3: tenants that are used, deleted and re-keyed through the
// binary and its command line.

// lifeTenant is a tenant made through the CLI that has been used: an inference key, a credential of the
// provider and one call that reached the fake.
type lifeTenant struct {
	id, name  string
	adminFile string
	adminKey  string
	inference string
	saName    string
	keyID     string
}

// newLifeTenant makes a tenant named name with an owner, an inference service account named saName, a
// credential equal to fixtures.CanaryCredential and one successful call.
func newLifeTenant(t *testing.T, p *proc, platform, name, saName string) lifeTenant {
	t.Helper()
	id, admin := p.newTenant(t, platform, name)
	p.mustCLI(t, admin, "service-account", "create", "--tenant", id, "--role", "inference", saName)
	list := p.mustCLI(t, admin, "service-account", "list", "--tenant", id, "-o", "json")
	var sas struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(list), &sas); err != nil {
		t.Fatal(err)
	}
	var sa string
	for _, i := range sas.Items {
		if i.Name == saName {
			sa = i.ID
		}
	}
	if sa == "" {
		t.Fatalf("no service account %s in %s", saName, list)
	}
	keyFile := fmt.Sprintf("%s/%s.inference.key", t.TempDir(), name)
	out := p.mustCLI(t, admin, "key", "create", "--tenant", id, "--service-account", sa, "--key-out", keyFile)
	keyID := regexp.MustCompile(`(?m)^(?:key )?id: (t[a-z2-7]{16})$`).FindStringSubmatch(out)
	p.track(id, sa)
	if keyID != nil {
		p.track(keyID[1])
	}
	p.prepareTenant(t, admin)
	lt := lifeTenant{id: id, name: name, adminFile: admin, adminKey: readKeyFile(t, admin), inference: readKeyFile(t, keyFile), saName: saName}
	if keyID != nil {
		lt.keyID = keyID[1]
	}
	if status, text := p.chatCall(t, lt.inference, fakeprovider.ModelBasic, false); status != 200 {
		t.Fatalf("a call of %s: %d\n%s", name, status, text)
	}
	return lt
}

func (l lifeTenant) tenantID(t *testing.T) tenant.ID {
	t.Helper()
	id, err := tenant.ParseID(l.id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// ok reports whether a call with the inference key of the tenant is let through to the fake.
func (l lifeTenant) ok(t *testing.T, p *proc) bool {
	t.Helper()
	status, _ := p.chatCall(t, l.inference, fakeprovider.ModelBasic, false)
	return status == 200
}

// restart starts another process on the database of p, with the key files of the options, and the platform
// key of p (the bootstrap key lives in the database).
func restart(t *testing.T, p *proc, o opts) *proc {
	t.Helper()
	o.db, o.noMigrate = p.db, true
	if o.up == nil {
		o.up = p.up
	}
	if o.level == "" {
		o.level = "info"
	}
	n := startBinary(t, o)
	n.platformFile, n.upReady, n.credN, n.tenants = p.platformFile, true, p.credN, p.tenants
	n.ids = append(n.ids, p.ids...)
	return n
}

// waitDeleted waits for the deletion of a tenant to complete. The settling time of two minutes is
// passed by ageing it (dbtest.AgeDeletion) each time the test looks.
func waitDeleted(t *testing.T, d *dbtest.Database, id tenant.ID) {
	t.Helper()
	testutil.Eventually(t, 120*time.Second, "the deletion of the tenant to complete", func() bool {
		d.AgeDeletion(t, id)
		return d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid AND state = 'completed'`, id.String()) == 1
	})
}

// cliJSON runs a command with -o json and decodes the object it prints.
func cliJSON(t *testing.T, p *proc, keyFile string, args ...string) map[string]any {
	t.Helper()
	out := p.mustCLI(t, keyFile, append(args, "-o", "json")...)
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return v
}

// kekStates returns the versions of the key-encryption key by state, through the CLI.
func kekStates(t *testing.T, p *proc, platform string) map[string]string {
	t.Helper()
	v := cliJSON(t, p, platform, "kek-version", "list")
	out := map[string]string{}
	items, _ := v["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		out[fmt.Sprint(m["id"])] = fmt.Sprint(m["state"])
	}
	return out
}

// statusOf calls the admin listener with a key and returns the status.
func statusOf(t *testing.T, p *proc, path, key string) int {
	t.Helper()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+key)
	status, _ := exchangeWith(t, p.client, "GET", p.adminURL(path), hdr, nil)
	return status
}

// principal is a caller of the tenant for the opener of the vault: an inference service account.
func (l lifeTenant) principal(t *testing.T) authz.Principal {
	t.Helper()
	return authz.Principal{Kind: authz.KindServiceAccount, ID: "0192f0f0-2222-7000-8000-000000000001", Name: "caller", KeyID: "tabcdefghijklmnop",
		TenantID: l.tenantID(t), TenantName: l.name, TenantState: tenant.StateActive, Roles: []string{authz.RoleInference}}
}
