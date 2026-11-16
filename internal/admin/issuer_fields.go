package admin

import (
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/store"
)

// The members that registrations of both kinds share, with the rules of section 5.3 that the
// document of the API cannot say: the shape of the URLs, the names of claims, control characters.

var (
	claimNameRE = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,64}$`)

	// reservedClaims are checked by their own rules of section 5.4; none can name the subject, the
	// tenant or a required claim.
	reservedClaims = map[string]bool{"iss": true, "aud": true, "exp": true, "nbf": true, "iat": true}

	defaultAlgorithms = []string{"RS256", "ES256"}
)

const (
	maxIssuerBytes = 255
	maxJWKSBytes   = 2048
)

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// checkURL refuses a value that is not an https URL with a host, without user information and
// fragment (and without a query when query is false). A message never repeats the value.
func checkURL(field, v string, query bool, max int) error {
	msg := "must be an https URL with a host, without user information or fragment"
	if !query {
		msg = "must be an https URL with a host, without user information, query or fragment"
	}
	bad := len(v) > max || !strings.HasPrefix(v, "https://") || strings.ContainsAny(v, "@#") || (!query && strings.Contains(v, "?")) ||
		strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0
	if !bad {
		u, err := url.Parse(v)
		bad = err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != ""
	}
	if bad {
		return invalid(field, msg)
	}
	return nil
}

func checkClaimName(field, v string) error {
	if !claimNameRE.MatchString(v) || reservedClaims[v] {
		return invalid(field, "must be a claim name of 1 to 64 characters of letters, digits and _ . : / -, other than iss, aud, exp, nbf and iat")
	}
	return nil
}

func checkClaims(claims map[string]string) error {
	if len(claims) > 16 {
		return invalid("required_claims", "must have at most 16 members")
	}
	for name, value := range claims {
		if !claimNameRE.MatchString(name) || reservedClaims[name] {
			return invalid("required_claims", "must name claims of 1 to 64 characters of letters, digits and _ . : / -, other than iss, aud, exp, nbf and iat")
		}
		if hasControl(value) {
			return invalid("required_claims", "must not contain control characters in a claim value")
		}
		if value == "" || len([]rune(value)) > 255 {
			return invalid("required_claims", "must map each claim to a string of 1 to 255 characters")
		}
	}
	return nil
}

// issuerValues are the members of a registration that an operation can write; TenantClaim is for
// platform-level registrations only.
type issuerValues struct {
	Name, Description, Issuer, Audience, JwksURI, SubjectClaim, TenantClaim string
	Algorithms                                                              []string
	RequiredClaims                                                          map[string]string
	Enabled                                                                 bool
}

// issuerPatch is the members of an update that were sent; nil is not sent.
type issuerPatch struct {
	Name, Description, Issuer, Audience, JwksURI, SubjectClaim, TenantClaim *string
	Algorithms                                                              *[]string
	RequiredClaims                                                          *map[string]string
	Enabled                                                                 *bool
}

func some[T any](o adminapi.Optional[T]) *T {
	if !o.Set {
		return nil
	}
	v := o.Value
	return &v
}

func patchOfToken(b adminapi.TokenIssuerUpdate) issuerPatch {
	return issuerPatch{Name: some(b.Name), Description: some(b.Description), Issuer: some(b.Issuer), Audience: some(b.Audience), JwksURI: some(b.JwksUri),
		SubjectClaim: some(b.SubjectClaim), Algorithms: some(b.Algorithms), RequiredClaims: some(b.RequiredClaims), Enabled: some(b.Enabled)}
}

func patchOfPlatform(b adminapi.PlatformIssuerUpdate) issuerPatch {
	p := patchOfToken(adminapi.TokenIssuerUpdate{Name: b.Name, Description: b.Description, Issuer: b.Issuer, Audience: b.Audience, JwksUri: b.JwksUri,
		SubjectClaim: b.SubjectClaim, Algorithms: b.Algorithms, RequiredClaims: b.RequiredClaims, Enabled: b.Enabled})
	p.TenantClaim = some(b.TenantClaim)
	return p
}

// validate applies the rules to the members that are present. What the document of the API
// already enforces (lengths, patterns, the list of algorithms) is not repeated.
func (p issuerPatch) validate() error {
	if p.Description != nil && hasControl(*p.Description) {
		return invalid("description", "must not contain control characters")
	}
	if p.Issuer != nil {
		if err := checkURL("issuer", *p.Issuer, false, maxIssuerBytes); err != nil {
			return err
		}
	}
	if p.JwksURI != nil && *p.JwksURI != "" {
		if err := checkURL("jwks_uri", *p.JwksURI, true, maxJWKSBytes); err != nil {
			return err
		}
	}
	if p.Audience != nil && (hasControl(*p.Audience) || *p.Audience == "") {
		return invalid("audience", "must be 1 to 255 characters without control characters")
	}
	if p.SubjectClaim != nil {
		if err := checkClaimName("subject_claim", *p.SubjectClaim); err != nil {
			return err
		}
	}
	if p.TenantClaim != nil {
		if err := checkClaimName("tenant_claim", *p.TenantClaim); err != nil {
			return err
		}
	}
	if p.Algorithms != nil && len(*p.Algorithms) == 0 {
		return invalid("algorithms", "must have at least 1 items")
	}
	if p.RequiredClaims != nil {
		if err := checkClaims(*p.RequiredClaims); err != nil {
			return err
		}
	}
	return nil
}

// patchOfCreate is the creation of a registration as a full patch, with the defaults of
// section 5.3 for what was not sent.
func patchOfCreate(v issuerValues) issuerPatch {
	p := issuerPatch{Name: &v.Name, Description: &v.Description, Issuer: &v.Issuer, Audience: &v.Audience, JwksURI: &v.JwksURI,
		SubjectClaim: &v.SubjectClaim, Algorithms: &v.Algorithms, RequiredClaims: &v.RequiredClaims, Enabled: &v.Enabled}
	if v.TenantClaim != "" {
		p.TenantClaim = &v.TenantClaim
	}
	return p
}

func withDefaults(v issuerValues) issuerValues {
	if v.Algorithms == nil {
		v.Algorithms = append([]string{}, defaultAlgorithms...)
	}
	if v.SubjectClaim == "" {
		v.SubjectClaim = "sub"
	}
	if v.RequiredClaims == nil {
		v.RequiredClaims = map[string]string{}
	}
	return v
}

func tokenCreateValues(b adminapi.TokenIssuerCreate) issuerValues {
	return withDefaults(issuerValues{Name: b.Name, Description: b.Description, Issuer: b.Issuer, Audience: b.Audience, JwksURI: b.JwksUri,
		SubjectClaim: b.SubjectClaim, Algorithms: b.Algorithms, RequiredClaims: b.RequiredClaims, Enabled: b.Enabled})
}

func platformCreateValues(b adminapi.PlatformIssuerCreate) issuerValues {
	return withDefaults(issuerValues{Name: b.Name, Description: b.Description, Issuer: b.Issuer, Audience: b.Audience, JwksURI: b.JwksUri,
		SubjectClaim: b.SubjectClaim, TenantClaim: b.TenantClaim, Algorithms: b.Algorithms, RequiredClaims: b.RequiredClaims, Enabled: b.Enabled})
}

// createDetail is what the audit row of a creation holds: every member of the registration, none
// of which is secret.
func (v issuerValues) createDetail() map[string]any {
	d := map[string]any{"name": v.Name, "description": v.Description, "issuer": v.Issuer, "audience": v.Audience, "jwks_uri": v.JwksURI,
		"algorithms": v.Algorithms, "subject_claim": v.SubjectClaim, "required_claims": v.RequiredClaims, "enabled": v.Enabled}
	if v.TenantClaim != "" {
		d["tenant_claim"] = v.TenantClaim
	}
	return d
}

// diff keeps of the patch only the members whose value differs from cur and returns the audit
// detail of the change: the sorted names of the members and their new values. An empty detail is a
// patch that changes nothing.
func (p issuerPatch) diff(cur issuerValues) (issuerPatch, map[string]any) {
	var out issuerPatch
	detail := map[string]any{}
	var changed []string
	str := func(name string, want *string, have string, dst **string) {
		if want != nil && *want != have {
			*dst = want
			detail[name] = *want
			changed = append(changed, name)
		}
	}
	str("name", p.Name, cur.Name, &out.Name)
	str("description", p.Description, cur.Description, &out.Description)
	str("issuer", p.Issuer, cur.Issuer, &out.Issuer)
	str("audience", p.Audience, cur.Audience, &out.Audience)
	str("jwks_uri", p.JwksURI, cur.JwksURI, &out.JwksURI)
	str("subject_claim", p.SubjectClaim, cur.SubjectClaim, &out.SubjectClaim)
	str("tenant_claim", p.TenantClaim, cur.TenantClaim, &out.TenantClaim)
	if p.Algorithms != nil && !reflect.DeepEqual(*p.Algorithms, cur.Algorithms) {
		out.Algorithms = p.Algorithms
		detail["algorithms"] = *p.Algorithms
		changed = append(changed, "algorithms")
	}
	if p.RequiredClaims != nil && !sameClaims(*p.RequiredClaims, cur.RequiredClaims) {
		out.RequiredClaims = p.RequiredClaims
		detail["required_claims"] = *p.RequiredClaims
		changed = append(changed, "required_claims")
	}
	if p.Enabled != nil && *p.Enabled != cur.Enabled {
		out.Enabled = p.Enabled
		detail["enabled"] = *p.Enabled
		changed = append(changed, "enabled")
	}
	if len(changed) == 0 {
		return out, nil
	}
	sort.Strings(changed)
	detail["changed"] = changed
	return out, detail
}

func sameClaims(a, b map[string]string) bool {
	return (len(a) == 0 && len(b) == 0) || reflect.DeepEqual(a, b)
}

// claimsText is the JSON text of required claims as the statements take it.
func claimsText(m map[string]string) string {
	if m == nil {
		m = map[string]string{}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func claimsPtr(m *map[string]string) *string {
	if m == nil {
		return nil
	}
	s := claimsText(*m)
	return &s
}

// issuerConstraintFields are the checks of migration 0008 that a value can break although the
// rules above passed (the database counts bytes and control characters its own way, and bounds the
// stored size of the required claims); each is a 400 on its member.
var issuerConstraintFields = map[string]string{
	"issuer_format": "issuer", "audience_format": "audience", "jwks_uri_format": "jwks_uri", "algorithms_valid": "algorithms",
	"subject_claim_format": "subject_claim", "tenant_claim_format": "tenant_claim", "required_claims_object": "required_claims",
	"name_format": "name", "description_length": "description",
}

// mapIssuerError turns what the database refuses into the answers of section 5.12: a name, a pair
// of issuer and audience, a subject or a claim value that is taken is a conflict with no detail
// (what holds it is never said); a value that breaks a check is a 400 on its member; anything else
// is the caller's to report.
func mapIssuerError(err error) error {
	kind, name, ok := store.ConstraintViolation(err)
	if !ok {
		return err
	}
	switch kind {
	case "unique":
		switch name {
		case "trusted_issuer_name_key", "platform_issuer_name_key", "issuer_directory_pkey", "subject_mapping_trusted_key",
			"subject_mapping_platform_key", "platform_issuer_tenant_claim_value_key":
			return errConflict
		}
	case "check":
		for _, prefix := range []string{"trusted_issuer_", "platform_issuer_"} {
			if rest, found := strings.CutPrefix(name, prefix); found {
				if field, known := issuerConstraintFields[rest]; known {
					return invalid(field, "is not accepted")
				}
			}
		}
		switch name {
		case "platform_issuer_tenant_claim_value_format":
			return invalid("claim_value", "is not accepted")
		case "subject_mapping_subject_format":
			return invalid("subject", "is not accepted")
		case "subject_mapping_description_length":
			return invalid("description", "is not accepted")
		}
	}
	return err
}
