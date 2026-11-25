//go:build drill

package drill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The recovery after a lost key (the guide's steps 6.3 and 6.4) and what a tenant does afterwards.

// RecoverLostKey runs step 6.3 or 6.4: the recovery script for the version, a new key, and starts
// serve on it; it returns the script's output. When serve still refuses (another version is lost: the
// caller runs the script for it next) the restore stays without a process.
func (r *Restored) RecoverLostKey(version string) string {
	r.W.T.Helper()
	w := r.W
	cmd, step := Cmd63, Step63
	if w.service != nil {
		cmd, step = Cmd63s, Step63s
	}
	var out string
	w.Step(step, func() {
		out = w.run(r.service, withDatabase(strings.NewReplacer("<version>", version, "<transit-version>", version).Replace(cmd), w.db))
	})
	if r.Proc != nil {
		return out // serve runs: the script is the operator's act on a live installation
	}
	// A new key. With a key file: pandokos kek generate; with a key service: a key in the service when
	// it has none (the key was deleted there); a service that was restored or installed again has one.
	w.Step(Step64, func() {
		if w.service != nil {
			if !r.svc.keyExists() {
				r.svc.createKey()
			}
			return
		}
		if r.newKey == nil {
			r.newKey = w.newKey(fmt.Sprintf("kek-new-%d", len(w.keys)+1))
		}
		r.conf = keyConf{files: []string{r.newKey.File}}
	})
	r.Proc, r.ServeCode, r.ServeLog = w.startServe(w.env(r.addr, r.conf))
	if r.Proc == nil {
		w.T.Logf("serve still refuses after the script for %s (exit %d): %s", version, r.ServeCode, w.redact(startupError(r.ServeLog)))
	}
	return out
}

// CredentialState is the state of a tenant's provider credential ("active", "revoked").
func (r *Restored) CredentialState(tenant string) string {
	r.W.T.Helper()
	tn := r.W.Tenants[tenant]
	out := r.W.mustCLIOn(r.mustProc(), tn.AdminKeyFile, "credential", "list", "--tenant", tn.ID, "--all", "-o", "json")
	var doc struct {
		Items []struct {
			Label string `json:"label"`
			State string `json:"state"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		r.W.T.Fatalf("credential list: %v", err)
	}
	for _, i := range doc.Items {
		if i.Label == "main" {
			return i.State
		}
	}
	return ""
}

// credentialID is the ID of the tenant's credential named main.
func (r *Restored) credentialID(tenant string) string {
	r.W.T.Helper()
	tn := r.W.Tenants[tenant]
	out := r.W.mustCLIOn(r.mustProc(), tn.AdminKeyFile, "credential", "list", "--tenant", tn.ID, "--all", "-o", "json")
	var doc struct {
		Items []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		r.W.T.Fatalf("credential list: %v", err)
	}
	for _, i := range doc.Items {
		if i.Label == "main" {
			return i.ID
		}
	}
	r.W.T.Fatalf("tenant %s has no credential named main", tenant)
	return ""
}

// EnterCredential makes the tenant enter a provider credential again (a new canary, accepted by the
// provider) and returns its value: the revoked credential is deleted and a new one created, which a
// new data key seals under the new key.
func (r *Restored) EnterCredential(tenant string) string {
	r.W.T.Helper()
	w := r.W
	tn := w.Tenants[tenant]
	proc := r.mustProc()
	fresh := w.reg("synthetic-drill-credential-" + tenant + "-again-" + randomHex(w.T, 6))
	w.accepted = append(w.accepted, fresh)
	w.Upstream.Accept(w.accepted...)
	file := filepath.Join(w.dir, "secrets", tenant+"-provider-secret-again")
	if err := os.WriteFile(file, []byte(fresh), 0o600); err != nil {
		w.T.Fatal(err)
	}
	if code, out, errs := proc.CLI(w.T, tn.AdminKeyFile, "credential", "delete", "--tenant", tn.ID, r.credentialID(tenant)); code != 0 {
		w.T.Fatalf("credential delete: exit %d\n%s%s", code, w.redact(tail(out)), w.redact(tail(errs)))
	}
	w.mustCLIOn(proc, tn.AdminKeyFile, "credential", "create", "--tenant", tn.ID, "--provider", "openai", "--secret-file", file, "main")
	tn.Credential = fresh
	return fresh
}

// RotateDestinationSecret runs event-destination rotate-secret for the tenant's destination and
// returns the new secret.
func (r *Restored) RotateDestinationSecret(tenant string) string {
	r.W.T.Helper()
	w := r.W
	tn := w.Tenants[tenant]
	out := w.mustCLIOn(r.mustProc(), tn.AdminKeyFile, "event-destination", "rotate-secret", "--tenant", tn.ID, "--previous-valid-for", "0s", tn.Destination)
	secret := reSecret.FindString(out)
	if secret == "" {
		w.T.Fatalf("event-destination rotate-secret printed no signing secret")
	}
	tn.Secret = w.reg(secret)
	return secret
}

// KekCheck is "pandokos kek check": its exit code and output.
func (r *Restored) KekCheck() (int, string) {
	r.W.T.Helper()
	code, out, errs := r.W.installation(r.W.env(r.addr, r.conf)).Run(r.W.T, nil, "kek", "check")
	return code, out + errs
}
