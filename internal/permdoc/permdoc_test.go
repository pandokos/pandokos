package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
)

// The nine operations with a required reason (sprint 4.3, section 5.8). The test sets the flag
// itself, so that it does not depend on which step of the plan made the document declare it.
var nine = []string{"suspendTenant", "reactivateTenant", "disableTenantCredential", "revokeTenantCredential", "openSupportAction",
	"resetUserPassword", "resetMemberPassword", "deactivateRole", "activateRole"}

func operations() []adminapi.Operation {
	ops := append([]adminapi.Operation(nil), adminapi.Operations...)
	for i := range ops {
		ops[i].ReasonRequired = false
		for _, id := range nine {
			if ops[i].ID == id {
				ops[i].ReasonRequired = true
			}
		}
	}
	return ops
}

// table is a markdown table: the cells of its header and of each row, trimmed.
type table struct {
	header []string
	rows   [][]string
}

func cells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(strings.TrimSuffix(line, "|"), "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// tables returns every table of the markdown text, in order, with the heading that precedes it.
func tables(text string) (out []table, headings []string) {
	var cur *table
	heading := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "#"):
			heading = strings.TrimSpace(strings.TrimLeft(line, "#"))
			cur = nil
		case strings.HasPrefix(strings.TrimSpace(line), "|"):
			c := cells(line)
			if cur == nil {
				out = append(out, table{header: c})
				headings = append(headings, heading)
				cur = &out[len(out)-1]
				continue
			}
			if isSeparator(c) {
				continue
			}
			cur.rows = append(cur.rows, c)
		default:
			cur = nil
		}
	}
	return out, headings
}

func isSeparator(c []string) bool {
	for _, x := range c {
		if strings.Trim(x, "-: ") != "" {
			return false
		}
	}
	return true
}

func unquote(s string) string { return strings.Trim(s, "` ") }

var rePermission = regexp.MustCompile("[a-z_]+:[a-z_]+")

func marked(cell string) bool {
	c := strings.ToLower(strings.TrimSpace(cell))
	return c != "" && c != "-" && c != "no" && c != "—"
}

// TestGenerate is T-887.
func TestGenerate(t *testing.T) {
	perms, roles, ops := authz.Catalogue(), authz.Roles(), operations()
	out := Generate(perms, roles, ops)
	if len(bytes.TrimSpace(out)) == 0 {
		t.Fatal("Generate returns nothing")
	}

	t.Run("deterministic, whatever the order of the input", func(t *testing.T) {
		if again := Generate(perms, roles, ops); !bytes.Equal(out, again) {
			t.Error("two calls give two outputs")
		}
		ro := append([]adminapi.Operation(nil), ops...)
		for i, j := 0, len(ro)-1; i < j; i, j = i+1, j-1 {
			ro[i], ro[j] = ro[j], ro[i]
		}
		if reversed := Generate(perms, roles, ro); !bytes.Equal(out, reversed) {
			t.Error("the output depends on the order of the operations (the order is fixed: catalogue order, role order of Roles(), operations by ID)")
		}
		if !bytes.HasSuffix(out, []byte("\n")) || bytes.Contains(out, []byte("\r")) {
			t.Error("the output does not end with a newline or has carriage returns")
		}
		if bytes.Contains(out, []byte(beginMarker)) || bytes.Contains(out, []byte(endMarker)) {
			t.Error("the output contains the markers: they are written around it")
		}
	})

	tabs, headings := tables(string(out))

	t.Run("every permission with its scope, attributes, operations and holders", func(t *testing.T) {
		var tab *table
		for i := range tabs {
			if len(tabs[i].header) >= 6 && strings.EqualFold(tabs[i].header[0], "Permission") {
				tab = &tabs[i]
			}
		}
		if tab == nil {
			t.Fatalf("no permission table (columns: permission, scope, attributes, description, operations, roles); tables found: %v", headings)
		}
		rows := map[string][]string{}
		var order []string
		for _, r := range tab.rows {
			if len(r) < 6 {
				t.Errorf("a row with %d cells: %v", len(r), r)
				continue
			}
			rows[unquote(r[0])] = r
			order = append(order, unquote(r[0]))
		}
		var wantOrder []string
		for _, p := range perms {
			wantOrder = append(wantOrder, p.Name)
		}
		if !reflect.DeepEqual(order, wantOrder) {
			t.Errorf("the rows are %v, want the catalogue's order", order)
		}
		for _, p := range perms {
			r, ok := rows[p.Name]
			if !ok {
				t.Errorf("the table lacks %s", p.Name)
				continue
			}
			if !strings.EqualFold(r[1], string(p.Scope)) {
				t.Errorf("%s: scope %q, want %s", p.Name, r[1], p.Scope)
			}
			attrs := strings.ToLower(r[2])
			if got, want := strings.Contains(attrs, "in tenant"), p.InTenant; got != want {
				t.Errorf("%s: attribute in tenant %v (%q), want %v", p.Name, got, r[2], want)
			}
			hasSupport := false
			for _, a := range strings.Split(attrs, ",") {
				hasSupport = hasSupport || strings.TrimSpace(a) == "support"
			}
			if hasSupport != p.Support {
				t.Errorf("%s: attribute support %v (%q), want %v", p.Name, hasSupport, r[2], p.Support)
			}
			if !strings.Contains(r[3], p.Description) {
				t.Errorf("%s: the description is not the catalogue's: %q", p.Name, r[3])
			}
			// The operations that need it, by ID.
			var wantOps []string
			for _, op := range ops {
				if op.Permission == p.Name {
					wantOps = append(wantOps, op.ID)
				}
			}
			sort.Strings(wantOps)
			// Routes outside the OpenAPI document ("GET /metrics (…)") are listed with the IDs.
			cell := regexp.MustCompile("`[A-Z]+ /[^`]*`").ReplaceAllString(r[4], "")
			gotOps := regexp.MustCompile(`[a-z][A-Za-z]+`).FindAllString(cell, -1)
			if !reflect.DeepEqual(nilIfEmpty(gotOps), nilIfEmpty(wantOps)) {
				t.Errorf("%s: operations %v, want %v (by ID)", p.Name, gotOps, wantOps)
			}
			// The built-in roles that hold it, in the order of Roles().
			var wantRoles []string
			for _, role := range roles {
				for _, rp := range role.Permissions {
					if rp == p.Name {
						wantRoles = append(wantRoles, role.Name)
					}
				}
			}
			var gotRoles []string
			for _, role := range roles {
				if regexp.MustCompile("(^|[^a-z-])" + regexp.QuoteMeta(role.Name) + "([^a-z-]|$)").MatchString(r[5]) {
					gotRoles = append(gotRoles, role.Name)
				}
			}
			if !reflect.DeepEqual(nilIfEmpty(gotRoles), nilIfEmpty(wantRoles)) {
				t.Errorf("%s: held by %v, want %v", p.Name, gotRoles, wantRoles)
			}
		}
	})

	golden := readGolden(t)
	t.Run("every role's line equals the golden file", func(t *testing.T) {
		var tab *table
		for i := range tabs {
			if len(tabs[i].header) >= 5 && strings.EqualFold(tabs[i].header[0], "Role") {
				tab = &tabs[i]
			}
		}
		if tab == nil {
			t.Fatal("no role table (columns: role, scope, members, description, permissions)")
		}
		if len(tab.rows) != 10 {
			t.Errorf("%d roles in the table, want ten", len(tab.rows))
		}
		for _, r := range tab.rows {
			name := unquote(r[0])
			g, ok := golden[name]
			if !ok {
				t.Errorf("the role %q is not in the golden file", name)
				continue
			}
			if !strings.EqualFold(r[1], g.scope) {
				t.Errorf("%s: scope %q, golden %s", name, r[1], g.scope)
			}
			if got := strings.EqualFold(strings.TrimSpace(r[2]), "yes"); got != g.members {
				t.Errorf("%s: members %q, golden %v", name, r[2], g.members)
			}
			got := rePermission.FindAllString(r[4], -1)
			sort.Strings(got)
			if !reflect.DeepEqual(got, g.permissions) {
				t.Errorf("%s: permissions %v, the golden file says %v", name, got, g.permissions)
			}
		}
	})

	t.Run("the role-by-permission matrix, for the tenant and for the platform", func(t *testing.T) {
		for _, scope := range []string{"tenant", "platform"} {
			var tab *table
			for i := range tabs {
				if matrixOf(tabs[i], scope, golden) {
					tab = &tabs[i]
				}
			}
			if tab == nil {
				t.Errorf("no matrix of the %s roles (one column per role, one row per permission)", scope)
				continue
			}
			col := map[string]int{}
			for i, h := range tab.header {
				col[unquote(h)] = i
			}
			rows := map[string][]string{}
			for _, r := range tab.rows {
				rows[unquote(r[0])] = r
			}
			for _, p := range perms {
				if string(p.Scope) != scope {
					continue
				}
				r, ok := rows[p.Name]
				if !ok {
					t.Errorf("the %s matrix lacks the row of %s", scope, p.Name)
					continue
				}
				for name, g := range golden {
					if g.scope != scope {
						continue
					}
					i, ok := col[name]
					if !ok || i >= len(r) {
						t.Errorf("the %s matrix lacks the column of %s", scope, name)
						continue
					}
					held := false
					for _, hp := range g.permissions {
						held = held || hp == p.Name
					}
					if marked(r[i]) != held {
						t.Errorf("the %s matrix: %s x %s is %q, the golden file says held=%v", scope, name, p.Name, r[i], held)
					}
				}
			}
		}
	})

	t.Run("the operations that require a reason", func(t *testing.T) {
		text := string(out)
		idx := regexp.MustCompile(`(?im)^#+ .*reason.*$`).FindStringIndex(text)
		if idx == nil {
			t.Fatal("no heading that names the reason")
		}
		section := text[idx[1]:]
		if next := regexp.MustCompile(`(?m)^#`).FindStringIndex(section); next != nil {
			section = section[:next[0]]
		}
		var got []string
		for _, op := range ops {
			if regexp.MustCompile("(^|[^A-Za-z])" + op.ID + "([^A-Za-z]|$)").MatchString(section) {
				got = append(got, op.ID)
			}
		}
		want := append([]string(nil), nine...)
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the section lists %v, want the nine %v", got, want)
		}
	})
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

type goldenRole struct {
	scope       string
	members     bool
	permissions []string
}

func readGolden(t *testing.T) map[string]goldenRole {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "authz", "testdata", "roles.golden"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]goldenRole{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		c := strings.Split(line, "\t")
		if len(c) != 4 {
			t.Fatalf("roles.golden: %q", line)
		}
		out[c[0]] = goldenRole{c[1], c[2] == "yes", strings.Fields(c[3])}
	}
	return out
}

// matrixOf reports whether the table is the matrix of the roles of the scope: its header names
// every role of the scope and none of the other.
func matrixOf(tab table, scope string, golden map[string]goldenRole) bool {
	have := map[string]bool{}
	for _, h := range tab.header {
		have[unquote(h)] = true
	}
	any := false
	for name, g := range golden {
		if g.scope == scope && !have[name] {
			return false
		}
		if g.scope != scope && have[name] {
			return false
		}
		any = true
	}
	return any
}
