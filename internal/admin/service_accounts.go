package admin

import (
	"context"
	"sort"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// checkRoles refuses a role that does not exist or is not a role of service accounts.
func checkRoles(roles []string) error {
	for _, r := range roles {
		if role, ok := authz.RoleByName(r); !ok || role.Kind != authz.KindServiceAccount {
			return invalid("roles", "must name built-in roles of service accounts")
		}
	}
	return nil
}

func sortedCopy(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}

func mapSAConstraint(err error) error {
	kind, name, ok := store.ConstraintViolation(err)
	switch {
	case ok && kind == "unique" && name == "service_account_name_key":
		return errConflict
	case ok && kind == "check" && name == "service_account_validity_order":
		return invalid("valid_until", "must be later than valid_from")
	}
	return err
}

// ListServiceAccounts lists the service accounts of a tenant.
func (s *server) ListServiceAccounts(ctx context.Context, in adminapi.ListServiceAccountsInput) (*adminapi.ServiceAccountList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, name, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListServiceAccountsRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListServiceAccounts(ctx, queries.ListServiceAccountsParams{
			State: string(in.State), Name: in.Name, After: after, Sort: sortBy, AfterID: afterID(after, id), AfterName: name, PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.ServiceAccountList{Items: []adminapi.ServiceAccount{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = encodeCursor(sortBy, last.Name, last.ID)
			break
		}
		out.Items = append(out.Items, saFromList(r).out())
	}
	return out, nil
}

// GetServiceAccount reads a service account.
func (s *server) GetServiceAccount(ctx context.Context, in adminapi.GetServiceAccountInput) (*adminapi.ServiceAccount, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var row queries.GetServiceAccountRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetServiceAccount(ctx, in.ServiceAccount)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	o := saFromGet(row).out()
	return &o, nil
}

// CreateServiceAccount creates a service account with its roles.
func (s *server) CreateServiceAccount(ctx context.Context, in adminapi.CreateServiceAccountInput) (*adminapi.ServiceAccount, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if err := checkRoles(b.Roles); err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.ServiceAccount
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		sa, err := q.CreateServiceAccount(ctx, queries.CreateServiceAccountParams{
			TenantID: tid.String(), Name: b.Name, Description: b.Description, ValidFrom: utcPtr(b.ValidFrom), ValidUntil: utcPtr(b.ValidUntil),
		})
		if err != nil {
			return err
		}
		for _, r := range b.Roles {
			if err := q.CreateRoleAssignment(ctx, queries.CreateRoleAssignmentParams{TenantID: tid.String(), ServiceAccountID: sa.ID, Role: r}); err != nil {
				return err
			}
		}
		row, err := q.GetServiceAccount(ctx, sa.ID)
		if err != nil {
			return err
		}
		out = saFromGet(row).out()
		detail := map[string]any{"name": b.Name, "roles": sortedCopy(b.Roles)}
		if sa.ValidFrom != nil {
			detail["valid_from"] = fmtTime(sa.ValidFrom)
		}
		if sa.ValidUntil != nil {
			detail["valid_until"] = fmtTime(sa.ValidUntil)
		}
		return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(p), Action: "service_account.create", ResourceType: "service_account",
			ResourceID: sa.ID, Outcome: "success", RequestID: st.id, Detail: detail})
	})
	if err != nil {
		return nil, mapSAConstraint(err)
	}
	return &out, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// UpdateServiceAccount changes the members that are present.
func (s *server) UpdateServiceAccount(ctx context.Context, in adminapi.UpdateServiceAccountInput) (*adminapi.ServiceAccount, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if b.Name == "" && b.Description == "" && !b.ValidFrom.Set && !b.ValidUntil.Set && b.Roles == nil {
		return nil, invalid("body", "must change at least one member")
	}
	if err := checkRoles(b.Roles); err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.ServiceAccount
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if _, err := q.LockServiceAccount(ctx, in.ServiceAccount); err != nil {
			if isNoRowsErr(err) {
				return errNotFound
			}
			return err
		}
		cur, err := q.GetServiceAccount(ctx, in.ServiceAccount)
		if err != nil {
			return err
		}
		detail := map[string]any{}
		par := queries.UpdateServiceAccountParams{ID: cur.ID}
		if b.Name != "" && b.Name != cur.Name {
			par.SetName, par.Name = true, b.Name
			detail["name"] = change(cur.Name, b.Name)
		}
		if b.Description != "" && b.Description != cur.Description {
			par.SetDescription, par.Description = true, b.Description
			detail["description"] = change(cur.Description, b.Description)
		}
		for _, bound := range []struct {
			name  string
			in    adminapi.Nullable[time.Time]
			cur   *time.Time
			set   *bool
			value **time.Time
		}{
			{"valid_from", b.ValidFrom, cur.ValidFrom, &par.SetValidFrom, &par.ValidFrom},
			{"valid_until", b.ValidUntil, cur.ValidUntil, &par.SetValidUntil, &par.ValidUntil},
		} {
			if !bound.in.Set {
				continue
			}
			var want *time.Time
			if !bound.in.Null {
				v := bound.in.Value.UTC()
				want = &v
			}
			if sameTime(want, bound.cur) {
				continue
			}
			*bound.set, *bound.value = true, want
			detail[bound.name] = change(fmtTime(bound.cur), fmtTime(want))
		}
		var newRoles []string
		rolesChange := b.Roles != nil && !sameStrings(sortedCopy(b.Roles), sortedCopy(cur.Roles))
		if rolesChange {
			newRoles = sortedCopy(b.Roles)
			detail["roles"] = change(sortedCopy(cur.Roles), newRoles)
		}
		if len(detail) == 0 {
			out = saFromGet(cur).out()
			return nil
		}
		if par.SetName || par.SetDescription || par.SetValidFrom || par.SetValidUntil {
			if _, err := q.UpdateServiceAccount(ctx, par); err != nil {
				return err
			}
		}
		if rolesChange {
			if _, err := q.DeleteRoleAssignments(ctx, cur.ID); err != nil {
				return err
			}
			for _, r := range newRoles {
				if err := q.CreateRoleAssignment(ctx, queries.CreateRoleAssignmentParams{TenantID: tid.String(), ServiceAccountID: cur.ID, Role: r}); err != nil {
					return err
				}
			}
		}
		row, err := q.GetServiceAccount(ctx, cur.ID)
		if err != nil {
			return err
		}
		out = saFromGet(row).out()
		return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(p), Action: "service_account.update", ResourceType: "service_account",
			ResourceID: cur.ID, Outcome: "success", RequestID: st.id, Detail: detail})
	})
	if err != nil {
		return nil, mapSAConstraint(err)
	}
	return &out, nil
}

func (s *server) setSAState(ctx context.Context, tenantText, id, reason, to, action string) (*adminapi.ServiceAccount, error) {
	tid, err := tenantID(tenantText)
	if err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.ServiceAccount
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		lock, err := q.LockServiceAccount(ctx, id)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if lock.State != to {
			if _, err := q.SetServiceAccountState(ctx, queries.SetServiceAccountStateParams{ID: id, State: to}); err != nil {
				return err
			}
			if err := audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(p), Action: action, ResourceType: "service_account",
				ResourceID: id, Outcome: "success", RequestID: st.id, Reason: reason}); err != nil {
				return err
			}
		}
		row, err := q.GetServiceAccount(ctx, id)
		if err != nil {
			return err
		}
		out = saFromGet(row).out()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DisableServiceAccount disables a service account.
func (s *server) DisableServiceAccount(ctx context.Context, in adminapi.DisableServiceAccountInput) (*adminapi.ServiceAccount, error) {
	return s.setSAState(ctx, in.Tenant, in.ServiceAccount, in.Body.Reason, "disabled", "service_account.disable")
}

// EnableServiceAccount enables a service account.
func (s *server) EnableServiceAccount(ctx context.Context, in adminapi.EnableServiceAccountInput) (*adminapi.ServiceAccount, error) {
	return s.setSAState(ctx, in.Tenant, in.ServiceAccount, in.Body.Reason, "active", "service_account.enable")
}
