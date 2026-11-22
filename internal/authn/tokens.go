package authn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// checkTokens validates the options of an authenticator that accepts KindToken: it needs a key
// source and the pool of the gateway role, or of the admin role when the administration listener
// accepts tokens (tokens.admin_listener).
func checkTokens(o Options) error {
	switch {
	case o.Tokens.Keys == nil:
		return errors.New("authn: tokens need Tokens.Keys")
	case o.DB.Role() != store.RoleGateway && o.DB.Role() != store.RoleAdmin:
		return errors.New("authn: tokens need the pool of the gateway or the admin role")
	}
	return nil
}

// principalEntry is what the principal cache holds for a subject: the row read in the tenant's
// session.
type principalEntry struct {
	row queries.GetTokenPrincipalRow
}

// token authenticates a credential of the shape of a JWT (section 5.1): the registration is
// selected by the unverified issuer and audiences, the token is validated against it, and only
// then the tenant, the subject mapping and the service account are read.
func (a *Authenticator) token(ctx context.Context, presented string) (authz.Principal, error) {
	alg, kid, iss, aud, err := peek(presented)
	if err != nil {
		return authz.Principal{}, err
	}
	reg, err := a.selectRegistration(ctx, iss, aud)
	if err != nil {
		return authz.Principal{}, err
	}
	v, err := a.validate(ctx, presented, alg, kid, reg)
	if err != nil {
		return authz.Principal{}, err
	}
	return a.tokenPrincipal(ctx, reg, v)
}

// selectionKey is the key of the registration cache for an issuer and a sorted list of audiences.
// It is a digest of what the token claims, not of the token: the claims are unverified and as long
// as the sender likes, and only their digest is kept.
func selectionKey(iss string, auds []string) string {
	h := sha256.New()
	h.Write([]byte(iss))
	for _, a := range auds {
		h.Write([]byte{0})
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// selectRegistration finds the one registration whose issuer is iss and whose audience is one of
// aud (section 5.3): none is unknown_issuer, more than one is ambiguous_audience; the tenant is
// never chosen among candidates.
func (a *Authenticator) selectRegistration(ctx context.Context, iss string, aud []string) (registration, error) {
	auds := slices.Clone(aud)
	slices.Sort(auds)
	auds = slices.Compact(auds)
	auds = slices.DeleteFunc(auds, func(s string) bool { return s == "" })
	// PostgreSQL text cannot hold NUL; such a value names no registration.
	if iss == "" || len(auds) == 0 || strings.ContainsRune(iss, 0) || slices.ContainsFunc(auds, func(s string) bool { return strings.ContainsRune(s, 0) }) {
		return registration{}, &Failure{Reason: "unknown_issuer"}
	}
	key := selectionKey(iss, auds)
	if reason, _, ok := a.regs.negative(key); ok {
		return registration{}, &Failure{Reason: reason}
	}
	if r, ok := a.regs.get(key); ok {
		return r, nil
	}
	start := a.now() // the lifetime of what is cached counts from here, before the reads
	var rows []queries.LookupIssuersRow
	err := a.db.Unscoped(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).LookupIssuers(ctx, queries.LookupIssuersParams{Issuer: iss, Audiences: auds})
		return err
	})
	switch {
	case err != nil:
		return registration{}, unavailable(err)
	case len(rows) == 0:
		a.regs.putNegativeAt(key, "unknown_issuer", "", start)
		return registration{}, &Failure{Reason: "unknown_issuer"}
	case len(rows) > 1:
		a.regs.putNegativeAt(key, "ambiguous_audience", "", start)
		return registration{}, &Failure{Reason: "ambiguous_audience"}
	}
	r, err := a.loadRegistration(ctx, rows[0])
	if err != nil {
		return registration{}, err
	}
	owner := ""
	if r.Scope == "tenant" {
		owner = r.TenantID.String()
	}
	a.regs.putAt(key, owner, r.Scope == "platform", r, start)
	return r, nil
}

// loadRegistration reads the row the directory pointed at: of a tenant-bound registration in the
// session of the tenant the directory names, of a platform-level one without a tenant.
func (a *Authenticator) loadRegistration(ctx context.Context, d queries.LookupIssuersRow) (registration, error) {
	gone := &Failure{Reason: "unknown_issuer"}
	var r registration
	var claims string
	switch d.Scope {
	case "tenant":
		tid, err := tenant.ParseID(d.TenantID)
		if err != nil {
			return registration{}, unavailable(err)
		}
		var row queries.GetTrustedIssuerForAuthRow
		err = a.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			var err error
			row, err = queries.New(tx).GetTrustedIssuerForAuth(ctx, d.IssuerID)
			return err
		})
		switch {
		case errors.Is(err, store.ErrNoRows):
			return registration{}, gone
		case err != nil:
			return registration{}, unavailable(err)
		}
		r = registration{Scope: "tenant", ID: row.ID, TenantID: tid, Issuer: row.Issuer, Audience: row.Audience, JWKSURI: row.JwksUri,
			Algorithms: row.Algorithms, SubjectClaim: row.SubjectClaim, Enabled: row.Enabled}
		claims = row.RequiredClaims
	case "platform":
		var row queries.GetPlatformIssuerForAuthRow
		err := a.db.Unscoped(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			row, err = queries.New(tx).GetPlatformIssuerForAuth(ctx, d.IssuerID)
			return err
		})
		switch {
		case errors.Is(err, store.ErrNoRows):
			return registration{}, gone
		case err != nil:
			return registration{}, unavailable(err)
		}
		r = registration{Scope: "platform", ID: row.ID, Issuer: row.Issuer, Audience: row.Audience, JWKSURI: row.JwksUri,
			Algorithms: row.Algorithms, SubjectClaim: row.SubjectClaim, TenantClaim: row.TenantClaim, Enabled: row.Enabled}
		claims = row.RequiredClaims
	default:
		return registration{}, unavailable(errors.New("unknown registration scope"))
	}
	if err := json.Unmarshal([]byte(claims), &r.RequiredClaims); err != nil {
		return registration{}, unavailable(err)
	}
	return r, nil
}

// tokenPrincipal runs checks 12 (the tenant of a platform-level registration), 13 and 14 and builds
// the principal of section 5.7. Every failure from here on names the registration, and the subject.
func (a *Authenticator) tokenPrincipal(ctx context.Context, r registration, v verified) (authz.Principal, error) {
	fail := func(reason string, tid tenant.ID) error {
		return &Failure{Reason: reason, IssuerID: r.ID, IssuerScope: r.Scope, TenantID: tid, Subject: v.Subject}
	}
	platform := r.Scope == "platform"
	key := r.ID + "\x00" + v.TenantClaimValue + "\x00" + v.Subject
	if reason, detail, ok := a.principals.negative(key); ok {
		tid, _ := tenant.ParseID(detail)
		return authz.Principal{}, fail(reason, tid)
	}
	var row queries.GetTokenPrincipalRow
	if e, ok := a.principals.get(key); ok {
		row = e.row
	} else {
		start := a.now() // the lifetime of what is cached counts from here, before the reads
		tid := r.TenantID
		if platform {
			var text string
			err := a.db.Unscoped(ctx, func(ctx context.Context, tx store.Tx) error {
				var err error
				text, err = queries.New(tx).LookupPlatformIssuerTenant(ctx, queries.LookupPlatformIssuerTenantParams{IssuerID: r.ID, ClaimValue: v.TenantClaimValue})
				return err
			})
			if err != nil {
				return authz.Principal{}, unavailable(err)
			}
			var perr error
			if tid, perr = tenant.ParseID(text); text == "" || perr != nil {
				a.principals.putNegativeAt(key, "tenant_not_listed", "", start)
				return authz.Principal{}, fail("tenant_not_listed", tenant.ID{})
			}
		}
		err := a.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			var err error
			row, err = queries.New(tx).GetTokenPrincipal(ctx, queries.GetTokenPrincipalParams{
				Subject: v.Subject, Scope: r.Scope, IssuerID: r.ID, ClaimValue: v.TenantClaimValue})
			return err
		})
		switch {
		case errors.Is(err, store.ErrNoRows):
			a.principals.putNegativeAt(key, "subject_not_mapped", tid.String(), start)
			return authz.Principal{}, fail("subject_not_mapped", tid)
		case err != nil:
			return authz.Principal{}, unavailable(err)
		}
		a.principals.putAt(key, row.TenantID, platform, principalEntry{row: row}, start)
	}
	tid, err := tenant.ParseID(row.TenantID)
	if err != nil {
		return authz.Principal{}, unavailable(err)
	}
	if reason := serviceAccountRefusal(row.ServiceAccountState, row.ValidFrom, row.ValidUntil, a.now()); reason != "" {
		return authz.Principal{}, fail(reason, tid)
	}
	return authz.Principal{
		Kind: authz.KindServiceAccount, ID: row.ServiceAccountID, Name: row.ServiceAccountName, KeyID: row.MappingID,
		IssuerID: r.ID, Subject: v.Subject,
		TenantID: tid, TenantName: row.TenantName, TenantState: tenant.State(row.TenantState),
		Roles: append([]string{}, row.Roles...),
	}, nil
}
