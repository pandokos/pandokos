package authn_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/testutil"
)

// failed asserts that err is a refusal with the reason and returns it.
func failed(t *testing.T, what string, err error, reason string) *authn.Failure {
	t.Helper()
	if err == nil {
		t.Errorf("%s: the token was accepted, want %s", what, reason)
		return &authn.Failure{}
	}
	f := failure(t, err)
	if f.Reason != reason {
		t.Errorf("%s: reason %q, want %q", what, f.Reason, reason)
	}
	return f
}

// TestTokenPrincipalTenantBound is T-580: a token of a tenant-bound registration yields the
// principal of the service account its subject is mapped to, with the tenant's state; every other
// case is a refusal that names the registration and, from the mapping on, the subject.
func TestTokenPrincipalTenantBound(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		r := newTokenRig(t, d, 0)
		f := newFixture(t, d, "bound") // a service account "bot" with the roles tenant-admin and inference
		const aud = "pandokos-bound-eu"
		issuerID := newTrustedIssuer(t, d, f.tenant, r.idp, aud)
		mappingID := mapSubject(t, d, f.tenant, issuerID, false, "workload-1", f.sa)

		t.Run("a mapped subject yields the principal of the service account", func(t *testing.T) {
			for _, alg := range []string{"ES256", "RS256"} {
				p, err := r.auth.Authenticate(ctx(t), r.token(t, alg, "workload-1", aud))
				if err != nil {
					t.Fatalf("%s: %v", alg, err)
				}
				if p.Kind != authz.KindServiceAccount || p.ID != f.sa || p.Name != "bot" || p.KeyID != mappingID || p.IssuerID != issuerID || p.Subject != "workload-1" ||
					p.TenantID != f.tenant || p.TenantName != "bound" || p.TenantState != tenant.StateActive || p.Bootstrap ||
					strings.Join(p.Roles, ",") != "inference,tenant-admin" {
					t.Errorf("%s: principal %+v", alg, p)
				}
				if p.Scopes != nil || p.Models != nil || p.CredentialID != "" {
					t.Errorf("%s: a token principal has no scopes, models or pin: %+v", alg, p)
				}
				// It decides as a service account does.
				if authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) != authz.Allow ||
					authz.Decide(p, authz.TokenIssuerRead, authz.Target{TenantID: p.TenantID}) != authz.Allow {
					t.Errorf("%s: the decisions of the principal differ from a key's", alg)
				}
			}
			// The audience as an array, with the registered one among others.
			if _, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "workload-1", []string{"x", aud})); err != nil {
				t.Errorf("aud as an array: %v", err)
			}
		})

		t.Run("an unmapped subject", func(t *testing.T) {
			_, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "stranger", aud))
			fl := failed(t, "an unmapped subject", err, "subject_not_mapped")
			d := fl.Detail()
			if d.IssuerID != issuerID || d.IssuerScope != "tenant" || d.TenantID != f.tenant.String() || d.Subject != "stranger" || d.KeyID != "" {
				t.Errorf("detail %+v", d)
			}
			// Another case, another subject: nothing is matched by prefix or case.
			for _, sub := range []string{"workload-", "workload-12", "Workload-1", "workload-1 ", " workload-1", "WORKLOAD-1"} {
				_, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", sub, aud))
				_ = failed(t, "subject "+sub, err, "subject_not_mapped")
			}
		})

		t.Run("a service account that is disabled, or not valid yet or any more", func(t *testing.T) {
			for name, c := range map[string]struct{ set, reason string }{
				"disabled":       {"state = 'disabled'", "service_account_disabled"},
				"not valid yet":  {"valid_from = now() + interval '1 hour'", "service_account_not_valid"},
				"not valid more": {"valid_until = now() - interval '1 hour'", "service_account_not_valid"},
			} {
				g := newFixture(t, d, "bound-"+strings.ReplaceAll(name, " ", "-"))
				iss := newTrustedIssuer(t, d, g.tenant, r.idp, "aud-"+name)
				mapSubject(t, d, g.tenant, iss, false, "w", g.sa)
				g.exec(t, "UPDATE pandokos.service_account SET "+c.set+" WHERE id = $1::uuid", g.sa)
				_, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", "aud-"+name))
				fl := failed(t, name, err, c.reason)
				if fl.Subject != "w" || fl.IssuerID != iss || fl.TenantID != g.tenant {
					t.Errorf("%s: failure %+v", name, fl)
				}
			}
		})

		t.Run("a disabled registration", func(t *testing.T) {
			g := newFixture(t, d, "bound-registration-off")
			iss := newTrustedIssuer(t, d, g.tenant, r.idp, "aud-registration-off")
			mapSubject(t, d, g.tenant, iss, false, "w", g.sa)
			if _, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", "aud-registration-off")); err != nil {
				t.Fatal(err)
			}
			g.exec(t, "UPDATE pandokos.trusted_issuer SET enabled = false WHERE id = $1::uuid", iss)
			_, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", "aud-registration-off"))
			fl := failed(t, "disabled", err, "issuer_disabled")
			if fl.IssuerID != iss || fl.TenantID != g.tenant {
				t.Errorf("failure %+v", fl)
			}
			g.exec(t, "UPDATE pandokos.trusted_issuer SET enabled = true WHERE id = $1::uuid", iss)
			if _, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", "aud-registration-off")); err != nil {
				t.Errorf("enabled again: %v", err)
			}
		})

		t.Run("a suspended tenant gives a principal that carries the state", func(t *testing.T) {
			g := newFixture(t, d, "bound-suspended")
			iss := newTrustedIssuer(t, d, g.tenant, r.idp, "aud-suspended")
			mapSubject(t, d, g.tenant, iss, false, "w", g.sa)
			g.exec(t, "UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid", g.tenant.String())
			p, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", "aud-suspended"))
			if err != nil || p.TenantState != tenant.StateSuspended {
				t.Fatalf("%+v %v", p, err)
			}
			if authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) != authz.DenyTenantState {
				t.Error("a suspended tenant is not refused by the decision")
			}
		})

		t.Run("a mapping is the registration's own: the same subject of another registration is another service account", func(t *testing.T) {
			g := newFixture(t, d, "bound-two")
			sa2 := newServiceAccount(t, d, g.tenant, "second", "inference")
			issA := newTrustedIssuer(t, d, g.tenant, r.idp, "aud-two-a")
			issB := newTrustedIssuer(t, d, g.tenant, r.idp, "aud-two-b")
			mapSubject(t, d, g.tenant, issA, false, "same", g.sa)
			mapSubject(t, d, g.tenant, issB, false, "same", sa2)
			pa, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "same", "aud-two-a"))
			pb, errB := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "same", "aud-two-b"))
			if err != nil || errB != nil || pa.ID != g.sa || pb.ID != sa2 || pa.IssuerID != issA || pb.IssuerID != issB {
				t.Errorf("%+v %v / %+v %v", pa, err, pb, errB)
			}
		})

		t.Run("a token with a registration's required claims and subject claim", func(t *testing.T) {
			g := newFixture(t, d, "bound-claims")
			iss := newTrustedIssuer(t, d, g.tenant, r.idp, "aud-claims")
			g.exec(t, `UPDATE pandokos.trusted_issuer SET subject_claim = 'azp', required_claims = '{"tier":"gold"}' WHERE id = $1::uuid`, iss)
			mapSubject(t, d, g.tenant, iss, false, "client-7", g.sa)
			good := r.idp.StandardAt(r.clk.now(), "ignored", "aud-claims", 10*time.Minute).With("azp", "client-7").With("tier", "gold")
			p, err := r.auth.Authenticate(ctx(t), r.idp.Sign("ES256", good))
			if err != nil || p.Subject != "client-7" {
				t.Fatalf("%+v %v", p, err)
			}
			_, err = r.auth.Authenticate(ctx(t), r.idp.Sign("ES256", good.With("tier", "silver")))
			_ = failed(t, "a required claim that differs", err, "required_claim_missing")
		})

		t.Run("a token that is altered after it was accepted is refused, with a cache warm", func(t *testing.T) {
			// T-573, the half that needs a database: the principal is cached by what the token says,
			// and a token that was altered is verified again anyway.
			w := newTokenRig(t, d, time.Minute)
			g := newFixture(t, d, "bound-altered")
			iss := newTrustedIssuer(t, d, g.tenant, w.idp, "aud-altered")
			mapSubject(t, d, g.tenant, iss, false, "w", g.sa)
			mapSubject(t, d, g.tenant, iss, false, "admin", newServiceAccount(t, d, g.tenant, "admin-bot", "tenant-admin"))
			tok := w.token(t, "ES256", "w", "aud-altered")
			for i := 0; i < 3; i++ {
				if _, err := w.auth.Authenticate(ctx(t), tok); err != nil {
					t.Fatalf("call %d: %v", i, err)
				}
			}
			parts := strings.Split(tok, ".")
			payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
			forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(payload), `"sub":"w"`, `"sub":"admin"`, 1))) + "." + parts[2]
			if forged == tok {
				t.Fatal("the forged token is the token")
			}
			_, err := w.auth.Authenticate(ctx(t), forged)
			_ = failed(t, "forged payload with a warm cache", err, "bad_signature")
			// The first character carries six bits of the signature, so changing it keeps the
			// segment canonical base64url: the token is refused for its signature, not its form.
			// The last characters do not do that: some of their bits must be zero, and a strict
			// decoder refuses the token as malformed (about one run in 256 with the former "AA"/"BB").
			sig := parts[2]
			flipped := map[bool]string{true: "B", false: "A"}[sig[0] == 'A'] + sig[1:]
			_, err = w.auth.Authenticate(ctx(t), parts[0]+"."+parts[1]+"."+flipped)
			_ = failed(t, "altered signature with a warm cache", err, "bad_signature")
			if _, err := w.auth.Authenticate(ctx(t), tok); err != nil {
				t.Errorf("the original token after the refusals: %v", err)
			}
		})

		t.Run("a token never reaches the database before its signature is verified", func(t *testing.T) {
			// A token that no registration signs: the answer does not depend on what the database
			// holds, and nothing of the token is in the log.
			other := fakeidp.Start(t, fakeidp.Options{})
			tok := other.Sign("ES256", other.Standard("w", aud, time.Minute).With("iss", r.idp.Issuer()))
			_, err := r.auth.Authenticate(ctx(t), tok)
			_ = failed(t, "a token of another issuer under this issuer's name", err, "unknown_signing_key")
			for _, seg := range strings.Split(tok, ".") {
				if len(seg) >= 16 && strings.Contains(r.logs.String(), seg) {
					t.Errorf("the log holds a segment of a refused token: %.30s", seg)
				}
			}
		})
	})
}

// TestTokenPrincipalPlatform is T-581: a platform-level registration finds the tenant of a token in
// the claim it names, through the list of the registration and by the listed value only.
func TestTokenPrincipalPlatform(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		r := newTokenRig(t, d, 0)
		a, b, c := newFixture(t, d, "plat-a"), newFixture(t, d, "plat-b"), newFixture(t, d, "plat-outside")
		const aud = "pandokos-shared"
		issuerID := newPlatformIssuer(t, d, r.idp, aud, "org", map[tenant.ID]string{a.tenant: "org-a", b.tenant: "org-b"})
		mapA := mapSubject(t, d, a.tenant, issuerID, true, "svc", a.sa)
		mapB := mapSubject(t, d, b.tenant, issuerID, true, "svc", b.sa)
		mapSubject(t, d, c.tenant, issuerID, true, "svc", c.sa) // a mapping of a tenant that is not in the list: inert
		withClaim := func(v any) string {
			return r.idp.Sign("ES256", r.idp.StandardAt(r.clk.now(), "svc", aud, 10*time.Minute).With("org", v))
		}

		t.Run("each tenant's claim value acts for that tenant", func(t *testing.T) {
			pa, err := r.auth.Authenticate(ctx(t), withClaim("org-a"))
			if err != nil || pa.TenantID != a.tenant || pa.ID != a.sa || pa.KeyID != mapA || pa.IssuerID != issuerID || pa.Subject != "svc" || pa.TenantName != "plat-a" {
				t.Errorf("A: %+v %v", pa, err)
			}
			pb, err := r.auth.Authenticate(ctx(t), withClaim("org-b"))
			if err != nil || pb.TenantID != b.tenant || pb.ID != b.sa || pb.KeyID != mapB {
				t.Errorf("B: %+v %v", pb, err)
			}
			if pa.Scopes != nil || pa.Models != nil || pa.CredentialID != "" {
				t.Errorf("a token principal has no scopes, models or pin: %+v", pa)
			}
		})

		t.Run("a value that does not designate a listed tenant is refused", func(t *testing.T) {
			for name, v := range map[string]string{
				"the ID of an existing tenant that is not in the list": c.tenant.String(),
				"an unknown value": "org-unknown",
				"the tenant's name instead of the listed value": "plat-a",
				"the tenant's ID instead of the listed value":   a.tenant.String(),
				"the listed value in another case":              "ORG-A",
				"the listed value with a space":                 "org-a ",
				"the listed value of another registration":      "org",
			} {
				_, err := r.auth.Authenticate(ctx(t), withClaim(v))
				fl := failed(t, name, err, "tenant_not_listed")
				d := fl.Detail()
				if d.TenantID != "" || d.IssuerScope != "platform" || d.IssuerID != issuerID || d.Subject != "svc" {
					t.Errorf("%s: detail %+v (the tenant is not known: none is named)", name, d)
				}
			}
		})

		t.Run("an empty, numeric or array claim, or none, is refused", func(t *testing.T) {
			for name, v := range map[string]any{"empty": "", "a number": 7, "an array": []string{"org-a"}, "an object": map[string]any{"id": "org-a"}, "null": nil, "a boolean": true} {
				_, err := r.auth.Authenticate(ctx(t), withClaim(v))
				_ = failed(t, name, err, "tenant_claim_missing")
			}
			tok := r.idp.Sign("ES256", r.idp.StandardAt(r.clk.now(), "svc", aud, 10*time.Minute))
			_, err := r.auth.Authenticate(ctx(t), tok)
			_ = failed(t, "a missing claim", err, "tenant_claim_missing")
		})

		t.Run("a tenant in the list without a mapping of the subject is refused", func(t *testing.T) {
			e := newFixture(t, d, "plat-nomap")
			exec(t, d, `INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, $2::uuid, 'org-e')`, issuerID, e.tenant.String())
			_, err := r.auth.Authenticate(ctx(t), withClaim("org-e"))
			fl := failed(t, "no mapping", err, "subject_not_mapped")
			if fl.Detail().TenantID != e.tenant.String() || fl.Subject != "svc" || fl.IssuerScope != "platform" {
				t.Errorf("failure %+v", fl)
			}
		})

		t.Run("a mapping of a tenant that is not in the list is inert", func(t *testing.T) {
			// C maps the subject but is not listed: no claim value reaches it.
			for _, v := range []string{c.tenant.String(), "plat-outside", "org-c", ""} {
				p, err := r.auth.Authenticate(ctx(t), withClaim(v))
				if err == nil {
					t.Errorf("claim %q acted for %+v", v, p)
				}
			}
		})

		t.Run("nothing but the claim selects the tenant", func(t *testing.T) {
			// Other claims that name a tenant, and a subject mapped only in the other tenant.
			tok := r.idp.Sign("ES256", r.idp.StandardAt(r.clk.now(), "svc", aud, 10*time.Minute).With("org", "org-a").
				With("tenant", b.tenant.String()).With("tenant_id", b.tenant.String()).With("tid", "org-b").With("org_id", "org-b"))
			p, err := r.auth.Authenticate(ctx(t), tok)
			if err != nil || p.TenantID != a.tenant {
				t.Errorf("a token that names B in other claims: %+v %v", p, err)
			}
			onlyB := mapSubject(t, d, b.tenant, issuerID, true, "only-b", b.sa)
			_ = onlyB
			tok = r.idp.Sign("ES256", r.idp.StandardAt(r.clk.now(), "only-b", aud, 10*time.Minute).With("org", "org-a"))
			_, err = r.auth.Authenticate(ctx(t), tok)
			_ = failed(t, "a subject that is mapped in B only, with A's value", err, "subject_not_mapped")
		})

		t.Run("a disabled registration, a tenant removed from the list", func(t *testing.T) {
			g := newFixture(t, d, "plat-lifecycle")
			iss := newPlatformIssuer(t, d, r.idp, "aud-lifecycle", "org", map[tenant.ID]string{g.tenant: "org-g"})
			mapSubject(t, d, g.tenant, iss, true, "svc", g.sa)
			tok := r.idp.Sign("ES256", r.idp.StandardAt(r.clk.now(), "svc", "aud-lifecycle", 10*time.Minute).With("org", "org-g"))
			if _, err := r.auth.Authenticate(ctx(t), tok); err != nil {
				t.Fatal(err)
			}
			exec(t, d, "UPDATE pandokos.platform_issuer SET enabled = false WHERE id = $1::uuid", iss)
			_, err := r.auth.Authenticate(ctx(t), tok)
			_ = failed(t, "a disabled platform-level registration", err, "issuer_disabled")
			exec(t, d, "UPDATE pandokos.platform_issuer SET enabled = true WHERE id = $1::uuid", iss)
			if _, err := r.auth.Authenticate(ctx(t), tok); err != nil {
				t.Fatal(err)
			}
			exec(t, d, "DELETE FROM pandokos.platform_issuer_tenant WHERE issuer_id = $1::uuid AND tenant_id = $2::uuid", iss, g.tenant.String())
			_, err = r.auth.Authenticate(ctx(t), tok)
			_ = failed(t, "a tenant removed from the list", err, "tenant_not_listed")
			// The mapping stays and is inert; adding the tenant again revives it.
			exec(t, d, `INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, $2::uuid, 'org-g')`, iss, g.tenant.String())
			if _, err := r.auth.Authenticate(ctx(t), tok); err != nil {
				t.Errorf("a tenant added to the list again: %v", err)
			}
			// A suspended tenant gives a principal with that state.
			exec(t, d, "UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid", g.tenant.String())
			p, err := r.auth.Authenticate(ctx(t), tok)
			if err != nil || p.TenantState != tenant.StateSuspended {
				t.Errorf("a suspended tenant: %+v %v", p, err)
			}
		})
	})
}

// TestIssuerSelection is T-582: the pair of issuer and audience selects the registration, and so
// the tenant; a token that matches two is refused; an unknown pair is remembered for five seconds.
func TestIssuerSelection(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		r := newTokenRig(t, d, 30*time.Second)
		a, b := newFixture(t, d, "sel-a"), newFixture(t, d, "sel-b")
		issA := newTrustedIssuer(t, d, a.tenant, r.idp, "pandokos-sel-a")
		issB := newTrustedIssuer(t, d, b.tenant, r.idp, "pandokos-sel-b")
		mapSubject(t, d, a.tenant, issA, false, "w", a.sa)
		mapSubject(t, d, b.tenant, issB, false, "w", b.sa)

		t.Run("each token reaches its own tenant only", func(t *testing.T) {
			pa, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", "pandokos-sel-a"))
			if err != nil || pa.TenantID != a.tenant || pa.IssuerID != issA {
				t.Errorf("A: %+v %v", pa, err)
			}
			pb, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", "pandokos-sel-b"))
			if err != nil || pb.TenantID != b.tenant || pb.IssuerID != issB {
				t.Errorf("B: %+v %v", pb, err)
			}
			// A token for A's audience with a subject that only B maps is refused: A's session finds nothing.
			mapSubject(t, d, b.tenant, issB, false, "only-b", b.sa)
			_, err = r.auth.Authenticate(ctx(t), r.token(t, "ES256", "only-b", "pandokos-sel-a"))
			_ = failed(t, "B's subject under A's audience", err, "subject_not_mapped")
		})

		t.Run("a token with both audiences is ambiguous", func(t *testing.T) {
			_, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", []string{"pandokos-sel-a", "pandokos-sel-b"}))
			fl := failed(t, "both audiences", err, "ambiguous_audience")
			if fl.Detail().TenantID != "" || fl.Detail().IssuerID != "" || fl.Subject != "" {
				t.Errorf("the failure names a tenant, a registration or a subject among candidates: %+v", fl.Detail())
			}
			// With one registered audience among others that are not registered, the one is chosen.
			p, err := r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", []string{"nobody", "pandokos-sel-a", "nobody-else"}))
			if err != nil || p.TenantID != a.tenant {
				t.Errorf("one known audience among unknown ones: %+v %v", p, err)
			}
			// Across the two kinds: a platform-level registration and a tenant-bound one of the same issuer.
			newPlatformIssuer(t, d, r.idp, "pandokos-sel-platform", "org", map[tenant.ID]string{a.tenant: "org-a"})
			_, err = r.auth.Authenticate(ctx(t), r.token(t, "ES256", "w", []string{"pandokos-sel-a", "pandokos-sel-platform"}))
			_ = failed(t, "a tenant-bound and a platform-level audience", err, "ambiguous_audience")
		})

		t.Run("an unknown pair is refused and remembered for five seconds", func(t *testing.T) {
			u := newTokenRig(t, d, 30*time.Second)
			g := newFixture(t, d, "sel-late")
			tok := func() string { return u.token(t, "ES256", "w", "pandokos-sel-late") }
			_, err := u.auth.Authenticate(ctx(t), tok())
			fl := failed(t, "an unknown pair", err, "unknown_issuer")
			if d := fl.Detail(); d.TenantID != "" || d.IssuerID != "" || d.KeyID != "" {
				t.Errorf("an unknown issuer names %+v", d)
			}
			// The pair is registered now, without a notification reaching this authenticator.
			iss := newTrustedIssuer(t, d, g.tenant, u.idp, "pandokos-sel-late")
			mapSubject(t, d, g.tenant, iss, false, "w", g.sa)
			u.clk.advance(4*time.Second + 900*time.Millisecond)
			_, err = u.auth.Authenticate(ctx(t), tok())
			_ = failed(t, "within the five seconds", err, "unknown_issuer")
			u.clk.advance(200 * time.Millisecond)
			if p, err := u.auth.Authenticate(ctx(t), tok()); err != nil || p.TenantID != g.tenant {
				t.Errorf("after the five seconds: %+v %v", p, err)
			}
			// A token of an issuer nobody registered, and one whose audience differs by case.
			other := fakeidp.Start(t, fakeidp.Options{})
			u2 := newTokenRig(t, d, 0, other)
			_, err = u2.auth.Authenticate(ctx(t), other.Sign("ES256", other.Standard("w", "pandokos-sel-late", time.Minute)))
			_ = failed(t, "an issuer that is not registered", err, "unknown_issuer")
			_, err = u2.auth.Authenticate(ctx(t), u2.token(t, "ES256", "w", "PANDOKOS-SEL-LATE"))
			_ = failed(t, "an audience in another case", err, "unknown_issuer")
		})

		t.Run("the answer for an unknown pair is the same for every reason that is not known to the caller", func(t *testing.T) {
			// A token with a bad signature for an unknown pair is unknown_issuer, not bad_signature:
			// the signature is not looked at before a registration is selected.
			other := fakeidp.Start(t, fakeidp.Options{})
			_, err := r.auth.Authenticate(ctx(t), other.Sign("ES256", other.Standard("w", "no-such-audience", time.Minute)))
			_ = failed(t, "an unknown pair, a foreign key", err, "unknown_issuer")
		})
	})
}

// TestTokenCacheInvalidation is T-583: with a lifetime of one minute, every change that matters
// reaches the next call after the notification that its trigger sends, the right entries are
// dropped, and without a notification the lifetime bounds it.
func TestTokenCacheInvalidation(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	logs := &testutil.LogBuffer{}
	r := newTokenRig(t, d, time.Minute)

	// A listener on the direct connection, as the gateway runs it: the payloads are captured, and
	// each is applied to the authenticator.
	var mu = make(chan string, 64)
	l, err := store.NewListener(store.ListenerOptions{
		Conn: d.ConnOptions("gateway"), Role: store.RoleGateway, Logger: newLogger(logs),
		OnNotify: func(p string) {
			select {
			case mu <- p:
			default:
			}
			r.auth.Invalidate(p)
		},
		OnReset: r.auth.Flush,
	})
	if err != nil {
		t.Fatal(err)
	}
	lctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(lctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	testutil.Eventually(t, 10*time.Second, "the cache listener to connect", func() bool {
		return strings.Contains(logs.String(), `"msg":"cache listener connected"`)
	})
	drain := func() {
		for {
			select {
			case <-mu:
			default:
				return
			}
		}
	}
	// payloads waits for the payloads of a change and returns them.
	payloadsOf := func(t *testing.T, change func()) []string {
		t.Helper()
		drain()
		change()
		var got []string
		deadline := time.After(3 * time.Second)
		for {
			select {
			case p := <-mu:
				got = append(got, p)
				time.Sleep(50 * time.Millisecond) // the other notifications of the same change
				for len(mu) > 0 {
					got = append(got, <-mu)
				}
				return got
			case <-deadline:
				t.Fatal("no notification arrived")
			}
		}
	}
	// refused polls until the token is refused with the reason, within the 2 s of a notification.
	refusedWith := func(t *testing.T, what string, tok string, reason string) {
		t.Helper()
		testutil.Eventually(t, 2*time.Second, what, func() bool {
			_, err := r.auth.Authenticate(ctx(t), tok)
			var f *authn.Failure
			return err != nil && errors.As(err, &f) && f.Reason == reason
		})
	}
	mustWork := func(t *testing.T, what, tok string) authz.Principal {
		t.Helper()
		p, err := r.auth.Authenticate(ctx(t), tok)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return p
	}

	// A tenant with a registration, mappings and service accounts for each case.
	f := newFixture(t, d, "inval")
	mk := func(aud, sub string, roles ...string) (iss, sa, mapping, tok string) {
		iss = newTrustedIssuer(t, d, f.tenant, r.idp, aud)
		sa = newServiceAccount(t, d, f.tenant, "sa-"+aud, roles...)
		mapping = mapSubject(t, d, f.tenant, iss, false, sub, sa)
		return iss, sa, mapping, r.token(t, "ES256", sub, aud)
	}

	t.Run("a mapping that is deleted", func(t *testing.T) {
		_, _, mapping, tok := mk("aud-del-mapping", "w", "inference")
		mustWork(t, "warm", tok)
		got := payloadsOf(t, func() { exec(t, d, "DELETE FROM pandokos.subject_mapping WHERE id = $1::uuid", mapping) })
		if !contains(got, "tenant:"+f.tenant.String()) {
			t.Errorf("the notifications of a deleted mapping: %v", got)
		}
		refusedWith(t, "the deleted mapping to be refused", tok, "subject_not_mapped")
	})
	t.Run("a registration that is disabled", func(t *testing.T) {
		iss, _, _, tok := mk("aud-disable", "w", "inference")
		mustWork(t, "warm", tok)
		got := payloadsOf(t, func() { exec(t, d, "UPDATE pandokos.trusted_issuer SET enabled = false WHERE id = $1::uuid", iss) })
		if !contains(got, "tenant:"+f.tenant.String()) {
			t.Errorf("the notifications of a disabled registration: %v", got)
		}
		refusedWith(t, "the disabled registration to be refused", tok, "issuer_disabled")
		exec(t, d, "UPDATE pandokos.trusted_issuer SET enabled = true WHERE id = $1::uuid", iss)
		testutil.Eventually(t, 2*time.Second, "the enabled registration to work", func() bool {
			_, err := r.auth.Authenticate(ctx(t), tok)
			return err == nil
		})
	})
	t.Run("a registration whose audience changes", func(t *testing.T) {
		iss, _, _, tok := mk("aud-change", "w", "inference")
		mustWork(t, "warm", tok)
		exec(t, d, "UPDATE pandokos.trusted_issuer SET audience = 'aud-changed' WHERE id = $1::uuid", iss)
		refusedWith(t, "the old audience to be refused", tok, "unknown_issuer")
		mustWork(t, "the new audience", r.token(t, "ES256", "w", "aud-changed"))
	})
	t.Run("a service account that is disabled", func(t *testing.T) {
		_, sa, _, tok := mk("aud-sa-disable", "w", "inference")
		mustWork(t, "warm", tok)
		exec(t, d, "UPDATE pandokos.service_account SET state = 'disabled' WHERE id = $1::uuid", sa)
		refusedWith(t, "the disabled service account to be refused", tok, "service_account_disabled")
	})
	t.Run("a service account that loses a role", func(t *testing.T) {
		_, sa, _, tok := mk("aud-role", "w", "inference")
		p := mustWork(t, "warm", tok)
		if authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) != authz.Allow {
			t.Fatal("the role does not allow the call")
		}
		exec(t, d, "DELETE FROM pandokos.role_assignment WHERE service_account_id = $1::uuid", sa)
		testutil.Eventually(t, 2*time.Second, "the role to be gone", func() bool {
			p, err := r.auth.Authenticate(ctx(t), tok)
			return err == nil && authz.Decide(p, authz.ModelInvoke, authz.Target{TenantID: p.TenantID}) == authz.Deny
		})
	})
	t.Run("a tenant that is suspended", func(t *testing.T) {
		g := newFixture(t, d, "inval-suspend")
		iss := newTrustedIssuer(t, d, g.tenant, r.idp, "aud-suspend")
		mapSubject(t, d, g.tenant, iss, false, "w", g.sa)
		tok := r.token(t, "ES256", "w", "aud-suspend")
		mustWork(t, "warm", tok)
		exec(t, d, "UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid", g.tenant.String())
		testutil.Eventually(t, 2*time.Second, "the suspended tenant", func() bool {
			p, err := r.auth.Authenticate(ctx(t), tok)
			return err == nil && p.TenantState == tenant.StateSuspended
		})
	})

	t.Run("a tenant that leaves a platform-level list", func(t *testing.T) {
		g := newFixture(t, d, "inval-platform")
		iss := newPlatformIssuer(t, d, r.idp, "aud-platform-list", "org", map[tenant.ID]string{g.tenant: "org-g"})
		mapSubject(t, d, g.tenant, iss, true, "svc", g.sa)
		tok := r.idp.Sign("ES256", r.idp.StandardAt(r.clk.now(), "svc", "aud-platform-list", 10*time.Minute).With("org", "org-g"))
		mustWork(t, "warm", tok)
		got := payloadsOf(t, func() {
			exec(t, d, "DELETE FROM pandokos.platform_issuer_tenant WHERE issuer_id = $1::uuid", iss)
		})
		if !contains(got, "issuers") {
			t.Errorf("the notifications of a change of a platform-level list: %v", got)
		}
		refusedWith(t, "the removed tenant to be refused", tok, "tenant_not_listed")
	})

	t.Run("the payload issuers drops platform-level entries only", func(t *testing.T) {
		// Without a listener's help: the payloads are applied by hand, and the database changes
		// under the cache with the listener's effect undone by flushing first.
		w := newTokenRig(t, d, time.Minute)
		g := newFixture(t, d, "inval-issuers")
		issT := newTrustedIssuer(t, d, g.tenant, w.idp, "aud-i-tenant")
		saP := newServiceAccount(t, d, g.tenant, "platform-sa", "inference")
		issP := newPlatformIssuer(t, d, w.idp, "aud-i-platform", "org", map[tenant.ID]string{g.tenant: "org-g"})
		mT := mapSubject(t, d, g.tenant, issT, false, "w", g.sa)
		mP := mapSubject(t, d, g.tenant, issP, true, "w", saP)
		kv := w.auth // for readability
		tokT := w.token(t, "ES256", "w", "aud-i-tenant")
		tokP := w.idp.Sign("ES256", w.idp.StandardAt(w.clk.now(), "w", "aud-i-platform", 10*time.Minute).With("org", "org-g"))
		for _, tok := range []string{tokT, tokP} {
			if _, err := kv.Authenticate(ctx(t), tok); err != nil {
				t.Fatal(err)
			}
		}
		// A virtual key too: its principal must survive the payload.
		key, _ := g.key(t, nil, nil, "")
		if _, err := kv.Authenticate(ctx(t), key); err != nil {
			t.Fatal(err)
		}
		// The database moves under the cache: both mappings and the key are gone.
		exec(t, d, "DELETE FROM pandokos.subject_mapping WHERE id = ANY ($1::uuid[])", []string{mT, mP})
		exec(t, d, "UPDATE pandokos.virtual_key SET revoked_at = now() WHERE service_account_id = $1::uuid", g.sa)
		// Another tenant's payload drops nothing of this tenant.
		kv.Invalidate("tenant:" + newFixture(t, d, "inval-other").tenant.String())
		for name, tok := range map[string]string{"tenant-bound": tokT, "platform-level": tokP, "virtual key": key} {
			if _, err := kv.Authenticate(ctx(t), tok); err != nil {
				t.Errorf("%s: another tenant's payload dropped the entry: %v", name, err)
			}
		}
		kv.Invalidate("issuers")
		if _, err := kv.Authenticate(ctx(t), tokP); err == nil {
			t.Error("the payload issuers left a platform-level entry")
		} else {
			_ = failed(t, "platform-level after issuers", err, "subject_not_mapped")
		}
		if _, err := kv.Authenticate(ctx(t), tokT); err != nil {
			t.Errorf("the payload issuers dropped a tenant-bound entry: %v", err)
		}
		if _, err := kv.Authenticate(ctx(t), key); err != nil {
			t.Errorf("the payload issuers dropped a virtual key entry: %v", err)
		}
		// tenant:<id> drops the tenant's tokens and keys.
		kv.Invalidate("tenant:" + g.tenant.String())
		_ = failed(t, "tenant-bound after tenant:<id>", func() error { _, err := kv.Authenticate(ctx(t), tokT); return err }(), "subject_not_mapped")
		_ = failed(t, "key after tenant:<id>", func() error { _, err := kv.Authenticate(ctx(t), key); return err }(), "revoked")
		// Payloads of the other caches change nothing.
		for _, p := range []string{"vault:" + g.tenant.String(), "catalog", "catalog:" + g.tenant.String(), "limits", "limits:" + g.tenant.String(), "something-new"} {
			kv.Invalidate(p)
		}
	})

	t.Run("a reset flushes", func(t *testing.T) {
		w := newTokenRig(t, d, time.Minute)
		g := newFixture(t, d, "inval-reset")
		iss := newTrustedIssuer(t, d, g.tenant, w.idp, "aud-reset")
		m := mapSubject(t, d, g.tenant, iss, false, "w", g.sa)
		tok := w.token(t, "ES256", "w", "aud-reset")
		if _, err := w.auth.Authenticate(ctx(t), tok); err != nil {
			t.Fatal(err)
		}
		exec(t, d, "DELETE FROM pandokos.subject_mapping WHERE id = $1::uuid", m)
		if _, err := w.auth.Authenticate(ctx(t), tok); err != nil {
			t.Fatalf("the cached entry must still be accepted: %v", err)
		}
		w.auth.Flush()
		_, err := w.auth.Authenticate(ctx(t), tok)
		_ = failed(t, "after a flush", err, "subject_not_mapped")
	})

	t.Run("without notifications the lifetime bounds it", func(t *testing.T) {
		w := newTokenRig(t, d, time.Minute)
		g := newFixture(t, d, "inval-lifetime")
		iss := newTrustedIssuer(t, d, g.tenant, w.idp, "aud-lifetime")
		m := mapSubject(t, d, g.tenant, iss, false, "w", g.sa)
		fresh := func() string { return w.token(t, "ES256", "w", "aud-lifetime") }
		if _, err := w.auth.Authenticate(ctx(t), fresh()); err != nil {
			t.Fatal(err)
		}
		exec(t, d, "DELETE FROM pandokos.subject_mapping WHERE id = $1::uuid", m)
		w.clk.advance(time.Minute - time.Second)
		if _, err := w.auth.Authenticate(ctx(t), fresh()); err != nil {
			t.Fatalf("one second before the lifetime ends: %v", err)
		}
		w.clk.advance(time.Second) // a lifetime counts from the start of the lookup: exactly one minute
		_, err := w.auth.Authenticate(ctx(t), fresh())
		_ = failed(t, "at the end of the lifetime", err, "subject_not_mapped")
		// Use does not extend the lifetime; a negative entry lives five seconds.
		w.clk.advance(5 * time.Second)
		exec(t, d, "INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id) VALUES ($1::uuid, $2::uuid, 'w', $3::uuid)", g.tenant.String(), iss, g.sa)
		if _, err := w.auth.Authenticate(ctx(t), fresh()); err != nil {
			t.Errorf("five seconds after a refusal for an unmapped subject: %v", err)
		}
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
