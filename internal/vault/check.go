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

// The states of a key-encryption key version (migration 0016).
const (
	stateActive  = "active"
	stateRetired = "retired"
)

// checkAAD is the fixed associated data of the self-test of Check.
var checkAAD = []byte("pandokos/check/v1")

// Check runs at start. It calls ProbeAll (Probe of the provider that wraps and of every transit
// provider that is configured besides it: a move to the service must fail before the restart that
// makes it the active one), then requires every version the
// database still needs (state active or retiring; a retired version wraps no data key) to name p
// and to be Available. It returns the active key version for the log
// line. A fresh database has no row and passes. It writes nothing. When the provider's own version
// is retiring or retired, it answers ErrKeyReplaced, naming that version and the active one.
func Check(ctx context.Context, db *store.DB, p KeyProvider) (version string, err error) {
	version, err = ProbeAll(ctx, p)
	if err != nil {
		return "", err
	}
	return checkVersions(ctx, db, p, version)
}

// checkVersions is the part of Check that reads the database, after Probe.
func checkVersions(ctx context.Context, db *store.DB, p KeyProvider, version string) (string, error) {
	var err error
	var rows []queries.ListKEKVersionsRow
	err = db.Platform(ctx, func(c context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListKEKVersions(c)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("vault: cannot read the key versions: %w", err)
	}
	// The configured version itself was replaced (retiring or retired): that is the cause to name,
	// with the version that replaced it, whatever else the configuration lacks.
	active := ""
	for _, r := range rows {
		if r.State == stateActive {
			active = r.ID
		}
	}
	for _, r := range rows {
		if r.ID == version && r.State != stateActive {
			return "", &replacedError{configured: version, active: active}
		}
	}
	for _, r := range rows {
		if r.State == stateRetired {
			continue
		}
		owner := p.Name()
		if po, ok := p.(interface{ ProviderOf(string) string }); ok {
			owner = po.ProviderOf(r.ID)
		}
		if r.Provider != owner {
			return "", fmt.Errorf("the database holds data keys wrapped by key-encryption key version %s which this configuration does not provide; see docs/operations/vault.md", r.ID)
		}
		if err := p.Available(ctx, r.ID); err != nil {
			if errors.Is(err, ErrProviderUnavailable) {
				return "", fmt.Errorf("vault: cannot tell whether key-encryption key version %s is available: %w", r.ID, err)
			}
			return "", fmt.Errorf("the database holds data keys wrapped by key-encryption key version %s which this configuration does not provide; see docs/operations/vault.md", r.ID)
		}
	}
	return version, nil
}

// ProbeAll is Probe of p, and of every transit provider that p holds besides the one that wraps
// (a router: the service configured for unwrapping only while the key file still wraps, §5.2,
// §5.12). It returns the version the active provider wraps under.
func ProbeAll(ctx context.Context, p KeyProvider) (version string, err error) {
	version, err = Probe(ctx, p)
	if err != nil {
		return "", err
	}
	r, ok := p.(interface{ Others() []KeyProvider })
	if !ok {
		return version, nil
	}
	for _, o := range r.Others() {
		if _, ok := o.(*TransitProvider); !ok {
			continue
		}
		if _, err := Probe(ctx, o); err != nil {
			return "", fmt.Errorf("the %s key provider, configured but not the one that wraps: %w", o.Name(), err)
		}
	}
	return version, nil
}

// checkOtherAAD is the associated data of the negative probe: the same wrapped key must not open
// under it.
var checkOtherAAD = []byte("pandokos/check/v1/other")

// Probe is the part of Check that needs no database: the round trip of a random probe and the
// negative probe on the associated data (§5.2). It returns the version p wraps under.
func Probe(ctx context.Context, p KeyProvider) (version string, err error) {
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
	other, err := p.Unwrap(ctx, version, wrapped, checkOtherAAD)
	switch {
	case err == nil:
		clear(other)
		return "", ErrAADNotBound
	case errors.Is(err, ErrUnwrap):
		return version, nil
	default:
		return "", fmt.Errorf("vault: the key provider cannot prove that it binds associated data: %w", err)
	}
}
