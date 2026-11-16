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
