package genaiprices

import (
	_ "embed" // the snapshot and its description are embedded
	"strings"
	"sync"
	"time"
)

//go:embed snapshot/data.json
var snapshotData []byte

//go:embed snapshot/SOURCE
var snapshotSource string

// Snapshot returns the embedded dataset: the upstream file at the pinned commit, byte for byte.
// The caller must not modify it.
func Snapshot() []byte { return snapshotData }

type snapshotMeta struct {
	commit    string
	retrieved time.Time
	sha256    string
}

// snapshotInfo parses SOURCE once, on first use.
var snapshotInfo = sync.OnceValue(func() snapshotMeta {
	var m snapshotMeta
	for _, line := range strings.Split(snapshotSource, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "commit":
			m.commit = val
		case "sha256":
			m.sha256 = val
		case "retrieved":
			if t, err := time.Parse(time.DateOnly, val); err == nil {
				m.retrieved = t
			}
		}
	}
	return m
})

// SnapshotInfo returns the upstream commit, the retrieval date (a UTC day) and the SHA-256 of
// the embedded snapshot, as SOURCE records them.
func SnapshotInfo() (commit string, retrieved time.Time, sha256 string) {
	m := snapshotInfo()
	return m.commit, m.retrieved, m.sha256
}
