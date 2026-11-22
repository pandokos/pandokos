package admin

// Handlers of the operations on members of a tenant (sprint 4.2, sections 5.2 and 5.12): one transaction
// in the tenant's session per operation, with the audit row inside it. What the platform scope alone
// can see (the throttle row of an account) is read or removed in a transaction of its own.

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// memberResult reads a member again after a change, with the lock of its account when the tenant
// manages it.
func (s *server) memberResult(ctx context.Context, tid tenant.ID, userID string) (*adminapi.Member, error) {
	var row queries.GetMemberRow
	err := s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetMember(ctx, userID)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	var locks map[string]time.Time
	if row.ManagingTenantID != nil {
		if locks, err = s.lockedUntil(ctx, usernamesOf(row.Username)); err != nil {
			return nil, err
		}
	}
	m := memberOut(row, lockOf(locks, row.Username))
	return &m, nil
}

// memberEvent is an audit event of a tenant's log about a user or a membership.
func memberEvent(p authz.Principal, requestID, action, userID, reason string, detail map[string]any) audit.Event {
	return userEvent(p, requestID, action, userID, reason, detail)
}

// member runs fn in the tenant's session on the membership of the user; a user that is not a member is
// not found. With managed, a member that the platform manages is refused (409): the operations on the
// account are the managing side's, and the managed user is locked for the change.
func (s *server) member(ctx context.Context, tid tenant.ID, userID string, managed bool, fn func(ctx context.Context, tx store.Tx, q *queries.Queries, cur queries.GetMemberRow) error) error {
	return s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cur, err := q.GetMember(ctx, userID)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if managed {
			if cur.ManagingTenantID == nil {
				return invalidState("user", "is managed by the platform")
			}
			if _, err := q.LockUser(ctx, userID); err != nil {
				return err
			}
		}
		return fn(ctx, tx, q, cur)
	})
}

// ListMembers lists the members of a tenant.
func (s *server) ListMembers(ctx context.Context, in adminapi.ListMembersInput) (*adminapi.MemberList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, _, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListMembersRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListMembers(ctx, queries.ListMembersParams{Username: in.Username, After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.MemberList{Items: []adminapi.Member{}}
	if len(rows) > limit {
		out.NextCursor = encodeCursor(sortBy, "", rows[limit-1].ID)
		rows = rows[:limit]
	}
	var names []string
	for _, r := range rows {
		if r.ManagingTenantID != nil {
			names = usernamesOf(append(names, r.Username)...)
		}
	}
	locks, err := s.lockedUntil(ctx, names)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.Items = append(out.Items, memberOut(queries.GetMemberRow(r), lockOf(locks, r.Username)))
	}
	return out, nil
}

// GetMember reads a member of a tenant.
func (s *server) GetMember(ctx context.Context, in adminapi.GetMemberInput) (*adminapi.Member, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.memberResult(ctx, tid, in.User)
}

// CreateMember makes a user a member of the tenant: a new account that the tenant manages, or a user
// that the platform manages.
func (s *server) CreateMember(ctx context.Context, in adminapi.CreateMemberInput) (*adminapi.MemberCreated, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	p := principalOf(ctx)
	st := stateOf(ctx)
	if err := checkRolesIn(p, "roles", b.Roles, authz.ScopeTenant); err != nil {
		return nil, err
	}
	var gen account.Generated
	switch b.Account {
	case adminapi.MemberAccountKindNew:
		if b.Username == "" {
			return nil, invalid("username", "is required for a new account")
		}
		if b.UserID != "" {
			return nil, invalid("user_id", "is for an existing account")
		}
		if !s.authOpts.LocalEnabled {
			return nil, invalidState("account", "local accounts are switched off")
		}
		if gen, err = s.accounts.Generate(ctx); err != nil {
			return nil, err
		}
	default:
		if (b.UserID == "") == (b.Username == "") {
			return nil, invalid("user_id", "exactly one of user_id and username names an existing account")
		}
		if b.DisplayName != "" {
			return nil, invalid("display_name", "is for a new account")
		}
		if b.Email != "" {
			return nil, invalid("email", "is for a new account")
		}
	}
	var userID string
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		roleDetail := map[string]any{"roles": sortedCopy(b.Roles), "source": "manual"}
		if b.Account == adminapi.MemberAccountKindNew {
			managing := tid.String()
			u, err := q.CreateUser(ctx, queries.CreateUserParams{SignIn: "local", TenantID: &managing, DisplayName: b.DisplayName, Email: b.Email})
			if err != nil {
				return err
			}
			userID = u.ID
			if err := s.accounts.CreateAccount(ctx, tx, userID, b.Username, gen); err != nil {
				return err
			}
			if err := audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, "user.create", userID, "", map[string]any{"sign_in": "local", "username": b.Username})); err != nil {
				return err
			}
		} else {
			arg := queries.LookupPlatformUserParams{UserID: nilIfEmpty(b.UserID), Username: nilIfEmpty(b.Username)}
			var err error
			if userID, err = q.LookupPlatformUser(ctx, arg); err != nil {
				return err
			}
			if userID == "" {
				return errNotFound
			}
		}
		if _, err := q.CreateMembership(ctx, queries.CreateMembershipParams{TenantID: tid.String(), UserID: userID, Source: "manual"}); err != nil {
			return err
		}
		for _, r := range sortedCopy(b.Roles) {
			if err := q.AddMembershipRole(ctx, queries.AddMembershipRoleParams{TenantID: tid.String(), UserID: userID, Role: r, Source: "manual"}); err != nil {
				return err
			}
		}
		return audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, "member.add", userID, "", roleDetail))
	})
	if err != nil {
		return nil, mapPeopleConstraint(err)
	}
	m, err := s.memberResult(ctx, tid, userID)
	if err != nil {
		return nil, err
	}
	return &adminapi.MemberCreated{Member: *m, InitialPassword: gen.Password.Reveal()}, nil
}

// UpdateMember changes the roles of a member, and the attributes of a user that the tenant manages.
func (s *server) UpdateMember(ctx context.Context, in adminapi.UpdateMemberInput) (*adminapi.Member, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if !b.Roles.Set && !b.DisplayName.Set && !b.Email.Set {
		return nil, invalid("body", "must change at least one member")
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	if err := checkRolesIn(p, "roles", b.Roles.Value, authz.ScopeTenant); err != nil {
		return nil, err
	}
	err = s.member(ctx, tid, in.User, false, func(ctx context.Context, tx store.Tx, q *queries.Queries, cur queries.GetMemberRow) error {
		attributes := b.DisplayName.Set || b.Email.Set
		if attributes && cur.ManagingTenantID == nil {
			return invalidState("user", "is managed by the platform: its attributes are not the tenant's to change")
		}
		if attributes {
			if _, err := q.LockUser(ctx, cur.UserID); err != nil {
				return err
			}
			par := queries.UpdateUserParams{ID: cur.UserID}
			detail := map[string]any{}
			if b.DisplayName.Set && b.DisplayName.Value != cur.DisplayName {
				par.SetDisplayName, par.DisplayName = true, b.DisplayName.Value
				detail["display_name"] = change(cur.DisplayName, b.DisplayName.Value)
			}
			if b.Email.Set && b.Email.Value != cur.Email {
				par.SetEmail, par.Email = true, b.Email.Value
				detail["email"] = change(cur.Email, b.Email.Value)
			}
			if len(detail) > 0 {
				if _, err := q.UpdateUser(ctx, par); err != nil {
					return err
				}
				if err := audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, "user.update", cur.UserID, "", detail)); err != nil {
					return err
				}
			}
		}
		if !b.Roles.Set {
			return nil
		}
		want, have := sortedCopy(b.Roles.Value), sortedCopy(cur.Roles)
		if sameStrings(want, have) {
			return nil
		}
		for _, r := range difference(want, have) {
			if err := q.AddMembershipRole(ctx, queries.AddMembershipRoleParams{TenantID: tid.String(), UserID: cur.UserID, Role: r, Source: "manual"}); err != nil {
				return err
			}
		}
		for _, r := range difference(have, want) {
			if _, err := q.RemoveMembershipRole(ctx, queries.RemoveMembershipRoleParams{UserID: cur.UserID, Role: r}); err != nil {
				return err
			}
		}
		return audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, "member.update", cur.UserID, "", map[string]any{"roles": change(have, want), "source": "manual"}))
	})
	if err != nil {
		return nil, mapPeopleConstraint(err)
	}
	return s.memberResult(ctx, tid, in.User)
}

// DeleteMember removes the membership and its roles; a user that the tenant manages has no other
// place to act, so its account is disabled and its sessions end.
func (s *server) DeleteMember(ctx context.Context, in adminapi.DeleteMemberInput) (*adminapi.Member, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.Member
	err = s.member(ctx, tid, in.User, false, func(ctx context.Context, tx store.Tx, q *queries.Queries, cur queries.GetMemberRow) error {
		if p.Kind == authz.KindUser && p.ID == cur.UserID {
			return invalidState("user", "a user cannot remove itself")
		}
		managed := cur.ManagingTenantID != nil
		if managed {
			if _, err := q.LockUser(ctx, cur.UserID); err != nil {
				return err
			}
		}
		out = memberOut(cur, nil)
		if _, err := q.DeleteMembership(ctx, cur.UserID); err != nil {
			return err
		}
		if err := audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, "member.remove", cur.UserID, "", map[string]any{"roles": sortedCopy(cur.Roles)})); err != nil {
			return err
		}
		if !managed {
			return nil
		}
		if cur.State == "active" {
			if _, err := q.SetUserState(ctx, queries.SetUserStateParams{ID: cur.UserID, State: "disabled"}); err != nil {
				return err
			}
			if err := audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, "user.disable", cur.UserID, "membership removed", nil)); err != nil {
				return err
			}
		}
		_, err := q.DeleteSessionsOfUser(ctx, queries.DeleteSessionsOfUserParams{UserID: cur.UserID})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// setMemberState disables or enables a user that the tenant manages; disabling ends its sessions.
func (s *server) setMemberState(ctx context.Context, tenantText, userID, reason, to, action string) (*adminapi.Member, error) {
	tid, err := tenantID(tenantText)
	if err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	err = s.member(ctx, tid, userID, true, func(ctx context.Context, tx store.Tx, q *queries.Queries, cur queries.GetMemberRow) error {
		if to == "disabled" && p.Kind == authz.KindUser && p.ID == cur.UserID {
			return invalidState("user", "a user cannot disable itself")
		}
		if cur.State == to {
			return nil
		}
		if _, err := q.SetUserState(ctx, queries.SetUserStateParams{ID: cur.UserID, State: to}); err != nil {
			return err
		}
		if to == "disabled" {
			if _, err := q.DeleteSessionsOfUser(ctx, queries.DeleteSessionsOfUserParams{UserID: cur.UserID}); err != nil {
				return err
			}
		}
		return audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, action, cur.UserID, reason, nil))
	})
	if err != nil {
		return nil, err
	}
	return s.memberResult(ctx, tid, userID)
}

// DisableMember disables a member that the tenant manages and ends its sessions.
func (s *server) DisableMember(ctx context.Context, in adminapi.DisableMemberInput) (*adminapi.Member, error) {
	return s.setMemberState(ctx, in.Tenant, in.User, in.Body.Reason, "disabled", "user.disable")
}

// EnableMember enables a member that the tenant manages; its sessions do not come back.
func (s *server) EnableMember(ctx context.Context, in adminapi.EnableMemberInput) (*adminapi.Member, error) {
	return s.setMemberState(ctx, in.Tenant, in.User, in.Body.Reason, "active", "user.enable")
}

// unlock removes the throttle row of a username in a transaction of the platform scope, which the
// tenant's session cannot see.
func (s *server) unlock(ctx context.Context, username string) error {
	return s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error { return s.accounts.Unlock(ctx, tx, username) })
}

// ResetMemberPassword gives a member that the tenant manages a generated password that must be changed.
func (s *server) ResetMemberPassword(ctx context.Context, in adminapi.ResetMemberPasswordInput) (*adminapi.PasswordReset, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.PasswordReset
	var username string
	g, err := s.accounts.Generate(ctx) // hashed before the transaction, which holds the lock of the user
	if err != nil {
		return nil, err
	}
	err = s.member(ctx, tid, in.User, true, func(ctx context.Context, tx store.Tx, _ *queries.Queries, cur queries.GetMemberRow) error {
		username = cur.Username
		pw, expires, err := s.accounts.ResetPassword(ctx, tx, cur.UserID, g)
		if errors.Is(err, account.ErrNotLocal) {
			return invalidState("user", "has no password")
		}
		if err != nil {
			return err
		}
		out = adminapi.PasswordReset{UserID: cur.UserID, Password: pw.Reveal(), ExpiresAt: expires}
		return audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, "user.password_reset", cur.UserID, in.Body.Reason, nil))
	})
	if err != nil {
		return nil, err
	}
	// The failure count is in a table that the tenant's session does not see. The password is changed
	// and shown; a failure here leaves a lock that ends by itself, so it is logged and not reported.
	if err := s.unlock(ctx, username); err != nil {
		s.log.Warn("throttle row not removed after a password reset", "request_id", st.id)
	}
	return &out, nil
}

// RevokeMemberSessions ends every session of a member that the tenant manages.
func (s *server) RevokeMemberSessions(ctx context.Context, in adminapi.RevokeMemberSessionsInput) (*adminapi.SessionsRevoked, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var n int64
	err = s.member(ctx, tid, in.User, true, func(ctx context.Context, tx store.Tx, q *queries.Queries, cur queries.GetMemberRow) error {
		var err error
		if n, err = q.DeleteSessionsOfUser(ctx, queries.DeleteSessionsOfUserParams{UserID: cur.UserID}); err != nil {
			return err
		}
		return audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, "user.sessions_revoke", cur.UserID, in.Body.Reason, map[string]any{"revoked": n}))
	})
	if err != nil {
		return nil, err
	}
	return &adminapi.SessionsRevoked{Revoked: int(n)}, nil
}

// UnlockMember removes the lock and the failure count of the account of a member that the tenant manages.
func (s *server) UnlockMember(ctx context.Context, in adminapi.UnlockMemberInput) (*adminapi.Member, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var username string
	err = s.member(ctx, tid, in.User, true, func(ctx context.Context, tx store.Tx, _ *queries.Queries, cur queries.GetMemberRow) error {
		if cur.SignIn != "local" {
			return invalidState("user", "has no password")
		}
		username = cur.Username
		return audit.RecordTenant(ctx, tx, tid, memberEvent(p, st.id, "user.unlock", cur.UserID, in.Body.Reason, nil))
	})
	if err != nil {
		return nil, err
	}
	if err := s.unlock(ctx, username); err != nil {
		return nil, err
	}
	return s.memberResult(ctx, tid, in.User)
}
