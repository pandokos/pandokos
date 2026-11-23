package tenantexport_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/tenantexport"
)

const tenantID = "0192f0f0-1111-7000-8000-000000000001"

// listKinds maps a kind of the export to the operation that lists it.
var listKinds = map[string]string{
	"service_account": "listServiceAccounts", "virtual_key": "listVirtualKeys", "credential": "listCredentials", "token_issuer": "listTokenIssuers",
	"subject_mapping": "listSubjectMappings", "platform_issuer": "listTenantPlatformIssuers", "limit": "listLimits", "platform_limit": "listTenantPlatformLimits",
	"limit_event": "listLimitEvents", "price_list_version": "listPriceListVersions", "member": "listMembers", "role": "listRoles", "support_action": "listSupportActions",
}

// fakeAPI answers the reads of an export from canned data, as the administration API would: pages of
// two items with a cursor, an NDJSON stream for the ledger (by window of time) and the log, and the
// four reads of the settings. It records every request.
type fakeAPI struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	items    map[string][]string // by operation: the JSON of each item, in the order the API gives them
	requests []string            // method, path and query of every request
	usage    []string            // the "from" of every request of the ledger
	forbid   map[string]string   // by operation: the status to answer instead of the data
	cut      map[string]int      // by operation: close the connection after this many lines of a stream
	onFirst  func()              // called at the first request of the audit head
	ops      []adminapi.Operation
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, items: map[string][]string{}, forbid: map[string]string{}, cut: map[string]int{}, ops: adminapi.Operations}
	for kind, op := range listKinds {
		for i := 0; i < 5; i++ { // five items: three pages of two, two and one
			f.items[op] = append(f.items[op], fmt.Sprintf(`{"id":"%s-%d","name":"%s item %d","n":%d.50}`, kind, i, kind, i, i))
		}
	}
	f.items["exportAuditEvents"] = []string{`{"seq":1,"action":"a.b"}`, `{"seq":2,"action":"c.d"}`, `{"seq":3,"action":"e.f"}`}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) client() *adminapi.Client {
	return &adminapi.Client{BaseURL: f.srv.URL, HTTP: f.srv.Client(), Key: func() string { return "k" }}
}

func (f *fakeAPI) opOf(r *http.Request) (adminapi.Operation, bool) {
	for _, op := range f.ops {
		if op.Method != r.Method {
			continue
		}
		re := regexp.MustCompile("^" + regexp.MustCompile(`\\\{[a-z_]+\\\}`).ReplaceAllString(regexp.QuoteMeta(op.Path), `[^/]+`) + "$")
		if re.MatchString(r.URL.Path) {
			return op, true
		}
	}
	return adminapi.Operation{}, false
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	op, ok := f.opOf(r)
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	forbid, cut := f.forbid[op.ID], f.cut[op.ID]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if forbid != "" {
		status, _ := strconv.Atoi(forbid)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"type":"urn:pandokos:problem:forbidden","title":"Forbidden","status":%d,"code":"forbidden","detail":"The caller may not do this.","request_id":"0123456789abcdef0123456789abcdef"}`, status)
		return
	}
	switch op.ID {
	case "getTenantTimeZone", "getActiveCurrency", "getMetadataKeys", "getModelRestriction":
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"%s":"answer of %s"}`, op.ID, op.ID)
	case "getAuditHead":
		if f.onFirst != nil {
			f.onFirst()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"seq":3,"hash":"` + strings.Repeat("ab", 32) + `","anchor_seq":1,"anchor_hash":"` + strings.Repeat("00", 32) + `"}`))
	case "exportUsageRecords":
		f.mu.Lock()
		f.usage = append(f.usage, r.URL.Query().Get("from")+".."+r.URL.Query().Get("to"))
		n := len(f.usage)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, `{"window":%d,"from":%q}`+"\n", n, r.URL.Query().Get("from"))
	case "exportAuditEvents":
		w.Header().Set("Content-Type", "application/x-ndjson")
		for i, line := range f.items[op.ID] {
			if cut > 0 && i == cut {
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, err := hj.Hijack()
					if err == nil {
						_ = conn.Close()
					}
				}
				return
			}
			_, _ = w.Write([]byte(line + "\n"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
	default:
		items := f.items[op.ID]
		from := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			from, _ = strconv.Atoi(c)
		}
		to := min(from+2, len(items))
		w.Header().Set("Content-Type", "application/json")
		body := `{"items":[` + strings.Join(items[from:to], ",") + `]`
		if to < len(items) {
			body += `,"next_cursor":"` + strconv.Itoa(to) + `"`
		}
		_, _ = w.Write([]byte(body + "}"))
	}
}

func (f *fakeAPI) countRequests(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.Contains(r, substr) {
			n++
		}
	}
	return n
}

var fixedNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func export(t *testing.T, f *fakeAPI, dir string, usageFrom time.Time) (tenantexport.Manifest, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return tenantexport.Write(ctx, f.client(), tenantID, dir, tenantexport.Options{UsageFrom: usageFrom, Now: func() time.Time { return fixedNow }, PandokosVersion: "v0.2.0-test"})
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// TestWriter is T-1001: the files, the manifest, the order, the windows of usage, the directory that
// appears at the end, and what a refusal or a failure leaves.
func TestWriter(t *testing.T) {
	t.Run("the files of the export", func(t *testing.T) {
		f := newFakeAPI(t)
		dir := filepath.Join(t.TempDir(), "export")
		usageFrom := fixedNow.AddDate(0, 0, -70)
		f.onFirst = func() { // the head is read before the log: the directory does not exist yet
			if _, err := os.Stat(dir); err == nil {
				t.Error("the directory exists before the end of the export")
			}
			if _, err := os.Stat(dir + ".partial"); err != nil {
				t.Errorf("no %s.partial while the export runs: %v", filepath.Base(dir), err)
			}
		}
		m, err := export(t, f, dir, usageFrom)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dir + ".partial"); err == nil {
			t.Error("the partial directory is left after a complete export")
		}
		// A file for each kind of the table, one object per line for each item of each page, in the API's order.
		byName := map[string]tenantexport.ManifestFile{}
		for _, mf := range m.Files {
			byName[mf.Name] = mf
		}
		for _, k := range tenantexport.Kinds {
			mf, ok := byName[k.File]
			if !ok || mf.Kind != k.Name {
				t.Errorf("the manifest has no entry %s of kind %s: %+v", k.File, k.Name, mf)
				continue
			}
			path := filepath.Join(dir, k.File)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s: %v", k.File, err)
				continue
			}
			sum := sha256.Sum256(raw)
			if mf.SHA256 != hex.EncodeToString(sum[:]) {
				t.Errorf("%s: the manifest's SHA-256 is not the file's", k.File)
			}
			switch op := listKinds[k.Name]; {
			case op != "":
				got := lines(t, path)
				want := f.items[op]
				if strings.Join(got, "\n") != strings.Join(want, "\n") || mf.Records != int64(len(want)) {
					t.Errorf("%s holds %v (%d records), want the items of every page in the API's order %v", k.File, got, mf.Records, want)
				}
			case k.Name == "audit_event":
				if got, want := lines(t, path), f.items["exportAuditEvents"]; strings.Join(got, "\n") != strings.Join(want, "\n") || mf.Records != 3 {
					t.Errorf("%s holds %v, want %v", k.File, got, want)
				}
			case k.Name == "usage_record":
				if len(lines(t, path)) != 3 || mf.Records != 3 {
					t.Errorf("%s holds %d lines (%d records), want one per request of a window: 3", k.File, len(lines(t, path)), mf.Records)
				}
			case k.Name == "settings":
				var settings map[string]json.RawMessage
				if err := json.Unmarshal(raw, &settings); err != nil || len(settings) != 4 || mf.Records != 1 {
					t.Errorf("%s: %v, %d members, %d records; want one object of four members", k.File, err, len(settings), mf.Records)
				}
			}
		}
		if len(m.Files) != len(tenantexport.Kinds) {
			t.Errorf("the manifest lists %d files for %d kinds", len(m.Files), len(tenantexport.Kinds))
		}
		// The manifest on disk is the one returned, and says what §5.11 says.
		var onDisk tenantexport.Manifest
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &onDisk); err != nil {
			t.Fatal(err)
		}
		if onDisk.Format != "pandokos-tenant-export" || onDisk.Version != 1 || onDisk.TenantID != tenantID || onDisk.PandokosVersion != "v0.2.0-test" ||
			!onDisk.CreatedAt.Equal(fixedNow) || onDisk.AuditHead.Seq != 3 || onDisk.AuditHead.AnchorSeq != 1 || onDisk.AuditHead.Hash != strings.Repeat("ab", 32) ||
			!onDisk.UsageFrom.Equal(usageFrom) || !onDisk.UsageTo.Equal(fixedNow) || len(onDisk.Files) != len(m.Files) {
			t.Errorf("manifest.json: %+v", onDisk)
		}
		for _, mf := range m.Files {
			if _, err := os.Stat(filepath.Join(dir, mf.Name)); err != nil {
				t.Errorf("the manifest names %s: %v", mf.Name, err)
			}
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != len(tenantexport.Kinds)+1 {
			t.Errorf("%d files in the directory, want the %d of the kinds and the manifest", len(entries), len(tenantexport.Kinds))
		}
		// The log is read to the head named in the manifest.
		if n := f.countRequests("audit-events/export?"); n != 1 || f.countRequests("to_seq=3") != 1 {
			t.Errorf("the log was exported by %d requests (%d to the head): one request up to the head read first", n, f.countRequests("to_seq=3"))
		}
		// Every page of every list was asked for, through the cursor.
		for _, op := range listKinds {
			if f.countRequests("cursor=2") == 0 || f.countRequests("cursor=4") == 0 {
				t.Errorf("%s: the later pages were not requested (%v)", op, f.requests)
				break
			}
		}
		// The file permissions: a private directory.
		if st, err := os.Stat(dir); err != nil || st.Mode().Perm()&0o077 != 0 {
			t.Errorf("the export directory has mode %v: it holds a tenant's data", st.Mode().Perm())
		}
	})

	t.Run("seventy days of usage are three requests", func(t *testing.T) {
		f := newFakeAPI(t)
		dir := filepath.Join(t.TempDir(), "export")
		usageFrom := fixedNow.AddDate(0, 0, -70)
		if _, err := export(t, f, dir, usageFrom); err != nil {
			t.Fatal(err)
		}
		if len(f.usage) != 3 {
			t.Fatalf("%d requests for 70 days of usage, want 3 windows of at most 31 days: %v", len(f.usage), f.usage)
		}
		var prevTo string
		for i, w := range f.usage {
			from, to, _ := strings.Cut(w, "..")
			ft, err1 := time.Parse(time.RFC3339, from)
			tt, err2 := time.Parse(time.RFC3339, to)
			if err1 != nil || err2 != nil || tt.Sub(ft) > 31*24*time.Hour || !tt.After(ft) {
				t.Errorf("window %d %q is not a span of at most 31 days", i, w)
			}
			if i == 0 && !ft.Equal(usageFrom) {
				t.Errorf("the first window starts %s, want %s", from, usageFrom.Format(time.RFC3339))
			}
			if i > 0 && from != prevTo {
				t.Errorf("window %d starts at %s, the one before ended at %s: a gap or an overlap", i, from, prevTo)
			}
			prevTo = to
		}
		if prevTo != fixedNow.Format(time.RFC3339) {
			t.Errorf("the last window ends at %s, want now (%s)", prevTo, fixedNow.Format(time.RFC3339))
		}
		// The default is sixty months, in windows of 31 days.
		g := newFakeAPI(t)
		if _, err := export(t, g, filepath.Join(t.TempDir(), "export"), time.Time{}); err != nil {
			t.Fatal(err)
		}
		if n := len(g.usage); n < 58 || n > 62 {
			t.Errorf("%d windows for the default 60 months, want about 59 to 60 of 31 days", n)
		}
	})

	t.Run("a refusal names the permission and leaves no manifest", func(t *testing.T) {
		for _, c := range []struct{ op, perm string }{{"listLimits", "limit:read"}, {"listCredentials", "credential:read"}, {"exportAuditEvents", "audit:read"}, {"getMetadataKeys", "usage:read"}} {
			f := newFakeAPI(t)
			f.forbid[c.op] = "403"
			dir := filepath.Join(t.TempDir(), "export")
			_, err := export(t, f, dir, fixedNow.AddDate(0, 0, -40))
			if err == nil || !strings.Contains(err.Error(), c.perm) {
				t.Errorf("%s answers 403: %v, want an error that names %s", c.op, err, c.perm)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); statErr == nil {
				t.Errorf("%s answers 403: a manifest was written", c.op)
			}
			if _, statErr := os.Stat(dir); statErr == nil {
				t.Errorf("%s answers 403: the directory exists", c.op)
			}
			if _, statErr := os.Stat(filepath.Join(dir+".partial", "manifest.json")); statErr == nil {
				t.Errorf("%s answers 403: the partial directory holds a manifest", c.op)
			}
		}
	})

	t.Run("a failure in the middle of a stream leaves the partial directory without a manifest", func(t *testing.T) {
		f := newFakeAPI(t)
		f.cut["exportAuditEvents"] = 2
		dir := filepath.Join(t.TempDir(), "export")
		_, err := export(t, f, dir, fixedNow.AddDate(0, 0, -40))
		if err == nil {
			t.Fatal("a stream cut in the middle ended well")
		}
		if _, statErr := os.Stat(dir); statErr == nil {
			t.Error("the directory exists after a failed export")
		}
		if _, statErr := os.Stat(dir + ".partial"); statErr != nil {
			t.Errorf("no partial directory after a failed export: %v", statErr)
		}
		if _, statErr := os.Stat(filepath.Join(dir+".partial", "manifest.json")); statErr == nil {
			t.Error("a manifest is in the partial directory")
		}
		// The export is not resumed into a directory that exists.
		other := filepath.Join(t.TempDir(), "taken")
		if err := os.Mkdir(other, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := export(t, newFakeAPI(t), other, fixedNow.AddDate(0, 0, -40)); err == nil {
			t.Error("an export wrote into a directory that exists")
		}
	})

	t.Run("no secret is asked for", func(t *testing.T) {
		f := newFakeAPI(t)
		if _, err := export(t, f, filepath.Join(t.TempDir(), "export"), fixedNow.AddDate(0, 0, -40)); err != nil {
			t.Fatal(err)
		}
		for _, r := range f.requests {
			if strings.Contains(r, "/secret") || strings.Contains(r, "reset-password") || !strings.HasPrefix(r, "GET ") {
				t.Errorf("the export made the request %s: it only reads, and no secret", r)
			}
		}
		if bytes.Contains([]byte(strings.Join(f.requests, "\n")), []byte("platform-keys")) {
			t.Error("the export reads the platform's keys")
		}
	})
}
