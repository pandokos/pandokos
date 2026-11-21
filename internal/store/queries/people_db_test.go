package queries_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// peopleStatements covers the 52 statements of sprint 4.2: users, local accounts, identities,
// platform roles, memberships, sessions, sign-in throttling, OIDC transactions and idempotency
// keys. Each runs in the session it is written for (the platform scope, or a tenant's session)
// and what goes in comes back unchanged.
func peopleStatements(c context.Context, t *testing.T, admin *store.DB, tid tenant.ID, tenantID string, called map[string]bool) {
	t.Helper()
	mark := func(name string) { called[name] = true }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	try := func(what string, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		return nil
	}
	// A PHC string of the shape the table accepts; no password hashes to it.
	phc := func(seed byte) string {
		return "$argon2id$v=19$m=19456,t=2,p=1$" + strings.Repeat(string(rune('A'+seed%26)), 22) + "$" + strings.Repeat(string(rune('a'+seed%26)), 43)
	}
	hash := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	interval := func(d time.Duration) pgtype.Interval {
		return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
	}
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)

	var platformUser, federatedUser, tenantUser, memberUser string
	username := "ada-" + tenantID[:8]

	// Platform scope: users, accounts, identities, platform roles.
	must("platform statements", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("CreateUser")
		u, err := q.CreateUser(c, queries.CreateUserParams{SignIn: "local", DisplayName: "Ada Ünï", Email: "ada@example.test"})
		if err != nil {
			return err
		}
		if u.TenantID != nil || u.State != "active" || u.DisplayName != "Ada Ünï" || len(u.ID) != 36 || u.CreatedAt.IsZero() {
			t.Errorf("CreateUser: %+v", u)
		}
		platformUser = u.ID
		mark("CreateLocalAccount")
		if err := q.CreateLocalAccount(c, queries.CreateLocalAccountParams{UserID: u.ID, Username: username, PasswordHash: phc(1), MustChange: true, PasswordExpiresAt: &expires}); err != nil {
			return err
		}
		mark("GetUser")
		got, err := q.GetUser(c, u.ID)
		if err != nil || got.Username != username || !got.MustChange || got.PasswordExpiresAt == nil || got.Subject != "" || len(got.PlatformRoles) != 0 || got.TenantID != nil {
			t.Errorf("GetUser: %+v %v", got, err)
		}
		noRowsUser := "00000000-0000-7000-8000-000000000000"
		if _, err := q.GetUser(c, noRowsUser); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetUser of an ID that does not exist: %v", err)
		}
		mark("LockUser")
		if l, err := q.LockUser(c, u.ID); err != nil || l.ID != u.ID || l.State != "active" || l.TenantID != nil {
			t.Errorf("LockUser: %+v %v", l, err)
		}
		mark("GetAccountForSignIn")
		acct, err := q.GetAccountForSignIn(c, username)
		if err != nil || acct.UID != u.ID || acct.ManagingTenantID != "" || acct.PasswordHash != phc(1) || !acct.MustChange || acct.Username != username {
			t.Errorf("GetAccountForSignIn: %+v %v", acct, err)
		}
		if _, err := q.GetAccountForSignIn(c, "nobody"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetAccountForSignIn of an unknown username: %v", err)
		}
		mark("UpdateUser")
		if n, err := q.UpdateUser(c, queries.UpdateUserParams{ID: u.ID, SetDisplayName: true, DisplayName: "Ada L."}); err != nil || n != 1 {
			t.Errorf("UpdateUser: %d %v", n, err)
		}
		if got, err := q.GetUser(c, u.ID); err != nil || got.DisplayName != "Ada L." || got.Email != "ada@example.test" {
			t.Errorf("after UpdateUser: %+v %v (the email was not given and must stay)", got, err)
		}
		mark("SetUserState")
		if n, err := q.SetUserState(c, queries.SetUserStateParams{ID: u.ID, State: "disabled"}); err != nil || n != 1 {
			t.Errorf("SetUserState: %d %v", n, err)
		}
		if got, err := q.GetUser(c, u.ID); err != nil || got.State != "disabled" || got.StateChangedAt.Before(got.CreatedAt) {
			t.Errorf("after SetUserState: %+v %v", got, err)
		}
		if n, err := q.SetUserState(c, queries.SetUserStateParams{ID: u.ID, State: "active"}); err != nil || n != 1 {
			t.Errorf("SetUserState back: %d %v", n, err)
		}
		mark("SetPassword")
		newExp := expires.Add(time.Hour)
		if n, err := q.SetPassword(c, queries.SetPasswordParams{UserID: u.ID, PasswordHash: phc(2), MustChange: true, PasswordExpiresAt: &newExp}); err != nil || n != 1 {
			t.Errorf("SetPassword: %d %v", n, err)
		}
		mark("UpgradePasswordHash")
		if n, err := q.UpgradePasswordHash(c, queries.UpgradePasswordHashParams{UserID: u.ID, OldHash: phc(1), NewHash: phc(3)}); err != nil || n != 0 {
			t.Errorf("UpgradePasswordHash with a hash that is no longer the stored one: %d %v", n, err)
		}
		if n, err := q.UpgradePasswordHash(c, queries.UpgradePasswordHashParams{UserID: u.ID, OldHash: phc(2), NewHash: phc(3)}); err != nil || n != 1 {
			t.Errorf("UpgradePasswordHash: %d %v", n, err)
		}
		if acct, err := q.GetAccountForSignIn(c, username); err != nil || acct.PasswordHash != phc(3) {
			t.Errorf("after the upgrade: %+v %v", acct, err)
		}
		mark("ListUsers")
		page, err := q.ListUsers(c, queries.ListUsersParams{Sort: "created_at", PageSize: 10, AfterID: "00000000-0000-7000-8000-000000000000"})
		if err != nil || len(page) != 1 || page[0].ID != u.ID || page[0].Username != username {
			t.Errorf("ListUsers: %+v %v", page, err)
		}
		if page, err := q.ListUsers(c, queries.ListUsersParams{Sort: "-username", PageSize: 10, State: "active", Username: username,
			AfterID: "00000000-0000-7000-8000-000000000000"}); err != nil || len(page) != 1 {
			t.Errorf("ListUsers by username: %+v %v", page, err)
		}
		if page, err := q.ListUsers(c, queries.ListUsersParams{Sort: "username", PageSize: 10, After: true, AfterUsername: username, AfterID: u.ID}); err != nil || len(page) != 0 {
			t.Errorf("ListUsers after the only user: %+v %v", page, err)
		}

		// A federated user, and its identity.
		fu, err := q.CreateUser(c, queries.CreateUserParams{SignIn: "oidc", DisplayName: "Fed"})
		if err != nil {
			return err
		}
		federatedUser = fu.ID
		mark("CreateUserIdentity")
		if err := q.CreateUserIdentity(c, queries.CreateUserIdentityParams{UserID: fu.ID, Issuer: "https://idp-4-2.example.test/realms/é", Subject: "sub-ünï"}); err != nil {
			return err
		}
		mark("GetUserByIdentity")
		byID, err := q.GetUserByIdentity(c, queries.GetUserByIdentityParams{Issuer: "https://idp-4-2.example.test/realms/é", Subject: "sub-ünï"})
		if err != nil || byID.ID != fu.ID || byID.SignIn != "oidc" || byID.Subject != "sub-ünï" {
			t.Errorf("GetUserByIdentity: %+v %v", byID, err)
		}
		if _, err := q.GetUserByIdentity(c, queries.GetUserByIdentityParams{Issuer: "https://idp-4-2.example.test/realms/é", Subject: "other"}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetUserByIdentity of an unknown subject: %v", err)
		}
		if g, err := q.GetUser(c, fu.ID); err != nil || g.Subject != "sub-ünï" || g.Username != "" {
			t.Errorf("GetUser of a federated user: %+v %v", g, err)
		}

		// Platform roles.
		mark("AddPlatformRole")
		if err := try("AddPlatformRole", q.AddPlatformRole(c, queries.AddPlatformRoleParams{UserID: u.ID, Role: "platform-admin", Source: "manual"})); err != nil {
			return err
		}
		if err := try("AddPlatformRole idp", q.AddPlatformRole(c, queries.AddPlatformRoleParams{UserID: fu.ID, Role: "platform-admin", Source: "idp"})); err != nil {
			return err
		}
		mark("ListPlatformRoles")
		if roles, err := q.ListPlatformRoles(c, u.ID); err != nil || !reflect.DeepEqual(roles, []queries.ListPlatformRolesRow{{Role: "platform-admin", Source: "manual"}}) {
			t.Errorf("ListPlatformRoles: %+v %v", roles, err)
		}
		mark("CountPlatformRoleHolders")
		if n, err := q.CountPlatformRoleHolders(c, "platform-admin"); err != nil || n != 2 {
			t.Errorf("CountPlatformRoleHolders: %d %v", n, err)
		}
		mark("RemovePlatformRole")
		if n, err := q.RemovePlatformRole(c, queries.RemovePlatformRoleParams{UserID: fu.ID, Role: "platform-admin"}); err != nil || n != 1 {
			t.Errorf("RemovePlatformRole: %d %v", n, err)
		}
		if n, err := q.CountPlatformRoleHolders(c, "platform-admin"); err != nil || n != 1 {
			t.Errorf("CountPlatformRoleHolders after the removal: %d %v", n, err)
		}
		if g, err := q.GetUser(c, u.ID); err != nil || !reflect.DeepEqual(g.PlatformRoles, []string{"platform-admin"}) {
			t.Errorf("GetUser with a role: %+v %v", g, err)
		}

		// The tenants by name, for the group rules.
		mark("TenantIDsByName")
		if ts, err := q.TenantIDsByName(c, []string{"no-such-tenant"}); err != nil || len(ts) != 0 {
			t.Errorf("TenantIDsByName of a missing name: %+v %v", ts, err)
		}
		if ts, err := q.TenantIDsByName(c, []string{}); err != nil || len(ts) != 0 {
			t.Errorf("TenantIDsByName of no name: %+v %v", ts, err)
		}

		// Sign-in throttling.
		k1, k2 := hash("account:"+username), hash("source:192.0.2.0/24")
		mark("CountFailure")
		cf := func(key []byte, kind string) queries.CountFailureRow {
			r, err := q.CountFailure(c, queries.CountFailureParams{Key: key, Kind: kind, FailureWindow: interval(time.Hour), MaxFailures: 2, LockDuration: interval(time.Minute)})
			if err != nil {
				t.Errorf("CountFailure: %v", err)
			}
			return r
		}
		if r := cf(k1, "account"); r.Failures != 1 || (r.LockedNow != nil && *r.LockedNow) {
			t.Errorf("the first CountFailure: %+v", r)
		}
		if r := cf(k1, "account"); r.Failures != 2 || r.LockedNow == nil || !*r.LockedNow {
			t.Errorf("the second CountFailure must lock: %+v", r)
		}
		cf(k2, "source")
		mark("GetThrottle")
		rows, err := q.GetThrottle(c, [][]byte{k1, k2, hash("absent")})
		if err != nil || len(rows) != 2 {
			t.Errorf("GetThrottle: %+v %v", rows, err)
		}
		for _, r := range rows {
			if reflect.DeepEqual(r.Key, k1) && (r.Kind != "account" || r.Failures != 2 || r.LockedUntil == nil || !r.LockedUntil.After(r.Now)) {
				t.Errorf("the locked key: %+v", r)
			}
		}
		mark("DeleteThrottle")
		if n, err := q.DeleteThrottle(c, k1); err != nil || n != 1 {
			t.Errorf("DeleteThrottle: %d %v", n, err)
		}
		mark("PurgeThrottle")
		if n, err := q.PurgeThrottle(c, interval(0)); err != nil || n != 1 {
			t.Errorf("PurgeThrottle: %d %v", n, err)
		}

		// OpenID Connect transactions.
		state := hash("state")
		mark("CreateOidcLogin")
		if err := try("CreateOidcLogin", q.CreateOidcLogin(c, queries.CreateOidcLoginParams{StateHash: state, ExpiresAt: expires})); err != nil {
			return err
		}
		if err := try("CreateOidcLogin expired", q.CreateOidcLogin(c, queries.CreateOidcLoginParams{StateHash: hash("old"), ExpiresAt: time.Now().Add(time.Microsecond)})); err != nil {
			return err
		}
		mark("CountOidcLogins")
		if n, err := q.CountOidcLogins(c); err != nil || n < 1 {
			t.Errorf("CountOidcLogins: %d %v", n, err)
		}
		mark("TakeOidcLogin")
		if n, err := q.TakeOidcLogin(c, state); err != nil || n != 1 {
			t.Errorf("TakeOidcLogin: %d %v", n, err)
		}
		if n, err := q.TakeOidcLogin(c, state); err != nil || n != 0 {
			t.Errorf("a state is usable once: %d %v", n, err)
		}
		mark("PurgeOidcLogins")
		if _, err := q.PurgeOidcLogins(c); err != nil {
			t.Errorf("PurgeOidcLogins: %v", err)
		}
		return nil
	}))

	// Sessions.
	must("session statements", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("CreateSession")
		var ids []string
		for i, name := range []string{"one", "two", "three"} {
			s, err := q.CreateSession(c, queries.CreateSessionParams{TokenHash: hash("tok-" + name), UserID: platformUser, Method: "local", ExpiresAt: expires})
			if err != nil {
				return err
			}
			if i == 0 && (s.UserID != platformUser || s.Method != "local" || len(s.ID) != 36 || !s.ExpiresAt.Equal(expires)) {
				t.Errorf("CreateSession: %+v", s)
			}
			ids = append(ids, s.ID)
		}
		mark("GetSessionPrincipal")
		p, err := q.GetSessionPrincipal(c, hash("tok-one"))
		if err != nil || p.SID != ids[0] || p.SUserID != platformUser || p.Username != username || !p.MustChange || p.ManagingTenantID != "" ||
			!reflect.DeepEqual(p.PlatformRoles, []string{"platform-admin"}) || p.State != "active" || p.SignIn != "local" {
			t.Errorf("GetSessionPrincipal: %+v %v", p, err)
		}
		if _, err := q.GetSessionPrincipal(c, hash("tok-none")); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetSessionPrincipal of an unknown token: %v", err)
		}
		mark("TouchSession")
		if err := try("TouchSession", q.TouchSession(c, ids[0])); err != nil {
			return err
		}
		mark("ListSessionsOfUser")
		if l, err := q.ListSessionsOfUser(c, platformUser); err != nil || len(l) != 3 || l[0].ID != ids[2] {
			t.Errorf("ListSessionsOfUser: %+v %v (newest first)", l, err)
		}
		mark("DeleteOldestSessions")
		if err := try("DeleteOldestSessions", q.DeleteOldestSessions(c, queries.DeleteOldestSessionsParams{UserID: platformUser, Offset: 2})); err != nil {
			return err
		}
		if l, err := q.ListSessionsOfUser(c, platformUser); err != nil || len(l) != 2 {
			t.Errorf("after DeleteOldestSessions: %+v %v", l, err)
		}
		mark("DeleteSessionOfUser")
		if n, err := q.DeleteSessionOfUser(c, queries.DeleteSessionOfUserParams{ID: ids[2], UserID: federatedUser}); err != nil || n != 0 {
			t.Errorf("DeleteSessionOfUser of another user's session: %d %v", n, err)
		}
		if n, err := q.DeleteSessionOfUser(c, queries.DeleteSessionOfUserParams{ID: ids[2], UserID: platformUser}); err != nil || n != 1 {
			t.Errorf("DeleteSessionOfUser: %d %v", n, err)
		}
		mark("DeleteSessionsOfUser")
		keep := ids[1]
		if n, err := q.DeleteSessionsOfUser(c, queries.DeleteSessionsOfUserParams{UserID: platformUser, ExceptID: &keep}); err != nil || n != 0 {
			t.Errorf("DeleteSessionsOfUser except the only one: %d %v", n, err)
		}
		if n, err := q.DeleteSessionsOfUser(c, queries.DeleteSessionsOfUserParams{UserID: platformUser}); err != nil || n != 1 {
			t.Errorf("DeleteSessionsOfUser: %d %v", n, err)
		}
		s, err := q.CreateSession(c, queries.CreateSessionParams{TokenHash: hash("tok-four"), UserID: platformUser, Method: "oidc", ExpiresAt: expires})
		if err != nil {
			return err
		}
		mark("DeleteSession")
		if n, err := q.DeleteSession(c, s.ID); err != nil || n != 1 {
			t.Errorf("DeleteSession: %d %v", n, err)
		}
		mark("PurgeSessions")
		if _, err := q.CreateSession(c, queries.CreateSessionParams{TokenHash: hash("tok-five"), UserID: platformUser, Method: "local", ExpiresAt: expires}); err != nil {
			return err
		}
		if n, err := q.PurgeSessions(c, interval(24*time.Hour)); err != nil || n != 0 {
			t.Errorf("PurgeSessions of live sessions: %d %v", n, err)
		}
		if n, err := q.PurgeSessions(c, interval(0)); err != nil || n != 1 {
			t.Errorf("PurgeSessions of idle sessions: %d %v", n, err)
		}
		return nil
	}))

	// A tenant's session: the user it manages, its members and their roles.
	must("tenant statements", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		u, err := q.CreateUser(c, queries.CreateUserParams{SignIn: "local", TenantID: &tenantID, DisplayName: "Managed"})
		if err != nil {
			return err
		}
		tenantUser = u.ID
		if u.TenantID == nil || *u.TenantID != tenantID {
			t.Errorf("CreateUser in a tenant: %+v", u)
		}
		if err := q.CreateLocalAccount(c, queries.CreateLocalAccountParams{UserID: u.ID, Username: "managed-" + tenantID[:8], PasswordHash: phc(4), MustChange: true}); err != nil {
			return err
		}
		if got, err := q.GetUser(c, u.ID); err != nil || got.Username != "managed-"+tenantID[:8] || got.TenantID == nil {
			t.Errorf("GetUser of a managed user: %+v %v", got, err)
		}
		mark("CreateMembership")
		m, err := q.CreateMembership(c, queries.CreateMembershipParams{TenantID: tenantID, UserID: u.ID, Source: "manual"})
		if err != nil || m.UserID != u.ID || m.Source != "manual" || len(m.ID) != 36 {
			t.Errorf("CreateMembership: %+v %v", m, err)
		}
		mark("AddMembershipRole")
		if err := try("AddMembershipRole", q.AddMembershipRole(c, queries.AddMembershipRoleParams{TenantID: tenantID, UserID: u.ID, Role: "tenant-admin", Source: "manual"})); err != nil {
			return err
		}
		if err := try("AddMembershipRole idp", q.AddMembershipRole(c, queries.AddMembershipRoleParams{TenantID: tenantID, UserID: u.ID, Role: "inference", Source: "idp"})); err != nil {
			return err
		}
		mark("ListMembershipRoles")
		if roles, err := q.ListMembershipRoles(c, u.ID); err != nil || !reflect.DeepEqual(roles, []queries.ListMembershipRolesRow{{Role: "inference", Source: "idp"}, {Role: "tenant-admin", Source: "manual"}}) {
			t.Errorf("ListMembershipRoles: %+v %v", roles, err)
		}
		mark("GetMember")
		got, err := q.GetMember(c, u.ID)
		if err != nil || got.UserID != u.ID || got.ManagingTenantID == nil || got.Username != "managed-"+tenantID[:8] || !got.MustChange || !reflect.DeepEqual(got.Roles, []string{"inference", "tenant-admin"}) {
			t.Errorf("GetMember: %+v %v", got, err)
		}
		if _, err := q.GetMember(c, "00000000-0000-7000-8000-000000000000"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetMember of a user that is not a member: %v", err)
		}
		mark("GetMemberPrincipal")
		if p, err := q.GetMemberPrincipal(c, u.ID); err != nil || p.TenantName == "" || p.TenantState != "active" || len(p.Roles) != 2 {
			t.Errorf("GetMemberPrincipal: %+v %v", p, err)
		}
		mark("SetMembershipSource")
		if n, err := q.SetMembershipSource(c, queries.SetMembershipSourceParams{UserID: u.ID, Source: "idp"}); err != nil || n != 1 {
			t.Errorf("SetMembershipSource: %d %v", n, err)
		}
		mark("RemoveMembershipRole")
		if n, err := q.RemoveMembershipRole(c, queries.RemoveMembershipRoleParams{UserID: u.ID, Role: "inference"}); err != nil || n != 1 {
			t.Errorf("RemoveMembershipRole: %d %v", n, err)
		}

		// A user that the platform manages, as a member: found by ID or by name, nothing else.
		mark("LookupPlatformUser")
		if id, err := q.LookupPlatformUser(c, queries.LookupPlatformUserParams{UserID: &platformUser}); err != nil || id != platformUser {
			t.Errorf("LookupPlatformUser by ID: %q %v", id, err)
		}
		if id, err := q.LookupPlatformUser(c, queries.LookupPlatformUserParams{Username: &username}); err != nil || id != platformUser {
			t.Errorf("LookupPlatformUser by username: %q %v", id, err)
		}
		if id, err := q.LookupPlatformUser(c, queries.LookupPlatformUserParams{}); err != nil || id != "" {
			t.Errorf("LookupPlatformUser by neither: %q %v", id, err)
		}
		if id, err := q.LookupPlatformUser(c, queries.LookupPlatformUserParams{UserID: &u.ID}); err != nil || id != "" {
			t.Errorf("LookupPlatformUser of a user that the tenant manages must find nothing: %q %v", id, err)
		}
		pm, err := q.CreateMembership(c, queries.CreateMembershipParams{TenantID: tenantID, UserID: platformUser, Source: "manual"})
		if err != nil {
			return err
		}
		memberUser = pm.UserID
		mark("ListMembers")
		page, err := q.ListMembers(c, queries.ListMembersParams{Sort: "created_at", PageSize: 10, AfterID: "00000000-0000-7000-8000-000000000000"})
		if err != nil || len(page) != 2 || page[0].UserID != u.ID || page[1].UserID != platformUser || page[1].ManagingTenantID != nil || page[1].Username != username {
			t.Errorf("ListMembers: %+v %v", page, err)
		}
		if page, err := q.ListMembers(c, queries.ListMembersParams{Sort: "-created_at", PageSize: 1, Username: username, AfterID: "00000000-0000-7000-8000-000000000000"}); err != nil || len(page) != 1 {
			t.Errorf("ListMembers by username: %+v %v", page, err)
		}
		if page, err := q.ListMembers(c, queries.ListMembersParams{Sort: "created_at", PageSize: 10, After: true, AfterID: pm.ID}); err != nil || len(page) != 0 {
			t.Errorf("ListMembers after the last: %+v %v", page, err)
		}
		return nil
	}))

	// Which tenants a user is a member of, in the platform scope.
	must("user tenants", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("UserTenants")
		if ts, err := q.UserTenants(c, memberUser); err != nil || !reflect.DeepEqual(ts, []string{tenantID}) {
			t.Errorf("UserTenants: %v %v", ts, err)
		}
		if ts, err := q.UserTenants(c, tenantUser); err != nil || len(ts) != 1 {
			t.Errorf("UserTenants of a managed user: %v %v", ts, err)
		}
		if ts, err := q.UserTenants(c, federatedUser); err != nil || len(ts) != 0 {
			t.Errorf("UserTenants of a user without membership: %v %v", ts, err)
		}
		if ts, err := q.TenantIDsByName(c, []string{"x"}); err != nil || len(ts) > 1 {
			t.Errorf("TenantIDsByName: %+v %v", ts, err)
		}
		return nil
	}))

	// The membership goes; its roles go with it.
	must("membership removal", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("DeleteMembership")
		if n, err := q.DeleteMembership(c, tenantUser); err != nil || n != 1 {
			t.Errorf("DeleteMembership: %d %v", n, err)
		}
		if roles, err := q.ListMembershipRoles(c, tenantUser); err != nil || len(roles) != 0 {
			t.Errorf("the roles after DeleteMembership: %+v %v", roles, err)
		}
		return nil
	}))

	// Idempotency keys: in a tenant's session, and in the platform scope.
	claim := func(q *queries.Queries, scope *string, key string) (queries.ClaimIdempotencyKeyRow, error) {
		return q.ClaimIdempotencyKey(c, queries.ClaimIdempotencyKeyParams{TenantID: scope, PrincipalKind: "user", PrincipalID: "p1", Key: key, Operation: "createMember",
			Salt: []byte("0123456789abcdef"), RequestHash: hash("req-" + key), Retention: interval(time.Hour)})
	}
	idem := func(what string, scope *string, run func(f func(ctx context.Context, fn func(context.Context, store.Tx) error) error) error) {
		t.Helper()
		var f func(ctx context.Context, fn func(context.Context, store.Tx) error) error
		if scope == nil {
			f = admin.Platform
		} else {
			f = func(ctx context.Context, fn func(context.Context, store.Tx) error) error {
				return admin.WithTenant(ctx, tid, fn)
			}
		}
		must(what, run(f))
	}
	for _, scope := range []*string{&tenantID, nil} {
		scope := scope
		idem("idempotency keys", scope, func(in func(context.Context, func(context.Context, store.Tx) error) error) error {
			// Claim, find, commit, complete.
			var id string
			if err := in(c, func(c context.Context, tx store.Tx) error {
				q := queries.New(tx)
				mark("ClaimIdempotencyKey")
				row, err := claim(q, scope, "key-1")
				if err != nil || row.Attempt != 1 || len(row.ID) != 36 {
					t.Errorf("ClaimIdempotencyKey: %+v %v", row, err)
				}
				id = row.ID
				if _, err := claim(q, scope, "key-1"); !errors.Is(err, store.ErrNoRows) {
					t.Errorf("a second ClaimIdempotencyKey must return no row: %v", err)
				}
				mark("GetIdempotencyKey")
				g, err := q.GetIdempotencyKey(c, queries.GetIdempotencyKeyParams{TenantID: scope, PrincipalKind: "user", PrincipalID: "p1", Key: "key-1"})
				if err != nil || g.ID != id || g.Operation != "createMember" || !g.Locked || g.Committed || g.Completed || g.Expired || g.Attempt != 1 || !reflect.DeepEqual(g.RequestHash, hash("req-key-1")) {
					t.Errorf("GetIdempotencyKey: %+v %v", g, err)
				}
				if _, err := q.GetIdempotencyKey(c, queries.GetIdempotencyKeyParams{TenantID: scope, PrincipalKind: "user", PrincipalID: "p2", Key: "key-1"}); !errors.Is(err, store.ErrNoRows) {
					t.Errorf("GetIdempotencyKey of another principal: %v", err)
				}
				mark("TakeOverIdempotencyKey")
				if _, err := q.TakeOverIdempotencyKey(c, id); !errors.Is(err, store.ErrNoRows) {
					t.Errorf("TakeOverIdempotencyKey of a key that is still locked: %v", err)
				}
				mark("MarkIdempotencyKeyCommitted")
				if n, err := q.MarkIdempotencyKeyCommitted(c, queries.MarkIdempotencyKeyCommittedParams{ID: id, Attempt: 2, ResourceType: "user", ResourceID: "u1"}); err != nil || n != 0 {
					t.Errorf("MarkIdempotencyKeyCommitted by a superseded attempt: %d %v", n, err)
				}
				if n, err := q.MarkIdempotencyKeyCommitted(c, queries.MarkIdempotencyKeyCommittedParams{ID: id, Attempt: 1, ResourceType: "user", ResourceID: "u1"}); err != nil || n != 1 {
					t.Errorf("MarkIdempotencyKeyCommitted: %d %v", n, err)
				}
				mark("CompleteIdempotencyKey")
				status := int32(201)
				if err := try("CompleteIdempotencyKey", q.CompleteIdempotencyKey(c, queries.CompleteIdempotencyKeyParams{ID: id, ResponseStatus: &status, ResponseBody: `{"user":{"id":"u1"}}`})); err != nil {
					return err
				}
				g, err = q.GetIdempotencyKey(c, queries.GetIdempotencyKeyParams{TenantID: scope, PrincipalKind: "user", PrincipalID: "p1", Key: "key-1"})
				if err != nil || !g.Committed || !g.Completed || g.ResponseStatus == nil || *g.ResponseStatus != 201 || !strings.Contains(g.ResponseBody, `"u1"`) {
					t.Errorf("GetIdempotencyKey after completion: %+v %v", g, err)
				}
				// A key that was claimed and not used is released.
				r2, err := claim(q, scope, "key-2")
				if err != nil {
					return err
				}
				mark("ReleaseIdempotencyKey")
				if err := try("ReleaseIdempotencyKey", q.ReleaseIdempotencyKey(c, queries.ReleaseIdempotencyKeyParams{ID: r2.ID, Attempt: 1})); err != nil {
					return err
				}
				if _, err := q.GetIdempotencyKey(c, queries.GetIdempotencyKeyParams{TenantID: scope, PrincipalKind: "user", PrincipalID: "p1", Key: "key-2"}); !errors.Is(err, store.ErrNoRows) {
					t.Errorf("a released key: %v", err)
				}
				mark("PurgeIdempotencyKeys")
				if n, err := q.PurgeIdempotencyKeys(c); err != nil || n != 0 {
					t.Errorf("PurgeIdempotencyKeys of live keys: %d %v", n, err)
				}
				return nil
			}); err != nil {
				return err
			}
			return nil
		})
	}
}
