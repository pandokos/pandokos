//go:build upgrade

package upgrade

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/instance"
	"github.com/pandokos/pandokos/test/testutil"
)

// upgraded is what the upgrade of a world leaves for the assertions: the outcomes of the traffic, the
// moments of the procedure and the processes.
type upgraded struct {
	w        *world
	proc     *Procedure
	newProc  *instance.Process
	outcomes []Outcome
	migrate  string // the output of migrate
	dump     string // the dump taken before migrate
	inFlight []Outcome
	started  time.Time // when the traffic started
	migrated time.Time // when migrate finished
}

// runUpgrade is the exit scenario of sprint 12.3 (5.9): the world on the previous release, traffic on
// it, and the upgrade as the guide gives it — check, backup, stop, migrate, start — with the clients
// calling throughout and finding the instance again by themselves. It is run once per test that
// reads its result.
func runUpgrade(t *testing.T) *upgraded {
	t.Helper()
	w := newWorld(t)
	newBin := instance.Build(t)
	u := &upgraded{w: w}
	u.proc = &Procedure{Old: w.inst.Bin, New: newBin, DB: w.db, Env: w.env, Canaries: w.inst.Canaries}

	tr := NewTraffic(w.url)
	tr.Start(w.clients()...)
	u.started = time.Now()

	// The previous release serves for a while: tenant b reaches its early quota, streams are in flight.
	testutil.Eventually(t, 60*time.Second, "the previous release to serve complete calls and refuse b's quota", func() bool {
		var complete, refusedB int
		for _, o := range tr.Outcomes() {
			if o.Class == Complete {
				complete++
			}
			if o.Class == Quota && o.Tenant == "b" {
				refusedB++
			}
		}
		return complete >= 60 && refusedB >= 3
	})

	u.proc.Check(t)
	u.dump = u.proc.Backup(t)

	// SIGTERM while a stream is in flight at the provider.
	testutil.Eventually(t, 30*time.Second, "a call in flight at the provider", func() bool { return w.up.InFlight() > 0 })
	u.proc.Stop(t, w.proc)
	u.migrate = u.proc.Migrate(t)
	u.migrated = time.Now()
	u.newProc = u.proc.Start(t)
	w.setInference(u.newProc.Inference)

	// The new release serves until tenant a's late quota is reached.
	testutil.Eventually(t, 90*time.Second, "tenant a's quota to be reached on the new release", func() bool {
		for _, o := range tr.Outcomes() {
			if o.Tenant == "a" && o.Class == Quota && o.Started.After(u.proc.NewReady) {
				return true
			}
		}
		return false
	})
	time.Sleep(500 * time.Millisecond)
	u.outcomes = Correlate(tr.Stop(), w.up)
	for _, o := range u.outcomes {
		if o.Started.Before(u.proc.StopSent) && o.Ended.After(u.proc.StopSent) && o.Status == http.StatusOK {
			u.inFlight = append(u.inFlight, o)
		}
	}
	WriteReport(t, "upgrade", map[string]any{
		"previous": ReadPin(t).Version, "gap_ms": u.proc.Gap().Milliseconds(), "calls": len(u.outcomes),
		"steps": stepMillis(u.proc.Steps),
	})
	return u
}

func stepMillis(steps []StepRecord) map[string]int64 {
	out := map[string]int64{}
	for _, s := range steps {
		out[s.Name] = s.End.Sub(s.Start).Milliseconds()
	}
	return out
}

// TestUpgradeWithTraffic is T-1130, the exit test of the upgrade: v0.2.x with traffic is upgraded by
// the procedure of the guide, and what happened to every request is asserted (sprint 12.3, 5.9).
//
// The quota is the one of requests per day. The installation's time zone is a fixed-offset zone
// (zoneNoMidnight) in which it is mid-morning to mid-afternoon, so that no start of a tenant's day, and
// with it no reset of the quota, can fall within the test's minutes whatever the hour it runs at.
func TestUpgradeWithTraffic(t *testing.T) {
	t.Parallel()
	u := runUpgrade(t)
	w := u.w

	t.Run("the procedure", func(t *testing.T) {
		if u.proc.OldExited != 0 {
			t.Errorf("the previous release exited %d on SIGTERM, want 0 within its deadline", u.proc.OldExited)
		}
		for _, want := range []string{"applied 0017_events", "schema is at version 17"} {
			if !strings.Contains(u.migrate, want) {
				t.Errorf("migrate printed no %q:\n%s", want, u.migrate)
			}
		}
		if gap := u.proc.Gap(); gap <= 0 || gap > 60*time.Second {
			t.Errorf("the interval without service is %v, want a positive time under the ceiling of 60 s", gap)
		}
	})

	t.Run("every call ended in an acceptable way", func(t *testing.T) {
		counts := map[Class]int{}
		for _, o := range u.outcomes {
			counts[o.Class]++
			switch o.Class {
			case Complete, Quota:
			case NoAnswer, Draining:
				// Only between the SIGTERM and the readiness of the new instance.
				if o.Ended.Before(u.proc.StopSent) || o.Started.After(u.proc.NewReady) {
					t.Errorf("a call of tenant %s (%s) got no answer (%s) at %v, outside [%v, %v]", o.Tenant, o.Kind, o.Error, o.Started.Sub(u.started),
						u.proc.StopSent.Sub(u.started), u.proc.NewReady.Sub(u.started))
				}
			default:
				t.Errorf("a call of tenant %s (%s) ended as %s (status %d, %s)", o.Tenant, o.Kind, o.Class, o.Status, o.Error)
			}
		}
		if counts[Complete] == 0 || counts[Quota] == 0 {
			t.Errorf("the outcomes are %v: the test did not exercise answers and refusals", counts)
		}
		// Every call that was answered was answered with its own tenant's credential, never another's.
		for _, o := range u.outcomes {
			if o.Class != Complete {
				continue
			}
			if want := Fingerprint(w.tenants[o.Tenant].Credential); o.CredentialSeen != want {
				t.Errorf("a call of tenant %s reached the provider with credential %q, want this tenant's (%s)", o.Tenant, o.CredentialSeen, want)
			}
		}
	})

	t.Run("streams in flight at the signal completed", func(t *testing.T) {
		if len(u.inFlight) == 0 {
			t.Fatal("no call was in flight when the signal came: the test did not exercise the drain")
		}
		var streams int
		for _, o := range u.inFlight {
			if o.Kind == Stream {
				streams++
			}
			if o.Class != Complete {
				t.Errorf("a call in flight at SIGTERM (%s, tenant %s) ended as %s", o.Kind, o.Tenant, o.Class)
			}
		}
		if streams == 0 {
			t.Error("no stream was in flight at the signal")
		}
	})

	t.Run("one usage record for every complete answer, none lost at the stop", func(t *testing.T) {
		complete := map[string]int{}
		for _, o := range u.outcomes {
			if o.Class == Complete {
				complete[o.Tenant]++
			}
		}
		for name, n := range complete {
			tn := w.tenants[name]
			testutil.Eventually(t, 30*time.Second, "the ledger of "+name+" to hold what was answered", func() bool { return w.usageCount(tn.ID, "ok") >= n })
			if got := w.usageCount(tn.ID, "ok"); got != n {
				t.Errorf("tenant %s: %d complete answers and %d usage records", name, n, got)
			}
		}
	})

	t.Run("the quota is one quota across the upgrade", func(t *testing.T) {
		for _, name := range []string{"a", "b"} {
			tn := w.tenants[name]
			var admitted int
			var before, after []Outcome
			for _, o := range u.outcomes {
				if o.Tenant != name {
					continue
				}
				switch o.Class {
				case Complete:
					admitted++
				case Quota:
					if o.Started.Before(u.proc.StopSent) {
						before = append(before, o)
					} else {
						after = append(after, o)
					}
				}
			}
			if admitted != tn.Quota {
				t.Errorf("tenant %s: %d calls were admitted, the quota is %d", name, admitted, tn.Quota)
			}
			if name == "b" && (len(before) == 0 || len(after) == 0) {
				t.Errorf("tenant b was refused %d times before and %d after the upgrade, want both", len(before), len(after))
			}
			if len(before) > 0 && len(after) > 0 {
				if a, b := stableHeaders(before[0].Header), stableHeaders(after[len(after)-1].Header); a != b {
					t.Errorf("tenant %s: a refusal before the upgrade carries\n%s\nand one after it\n%s", name, a, b)
				}
			}
		}
	})

	t.Run("keys, session, revocation and issuer survive", func(t *testing.T) {
		p := u.newProc
		client := &http.Client{Timeout: 10 * time.Second}
		status := func(method, url string, hdr http.Header) int {
			req, err := http.NewRequest(method, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header = hdr
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		bearer := func(k string) http.Header { return http.Header{"Authorization": {"Bearer " + k}} }
		for name, tn := range w.tenants {
			if got := status("GET", "http://"+p.Admin+"/v1/me", bearer(readFileTrim(t, tn.Admin))); got != 200 {
				t.Errorf("the administrator key of tenant %s on the new release: %d, want 200", name, got)
			}
		}
		if got := status("GET", "http://"+p.Admin+"/v1/me", bearer(readFileTrim(t, w.admin))); got != 200 {
			t.Errorf("the platform key on the new release: %d, want 200", got)
		}
		if got := status("GET", "http://"+p.Admin+"/v1/me", http.Header{"Cookie": {"__Host-pandokos_session=" + w.session}}); got != 200 {
			t.Errorf("the session of the local account on the new release: %d, want 200", got)
		}
		if got := status("GET", "http://"+p.Admin+"/v1/me", bearer(w.revokedKey)); got != 401 {
			t.Errorf("the revoked key on the new release: %d, want 401", got)
		}
		// A token of the issuer maps to the service account of tenant c.
		token := w.idp.Sign("ES256", w.idp.Standard("svc", "pandokos-upgrade", 10*time.Minute).With("org", "org-c"))
		req, _ := http.NewRequest("POST", "http://"+p.Inference+"/openai/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"token"}]}`, fakeprovider.ModelBasic)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("a token of the issuer on the new release: %d, want 200", resp.StatusCode)
		}
		var principal string
		testutil.Eventually(t, 15*time.Second, "the usage record of the token", func() bool {
			err := w.db.Super(t).QueryRow(testCtx(), `SELECT principal_id FROM pandokos.usage_record WHERE tenant_id = $1::uuid AND principal_kind = 'token' ORDER BY started_at DESC LIMIT 1`, w.tenants["c"].ID).Scan(&principal)
			return err == nil
		})
		if principal != w.tenants["c"].SA {
			t.Errorf("the token acted as %q, want tenant c's service account %q", principal, w.tenants["c"].SA)
		}
	})

	t.Run("both chains verify, and rows sealed by the previous release verify under this one", func(t *testing.T) {
		p := u.newProc
		if code, out, errs := p.CLI(t, w.admin, "platform-audit", "verify"); code != 0 {
			t.Errorf("platform-audit verify: exit %d\n%s%s", code, out, errs)
		}
		kept := w.heads["platform"]
		export := filepath.Join(t.TempDir(), "platform.ndjson")
		if code, out, errs := p.CLI(t, w.admin, "platform-audit", "export", "--to-seq", fmt.Sprint(kept.Seq), "--out", export); code != 0 {
			t.Fatalf("platform-audit export: exit %d\n%s%s", code, out, errs)
		}
		if code, out, errs := p.CLI(t, w.admin, "platform-audit", "verify", "--file", export, "--expect-head", kept.Hash); code != 0 {
			t.Errorf("the platform's rows up to the kept head do not verify against it: exit %d\n%s%s", code, out, errs)
		}
		for name, tn := range w.tenants {
			kept := w.heads[tn.ID]
			if code, out, errs := p.CLI(t, tn.Admin, "audit", "verify", "--tenant", tn.ID); code != 0 {
				t.Errorf("audit verify of tenant %s: exit %d\n%s%s", name, code, out, errs)
			}
			file := filepath.Join(t.TempDir(), name+".ndjson")
			if code, out, errs := p.CLI(t, tn.Admin, "audit", "export", "--tenant", tn.ID, "--to-seq", fmt.Sprint(kept.Seq), "--out", file); code != 0 {
				t.Fatalf("audit export of tenant %s: exit %d\n%s%s", name, code, out, errs)
			}
			if code, out, errs := p.CLI(t, tn.Admin, "audit", "verify", "--tenant", tn.ID, "--file", file, "--expect-head", kept.Hash); code != 0 {
				t.Errorf("the rows of tenant %s up to the kept head do not verify against it: exit %d\n%s%s", name, code, out, errs)
			}
		}
	})

	t.Run("events exist for the usage after the migration and for none before it", func(t *testing.T) {
		for name, tn := range w.tenants {
			var events, after int
			testutil.Eventually(t, 30*time.Second, "the events of tenant "+name, func() bool {
				if err := w.db.Super(t).QueryRow(testCtx(), `SELECT count(*)::int FROM pandokos.usage_record WHERE tenant_id = $1::uuid AND recorded_at > $2`, tn.ID, u.migrated).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if err := w.db.Super(t).QueryRow(testCtx(), `SELECT count(*)::int FROM pandokos.outbox_event WHERE tenant_id = $1::uuid AND type = 'io.pandokos.usage.recorded.v1'`, tn.ID).Scan(&events); err != nil {
					t.Fatal(err)
				}
				return events >= after
			})
			if events != after {
				t.Errorf("tenant %s: %d usage events and %d usage records written after the migration", name, events, after)
			}
		}
	})

	t.Run("the key-encryption key is the same and nothing was re-wrapped", func(t *testing.T) {
		var activation, version string
		for _, l := range completeLogLines(u.newProc.Stdout.String(), "vault ready") {
			activation, _ = l["kek_activation"].(string)
			version, _ = l["kek_version"].(string)
		}
		if activation != "unchanged" || version == "" {
			t.Errorf("vault ready on the new release says kek_version %q and kek_activation %q, want unchanged", version, activation)
		}
		for _, l := range completeLogLines(w.proc.Stdout.String(), "vault ready") {
			if v, _ := l["kek_version"].(string); version != "" && v != version {
				t.Errorf("the key-encryption key version was %q on the previous release and is %q now", v, version)
			}
		}
		var rewrapped int
		if err := w.db.Super(t).QueryRow(testCtx(), `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE kek_version <> $1`, version).Scan(&rewrapped); err == nil && rewrapped != 0 {
			t.Errorf("%d data keys are wrapped under another version than the active one", rewrapped)
		}
	})

	t.Run("the interval without service is written to the report", func(t *testing.T) {
		if u.proc.Gap() <= 0 {
			t.Error("no interval was measured")
		}
		t.Logf("interval without service: %v; steps %v", u.proc.Gap().Round(time.Millisecond), stepMillis(u.proc.Steps))
	})
}

// stableHeaders is the headers of a refusal that a person reads, without the ones that differ by
// time or by request (the seconds until the reset among them), in one text.
func stableHeaders(h http.Header) string {
	var out []string
	for k, v := range h {
		switch http.CanonicalHeaderKey(k) {
		case "Date", "Content-Length", "X-Request-Id", "X-Pandokos-Request-Id", "Retry-After", "X-Pandokos-Limit-Reset":
			continue
		}
		out = append(out, k+": "+strings.Join(v, ","))
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func readFileTrim(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// completeLogLines decodes the complete JSON lines of text whose msg is msg (a trailing fragment is
// never parsed).
func completeLogLines(text, msg string) []map[string]any {
	i := strings.LastIndexByte(text, '\n')
	if i < 0 {
		return nil
	}
	var out []map[string]any
	for _, l := range strings.Split(text[:i], "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}
