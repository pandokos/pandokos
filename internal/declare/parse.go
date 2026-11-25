package declare

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// MaxDocument is the largest document Parse accepts.
const MaxDocument = 4 << 20

// InvalidError is a document that cannot be applied: the command line answers it with exit code 2.
// Its message names what is wrong and never repeats a value of the document.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return "invalid document: " + e.Msg }

func invalidf(format string, args ...any) error {
	return &InvalidError{Msg: fmt.Sprintf(format, args...)}
}

// forbidden are the members no document holds: a document carries no secret.
var forbidden = map[string]bool{
	"secret": true, "password": true, "token": true, "api_key": true, "apikey": true, "key_secret": true,
	"secret_key": true, "credential": true, "credentials": true, "private_key": true,
}

var reBackticked = regexp.MustCompile("`[^`]*`")

// Parse decodes a document: strict about members (an unknown one is an error), without anchors or
// aliases, one document, at most MaxDocument bytes. It does not check the values (Validate does).
func Parse(data []byte) (*Document, error) {
	if len(data) > MaxDocument {
		return nil, invalidf("the document is over 4 MiB")
	}
	var root yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, invalidf("the document is empty")
		}
		return nil, invalidf("not valid YAML: %s", scrub(err))
	}
	if err := checkNode(&root); err != nil {
		return nil, err
	}
	var doc Document
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, invalidf("%s", scrub(err))
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, invalidf("a file holds one document")
	}
	return &doc, nil
}

// scrub is the message of a YAML error without the values it quotes.
func scrub(err error) string {
	return strings.TrimSpace(strings.TrimPrefix(reBackticked.ReplaceAllString(err.Error(), "a value"), "yaml: "))
}

func checkNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return invalidf("anchors and aliases are not allowed (line %d)", n.Line)
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i]; k.Kind == yaml.ScalarNode && forbidden[strings.ToLower(k.Value)] {
				return invalidf("the member %s is not allowed: a document holds no secret (line %d)", k.Value, k.Line)
			}
		}
	}
	for _, c := range n.Content {
		if err := checkNode(c); err != nil {
			return err
		}
	}
	return nil
}

var (
	reSubjectSA  = regexp.MustCompile(`^service_account: ?(.+)$`)
	reSubjectKey = regexp.MustCompile(`^key: ?(t[a-z2-7]{16})$`)
)

// subjectOf splits a subject of a limit into its kind and reference.
func subjectOf(s string) (kind, ref string, ok bool) {
	switch {
	case s == "tenant":
		return "tenant", "", true
	case reSubjectSA.MatchString(s):
		return "service_account", reSubjectSA.FindStringSubmatch(s)[1], true
	case reSubjectKey.MatchString(s):
		return "key", reSubjectKey.FindStringSubmatch(s)[1], true
	}
	return "", "", false
}

// Validate checks a document against what it is applied to: version, kind, the sections of the
// kind, natural keys that occur once, and the form of the values the plan compares.
func (d *Document) Validate(target Target) error {
	switch {
	case d.Pandokos == "":
		return invalidf("the member pandokos is missing: write pandokos: %s", Version)
	case d.Pandokos != Version:
		return invalidf("the version %q is not supported: %s is the only one", d.Pandokos, Version)
	case d.Kind == "":
		return invalidf("the member kind is missing: platform or tenant")
	case d.Kind != KindPlatform && d.Kind != KindTenant:
		return invalidf("the kind %q is not platform or tenant", d.Kind)
	case target.Platform && d.Kind != KindPlatform:
		return invalidf("the document is of kind %s, and --platform needs a document of kind platform", d.Kind)
	case !target.Platform && d.Kind != KindTenant:
		return invalidf("the document is of kind %s, and --tenant needs a document of kind tenant", d.Kind)
	}
	// The sections of sprint 12.3 are parsed and typed; planning them is part C1 and C2.
	for name, present := range map[string]bool{"platform_issuers": len(d.PlatformIssuers) > 0, "tenants": len(d.Tenants) > 0,
		"token_issuers": len(d.TokenIssuers) > 0, "subject_mappings": len(d.SubjectMappings) > 0, "event_destinations": len(d.EventDestinations) > 0} {
		if present {
			return invalidf("the section %s is not supported yet", name)
		}
	}
	for _, l := range append(slices.Clone(d.Limits), d.PlatformLimits...) {
		if l.Mapping != nil {
			return invalidf("a limit on a subject mapping is not supported yet")
		}
	}
	if d.Kind == KindPlatform {
		for name, present := range map[string]bool{"settings": d.Settings != nil, "model_restriction": d.ModelRestriction != nil, "roles": len(d.Roles) > 0,
			"service_accounts": len(d.ServiceAccounts) > 0, "limits": len(d.Limits) > 0, "price_lists": len(d.PriceLists) > 0} {
			if present {
				return invalidf("the section %s belongs to a document of kind tenant", name)
			}
		}
	} else {
		for name, present := range map[string]bool{"providers": len(d.Providers) > 0, "models": len(d.Models) > 0, "model_grants": len(d.ModelGrants) > 0,
			"price_overrides": len(d.PriceOverrides) > 0, "platform_limits": len(d.PlatformLimits) > 0} {
			if present {
				return invalidf("the section %s belongs to a document of kind platform", name)
			}
		}
	}
	seen := map[string]bool{}
	once := func(section, key string) error {
		k := section + "\x00" + key
		if seen[k] {
			return invalidf("%s %q occurs twice", section, key)
		}
		seen[k] = true
		return nil
	}
	instant := func(what, v string) error {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return invalidf("%s: valid_from must be an RFC 3339 instant", what)
		}
		return nil
	}
	for _, p := range d.Providers {
		if p.Name == "" {
			return invalidf("a provider has no name")
		}
		if err := once("provider", p.Name); err != nil {
			return err
		}
		for _, e := range p.Endpoints {
			if e.Name == "" {
				return invalidf("an endpoint of provider %s has no name", p.Name)
			}
			if err := once("endpoint", p.Name+"/"+e.Name); err != nil {
				return err
			}
		}
	}
	for _, m := range d.Models {
		if m.Provider == "" || m.Name == "" {
			return invalidf("a model needs provider and name")
		}
		if err := once("model", m.Provider+"/"+m.Name); err != nil {
			return err
		}
	}
	for _, g := range d.ModelGrants {
		if err := once("model grant", g.Provider+"/"+g.Model+" "+g.Tenant); err != nil {
			return err
		}
	}
	for _, v := range d.PriceOverrides {
		if err := instant("price override "+v.Provider+"/"+v.Model, v.ValidFrom); err != nil {
			return err
		}
		if err := once("price override", v.Provider+"/"+v.Model+" "+v.ValidFrom); err != nil {
			return err
		}
	}
	for _, l := range d.PlatformLimits {
		if l.Metric == "" {
			return invalidf("a platform limit has no metric")
		}
		if err := once("platform limit", limitKey(l.tenantRef(), l)); err != nil {
			return err
		}
	}
	for _, r := range d.Roles {
		if r.Name == "" {
			return invalidf("a role has no name")
		}
		if err := once("role", r.Name); err != nil {
			return err
		}
	}
	for _, a := range d.ServiceAccounts {
		if a.Name == "" {
			return invalidf("a service account has no name")
		}
		if err := once("service account", a.Name); err != nil {
			return err
		}
		for _, v := range []string{a.ValidFrom, a.ValidUntil} {
			if v != "" {
				if _, err := time.Parse(time.RFC3339, v); err != nil {
					return invalidf("service account %s: valid_from and valid_until must be RFC 3339 instants", a.Name)
				}
			}
		}
	}
	for _, l := range d.Limits {
		if l.Metric == "" {
			return invalidf("a limit has no metric")
		}
		if _, _, ok := subjectOf(l.Subject); !ok {
			return invalidf("the subject %q of a limit is not tenant, service_account: NAME or key: KEY-ID", l.Subject)
		}
		if err := once("limit", limitKey(l.Subject, l)); err != nil {
			return err
		}
	}
	for _, pl := range d.PriceLists {
		if pl.Currency == "" {
			return invalidf("a price list has no currency")
		}
		if err := once("price list", pl.Currency); err != nil {
			return err
		}
		for _, v := range pl.Versions {
			if v.Provider == "" || v.Model == "" {
				return invalidf("a version of price list %s needs provider and model", pl.Currency)
			}
			if err := instant("price list "+pl.Currency+" "+v.Provider+"/"+v.Model, v.ValidFrom); err != nil {
				return err
			}
			if err := once("price version", pl.Currency+" "+v.Provider+"/"+v.Model+" "+v.ValidFrom); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l Limit) tenantRef() string {
	if l.Tenant == "" {
		return "default"
	}
	return l.Tenant
}

// limitKey is the natural key of a limit rule: its subject (or tenant), provider, model, metric and currency.
func limitKey(subject string, l Limit) string {
	return strings.Join([]string{subject, l.Provider, l.Model, l.Metric, l.Currency}, "|")
}
