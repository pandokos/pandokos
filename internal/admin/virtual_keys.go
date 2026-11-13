package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

func vkFromGet(r queries.GetVirtualKeyRow) vkRow {
	return vkRow{r.KeyID, r.TenantID, r.ServiceAccountID, r.Description, r.Scopes, r.Scoped, r.ExpiresAt, r.RevokedAt, r.CreatedAt, r.State}
}

// ListVirtualKeys lists the virtual keys of a tenant.
func (s *server) ListVirtualKeys(ctx context.Context, in adminapi.ListVirtualKeysInput) (*adminapi.VirtualKeyList, error) {
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
	saID := in.ServiceAccountID
	if saID == "" {
		saID = "00000000-0000-0000-0000-000000000000"
	}
	var rows []queries.ListVirtualKeysRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListVirtualKeys(ctx, queries.ListVirtualKeysParams{
			State: string(in.State), ByServiceAccount: in.ServiceAccountID != "", ServiceAccountID: saID,
			After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.VirtualKeyList{Items: []adminapi.VirtualKey{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, vkRow{r.KeyID, r.TenantID, r.ServiceAccountID, r.Description, r.Scopes, r.Scoped, r.ExpiresAt, r.RevokedAt, r.CreatedAt, r.State}.out())
	}
	return out, nil
}

// GetVirtualKey reads the metadata of a virtual key.
func (s *server) GetVirtualKey(ctx context.Context, in adminapi.GetVirtualKeyInput) (*adminapi.VirtualKey, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var row queries.GetVirtualKeyRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetVirtualKey(ctx, in.Key)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	o := vkFromGet(row).out()
	return &o, nil
}

// CreateVirtualKey issues a virtual key for a service account.
func (s *server) CreateVirtualKey(ctx context.Context, in adminapi.CreateVirtualKeyInput) (*adminapi.VirtualKeyCreated, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	for _, sc := range b.Scopes {
		if perm, ok := authz.Lookup(sc); !ok || perm.Scope == authz.ScopePlatform {
			return nil, invalid("scopes", "must name permissions of tenants or of the caller")
		}
	}
	if b.ExpiresAt != nil && !b.ExpiresAt.After(s.now()) {
		return nil, invalid("expires_at", "must be in the future")
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.VirtualKeyCreated
	for attempt := 0; ; attempt++ {
		key, keyID, hash, gerr := authn.Generate(authn.KindTenant)
		if gerr != nil {
			return nil, gerr
		}
		err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			q := queries.New(tx)
			if _, err := q.LockServiceAccount(ctx, b.ServiceAccountID); err != nil {
				if isNoRowsErr(err) {
					return invalid("service_account_id", "must name a service account of the tenant")
				}
				return err
			}
			row, err := q.CreateVirtualKey(ctx, queries.CreateVirtualKeyParams{
				TenantID: tid.String(), ServiceAccountID: b.ServiceAccountID, KeyID: keyID, Description: b.Description, SecretHash: hash[:],
				Scoped: len(b.Scopes) > 0, Scopes: b.Scopes, ExpiresAt: utcPtr(b.ExpiresAt), CredentialID: nil,
			})
			if err != nil {
				return err
			}
			detail := map[string]any{"service_account_id": row.ServiceAccountID}
			if row.Scoped {
				detail["scopes"] = row.Scopes
			}
			if row.ExpiresAt != nil {
				detail["expires_at"] = fmtTime(row.ExpiresAt)
			}
			out = adminapi.VirtualKeyCreated{
				Key:    vkRow{row.KeyID, row.TenantID, row.ServiceAccountID, row.Description, row.Scopes, row.Scoped, row.ExpiresAt, row.RevokedAt, row.CreatedAt, row.State}.out(),
				Secret: revealOnce(key),
			}
			return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(p), Action: "virtual_key.create", ResourceType: "virtual_key",
				ResourceID: keyID, Outcome: "success", RequestID: st.id, Detail: detail})
		})
		if kind, name, ok := store.ConstraintViolation(err); ok && kind == "unique" && name == "virtual_key_key_id_key" && attempt == 0 {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &out, nil
	}
}

// RevokeVirtualKey revokes a virtual key; revoking a revoked key changes nothing.
func (s *server) RevokeVirtualKey(ctx context.Context, in adminapi.RevokeVirtualKeyInput) (*adminapi.VirtualKey, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.VirtualKey
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		n, err := q.RevokeVirtualKey(ctx, in.Key)
		if err != nil {
			return err
		}
		row, err := q.GetVirtualKey(ctx, in.Key)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		out = vkFromGet(row).out()
		if n == 0 {
			return nil
		}
		return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(p), Action: "virtual_key.revoke", ResourceType: "virtual_key",
			ResourceID: in.Key, Outcome: "success", RequestID: st.id, Reason: in.Body.Reason, Detail: map[string]any{"service_account_id": row.ServiceAccountID}})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
