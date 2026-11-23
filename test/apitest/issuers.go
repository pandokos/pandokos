package apitest

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// SeedTokenIssuer inserts a tenant-bound trusted issuer for the tenant, as superuser (the
// administration handlers of sprint 4.1 are not needed to build a world), and returns its ID. The
// issuer URL and the audience are unique in the process.
func (a *API) SeedTokenIssuer(tb testing.TB, tenantID string) string {
	tb.Helper()
	n := unique.Add(1)
	var id string
	err := a.Super(tb).QueryRow(seedCtx(tb), `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience)
		VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
		tenantID, fmt.Sprintf("seed-issuer-%d", n), fmt.Sprintf("https://idp-%d.example.test", n), fmt.Sprintf("aud-%d", n)).Scan(&id)
	if err != nil {
		tb.Fatalf("seed trusted issuer: %v", err)
	}
	return id
}

// SeedPlatformIssuer inserts a platform-level issuer with the claim "org" and no tenant in its
// list, and returns its ID.
func (a *API) SeedPlatformIssuer(tb testing.TB) string {
	tb.Helper()
	n := unique.Add(1)
	var id string
	err := a.Super(tb).QueryRow(seedCtx(tb), `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim)
		VALUES ($1, $2, $3, 'org') RETURNING id::text`,
		fmt.Sprintf("seed-platform-%d", n), fmt.Sprintf("https://platform-idp-%d.example.test", n), fmt.Sprintf("platform-aud-%d", n)).Scan(&id)
	if err != nil {
		tb.Fatalf("seed platform issuer: %v", err)
	}
	return id
}

// SeedPlatformIssuerTenant puts the tenant in the list of the platform-level issuer, with the
// tenant's ID as the claim value.
func (a *API) SeedPlatformIssuerTenant(tb testing.TB, issuerID, tenantID string) {
	tb.Helper()
	if _, err := a.Super(tb).Exec(seedCtx(tb), `INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value)
		VALUES ($1::uuid, $2::uuid, $2) ON CONFLICT DO NOTHING`, issuerID, tenantID); err != nil {
		tb.Fatalf("seed platform issuer tenant: %v", err)
	}
}

// SeedSubjectMapping inserts a mapping of a subject that is unique in the process, of the
// tenant-bound issuer to the service account, and returns its ID.
func (a *API) SeedSubjectMapping(tb testing.TB, tenantID, issuerID, serviceAccountID string) string {
	tb.Helper()
	var id string
	err := a.Super(tb).QueryRow(seedCtx(tb), `INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid) RETURNING id::text`,
		tenantID, issuerID, fmt.Sprintf("subject-%d", unique.Add(1)), serviceAccountID).Scan(&id)
	if err != nil {
		tb.Fatalf("seed subject mapping: %v", err)
	}
	return id
}

func seedCtx(tb testing.TB) context.Context {
	tb.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	tb.Cleanup(cancel)
	return c
}

// SeedLimitRule inserts a limit rule of the tenant, as superuser, and returns its ID: a daily
// tokens quota of the tenant itself, which needs no subject row (a rule names a service account,
// a key or a mapping by a foreign key, and creating one would change the counts of other tests).
// A tenant has one such rule: the call is made once per tenant.
func (a *API) SeedLimitRule(tb testing.TB, tenantID string) string {
	tb.Helper()
	var id string
	err := a.Super(tb).QueryRow(seedCtx(tb), `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value)
		VALUES ($1::uuid, 'tenant', 'tokens_per_day', 1000000) RETURNING id::text`, tenantID).Scan(&id)
	if err != nil {
		tb.Fatalf("seed limit rule: %v", err)
	}
	return id
}

// SeedExtraLimitRule inserts another limit rule of the tenant, as superuser, and returns its ID: a
// daily budget in a currency that is unique in the process, for a test whose operation removes the
// rule it is given (the rule of SeedLimitRule is the one the other operations name).
func (a *API) SeedExtraLimitRule(tb testing.TB, tenantID string) string {
	tb.Helper()
	var id string
	err := a.Super(tb).QueryRow(seedCtx(tb), `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value, currency)
		VALUES ($1::uuid, 'tenant', 'cost_per_day', 1000000, $2) RETURNING id::text`, tenantID, seedCurrency()).Scan(&id)
	if err != nil {
		tb.Fatalf("seed extra limit rule: %v", err)
	}
	return id
}

// seedCurrency is a currency code that is unique in the process.
func seedCurrency() string {
	n := int(unique.Add(1))
	return string([]byte{byte('A' + n/676%26), byte('A' + n/26%26), byte('A' + n%26)})
}

// SeedPlatformLimit inserts a platform limit rule for the tenant, as superuser, and returns its ID;
// the currency of its budget is unique in the process, so that the call can be repeated for a
// tenant (a scope would need a provider of the catalogue, which is not the tenant's to create).
func (a *API) SeedPlatformLimit(tb testing.TB, tenantID string) string {
	tb.Helper()
	var id string
	err := a.Super(tb).QueryRow(seedCtx(tb), `INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value, currency)
		VALUES ($1::uuid, 'cost_per_day', 1000000, $2) RETURNING id::text`, tenantID, seedCurrency()).Scan(&id)
	if err != nil {
		tb.Fatalf("seed platform limit: %v", err)
	}
	return id
}

// SeedEventDestination inserts an event destination of the tenant, as superuser, and returns its
// ID. The sealed secret is a placeholder: the destination is there to be named, not to deliver
// (sprint 6.4; a test that delivers uses dbtest.SeedEventDestination).
func (a *API) SeedEventDestination(tb testing.TB, tenantID string) string {
	tb.Helper()
	var id string
	err := a.Super(tb).QueryRow(seedCtx(tb), `INSERT INTO pandokos.event_destination (tenant_id, name, url, secret, secret_key_version)
		VALUES ($1::uuid, 'seeded-' || substr(md5(random()::text), 1, 12), 'https://hooks.example.test/in', decode(repeat('ab', 40), 'hex'), 1) RETURNING id::text`, tenantID).Scan(&id)
	if err != nil {
		tb.Fatalf("seed event destination: %v", err)
	}
	return id
}
