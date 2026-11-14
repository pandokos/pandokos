package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// checkAAD is the fixed associated data of the self-test of Check.
var checkAAD = []byte("pandokos/check/v1")

// Check runs at start. It wraps and unwraps 32 random bytes, then requires every row of
// kek_version to name p and to be Available. It returns the active key version for the log
// line. A fresh database has no row and passes. It writes nothing.
func Check(ctx context.Context, db *store.DB, p KeyProvider) (version string, err error) {
	probe := make([]byte, dataKeyLen)
	defer clear(probe)
	if _, err := io.ReadFull(rand.Reader, probe); err != nil {
		return "", fmt.Errorf("vault: no random bytes: %w", err)
	}
	version, wrapped, err := p.Wrap(ctx, probe, checkAAD)
	if err != nil {
		return "", fmt.Errorf("vault: the key provider cannot wrap: %w", err)
	}
	back, err := p.Unwrap(ctx, version, wrapped, checkAAD)
	if err != nil {
		return "", fmt.Errorf("vault: the key provider cannot unwrap what it wrapped: %w", err)
	}
	ok := bytes.Equal(back, probe)
	clear(back)
	if !ok {
		return "", errors.New("vault: the key provider unwrapped other bytes than it wrapped")
	}
	var rows []queries.ListKEKVersionsRow
	err = db.Platform(ctx, func(c context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListKEKVersions(c)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("vault: cannot read the key versions: %w", err)
	}
	for _, r := range rows {
		if r.Provider != p.Name() || p.Available(ctx, r.ID) != nil {
			return "", fmt.Errorf("the database holds data keys wrapped by key-encryption key version %s which this configuration does not provide; see docs/operations/vault.md", r.ID)
		}
	}
	return version, nil
}
