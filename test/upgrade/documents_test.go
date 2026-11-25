//go:build upgrade

package upgrade

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPreviousDocumentsApply is T-1135: the platform and tenant documents that the previous release's
// export writes are valid for the new apply and plan nothing on the upgraded installation (a
// document written by the previous minor is accepted by the new one: the document is v1 and grows by
// addition). What the installation holds that the old document does not know is reported as not in the
// document, never changed.
func TestPreviousDocumentsApply(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	dir := t.TempDir()
	platformDoc := filepath.Join(dir, "platform.yaml")
	w.cli(w.admin, "export", "--platform", "--out", platformDoc)
	tenantDocs := map[string]string{}
	for name, tn := range w.tenants {
		tenantDocs[name] = filepath.Join(dir, name+".yaml")
		w.cli(tn.Admin, "export", "--tenant", tn.ID, "--out", tenantDocs[name])
	}

	_, _, p := quietUpgrade(t, w)
	check := func(what, key string, args ...string) {
		t.Helper()
		for _, mode := range [][]string{{"--dry-run"}, nil} {
			code, out, errs := p.CLI(t, key, append(append([]string{"apply"}, args...), mode...)...)
			if code != 0 {
				t.Errorf("%s (%v): exit %d\n%s%s", what, mode, code, out, errs)
				continue
			}
			if !strings.Contains(out, "0 changes, 0 conflicts") {
				t.Errorf("%s (%v) plans something on the upgraded installation:\n%s", what, mode, out)
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "create ") || strings.HasPrefix(line, "update ") || strings.HasPrefix(line, "conflict ") {
					t.Errorf("%s (%v): the plan has the line %q", what, mode, line)
				}
			}
		}
	}
	check("the platform document of the previous release", w.admin, "--platform", "--file", platformDoc)
	for name, tn := range w.tenants {
		check("the document of tenant "+name+" of the previous release", tn.Admin, "--tenant", tn.ID, "--file", tenantDocs[name])
	}
}
