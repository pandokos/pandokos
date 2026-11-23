package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// ListKEKVersions lists the versions of the key-encryption key with the completed deletions whose
// backups each still holds up.
func (s *server) ListKEKVersions(ctx context.Context, _ adminapi.ListKEKVersionsInput) (*adminapi.KEKVersionList, error) {
	var rows []queries.ListKEKVersionsWithErasuresRow
	err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListKEKVersionsWithErasures(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.KEKVersionList{Items: []adminapi.KEKVersion{}}
	for _, r := range rows {
		v := adminapi.KEKVersion{ID: r.ID, Provider: r.Provider, State: adminapi.KEKVersionState(r.State), CreatedAt: r.CreatedAt.UTC(),
			ActivatedAt: r.ActivatedAt.UTC(), ErasuresPending: int(r.ErasuresPending)}
		if r.RetiredAt != nil {
			t := r.RetiredAt.UTC()
			v.RetiredAt = &t
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}
