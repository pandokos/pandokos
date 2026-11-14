package pricing

import "time"

// VersionAt returns the version in force at t: the one with the greatest ValidFrom that is not
// after t. The versions need not be sorted.
func VersionAt(versions []Version, t time.Time) (Version, bool) {
	var best Version
	found := false
	for _, v := range versions {
		if v.ValidFrom.After(t) {
			continue
		}
		if !found || v.ValidFrom.After(best.ValidFrom) {
			best, found = v, true
		}
	}
	return best, found
}
