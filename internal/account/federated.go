package account

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Federated sign-in (sprint 4.2, sections 5.8 and 5.9): a user that the identity provider vouches
// for is found by its link, or made when the group rules say so; its display attributes and the
// roles that the rules give are reconciled; and a session is created.

const (
	maxDisplayName = 200 // the column limits of app_user
	maxEmail       = 254
	sourceIDP      = "idp"
)

// FederatedInput is what a sign-in through the identity provider hands over. TenantRoles is keyed by
// the ID of the tenant that a rule names, and holds only tenants that exist.
type FederatedInput struct {
	Issuer, Subject, DisplayName, Email string
	AutoProvision                       bool
	Reconcile                           bool // group rules are configured: the roles of source idp follow them
	PlatformRoles                       []string
	TenantRoles                         map[string][]string
	Presented                           string // the session cookie of the request, ended by the sign-in
	// Assignable, when set, is called in the transaction of each tenant whose roles are reconciled,
	// with the roles that the rules give there, and returns those that may be assigned: the caller
	// reads the custom roles in that transaction, so a role deleted meanwhile is not assigned.
	Assignable func(ctx context.Context, q *queries.Queries, tid tenant.ID, roles []string) ([]string, error)
}

// displayName and emailAddress limit a display attribute to its column: control characters are
// dropped from a name, and an e-mail address with a space or a control character is not kept.
// address with a space is not kept at all.
func displayName(v string) string {
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
	return clip(v, maxDisplayName)
}

func emailAddress(v string) string {
	if strings.ContainsFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return ""
	}
	return clip(v, maxEmail)
}

func clip(v string, n int) string {
	if utf8.RuneCountInString(v) <= n {
		return v
	}
	return string([]rune(v)[:n])
}

func idpActor(u User) audit.Actor {
	return audit.Actor{Type: audit.ActorUser, ID: u.ID, Name: u.DisplayName}
}

// SignInFederated finds or makes the user of the subject, reconciles its roles with the rules and
// creates a session (method oidc). A subject that maps to no user, and a disabled user, are a
// *Refusal (unmapped_subject, user_disabled).
func (s *Service) SignInFederated(ctx context.Context, in FederatedInput) (SignInResult, error) {
	name, mail := displayName(in.DisplayName), emailAddress(in.Email)
	var u User
	var member []string
	err := s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		row, err := q.GetUserByIdentity(ctx, queries.GetUserByIdentityParams{Issuer: in.Issuer, Subject: in.Subject})
		switch {
		case errors.Is(err, store.ErrNoRows):
			granted := len(in.PlatformRoles) > 0
			for _, r := range in.TenantRoles {
				granted = granted || len(r) > 0
			}
			if !in.AutoProvision || !granted {
				return &Refusal{Reason: "unmapped_subject"}
			}
			created, err := q.CreateUser(ctx, queries.CreateUserParams{SignIn: "oidc", DisplayName: name, Email: mail})
			if err != nil {
				return err
			}
			if err := q.CreateUserIdentity(ctx, queries.CreateUserIdentityParams{UserID: created.ID, Issuer: in.Issuer, Subject: in.Subject}); err != nil {
				return err
			}
			u = User{ID: created.ID, SignIn: "oidc", DisplayName: name, Email: mail, State: created.State}
			if err := audit.RecordPlatform(ctx, tx, audit.Event{Actor: idpActor(u), Action: "user.create", ResourceType: "user", ResourceID: u.ID,
				Outcome: "success", Detail: map[string]any{"sign_in": "oidc", "source": sourceIDP}}); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			u = User{ID: row.ID, SignIn: row.SignIn, DisplayName: row.DisplayName, Email: row.Email, State: row.State}
			if row.TenantID != nil {
				if u.TenantID, err = tenant.ParseID(*row.TenantID); err != nil {
					return err
				}
			}
			if row.State != "active" {
				return &Refusal{Reason: "user_disabled", UserID: u.ID, TenantID: u.TenantID}
			}
			par := queries.UpdateUserParams{ID: u.ID}
			var fields []string
			if name != u.DisplayName {
				par.SetDisplayName, par.DisplayName = true, name
				fields = append(fields, "display_name")
			}
			if mail != u.Email {
				par.SetEmail, par.Email = true, mail
				fields = append(fields, "email")
			}
			if len(fields) > 0 {
				if _, err := q.UpdateUser(ctx, par); err != nil {
					return err
				}
				u.DisplayName, u.Email = name, mail
				if err := audit.RecordPlatform(ctx, tx, audit.Event{Actor: idpActor(u), Action: "user.update", ResourceType: "user", ResourceID: u.ID,
					Outcome: "success", Detail: map[string]any{"fields": fields, "source": sourceIDP}}); err != nil {
					return err
				}
			}
		}
		if !in.Reconcile {
			return nil
		}
		if err := s.reconcilePlatform(ctx, tx, u, in.PlatformRoles); err != nil {
			return err
		}
		member, err = q.UserTenants(ctx, u.ID)
		return err
	})
	if err != nil {
		return SignInResult{}, err
	}
	if in.Reconcile {
		if err := s.reconcileTenants(ctx, u, member, in.TenantRoles, in.Assignable); err != nil {
			return SignInResult{}, err
		}
	}

	var res SignInResult
	err = s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		// A disable that committed after the first read ended the sessions of the user: no new one.
		lock, err := queries.New(tx).LockUser(ctx, u.ID)
		if err != nil {
			return err
		}
		if lock.State != "active" {
			return &Refusal{Reason: "user_disabled", UserID: u.ID, TenantID: u.TenantID}
		}
		if in.Presented != "" {
			if err := s.o.Sessions.DeletePresented(ctx, tx, in.Presented, u.ID); err != nil {
				return err
			}
		}
		tok, sess, err := s.o.Sessions.Create(ctx, tx, u.ID, "oidc")
		if err != nil {
			return err
		}
		res = SignInResult{User: u, Token: tok, Session: sess}
		return audit.RecordPlatform(ctx, tx, audit.Event{Actor: idpActor(u), Action: "auth.sign_in", ResourceType: "user", ResourceID: u.ID,
			Outcome: "success", Detail: map[string]any{"method": "oidc"}})
	})
	return res, err
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// reconcilePlatform makes the platform roles of source idp what the rules give: a role that exists as
// manual is not duplicated and is never removed here.
func (s *Service) reconcilePlatform(ctx context.Context, tx store.Tx, u User, want []string) error {
	q := queries.New(tx)
	rows, err := q.ListPlatformRoles(ctx, u.ID)
	if err != nil {
		return err
	}
	have := map[string]string{}
	for _, r := range rows {
		have[r.Role] = r.Source
	}
	want = sortedUnique(want)
	for _, r := range want {
		if _, ok := have[r]; ok {
			continue
		}
		if err := q.AddPlatformRole(ctx, queries.AddPlatformRoleParams{UserID: u.ID, Role: r, Source: sourceIDP}); err != nil {
			return err
		}
		if err := audit.RecordPlatform(ctx, tx, audit.Event{Actor: idpActor(u), Action: "platform_role.assign", ResourceType: "user", ResourceID: u.ID,
			Outcome: "success", Detail: map[string]any{"role": r, "source": sourceIDP}}); err != nil {
			return err
		}
	}
	for _, r := range rows {
		if r.Source != sourceIDP || contains(want, r.Role) {
			continue
		}
		if _, err := q.RemovePlatformRole(ctx, queries.RemovePlatformRoleParams{UserID: u.ID, Role: r.Role}); err != nil {
			return err
		}
		if err := audit.RecordPlatform(ctx, tx, audit.Event{Actor: idpActor(u), Action: "platform_role.remove", ResourceType: "user", ResourceID: u.ID,
			Outcome: "success", Detail: map[string]any{"role": r.Role, "source": sourceIDP}}); err != nil {
			return err
		}
	}
	return nil
}

// reconcileTenants runs one transaction of the tenant's own for each tenant that has something to
// change: the tenants the user is a member of, and those that the rules give it a role in.
func (s *Service) reconcileTenants(ctx context.Context, u User, member []string, want map[string][]string,
	assignable func(context.Context, *queries.Queries, tenant.ID, []string) ([]string, error)) error {
	ids := map[string]bool{}
	for _, id := range member {
		ids[id] = true
	}
	for id := range want {
		ids[id] = true
	}
	order := make([]string, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	for _, raw := range order {
		tid, err := tenant.ParseID(raw)
		if err != nil {
			return err
		}
		if err := s.reconcileTenant(ctx, tid, u, sortedUnique(want[raw]), assignable); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcileTenant(ctx context.Context, tid tenant.ID, u User, want []string,
	assignable func(context.Context, *queries.Queries, tenant.ID, []string) ([]string, error)) error {
	return s.o.DB.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if assignable != nil && len(want) > 0 {
			var err error
			if want, err = assignable(ctx, q, tid, want); err != nil {
				return err
			}
		}
		rows, err := q.ListMembershipRoles(ctx, u.ID)
		if err != nil {
			return err
		}
		have := map[string]string{}
		var haveRoles []string
		for _, r := range rows {
			have[r.Role] = r.Source
			haveRoles = append(haveRoles, r.Role)
		}
		var add, remove []string
		for _, r := range want {
			if _, ok := have[r]; !ok {
				add = append(add, r)
			}
		}
		for _, r := range rows {
			if r.Source == sourceIDP && !contains(want, r.Role) {
				remove = append(remove, r.Role)
			}
		}
		if len(add) == 0 && len(remove) == 0 {
			return nil
		}
		cur, err := q.GetMember(ctx, u.ID)
		exists := err == nil
		if err != nil && !errors.Is(err, store.ErrNoRows) {
			return err
		}
		if !exists {
			if _, err := q.CreateMembership(ctx, queries.CreateMembershipParams{TenantID: tid.String(), UserID: u.ID, Source: sourceIDP}); err != nil {
				return err
			}
		}
		for _, r := range add {
			if err := q.AddMembershipRole(ctx, queries.AddMembershipRoleParams{TenantID: tid.String(), UserID: u.ID, Role: r, Source: sourceIDP}); err != nil {
				return err
			}
		}
		for _, r := range remove {
			if _, err := q.RemoveMembershipRole(ctx, queries.RemoveMembershipRoleParams{UserID: u.ID, Role: r}); err != nil {
				return err
			}
		}
		now := append([]string{}, add...)
		for _, r := range haveRoles {
			if !contains(remove, r) {
				now = append(now, r)
			}
		}
		sort.Strings(now)
		ev := audit.Event{Actor: idpActor(u), ResourceType: "user", ResourceID: u.ID, Outcome: "success"}
		switch {
		case !exists:
			ev.Action, ev.Detail = "member.add", map[string]any{"roles": sortedUnique(add), "source": sourceIDP}
		case len(now) == 0 && cur.Source == sourceIDP:
			if _, err := q.DeleteMembership(ctx, u.ID); err != nil {
				return err
			}
			ev.Action, ev.Detail = "member.remove", map[string]any{"roles": sortedUnique(remove), "source": sourceIDP}
		default:
			sort.Strings(haveRoles)
			ev.Action, ev.Detail = "member.update", map[string]any{"roles": map[string]any{"from": haveRoles, "to": now}, "source": sourceIDP}
		}
		return audit.RecordTenant(ctx, tx, tid, ev)
	})
}
