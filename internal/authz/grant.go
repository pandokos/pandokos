package authz

// CanGrant reports whether p may give, or take over, the permissions: the rule against escalation
// (sprint 4.3, section 5.4). Part A: a stub that refuses; step C4 writes it.
func (Principal) CanGrant(_ []string) bool { return false }
