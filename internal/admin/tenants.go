package admin

import (
	"context"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// ListTenants lists tenants.
func (s *server) ListTenants(ctx context.Context, in adminapi.ListTenantsInput) (*adminapi.TenantList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, name, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.PandokosTenant
	err = s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListTenants(ctx, queries.ListTenantsParams{
			State: string(in.State), Name: in.Name, After: after, Sort: sortBy, AfterID: afterID(after, id), AfterName: name, PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.TenantList{Items: []adminapi.Tenant{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = encodeCursor(sortBy, last.Name, last.ID)
			break
		}
		out.Items = append(out.Items, tenantOut(r))
	}
	return out, nil
}

// afterID returns a UUID the statement can cast when there is no cursor: the value is not used.
func afterID(after bool, id string) string {
	if after {
		return id
	}
	return "00000000-0000-0000-0000-000000000000"
}

// GetTenant reads a tenant.
func (s *server) GetTenant(ctx context.Context, in adminapi.GetTenantInput) (*adminapi.Tenant, error) {
	var row queries.PandokosTenant
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetTenant(ctx, in.Tenant)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	t := tenantOut(row)
	return &t, nil
}

// CreateTenant creates a tenant with its first administrator, in one transaction.
func (s *server) CreateTenant(ctx context.Context, in adminapi.CreateTenantInput) (*adminapi.TenantCreated, error) {
	p := principalOf(ctx)
	st := stateOf(ctx)
	b := in.Body
	if b.AdminKeyExpiresAt != nil && !b.AdminKeyExpiresAt.After(s.now()) {
		return nil, invalid("admin_key_expires_at", "must be in the future")
	}
	saName := b.AdminServiceAccountName
	if saName == "" {
		saName = "admin"
	}
	var out adminapi.TenantCreated
	for attempt := 0; ; attempt++ {
		key, keyID, hash, err := authn.Generate(authn.KindTenant)
		if err != nil {
			return nil, err
		}
		err = s.db.Provision(ctx, func(ctx context.Context, tx store.Tx, bind func(context.Context, tenant.ID) error) error {
			q := queries.New(tx)
			t, err := q.CreateTenant(ctx, queries.CreateTenantParams{Name: b.Name, DisplayName: b.DisplayName})
			if err != nil {
				return err
			}
			tid, err := tenant.ParseID(t.ID)
			if err != nil {
				return err
			}
			if err := bind(ctx, tid); err != nil {
				return err
			}
			created, kekVersion, err := s.sealer.EnsureDataKey(ctx, tx, tid)
			if err != nil {
				return err
			}
			sa, err := q.CreateServiceAccount(ctx, queries.CreateServiceAccountParams{TenantID: t.ID, Name: saName})
			if err != nil {
				return err
			}
			if err := q.CreateRoleAssignment(ctx, queries.CreateRoleAssignmentParams{TenantID: t.ID, ServiceAccountID: sa.ID, Role: "tenant-admin"}); err != nil {
				return err
			}
			var exp *time.Time
			if b.AdminKeyExpiresAt != nil {
				u := b.AdminKeyExpiresAt.UTC()
				exp = &u
			}
			vk, err := q.CreateVirtualKey(ctx, queries.CreateVirtualKeyParams{
				TenantID: t.ID, ServiceAccountID: sa.ID, KeyID: keyID, SecretHash: hash[:], ExpiresAt: exp, CredentialID: nil, Models: nil, ModelsRestricted: false,
			})
			if err != nil {
				return err
			}
			actor := audit.ActorOf(p)
			if err := audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: actor, Action: "service_account.create", ResourceType: "service_account", ResourceID: sa.ID,
				Outcome: "success", RequestID: st.id, Detail: map[string]any{"name": saName, "roles": []string{"tenant-admin"}}}); err != nil {
				return err
			}
			vkDetail := map[string]any{"service_account_id": sa.ID}
			if exp != nil {
				vkDetail["expires_at"] = fmtTime(exp)
			}
			if err := audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: actor, Action: "virtual_key.create", ResourceType: "virtual_key", ResourceID: keyID,
				Outcome: "success", RequestID: st.id, Detail: vkDetail}); err != nil {
				return err
			}
			if created {
				if err := audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: actor, Action: "data_key.create", ResourceType: "data_key", ResourceID: "1",
					Outcome: "success", RequestID: st.id, Detail: map[string]any{"version": 1, "kek_version": kekVersion}}); err != nil {
					return err
				}
			}
			active, err := q.SetTenantState(ctx, queries.SetTenantStateParams{ID: t.ID, State: "active"})
			if err != nil {
				return err
			}
			if err := audit.RecordPlatform(ctx, tx, audit.Event{Actor: actor, Action: "tenant.create", ResourceType: "tenant", ResourceID: t.ID,
				Outcome: "success", RequestID: st.id, Detail: map[string]any{"name": b.Name, "display_name": b.DisplayName,
					"admin_service_account_id": sa.ID, "admin_key_id": keyID}}); err != nil {
				return err
			}
			saOut := saRow{sa.ID, sa.TenantID, sa.Name, sa.Description, sa.State, sa.ValidFrom, sa.ValidUntil, sa.CreatedAt, sa.UpdatedAt, []string{"tenant-admin"}}
			out = adminapi.TenantCreated{
				Tenant:              tenantOut(active),
				AdminServiceAccount: saOut.out(),
				AdminKey: adminapi.VirtualKeyCreated{
					Key:    vkRow{vk.KeyID, vk.TenantID, vk.ServiceAccountID, vk.Description, vk.Scopes, vk.Scoped, vk.ExpiresAt, vk.RevokedAt, vk.CreatedAt, vk.State, vk.CredentialID}.out(),
					Secret: revealOnce(key),
				},
			}
			return nil
		})
		if kind, name, ok := store.ConstraintViolation(err); ok && kind == "unique" {
			if name == "virtual_key_key_id_key" && attempt == 0 {
				continue
			}
			if name == "tenant_name_key" {
				return nil, errConflict
			}
		}
		if err != nil {
			return nil, err
		}
		return &out, nil
	}
}

// UpdateTenant changes the name and the display name of a tenant.
func (s *server) UpdateTenant(ctx context.Context, in adminapi.UpdateTenantInput) (*adminapi.Tenant, error) {
	p := principalOf(ctx)
	st := stateOf(ctx)
	if !in.Body.Name.Set && !in.Body.DisplayName.Set {
		return nil, invalid("body", "must change the name or the display name")
	}
	var out adminapi.Tenant
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cur, err := q.GetTenantForUpdate(ctx, in.Tenant)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if cur.State == "deleted" {
			return errInvalidState
		}
		detail := map[string]any{}
		par := queries.UpdateTenantParams{ID: cur.ID}
		if in.Body.Name.Set && in.Body.Name.Value != cur.Name {
			par.SetName, par.Name = true, in.Body.Name.Value
			detail["name"] = change(cur.Name, in.Body.Name.Value)
		}
		if in.Body.DisplayName.Set && in.Body.DisplayName.Value != cur.DisplayName {
			par.SetDisplayName, par.DisplayName = true, in.Body.DisplayName.Value
			detail["display_name"] = change(cur.DisplayName, in.Body.DisplayName.Value)
		}
		if len(detail) == 0 {
			out = tenantOut(cur)
			return nil
		}
		upd, err := q.UpdateTenant(ctx, par)
		if err != nil {
			return err
		}
		out = tenantOut(upd)
		return audit.RecordPlatform(ctx, tx, audit.Event{Actor: audit.ActorOf(p), Action: "tenant.update", ResourceType: "tenant", ResourceID: cur.ID,
			Outcome: "success", RequestID: st.id, Detail: detail})
	})
	if kind, _, ok := store.ConstraintViolation(err); ok && kind == "unique" {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// transition suspends or reactivates a tenant.
func (s *server) transition(ctx context.Context, id, reason, from, to, action string) (*adminapi.Tenant, error) {
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.Tenant
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cur, err := q.GetTenantForUpdate(ctx, id)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		switch cur.State {
		case to:
			out = tenantOut(cur)
			return nil
		case from:
		default:
			return errInvalidState
		}
		upd, err := q.SetTenantState(ctx, queries.SetTenantStateParams{ID: cur.ID, State: to})
		if err != nil {
			return err
		}
		out = tenantOut(upd)
		return audit.RecordPlatform(ctx, tx, audit.Event{Actor: audit.ActorOf(p), Action: action, ResourceType: "tenant", ResourceID: cur.ID,
			Outcome: "success", RequestID: st.id, Reason: reason, Detail: map[string]any{"from": from, "to": to}})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SuspendTenant suspends a tenant.
func (s *server) SuspendTenant(ctx context.Context, in adminapi.SuspendTenantInput) (*adminapi.Tenant, error) {
	return s.transition(ctx, in.Tenant, in.Body.Reason, "active", "suspended", "tenant.suspend")
}

// ReactivateTenant reactivates a suspended tenant.
func (s *server) ReactivateTenant(ctx context.Context, in adminapi.ReactivateTenantInput) (*adminapi.Tenant, error) {
	return s.transition(ctx, in.Tenant, in.Body.Reason, "suspended", "active", "tenant.reactivate")
}
