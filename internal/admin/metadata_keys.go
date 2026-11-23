package admin

import (
	"context"
	"sort"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// metadataKeysOut is the representation of a tenant's policy; updated is nil for no policy.
func metadataKeysOut(keys []string, updated *time.Time) adminapi.MetadataKeys {
	if updated == nil {
		return adminapi.MetadataKeys{Keys: []string{}}
	}
	u := updated.UTC()
	k := append([]string{}, keys...)
	sort.Strings(k)
	return adminapi.MetadataKeys{Restricted: true, Keys: k, UpdatedAt: &u}
}

// GetMetadataKeys reads the caller metadata keys a tenant accepts.
func (s *server) GetMetadataKeys(ctx context.Context, in adminapi.GetMetadataKeysInput) (*adminapi.MetadataKeys, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	out := metadataKeysOut(nil, nil)
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		row, err := queries.New(tx).GetMetadataKeyPolicy(ctx)
		switch {
		case isNoRowsErr(err):
			return nil
		case err != nil:
			return err
		}
		out = metadataKeysOut(row.Keys, &row.UpdatedAt)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SetMetadataKeys replaces the policy of a tenant. The generated validation checks the count and
// the format; a repeated key is refused here.
func (s *server) SetMetadataKeys(ctx context.Context, in adminapi.SetMetadataKeysInput) (*adminapi.MetadataKeys, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	keys := append([]string{}, nonNil(in.Body.Keys)...)
	sort.Strings(keys)
	for i := 1; i < len(keys); i++ {
		if keys[i] == keys[i-1] {
			return nil, invalid("keys", "must not repeat a key")
		}
	}
	var out adminapi.MetadataKeys
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		row, err := q.SetMetadataKeyPolicy(ctx, keys)
		if err != nil {
			return err
		}
		out = metadataKeysOut(row.Keys, &row.UpdatedAt)
		return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: "tenant.metadata_keys_set", ResourceType: "tenant",
			ResourceID: tid.String(), Outcome: "success", RequestID: stateOf(ctx).id, Detail: map[string]any{"keys": keys}})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearMetadataKeys removes the policy of a tenant; clearing none changes nothing.
func (s *server) ClearMetadataKeys(ctx context.Context, in adminapi.ClearMetadataKeysInput) (*adminapi.MetadataKeys, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		prev, err := q.GetMetadataKeyPolicy(ctx)
		if isNoRowsErr(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := q.ClearMetadataKeyPolicy(ctx); err != nil {
			return err
		}
		p := append([]string{}, prev.Keys...)
		sort.Strings(p)
		return audit.RecordTenant(ctx, tx, tid, audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: "tenant.metadata_keys_clear", ResourceType: "tenant",
			ResourceID: tid.String(), Outcome: "success", RequestID: stateOf(ctx).id, Detail: map[string]any{"keys": p}})
	})
	if err != nil {
		return nil, err
	}
	out := metadataKeysOut(nil, nil)
	return &out, nil
}
