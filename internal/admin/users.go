package admin

// Handlers of the operations on users that the platform manages (sprint 4.2, sections 5.2 and 5.12):
// one transaction of the platform scope per operation, with the audit rows inside it.

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// lockedUntil reads, in the platform scope, the end of the lock of each username that is locked now.
func (s *server) lockedUntil(ctx context.Context, usernames []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(usernames) == 0 {
		return out, nil
	}
	byKey := map[string]string{}
	keys := make([][]byte, 0, len(usernames))
	for _, u := range usernames {
		k := account.UsernameKey(u)
		byKey[string(k)] = u
		keys = append(keys, k)
	}
	var rows []queries.GetThrottleRow
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).GetThrottle(ctx, keys)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if u, ok := byKey[string(r.Key)]; ok && r.Kind == "account" && r.LockedUntil != nil && r.LockedUntil.After(r.Now) {
			out[u] = *r.LockedUntil
		}
	}
	return out, nil
}

func lockOf(m map[string]time.Time, username string) *time.Time {
	if t, ok := m[username]; ok {
		return &t
	}
	return nil
}

// mapPeopleConstraint turns the violations that the requests of this group can cause into problems.
func mapPeopleConstraint(err error) error {
	kind, name, ok := store.ConstraintViolation(err)
	if !ok {
		return err
	}
	switch {
	case kind == "unique" && (name == "local_account_username_key" || name == "user_identity_subject_key" || name == "membership_user_key"):
		return errConflict
	case kind == "check" && name == "app_user_display_name_format":
		return invalid("display_name", "must have at most 200 characters and no control character")
	case kind == "check" && name == "app_user_email_format":
		return invalid("email", "must have at most 254 characters, no space and no control character")
	}
	return err
}

// checkRolesIn refuses a role that does not exist in the scope, that a user cannot hold, or that the
// caller does not hold itself (the rule against escalation).
func checkRolesIn(p authz.Principal, field string, roles []string, scope authz.Scope) error {
	for _, r := range roles {
		if !authz.Assignable(p, r, scope) {
			return invalid(field, "must name roles that users can hold and that the caller holds")
		}
	}
	return nil
}

var reSubject = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,255}$`)

// userEvent is an audit event about a user.
func userEvent(p authz.Principal, requestID, action, userID, reason string, detail map[string]any) audit.Event {
	return audit.Event{Actor: audit.ActorOf(p), Action: action, ResourceType: "user", ResourceID: userID, Outcome: "success", RequestID: requestID, Reason: reason, Detail: detail}
}

// platformUser runs fn in the platform scope on the user with the ID, locked, when the platform manages it;
// a user that a tenant manages is not found here.
func (s *server) platformUser(ctx context.Context, id string, fn func(ctx context.Context, tx store.Tx, q *queries.Queries, cur queries.GetUserRow) error) error {
	return s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		lock, err := q.LockUser(ctx, id)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if lock.TenantID != nil {
			return errNotFound
		}
		cur, err := q.GetUser(ctx, id)
		if err != nil {
			return err
		}
		return fn(ctx, tx, q, cur)
	})
}

// userResult reads a user again after a change, with the lock of its account.
func (s *server) userResult(ctx context.Context, id string) (*adminapi.User, error) {
	var row queries.GetUserRow
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetUser(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	locks, err := s.lockedUntil(ctx, usernamesOf(row.Username))
	if err != nil {
		return nil, err
	}
	u := userOut(row, lockOf(locks, row.Username))
	return &u, nil
}

func usernamesOf(names ...string) []string {
	var out []string
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// ListUsers lists the users that the platform manages.
func (s *server) ListUsers(ctx context.Context, in adminapi.ListUsersInput) (*adminapi.UserList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, name, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListUsersRow
	err = s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListUsers(ctx, queries.ListUsersParams{
			State: string(in.State), Username: in.Username, After: after, Sort: sortBy, AfterID: afterID(after, id), AfterUsername: name, PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.UserList{Items: []adminapi.User{}}
	if len(rows) > limit {
		last := rows[limit-1]
		out.NextCursor = encodeCursor(sortBy, last.Username, last.ID)
		rows = rows[:limit]
	}
	var names []string
	for _, r := range rows {
		names = usernamesOf(append(names, r.Username)...)
	}
	locks, err := s.lockedUntil(ctx, names)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.Items = append(out.Items, userOut(queries.GetUserRow(r), lockOf(locks, r.Username)))
	}
	return out, nil
}

// GetUser reads a user that the platform manages.
func (s *server) GetUser(ctx context.Context, in adminapi.GetUserInput) (*adminapi.User, error) {
	var tenantID *string
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		row, err := queries.New(tx).GetUser(ctx, in.User)
		tenantID = row.TenantID
		return err
	})
	if isNoRowsErr(err) || (err == nil && tenantID != nil) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.userResult(ctx, in.User)
}

// CreateUser creates a user that the platform manages, with its roles.
func (s *server) CreateUser(ctx context.Context, in adminapi.CreateUserInput) (*adminapi.UserCreated, error) {
	b := in.Body
	p := principalOf(ctx)
	st := stateOf(ctx)
	if err := checkRolesIn(p, "platform_roles", b.PlatformRoles, authz.ScopePlatform); err != nil {
		return nil, err
	}
	var gen account.Generated
	issuer := ""
	switch b.SignIn {
	case adminapi.SignInMethodLocal:
		if b.Username == "" {
			return nil, invalid("username", "is required for a local user")
		}
		if b.Subject != "" {
			return nil, invalid("subject", "is for a user of the identity provider")
		}
		if !s.authOpts.LocalEnabled {
			return nil, invalidState("sign_in", "local accounts are switched off")
		}
		var err error
		if gen, err = s.accounts.Generate(ctx); err != nil {
			return nil, err
		}
	default:
		if b.Subject == "" {
			return nil, invalid("subject", "is required for a user of the identity provider")
		}
		if b.Username != "" {
			return nil, invalid("username", "is for a local user")
		}
		if !reSubject.MatchString(b.Subject) {
			return nil, invalid("subject", "must have 1 to 255 characters and no control character")
		}
		if s.oidc == nil {
			return nil, invalidState("sign_in", "the identity provider is not configured")
		}
		issuer = s.oidc.Issuer()
	}
	var id string
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		u, err := q.CreateUser(ctx, queries.CreateUserParams{SignIn: string(b.SignIn), DisplayName: b.DisplayName, Email: b.Email})
		if err != nil {
			return err
		}
		id = u.ID
		detail := map[string]any{"sign_in": string(b.SignIn)}
		if b.SignIn == adminapi.SignInMethodLocal {
			detail["username"] = b.Username
			err = s.accounts.CreateAccount(ctx, tx, id, b.Username, gen)
		} else {
			detail["subject"] = b.Subject
			err = q.CreateUserIdentity(ctx, queries.CreateUserIdentityParams{UserID: id, Issuer: issuer, Subject: b.Subject})
		}
		if err != nil {
			return err
		}
		if err := audit.RecordPlatform(ctx, tx, userEvent(p, st.id, "user.create", id, "", detail)); err != nil {
			return err
		}
		for _, r := range sortedCopy(b.PlatformRoles) {
			if err := q.AddPlatformRole(ctx, queries.AddPlatformRoleParams{UserID: id, Role: r, Source: "manual"}); err != nil {
				return err
			}
			if err := audit.RecordPlatform(ctx, tx, userEvent(p, st.id, "platform_role.assign", id, "", map[string]any{"role": r, "source": "manual"})); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, mapPeopleConstraint(err)
	}
	out, err := s.userResult(ctx, id)
	if err != nil {
		return nil, err
	}
	return &adminapi.UserCreated{User: *out, InitialPassword: gen.Password.Reveal()}, nil
}

// UpdateUser changes the members that are present; the roles are replaced as a set.
func (s *server) UpdateUser(ctx context.Context, in adminapi.UpdateUserInput) (*adminapi.User, error) {
	b := in.Body
	if !b.DisplayName.Set && !b.Email.Set && !b.PlatformRoles.Set {
		return nil, invalid("body", "must change at least one member")
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	if err := checkRolesIn(p, "platform_roles", b.PlatformRoles.Value, authz.ScopePlatform); err != nil {
		return nil, err
	}
	err := s.platformUser(ctx, in.User, func(ctx context.Context, tx store.Tx, q *queries.Queries, cur queries.GetUserRow) error {
		par := queries.UpdateUserParams{ID: cur.ID}
		detail := map[string]any{}
		if b.DisplayName.Set && b.DisplayName.Value != cur.DisplayName {
			par.SetDisplayName, par.DisplayName = true, b.DisplayName.Value
			detail["display_name"] = change(cur.DisplayName, b.DisplayName.Value)
		}
		if b.Email.Set && b.Email.Value != cur.Email {
			par.SetEmail, par.Email = true, b.Email.Value
			detail["email"] = change(cur.Email, b.Email.Value)
		}
		var added, removed []string
		if b.PlatformRoles.Set {
			want := sortedCopy(b.PlatformRoles.Value)
			have := sortedCopy(cur.PlatformRoles)
			added, removed = difference(want, have), difference(have, want)
			if p.Kind == authz.KindUser && p.ID == cur.ID && len(want) == 0 && len(have) > 0 {
				return invalidState("platform_roles", "a user cannot remove its own last platform role")
			}
		}
		if par.SetDisplayName || par.SetEmail {
			if _, err := q.UpdateUser(ctx, par); err != nil {
				return err
			}
			if err := audit.RecordPlatform(ctx, tx, userEvent(p, st.id, "user.update", cur.ID, "", detail)); err != nil {
				return err
			}
		}
		for _, r := range added {
			if err := q.AddPlatformRole(ctx, queries.AddPlatformRoleParams{UserID: cur.ID, Role: r, Source: "manual"}); err != nil {
				return err
			}
			if err := audit.RecordPlatform(ctx, tx, userEvent(p, st.id, "platform_role.assign", cur.ID, "", map[string]any{"role": r, "source": "manual"})); err != nil {
				return err
			}
		}
		for _, r := range removed {
			if _, err := q.RemovePlatformRole(ctx, queries.RemovePlatformRoleParams{UserID: cur.ID, Role: r}); err != nil {
				return err
			}
			if err := audit.RecordPlatform(ctx, tx, userEvent(p, st.id, "platform_role.remove", cur.ID, "", map[string]any{"role": r})); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, mapPeopleConstraint(err)
	}
	return s.userResult(ctx, in.User)
}

// difference returns the elements of a that b lacks; both are sorted.
func difference(a, b []string) []string {
	have := map[string]bool{}
	for _, x := range b {
		have[x] = true
	}
	var out []string
	for _, x := range a {
		if !have[x] {
			out = append(out, x)
		}
	}
	return out
}

// setUserState disables or enables a user; disabling also ends its sessions. Repeating either changes nothing.
func (s *server) setUserState(ctx context.Context, id, reason, to, action string) (*adminapi.User, error) {
	p := principalOf(ctx)
	st := stateOf(ctx)
	err := s.platformUser(ctx, id, func(ctx context.Context, tx store.Tx, q *queries.Queries, cur queries.GetUserRow) error {
		if to == "disabled" && p.Kind == authz.KindUser && p.ID == cur.ID {
			return invalidState("user", "a user cannot disable itself")
		}
		if cur.State == to {
			return nil
		}
		if _, err := q.SetUserState(ctx, queries.SetUserStateParams{ID: cur.ID, State: to}); err != nil {
			return err
		}
		if to == "disabled" {
			if _, err := q.DeleteSessionsOfUser(ctx, queries.DeleteSessionsOfUserParams{UserID: cur.ID}); err != nil {
				return err
			}
		}
		return audit.RecordPlatform(ctx, tx, userEvent(p, st.id, action, cur.ID, reason, nil))
	})
	if err != nil {
		return nil, err
	}
	return s.userResult(ctx, id)
}

// DisableUser disables a user and ends its sessions.
func (s *server) DisableUser(ctx context.Context, in adminapi.DisableUserInput) (*adminapi.User, error) {
	return s.setUserState(ctx, in.User, in.Body.Reason, "disabled", "user.disable")
}

// EnableUser enables a user; its sessions do not come back.
func (s *server) EnableUser(ctx context.Context, in adminapi.EnableUserInput) (*adminapi.User, error) {
	return s.setUserState(ctx, in.User, in.Body.Reason, "active", "user.enable")
}

// ResetUserPassword gives a local user a generated password that must be changed.
func (s *server) ResetUserPassword(ctx context.Context, in adminapi.ResetUserPasswordInput) (*adminapi.PasswordReset, error) {
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.PasswordReset
	err := s.platformUser(ctx, in.User, func(ctx context.Context, tx store.Tx, _ *queries.Queries, cur queries.GetUserRow) error {
		pw, expires, err := s.accounts.ResetPassword(ctx, tx, cur.ID)
		if errors.Is(err, account.ErrNotLocal) {
			return invalidState("user", "has no password")
		}
		if err != nil {
			return err
		}
		out = adminapi.PasswordReset{UserID: cur.ID, Password: pw.Reveal(), ExpiresAt: expires}
		return audit.RecordPlatform(ctx, tx, userEvent(p, st.id, "user.password_reset", cur.ID, in.Body.Reason, nil))
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeUserSessions ends every session of a user.
func (s *server) RevokeUserSessions(ctx context.Context, in adminapi.RevokeUserSessionsInput) (*adminapi.SessionsRevoked, error) {
	p := principalOf(ctx)
	st := stateOf(ctx)
	var n int64
	err := s.platformUser(ctx, in.User, func(ctx context.Context, tx store.Tx, q *queries.Queries, cur queries.GetUserRow) error {
		var err error
		if n, err = q.DeleteSessionsOfUser(ctx, queries.DeleteSessionsOfUserParams{UserID: cur.ID}); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, userEvent(p, st.id, "user.sessions_revoke", cur.ID, in.Body.Reason, map[string]any{"revoked": n}))
	})
	if err != nil {
		return nil, err
	}
	return &adminapi.SessionsRevoked{Revoked: int(n)}, nil
}

// UnlockUser removes the lock and the failure count of a local user's account.
func (s *server) UnlockUser(ctx context.Context, in adminapi.UnlockUserInput) (*adminapi.User, error) {
	p := principalOf(ctx)
	st := stateOf(ctx)
	err := s.platformUser(ctx, in.User, func(ctx context.Context, tx store.Tx, _ *queries.Queries, cur queries.GetUserRow) error {
		if cur.SignIn != "local" {
			return invalidState("user", "has no password")
		}
		if err := s.accounts.Unlock(ctx, tx, cur.Username); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, userEvent(p, st.id, "user.unlock", cur.ID, in.Body.Reason, nil))
	})
	if err != nil {
		return nil, err
	}
	return s.userResult(ctx, in.User)
}
