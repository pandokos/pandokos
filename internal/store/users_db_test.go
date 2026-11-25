package store_test

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pandokos/pandokos/test/dbtest"
)

const goodHash = "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func wantCode(t *testing.T, what string, err error, code string) {
	t.Helper()
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != code {
		t.Errorf("%s: %v, want SQLSTATE %s", what, err, code)
	}
}

func wantOK(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: %v", what, err)
	}
}

// TestUserSchema is T-773: the constraints of migration 0013.
func TestUserSchema(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	c := d.Super(t)
	exec := func(q string, args ...any) error {
		_, err := c.Exec(ctx(t), q, args...)
		return err
	}
	scan := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := c.QueryRow(ctx(t), q, args...).Scan(&s); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		return s
	}
	user := func(signIn string, tenantID any) string {
		return scan(`INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ($1, $2::uuid) RETURNING id::text`, signIn, tenantID)
	}
	tenantID := scan(`INSERT INTO pandokos.tenant (name) VALUES ('schema-a') RETURNING id::text`)
	other := scan(`INSERT INTO pandokos.tenant (name) VALUES ('schema-b') RETURNING id::text`)

	t.Run("app_user", func(t *testing.T) {
		wantOK(t, "a local user of the platform", exec(`INSERT INTO pandokos.app_user (sign_in) VALUES ('local')`))
		wantOK(t, "a local user of a tenant", exec(`INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ('local', $1::uuid)`, tenantID))
		wantOK(t, "a federated user of the platform", exec(`INSERT INTO pandokos.app_user (sign_in) VALUES ('oidc')`))
		wantCode(t, "an unknown sign-in method", exec(`INSERT INTO pandokos.app_user (sign_in) VALUES ('saml')`), "23514")
		wantCode(t, "a federated user managed by a tenant", exec(`INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ('oidc', $1::uuid)`, tenantID), "23514")
		wantCode(t, "a tenant that does not exist", exec(`INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ('local', '0192f0f0-0000-7000-8000-000000000001')`), "23503")
		wantCode(t, "a display name of 201 characters", exec(`INSERT INTO pandokos.app_user (sign_in, display_name) VALUES ('local', $1)`, strings.Repeat("a", 201)), "23514")
		wantOK(t, "a display name of 200 characters", exec(`INSERT INTO pandokos.app_user (sign_in, display_name) VALUES ('local', $1)`, strings.Repeat("é", 200)))
		wantCode(t, "a display name with a control character", exec(`INSERT INTO pandokos.app_user (sign_in, display_name) VALUES ('local', E'a\nb')`), "23514")
		wantCode(t, "an e-mail address with a space", exec(`INSERT INTO pandokos.app_user (sign_in, email) VALUES ('local', 'a b@example.test')`), "23514")
		wantCode(t, "an e-mail address of 255 characters", exec(`INSERT INTO pandokos.app_user (sign_in, email) VALUES ('local', $1)`, strings.Repeat("a", 255)), "23514")
		wantCode(t, "an unknown state", exec(`INSERT INTO pandokos.app_user (sign_in, state) VALUES ('local', 'gone')`), "23514")

		u := user("local", nil)
		wantCode(t, "a change of the sign-in method", exec(`UPDATE pandokos.app_user SET sign_in = 'oidc' WHERE id = $1::uuid`, u), "23514")
		wantCode(t, "a change of the managing tenant", exec(`UPDATE pandokos.app_user SET tenant_id = $2::uuid WHERE id = $1::uuid`, u, tenantID), "23514")
		managed := user("local", tenantID)
		wantCode(t, "a change of the managing tenant to the platform", exec(`UPDATE pandokos.app_user SET tenant_id = NULL WHERE id = $1::uuid`, managed), "23514")
		wantOK(t, "a change of the display name and the state", exec(`UPDATE pandokos.app_user SET display_name = 'Ada', state = 'disabled' WHERE id = $1::uuid`, u))
		if scan(`SELECT (state_changed_at > created_at)::text FROM pandokos.app_user WHERE id = $1::uuid`, u) != "true" {
			t.Error("state_changed_at does not move with the state")
		}
	})

	t.Run("local_account", func(t *testing.T) {
		insert := func(userID, username, hash string, must bool, expires any) error {
			return exec(`INSERT INTO pandokos.local_account (user_id, username, password_hash, must_change, password_expires_at) VALUES ($1::uuid, $2, $3, $4, $5)`, userID, username, hash, must, expires)
		}
		for _, ok := range []string{"ada", "a.b", "ada.lovelace", "ada@example.test", "a+b-c_d", "0day", strings.Repeat("a", 64)} {
			wantOK(t, "username "+ok, insert(user("local", nil), ok, goodHash, true, nil))
		}
		for _, bad := range []string{"a", "Ada", "ada lovelace", ".ada", "ada.", "-ada", "ada-", "ada\n", "adá", "", strings.Repeat("a", 65)} {
			wantCode(t, "username "+bad, insert(user("local", nil), bad, goodHash, true, nil), "23514")
		}
		wantCode(t, "a username that exists", insert(user("local", nil), "ada", goodHash, true, nil), "23505")

		// The PHC check.
		for name, bad := range map[string]string{
			"bcrypt": "$2a$10$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyz01234", "argon2i": strings.Replace(goodHash, "argon2id", "argon2i", 1),
			"version 16": strings.Replace(goodHash, "v=19", "v=16", 1), "padded": strings.Replace(goodHash, "aaaaaaaaaaa", "aaaaaaaaaa=", 1),
			"no hash": "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA", "order": strings.Replace(goodHash, "m=19456,t=2,p=1", "t=2,m=19456,p=1", 1),
			"empty": "", "a password": "hunter2hunter2",
		} {
			wantCode(t, "hash: "+name, insert(user("local", nil), "hash."+strings.ReplaceAll(name, " ", "."), bad, true, nil), "23514")
		}

		// The expiry only with must_change.
		wantCode(t, "an expiry without must_change", insert(user("local", nil), "expiry.no", goodHash, false, "2030-01-01T00:00:00Z"), "23514")
		wantOK(t, "an expiry with must_change", insert(user("local", nil), "expiry.yes", goodHash, true, "2030-01-01T00:00:00Z"))
		wantOK(t, "no expiry without must_change", insert(user("local", nil), "expiry.none", goodHash, false, nil))

		// The exclusive sign-in method.
		federated := user("oidc", nil)
		wantCode(t, "a local account for a federated user", insert(federated, "wrong.kind", goodHash, true, nil), "23503")
		wantOK(t, "an identity for a federated user", exec(`INSERT INTO pandokos.user_identity (user_id, issuer, subject) VALUES ($1::uuid, 'https://idp.example.test', 'sub-1')`, federated))
		wantCode(t, "an identity for a local user", exec(`INSERT INTO pandokos.user_identity (user_id, issuer, subject) VALUES ($1::uuid, 'https://idp.example.test', 'sub-2')`, user("local", nil)), "23503")
		wantCode(t, "a subject that is linked already", exec(`INSERT INTO pandokos.user_identity (user_id, issuer, subject) VALUES ($1::uuid, 'https://idp.example.test', 'sub-1')`, user("oidc", nil)), "23505")
		wantOK(t, "the same subject at another issuer", exec(`INSERT INTO pandokos.user_identity (user_id, issuer, subject) VALUES ($1::uuid, 'https://other.example.test', 'sub-1')`, user("oidc", nil)))
		for name, bad := range map[string][2]string{
			"an issuer over http": {"http://idp.example.test", "s"}, "an issuer with a query": {"https://idp.example.test/?a=1", "s"},
			"an empty subject": {"https://idp.example.test", ""}, "a subject with a control character": {"https://idp.example.test", "a\nb"},
			"a subject of 256 characters": {"https://idp.example.test", strings.Repeat("s", 256)},
		} {
			wantCode(t, name, exec(`INSERT INTO pandokos.user_identity (user_id, issuer, subject) VALUES ($1::uuid, $2, $3)`, user("oidc", nil), bad[0], bad[1]), "23514")
		}
		// The key never changes.
		u := user("local", nil)
		wantOK(t, "an account", insert(u, "immutable.key", goodHash, true, nil))
		wantCode(t, "a change of user_id", exec(`UPDATE pandokos.local_account SET user_id = $2::uuid WHERE user_id = $1::uuid`, u, user("local", nil)), "23514")
	})

	t.Run("user_session", func(t *testing.T) {
		u := user("local", nil)
		hash := func(s string) []byte { return []byte(strings.Repeat(s, 32))[:32] }
		insert := func(h []byte, method, expires string) error {
			return exec(`INSERT INTO pandokos.user_session (token_hash, user_id, method, expires_at) VALUES ($1, $2::uuid, $3, `+expires+`)`, h, u, method)
		}
		wantOK(t, "a session of twelve hours", insert(hash("a"), "local", "now() + interval '12 hours'"))
		wantOK(t, "a session of seven days", insert(hash("b"), "oidc", "now() + interval '7 days'"))
		wantCode(t, "a session over seven days", insert(hash("c"), "local", "now() + interval '7 days 1 minute'"), "23514")
		wantCode(t, "a session that ended before it began", insert(hash("d"), "local", "now() - interval '1 minute'"), "23514")
		wantCode(t, "an unknown method", insert(hash("e"), "saml", "now() + interval '1 hour'"), "23514")
		wantCode(t, "a token hash of 31 bytes", insert(hash("f")[:31], "local", "now() + interval '1 hour'"), "23514")
		wantCode(t, "a token hash of 33 bytes", insert(append(hash("g"), 'x'), "local", "now() + interval '1 hour'"), "23514")
		wantCode(t, "a token hash that exists", insert(hash("a"), "local", "now() + interval '1 hour'"), "23505")
		wantCode(t, "a user that does not exist", exec(`INSERT INTO pandokos.user_session (token_hash, user_id, method, expires_at) VALUES ($1, '0192f0f0-0000-7000-8000-000000000001', 'local', now() + interval '1 hour')`, hash("h")), "23503")
		// The row is stamped by the database: whatever the insert says.
		if scan(`SELECT (created_at > now() - interval '1 minute')::text FROM pandokos.user_session WHERE token_hash = $1`, hash("a")) != "true" {
			t.Error("created_at is not stamped")
		}
	})

	t.Run("memberships", func(t *testing.T) {
		u := user("local", nil) // managed by the platform: a member of two tenants
		for _, tid := range []string{tenantID, other} {
			wantOK(t, "a membership", exec(`INSERT INTO pandokos.membership (tenant_id, user_id) VALUES ($1::uuid, $2::uuid)`, tid, u))
		}
		wantCode(t, "a membership twice", exec(`INSERT INTO pandokos.membership (tenant_id, user_id) VALUES ($1::uuid, $2::uuid)`, tenantID, u), "23505")
		wantCode(t, "an unknown source", exec(`INSERT INTO pandokos.membership_role (tenant_id, user_id, role, source) VALUES ($1::uuid, $2::uuid, 'tenant-admin', 'wish')`, tenantID, u), "23514")
		wantCode(t, "a role that is not a name", exec(`INSERT INTO pandokos.membership_role (tenant_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'Tenant Admin')`, tenantID, u), "23514")
		wantCode(t, "a role of a user that is not a member", exec(`INSERT INTO pandokos.membership_role (tenant_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'tenant-admin')`, other, user("local", nil)), "23503")
		wantOK(t, "a role", exec(`INSERT INTO pandokos.membership_role (tenant_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'tenant-admin')`, tenantID, u))
		wantCode(t, "a role twice", exec(`INSERT INTO pandokos.membership_role (tenant_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'tenant-admin')`, tenantID, u), "23505")
		wantCode(t, "a change of the user of a membership", exec(`UPDATE pandokos.membership SET user_id = $3::uuid WHERE tenant_id = $1::uuid AND user_id = $2::uuid`, tenantID, u, user("local", nil)), "23514")

		// The directory is kept by the trigger.
		dir := func(userID string) []string {
			rows, err := c.Query(ctx(t), `SELECT tenant_id::text FROM pandokos.membership_directory WHERE user_id = $1::uuid`, userID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					t.Fatal(err)
				}
				out = append(out, s)
			}
			sort.Strings(out)
			return out
		}
		want := []string{tenantID, other}
		sort.Strings(want)
		if got := dir(u); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("the directory holds %v, want %v", got, want)
		}
		// Deleting a membership deletes its roles and its line in the directory.
		wantOK(t, "delete a membership", exec(`DELETE FROM pandokos.membership WHERE tenant_id = $1::uuid AND user_id = $2::uuid`, tenantID, u))
		if scan(`SELECT count(*)::text FROM pandokos.membership_role WHERE tenant_id = $1::uuid AND user_id = $2::uuid`, tenantID, u) != "0" {
			t.Error("the roles of a deleted membership remain")
		}
		if got := dir(u); len(got) != 1 || got[0] != other {
			t.Errorf("the directory holds %v after the delete", got)
		}
	})

	t.Run("platform roles, throttle rows, transactions, idempotency keys", func(t *testing.T) {
		u := user("local", nil)
		wantOK(t, "a platform role", exec(`INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'platform-admin')`, u))
		wantCode(t, "a platform role twice", exec(`INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'platform-admin')`, u), "23505")
		wantCode(t, "a role that is not a name", exec(`INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'Platform Admin')`, user("local", nil)), "23514")

		hash32 := "sha256('x'::bytea)"
		wantOK(t, "a throttle row", exec(`INSERT INTO pandokos.signin_throttle (key, kind, failures) VALUES (`+hash32+`, 'account', 3)`))
		wantCode(t, "a key of 31 bytes", exec(`INSERT INTO pandokos.signin_throttle (key, kind) VALUES (substr(`+hash32+`, 1, 31), 'account')`), "23514")
		wantCode(t, "an unknown kind", exec(`INSERT INTO pandokos.signin_throttle (key, kind) VALUES (sha256('y'::bytea), 'address')`), "23514")
		wantCode(t, "failures out of range", exec(`INSERT INTO pandokos.signin_throttle (key, kind, failures) VALUES (sha256('z'::bytea), 'source', 1000001)`), "23514")

		wantOK(t, "a sign-in transaction of ten minutes", exec(`INSERT INTO pandokos.oidc_login (state_hash, expires_at) VALUES (sha256('s1'::bytea), now() + interval '10 minutes')`))
		wantCode(t, "a transaction of two hours", exec(`INSERT INTO pandokos.oidc_login (state_hash, expires_at) VALUES (sha256('s2'::bytea), now() + interval '2 hours')`), "23514")
		wantCode(t, "a transaction that ended", exec(`INSERT INTO pandokos.oidc_login (state_hash, expires_at) VALUES (sha256('s3'::bytea), now() - interval '1 minute')`), "23514")

		key := func(k string, tid any, kind, principal string) error {
			return exec(`INSERT INTO pandokos.idempotency_key (tenant_id, principal_kind, principal_id, key, operation, salt, request_hash, locked_until, expires_at)
				VALUES ($1::uuid, $2, $3, $4, 'createTenant', substr(sha256('a'::bytea), 1, 16), sha256('b'::bytea), now(), now() + interval '1 day')`, tid, kind, principal, k)
		}
		wantOK(t, "a key of the platform", key("k-1", nil, "platform", "p1"))
		wantCode(t, "the same key of the same principal", key("k-1", nil, "platform", "p1"), "23505")
		wantOK(t, "the same key of another principal", key("k-1", nil, "platform", "p2"))
		wantOK(t, "the same key in a tenant", key("k-1", tenantID, "platform", "p1"))
		wantCode(t, "a key with a space", key("has space", nil, "platform", "p3"), "23514")
		wantCode(t, "a key of 129 characters", key(strings.Repeat("k", 129), nil, "platform", "p3"), "23514")
		wantCode(t, "an unknown principal kind", key("k-2", nil, "robot", "p3"), "23514")
		wantCode(t, "a principal ID with a space", key("k-3", nil, "user", "p 3"), "23514")
		wantCode(t, "a completed response without a commit mark", exec(`UPDATE pandokos.idempotency_key SET completed_at = now(), response_status = 201, response_body = '{}' WHERE key = 'k-1' AND principal_id = 'p1' AND tenant_id IS NULL`), "23514")
		wantOK(t, "a completed response with the commit mark", exec(`UPDATE pandokos.idempotency_key SET committed_at = now(), completed_at = now(), response_status = 201, response_body = '{}' WHERE key = 'k-1' AND principal_id = 'p2'`))
		wantCode(t, "a response that is not an object", exec(`UPDATE pandokos.idempotency_key SET committed_at = now(), completed_at = now(), response_status = 201, response_body = '[]' WHERE key = 'k-1' AND principal_id = 'p1' AND tenant_id IS NULL`), "23514")
	})
}

// TestManagingRule is T-774: the rule of D-1.
func TestManagingRule(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	c := d.Super(t)
	exec := func(q string, args ...any) error {
		_, err := c.Exec(ctx(t), q, args...)
		return err
	}
	scan := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := c.QueryRow(ctx(t), q, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := scan(`INSERT INTO pandokos.tenant (name) VALUES ('rule-a') RETURNING id::text`)
	b := scan(`INSERT INTO pandokos.tenant (name) VALUES ('rule-b') RETURNING id::text`)
	managed := scan(`INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ('local', $1::uuid) RETURNING id::text`, a)
	shared := scan(`INSERT INTO pandokos.app_user (sign_in) VALUES ('local') RETURNING id::text`)
	federated := scan(`INSERT INTO pandokos.app_user (sign_in) VALUES ('oidc') RETURNING id::text`)

	// A tenant-managed user is a member of its tenant only.
	wantOK(t, "a member of the managing tenant", exec(`INSERT INTO pandokos.membership (tenant_id, user_id) VALUES ($1::uuid, $2::uuid)`, a, managed))
	wantCode(t, "a member of another tenant", exec(`INSERT INTO pandokos.membership (tenant_id, user_id) VALUES ($1::uuid, $2::uuid)`, b, managed), "23514")
	// And holds no platform role.
	wantCode(t, "a platform role", exec(`INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'platform-admin')`, managed), "23514")
	// A platform-managed user can be a member of two tenants, and hold a platform role.
	wantOK(t, "a member of the first tenant", exec(`INSERT INTO pandokos.membership (tenant_id, user_id) VALUES ($1::uuid, $2::uuid)`, a, shared))
	wantOK(t, "a member of the second tenant", exec(`INSERT INTO pandokos.membership (tenant_id, user_id) VALUES ($1::uuid, $2::uuid)`, b, shared))
	wantOK(t, "a platform role", exec(`INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'platform-admin')`, shared))
	// A federated user is always managed by the platform.
	wantCode(t, "a federated user managed by a tenant", exec(`INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ('oidc', $1::uuid)`, a), "23514")
	wantOK(t, "a federated user is a member and holds a role", exec(`INSERT INTO pandokos.membership (tenant_id, user_id) VALUES ($1::uuid, $2::uuid)`, b, federated))
	wantOK(t, "a federated user's platform role", exec(`INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'platform-admin')`, federated))
	// The managing tenant cannot be changed to make the rule void.
	wantCode(t, "a change of the managing tenant", exec(`UPDATE pandokos.app_user SET tenant_id = $2::uuid WHERE id = $1::uuid`, managed, b), "23514")
	wantCode(t, "a change of the managing tenant of a platform user", exec(`UPDATE pandokos.app_user SET tenant_id = $2::uuid WHERE id = $1::uuid`, shared, a), "23514")
}

// TestPersonalDataIsMinimal is T-776: the six tables about people hold the columns of the data model
// and nothing that identifies a place, a device or a token.
func TestPersonalDataIsMinimal(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	want := map[string]string{
		"app_user":        "created_at,display_name,email,id,sign_in,state,state_changed_at,tenant_id,updated_at",
		"local_account":   "created_at,must_change,password_changed_at,password_expires_at,password_hash,sign_in,updated_at,user_id,username",
		"user_identity":   "created_at,issuer,sign_in,subject,updated_at,user_id",
		"user_session":    "created_at,expires_at,id,last_used_at,method,token_hash,user_id",
		"membership":      "created_at,id,source,tenant_id,updated_at,user_id",
		"membership_role": "created_at,id,role,source,tenant_id,updated_at,user_id",
	}
	for table, cols := range want {
		rows, err := d.Super(t).Query(ctx(t), `SELECT column_name FROM information_schema.columns WHERE table_schema = 'pandokos' AND table_name = $1 ORDER BY column_name`, table)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			got = append(got, s)
		}
		rows.Close()
		if strings.Join(got, ",") != cols {
			t.Errorf("%s has the columns %v, want %s", table, got, cols)
		}
		for _, c := range got {
			for _, banned := range []string{"ip", "address", "agent", "browser", "device", "location", "phone", "token", "cookie"} {
				if strings.Contains(c, banned) && c != "token_hash" {
					t.Errorf("%s.%s names %q", table, c, banned)
				}
			}
		}
	}
}
