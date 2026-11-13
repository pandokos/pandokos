package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// ListPlatformKeys lists platform keys.
func (s *server) ListPlatformKeys(ctx context.Context, in adminapi.ListPlatformKeysInput) (*adminapi.PlatformKeyList, error) {
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, _, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListPlatformKeysRow
	err = s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListPlatformKeys(ctx, queries.ListPlatformKeysParams{State: string(in.State), After: after, Sort: sortBy, AfterID: afterID(after, id), PageSize: ask})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.PlatformKeyList{Items: []adminapi.PlatformKey{}}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(sortBy, "", rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, pkRow{r.KeyID, r.Description, r.Role, r.Bootstrap, r.ExpiresAt, r.RevokedAt, r.CreatedAt, r.State}.out())
	}
	return out, nil
}

// GetPlatformKey reads the metadata of a platform key.
func (s *server) GetPlatformKey(ctx context.Context, in adminapi.GetPlatformKeyInput) (*adminapi.PlatformKey, error) {
	var row queries.GetPlatformKeyRow
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetPlatformKey(ctx, in.Key)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	o := pkRow{row.KeyID, row.Description, row.Role, row.Bootstrap, row.ExpiresAt, row.RevokedAt, row.CreatedAt, row.State}.out()
	return &o, nil
}

// CreatePlatformKey issues a platform key with the role platform-admin.
func (s *server) CreatePlatformKey(ctx context.Context, in adminapi.CreatePlatformKeyInput) (*adminapi.PlatformKeyCreated, error) {
	b := in.Body
	if b.ExpiresAt != nil && !b.ExpiresAt.After(s.now()) {
		return nil, invalid("expires_at", "must be in the future")
	}
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.PlatformKeyCreated
	for attempt := 0; ; attempt++ {
		key, keyID, hash, gerr := authn.Generate(authn.KindPlatform)
		if gerr != nil {
			return nil, gerr
		}
		err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
			row, err := queries.New(tx).CreatePlatformKey(ctx, queries.CreatePlatformKeyParams{
				KeyID: keyID, Description: b.Description, Role: "platform-admin", SecretHash: hash[:], ExpiresAt: utcPtr(b.ExpiresAt),
			})
			if err != nil {
				return err
			}
			out = adminapi.PlatformKeyCreated{
				Key:    pkRow{row.KeyID, row.Description, row.Role, row.Bootstrap, row.ExpiresAt, row.RevokedAt, row.CreatedAt, row.State}.out(),
				Secret: revealOnce(key),
			}
			detail := map[string]any{"description": row.Description}
			if row.ExpiresAt != nil {
				detail["expires_at"] = fmtTime(row.ExpiresAt)
			}
			return audit.RecordPlatform(ctx, tx, audit.Event{Actor: audit.ActorOf(p), Action: "platform_key.create", ResourceType: "platform_key",
				ResourceID: keyID, Outcome: "success", RequestID: st.id, Detail: detail})
		})
		if kind, name, ok := store.ConstraintViolation(err); ok && kind == "unique" && name == "platform_key_key_id_key" && attempt == 0 {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &out, nil
	}
}

// RevokePlatformKey revokes a platform key; revoking a revoked key changes nothing.
func (s *server) RevokePlatformKey(ctx context.Context, in adminapi.RevokePlatformKeyInput) (*adminapi.PlatformKey, error) {
	p := principalOf(ctx)
	st := stateOf(ctx)
	var out adminapi.PlatformKey
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		n, err := q.RevokePlatformKey(ctx, in.Key)
		if err != nil {
			return err
		}
		row, err := q.GetPlatformKey(ctx, in.Key)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		out = pkRow{row.KeyID, row.Description, row.Role, row.Bootstrap, row.ExpiresAt, row.RevokedAt, row.CreatedAt, row.State}.out()
		if n == 0 {
			return nil
		}
		return audit.RecordPlatform(ctx, tx, audit.Event{Actor: audit.ActorOf(p), Action: "platform_key.revoke", ResourceType: "platform_key",
			ResourceID: in.Key, Outcome: "success", RequestID: st.id, Reason: in.Body.Reason, Detail: map[string]any{"bootstrap": row.Bootstrap}})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
