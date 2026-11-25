package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pandokos/pandokos/test/dbtest"
)

// constraintOf returns the SQLSTATE and the constraint name of a database error.
func constraintOf(err error) (code, constraint string) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code, pe.ConstraintName
	}
	return "", ""
}

// TestIssuerSchema is T-584: the constraints of migration 0008.
func TestIssuerSchema(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	super := d.Super(t)
	c := ctx(t)

	exec := func(sql string, args ...any) error {
		_, err := super.Exec(c, sql, args...)
		return err
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	// refused checks that a statement fails with the constraint, and that it changed nothing (the
	// caller counts rows where that matters).
	refused := func(what, code, constraint string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: accepted, want %s %s", what, code, constraint)
			return
		}
		if gotCode, gotConstraint := constraintOf(err); gotCode != code || (constraint != "" && gotConstraint != constraint) {
			t.Errorf("%s: %s %s (%v), want %s %s", what, gotCode, gotConstraint, err, code, constraint)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		must(sql, super.QueryRow(c, sql, args...).Scan(&n))
		return n
	}
	one := func(sql string, args ...any) string {
		t.Helper()
		var s string
		must(sql, super.QueryRow(c, sql, args...).Scan(&s))
		return s
	}

	a, b := d.SeedTenant(t, "issuer-a").String(), d.SeedTenant(t, "issuer-b").String()
	var saA, saB string
	must("service account a", super.QueryRow(c, `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'bot') RETURNING id::text`, a).Scan(&saA))
	must("service account b", super.QueryRow(c, `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'bot') RETURNING id::text`, b).Scan(&saB))

	ti := func(tenantID, name, issuer, audience string) (string, error) {
		var id string
		err := super.QueryRow(c, `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
			tenantID, name, issuer, audience).Scan(&id)
		return id, err
	}
	pi := func(name, issuer, audience string) (string, error) {
		var id string
		err := super.QueryRow(c, `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ($1, $2, $3, 'org') RETURNING id::text`,
			name, issuer, audience).Scan(&id)
		return id, err
	}

	t.Run("defaults", func(t *testing.T) {
		id, err := ti(a, "defaults", "https://defaults.example.test", "aud")
		must("insert", err)
		var algs []string
		var sub, req, desc, jwks string
		var enabled bool
		must("read", super.QueryRow(c, `SELECT algorithms, subject_claim, required_claims::text, description, jwks_uri, enabled FROM pandokos.trusted_issuer WHERE id = $1::uuid`, id).
			Scan(&algs, &sub, &req, &desc, &jwks, &enabled))
		if strings.Join(algs, ",") != "RS256,ES256" || sub != "sub" || req != "{}" || desc != "" || jwks != "" || !enabled {
			t.Errorf("defaults: %v %q %q %q %q %v", algs, sub, req, desc, jwks, enabled)
		}
		pid, err := pi("defaults-platform", "https://defaults-platform.example.test", "aud")
		must("platform insert", err)
		must("read platform", super.QueryRow(c, `SELECT algorithms, subject_claim, required_claims::text, enabled FROM pandokos.platform_issuer WHERE id = $1::uuid`, pid).Scan(&algs, &sub, &req, &enabled))
		if strings.Join(algs, ",") != "RS256,ES256" || sub != "sub" || req != "{}" || !enabled {
			t.Errorf("platform defaults: %v %q %q %v", algs, sub, req, enabled)
		}
		// Time-ordered identifiers.
		if len(id) != 36 || id[14] != '7' {
			t.Errorf("id %q is not a version 7 UUID", id)
		}
	})

	t.Run("formats of a registration, the same for both kinds", func(t *testing.T) {
		long := func(n int) string { return strings.Repeat("a", n) }
		type row struct {
			what       string
			name       string
			issuer     string
			audience   string
			jwks       string
			algs       string // an array literal
			subject    string
			required   string
			constraint string // suffix: trusted_issuer_<suffix>; "" accepted
		}
		const okIssuer, okAud = "https://formats.example.test", "aud"
		rows := []row{
			{what: "a valid registration", name: "ok", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: "{}"},
			{what: "a name of one character", name: "a", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: "{}"},
			{what: "a name of 63 characters", name: long(63), issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: "{}"},
			{what: "a name of 64 characters", name: long(64), issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "name_format"},
			{what: "a name in upper case", name: "Idp", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "name_format"},
			{what: "a name that starts with a hyphen", name: "-idp", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "name_format"},
			{what: "an empty name", name: "", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "name_format"},
			{what: "an http issuer", name: "n1", issuer: "http://x.example.test", audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "issuer_format"},
			{what: "an issuer with a query", name: "n2", issuer: "https://x.example.test/?a=b", audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "issuer_format"},
			{what: "an issuer with a fragment", name: "n3", issuer: "https://x.example.test/#f", audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "issuer_format"},
			{what: "an issuer with user information", name: "n4", issuer: "https://user@x.example.test", audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "issuer_format"},
			{what: "an issuer with a space", name: "n5", issuer: "https://x.example.test/a b", audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "issuer_format"},
			{what: "an issuer with a newline", name: "n6", issuer: "https://x.example.test/a\nb", audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "issuer_format"},
			{what: "an issuer of 255 bytes", name: "n7", issuer: "https://" + long(247), audience: okAud, algs: "{RS256}", subject: "sub", required: "{}"},
			{what: "an issuer of 256 bytes", name: "n8", issuer: "https://" + long(248), audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "issuer_format"},
			{what: "an issuer without a scheme", name: "n9", issuer: "x.example.test", audience: okAud, algs: "{RS256}", subject: "sub", required: "{}", constraint: "issuer_format"},
			{what: "an empty audience", name: "n10", issuer: okIssuer, audience: "", algs: "{RS256}", subject: "sub", required: "{}", constraint: "audience_format"},
			{what: "an audience of 255 characters", name: "n11", issuer: okIssuer, audience: long(255), algs: "{RS256}", subject: "sub", required: "{}"},
			{what: "an audience of 256 characters", name: "n12", issuer: okIssuer, audience: long(256), algs: "{RS256}", subject: "sub", required: "{}", constraint: "audience_format"},
			{what: "an audience with a control character", name: "n13", issuer: okIssuer, audience: "a\x01b", algs: "{RS256}", subject: "sub", required: "{}", constraint: "audience_format"},
			{what: "an audience with a newline", name: "n14", issuer: okIssuer, audience: "a\nb", algs: "{RS256}", subject: "sub", required: "{}", constraint: "audience_format"},
			{what: "an audience with spaces and symbols", name: "n15", issuer: okIssuer, audience: "api://pandokos/acme eu", algs: "{RS256}", subject: "sub", required: "{}"},
			{what: "an explicit jwks_uri", name: "n16", issuer: okIssuer, audience: okAud, jwks: "https://x.example.test/keys?v=1", algs: "{RS256}", subject: "sub", required: "{}"},
			{what: "an http jwks_uri", name: "n17", issuer: okIssuer, audience: okAud, jwks: "http://x.example.test/keys", algs: "{RS256}", subject: "sub", required: "{}", constraint: "jwks_uri_format"},
			{what: "a jwks_uri with a fragment", name: "n18", issuer: okIssuer, audience: okAud, jwks: "https://x.example.test/keys#f", algs: "{RS256}", subject: "sub", required: "{}", constraint: "jwks_uri_format"},
			{what: "a jwks_uri with user information", name: "n19", issuer: okIssuer, audience: okAud, jwks: "https://u:p@x.example.test/keys", algs: "{RS256}", subject: "sub", required: "{}", constraint: "jwks_uri_format"},
			{what: "a jwks_uri of 2049 bytes", name: "n20", issuer: okIssuer, audience: okAud, jwks: "https://x.example.test/" + long(2049-len("https://x.example.test/")), algs: "{RS256}", subject: "sub", required: "{}", constraint: "jwks_uri_format"},
			{what: "a jwks_uri of 2048 bytes", name: "n21", issuer: okIssuer, audience: okAud, jwks: "https://x.example.test/" + long(2048-len("https://x.example.test/")), algs: "{RS256}", subject: "sub", required: "{}"},
			{what: "all ten algorithms", name: "n22", issuer: okIssuer, audience: okAud, algs: "{RS256,RS384,RS512,PS256,PS384,PS512,ES256,ES384,ES512,EdDSA}", subject: "sub", required: "{}"},
			{what: "no algorithm", name: "n23", issuer: okIssuer, audience: okAud, algs: "{}", subject: "sub", required: "{}", constraint: "algorithms_valid"},
			{what: "eleven algorithms", name: "n24", issuer: okIssuer, audience: okAud, algs: "{RS256,RS384,RS512,PS256,PS384,PS512,ES256,ES384,ES512,EdDSA,RS256}", subject: "sub", required: "{}", constraint: "algorithms_valid"},
			{what: "HS256", name: "n25", issuer: okIssuer, audience: okAud, algs: "{HS256}", subject: "sub", required: "{}", constraint: "algorithms_valid"},
			{what: "none", name: "n26", issuer: okIssuer, audience: okAud, algs: "{none}", subject: "sub", required: "{}", constraint: "algorithms_valid"},
			{what: "an algorithm in lower case", name: "n27", issuer: okIssuer, audience: okAud, algs: "{rs256}", subject: "sub", required: "{}", constraint: "algorithms_valid"},
			{what: "a subject claim of 64 characters", name: "n28", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: long(64), required: "{}"},
			{what: "a subject claim of 65 characters", name: "n29", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: long(65), required: "{}", constraint: "subject_claim_format"},
			{what: "a subject claim with a namespaced URL name", name: "n30", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "https://claims.example.test/sub", required: "{}"},
			{what: "a subject claim with an at sign", name: "n30b", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "a@b", required: "{}", constraint: "subject_claim_format"},
			{what: "a subject claim with a query", name: "n30c", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "a?b", required: "{}", constraint: "subject_claim_format"},
			{what: "a subject claim with a namespace", name: "n31", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "kubernetes.io:sub", required: "{}"},
			{what: "an empty subject claim", name: "n32", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "", required: "{}", constraint: "subject_claim_format"},
			{what: "a subject claim with a space", name: "n33", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "a b", required: "{}", constraint: "subject_claim_format"},
			{what: "required claims", name: "n34", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: `{"tier":"gold"}`},
			{what: "required claims that are an array", name: "n35", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: `["tier"]`, constraint: "required_claims_object"},
			{what: "required claims that are a string", name: "n36", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: `"x"`, constraint: "required_claims_object"},
			{what: "required claims that are null", name: "n37", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: `null`, constraint: "required_claims_object"},
			{what: "required claims over 4096 bytes", name: "n38", issuer: okIssuer, audience: okAud, algs: "{RS256}", subject: "sub", required: `{"k":"` + long(4100) + `"}`, constraint: "required_claims_object"},
		}
		for _, kind := range []string{"trusted_issuer", "platform_issuer"} {
			// Every row is its own transaction-free insert; the rows that are accepted are removed
			// again, so that the directory does not make the next one a conflict.
			for i, r := range rows {
				var sql string
				var args []any
				if kind == "trusted_issuer" {
					sql = `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience, jwks_uri, algorithms, subject_claim, required_claims) VALUES ($1::uuid, $2, $3, $4, $5, $6::text[], $7, $8::jsonb)`
					args = []any{a, r.name, r.issuer, r.audience, r.jwks, r.algs, r.subject, r.required}
				} else {
					sql = `INSERT INTO pandokos.platform_issuer (name, issuer, audience, jwks_uri, algorithms, subject_claim, required_claims, tenant_claim) VALUES ($1, $2, $3, $4, $5::text[], $6, $7::jsonb, 'org')`
					args = []any{r.name, r.issuer, r.audience, r.jwks, r.algs, r.subject, r.required}
				}
				// Rows share an issuer and audience on purpose: delete what a previous row made.
				_ = exec(`DELETE FROM pandokos.`+kind+` WHERE issuer = $1 AND audience = $2`, r.issuer, r.audience)
				_ = exec(`DELETE FROM pandokos.`+kind+` WHERE name = $1`, r.name)
				err := exec(sql, args...)
				if r.constraint == "" {
					if err != nil {
						t.Errorf("%s, %s: %v", kind, r.what, err)
					}
				} else {
					refused(kind+", "+r.what, "23514", kind+"_"+r.constraint, err)
				}
				_ = i
			}
			must("clean "+kind, exec(`DELETE FROM pandokos.`+kind+` WHERE issuer LIKE 'https://formats.example.test%' OR issuer LIKE 'https://aaaa%' OR issuer LIKE 'https://x.example.test%'`))
		}
		// A platform-level registration also needs a well-formed tenant claim.
		for i, c := range []struct {
			claim string
			ok    bool
		}{{"org", true}, {"https://x/org", true}, {"", false}, {strings.Repeat("o", 64), true}, {strings.Repeat("o", 65), false}, {"a b", false}, {"org_id", true}, {"ns.claim:x/y-z", true}, {"a@b", false}, {"a#b", false}} {
			err := exec(`INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ($1, $2, 'aud', $3)`,
				"claim-"+string(rune('a'+i)), "https://tc"+string(rune('a'+i))+".example.test", c.claim)
			if c.ok && err != nil {
				t.Errorf("tenant claim %q: %v", c.claim, err)
			}
			if !c.ok {
				refused("tenant claim "+c.claim, "23514", "platform_issuer_tenant_claim_format", err)
			}
		}
	})

	t.Run("a name is unique per tenant for tenant-bound registrations, and for the installation for platform-level ones", func(t *testing.T) {
		_, err := ti(a, "dup-name", "https://dup-name-1.example.test", "aud")
		must("first", err)
		_, err = ti(a, "dup-name", "https://dup-name-2.example.test", "aud")
		refused("same tenant, same name", "23505", "trusted_issuer_name_key", err)
		if _, err := ti(b, "dup-name", "https://dup-name-3.example.test", "aud"); err != nil {
			t.Errorf("another tenant, same name: %v", err)
		}
		_, err = pi("dup-platform", "https://dup-platform-1.example.test", "aud")
		must("platform first", err)
		_, err = pi("dup-platform", "https://dup-platform-2.example.test", "aud")
		refused("platform, same name", "23505", "platform_issuer_name_key", err)
		// A tenant-bound and a platform-level registration may share a name: they are two namespaces.
		if _, err := pi("dup-name", "https://dup-platform-3.example.test", "aud"); err != nil {
			t.Errorf("a platform name equal to a tenant name: %v", err)
		}
	})

	t.Run("the pair of issuer and audience is unique across tenants and across both kinds", func(t *testing.T) {
		const iss = "https://pair.example.test"
		_, err := ti(a, "pair-a", iss, "aud-1")
		must("tenant a", err)
		// The same pair for another tenant, and for the same one under another name.
		before := count(`SELECT count(*) FROM pandokos.trusted_issuer`)
		_, err = ti(b, "pair-b", iss, "aud-1")
		refused("another tenant, same pair", "23505", "issuer_directory_pkey", err)
		_, err = ti(a, "pair-a2", iss, "aud-1")
		refused("same tenant, same pair", "23505", "issuer_directory_pkey", err)
		_, err = pi("pair-platform", iss, "aud-1")
		refused("platform-level, same pair", "23505", "issuer_directory_pkey", err)
		if after := count(`SELECT count(*) FROM pandokos.trusted_issuer`); after != before {
			t.Errorf("a refused registration left a row behind: %d rows, %d before", after, before)
		}
		if n := count(`SELECT count(*) FROM pandokos.platform_issuer WHERE name = 'pair-platform'`); n != 0 {
			t.Errorf("a refused platform registration left %d rows", n)
		}
		// Another audience of the same issuer is another pair; so is another issuer with the audience.
		if _, err := ti(b, "pair-b", iss, "aud-2"); err != nil {
			t.Errorf("another audience: %v", err)
		}
		if _, err := ti(b, "pair-b-2", "https://pair-2.example.test", "aud-1"); err != nil {
			t.Errorf("another issuer: %v", err)
		}
		// The other way round: platform first, then a tenant.
		_, err = pi("pair-platform-2", "https://pair-3.example.test", "aud-1")
		must("platform", err)
		_, err = ti(a, "pair-a3", "https://pair-3.example.test", "aud-1")
		refused("tenant-bound, the pair of a platform-level one", "23505", "issuer_directory_pkey", err)
		// Byte for byte: case and a trailing slash make another pair.
		if _, err := ti(a, "pair-case", "https://PAIR.example.test", "aud-1"); err != nil {
			t.Errorf("an issuer in another case: %v", err)
		}
		if _, err := ti(a, "pair-slash", iss+"/", "aud-1"); err != nil {
			t.Errorf("an issuer with a trailing slash: %v", err)
		}
		// One row of the directory per registration.
		if got, want := count(`SELECT count(*) FROM pandokos.issuer_directory`), count(`SELECT (SELECT count(*) FROM pandokos.trusted_issuer) + (SELECT count(*) FROM pandokos.platform_issuer)`); got != want {
			t.Errorf("%d directory rows for %d registrations", got, want)
		}
	})

	t.Run("the directory follows an update and a delete", func(t *testing.T) {
		id, err := ti(a, "moves", "https://moves.example.test", "old-aud")
		must("insert", err)
		dir := func(iss, aud string) int {
			return count(`SELECT count(*) FROM pandokos.issuer_directory WHERE issuer = $1 AND audience = $2 AND issuer_id = $3::uuid AND scope = 'tenant' AND tenant_id = $4::uuid`, iss, aud, id, a)
		}
		if dir("https://moves.example.test", "old-aud") != 1 {
			t.Fatal("the registration has no directory row")
		}
		must("change the audience", exec(`UPDATE pandokos.trusted_issuer SET audience = 'new-aud' WHERE id = $1::uuid`, id))
		if dir("https://moves.example.test", "old-aud") != 0 || dir("https://moves.example.test", "new-aud") != 1 {
			t.Error("the directory row did not move with the audience")
		}
		must("change the issuer", exec(`UPDATE pandokos.trusted_issuer SET issuer = 'https://moved.example.test' WHERE id = $1::uuid`, id))
		if dir("https://moves.example.test", "new-aud") != 0 || dir("https://moved.example.test", "new-aud") != 1 {
			t.Error("the directory row did not move with the issuer")
		}
		must("change something else", exec(`UPDATE pandokos.trusted_issuer SET description = 'x', enabled = false WHERE id = $1::uuid`, id))
		if dir("https://moved.example.test", "new-aud") != 1 {
			t.Error("the directory row is gone after an unrelated update")
		}
		// An update into a taken pair fails and the row keeps its old pair.
		_, err = ti(b, "taken", "https://taken.example.test", "aud")
		must("taken", err)
		refused("update into a taken pair", "23505", "issuer_directory_pkey", exec(`UPDATE pandokos.trusted_issuer SET issuer = 'https://taken.example.test', audience = 'aud' WHERE id = $1::uuid`, id))
		if got := one(`SELECT issuer FROM pandokos.trusted_issuer WHERE id = $1::uuid`, id); got != "https://moved.example.test" {
			t.Errorf("the registration moved to %s although the update failed", got)
		}
		if dir("https://moved.example.test", "new-aud") != 1 {
			t.Error("the directory row changed although the update failed")
		}
		must("delete", exec(`DELETE FROM pandokos.trusted_issuer WHERE id = $1::uuid`, id))
		if n := count(`SELECT count(*) FROM pandokos.issuer_directory WHERE issuer_id = $1::uuid`, id); n != 0 {
			t.Errorf("%d directory rows after the delete", n)
		}
		// The pair is free again.
		if _, err := ti(b, "reuse", "https://moved.example.test", "new-aud"); err != nil {
			t.Errorf("a deleted pair cannot be registered again: %v", err)
		}
		// The same for a platform-level registration.
		pid, err := pi("moves-platform", "https://moves-platform.example.test", "aud")
		must("platform", err)
		must("platform update", exec(`UPDATE pandokos.platform_issuer SET audience = 'aud-2' WHERE id = $1::uuid`, pid))
		if n := count(`SELECT count(*) FROM pandokos.issuer_directory WHERE issuer_id = $1::uuid AND audience = 'aud-2' AND scope = 'platform' AND tenant_id IS NULL`, pid); n != 1 {
			t.Errorf("platform directory rows: %d", n)
		}
		must("platform delete", exec(`DELETE FROM pandokos.platform_issuer WHERE id = $1::uuid`, pid))
		if n := count(`SELECT count(*) FROM pandokos.issuer_directory WHERE issuer_id = $1::uuid`, pid); n != 0 {
			t.Errorf("%d platform directory rows after the delete", n)
		}
	})

	t.Run("the directory has its own constraints", func(_ *testing.T) {
		id := one(`SELECT pandokos.uuid_v7()::text`)
		refused("a tenant scope without a tenant", "23514", "issuer_directory_scope_tenant",
			exec(`INSERT INTO pandokos.issuer_directory (issuer, audience, scope, tenant_id, issuer_id) VALUES ('https://d1.example.test', 'a', 'tenant', NULL, $1::uuid)`, id))
		refused("a platform scope with a tenant", "23514", "issuer_directory_scope_tenant",
			exec(`INSERT INTO pandokos.issuer_directory (issuer, audience, scope, tenant_id, issuer_id) VALUES ('https://d2.example.test', 'a', 'platform', $1::uuid, $2::uuid)`, a, id))
		refused("another scope", "23514", "issuer_directory_scope_valid",
			exec(`INSERT INTO pandokos.issuer_directory (issuer, audience, scope, tenant_id, issuer_id) VALUES ('https://d3.example.test', 'a', 'global', NULL, $1::uuid)`, id))
		refused("an unknown tenant", "23503", "issuer_directory_tenant_fkey",
			exec(`INSERT INTO pandokos.issuer_directory (issuer, audience, scope, tenant_id, issuer_id) VALUES ('https://d4.example.test', 'a', 'tenant', gen_random_uuid(), $1::uuid)`, id))
	})

	t.Run("subject mappings", func(t *testing.T) {
		ia, err := ti(a, "map-a", "https://map-a.example.test", "aud")
		must("issuer a", err)
		ib, err := ti(b, "map-b", "https://map-b.example.test", "aud")
		must("issuer b", err)
		pid, err := pi("map-platform", "https://map-platform.example.test", "aud")
		must("platform", err)
		must("list", exec(`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, $2::uuid, 'org-a')`, pid, a))

		ins := func(tenantID string, trusted, platform *string, subject, sa string) error {
			return exec(`INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, platform_issuer_id, subject, service_account_id) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid)`,
				tenantID, trusted, platform, subject, sa)
		}
		s := func(v string) *string { return &v }

		must("a mapping to a trusted issuer", ins(a, s(ia), nil, "spiffe://a/1", saA))
		must("a mapping to a platform-level issuer", ins(a, nil, s(pid), "spiffe://a/1", saA))
		must("the same subject of another registration", ins(a, s(ia), nil, "spiffe://a/2", saA))
		refused("no issuer", "23514", "subject_mapping_one_issuer", ins(a, nil, nil, "x", saA))
		refused("two issuers", "23514", "subject_mapping_one_issuer", ins(a, s(ia), s(pid), "x", saA))
		refused("a duplicate subject of a trusted issuer", "23505", "subject_mapping_trusted_key", ins(a, s(ia), nil, "spiffe://a/1", saA))
		refused("a duplicate subject of a platform-level issuer", "23505", "subject_mapping_platform_key", ins(a, nil, s(pid), "spiffe://a/1", saA))
		refused("an issuer of another tenant", "23503", "subject_mapping_trusted_issuer_fkey", ins(a, s(ib), nil, "x", saA))
		refused("a service account of another tenant", "23503", "subject_mapping_service_account_fkey", ins(a, s(ia), nil, "y", saB))
		refused("an unknown platform issuer", "23503", "subject_mapping_platform_issuer_fkey", ins(a, nil, s(one(`SELECT gen_random_uuid()::text`)), "z", saA))
		refused("an unknown tenant", "23503", "subject_mapping_tenant_fkey", ins(one(`SELECT gen_random_uuid()::text`), s(ia), nil, "z", saA))
		refused("an empty subject", "23514", "subject_mapping_subject_format", ins(a, s(ia), nil, "", saA))
		refused("a subject of 256 characters", "23514", "subject_mapping_subject_format", ins(a, s(ia), nil, strings.Repeat("s", 256), saA))
		refused("a subject with a control character", "23514", "subject_mapping_subject_format", ins(a, s(ia), nil, "a\tb", saA))
		must("a subject of 255 characters", ins(a, s(ia), nil, strings.Repeat("s", 255), saA))
		must("a subject with unicode and spaces", ins(a, s(ia), nil, "système de façade / équipe 1", saA))
		refused("a description of 201 characters", "23514", "subject_mapping_description_length",
			exec(`INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id, description) VALUES ($1::uuid, $2::uuid, 'd', $3::uuid, $4)`, a, ia, saA, strings.Repeat("d", 201)))
		// Subjects are compared byte for byte.
		must("a subject that differs in case", ins(a, s(ia), nil, "SPIFFE://a/1", saA))
		must("a subject that differs by a space", ins(a, s(ia), nil, "spiffe://a/1 ", saA))

		// A service account that has a mapping cannot be deleted from under it.
		_, err = super.Exec(c, `DELETE FROM pandokos.service_account WHERE id = $1::uuid`, saA)
		refused("deleting a service account that has a mapping", "23503", "subject_mapping_service_account_fkey", err)

		// Cascades: deleting a trusted issuer deletes its mappings; a platform-level one also its list.
		n := count(`SELECT count(*) FROM pandokos.subject_mapping WHERE trusted_issuer_id = $1::uuid`, ia)
		if n < 4 {
			t.Fatalf("%d mappings of the trusted issuer", n)
		}
		must("delete the trusted issuer", exec(`DELETE FROM pandokos.trusted_issuer WHERE id = $1::uuid`, ia))
		if n := count(`SELECT count(*) FROM pandokos.subject_mapping WHERE trusted_issuer_id = $1::uuid`, ia); n != 0 {
			t.Errorf("%d mappings survive the deletion of their trusted issuer", n)
		}
		if n := count(`SELECT count(*) FROM pandokos.subject_mapping WHERE platform_issuer_id = $1::uuid`, pid); n != 1 {
			t.Fatalf("%d mappings of the platform-level issuer", n)
		}
		must("delete the platform issuer", exec(`DELETE FROM pandokos.platform_issuer WHERE id = $1::uuid`, pid))
		if n := count(`SELECT count(*) FROM pandokos.subject_mapping WHERE platform_issuer_id = $1::uuid`, pid) +
			count(`SELECT count(*) FROM pandokos.platform_issuer_tenant WHERE issuer_id = $1::uuid`, pid); n != 0 {
			t.Errorf("%d mappings and list entries survive the deletion of their platform-level issuer", n)
		}
		// The mapping of tenant b is untouched.
		must("a mapping of b", ins(b, s(ib), nil, "spiffe://b/1", saB))
		must("delete a", exec(`DELETE FROM pandokos.subject_mapping WHERE tenant_id = $1::uuid`, a))
		if n := count(`SELECT count(*) FROM pandokos.subject_mapping WHERE tenant_id = $1::uuid`, b); n != 1 {
			t.Errorf("%d mappings of b", n)
		}
		// Identifiers of mappings are time ordered.
		if id := one(`SELECT id::text FROM pandokos.subject_mapping WHERE tenant_id = $1::uuid`, b); id[14] != '7' {
			t.Errorf("mapping id %q", id)
		}
	})

	t.Run("the tenant list of a platform-level registration", func(t *testing.T) {
		pid, err := pi("list-platform", "https://list-platform.example.test", "aud")
		must("platform", err)
		put := func(tenantID, claim string) error {
			return exec(`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, $2::uuid, $3)`, pid, tenantID, claim)
		}
		must("a tenant", put(a, "org-a"))
		refused("the same tenant twice", "23505", "platform_issuer_tenant_pkey", put(a, "org-a2"))
		refused("the same claim value for another tenant", "23505", "platform_issuer_tenant_claim_value_key", put(b, "org-a"))
		must("another tenant, another value", put(b, "org-b"))
		refused("an empty claim value", "23514", "platform_issuer_tenant_claim_value_format", func() error {
			return exec(`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, gen_random_uuid(), '')`, pid)
		}())
		refused("a claim value of 256 characters", "23514", "platform_issuer_tenant_claim_value_format", func() error {
			return exec(`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, gen_random_uuid(), $2)`, pid, strings.Repeat("v", 256))
		}())
		refused("a claim value with a control character", "23514", "platform_issuer_tenant_claim_value_format", func() error {
			return exec(`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, gen_random_uuid(), E'a\nb')`, pid)
		}())
		refused("an unknown tenant", "23503", "platform_issuer_tenant_tenant_fkey", func() error {
			return exec(`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, gen_random_uuid(), 'x')`, pid)
		}())
		refused("an unknown issuer", "23503", "platform_issuer_tenant_issuer_fkey", func() error {
			return exec(`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES (gen_random_uuid(), $1::uuid, 'x')`, a)
		}())
		// A claim value may be reused by another registration: the unique key is per registration.
		pid2, err := pi("list-platform-2", "https://list-platform-2.example.test", "aud")
		must("second", err)
		if err := exec(`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, $2::uuid, 'org-a')`, pid2, a); err != nil {
			t.Errorf("the same claim value in another registration: %v", err)
		}
	})

	t.Run("principal_kind token is accepted, another value is not", func(t *testing.T) {
		insert := func(kind string) error {
			return exec(`INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id,
				provider, api_family, operation, outcome, duration_ms, input_source, output_source, cost_status, key_id)
				VALUES ($1::uuid, pandokos.uuid_v7(), now(), md5(random()::text), $2, gen_random_uuid()::text,
				'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none', gen_random_uuid()::text)`, a, kind)
		}
		must("token", insert("token"))
		must("service_account", insert("service_account"))
		for _, kind := range []string{"jwt", "user", "platform", "Token", ""} {
			refused("principal_kind "+kind, "23514", "usage_record_principal_kind_valid", insert(kind))
		}
		// key_id holds a UUID (the ID of a subject mapping), as the existing format check allows.
		if n := count(`SELECT count(*) FROM pandokos.usage_record WHERE principal_kind = 'token' AND key_id ~ '^[0-9a-f-]{36}$'`); n != 1 {
			t.Errorf("%d token rows with a UUID as key_id", n)
		}
	})

	t.Run("the tables are in the schema with row level security where they are tenant-scoped", func(t *testing.T) {
		for table, want := range map[string]bool{"trusted_issuer": true, "subject_mapping": true, "platform_issuer": true, "platform_issuer_tenant": true, "issuer_directory": false} {
			var rls, force bool
			must(table, super.QueryRow(c, `SELECT relrowsecurity, relforcerowsecurity FROM pg_catalog.pg_class WHERE oid = ('pandokos.' || $1)::regclass`, table).Scan(&rls, &force))
			if rls != want || force != want {
				t.Errorf("%s: row level security %v forced %v, want %v", table, rls, force, want)
			}
		}
		var notify int
		must("triggers", super.QueryRow(c, `SELECT count(*) FROM pg_catalog.pg_trigger WHERE tgrelid IN ('pandokos.trusted_issuer'::regclass, 'pandokos.subject_mapping'::regclass, 'pandokos.platform_issuer'::regclass, 'pandokos.platform_issuer_tenant'::regclass)
			AND tgname LIKE '%\_notify' AND NOT tgisinternal`).Scan(&notify))
		if notify != 4 {
			t.Errorf("%d notify triggers, want 4", notify)
		}
	})

}
