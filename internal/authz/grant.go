package authz

// CanGrant reports whether p may give, or take over, the permissions: the rule against escalation
// (sprint 4.3, section 5.4). A self permission is always allowed; an unknown name never. Under a
// support action the platform acts with the tenant's authority: any tenant permission is
// grantable, a platform permission never. Otherwise p must hold every name.
func (p Principal) CanGrant(permissions []string) bool {
	var held map[string]bool
	for _, name := range permissions {
		perm, ok := Lookup(name)
		switch {
		case !ok:
			return false
		case perm.Scope == ScopeSelf:
			continue
		case p.SupportActionID != "":
			if perm.Scope != ScopeTenant {
				return false
			}
			continue
		}
		if held == nil {
			held = map[string]bool{}
			for _, e := range p.Effective() {
				held[e] = true
			}
		}
		if !held[name] {
			return false
		}
	}
	return true
}
