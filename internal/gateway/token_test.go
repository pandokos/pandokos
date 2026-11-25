package gateway_test

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/testutil"
)

// switchKeys is a key source over a fake issuer that can be made to fail as an issuer that cannot
// be reached does.
type switchKeys struct {
	idp  *fakeidp.Server
	down atomic.Bool
}

func (k *switchKeys) Key(_ context.Context, _ jwks.Source, kid, alg string) (crypto.PublicKey, error) {
	if k.down.Load() {
		return nil, fmt.Errorf("%w: the issuer cannot be reached", authz.ErrUnavailable)
	}
	if kid == "" {
		return k.idp.PublicKey(alg), nil
	}
	if pub := k.idp.PublicKeyByID(kid); pub != nil {
		return pub, nil
	}
	return nil, jwks.ErrKeyNotFound
}

// tokenSetup is a migrated database with one tenant, its service account (role inference), a
// tenant-bound registration of a fake issuer and a mapping for the subject "workload", and a real
// authenticator that accepts virtual keys and tokens.
type tokenSetup struct {
	d         *dbtest.Database
	idp       *fakeidp.Server
	keys      *switchKeys
	tid       tenant.ID
	sa        string
	issuerID  string
	mappingID string
	audience  string
	auth      *authn.Authenticator
	logs      *testutil.LogBuffer
}

const workload = "workload"

func newTokenSetup(t *testing.T) *tokenSetup {
	t.Helper()
	s := &tokenSetup{d: dbtest.NewMigrated(t), idp: fakeidp.Start(t, fakeidp.Options{}), audience: "pandokos-gw-tokens", logs: &testutil.LogBuffer{}}
	s.keys = &switchKeys{idp: s.idp}
	s.tid = s.d.SeedTenant(t, "token-gateway")
	super := s.d.Super(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(super.QueryRow(ctx5(t), `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'bot') RETURNING id::text`, s.tid.String()).Scan(&s.sa))
	_, err := super.Exec(ctx5(t), `INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'inference')`, s.tid.String(), s.sa)
	must(err)
	must(super.QueryRow(ctx5(t), `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, 'idp', $2, $3) RETURNING id::text`, s.tid.String(), s.idp.Issuer(), s.audience).Scan(&s.issuerID))
	must(super.QueryRow(ctx5(t), `INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id) VALUES ($1::uuid, $2::uuid, $3, $4::uuid) RETURNING id::text`,
		s.tid.String(), s.issuerID, workload, s.sa).Scan(&s.mappingID))
	s.auth, err = authn.New(authn.Options{
		DB: s.d.Open(t, store.RoleGateway), Logger: telemetry.NewLogger(s.logs, "debug"), Kinds: []authn.Kind{authn.KindTenant, authn.KindToken},
		Tokens: authn.TokenOptions{Keys: s.keys, MaxBytes: 8192, ClockSkew: 60 * time.Second, MaxLifetime: 24 * time.Hour},
	})
	must(err)
	return s
}

// token signs a token for the registration; mutate may change the claims.
func (s *tokenSetup) token(alg, sub string, mutate ...func(fakeidp.Claims) fakeidp.Claims) string {
	c := s.idp.Standard(sub, s.audience, 10*time.Minute)
	for _, m := range mutate {
		c = m(c)
	}
	return s.idp.Sign(alg, c)
}

// gateway builds the gateway over both families with the authenticator of the setup.
func (s *tokenSetup) gateway(t *testing.T, mutate func(*gateway.Options)) *harness {
	t.Helper()
	return newFamilies(t, func(o *gateway.Options) {
		o.Authenticator = s.auth
		if mutate != nil {
			mutate(o)
		}
	})
}

func bearer(cred string) http.Header {
	return http.Header{"Authorization": {"Bearer " + cred}, "Content-Type": {"application/json"}}
}

func anthropicBearer(cred string) http.Header {
	return http.Header{"Authorization": {"Bearer " + cred}, "Anthropic-Version": {anthropicVersion}, "Content-Type": {"application/json"}}
}

func anthropicKeyHeader(cred string) http.Header {
	return http.Header{"X-Api-Key": {cred}, "Anthropic-Version": {anthropicVersion}, "Content-Type": {"application/json"}}
}

// TestTokenAuthentication is T-593: a token is accepted wherever a key is, on both families; every
// refusal is the answer to an unknown key; keys that cannot be obtained are a 503; the inbound
// credential is never forwarded.
func TestTokenAuthentication(t *testing.T) {
	t.Parallel()
	s := newTokenSetup(t)
	h := s.gateway(t, nil)
	tok := s.token("ES256", workload)

	t.Run("a token on the openai route and on the anthropic route is accepted", func(t *testing.T) {
		for name, c := range map[string]struct {
			path string
			body []byte
			hdr  http.Header
		}{
			"openai, bearer":           {route, chat("fake-basic", false), bearer(tok)},
			"openai, streaming":        {route, chat("fake-basic", true), bearer(tok)},
			"anthropic, bearer":        {anthropicRoute, messages("fake-basic", false), anthropicBearer(tok)},
			"anthropic, X-Api-Key":     {anthropicRoute, messages("fake-basic", false), anthropicKeyHeader(tok)},
			"anthropic, streaming key": {anthropicRoute, messages("fake-basic", true), anthropicKeyHeader(tok)},
		} {
			got := h.send(t, http.MethodPost, c.path, c.body, c.hdr)
			if got.status != 200 {
				t.Errorf("%s: status %d\n%s", name, got.status, got.body)
			}
		}
		if n := len(h.records(t, 5)); n != 5 {
			t.Errorf("%d usage records for 5 calls", n)
		}
		// A token with the audience as an array and another algorithm.
		other := s.token("RS256", workload, func(c fakeidp.Claims) fakeidp.Claims { return c.With("aud", []string{"nobody", s.audience}) })
		if got := h.send(t, http.MethodPost, route, chat("fake-basic", false), bearer(other)); got.status != 200 {
			t.Errorf("an RS256 token with aud as an array: %d\n%s", got.status, got.body)
		}
		// A virtual key is not affected: the same gateway still accepts none that it did not know.
		if got := h.send(t, http.MethodPost, route, chat("fake-basic", false), bearer(testutil.NewKey(t))); got.status != 401 {
			t.Errorf("an unknown key: %d", got.status)
		}
	})

	t.Run("the forwarded request holds no inbound credential", func(t *testing.T) {
		up := fakeprovider.New(t)
		fh := newFamiliesWith(t, up, testutil.TestGuard(t), func(o *gateway.Options) { o.Authenticator = s.auth })
		for name, c := range map[string]struct {
			path string
			body []byte
			hdr  http.Header
		}{
			"openai":               {route, chat(fakeprovider.ModelEchoAuth, false), bearer(tok)},
			"anthropic, bearer":    {anthropicRoute, messages(fakeprovider.ModelEchoAuth, false), anthropicBearer(tok)},
			"anthropic, X-Api-Key": {anthropicRoute, messages(fakeprovider.ModelEchoAuth, false), anthropicKeyHeader(tok)},
		} {
			before := len(up.Requests())
			if got := fh.send(t, http.MethodPost, c.path, c.body, c.hdr); got.status != 200 {
				t.Fatalf("%s: %d\n%s", name, got.status, got.body)
			}
			reqs := up.Requests()
			if len(reqs) != before+1 {
				t.Fatalf("%s: %d requests reached the provider", name, len(reqs)-before)
			}
			r := reqs[len(reqs)-1]
			for k, vs := range r.Header {
				for _, v := range vs {
					for _, seg := range strings.Split(tok, ".") {
						if strings.Contains(v, seg) {
							t.Errorf("%s: the header %s of the forwarded request holds a segment of the token", name, k)
						}
					}
				}
			}
			if strings.Contains(string(r.Body), tok) {
				t.Errorf("%s: the body of the forwarded request holds the token", name)
			}
			// The provider credential travels in the header of the provider's family.
			credHeader, credPrefix := "Authorization", "Bearer "
			if strings.HasPrefix(name, "anthropic") {
				credHeader, credPrefix = "X-Api-Key", ""
			}
			if got := r.Header.Get(credHeader); !strings.HasPrefix(got, credPrefix) || got == credPrefix {
				t.Errorf("%s: %s %q is not the provider credential", name, credHeader, got)
			}
		}
	})

	t.Run("every refusal is the answer to an unknown key, in the shape of the family", func(t *testing.T) {
		// A second registration with the same issuer for the ambiguous case, a disabled one, one that requires a claim.
		super := s.d.Super(t)
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := super.Exec(ctx5(t), sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		exec(`INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, 'second', $2, 'aud-second')`, s.tid.String(), s.idp.Issuer())
		exec(`INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience, enabled) VALUES ($1::uuid, 'off', $2, 'aud-off', false)`, s.tid.String(), s.idp.Issuer())
		exec(`INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience, required_claims) VALUES ($1::uuid, 'needs', $2, 'aud-needs', '{"tier":"gold"}')`, s.tid.String(), s.idp.Issuer())
		exec(`INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('shared', $1, 'aud-shared', 'org')`, s.idp.Issuer())
		var disabledSA string
		if err := super.QueryRow(ctx5(t), `INSERT INTO pandokos.service_account (tenant_id, name, state) VALUES ($1::uuid, 'frozen', 'disabled') RETURNING id::text`, s.tid.String()).Scan(&disabledSA); err != nil {
			t.Fatal(err)
		}
		var offIssuer string
		if err := super.QueryRow(ctx5(t), `SELECT id::text FROM pandokos.trusted_issuer WHERE name = 'idp'`).Scan(&offIssuer); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id) VALUES ($1::uuid, $2::uuid, 'frozen-subject', $3::uuid)`, s.tid.String(), offIssuer, disabledSA)

		with := func(aud any, f func(fakeidp.Claims) fakeidp.Claims) string {
			return s.idp.Sign("ES256", f(s.idp.Standard(workload, aud, 10*time.Minute)))
		}
		same := func(c fakeidp.Claims) fakeidp.Claims { return c }
		now := time.Now().Unix()
		other := fakeidp.Start(t, fakeidp.Options{})
		reasons := map[string]string{
			"malformed":                "eyJhbGciOiJFUzI1NiJ9.W10.c2ln",
			"algorithm_not_allowed":    s.idp.Raw(map[string]any{"alg": "none"}, s.idp.Standard(workload, s.audience, time.Minute), nil) + "c2ln",
			"unknown_issuer":           other.Sign("ES256", other.Standard(workload, "aud-unknown", time.Minute)),
			"ambiguous_audience":       with([]string{s.audience, "aud-second"}, same),
			"issuer_disabled":          with("aud-off", same),
			"unknown_signing_key":      other.Sign("ES256", other.Standard(workload, s.audience, time.Minute).With("iss", s.idp.Issuer())),
			"bad_signature":            s.idp.Raw(map[string]any{"alg": "ES256", "kid": s.idp.KeyID("ES256")}, s.idp.Standard(workload, s.audience, time.Minute), other.SignWith("ES256")),
			"expired":                  with(s.audience, func(c fakeidp.Claims) fakeidp.Claims { return c.With("iat", now-7200).With("exp", now-3600) }),
			"not_yet_valid":            with(s.audience, func(c fakeidp.Claims) fakeidp.Claims { return c.With("nbf", now+3600).With("exp", now+7200) }),
			"lifetime_too_long":        with(s.audience, func(c fakeidp.Claims) fakeidp.Claims { return c.With("iat", now).With("exp", now+3*86400) }),
			"required_claim_missing":   with("aud-needs", same),
			"subject_missing":          with(s.audience, func(c fakeidp.Claims) fakeidp.Claims { return c.Without("sub") }),
			"subject_not_mapped":       s.token("ES256", "stranger"),
			"service_account_disabled": s.token("ES256", "frozen-subject"),
			"tenant_not_listed":        with("aud-shared", func(c fakeidp.Claims) fakeidp.Claims { return c.With("org", "nobody") }),
			"tenant_claim_missing":     with("aud-shared", same),
		}
		type shape struct {
			name  string
			path  string
			body  []byte
			hdr   func(string) http.Header
			code  family.ErrorCode
			body2 func(family.ErrorCode) string
		}
		shapes := []shape{
			{"openai", route, chat("fake-basic", false), bearer, family.CodeUnauthenticated, errorBody},
			{"anthropic bearer", anthropicRoute, messages("fake-basic", false), anthropicBearer, family.CodeUnauthenticated, anthropicErrorBody},
			{"anthropic key", anthropicRoute, messages("fake-basic", false), anthropicKeyHeader, family.CodeUnauthenticated, anthropicErrorBody},
		}
		for _, sh := range shapes {
			baseline := h.send(t, http.MethodPost, sh.path, sh.body, sh.hdr(testutil.NewKey(t)))
			assertShape := func(what string, c captured) {
				t.Helper()
				if c.status != 401 || string(c.body) != sh.body2(sh.code) || c.header.Get("X-Pandokos-Error") != string(sh.code) {
					t.Errorf("%s, %s: status %d, body %s", sh.name, what, c.status, c.body)
				}
				if c.normalized() != baseline.normalized() {
					t.Errorf("%s, %s: the answer differs from the answer to an unknown key:\n%s\n%s", sh.name, what, c.normalized(), baseline.normalized())
				}
			}
			assertShape("the baseline", baseline)
			for reason, tok := range reasons {
				c := h.send(t, http.MethodPost, sh.path, sh.body, sh.hdr(tok))
				assertShape(reason, c)
				// The answer is the same for every reason; the log says which it was.
				if sh.name == "openai" {
					var logged any
					for _, l := range h.logs.Find(t, "authentication failed") {
						if l["request_id"] == c.header.Get("X-Pandokos-Request-Id") {
							logged = l["reason"]
						}
					}
					if logged != reason {
						t.Errorf("%s: the log says %v", reason, logged)
					}
				}
			}
			// A value that is neither a key nor a token.
			for _, junk := range []string{"not.a-token", "aaa.bbb", "pdk_nonsense", strings.Repeat("a", 9000) + "." + "b" + "." + "c"} {
				assertShape("junk "+junk[:min(len(junk), 12)], h.send(t, http.MethodPost, sh.path, sh.body, sh.hdr(junk)))
			}
		}
	})

	t.Run("keys that cannot be obtained are a 503 with Retry-After, never a 401", func(t *testing.T) {
		up := fakeprovider.New(t)
		fh := newFamiliesWith(t, up, testutil.TestGuard(t), func(o *gateway.Options) { o.Authenticator = s.auth })
		s.keys.down.Store(true)
		defer s.keys.down.Store(false)
		for name, c := range map[string]struct {
			path string
			body []byte
			hdr  http.Header
			want string
		}{
			"openai":    {route, chat("fake-basic", false), bearer(tok), errorBody(family.CodeUnavailable)},
			"anthropic": {anthropicRoute, messages("fake-basic", false), anthropicKeyHeader(tok), anthropicErrorBody(family.CodeUnavailable)},
		} {
			got := fh.send(t, http.MethodPost, c.path, c.body, c.hdr)
			if got.status != 503 || string(got.body) != c.want || got.header.Get("X-Pandokos-Error") != "unavailable" {
				t.Errorf("%s: status %d, body %s", name, got.status, got.body)
			}
			if got.header.Get("Retry-After") == "" {
				t.Errorf("%s: no Retry-After on a 503: the credential cannot be verified now, and the caller is told to come back", name)
			}
		}
		if n := len(up.Requests()); n != 0 {
			t.Errorf("%d requests were forwarded although the token could not be verified", n)
		}
		// A virtual key is not affected by the outage of an issuer.
		s.keys.down.Store(false)
	})
}

// TestTokenUsageRecord is T-595: the record of a call authenticated by a token says so, and the
// spans and the request line agree with it; a call made with a key still says service_account.
func TestTokenUsageRecord(t *testing.T) {
	t.Parallel()
	mapping, issuer := "0192f0f0-0000-7000-8000-0000000000c1", "0192f0f0-0000-7000-8000-0000000000a1"
	viaToken := testutil.InferencePrincipal()
	viaToken.KeyID, viaToken.IssuerID, viaToken.Subject = mapping, issuer, "spiffe://acme/ns/ci"
	viaToken.Scopes, viaToken.Models, viaToken.CredentialID = nil, nil, ""

	h := newHarness(t, func(o *gateway.Options) {
		o.Authenticator = testutil.StaticAuthenticator(map[string]authz.Principal{"a-token-value": viaToken, testKey: testPrincipal})
	})
	c := h.send(t, http.MethodPost, route, chat("fake-basic", false), bearer("a-token-value"))
	if c.status != 200 {
		t.Fatalf("status %d\n%s", c.status, c.body)
	}
	rec := h.oneRecord(t)
	if rec.PrincipalKind != "token" || rec.PrincipalID != viaToken.ID || rec.KeyID != mapping || rec.TenantID != viaToken.TenantID {
		t.Errorf("record: kind %q principal %q key %q tenant %s", rec.PrincipalKind, rec.PrincipalID, rec.KeyID, rec.TenantID)
	}
	if err := rec.Validate(); err != nil {
		t.Errorf("the record is not valid: %v", err)
	}
	if rec.Outcome != usage.OutcomeOK {
		t.Errorf("outcome %q", rec.Outcome)
	}

	// The request line.
	line := h.lastRequestLine(t)
	for _, k := range []string{"issuer_id", "service_account_id", "key_id", "tenant_id", "subject"} {
		if _, ok := line[k]; ok {
			t.Errorf("the request line holds %s (ADR 0055): %v", k, line)
		}
	}
	_, _ = issuer, mapping
	// The spans: the same set of attributes as for a key, with the same values.
	var server tracetest.SpanStub
	found := false
	for _, s := range h.spans.Ended() {
		if s.SpanKind().String() == "server" {
			server, found = tracetest.SpanStubFromReadOnlySpan(s), true
		}
	}
	if !found {
		t.Fatal("no server span")
	}
	got := map[string]string{}
	for _, kv := range server.Attributes {
		got[string(kv.Key)] = kv.Value.String()
	}
	for k := range got {
		if strings.Contains(k, "issuer") || strings.Contains(k, "subject") || strings.Contains(k, "token") {
			t.Errorf("the server span carries the attribute %s: spans carry the same values as for a key", k)
		}
	}
	if len(got) != len(serverAttrs) {
		t.Errorf("%d server attributes, want the %d of a key: %v", len(got), len(serverAttrs), got)
	}

	// Refusals of an authenticated token principal are recorded as tokens too.
	denied := viaToken
	denied.Roles = nil
	h2 := newHarness(t, func(o *gateway.Options) { o.Authenticator = fixedAuth{p: denied} })
	if c := h2.send(t, http.MethodPost, route, chat("fake-basic", false), bearer("x")); c.status != 403 {
		t.Fatalf("status %d", c.status)
	}
	if r := h2.oneRecord(t); r.PrincipalKind != "token" || r.KeyID != mapping || r.Outcome != usage.OutcomeRejectedPolicy {
		t.Errorf("a refused call of a token principal: kind %q key %q outcome %q", r.PrincipalKind, r.KeyID, r.Outcome)
	}

	// A key still says service_account, with its key ID, and the line has no issuer.
	h3 := newHarness(t, func(o *gateway.Options) {
		o.Authenticator = testutil.StaticAuthenticator(map[string]authz.Principal{testKey: testPrincipal})
	})
	if c := h3.send(t, http.MethodPost, route, chat("fake-basic", false), authed()); c.status != 200 {
		t.Fatalf("status %d", c.status)
	}
	if r := h3.oneRecord(t); r.PrincipalKind != "service_account" || r.KeyID != testPrincipal.KeyID || r.PrincipalID != testPrincipal.ID {
		t.Errorf("a key call: kind %q key %q", r.PrincipalKind, r.KeyID)
	}
	if l := h3.lastRequestLine(t); l["issuer_id"] != nil {
		t.Errorf("a key call has an issuer on its line: %v", l)
	}
}

// TestTokenFailureAudit is T-594 (the gateway half): which refusals of a token write a row in the
// audit log, with what, and how often.
func TestTokenFailureAudit(t *testing.T) {
	t.Parallel()
	s := newTokenSetup(t)
	gw := s.d.Open(t, store.RoleGateway)
	auditor := resolver.NewFailureAuditor(gw, telemetry.NewLogger(s.logs, "debug"), nil)
	h := s.gateway(t, func(o *gateway.Options) { o.Failures = auditor })
	super := s.d.Super(t)

	// More registrations: a platform-level one that lists the tenant (with a mapping for "svc"), and one that does not list it.
	var platform string
	if err := super.QueryRow(ctx5(t), `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('shared', $1, 'aud-shared', 'org') RETURNING id::text`, s.idp.Issuer()).Scan(&platform); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ('` + platform + `', '` + s.tid.String() + `', 'org-acme')`,
		`INSERT INTO pandokos.subject_mapping (tenant_id, platform_issuer_id, subject, service_account_id) VALUES ('` + s.tid.String() + `', '` + platform + `', 'svc', '` + s.sa + `')`,
		`INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience, enabled) VALUES ('` + s.tid.String() + `', 'off', '` + s.idp.Issuer() + `', 'aud-off', false)`,
	} {
		if _, err := super.Exec(ctx5(t), sql); err != nil {
			t.Fatal(err)
		}
	}

	type auditRow struct {
		Action, ResourceType, ResourceID, Outcome, ActorType, RequestID string
		Detail                                                          map[string]any
	}
	rows := func() []auditRow {
		t.Helper()
		rs, err := super.Query(ctx5(t), `SELECT action, resource_type, resource_id, outcome, actor_type, request_id, detail::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure' ORDER BY id`, s.tid.String())
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		var out []auditRow
		for rs.Next() {
			var r auditRow
			var detail string
			if err := rs.Scan(&r.Action, &r.ResourceType, &r.ResourceID, &r.Outcome, &r.ActorType, &r.RequestID, &detail); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(detail), &r.Detail); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	send := func(tok string) captured {
		t.Helper()
		c := h.send(t, http.MethodPost, route, chat("fake-basic", false), bearer(tok))
		if c.status != 401 {
			t.Fatalf("status %d, want 401\n%s", c.status, c.body)
		}
		return c
	}
	other := fakeidp.Start(t, fakeidp.Options{})
	forged := func(sub string) string {
		return s.idp.Raw(map[string]any{"alg": "ES256", "kid": s.idp.KeyID("ES256")}, s.idp.Standard(sub, s.audience, time.Minute), other.SignWith("ES256"))
	}
	authFailureLine := func(request string) map[string]any {
		t.Helper()
		for _, l := range s.logs.Find(t, "authentication failed") {
			if l["request_id"] == request {
				return l
			}
		}
		for _, l := range h.logs.Find(t, "authentication failed") {
			if l["request_id"] == request {
				return l
			}
		}
		t.Fatalf("no log line for request %s", request)
		return nil
	}

	t.Run("failures that name no tenant are logged, not audited", func(t *testing.T) {
		before := len(rows())
		for reason, tok := range map[string]string{
			"malformed":             "eyJhbGciOiJFUzI1NiJ9.W10.c2ln",
			"algorithm_not_allowed": s.idp.Raw(map[string]any{"alg": "none"}, s.idp.Standard(workload, s.audience, time.Minute), nil) + "c2ln",
			"unknown_issuer":        other.Sign("ES256", other.Standard(workload, "aud-unknown", time.Minute)),
		} {
			c := send(tok)
			l := authFailureLine(c.header.Get("X-Pandokos-Request-Id"))
			if l["reason"] != reason || l["listener"] != "inference" || l["tenant_id"] != nil || l["issuer_id"] != nil || l["subject"] != nil || l["level"] != "WARN" {
				t.Errorf("%s: log line %v", reason, l)
			}
		}
		if n := len(rows()); n != before {
			t.Errorf("%d rows were written for failures that name no tenant", n-before)
		}
	})

	t.Run("a failure on a tenant-bound registration is audited without a subject until the signature is verified", func(t *testing.T) {
		before := len(rows())
		c := send(forged(workload))
		rs := rows()
		if len(rs) != before+1 {
			t.Fatalf("%d new rows, want 1", len(rs)-before)
		}
		r := rs[len(rs)-1]
		if r.ActorType != "anonymous" || r.ResourceType != "token_issuer" || r.ResourceID != s.issuerID || r.Outcome != "failure" || r.RequestID != c.header.Get("X-Pandokos-Request-Id") ||
			r.Detail["reason"] != "bad_signature" || r.Detail["listener"] != "inference" || r.Detail["issuer_scope"] != "tenant" || r.Detail["subject"] != nil || len(r.Detail) != 3 {
			t.Errorf("row %+v", r)
		}
		l := authFailureLine(r.RequestID)
		if l["issuer_id"] != nil || l["tenant_id"] != nil || l["audited"] != true || l["subject"] != nil || l["reason"] != "bad_signature" {
			t.Errorf("log line %v", l)
		}
		// The registration that is disabled names its tenant too, before the signature is looked at.
		c = send(s.idp.Sign("ES256", s.idp.Standard(workload, "aud-off", time.Minute)))
		rs = rows()
		if last := rs[len(rs)-1]; last.Detail["reason"] != "issuer_disabled" || last.ResourceType != "token_issuer" || last.Detail["subject"] != nil {
			t.Errorf("a disabled registration: %+v", last)
		}
	})

	t.Run("a failure after the subject was verified is audited with it", func(t *testing.T) {
		before := len(rows())
		c := send(s.token("ES256", "stranger"))
		rs := rows()
		if len(rs) != before+1 {
			t.Fatalf("%d new rows, want 1", len(rs)-before)
		}
		r := rs[len(rs)-1]
		if r.ResourceID != s.issuerID || r.Detail["reason"] != "subject_not_mapped" || r.Detail["subject"] != "stranger" || r.Detail["issuer_scope"] != "tenant" || len(r.Detail) != 4 {
			t.Errorf("row %+v", r)
		}
		if l := authFailureLine(c.header.Get("X-Pandokos-Request-Id")); l["subject"] != nil || l["audited"] != true {
			t.Errorf("log line %v", l)
		}
		// A disabled service account: the same.
		var frozen string
		if err := super.QueryRow(ctx5(t), `INSERT INTO pandokos.service_account (tenant_id, name, state) VALUES ($1::uuid, 'frozen', 'disabled') RETURNING id::text`, s.tid.String()).Scan(&frozen); err != nil {
			t.Fatal(err)
		}
		if _, err := super.Exec(ctx5(t), `INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id) VALUES ($1::uuid, $2::uuid, 'frozen-subject', $3::uuid)`, s.tid.String(), s.issuerID, frozen); err != nil {
			t.Fatal(err)
		}
		send(s.token("ES256", "frozen-subject"))
		rs = rows()
		if last := rs[len(rs)-1]; last.Detail["reason"] != "service_account_disabled" || last.Detail["subject"] != "frozen-subject" {
			t.Errorf("a disabled service account: %+v", last)
		}
	})

	t.Run("a platform-level failure before the tenant is known is logged with the registration, not audited", func(t *testing.T) {
		before := len(rows())
		for reason, tok := range map[string]string{
			"bad_signature":        s.idp.Raw(map[string]any{"alg": "ES256", "kid": s.idp.KeyID("ES256")}, s.idp.Standard("svc", "aud-shared", time.Minute).With("org", "org-acme"), other.SignWith("ES256")),
			"tenant_not_listed":    s.idp.Sign("ES256", s.idp.Standard("svc", "aud-shared", time.Minute).With("org", "nobody")),
			"tenant_claim_missing": s.idp.Sign("ES256", s.idp.Standard("svc", "aud-shared", time.Minute)),
		} {
			c := send(tok)
			l := authFailureLine(c.header.Get("X-Pandokos-Request-Id"))
			// The line says why, never who (ADR 0055).
			if l["reason"] != reason || l["issuer_id"] != nil || l["tenant_id"] != nil || l["audited"] != nil || l["subject"] != nil {
				t.Errorf("%s: log line %v", reason, l)
			}
		}
		if n := len(rows()); n != before {
			t.Errorf("%d rows were written for platform-level failures before the tenant is known", n-before)
		}
		// Once the tenant is known (listed), a failure is audited in its log with the scope platform.
		c := send(s.idp.Sign("ES256", s.idp.Standard("not-mapped", "aud-shared", time.Minute).With("org", "org-acme")))
		rs := rows()
		if len(rs) != before+1 {
			t.Fatalf("%d new rows, want 1", len(rs)-before)
		}
		if r := rs[len(rs)-1]; r.ResourceID != platform || r.Detail["issuer_scope"] != "platform" || r.Detail["reason"] != "subject_not_mapped" || r.Detail["subject"] != "not-mapped" || r.RequestID != c.header.Get("X-Pandokos-Request-Id") {
			t.Errorf("row %+v", r)
		}
	})

	t.Run("100 forged tokens for one registration write one row per reason", func(t *testing.T) {
		pw := newTokenSetup(t)
		pwAuditor := resolver.NewFailureAuditor(pw.d.Open(t, store.RoleGateway), telemetry.NewLogger(pw.logs, "debug"), nil)
		ph := pw.gateway(t, func(o *gateway.Options) { o.Failures = pwAuditor })
		for i := 0; i < 100; i++ {
			tok := pw.idp.Raw(map[string]any{"alg": "ES256", "kid": pw.idp.KeyID("ES256")}, pw.idp.Standard(fmt.Sprintf("s%d", i), pw.audience, time.Minute), other.SignWith("ES256"))
			if c := ph.send(t, http.MethodPost, route, chat("fake-basic", false), bearer(tok)); c.status != 401 {
				t.Fatalf("status %d", c.status)
			}
		}
		var n int
		if err := pw.d.Super(t).QueryRow(ctx5(t), `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure'`, pw.tid.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows for 100 forged tokens of one registration, want 1", n)
		}
		// Another reason is another row, once.
		for i := 0; i < 5; i++ {
			ph.send(t, http.MethodPost, route, chat("fake-basic", false), bearer(pw.idp.Sign("ES256", pw.idp.Standard("w", pw.audience, time.Minute).With("exp", time.Now().Add(-time.Hour).Unix()).With("iat", time.Now().Add(-2*time.Hour).Unix()))))
		}
		if err := pw.d.Super(t).QueryRow(ctx5(t), `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure'`, pw.tid.String()).Scan(&n); err != nil || n != 2 {
			t.Errorf("%d rows after a second reason: %v", n, err)
		}
	})

	t.Run("two unmapped subjects write two rows", func(t *testing.T) {
		before := len(rows())
		send(s.token("ES256", "unmapped-one"))
		send(s.token("ES256", "unmapped-two"))
		send(s.token("ES256", "unmapped-one")) // a repeat: suppressed
		rs := rows()
		if len(rs) != before+2 {
			t.Fatalf("%d new rows, want 2", len(rs)-before)
		}
		if a, b := rs[len(rs)-2].Detail["subject"], rs[len(rs)-1].Detail["subject"]; a != "unmapped-one" || b != "unmapped-two" {
			t.Errorf("subjects %v %v", a, b)
		}
	})

	t.Run("nothing of a token is in a row or in the log", func(t *testing.T) {
		tok := forged("canary-subject")
		send(tok)
		text := fmt.Sprintf("%+v", rows()) + s.logs.String() + h.logs.String()
		for _, seg := range append(strings.Split(tok, "."), tok) {
			if len(seg) >= 16 && strings.Contains(text, seg) {
				t.Errorf("a segment of a refused token is in an audit row or in the log: %.30s", seg)
			}
		}
		if strings.Contains(text, "canary-subject") {
			t.Error("the unverified subject of a refused token is in an audit row or in the log")
		}
	})
}

// TestTokenCanary is the gateway half of T-597: for accepted and refused tokens at log level
// debug, no segment of a presented token and nothing of the key set of the issuer appears in what the
// process writes or sends: the log, a response, an audit row, a usage record, a span, the forwarded request.
func TestTokenCanary(t *testing.T) {
	t.Parallel()
	s := newTokenSetup(t)
	up := fakeprovider.New(t)
	logs := s.logs
	// The keys come from the fake issuer over HTTPS through the real cache, so that what the key
	// retrieval logs is part of what is searched.
	cache, err := jwks.New(jwks.Options{
		Transport: func(bool) http.RoundTripper { return s.idp.Transport() }, FetchTimeout: 2 * time.Second, RefreshInterval: 15 * time.Minute,
		MinRefetchInterval: 10 * time.Second, MaxStale: 6 * time.Hour, Logger: telemetry.NewLogger(logs, "debug"),
	})
	if err != nil {
		t.Fatal(err)
	}
	gwAuth, err := authn.New(authn.Options{
		DB: s.d.Open(t, store.RoleGateway), Logger: telemetry.NewLogger(logs, "debug"), Kinds: []authn.Kind{authn.KindTenant, authn.KindToken},
		Tokens: authn.TokenOptions{Keys: cache, MaxBytes: 8192, ClockSkew: time.Minute, MaxLifetime: 24 * time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	auditor := resolver.NewFailureAuditor(s.d.Open(t, store.RoleGateway), telemetry.NewLogger(logs, "debug"), nil)
	h := newFamiliesWith(t, up, testutil.TestGuard(t), func(o *gateway.Options) {
		o.Authenticator, o.Failures, o.Logger = gwAuth, auditor, telemetry.NewLogger(logs, "debug")
	})
	h.logs = logs
	other := fakeidp.Start(t, fakeidp.Options{})

	// The registration allows RS256 and ES256 (the default); EdDSA would be refused.
	good := []string{s.token("ES256", workload), s.token("RS256", workload), s.token("ES256", workload)}
	bad := []string{
		s.token("ES256", "stranger"),
		other.Sign("ES256", other.Standard(workload, s.audience, time.Minute).With("iss", s.idp.Issuer())),
		s.idp.Raw(map[string]any{"alg": "ES256", "kid": s.idp.KeyID("ES256")}, s.idp.Standard(workload, s.audience, time.Minute), other.SignWith("ES256")),
		s.idp.Raw(map[string]any{"alg": "none"}, s.idp.Standard(workload, s.audience, time.Minute), nil) + "c2lnbmF0dXJl",
		s.token("ES256", workload, func(c fakeidp.Claims) fakeidp.Claims {
			return c.With("exp", time.Now().Add(-time.Hour).Unix()).With("iat", time.Now().Add(-2*time.Hour).Unix())
		}),
		other.Sign("ES256", other.Standard(workload, "aud-nobody", time.Minute)),
	}
	var responses strings.Builder
	for i, tok := range append(append([]string(nil), good...), bad...) {
		var c captured
		switch i % 3 {
		case 0:
			c = h.send(t, http.MethodPost, route, chat("fake-basic", i%2 == 0), bearer(tok))
		case 1:
			c = h.send(t, http.MethodPost, anthropicRoute, messages("fake-basic", false), anthropicBearer(tok))
		default:
			c = h.send(t, http.MethodPost, anthropicRoute, messages("fake-basic", false), anthropicKeyHeader(tok))
		}
		responses.WriteString(c.dump())
	}
	h.records(t, len(good))

	// Everything the test captured.
	var all strings.Builder
	all.WriteString(logs.String())
	all.WriteString(responses.String())
	for _, r := range h.rec.Records() {
		all.WriteString(fmt.Sprintf("%+v", r))
	}
	for _, sp := range h.spans.Ended() {
		all.WriteString(sp.Name())
		for _, kv := range sp.Attributes() {
			all.WriteString(string(kv.Key) + "=" + kv.Value.String() + "\n")
		}
		for _, ev := range sp.Events() {
			all.WriteString(ev.Name)
			for _, kv := range ev.Attributes {
				all.WriteString(string(kv.Key) + "=" + kv.Value.String() + "\n")
			}
		}
		all.WriteString(sp.Status().Description)
	}
	for _, r := range up.Requests() {
		all.WriteString(fmt.Sprintf("%v %s", r.Header, r.Body))
	}
	rs, err := s.d.Super(t).Query(ctx5(t), `SELECT detail::text || action || resource_id || COALESCE(reason, '') FROM pandokos.audit_event`)
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var text string
		if err := rs.Scan(&text); err != nil {
			t.Fatal(err)
		}
		all.WriteString(text)
	}
	rs.Close()
	text := all.String()

	checked := 0
	for _, tok := range append(append([]string(nil), good...), bad...) {
		for _, seg := range append(strings.Split(tok, "."), tok) {
			if len(seg) < 16 {
				continue
			}
			checked++
			if strings.Contains(text, seg) {
				t.Errorf("a segment of a presented token is in the output: %.30s", seg)
			}
		}
	}
	if checked < 30 {
		t.Errorf("only %d segments were searched", checked)
	}
	if strings.Contains(text, string(s.idp.JWKS())) {
		t.Error("the key set of the issuer is in the output")
	}
	for _, line := range strings.Split(string(s.idp.JWKS()), `"`) {
		if len(line) >= 40 && strings.Contains(text, line) { // a key's modulus or coordinates
			t.Errorf("key material of the issuer is in the output: %.30s", line)
		}
	}
	if !strings.Contains(text, "authentication failed") {
		t.Error("the test did not look at a log that holds the refusals")
	}
}
