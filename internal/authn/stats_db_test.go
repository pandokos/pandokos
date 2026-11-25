package authn_test

import (
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
)

// attempts flattens the Stats of an authenticator into "kind|result|reason" counts. Stats().Attempts
// is a map keyed by a value with the fields Kind, Result and Reason.
func attempts(a *authn.Authenticator) map[string]int64 {
	out := map[string]int64{}
	for k, v := range a.Stats().Attempts {
		out[k.Kind+"|"+k.Result+"|"+k.Reason] += v
	}
	return out
}

// TestStats is T-918: per listener instance, a good key, a good token, each failure reason and an
// unavailable database are counted by kind, result and reason; one count per call; the snapshot
// holds no key, tenant, issuer or subject value.
func TestStats(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	r := newTokenRig(t, d, 0)
	f := newFixture(t, d, "stats")
	gw := r.auth
	var admin *authn.Authenticator // made after the database was stopped and started: a pool made before holds dead connections

	want := map[string]int64{}
	count := func(kind, result, reason string) { want[kind+"|"+result+"|"+reason]++ }
	check := func(t *testing.T, what string) {
		t.Helper()
		got := attempts(gw)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("after %s: %q counted %d, want %d", what, k, got[k], v)
			}
		}
		for k, v := range got {
			if want[k] == 0 {
				t.Errorf("after %s: %q counted %d, want nothing", what, k, v)
			}
		}
	}
	if got := attempts(gw); len(got) != 0 {
		t.Fatalf("a new authenticator has counted %v", got)
	}

	t.Run("keys", func(t *testing.T) {
		good, _ := f.key(t, nil, nil, "")
		if _, err := gw.Authenticate(ctx(t), good); err != nil {
			t.Fatal(err)
		}
		count("key", "ok", "")
		check(t, "a good key")

		plat, _ := platformKey(t, d, false, "")
		unknown, _, _, _ := authn.Generate(authn.KindTenant)
		_, _, other, _ := authn.Generate(authn.KindTenant)
		wrong, _ := f.key(t, &other, nil, "")
		revoked, rid := f.key(t, nil, nil, "")
		f.exec(t, "UPDATE pandokos.virtual_key SET revoked_at = now() WHERE key_id = $1", rid)
		expired, _ := f.key(t, nil, nil, "now() - interval '1 hour'")
		g := newFixture(t, d, "stats-disabled")
		disabled, _ := g.key(t, nil, nil, "")
		g.exec(t, "UPDATE pandokos.service_account SET state = 'disabled' WHERE id = $1::uuid", g.sa)
		h := newFixture(t, d, "stats-early")
		early, _ := h.key(t, nil, nil, "")
		h.exec(t, "UPDATE pandokos.service_account SET valid_from = now() + interval '1 hour' WHERE id = $1::uuid", h.sa)

		for _, c := range []struct{ presented, kind, reason string }{
			{"pdk_nonsense", "unknown", authn.ReasonMalformed},
			{"", "unknown", authn.ReasonMalformed},
			{plat, "key", authn.ReasonKindNotAccepted}, // the gateway cannot look at a platform key
			{unknown.Reveal(), "key", authn.ReasonUnknownKey},
			{wrong, "key", authn.ReasonWrongSecret},
			{revoked, "key", authn.ReasonRevoked},
			{expired, "key", authn.ReasonExpired},
			{disabled, "key", authn.ReasonServiceAccountDisabled},
			{early, "key", authn.ReasonServiceAccountNotValid},
		} {
			if _, err := gw.Authenticate(ctx(t), c.presented); err == nil {
				t.Fatalf("%q was accepted", c.reason)
			}
			count(c.kind, "failed", c.reason)
			check(t, c.reason)
		}
	})

	t.Run("tokens", func(t *testing.T) {
		const aud = "pandokos-stats"
		issuerID := newTrustedIssuer(t, d, f.tenant, r.idp, aud)
		mapSubject(t, d, f.tenant, issuerID, false, "workload-1", f.sa)
		if _, err := gw.Authenticate(ctx(t), r.token(t, "ES256", "workload-1", aud)); err != nil {
			t.Fatal(err)
		}
		count("token", "ok", "")
		check(t, "a good token")

		if _, err := gw.Authenticate(ctx(t), r.token(t, "ES256", "stranger", aud)); err == nil {
			t.Fatal("an unmapped subject was accepted")
		}
		count("token", "failed", authn.ReasonSubjectNotMapped)
		check(t, "an unmapped subject")
	})

	t.Run("an unavailable database", func(t *testing.T) {
		good, _ := f.key(t, nil, nil, "")
		d.SetAcceptingConnections(t, false)
		_, err := gw.Authenticate(ctx(t), good)
		d.SetAcceptingConnections(t, true)
		if err == nil {
			t.Fatal("authenticated with the database stopped")
		}
		count("key", "unavailable", "")
		check(t, "an unavailable database")
	})

	t.Run("each listener instance counts its own", func(t *testing.T) {
		admin, _ = newAuth(t, d.Open(t, store.RoleAdmin), 0, authn.KindTenant, authn.KindPlatform)
		if got := attempts(admin); len(got) != 0 {
			t.Fatalf("the admin authenticator counted the gateway's calls: %v", got)
		}
		plat, _ := platformKey(t, d, false, "")
		if _, err := admin.Authenticate(ctx(t), plat); err != nil {
			t.Fatal(err)
		}
		if got := attempts(admin); got["key||"] != 0 || got["key|ok|"] != 1 || len(got) != 1 {
			t.Errorf("admin: %v", got)
		}
		check(t, "a call on the other instance") // the gateway's counts did not move
	})

	t.Run("the snapshot holds no identifier", func(t *testing.T) {
		kinds := map[string]bool{"key": true, "token": true, "unknown": true}
		results := map[string]bool{"ok": true, "failed": true, "unavailable": true}
		reasons := map[string]bool{"": true}
		for _, r := range authn.Reasons {
			reasons[r] = true
		}
		for _, a := range []*authn.Authenticator{gw, admin} {
			for k := range a.Stats().Attempts {
				if !kinds[k.Kind] || !results[k.Result] || !reasons[k.Reason] {
					t.Errorf("a value outside the closed lists: %+v", k)
				}
				if strings.Contains(k.Kind+k.Result+k.Reason, f.sa) || strings.Contains(k.Kind+k.Result+k.Reason, "stats") {
					t.Errorf("an identifier in %+v", k)
				}
			}
		}
	})
}
