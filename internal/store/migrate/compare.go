package migrate

import "fmt"

// compare checks that the ledger is a prefix of ms and returns the migrations that are not in it.
// Order of the checks: gaps, a newer schema, a different name or checksum.
func compare(applied []Applied, ms []Migration) ([]Migration, error) {
	prev := 0
	for _, a := range applied {
		if a.Version != prev+1 {
			return nil, fmt.Errorf("%w: version %d follows version %d", ErrLedger, a.Version, prev)
		}
		prev = a.Version
	}
	if k, n := len(applied), len(ms); k > n {
		return nil, fmt.Errorf("%w: schema version %d, this binary knows version %d; upgrade Pandokos", ErrSchemaNewer, k, n)
	}
	for _, a := range applied {
		m := ms[a.Version-1]
		if a.Name != m.Name || a.Checksum != m.Checksum {
			return nil, fmt.Errorf("%w: %s", ErrChecksum, m.ID())
		}
	}
	return ms[len(applied):], nil
}
