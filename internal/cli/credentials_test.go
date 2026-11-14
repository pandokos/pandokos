package cli_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestCredentialCommands is T-311 (with TestEveryOperationHasACommand, T-219): each command of
// sprint 2.1, section 5.9 calls its operation with the right input; create and replace read the
// secret from --secret-file and from nothing else; no output carries it.
func TestCredentialCommands(t *testing.T) {
	secretForms := testutil.SecretForms(fixtures.CanaryCredential)
	secretFile := func(t *testing.T) string {
		t.Helper()
		return testutil.WriteSecret(t, "credential", fixtures.CanaryCredential)
	}
	assertClean := func(t *testing.T, what, out, errs string) {
		t.Helper()
		testutil.AssertAbsent(t, "stdout of "+what, out, secretForms...)
		testutil.AssertAbsent(t, "stderr of "+what, errs, secretForms...)
	}

	t.Run("create", func(t *testing.T) {
		r := newRig(t)
		r.f.out["createCredential"] = &cred
		file := secretFile(t)
		out, errs := mustRun(t, r, "credential", "create", "--tenant", tid, "--provider", "openai", "--secret-file", file,
			"--description", "the main one", "--expires-at", t1.Format(time.RFC3339), "--validate", "main")
		want := adminapi.CreateCredentialInput{Tenant: tid, Body: adminapi.CredentialCreate{
			Provider: "openai", Label: "main", Secret: fixtures.CanaryCredential, Description: "the main one", ExpiresAt: &t1, ValidateAtWrite: true}}
		if got := r.f.only(t, "createCredential"); !reflect.DeepEqual(got, want) {
			t.Errorf("create: the API received %+v", got)
		}
		for _, line := range []string{"id: " + cid, "provider: openai", "label: main", "state: active", "is_default: true", "fingerprint: " + cred.Fingerprint, "hint: wxyz"} {
			if !lineWith(out, line) {
				t.Errorf("the credential is printed as name: value lines; %q is missing:\n%s", line, out)
			}
		}
		assertClean(t, "credential create", out, errs)

		// Without the optional flags nothing optional is sent, and the file may end without a newline.
		r = newRig(t)
		r.f.out["createCredential"] = &cred
		bare := filepath.Join(t.TempDir(), "bare")
		if err := os.WriteFile(bare, []byte(fixtures.CanaryCredential), 0o600); err != nil {
			t.Fatal(err)
		}
		mustRun(t, r, "credential", "create", "--tenant", tid, "--provider", "anthropic", "--secret-file", bare, "second")
		want = adminapi.CreateCredentialInput{Tenant: tid, Body: adminapi.CredentialCreate{Provider: "anthropic", Label: "second", Secret: fixtures.CanaryCredential}}
		if got := r.f.only(t, "createCredential"); !reflect.DeepEqual(got, want) {
			t.Errorf("create without options: %+v", got)
		}

		// -o json prints the metadata, never the secret.
		out, errs = mustRun(t, r, "credential", "create", "--tenant", tid, "--provider", "openai", "--secret-file", file, "-o", "json", "third")
		assertClean(t, "credential create -o json", out, errs)
		if !strings.Contains(out, `"fingerprint"`) {
			t.Errorf("the JSON output lacks the metadata:\n%s", out)
		}
	})

	t.Run("the secret comes from --secret-file and from nothing else", func(t *testing.T) {
		r := newRig(t)
		file := secretFile(t)
		for _, c := range []struct {
			name string
			args []string
			env  []string
		}{
			{"no secret source at all", []string{"credential", "create", "--tenant", tid, "--provider", "openai", "main"}, nil},
			{"a --secret flag", []string{"credential", "create", "--tenant", tid, "--provider", "openai", "--secret", fixtures.CanaryCredential, "main"}, nil},
			{"a --secret flag next to the file", []string{"credential", "create", "--tenant", tid, "--provider", "openai", "--secret-file", file, "--secret", fixtures.CanaryCredential, "main"}, nil},
			{"--secret-stdin", []string{"credential", "create", "--tenant", tid, "--provider", "openai", "--secret-stdin", "main"}, nil},
			{"--key-stdin", []string{"credential", "create", "--tenant", tid, "--provider", "openai", "--key-stdin", "main"}, nil},
			{"a variable instead of the file", []string{"credential", "create", "--tenant", tid, "--provider", "openai", "main"},
				[]string{"PANDOKOS_CLI_SECRET=" + fixtures.CanaryCredential, "PANDOKOS_CLI_SECRET_FILE=" + file, "PANDOKOS_CLI_PROVIDER_SECRET=" + fixtures.CanaryCredential}},
			{"replace with a --secret flag", []string{"credential", "replace", "--tenant", tid, "--secret", fixtures.CanaryCredential, cid}, nil},
			{"replace without a file", []string{"credential", "replace", "--tenant", tid, cid}, nil},
			{"replace with a variable", []string{"credential", "replace", "--tenant", tid, cid}, []string{"PANDOKOS_CLI_SECRET=" + fixtures.CanaryCredential}},
		} {
			code, out, errs := runWith(t, append(r.environ(), c.env...), c.args...)
			if code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q", c.name, code, out, errs)
			}
			assertClean(t, c.name, out, errs)
		}
		if n := len(r.f.calls("createCredential")) + len(r.f.calls("replaceCredentialSecret")); n != 0 {
			t.Errorf("a refused command reached the API %d times", n)
		}

		// A file that cannot serve as a secret is refused before the API is called, and its
		// content is not repeated in the message.
		bad := map[string]string{
			"a control character": "pandokos-canary-credential-7b1e4c9d\x07tail",
			"two lines":           fixtures.CanaryCredential + "\nsecond-line-of-the-file",
			"empty":               "",
		}
		for name, content := range bad {
			p := filepath.Join(t.TempDir(), "bad")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, errs := r.run(t, "credential", "create", "--tenant", tid, "--provider", "openai", "--secret-file", p, "main")
			if code == 0 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, out, errs)
			}
			assertClean(t, name, out, errs)
			testutil.AssertAbsent(t, "stderr for "+name, errs, "second-line-of-the-file", "7b1e4c9d")
		}
		code, _, errs := r.run(t, "credential", "create", "--tenant", tid, "--provider", "openai", "--secret-file", filepath.Join(t.TempDir(), "missing"), "main")
		if code == 0 || errs == "" {
			t.Errorf("a missing file: exit %d", code)
		}
		if n := len(r.f.calls("createCredential")); n != 0 {
			t.Errorf("a refused secret file reached the API %d times", n)
		}
	})

	t.Run("a failure of the API prints no secret", func(t *testing.T) {
		r := newRig(t)
		r.f.fail["createCredential"] = &problemErr{adminapi.Problem{
			Type: "urn:pandokos:problem:credential_rejected", Title: "Credential rejected", Status: 422, Code: adminapi.ProblemCodeCredentialRejected,
			Detail: "The provider did not accept the credential. Nothing was stored.", RequestID: "req422"}}
		code, out, errs := r.run(t, "credential", "create", "--tenant", tid, "--provider", "openai", "--secret-file", secretFile(t), "--validate", "main")
		if code != 1 || out != "" || errs != "error: credential_rejected: The provider did not accept the credential. Nothing was stored. (request req422)\n" {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
		assertClean(t, "a rejected credential", out, errs)
		r.f.fail["replaceCredentialSecret"] = &problemErr{adminapi.Problem{
			Type: "urn:pandokos:problem:validation_unavailable", Title: "Validation unavailable", Status: 502, Code: adminapi.ProblemCodeValidationUnavailable,
			Detail: "The provider could not confirm the credential. Nothing was stored.", RequestID: "req502"}}
		code, out, errs = r.run(t, "credential", "replace", "--tenant", tid, "--secret-file", secretFile(t), "--validate", cid)
		if code != 1 || !strings.HasPrefix(errs, "error: validation_unavailable: ") {
			t.Errorf("exit %d, stderr %q", code, errs)
		}
		assertClean(t, "an unavailable validation", out, errs)
	})

	t.Run("replace", func(t *testing.T) {
		r := newRig(t)
		r.f.out["replaceCredentialSecret"] = &cred
		out, errs := mustRun(t, r, "credential", "replace", "--tenant", tid, "--secret-file", secretFile(t), "--expires-at", t1.Format(time.RFC3339), "--validate", cid)
		want := adminapi.ReplaceCredentialSecretInput{Tenant: tid, Credential: cid, Body: adminapi.CredentialSecret{Secret: fixtures.CanaryCredential, ExpiresAt: &t1, ValidateAtWrite: true}}
		if got := r.f.only(t, "replaceCredentialSecret"); !reflect.DeepEqual(got, want) {
			t.Errorf("replace: %+v", got)
		}
		assertClean(t, "credential replace", out, errs)
	})

	t.Run("list get update delete", func(t *testing.T) {
		r := newRig(t)
		r.f.out["listCredentials"] = &adminapi.CredentialList{Items: []adminapi.Credential{cred}}
		r.f.out["getCredential"] = &cred
		r.f.out["updateCredential"] = &cred
		r.f.out["deleteCredential"] = &cred
		out, _ := mustRun(t, r, "credential", "list", "--tenant", tid, "--state", "active", "--provider", "openai", "--sort", "-created_at", "--limit", "5")
		if got := r.f.only(t, "listCredentials"); !reflect.DeepEqual(got, adminapi.ListCredentialsInput{Tenant: tid, State: adminapi.CredentialStateActive, Provider: "openai", Sort: adminapi.SortMinusCreatedAt, Limit: 5}) {
			t.Errorf("list: %+v", got)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 2 || !strings.Contains(lines[1], "main") || !strings.Contains(lines[1], cid) {
			t.Errorf("a list is a table with a header:\n%s", out)
		}
		mustRun(t, r, "credential", "get", "--tenant", tid, cid)
		if got := r.f.only(t, "getCredential"); !reflect.DeepEqual(got, adminapi.GetCredentialInput{Tenant: tid, Credential: cid}) {
			t.Errorf("get: %+v", got)
		}
		mustRun(t, r, "credential", "update", "--tenant", tid, "--label", "other", "--description", "d2", "--expires-at", t1.Format(time.RFC3339), cid)
		want := adminapi.UpdateCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.CredentialUpdate{Label: adminapi.Some("other"), Description: adminapi.Some("d2"), ExpiresAt: adminapi.Value(t1)}}
		if got := r.f.only(t, "updateCredential"); !reflect.DeepEqual(got, want) {
			t.Errorf("update: %+v", got)
		}
		r.f.got = map[string][]any{}
		mustRun(t, r, "credential", "update", "--tenant", tid, "--no-expires-at", cid)
		want = adminapi.UpdateCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.CredentialUpdate{ExpiresAt: adminapi.Null[time.Time]()}}
		if got := r.f.only(t, "updateCredential"); !reflect.DeepEqual(got, want) {
			t.Errorf("update --no-expires-at: %+v", got)
		}
		// An empty --description is given, and clears the description: the member is on the wire.
		r.f.got = map[string][]any{}
		mustRun(t, r, "credential", "update", "--tenant", tid, "--description", "", cid)
		if got := r.f.only(t, "updateCredential"); got == nil {
			t.Error("update --description \"\": no call")
		}
		if r.f.patchBody != `{"description":""}` {
			t.Errorf("update --description \"\": the body is %s, want {\"description\":\"\"}", r.f.patchBody)
		}
		mustRun(t, r, "credential", "update", "--tenant", tid, "--label", "other", cid)
		if r.f.patchBody != `{"label":"other"}` {
			t.Errorf("update --label: the body is %s: an absent description must stay absent", r.f.patchBody)
		}
		for _, args := range [][]string{
			{"credential", "update", "--tenant", tid, cid}, // changes nothing
			{"credential", "update", "--tenant", tid, "--no-expires-at", "--expires-at", t1.Format(time.RFC3339), cid},
		} {
			n := len(r.f.calls("updateCredential"))
			if code, _, errs := r.run(t, args...); code != 2 || errs == "" || len(r.f.calls("updateCredential")) != n {
				t.Errorf("%v: exit %d, stderr %q", args, code, errs)
			}
		}
		mustRun(t, r, "credential", "delete", "--tenant", tid, cid)
		if got := r.f.only(t, "deleteCredential"); !reflect.DeepEqual(got, adminapi.DeleteCredentialInput{Tenant: tid, Credential: cid}) {
			t.Errorf("delete: %+v", got)
		}
	})

	t.Run("state commands", func(t *testing.T) {
		r := newRig(t)
		for _, id := range []string{"disableCredential", "enableCredential", "setDefaultCredential", "revokeCredential"} {
			r.f.out[id] = &cred
		}
		mustRun(t, r, "credential", "disable", "--tenant", tid, "--reason", "rotating", cid)
		if got := r.f.only(t, "disableCredential"); !reflect.DeepEqual(got, adminapi.DisableCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.Reason{Reason: "rotating"}}) {
			t.Errorf("disable: %+v", got)
		}
		mustRun(t, r, "credential", "enable", "--tenant", tid, cid)
		if got := r.f.only(t, "enableCredential"); !reflect.DeepEqual(got, adminapi.EnableCredentialInput{Tenant: tid, Credential: cid}) {
			t.Errorf("enable: %+v", got)
		}
		mustRun(t, r, "credential", "set-default", "--tenant", tid, "--reason", "preferred", cid)
		if got := r.f.only(t, "setDefaultCredential"); !reflect.DeepEqual(got, adminapi.SetDefaultCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.Reason{Reason: "preferred"}}) {
			t.Errorf("set-default: %+v", got)
		}
		mustRun(t, r, "credential", "revoke", "--tenant", tid, "--reason", "leaked", cid)
		if got := r.f.only(t, "revokeCredential"); !reflect.DeepEqual(got, adminapi.RevokeCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.Reason{Reason: "leaked"}}) {
			t.Errorf("revoke: %+v", got)
		}
		// A credential command takes its credential as an ID and needs one.
		if code, _, errs := r.run(t, "credential", "get", "--tenant", tid); code != 2 || errs == "" {
			t.Errorf("a missing credential: exit %d, stderr %q", code, errs)
		}
	})

	t.Run("the tenant is a name or defaults to the caller's", func(t *testing.T) {
		r := newRig(t)
		r.f.out["listTenants"] = &adminapi.TenantList{Items: []adminapi.Tenant{tenant}}
		r.f.out["listCredentials"] = &adminapi.CredentialList{}
		mustRun(t, r, "credential", "list", "--tenant", "acme")
		if got := r.f.only(t, "listCredentials"); !reflect.DeepEqual(got, adminapi.ListCredentialsInput{Tenant: tid, Limit: 50, Sort: adminapi.SortCreatedAt}) {
			t.Errorf("list with a tenant name: %+v", got)
		}
		r = newRig(t)
		r.f.out["getMe"] = &adminapi.Me{PrincipalType: adminapi.PrincipalTypeServiceAccount, ID: sid, Name: "admin", KeyID: vk, Tenant: &adminapi.TenantRef{ID: tid, Name: "acme", State: adminapi.TenantStateActive}}
		r.f.out["listCredentials"] = &adminapi.CredentialList{}
		mustRun(t, r, "credential", "list")
		if got := r.f.only(t, "listCredentials"); !reflect.DeepEqual(got, adminapi.ListCredentialsInput{Tenant: tid, Limit: 50, Sort: adminapi.SortCreatedAt}) {
			t.Errorf("list with the caller's tenant: %+v", got)
		}
	})

	t.Run("tenant-credential commands", func(t *testing.T) {
		r := newRig(t)
		r.f.out["listTenantCredentials"] = &adminapi.CredentialList{Items: []adminapi.Credential{cred}}
		r.f.out["getTenantCredential"] = &cred
		r.f.out["disableTenantCredential"] = &cred
		r.f.out["revokeTenantCredential"] = &cred
		mustRun(t, r, "tenant-credential", "list", "--tenant", tid, "--state", "disabled", "--provider", "anthropic", "--sort", "created_at", "--limit", "3")
		if got := r.f.only(t, "listTenantCredentials"); !reflect.DeepEqual(got, adminapi.ListTenantCredentialsInput{Tenant: tid, State: adminapi.CredentialStateDisabled, Provider: "anthropic", Sort: adminapi.SortCreatedAt, Limit: 3}) {
			t.Errorf("list: %+v", got)
		}
		mustRun(t, r, "tenant-credential", "get", "--tenant", tid, cid)
		if got := r.f.only(t, "getTenantCredential"); !reflect.DeepEqual(got, adminapi.GetTenantCredentialInput{Tenant: tid, Credential: cid}) {
			t.Errorf("get: %+v", got)
		}
		mustRun(t, r, "tenant-credential", "disable", "--tenant", tid, "--reason", "abuse report", cid)
		if got := r.f.only(t, "disableTenantCredential"); !reflect.DeepEqual(got, adminapi.DisableTenantCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.RequiredReason{Reason: "abuse report"}}) {
			t.Errorf("disable: %+v", got)
		}
		mustRun(t, r, "tenant-credential", "revoke", "--tenant", tid, "--reason", "abuse report", cid)
		if got := r.f.only(t, "revokeTenantCredential"); !reflect.DeepEqual(got, adminapi.RevokeTenantCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.RequiredReason{Reason: "abuse report"}}) {
			t.Errorf("revoke: %+v", got)
		}
		// The reason is mandatory for a platform administrator: a usage error, nothing sent.
		for _, verb := range []string{"disable", "revoke"} {
			code, _, errs := r.run(t, "tenant-credential", verb, "--tenant", tid, cid)
			if code != 2 || !strings.Contains(errs, "reason") {
				t.Errorf("%s without a reason: exit %d, stderr %q", verb, code, errs)
			}
		}
		if len(r.f.calls("disableTenantCredential")) != 1 || len(r.f.calls("revokeTenantCredential")) != 1 {
			t.Error("a platform command without a reason reached the API")
		}
		// A platform command has no way to carry a secret.
		for _, args := range [][]string{
			{"tenant-credential", "disable", "--tenant", tid, "--reason", "x", "--secret", fixtures.CanaryCredential, cid},
			{"tenant-credential", "get", "--tenant", tid, "--secret-file", secretFile(t), cid},
		} {
			if code, _, errs := r.run(t, args...); code != 2 || errs == "" {
				t.Errorf("%v: exit %d, stderr %q", args[:2], code, errs)
			}
		}
	})
}
