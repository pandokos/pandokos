package resolver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestTokenFailureAuditor is T-594 (the half of the recorder): the failures of a token that name a
// tenant are written through the second definer function, with their own suppression key, in the
// same bounds as the failures of keys.
func TestTokenFailureAuditor(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	gw := d.Open(t, store.RoleGateway)
	a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
	super := d.Super(t)
	logs := &testutil.LogBuffer{}
	clock := &fakeClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	newAuditor := func() *resolver.FailureAuditor {
		return resolver.NewFailureAuditor(gw, telemetry.NewLogger(logs, "debug"), clock.now)
	}
	var issuerA, platformIssuer string
	if err := super.QueryRow(ctx(t), `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, 'idp', 'https://idp.example.test', 'aud') RETURNING id::text`, a.String()).Scan(&issuerA); err != nil {
		t.Fatal(err)
	}
	if err := super.QueryRow(ctx(t), `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('shared', 'https://shared.example.test', 'aud', 'org') RETURNING id::text`).Scan(&platformIssuer); err != nil {
		t.Fatal(err)
	}
	type row struct {
		ResourceType, ResourceID, Actor, Outcome, Request string
		Detail                                            map[string]any
	}
	rowsOf := func(tid string) []row {
		t.Helper()
		rs, err := super.Query(ctx(t), `SELECT resource_type, resource_id, actor_type, outcome, request_id, detail::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure' ORDER BY id`, tid)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		var out []row
		for rs.Next() {
			var r row
			var detail string
			if err := rs.Scan(&r.ResourceType, &r.ResourceID, &r.Actor, &r.Outcome, &r.Request, &detail); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(detail), &r.Detail); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	n := func() int { return len(rowsOf(a.String())) }
	tokenFailure := func(i int, issuer, scope, reason, subject string) gateway.AuthFailure {
		return gateway.AuthFailure{RequestID: requestIDFor(i), Reason: reason, TenantID: a.String(), IssuerID: issuer, IssuerScope: scope, Subject: subject}
	}

	t.Run("a row in the tenant's log, written by the definer function", func(t *testing.T) {
		au := newAuditor()
		if !au.Record(ctx(t), tokenFailure(1, issuerA, "tenant", "bad_signature", "")) {
			t.Fatal("not recorded")
		}
		rs := rowsOf(a.String())
		if len(rs) != 1 {
			t.Fatalf("%d rows", len(rs))
		}
		r := rs[0]
		if r.ResourceType != "token_issuer" || r.ResourceID != issuerA || r.Actor != "anonymous" || r.Outcome != "failure" || r.Request != requestIDFor(1) ||
			r.Detail["reason"] != "bad_signature" || r.Detail["listener"] != "inference" || r.Detail["issuer_scope"] != "tenant" || len(r.Detail) != 3 {
			t.Errorf("row %+v", r)
		}
		var keyID string
		if err := super.QueryRow(ctx(t), `SELECT key_id FROM pandokos.audit_event WHERE request_id = $1`, requestIDFor(1)).Scan(&keyID); err != nil || keyID != "" {
			t.Errorf("key_id %q %v: a token failure has no key", keyID, err)
		}
		// The platform log and the other tenant's log are untouched.
		if len(rowsOf(b.String())) != 0 {
			t.Error("a row in another tenant's log")
		}
	})

	t.Run("a subject is recorded when given, and a platform-level registration says so", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := n()
		if !au.Record(ctx(t), tokenFailure(2, platformIssuer, "platform", "subject_not_mapped", "spiffe://a/ns/ci")) {
			t.Fatal("not recorded")
		}
		rs := rowsOf(a.String())
		r := rs[len(rs)-1]
		if len(rs) != before+1 || r.ResourceID != platformIssuer || r.Detail["issuer_scope"] != "platform" || r.Detail["subject"] != "spiffe://a/ns/ci" || len(r.Detail) != 4 {
			t.Errorf("row %+v", r)
		}
	})

	t.Run("the suppression key is the registration, the reason and the subject", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := n()
		if !au.Record(ctx(t), tokenFailure(10, issuerA, "tenant", "subject_not_mapped", "one")) {
			t.Fatal("the first was not recorded")
		}
		if au.Record(ctx(t), tokenFailure(11, issuerA, "tenant", "subject_not_mapped", "one")) {
			t.Error("a repeat within ten minutes was recorded")
		}
		if !au.Record(ctx(t), tokenFailure(12, issuerA, "tenant", "subject_not_mapped", "two")) {
			t.Error("another subject must be recorded: one unmapped subject cannot hide another")
		}
		if !au.Record(ctx(t), tokenFailure(13, issuerA, "tenant", "service_account_disabled", "one")) {
			t.Error("another reason for the same subject must be recorded")
		}
		if !au.Record(ctx(t), tokenFailure(14, platformIssuer, "platform", "subject_not_mapped", "one")) {
			t.Error("another registration with the same reason and subject must be recorded")
		}
		if n() != before+4 {
			t.Errorf("%d rows written, want 4", n()-before)
		}
		clock.t = clock.t.Add(9 * time.Minute)
		if au.Record(ctx(t), tokenFailure(15, issuerA, "tenant", "subject_not_mapped", "one")) {
			t.Error("a repeat after nine minutes was recorded")
		}
		clock.t = clock.t.Add(2 * time.Minute)
		if !au.Record(ctx(t), tokenFailure(16, issuerA, "tenant", "subject_not_mapped", "one")) {
			t.Error("a repeat after the window was not recorded")
		}
	})

	t.Run("100 forged tokens of one registration write one row per reason", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := n()
		wrote := 0
		for i := 0; i < 100; i++ {
			if au.Record(ctx(t), tokenFailure(100+i, issuerA, "tenant", "bad_signature", "")) { // a subject that was not verified is empty
				wrote++
			}
		}
		if wrote != 1 || n() != before+1 {
			t.Errorf("%d of 100 recorded, %d rows written, want 1", wrote, n()-before)
		}
		for _, reason := range []string{"expired", "wrong_audience", "required_claim_missing"} {
			if !au.Record(ctx(t), tokenFailure(300, issuerA, "tenant", reason, "")) {
				t.Errorf("%s was not recorded", reason)
			}
		}
	})

	t.Run("concurrent identical failures write one row", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := n()
		var wrote atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if au.Record(ctx(t), tokenFailure(500+i, issuerA, "tenant", "not_yet_valid", "")) {
					wrote.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wrote.Load() != 1 || n() != before+1 {
			t.Errorf("%d recorded, %d rows written, want 1", wrote.Load(), n()-before)
		}
	})

	t.Run("keys and tokens share the bucket and not the suppression key", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := n()
		// A key failure with the same reason is another failure than a token failure. (A key ID
		// has the shape t + 16 base32 characters, so it can never be the string of a registration's ID.)
		key := gateway.AuthFailure{RequestID: requestIDFor(600), Reason: "bad_signature", KeyID: "tabcdefghijklmnop", TenantID: a.String()}
		if !au.Record(ctx(t), tokenFailure(601, issuerA, "tenant", "bad_signature", "")) || !au.Record(ctx(t), key) {
			t.Error("a token failure and a key failure with equal strings must both be recorded")
		}
		if n() != before+2 {
			t.Errorf("%d rows", n()-before)
		}
		// 60 distinct failures of either kind empty the bucket.
		clock.t = clock.t.Add(time.Hour)
		au = newAuditor()
		before = n()
		for i := 0; i < 60; i++ {
			f := tokenFailure(700+i, issuerA, "tenant", "subject_not_mapped", fmt.Sprintf("s%d", i))
			if i%2 == 1 {
				f = gateway.AuthFailure{RequestID: requestIDFor(700 + i), Reason: "wrong_secret", KeyID: keyIDFor(5000 + i), TenantID: a.String()}
			}
			if !au.Record(ctx(t), f) {
				t.Fatalf("failure %d was refused before the bucket was empty", i)
			}
		}
		if au.Record(ctx(t), tokenFailure(800, issuerA, "tenant", "subject_not_mapped", "one-more")) || n() != before+60 {
			t.Error("the 61st failure within the same second was recorded")
		}
		clock.t = clock.t.Add(time.Second)
		if !au.Record(ctx(t), tokenFailure(801, issuerA, "tenant", "subject_not_mapped", "one-more")) {
			t.Error("a token is back after a second")
		}
	})

	t.Run("a failure that names no tenant, or that the function refuses, is not written and leaves no window", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := n()
		none := tokenFailure(900, issuerA, "tenant", "unknown_issuer", "")
		none.TenantID = ""
		if au.Record(ctx(t), none) {
			t.Error("a failure without a tenant was recorded")
		}
		for name, f := range map[string]gateway.AuthFailure{
			"a reason in upper case":      tokenFailure(901, issuerA, "tenant", "Bad_Signature", ""),
			"a bad scope":                 tokenFailure(902, issuerA, "global", "bad_signature", ""),
			"a bad request ID":            {RequestID: "not-hex", Reason: "bad_signature", TenantID: a.String(), IssuerID: issuerA, IssuerScope: "tenant"},
			"a subject with a newline":    tokenFailure(903, issuerA, "tenant", "subject_not_mapped", "a\nb"),
			"a subject of 256 characters": tokenFailure(904, issuerA, "tenant", "subject_not_mapped", strings.Repeat("s", 256)),
			"an issuer that is not an ID": tokenFailure(905, "not-a-uuid", "tenant", "bad_signature", ""),
		} {
			if au.Record(ctx(t), f) {
				t.Errorf("%s: recorded", name)
			}
		}
		if n() != before {
			t.Errorf("%d rows written by failures that must not be", n()-before)
		}
		// The refused ones started no window: a good one with the same reason is recorded now.
		if !au.Record(ctx(t), tokenFailure(906, issuerA, "tenant", "bad_signature", "")) {
			t.Error("a refused write started the suppression window")
		}
		// Nothing about a refused write is logged above debug, and no value is in the log.
		for _, l := range logs.Lines(t) {
			if l["level"] != "DEBUG" && strings.Contains(fmt.Sprint(l), "authentication failure not recorded") {
				t.Errorf("a refused write is logged above debug: %v", l)
			}
		}
		if strings.Contains(logs.String(), "a\\nb") || strings.Contains(logs.String(), strings.Repeat("s", 256)) {
			t.Error("the log holds the subject of a refused record")
		}
	})

	t.Run("the write does not depend on the request's context", func(t *testing.T) {
		au := newAuditor()
		clock.t = clock.t.Add(time.Hour)
		before := n()
		cctx, cancel := context.WithCancel(ctx(t))
		cancel()
		if !au.Record(cctx, tokenFailure(950, issuerA, "tenant", "bad_signature", "")) || n() != before+1 {
			t.Error("a failure whose request was cancelled was not recorded")
		}
	})
}
