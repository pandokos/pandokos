package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// The seeds of people (sprint 4.2) insert rows as the admin role, through the statements of
// internal/store/queries, with a hash that internal/password makes at the floor parameters
// (m=19456, t=2, p=1) so that a suite that signs in many times stays fast. Passwords are made
// when the test runs; no password is written down.

var (
	floorOnce   sync.Once
	floorHasher *password.Hasher
	floorErr    error
)

// FloorParams are the cheapest Argon2id parameters the configuration accepts.
var FloorParams = password.Params{MemoryKiB: 19456, Iterations: 2, Parallelism: 1}

func floor(tb testing.TB) *password.Hasher {
	tb.Helper()
	floorOnce.Do(func() { floorHasher, floorErr = password.New(FloorParams, 8) })
	if floorErr != nil {
		tb.Fatalf("seed: the password hasher: %v", floorErr)
	}
	return floorHasher
}

// NewPassword returns a random password of 20 characters that satisfies the policy.
func NewPassword(tb testing.TB) string {
	tb.Helper()
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// Person is a seeded user.
type Person struct {
	ID       string
	Username string // empty for a federated user
	Password string // empty for a federated user
	Subject  string // of a federated user
	TenantID tenant.ID
}

// PersonOption changes what a seed inserts.
type PersonOption func(*personSeed)

type personSeed struct {
	mustChange bool
	expires    *time.Time
	disabled   bool
	display    string
	email      string
	password   string
	source     string
}

// MustChange gives the account the flag of a generated password, expiring at the time (zero: no
// expiry).
func MustChange(expires time.Time) PersonOption {
	return func(p *personSeed) {
		p.mustChange = true
		if !expires.IsZero() {
			p.expires = &expires
		}
	}
}

// Disabled makes the user disabled.
func Disabled() PersonOption { return func(p *personSeed) { p.disabled = true } }

// Display sets the display name and the e-mail address.
func Display(name, email string) PersonOption {
	return func(p *personSeed) { p.display, p.email = name, email }
}

// WithPassword uses the given password instead of a random one.
func WithPassword(pw string) PersonOption { return func(p *personSeed) { p.password = pw } }

// FromIdP marks the roles and memberships of the seed as kept by the group rules.
func FromIdP() PersonOption { return func(p *personSeed) { p.source = "idp" } }

func seedOf(opts []PersonOption) personSeed {
	s := personSeed{source: "manual"}
	for _, o := range opts {
		o(&s)
	}
	return s
}

func (d *Database) admin(tb testing.TB) *store.DB {
	tb.Helper()
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if d.s.adminDB == nil {
		d.s.adminDB = d.Open(d.s.root, store.RoleAdmin)
	}
	return d.s.adminDB
}

func (d *Database) insertUser(tb testing.TB, tid *tenant.ID, username string, opts []PersonOption) Person {
	tb.Helper()
	s := seedOf(opts)
	pw := s.password
	if pw == "" {
		pw = NewPassword(tb)
	}
	ctx, cancel := ctx30()
	defer cancel()
	phc, err := floor(tb).Hash(ctx, secret.New(pw))
	if err != nil {
		tb.Fatalf("seed user: hash: %v", err)
	}
	p := Person{Username: username, Password: pw}
	var managing *string
	if tid != nil {
		v := tid.String()
		managing = &v
		p.TenantID = *tid
	}
	err = d.admin(tb).Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		u, err := q.CreateUser(ctx, queries.CreateUserParams{SignIn: "local", TenantID: managing, DisplayName: s.display, Email: s.email})
		if err != nil {
			return err
		}
		p.ID = u.ID
		if err := q.CreateLocalAccount(ctx, queries.CreateLocalAccountParams{
			UserID: u.ID, Username: username, PasswordHash: phc, MustChange: s.mustChange, PasswordExpiresAt: s.expires,
		}); err != nil {
			return err
		}
		if s.disabled {
			_, err = q.SetUserState(ctx, queries.SetUserStateParams{ID: u.ID, State: "disabled"})
		}
		return err
	})
	if err != nil {
		tb.Fatalf("seed user %s: %v", username, err)
	}
	return p
}

// PlatformUser inserts an active local user that the platform manages, holding the platform roles.
func (d *Database) PlatformUser(tb testing.TB, username string, roles []string, opts ...PersonOption) Person {
	tb.Helper()
	p := d.insertUser(tb, nil, username, opts)
	s := seedOf(opts)
	ctx, cancel := ctx30()
	defer cancel()
	err := d.admin(tb).Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		for _, r := range roles {
			if err := queries.New(tx).AddPlatformRole(ctx, queries.AddPlatformRoleParams{UserID: p.ID, Role: r, Source: s.source}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatalf("seed platform roles: %v", err)
	}
	return p
}

// TenantUser inserts an active local user that tenant tid manages and makes it a member with the
// roles.
func (d *Database) TenantUser(tb testing.TB, tid tenant.ID, username string, roles []string, opts ...PersonOption) Person {
	tb.Helper()
	p := d.insertUser(tb, &tid, username, opts)
	d.Member(tb, tid, p.ID, roles, opts...)
	return p
}

// Member makes an existing user a member of the tenant with the roles.
func (d *Database) Member(tb testing.TB, tid tenant.ID, userID string, roles []string, opts ...PersonOption) {
	tb.Helper()
	s := seedOf(opts)
	ctx, cancel := ctx30()
	defer cancel()
	err := d.admin(tb).WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if _, err := q.CreateMembership(ctx, queries.CreateMembershipParams{TenantID: tid.String(), UserID: userID, Source: s.source}); err != nil {
			return err
		}
		for _, r := range roles {
			if err := q.AddMembershipRole(ctx, queries.AddMembershipRoleParams{TenantID: tid.String(), UserID: userID, Role: r, Source: s.source}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatalf("seed member: %v", err)
	}
}

// FederatedUser inserts an active user that signs in through the identity provider issuer, with the
// subject, managed by the platform and holding the platform roles.
func (d *Database) FederatedUser(tb testing.TB, issuer, subject string, roles []string, opts ...PersonOption) Person {
	tb.Helper()
	s := seedOf(opts)
	ctx, cancel := ctx30()
	defer cancel()
	p := Person{Subject: subject}
	err := d.admin(tb).Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		u, err := q.CreateUser(ctx, queries.CreateUserParams{SignIn: "oidc", DisplayName: s.display, Email: s.email})
		if err != nil {
			return err
		}
		p.ID = u.ID
		if err := q.CreateUserIdentity(ctx, queries.CreateUserIdentityParams{UserID: u.ID, Issuer: issuer, Subject: subject}); err != nil {
			return err
		}
		for _, r := range roles {
			if err := q.AddPlatformRole(ctx, queries.AddPlatformRoleParams{UserID: u.ID, Role: r, Source: s.source}); err != nil {
				return err
			}
		}
		if s.disabled {
			_, err = q.SetUserState(ctx, queries.SetUserStateParams{ID: u.ID, State: "disabled"})
		}
		return err
	})
	if err != nil {
		tb.Fatalf("seed federated user: %v", err)
	}
	return p
}

// Session inserts a session of the user for a token hash and returns its ID; created, last used and
// expiry are the given ages: the session was created `age` ago, last used `idle` ago, and expires
// `lifetime` after its creation.
func (d *Database) Session(tb testing.TB, userID, method string, tokenHash []byte, lifetime time.Duration) string {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	var id string
	err := d.admin(tb).Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		row, err := queries.New(tx).CreateSession(ctx, queries.CreateSessionParams{
			TokenHash: tokenHash, UserID: userID, Method: method, ExpiresAt: time.Now().Add(lifetime).UTC().Truncate(time.Microsecond),
		})
		id = row.ID
		return err
	})
	if err != nil {
		tb.Fatalf("seed session: %v", err)
	}
	return id
}

// Age moves the listed timestamp columns of the rows of table (of the schema pandokos) that satisfy
// where, as the owner, by d into the past. Where SQL uses now(), tests move timestamps instead of
// the clock. An empty where means every row.
func (d *Database) Age(tb testing.TB, table, where string, by time.Duration, columns ...string) int {
	tb.Helper()
	if len(columns) == 0 {
		tb.Fatal("dbtest.Age: no column")
	}
	sets := make([]string, len(columns))
	for i, c := range columns {
		sets[i] = fmt.Sprintf("%s = %s - make_interval(secs => %f)", c, c, by.Seconds())
	}
	sql := "UPDATE pandokos." + table + " SET " + strings.Join(sets, ", ")
	if where != "" {
		sql += " WHERE " + where
	}
	ctx, cancel := ctx30()
	defer cancel()
	tag, err := d.Super(tb).Exec(ctx, sql)
	if err != nil {
		tb.Fatalf("dbtest.Age %s: %v", table, err)
	}
	return int(tag.RowsAffected())
}

// AuditRow is one row of an audit log.
type AuditRow struct {
	Action, Outcome, ActorType, ActorID, ActorName, KeyID string
	ResourceType, ResourceID, Reason, RequestID           string
	// SupportActionID is the support action a tenant's row was written under (sprint 4.3); empty
	// for none, and always empty in the platform's log.
	SupportActionID string
	Detail          map[string]any
	// Raw is the whole row as JSON, for the tests that search the log for a value.
	Raw string
}

// AuditRows returns the rows of a tenant's audit log, oldest first, read as the owner with the
// tenant set; for the platform's log use PlatformAuditRows.
func (d *Database) AuditRows(tb testing.TB, tid tenant.ID) []AuditRow {
	tb.Helper()
	return d.auditRows(tb, `SELECT action, outcome, actor_type, actor_id, actor_name, key_id, resource_type, resource_id, reason, request_id, detail::text,
		COALESCE(support_action_id::text, ''), to_jsonb(e)::text FROM pandokos.audit_event e WHERE tenant_id = $1::uuid ORDER BY id`, tid.String())
}

// PlatformAuditRows returns the rows of the platform's audit log, oldest first.
func (d *Database) PlatformAuditRows(tb testing.TB) []AuditRow {
	tb.Helper()
	return d.auditRows(tb, `SELECT action, outcome, actor_type, actor_id, actor_name, key_id, resource_type, resource_id, reason, request_id, detail::text,
		'', to_jsonb(e)::text FROM pandokos.platform_audit_event e ORDER BY id`)
}

// AllAuditText returns every audit row of every log as JSON text, for the searches of the canary
// tests.
func (d *Database) AllAuditText(tb testing.TB) string {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	var out []string
	for _, q := range []string{"SELECT to_jsonb(e)::text FROM pandokos.audit_event e", "SELECT to_jsonb(e)::text FROM pandokos.platform_audit_event e"} {
		rows, err := d.Super(tb).Query(ctx, q)
		if err != nil {
			tb.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				tb.Fatal(err)
			}
			out = append(out, s)
		}
		rows.Close()
	}
	return strings.Join(out, "\n")
}

// FailureRows returns the rows of the tenant's log (or, for a zero tid, of the platform's) whose
// action is auth.failure.
func (d *Database) FailureRows(tb testing.TB, tid tenant.ID) []AuditRow {
	tb.Helper()
	var all []AuditRow
	if tid.IsZero() {
		all = d.PlatformAuditRows(tb)
	} else {
		all = d.AuditRows(tb, tid)
	}
	var out []AuditRow
	for _, r := range all {
		if r.Action == "auth.failure" {
			out = append(out, r)
		}
	}
	return out
}

func (d *Database) auditRows(tb testing.TB, sql string, args ...any) []AuditRow {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	rows, err := d.Super(tb).Query(ctx, sql, args...)
	if err != nil {
		tb.Fatalf("audit rows: %v", err)
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		var detail string
		if err := rows.Scan(&r.Action, &r.Outcome, &r.ActorType, &r.ActorID, &r.ActorName, &r.KeyID, &r.ResourceType, &r.ResourceID, &r.Reason, &r.RequestID, &detail, &r.SupportActionID, &r.Raw); err != nil {
			tb.Fatal(err)
		}
		if err := json.Unmarshal([]byte(detail), &r.Detail); err != nil {
			tb.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}
	return out
}

// Throttle describes a row of signin_throttle.
type Throttle struct {
	Kind        string
	Failures    int
	LockedUntil *time.Time
}

// ThrottleRows returns the rows of signin_throttle, as the owner.
func (d *Database) ThrottleRows(tb testing.TB) []Throttle {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	rows, err := d.Super(tb).Query(ctx, "SELECT kind, failures, locked_until FROM pandokos.signin_throttle ORDER BY kind, failures")
	if err != nil {
		tb.Fatal(err)
	}
	defer rows.Close()
	var out []Throttle
	for rows.Next() {
		var t Throttle
		var lu pgtype.Timestamptz
		if err := rows.Scan(&t.Kind, &t.Failures, &lu); err != nil {
			tb.Fatal(err)
		}
		if lu.Valid {
			v := lu.Time
			t.LockedUntil = &v
		}
		out = append(out, t)
	}
	return out
}
