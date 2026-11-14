package genaiprices

import (
	"regexp"
	"strings"
)

// Clause is a match clause of the dataset. Every member that is present must hold; a clause
// with no member matches nothing.
type Clause struct {
	Equals     *string  `json:"equals"`
	StartsWith *string  `json:"starts_with"`
	EndsWith   *string  `json:"ends_with"`
	Contains   *string  `json:"contains"`
	Regex      *string  `json:"regex"`
	Or         []Clause `json:"or"`
	And        []Clause `json:"and"`

	re *compiled // set by prepare when Regex is present
}

// compiled is a pattern compiled once; re is nil when RE2 refuses it.
type compiled struct{ re *regexp.Regexp }

// prepare compiles the regular expressions of the clause and its members, sharing the cache.
// It reports whether a pattern was refused.
func (c *Clause) prepare(cache map[string]*compiled) (refused bool) {
	if c.Regex != nil {
		cp, ok := cache[*c.Regex]
		if !ok {
			re, err := regexp.Compile(*c.Regex)
			if err != nil {
				re = nil
			}
			cp = &compiled{re: re}
			cache[*c.Regex] = cp
		}
		c.re = cp
		refused = cp.re == nil
	}
	for i := range c.Or {
		refused = c.Or[i].prepare(cache) || refused
	}
	for i := range c.And {
		refused = c.And[i].prepare(cache) || refused
	}
	return refused
}

// matches reports whether the clause accepts id: exact, case-sensitive string tests; the regular
// expression searches the identifier (regexp.MatchString semantics).
func (c *Clause) matches(id string) bool {
	set := false
	if c.Equals != nil {
		set = true
		if id != *c.Equals {
			return false
		}
	}
	if c.StartsWith != nil {
		set = true
		if !strings.HasPrefix(id, *c.StartsWith) {
			return false
		}
	}
	if c.EndsWith != nil {
		set = true
		if !strings.HasSuffix(id, *c.EndsWith) {
			return false
		}
	}
	if c.Contains != nil {
		set = true
		if !strings.Contains(id, *c.Contains) {
			return false
		}
	}
	if c.Regex != nil {
		set = true
		if c.re == nil || c.re.re == nil || !c.re.re.MatchString(id) {
			return false
		}
	}
	if len(c.Or) > 0 {
		set = true
		found := false
		for i := range c.Or {
			if c.Or[i].matches(id) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(c.And) > 0 {
		set = true
		for i := range c.And {
			if !c.And[i].matches(id) {
				return false
			}
		}
	}
	return set
}
