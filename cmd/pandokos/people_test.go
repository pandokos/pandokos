package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/test/dbtest"
)

var (
	rePasswordLine = regexp.MustCompile(`(?m)^password: ([a-z2-7]{24})$`)
	reNoKeyYet     = regexp.MustCompile(`(?m)^key:\s+pdk_`)
)

func count(t *testing.T, d *dbtest.Database, sql string, args ...any) int {
	t.Helper()
	return d.Count(t, sql, args...)
}

// verifies reports whether the password of the account in the database is pw.
func verifies(t *testing.T, d *dbtest.Database, username, pw string) bool {
	t.Helper()
	var phc string
	if err := d.Super(t).QueryRow(context.Background(), "SELECT password_hash FROM pandokos.local_account WHERE username = $1", username).Scan(&phc); err != nil {
		t.Fatalf("the account %s: %v", username, err)
	}
	h, err := password.New(dbtest.FloorParams, 1)
	if err != nil {
		t.Fatal(err)
	}
	ok, _, err := h.Verify(context.Background(), phc, secret.New(pw))
	return err == nil && ok
}

// TestInitAdminUser is T-828.
func TestInitAdminUser(t *testing.T) {
	t.Run("the first administrator", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		env := d.Env(t)
		code, out, errs := runCmd(t, env, "init", "--admin-username", "ada.lovelace")
		if code != 0 {
			t.Fatalf("init: %d\n%s\n%s", code, out, errs)
		}
		m := rePasswordLine.FindStringSubmatch(out)
		if m == nil || !strings.Contains(out, "admin user: ada.lovelace") || strings.Count(out, m[1]) != 1 || strings.Contains(errs, m[1]) {
			t.Fatalf("the password must be printed once on stdout, with the name of the user:\nstdout %q\nstderr %q", out, errs)
		}
		if !verifies(t, d, "ada.lovelace", m[1]) {
			t.Error("the printed password is not the password of the account")
		}
		var sign, state string
		var tenantID *string
		var must bool
		var expires *time.Time
		var role, source string
		err := d.Super(t).QueryRow(context.Background(), `SELECT u.sign_in, u.state, u.tenant_id::text, a.must_change, a.password_expires_at, r.role, r.source
			FROM pandokos.app_user u JOIN pandokos.local_account a ON a.user_id = u.id JOIN pandokos.platform_role_assignment r ON r.user_id = u.id
			WHERE a.username = 'ada.lovelace'`).Scan(&sign, &state, &tenantID, &must, &expires, &role, &source)
		if err != nil {
			t.Fatalf("the account: %v", err)
		}
		if sign != "local" || state != "active" || tenantID != nil || !must || role != "platform-admin" || source != "manual" || expires == nil ||
			time.Until(*expires) < 71*time.Hour || time.Until(*expires) > 73*time.Hour {
			t.Errorf("account %s %s %v must_change=%v %s %s expires %v", sign, state, tenantID, must, role, source, expires)
		}
		// The audit rows of the platform log, in the transaction of the key: the host did it.
		actions := map[string]string{}
		for _, r := range d.PlatformAuditRows(t) {
			actions[r.Action] = r.ActorType
		}
		for _, a := range []string{"platform_key.bootstrap", "user.create", "platform_role.assign"} {
			if actions[a] != "host" {
				t.Errorf("audit: %s by %q, want host (%v)", a, actions[a], actions)
			}
		}
		if n := count(t, d, "SELECT count(*) FROM pandokos.platform_key"); n != 1 {
			t.Errorf("%d platform keys", n)
		}
		all := d.AllAuditText(t)
		if strings.Contains(all, m[1]) || strings.Contains(all, "$argon2id") {
			t.Error("the audit log holds the password or its hash")
		}
	})

	t.Run("the password goes to a new file of mode 0600", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		env := d.Env(t)
		file := filepath.Join(t.TempDir(), "admin.password")
		code, out, errs := runCmd(t, env, "init", "--admin-username", "ada.lovelace", "--password-out", file)
		if code != 0 {
			t.Fatalf("init: %d\n%s\n%s", code, out, errs)
		}
		b, err := os.ReadFile(file)
		st, serr := os.Stat(file)
		if err != nil || serr != nil || st.Mode().Perm() != 0o600 || !regexp.MustCompile(`^[a-z2-7]{24}\n$`).Match(b) {
			t.Fatalf("out-file: %v %v %q", err, serr, b)
		}
		pw := strings.TrimSpace(string(b))
		if strings.Contains(out+errs, pw) || !strings.Contains(out, "password written to "+file) || !verifies(t, d, "ada.lovelace", pw) {
			t.Errorf("stdout %q stderr %q", out, errs)
		}
	})

	t.Run("refused when a user with a platform role exists", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		env := d.Env(t)
		if code, out, errs := runCmd(t, env, "init", "--admin-username", "ada.lovelace"); code != 0 {
			t.Fatalf("init: %d %s %s", code, out, errs)
		}
		keys, users := count(t, d, "SELECT count(*) FROM pandokos.platform_key"), count(t, d, "SELECT count(*) FROM pandokos.app_user")
		code, out, errs := runCmd(t, env, "init", "--recover", "--admin-username", "grace.hopper")
		if code != 1 || rePasswordLine.MatchString(out) || reNoKeyYet.MatchString(out) || !strings.Contains(errs, "platform role") {
			t.Errorf("a second administrator: %d %q %q", code, out, errs)
		}
		if n := count(t, d, "SELECT count(*) FROM pandokos.platform_key"); n != keys {
			t.Errorf("the refused init left %d new keys: the key is rolled back with the user", n-keys)
		}
		if n := count(t, d, "SELECT count(*) FROM pandokos.app_user"); n != users {
			t.Error("the refused init created a user")
		}
	})

	t.Run("without the flag nothing is created and the hint is printed", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		code, out, errs := runCmd(t, d.Env(t), "init")
		if code != 0 {
			t.Fatalf("init: %d %s %s", code, out, errs)
		}
		if n := count(t, d, "SELECT count(*) FROM pandokos.app_user"); n != 0 {
			t.Errorf("%d users", n)
		}
		if !regexp.MustCompile(`(?m)^hint: .*pandokos init --admin-username`).MatchString(errs) {
			t.Errorf("stderr lacks the hint line about --admin-username: %q", errs)
		}
		if strings.Contains(out, "password") {
			t.Errorf("stdout %q", out)
		}
	})

	t.Run("a failure rolls the key back too", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		env := d.Env(t)
		file := filepath.Join(t.TempDir(), "taken")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errs := runCmd(t, env, "init", "--admin-username", "ada.lovelace", "--password-out", file)
		if code != 1 || strings.Contains(out+errs, "pdk_") {
			t.Errorf("an existing password file: %d %q %q", code, out, errs)
		}
		if n := count(t, d, "SELECT count(*) FROM pandokos.platform_key"); n != 0 {
			t.Errorf("%d platform keys after a failure", n)
		}
		if n := count(t, d, "SELECT count(*) FROM pandokos.app_user"); n != 0 {
			t.Errorf("%d users after a failure", n)
		}
		if b, _ := os.ReadFile(file); string(b) != "x" {
			t.Error("the existing file was overwritten")
		}
		// A username that is not valid is a usage error before anything happens.
		for _, name := range []string{"Bad Name", "A", "x", "UPPER.case", ""} {
			if code, _, _ := runCmd(t, env, "init", "--admin-username", name); code != 2 {
				t.Errorf("--admin-username %q: exit %d, want 2", name, code)
			}
		}
		if code, _, _ := runCmd(t, env, "init", "--password-out", filepath.Join(t.TempDir(), "p")); code != 2 {
			t.Errorf("--password-out without --admin-username: exit %d, want 2", code)
		}
		if n := count(t, d, "SELECT count(*) FROM pandokos.platform_key"); n != 0 {
			t.Errorf("%d platform keys after usage errors", n)
		}
	})
}

// TestUserHostCommands is T-829.
func TestUserHostCommands(t *testing.T) {
	d := dbtest.NewMigrated(t)
	env := d.Env(t)
	tid := d.SeedTenant(t, "acme")
	plat := d.PlatformUser(t, "plat.person", []string{"platform-admin"})
	mine := d.TenantUser(t, tid, "tenant.person", []string{"tenant-admin"})
	lock := func(username string) {
		t.Helper()
		if _, err := d.Super(t).Exec(context.Background(), `INSERT INTO pandokos.signin_throttle (key, kind, failures, locked_until)
			VALUES (sha256(decode('6163636f756e7400', 'hex') || convert_to($1, 'UTF8')), 'account', 10, now() + interval '15 minutes')`, username); err != nil {
			t.Fatal(err)
		}
	}
	sessions := func(id string) int {
		return count(t, d, "SELECT count(*) FROM pandokos.user_session WHERE user_id = $1::uuid", id)
	}

	for _, c := range []struct {
		who    dbtest.Person
		tenant bool
	}{{plat, false}, {mine, true}} {
		t.Run("reset-password of "+c.who.Username, func(t *testing.T) {
			tok1 := make([]byte, 32)
			tok1[0] = byte(len(c.who.Username))
			d.Session(t, c.who.ID, "local", tok1, time.Hour)
			lock(c.who.Username)
			code, out, errs := runCmd(t, env, "user", "reset-password", strings.ToUpper(c.who.Username[:1])+c.who.Username[1:]) // the username is case-insensitive
			if code != 0 {
				t.Fatalf("exit %d\n%s\n%s", code, out, errs)
			}
			m := rePasswordLine.FindStringSubmatch(out)
			if m == nil || strings.Count(out, m[1]) != 1 || strings.Contains(errs, m[1]) {
				t.Fatalf("the password must be printed once on stdout: %q %q", out, errs)
			}
			if !verifies(t, d, c.who.Username, m[1]) || verifies(t, d, c.who.Username, c.who.Password) {
				t.Error("the account does not have the printed password")
			}
			var must bool
			var expires *time.Time
			if err := d.Super(t).QueryRow(context.Background(), "SELECT must_change, password_expires_at FROM pandokos.local_account WHERE username = $1", c.who.Username).Scan(&must, &expires); err != nil {
				t.Fatal(err)
			}
			if !must || expires == nil || time.Until(*expires) < 71*time.Hour {
				t.Errorf("must_change=%v expires=%v", must, expires)
			}
			if n := sessions(c.who.ID); n != 0 {
				t.Errorf("%d sessions after the reset", n)
			}
			for _, r := range d.ThrottleRows(t) {
				if r.Kind == "account" && r.LockedUntil != nil {
					t.Error("the throttle row survived the reset")
				}
			}
			// Audited with the actor host, in the tenant's log too for a user that a tenant manages.
			found := func(rows []dbtest.AuditRow) bool {
				for _, r := range rows {
					if r.Action == "user.password_reset" && r.ResourceID == c.who.ID && r.ActorType == "host" && !strings.Contains(r.Raw, m[1]) {
						return true
					}
				}
				return false
			}
			if !found(d.PlatformAuditRows(t)) {
				t.Error("no user.password_reset row of the host in the platform log")
			}
			if c.tenant != found(d.AuditRows(t, tid)) {
				t.Errorf("the row in the tenant's log: want %v", c.tenant)
			}
		})
		t.Run("unlock of "+c.who.Username, func(t *testing.T) {
			lock(c.who.Username)
			code, out, errs := runCmd(t, env, "user", "unlock", c.who.Username)
			if code != 0 || strings.Contains(out+errs, "password") {
				t.Fatalf("exit %d\n%s\n%s", code, out, errs)
			}
			for _, r := range d.ThrottleRows(t) {
				if r.Kind == "account" {
					t.Errorf("a throttle row of an account survived the unlock: %+v", r)
				}
			}
			found := func(rows []dbtest.AuditRow) bool {
				for _, r := range rows {
					if r.Action == "user.unlock" && r.ResourceID == c.who.ID && r.ActorType == "host" {
						return true
					}
				}
				return false
			}
			if !found(d.PlatformAuditRows(t)) || c.tenant != found(d.AuditRows(t, tid)) {
				t.Error("the unlock is not audited as the host's, in the platform log and, for a tenant's user, in the tenant's")
			}
		})
	}

	t.Run("the password goes to a new file of mode 0600", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "reset.password")
		code, out, errs := runCmd(t, env, "user", "reset-password", "--password-out", file, plat.Username)
		b, err := os.ReadFile(file)
		st, _ := os.Stat(file)
		if code != 0 || err != nil || st.Mode().Perm() != 0o600 || strings.Contains(out+errs, strings.TrimSpace(string(b))) || !verifies(t, d, plat.Username, strings.TrimSpace(string(b))) {
			t.Errorf("exit %d %v\n%s\n%s", code, err, out, errs)
		}
		if code, _, _ := runCmd(t, env, "user", "reset-password", "--password-out", file, plat.Username); code != 1 {
			t.Errorf("an existing file: exit %d, want 1", code)
		}
	})

	t.Run("an unknown username exits 1 and says no more", func(t *testing.T) {
		before := len(d.PlatformAuditRows(t))
		for _, args := range [][]string{{"user", "reset-password", "nobody.here"}, {"user", "unlock", "nobody.here"}} {
			code, out, errs := runCmd(t, env, args...)
			if code != 1 || out != "" || strings.Contains(errs, "nobody.here") || strings.Contains(errs, "tenant") || strings.Contains(errs, "platform") {
				t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, errs)
			}
		}
		if len(d.PlatformAuditRows(t)) != before {
			t.Error("a command about an unknown username wrote an audit row")
		}
	})

	t.Run("usage", func(t *testing.T) {
		for _, args := range [][]string{{"user"}, {"user", "frobnicate"}, {"user", "reset-password"}, {"user", "unlock"}, {"user", "unlock", "a", "b"}, {"user", "unlock", "--bogus", "x"}} {
			if code, out, errs := runCmd(t, env, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, errs)
			}
		}
	})
}

// TestPeopleHostUsage is the part of the changed tests that reads the usage text and the groups.
func TestPeopleHostUsage(t *testing.T) {
	code, out, _ := runCmd(t, nil, "help")
	if code != 0 {
		t.Fatalf("help: %d", code)
	}
	for _, want := range []string{
		"[--recover] [--admin-username NAME] [--password-out FILE]",
		"user reset-password USERNAME [--config FILE] [--password-out FILE]",
		"user unlock USERNAME [--config FILE]",
		"platform-user list|create|get|update|disable|enable|reset-password|unlock|revoke-sessions",
		"member list|add|get|update|remove|disable|enable|reset-password|unlock|revoke-sessions",
		"permission list",
		"role builtin|list|create|get|update|deactivate|activate|delete",
		"tenant-support open|list|get|close",
		"support-action list|get|end",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the usage text lacks %q", want)
		}
	}
	for _, args := range [][]string{{"platform-user"}, {"member"}, {"platform-user", "frobnicate"}, {"member", "frobnicate"}} {
		if code, out, errs := runCmd(t, nil, args...); code != 2 || out != "" || errs == "" {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
		}
	}
}
