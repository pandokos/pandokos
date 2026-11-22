package oidc

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	maxGroups     = 256
	maxClaimDepth = 4
)

// ParseRules parses the rules of auth.oidc.group_mappings: "<target>=<group>", the target being
// platform:<role> or tenant:<tenant name>:<role>; the group is everything after the first "=".
// Whether a role is one a user can hold, and whether a tenant exists, is the caller's to check.
func ParseRules(mappings []string) ([]Rule, error) {
	rules := make([]Rule, 0, len(mappings))
	for i, m := range mappings {
		target, group, ok := strings.Cut(m, "=")
		if !ok || target == "" || group == "" {
			return nil, fmt.Errorf("group mapping %d: want <target>=<group>", i+1)
		}
		parts := strings.Split(target, ":")
		for _, p := range parts {
			if p == "" {
				return nil, fmt.Errorf("group mapping %d: an empty part in the target", i+1)
			}
		}
		switch {
		case parts[0] == "platform" && len(parts) == 2:
			rules = append(rules, Rule{Platform: true, Role: parts[1], Group: group})
		case parts[0] == "tenant" && len(parts) == 3:
			rules = append(rules, Rule{Tenant: parts[1], Role: parts[2], Group: group})
		default:
			return nil, fmt.Errorf("group mapping %d: the target is platform:<role> or tenant:<tenant>:<role>", i+1)
		}
	}
	return rules, nil
}

// Groups reads the groups of a user from the claims of the ID token: the claim at the dotted path
// (at most four members), as an array of strings or as one string; any other shape gives none. At
// most 256 groups are read.
func Groups(claims map[string]json.RawMessage, path string) []string {
	list, _ := readGroups(claims, path)
	return list
}

// readGroups is Groups and, when the claim is there with a shape that gives no group, the name of
// that shape (a JSON type, never any content of the claim).
func readGroups(claims map[string]json.RawMessage, path string) (groups []string, badShape string) {
	if path == "" {
		return nil, ""
	}
	names := strings.Split(path, ".")
	if len(names) > maxClaimDepth {
		return nil, ""
	}
	raw, ok := claims[names[0]]
	for _, name := range names[1:] {
		if !ok {
			return nil, ""
		}
		var obj map[string]json.RawMessage
		if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &obj) != nil {
			return nil, shapeOf(raw)
		}
		raw, ok = obj[name]
	}
	if !ok || len(raw) == 0 {
		return nil, ""
	}
	switch raw[0] {
	case '"':
		var one string
		if json.Unmarshal(raw, &one) != nil {
			return nil, "string"
		}
		if one == "" {
			return nil, ""
		}
		return []string{one}, ""
	case '[':
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, "array of other values"
		}
		if len(list) == 0 {
			return nil, ""
		}
		if len(list) > maxGroups {
			list = list[:maxGroups]
		}
		return list, ""
	case 'n': // null is a claim that is not there
		return nil, ""
	}
	return nil, shapeOf(raw)
}

// shapeOf names the JSON type of a value; "" for a missing value or null.
func shapeOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return ""
	}
	return "number"
}

// Grants returns the platform roles and the roles per tenant name that rules give to groups. A
// group is compared exactly; a role given twice is given once.
func Grants(rules []Rule, groups []string) (platformRoles []string, tenantRoles map[string][]string) {
	have := make(map[string]bool, len(groups))
	for _, g := range groups {
		have[g] = true
	}
	for _, r := range rules {
		if !have[r.Group] {
			continue
		}
		if r.Platform {
			platformRoles = appendOnce(platformRoles, r.Role)
			continue
		}
		if tenantRoles == nil {
			tenantRoles = map[string][]string{}
		}
		tenantRoles[r.Tenant] = appendOnce(tenantRoles[r.Tenant], r.Role)
	}
	return platformRoles, tenantRoles
}

func appendOnce(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
