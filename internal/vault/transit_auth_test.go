package vault_test

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/internal/vault/transittest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

func newClock() *tClock { return &tClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)} }

// lastWrapToken is the token of the latest encrypt request the double saw.
func lastWrapToken(s *transittest.Server) string {
	reqs := requestsOf(s, http.MethodPost, "/encrypt/")
	if len(reqs) == 0 {
		return ""
	}
	return reqs[len(reqs)-1].Header.Get("X-Vault-Token")
}

// settle gives the lifecycle goroutine time to act on a clock it was told about, for a test that
// asserts that nothing happens.
func settle() { time.Sleep(300 * time.Millisecond) }

// TestTransitTokenAuth is T-1064.
func TestTransitTokenAuth(t *testing.T) {
	aad := vault.DataAAD(testTenant(10), 1)
	dek := dataKeyBytes(29)

	t.Run("the token of the file, its lifetime, renewal at two thirds", func(t *testing.T) {
		c := newClock()
		r := newTransitRig(t, vaulttest.TransitSetup{TTL: 30 * time.Second, Renewable: true}, withClock(c))
		r.start(t, true)
		if got := lastLookupFile(t, r); got != r.f.Token {
			t.Errorf("the lookup was made with another token than the one of the file")
		}
		st := r.p.Stats()
		if !st.TokenExpires || st.TokenTTL < 29*time.Second || st.TokenTTL > 30*time.Second {
			t.Errorf("Stats: expires %v, ttl %v; want a token of 30 s", st.TokenExpires, st.TokenTTL)
		}
		if r.s.Count(transittest.OpLookup) != 1 {
			t.Errorf("%d lookups at the start", r.s.Count(transittest.OpLookup))
		}
		r.advance(19 * time.Second)
		settle()
		if n := r.s.Count(transittest.OpRenew); n != 0 {
			t.Errorf("renewed at 19 s of 30: %d renewals", n)
		}
		r.advance(2 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the renewal at two thirds", func() bool { return r.s.Count(transittest.OpRenew) == 1 })
		if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
			t.Errorf("a call after the renewal: %v", err)
		}
		if n := len(r.logs.Find(t, "key provider token expiring")); n != 0 {
			t.Errorf("a renewable token logged that it is expiring")
		}
	})

	t.Run("a token that cannot be renewed logs that it is expiring", func(t *testing.T) {
		c := newClock()
		r := newTransitRig(t, vaulttest.TransitSetup{TTL: 30 * time.Second}, withClock(c))
		r.start(t, true)
		r.advance(21 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the line key provider token expiring", func() bool {
			return len(r.logs.Find(t, "key provider token expiring")) >= 1
		})
		l := r.logs.Find(t, "key provider token expiring")[0]
		if l["level"] != "WARN" || l["kek_provider"] != "transit" || l["auth_method"] != "token" {
			t.Errorf("the line is %v", l)
		}
		if v, ok := l["expires_in_seconds"].(float64); !ok || v < 1 || v > 10 {
			t.Errorf("expires_in_seconds = %v, want what is left of the 30 s", l["expires_in_seconds"])
		}
		if n := r.s.Count(transittest.OpRenew); n != 0 {
			t.Errorf("a token that is not renewable was renewed %d times", n)
		}
	})

	t.Run("a renewable token at its maximum lifetime logs that it is expiring", func(t *testing.T) {
		c := newClock()
		r := newTransitRig(t, vaulttest.TransitSetup{TTL: 30 * time.Second, MaxTTL: 36 * time.Second, Renewable: true}, withClock(c))
		r.start(t, true)
		r.advance(21 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the first renewal", func() bool { return r.s.Count(transittest.OpRenew) == 1 })
		// 15 s are left of the 36 s the token may live: the renewal at two thirds of that grants no more.
		r.advance(11 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the renewal that extends nothing", func() bool { return r.s.Count(transittest.OpRenew) >= 2 })
		testutil.Eventually(t, 2*time.Second, "the line key provider token expiring", func() bool {
			return len(r.logs.Find(t, "key provider token expiring")) >= 1
		})
		l := r.logs.Find(t, "key provider token expiring")[0]
		if l["level"] != "WARN" || l["auth_method"] != "token" {
			t.Errorf("the line is %v", l)
		}
		if n := len(r.logs.Find(t, "key provider authentication failing")); n != 0 {
			t.Errorf("a token that cannot be extended logged an authentication failure")
		}
	})

	t.Run("a token that does not expire is never renewed", func(t *testing.T) {
		c := newClock()
		r := newTransitRig(t, vaulttest.TransitSetup{Renewable: true}, withClock(c)) // TTL 0
		r.start(t, true)
		if r.p.Stats().TokenExpires {
			t.Errorf("Stats().TokenExpires is true for a token without a lifetime")
		}
		r.advance(2 * time.Hour)
		settle()
		if n := r.s.Count(transittest.OpRenew); n != 0 {
			t.Errorf("%d renewals of a token that does not expire", n)
		}
		if n := len(r.logs.Find(t, "key provider token expiring")); n != 0 {
			t.Errorf("a token that does not expire logged that it is expiring")
		}
	})

	t.Run("a file replaced on disk is adopted at the next tick", func(t *testing.T) {
		c := newClock()
		r := newTransitRig(t, vaulttest.TransitSetup{TTL: 30 * time.Second, Renewable: true}, withClock(c))
		r.start(t, true)
		second := r.s.Namespace("").Token(transittest.TokenOptions{TTL: time.Minute, Renewable: true})
		writeFile(t, r.f.TokenFile, second+"\n")
		r.advance(21 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the new token to be adopted", func() bool {
			if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
				return false
			}
			return lastWrapToken(r.s) == second
		})
		if n := r.s.Count(transittest.OpLookup); n < 2 {
			t.Errorf("the new token was not looked up (%d lookups)", n)
		}
	})

	t.Run("a 403 re-reads the file, adopts a different token and sends the request once more", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{TTL: time.Hour, Renewable: true})
		r.start(t, false)
		sc := r.s.Namespace("")
		sc.RevokeToken(r.f.Token)
		second := sc.Token(transittest.TokenOptions{TTL: time.Hour, Renewable: true})
		writeFile(t, r.f.TokenFile, second+"\n")
		r.s.Reset()
		if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
			t.Fatalf("Wrap with a replaced token file: %v", err)
		}
		reqs := requestsOf(r.s, http.MethodPost, "/encrypt/")
		if len(reqs) != 2 || reqs[0].Header.Get("X-Vault-Token") != r.f.Token || reqs[1].Header.Get("X-Vault-Token") != second {
			t.Errorf("the encrypt requests are not the refused one and the one with the new token: %d", len(reqs))
		}
		// The file did not change this time: denied, and sent no more than once more.
		sc.RevokeToken(second)
		r.s.Reset()
		_, _, err := r.p.Wrap(ctx(t), dek, aad)
		te, ok := transitErr(err)
		if !ok || te.Class != "denied" {
			t.Errorf("a revoked token with an unchanged file: %v, want denied", err)
		}
		if n := r.s.Count(transittest.OpWrap); n > 2 {
			t.Errorf("a refused request was sent %d times, want at most twice", n)
		}
	})

	t.Run("a 403 with a single slot re-reads the file and sends again", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{TTL: time.Hour, Renewable: true}, func(o *vault.TransitOptions) { o.MaxConcurrent = 1 })
		r.start(t, false)
		sc := r.s.Namespace("")
		sc.RevokeToken(r.f.Token)
		second := sc.Token(transittest.TokenOptions{TTL: time.Hour, Renewable: true})
		writeFile(t, r.f.TokenFile, second+"\n")
		var wg sync.WaitGroup
		errs := make([]error, 3)
		begin := time.Now()
		for i := range errs {
			wg.Add(1)
			go func() { defer wg.Done(); _, _, errs[i] = r.p.Wrap(ctx(t), dek, aad) }()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("call %d: %v", i, err)
			}
		}
		if d := time.Since(begin); d > time.Second {
			t.Errorf("took %v, the lookup waited for a slot", d)
		}
	})

	t.Run("a lookup without a usable ttl is rejected, not read as a token that does not expire", func(t *testing.T) {
		for name, change := range map[string]func(map[string]any){
			"missing":  func(d map[string]any) { delete(d, "ttl") },
			"text":     func(d map[string]any) { d["ttl"] = "soon" },
			"object":   func(d map[string]any) { d["ttl"] = map[string]any{} },
			"negative": func(d map[string]any) { d["ttl"] = -5 },
		} {
			ic := newInterceptor(nil)
			r := newTransitRig(t, vaulttest.TransitSetup{TTL: time.Minute, Renewable: true}, withTransport(func(rt http.RoundTripper) http.RoundTripper { ic.inner = rt; return ic }))
			ic.rewrite(func(req *http.Request, resp *http.Response) *http.Response {
				if !strings.Contains(req.URL.Path, "lookup-self") {
					return resp
				}
				return rewriteJSON(resp, func(m map[string]any) {
					if d, ok := m["data"].(map[string]any); ok {
						change(d)
					}
				})
			})
			err := r.p.Start(ctx(t))
			te, ok := transitErr(err)
			if !ok || te.Class != "rejected" {
				t.Errorf("%s ttl: Start = %v, want a rejected lookup", name, err)
			}
		}
	})

	t.Run("a renewal without a lease_duration is rejected and the lifetime stays", func(t *testing.T) {
		c := newClock()
		ic := newInterceptor(nil)
		r := newTransitRig(t, vaulttest.TransitSetup{TTL: 30 * time.Second, Renewable: true}, withClock(c),
			withTransport(func(rt http.RoundTripper) http.RoundTripper { ic.inner = rt; return ic }))
		r.start(t, true)
		ic.rewrite(func(req *http.Request, resp *http.Response) *http.Response {
			if !strings.Contains(req.URL.Path, "renew-self") {
				return resp
			}
			return rewriteJSON(resp, func(m map[string]any) { delete(m, "auth") })
		})
		r.advance(21 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the failing renewal", func() bool {
			return len(r.logs.Find(t, "key provider authentication failing")) >= 1
		})
		if l := r.logs.Find(t, "key provider authentication failing")[0]; l["error_class"] != "rejected" {
			t.Errorf("the line is %v, want error_class rejected", l)
		}
		if st := r.p.Stats(); !st.TokenExpires || st.TokenTTL > 10*time.Second {
			t.Errorf("Stats: expires %v ttl %v, the refused renewal changed the lifetime", st.TokenExpires, st.TokenTTL)
		}
	})

	t.Run("a refused lookup fails the start as denied", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{})
		noLookup := r.s.Namespace("").Token(transittest.TokenOptions{NoLookup: true, TTL: time.Hour})
		writeFile(t, r.f.TokenFile, noLookup+"\n")
		err := r.p.Start(ctx(t))
		te, ok := transitErr(err)
		if !ok || te.Operation != "lookup" || te.Class != "denied" {
			t.Errorf("Start with a token that cannot look itself up: %v", err)
		}
	})

	t.Run("a file with a control character or an empty file is refused without quoting it", func(t *testing.T) {
		for name, content := range map[string]string{
			"a control character": "canary-token-text\x01rest\n", "empty": "", "only a newline": "\n",
			"a NUL": "canary-token-text\x00\n",
		} {
			r := newTransitRig(t, vaulttest.TransitSetup{})
			writeFile(t, r.f.TokenFile, content)
			err := r.p.Start(ctx(t))
			if err == nil {
				t.Errorf("%s: Start succeeded", name)
				continue
			}
			if strings.Contains(err.Error(), "canary-token-text") || strings.Contains(err.Error(), "rest") {
				t.Errorf("%s: the error quotes the file: %v", name, err)
			}
			if n := len(r.s.Requests()); n != 0 {
				t.Errorf("%s: %d requests were sent with a token that is not valid", name, n)
			}
		}
		r := newTransitRig(t, vaulttest.TransitSetup{})
		if err := os.Remove(r.f.TokenFile); err != nil {
			t.Fatal(err)
		}
		if err := r.p.Start(ctx(t)); err == nil || strings.Contains(err.Error(), r.f.TokenFile) && strings.Contains(err.Error(), "canary") {
			t.Errorf("a missing file: %v", err)
		}
	})
}

// lastLookupFile returns the token the first lookup was made with.
func lastLookupFile(t *testing.T, r *transitRig) string {
	t.Helper()
	reqs := requestsOf(r.s, http.MethodGet, "lookup-self")
	if len(reqs) == 0 {
		t.Fatal("no lookup")
	}
	return reqs[0].Header.Get("X-Vault-Token")
}

// TestTransitAppRoleAuth is T-1065.
func TestTransitAppRoleAuth(t *testing.T) {
	aad := vault.DataAAD(testTenant(11), 1)
	dek := dataKeyBytes(31)

	t.Run("a login with the two files at the configured mount", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{Method: "approle", AppRoleMount: "approle-two"})
		r.start(t, false)
		logins := requestsOf(r.s, http.MethodPost, "/login")
		if len(logins) != 1 || logins[0].Path != "/v1/auth/approle-two/login" {
			t.Fatalf("the logins are %v", logins)
		}
		b := bodyOf(t, logins[0])
		if strings.Join(keysOf(b), ",") != "role_id,secret_id" || b["role_id"] != r.f.RoleID || b["secret_id"] != r.f.SecretID {
			t.Errorf("the login body is not the two files: %v", keysOf(b))
		}
		if logins[0].Header.Get("X-Vault-Token") != "" {
			t.Errorf("the login carries a token")
		}
		if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
			t.Fatal(err)
		}
		if lastWrapToken(r.s) == r.f.RoleID || lastWrapToken(r.s) == "" {
			t.Errorf("the requests do not use the token of the login")
		}
	})

	t.Run("renewal, then a new login when the lifetime granted shrinks to a third", func(t *testing.T) {
		c := newClock()
		r := newTransitRig(t, vaulttest.TransitSetup{Method: "approle", TTL: 9 * time.Second, MaxTTL: 12 * time.Second}, withClock(c))
		r.start(t, true)
		r.advance(6 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the renewal", func() bool { return r.s.Count(transittest.OpRenew) == 1 })
		if n := r.s.Count(transittest.OpLogin); n != 1 {
			t.Errorf("%d logins before the lifetime shrank", n)
		}
		r.advance(4 * time.Second) // 2 s are left of the 12 s the token may live: a third of 9 s or less
		testutil.Eventually(t, 2*time.Second, "a new login", func() bool { return r.s.Count(transittest.OpLogin) == 2 })
		first := requestsOf(r.s, http.MethodPost, "/login")
		if len(first) != 2 {
			t.Fatalf("%d logins", len(first))
		}
		testutil.Eventually(t, 2*time.Second, "calls with the new token", func() bool {
			_, _, err := r.p.Wrap(ctx(t), dek, aad)
			return err == nil
		})
	})

	t.Run("a 403 makes one login shared by 20 concurrent calls", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{Method: "approle", TTL: time.Hour})
		r.start(t, false)
		if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
			t.Fatal(err)
		}
		r.s.Namespace("").RevokeToken(lastWrapToken(r.s))
		r.s.Reset()
		var wg sync.WaitGroup
		errs := make([]error, 20)
		for i := range errs {
			wg.Add(1)
			go func() { defer wg.Done(); _, _, errs[i] = r.p.Wrap(ctx(t), dek, aad) }()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("call %d: %v", i, err)
			}
		}
		if n := r.s.Count(transittest.OpLogin); n != 1 {
			t.Errorf("%d logins for 20 calls refused at once, want 1", n)
		}
		ok := 0
		for _, q := range requestsOf(r.s, http.MethodPost, "/encrypt/") {
			if q.Header.Get("X-Vault-Token") == lastWrapToken(r.s) {
				ok++
			}
		}
		if ok < 20 {
			t.Errorf("only %d requests were sent again with the new token", ok)
		}
	})

	t.Run("a 403 with every slot taken by refused requests still makes its login and sends again", func(t *testing.T) {
		for _, tc := range []struct{ max, calls int }{{1, 1}, {1, 3}, {4, 8}} {
			r := newTransitRig(t, vaulttest.TransitSetup{Method: "approle", TTL: time.Hour}, func(o *vault.TransitOptions) { o.MaxConcurrent = tc.max })
			r.start(t, false)
			if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
				t.Fatal(err)
			}
			r.s.Namespace("").RevokeToken(lastWrapToken(r.s))
			r.s.Reset()
			var wg sync.WaitGroup
			errs := make([]error, tc.calls)
			begin := time.Now()
			for i := range errs {
				wg.Add(1)
				go func() { defer wg.Done(); _, _, errs[i] = r.p.Wrap(ctx(t), dek, aad) }()
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Errorf("max %d, %d calls: call %d: %v", tc.max, tc.calls, i, err)
				}
			}
			if n := r.s.Count(transittest.OpLogin); n != 1 {
				t.Errorf("max %d, %d calls: %d logins, want 1", tc.max, tc.calls, n)
			}
			if d := time.Since(begin); d > time.Second {
				t.Errorf("max %d, %d calls: took %v, the login waited for a slot", tc.max, tc.calls, d)
			}
		}
	})

	t.Run("a login refused while running keeps the token, logs, backs off and reads the secret ID again", func(t *testing.T) {
		c := newClock()
		r := newTransitRig(t, vaulttest.TransitSetup{Method: "approle", TTL: 9 * time.Second, MaxTTL: 12 * time.Second}, withClock(c))
		r.start(t, true)
		sc := r.s.Namespace("")
		newSecret := sc.NewSecretID(r.f.AppRoleMount, r.f.RoleID) // the one of the file stops working
		r.advance(6 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the renewal", func() bool { return r.s.Count(transittest.OpRenew) == 1 })
		r.advance(4 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the refused login", func() bool { return r.s.Count(transittest.OpLogin) == 2 })
		testutil.Eventually(t, 2*time.Second, "the line key provider authentication failing", func() bool {
			return len(r.logs.Find(t, "key provider authentication failing")) >= 1
		})
		l := r.logs.Find(t, "key provider authentication failing")[0]
		if l["level"] != "ERROR" || l["kek_provider"] != "transit" || l["auth_method"] != "approle" || l["error_class"] == nil || l["failures"] == nil {
			t.Errorf("the line is %v", l)
		}
		if _, _, err := r.p.Wrap(ctx(t), dek, aad); err != nil {
			t.Errorf("the current token stopped working before it expired: %v", err)
		}
		// Back off: 1 s, then 2 s.
		r.advance(1 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the second attempt after 1 s", func() bool { return r.s.Count(transittest.OpLogin) == 3 })
		r.advance(1 * time.Second)
		settle()
		if n := r.s.Count(transittest.OpLogin); n != 3 {
			t.Errorf("%d logins 1 s after the second attempt: the backoff did not double", n)
		}
		r.advance(1 * time.Second)
		testutil.Eventually(t, 2*time.Second, "the third attempt after 2 s", func() bool { return r.s.Count(transittest.OpLogin) == 4 })
		// The backoff ends at 60 s.
		before := r.s.Count(transittest.OpLogin)
		r.advance(61 * time.Second)
		testutil.Eventually(t, 2*time.Second, "an attempt within 60 s", func() bool { return r.s.Count(transittest.OpLogin) > before })
		// The secret ID file is read again at every login.
		writeFile(t, r.f.SecretIDFile, newSecret+"\n")
		r.advance(61 * time.Second)
		testutil.Eventually(t, 3*time.Second, "calls to work with the new secret ID", func() bool {
			_, _, err := r.p.Wrap(ctx(t), dek, aad)
			return err == nil
		})
		logins := requestsOf(r.s, http.MethodPost, "/login")
		if b := bodyOf(t, logins[len(logins)-1]); b["secret_id"] != newSecret {
			t.Errorf("the last login did not use the secret ID read from the file again")
		}
	})

	t.Run("a login without a lease_duration is rejected", func(t *testing.T) {
		ic := newInterceptor(nil)
		r := newTransitRig(t, vaulttest.TransitSetup{Method: "approle", TTL: time.Hour}, withTransport(func(rt http.RoundTripper) http.RoundTripper { ic.inner = rt; return ic }))
		ic.rewrite(func(req *http.Request, resp *http.Response) *http.Response {
			return rewriteJSON(resp, func(m map[string]any) {
				if a, ok := m["auth"].(map[string]any); ok {
					delete(a, "lease_duration")
				}
			})
		})
		err := r.p.Start(ctx(t))
		if te, ok := transitErr(err); !ok || te.Class != "rejected" {
			t.Errorf("Start = %v, want a rejected login", err)
		}
	})

	t.Run("a login answered 503 is unavailable", func(t *testing.T) {
		r := newTransitRig(t, vaulttest.TransitSetup{Method: "approle"})
		r.s.Status(http.StatusServiceUnavailable)
		err := r.p.Start(ctx(t))
		te, ok := transitErr(err)
		if !ok || te.Operation != "login" || te.Class != "unavailable" || !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Errorf("Start with a login answered 503: %v", err)
		}
	})
}
