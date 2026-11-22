package apitest

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/testutil"
)

// GoldenRole is one line of internal/authz/testdata/roles.golden.
type GoldenRole struct {
	Name        string
	Scope       string // "tenant" or "platform"
	Members     bool   // a person can hold the role
	Permissions []string
}

// GoldenRoles reads the golden file of the built-in roles, in the order of the file. The file is
// parsed here and nowhere else in a test of the routes: it is the matrix of the exit condition.
func GoldenRoles(tb testing.TB) []GoldenRole {
	tb.Helper()
	f, err := os.Open(filepath.Join(testutil.RepoRoot(tb), "internal", "authz", "testdata", "roles.golden"))
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	var out []GoldenRole
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 4 {
			tb.Fatalf("roles.golden: %q is not four columns", line)
		}
		out = append(out, GoldenRole{Name: cols[0], Scope: cols[1], Members: cols[2] == "yes", Permissions: strings.Fields(cols[3])})
	}
	if err := sc.Err(); err != nil {
		tb.Fatal(err)
	}
	return out
}

// Golden returns the permissions of each built-in role, as the golden file says.
func Golden(tb testing.TB) map[string][]string {
	tb.Helper()
	out := map[string][]string{}
	for _, r := range GoldenRoles(tb) {
		out[r.Name] = r.Permissions
	}
	return out
}

// Refused reports whether the response is the refusal of a missing permission: 403 with the code
// forbidden, and nothing else (not 404, not tenant_suspended, not escalation_refused).
func Refused(r *Response) bool { return r.Status == http.StatusForbidden && r.Problem() == "forbidden" }

// Probe sends the request that the call table gives for the operation, with the resources in p, as
// the identity d.
func Probe(tb testing.TB, d Doer, op adminapi.Operation, p Params) *Response {
	tb.Helper()
	call := CallFor(tb, op, p)
	return d.Do(tb, call.Method, call.Path, call.Body, nil)
}
