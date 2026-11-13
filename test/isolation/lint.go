package isolation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	schema         = "pandokos"
	ownerRole      = "pandokos_owner"
	canonicalCheck = "(tenant_id = ( SELECT pandokos.current_tenant_id() AS current_tenant_id))"
)

var (
	runtimeRoleNames = []string{"pandokos_gateway", "pandokos_admin", "pandokos_worker"}
	privilegeRoles   = []string{ownerRole, "pandokos_gateway", "pandokos_admin", "pandokos_worker"}
)

type relation struct {
	name, kind, owner string
	rls, forced       bool
	options           []string // pg_class.reloptions
}

type policyRow struct {
	name, cmd       string
	permissive      bool
	roles           []string
	using, withChek string
}

type constraint struct {
	name, kind string
	cols       []string
	refTable   string
	refCols    []string
}

// Lint reads the system catalogues of schema pandokos through the connection (a superuser's, so
// that it can read every privilege) and returns one finding per violation of the rules L-01 to
// L-16 of sprint 1.2, section 6.1 and L-19 of sprint 2.1 (nineteen rules), sorted by rule and object. A table is tenant-scoped unless
// spec.Global lists it.
func Lint(ctx context.Context, conn *pgx.Conn, spec Spec) ([]Finding, error) {
	var findings []Finding
	add := func(rule, object, format string, args ...any) {
		findings = append(findings, Finding{Rule: rule, Object: schema + "." + object, Detail: fmt.Sprintf(format, args...)})
	}

	rels, err := readRelations(ctx, conn)
	if err != nil {
		return nil, err
	}
	tables := map[string]bool{}
	for _, r := range rels {
		if r.kind == "r" || r.kind == "p" {
			tables[r.name] = true
		}
	}
	scoped := func(name string) bool {
		_, global := spec.Global[name]
		return tables[name] && !global
	}
	allowedViews := map[string]bool{}
	for _, v := range spec.Views {
		allowedViews[v] = true
	}

	for _, r := range rels {
		if r.owner != ownerRole {
			add("L-01", r.name, "owned by %s, not by %s", r.owner, ownerRole)
		}
		switch r.kind {
		case "v":
			if !allowedViews[r.name] {
				add("L-02", r.name, "a view that is not listed in Spec.Views")
			}
			if !contains(r.options, "security_invoker=true") {
				add("L-19", r.name, "a view without security_invoker=true")
			}
		case "m":
			add("L-02", r.name, "a materialized view; they are never allowed")
		case "f":
			add("L-02", r.name, "a foreign table; they are never allowed")
		}
		if global, ok := spec.Global[r.name]; ok && global.Restricted {
			pols, err := readPolicies(ctx, conn, r.name)
			if err != nil {
				return nil, err
			}
			if !r.rls {
				add("L-13", r.name, "row-level security is not enabled")
			}
			if !r.forced {
				add("L-13", r.name, "row-level security is not forced")
			}
			if len(pols) == 0 {
				add("L-13", r.name, "no policy")
			}
		}
		if r.kind != "r" && r.kind != "p" || !scoped(r.name) {
			continue
		}

		// Tenant-scoped table.
		if !r.rls {
			add("L-05", r.name, "row-level security is not enabled")
		}
		if !r.forced {
			add("L-06", r.name, "row-level security is not forced")
		}
		var notNull bool
		var typ string
		err := conn.QueryRow(ctx, `SELECT a.attnotnull, pg_catalog.format_type(a.atttypid, a.atttypmod)
  FROM pg_catalog.pg_attribute a
 WHERE a.attrelid = $1::regclass AND a.attname = 'tenant_id' AND NOT a.attisdropped`, qualified(r.name)).Scan(&notNull, &typ)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			add("L-03", r.name, "no column tenant_id")
		case err != nil:
			return nil, err
		case typ != "uuid":
			add("L-03", r.name, "tenant_id has type %s, not uuid", typ)
		case !notNull:
			add("L-03", r.name, "tenant_id is nullable")
		}

		cons, err := readConstraints(ctx, conn, r.name)
		if err != nil {
			return nil, err
		}
		hasTenantFK, pkHasTenant := false, false
		for _, c := range cons {
			switch c.kind {
			case "p":
				pkHasTenant = contains(c.cols, "tenant_id")
			case "f":
				if c.refTable == "tenant" && equal(c.cols, []string{"tenant_id"}) && equal(c.refCols, []string{"id"}) {
					hasTenantFK = true
				}
				if scoped(c.refTable) && !pairsTenant(c) {
					add("L-11", r.name, "foreign key %s to %s does not pair tenant_id with tenant_id", c.name, c.refTable)
				}
			}
		}
		if !hasTenantFK {
			add("L-04", r.name, "no foreign key from (tenant_id) to tenant (id)")
		}
		if !pkHasTenant {
			add("L-10", r.name, "the primary key does not include tenant_id")
		}

		pols, err := readPolicies(ctx, conn, r.name)
		if err != nil {
			return nil, err
		}
		found := false
		for _, p := range pols {
			if p.name != "tenant_isolation" {
				add("L-09", r.name, "policy %s besides tenant_isolation", p.name)
				continue
			}
			found = true
			switch {
			case p.cmd != "*":
				add("L-08", r.name, "tenant_isolation is not for all commands (polcmd %q)", p.cmd)
			case !p.permissive:
				add("L-08", r.name, "tenant_isolation is not permissive")
			case !equal(p.roles, []string{"public"}):
				add("L-08", r.name, "tenant_isolation is for %v, not for PUBLIC", p.roles)
			case collapse(p.using) != canonicalCheck:
				add("L-08", r.name, "tenant_isolation USING is %q", collapse(p.using))
			case collapse(p.withChek) != canonicalCheck:
				add("L-08", r.name, "tenant_isolation WITH CHECK is %q", collapse(p.withChek))
			}
		}
		if !found {
			add("L-07", r.name, "no policy named tenant_isolation")
		}
		if _, ok := spec.Fixtures[r.name]; !ok {
			add("L-16", r.name, "no entry in Spec.Fixtures: the table has no isolation tests")
		}
	}

	if err := lintPrivileges(ctx, conn, spec, rels, add); err != nil {
		return nil, err
	}
	if err := lintFunctions(ctx, conn, spec, add); err != nil {
		return nil, err
	}
	if err := lintRoles(ctx, conn, add); err != nil {
		return nil, err
	}
	if err := lintAppendOnly(ctx, conn, spec, add); err != nil {
		return nil, err
	}

	for name := range spec.Global {
		if !tables[name] {
			add("L-16", name, "Spec.Global names a table that does not exist")
		}
	}
	for name := range spec.Grants {
		if !tables[name] && !isRelation(rels, name) {
			add("L-16", name, "Spec.Grants names a table that does not exist")
		}
	}
	for name := range spec.Fixtures {
		if !tables[name] {
			add("L-16", name, "Spec.Fixtures names a table that does not exist")
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Rule != findings[j].Rule {
			return findings[i].Rule < findings[j].Rule
		}
		if findings[i].Object != findings[j].Object {
			return findings[i].Object < findings[j].Object
		}
		return findings[i].Detail < findings[j].Detail
	})
	return findings, nil
}

func qualified(table string) string { return pgx.Identifier{schema, table}.Sanitize() }

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isRelation(rels []relation, name string) bool {
	for _, r := range rels {
		if r.name == name {
			return true
		}
	}
	return false
}

// pairsTenant reports whether the foreign key pairs tenant_id with tenant_id.
func pairsTenant(c constraint) bool {
	for i := range c.cols {
		if c.cols[i] == "tenant_id" && i < len(c.refCols) && c.refCols[i] == "tenant_id" {
			return true
		}
	}
	return false
}

func readRelations(ctx context.Context, conn *pgx.Conn) ([]relation, error) {
	rows, err := conn.Query(ctx, `SELECT c.relname::text, c.relkind::text, pg_catalog.pg_get_userbyid(c.relowner)::text, c.relrowsecurity, c.relforcerowsecurity, COALESCE(c.reloptions, '{}')::text[]
  FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition
 ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (relation, error) {
		var x relation
		err := r.Scan(&x.name, &x.kind, &x.owner, &x.rls, &x.forced, &x.options)
		return x, err
	})
}

func readPolicies(ctx context.Context, conn *pgx.Conn, table string) ([]policyRow, error) {
	rows, err := conn.Query(ctx, `SELECT p.polname::text, p.polcmd::text, p.polpermissive,
       ARRAY(SELECT CASE WHEN r = 0 THEN 'public' ELSE pg_catalog.pg_get_userbyid(r)::text END
               FROM unnest(p.polroles) AS r ORDER BY 1)::text[],
       COALESCE(pg_catalog.pg_get_expr(p.polqual, p.polrelid), ''),
       COALESCE(pg_catalog.pg_get_expr(p.polwithcheck, p.polrelid), '')
  FROM pg_catalog.pg_policy p
 WHERE p.polrelid = $1::regclass
 ORDER BY 1`, qualified(table))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (policyRow, error) {
		var x policyRow
		err := r.Scan(&x.name, &x.cmd, &x.permissive, &x.roles, &x.using, &x.withChek)
		return x, err
	})
}

func readConstraints(ctx context.Context, conn *pgx.Conn, table string) ([]constraint, error) {
	rows, err := conn.Query(ctx, `SELECT con.conname::text, con.contype::text,
       ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
               JOIN pg_catalog.pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
              ORDER BY k.ord)::text[],
       COALESCE(fc.relname::text, ''),
       ARRAY(SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
               JOIN pg_catalog.pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
              ORDER BY k.ord)::text[]
  FROM pg_catalog.pg_constraint con LEFT JOIN pg_catalog.pg_class fc ON fc.oid = con.confrelid
 WHERE con.conrelid = $1::regclass AND con.contype IN ('p', 'u', 'f')
 ORDER BY 1`, qualified(table))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (constraint, error) {
		var x constraint
		err := r.Scan(&x.name, &x.kind, &x.cols, &x.refTable, &x.refCols)
		return x, err
	})
}

// lintPrivileges is L-12: the table-level and column-level privileges of the runtime roles and
// PUBLIC equal the matrix of the spec, and nobody else holds any.
func lintPrivileges(ctx context.Context, conn *pgx.Conn, spec Spec, rels []relation, add func(rule, object, format string, args ...any)) error {
	held := map[string]map[string][]string{} // table -> grantee -> privileges

	rows, err := conn.Query(ctx, `SELECT c.relname::text, CASE WHEN a.grantee = 0 THEN 'public' ELSE pg_catalog.pg_get_userbyid(a.grantee)::text END, a.privilege_type
  FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace,
       LATERAL aclexplode(COALESCE(c.relacl, acldefault('r', c.relowner))) AS a
 WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
 ORDER BY 1, 2, 3`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var table, grantee, priv string
		if err := rows.Scan(&table, &grantee, &priv); err != nil {
			return err
		}
		if held[table] == nil {
			held[table] = map[string][]string{}
		}
		held[table][grantee] = append(held[table][grantee], priv)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = conn.Query(ctx, `SELECT c.relname::text, a.privilege_type,
       CASE WHEN a.grantee = 0 THEN 'public' ELSE pg_catalog.pg_get_userbyid(a.grantee)::text END,
       string_agg(att.attname::text, ',' ORDER BY att.attname)
  FROM pg_catalog.pg_attribute att
  JOIN pg_catalog.pg_class c ON c.oid = att.attrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace,
       LATERAL aclexplode(att.attacl) AS a
 WHERE n.nspname = 'pandokos' AND att.attnum > 0 AND NOT att.attisdropped
 GROUP BY 1, 2, 3
 ORDER BY 1, 3, 2`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var table, priv, grantee, cols string
		if err := rows.Scan(&table, &priv, &grantee, &cols); err != nil {
			return err
		}
		if held[table] == nil {
			held[table] = map[string][]string{}
		}
		held[table][grantee] = append(held[table][grantee], priv+"("+cols+")")
	}
	if err := rows.Err(); err != nil {
		return err
	}

	checked := append(append([]string{}, runtimeRoleNames...), "public")
	for _, r := range rels {
		for grantee, privs := range held[r.name] {
			if grantee != ownerRole && !contains(checked, grantee) {
				add("L-12", r.name, "%s holds privileges (%s): only the runtime roles and the owner may", grantee, strings.Join(privs, ", "))
			}
		}
		for _, grantee := range checked {
			got := append([]string(nil), held[r.name][grantee]...)
			want := append([]string(nil), spec.Grants[r.name][grantee]...)
			sort.Strings(got)
			sort.Strings(want)
			if !equal(got, want) {
				add("L-12", r.name, "%s holds [%s], the matrix says [%s]", grantee, strings.Join(got, ", "), strings.Join(want, ", "))
			}
		}
	}
	return nil
}

// lintFunctions is L-14.
func lintFunctions(ctx context.Context, conn *pgx.Conn, spec Spec, add func(rule, object, format string, args ...any)) error {
	rows, err := conn.Query(ctx, `SELECT p.proname::text, p.prosecdef, pg_catalog.pg_get_userbyid(p.proowner)::text,
       ARRAY(SELECT CASE WHEN a.grantee = 0 THEN 'public' ELSE pg_catalog.pg_get_userbyid(a.grantee)::text END
               FROM aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) AS a
              WHERE a.privilege_type = 'EXECUTE' ORDER BY 1)::text[],
       COALESCE('search_path=pg_catalog, pg_temp' = ANY (p.proconfig), false)
  FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = 'pandokos'
 ORDER BY 1`)
	if err != nil {
		return err
	}
	type fn struct {
		name, owner string
		definer     bool
		executors   []string

		hasSearchPath bool
	}
	funcs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (fn, error) {
		var f fn
		err := r.Scan(&f.name, &f.definer, &f.owner, &f.executors, &f.hasSearchPath)
		return f, err
	})
	if err != nil {
		return err
	}
	for _, f := range funcs {
		if f.definer && !f.hasSearchPath {
			add("L-18", f.name, "SECURITY DEFINER without search_path = pg_catalog, pg_temp")
		}
		if f.definer && !contains(spec.SecurityDefiner, f.name) {
			add("L-14", f.name, "SECURITY DEFINER and not listed in Spec.SecurityDefiner")
		}
		if contains(f.executors, "public") {
			add("L-14", f.name, "executable by PUBLIC")
		}
		if f.owner != ownerRole {
			add("L-14", f.name, "owned by %s, not by %s", f.owner, ownerRole)
		}
	}
	return nil
}

// lintRoles is L-15.
func lintRoles(ctx context.Context, conn *pgx.Conn, add func(rule, object, format string, args ...any)) error {
	rows, err := conn.Query(ctx, `SELECT r.rolname::text, r.rolsuper, r.rolbypassrls, r.rolcreaterole, r.rolcreatedb, r.rolreplication
  FROM pg_catalog.pg_roles r
 WHERE r.rolname = ANY($1)`, privilegeRoles)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		var super, bypass, createRole, createDB, repl bool
		if err := rows.Scan(&name, &super, &bypass, &createRole, &createDB, &repl); err != nil {
			return err
		}
		for attr, set := range map[string]bool{"rolsuper": super, "rolbypassrls": bypass, "rolcreaterole": createRole, "rolcreatedb": createDB, "rolreplication": repl} {
			if set {
				add("L-15", name, "the role has %s", attr)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = conn.Query(ctx, `SELECT pg_catalog.pg_get_userbyid(m.roleid)::text, pg_catalog.pg_get_userbyid(m.member)::text
  FROM pg_catalog.pg_auth_members m
 WHERE pg_catalog.pg_get_userbyid(m.roleid) LIKE 'pandokos\_%'
   AND pg_catalog.pg_get_userbyid(m.member) LIKE 'pandokos\_%'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var role, member string
		if err := rows.Scan(&role, &member); err != nil {
			return err
		}
		add("L-15", member, "the role is a member of %s", role)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, role := range runtimeRoleNames {
		var owned int
		var createSchema, createDB bool
		if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_catalog.pg_class WHERE pg_catalog.pg_get_userbyid(relowner) = $1)
     + (SELECT count(*) FROM pg_catalog.pg_proc WHERE pg_catalog.pg_get_userbyid(proowner) = $1)
     + (SELECT count(*) FROM pg_catalog.pg_namespace WHERE pg_catalog.pg_get_userbyid(nspowner) = $1),
       pg_catalog.has_schema_privilege($1, 'pandokos', 'CREATE'),
       pg_catalog.has_database_privilege($1, pg_catalog.current_database(), 'CREATE')`, role).Scan(&owned, &createSchema, &createDB); err != nil {
			return err
		}
		if owned > 0 {
			add("L-15", role, "the role owns %d objects", owned)
		}
		if createSchema {
			add("L-15", role, "the role has CREATE on schema pandokos")
		}
		if createDB {
			add("L-15", role, "the role has CREATE on the database")
		}
	}
	return nil
}

// lintAppendOnly is L-17: no runtime role or PUBLIC can change or remove a row of an append-only
// table, and the table has the enabled triggers that stop everybody else.
func lintAppendOnly(ctx context.Context, conn *pgx.Conn, spec Spec, add func(rule, object, format string, args ...any)) error {
	for _, table := range spec.AppendOnly {
		for _, grantee := range append(append([]string{}, runtimeRoleNames...), "public") {
			var held bool
			who := grantee
			if err := conn.QueryRow(ctx, `SELECT pg_catalog.has_any_column_privilege($2, $1::regclass, 'UPDATE')
    OR pg_catalog.has_table_privilege($2, $1::regclass, 'UPDATE')
    OR pg_catalog.has_table_privilege($2, $1::regclass, 'DELETE')
    OR pg_catalog.has_table_privilege($2, $1::regclass, 'TRUNCATE')`, schema+"."+pgx.Identifier{table}.Sanitize(), who).Scan(&held); err != nil {
				return err
			}
			if held {
				add("L-17", table, "%s holds UPDATE, DELETE or TRUNCATE on an append-only table", grantee)
			}
		}
		rows, err := conn.Query(ctx, `SELECT t.tgtype::int, p.proname::text, t.tgenabled::text
  FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
 WHERE t.tgrelid = $1::regclass AND NOT t.tgisinternal`, schema+"."+pgx.Identifier{table}.Sanitize())
		if err != nil {
			return err
		}
		rowTrigger, truncTrigger := false, false
		for rows.Next() {
			var tgtype int
			var fn, enabled string
			if err := rows.Scan(&tgtype, &fn, &enabled); err != nil {
				return err
			}
			if fn != "append_only" || enabled == "D" {
				continue
			}
			const row, before, del, upd, trunc = 1, 2, 8, 16, 32
			if tgtype&row != 0 && tgtype&before != 0 && tgtype&del != 0 && tgtype&upd != 0 {
				rowTrigger = true
			}
			if tgtype&row == 0 && tgtype&before != 0 && tgtype&trunc != 0 {
				truncTrigger = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if !rowTrigger {
			add("L-17", table, "no enabled BEFORE UPDATE OR DELETE row trigger that executes pandokos.append_only()")
		}
		if !truncTrigger {
			add("L-17", table, "no enabled BEFORE TRUNCATE statement trigger that executes pandokos.append_only()")
		}
	}
	return nil
}
