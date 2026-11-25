//go:build drill

package drill

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// dataset writes the dataset of the figure case (T-1146): ledger rows by SQL, as a superuser, through
// the partitions that exist; audit rows only through the product, so that the chains are real. A
// world with no sizes asked for has none.
func (w *World) dataset() {
	w.T.Helper()
	if w.Opts.LedgerRows > 0 {
		defer w.phase(fmt.Sprintf("dataset: %d ledger rows", w.Opts.LedgerRows))()
		w.bulkLedger(w.Opts.LedgerRows)
	}
	if w.Opts.AuditRows > 0 {
		defer w.phase(fmt.Sprintf("dataset: %d audit rows", w.Opts.AuditRows))()
		w.bulkAudit(w.Opts.AuditRows)
	}
}

// bulkLedger inserts n usage records for the four tenants, written as the gateway writes them (the
// shape of test/e2e/upgrade_test.go), in the days before today so that no quota counts them, in
// chunks of 20000 rows. The insert is the one statement that the outbox trigger reads, so the events
// of the rows are written with them.
func (w *World) bulkLedger(n int) {
	w.T.Helper()
	var ids, sas []string
	for _, name := range worldTenants {
		ids = append(ids, "'"+w.Tenants[name].ID+"'")
		sas = append(sas, "'"+w.Tenants[name].serviceAccount+"'")
	}
	tenants, accounts := "ARRAY["+strings.Join(ids, ",")+"]::uuid[]", "ARRAY["+strings.Join(sas, ",")+"]::text[]"
	const chunk = 20000
	for done := 0; done < n; done += chunk {
		count := min(chunk, n-done)
		w.sql(Source, w.db, fmt.Sprintf(`INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, key_id,
				credential_id, provider, api_family, operation, outcome, duration_ms, input_source, output_source, input_tokens, cost_status)
			SELECT (%[1]s)[1 + g %% 4], pandokos.uuid_v7(),
			       now() - interval '2 days' - ((g %% 23) * interval '1 day') - (g * interval '1 millisecond'),
			       md5(g::text || '-%[3]d'), 'service_account', (%[2]s)[1 + g %% 4], '', gen_random_uuid(),
			       'openai', 'openai', 'chat.completions', 'ok', 1, 'provider', 'none', 10, 'none'
			  FROM generate_series(1, %[4]d) g`, tenants, accounts, done, count))
	}
}

// bulkAudit makes administration calls until the two kinds of audit log together hold n rows more than
// they did: a change of a tenant's display name, with the platform's key, round-robin over the tenants,
// over a handful of connections. Each call is a row of the tenant's chain through the product.
func (w *World) bulkAudit(n int) {
	w.T.Helper()
	have := func() int64 {
		return w.count(Source, w.db, `SELECT (SELECT count(*) FROM pandokos.audit_event) + (SELECT count(*) FROM pandokos.platform_audit_event)`)
	}
	target := have() + int64(n)
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 16}}
	key := readTrim(w.platform)
	var seq atomic.Int64
	for round := 0; have() < target; round++ {
		if round > 200 {
			w.T.Fatalf("the audit log did not reach %d rows after %d rounds of calls", target, round)
		}
		batch := int(min(target-have(), 2000))
		var wg sync.WaitGroup
		var failed atomic.Int64
		jobs := make(chan int, batch)
		for i := 0; i < batch; i++ {
			jobs <- i
		}
		close(jobs)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range jobs {
					k := seq.Add(1)
					tn := w.Tenants[worldTenants[int(k)%len(worldTenants)]]
					body := fmt.Sprintf(`{"display_name":"drill tenant %d"}`, k)
					req, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, "http://"+w.proc.Admin+"/v1/tenants/"+tn.ID, bytes.NewReader([]byte(body)))
					if err != nil {
						failed.Add(1)
						continue
					}
					req.Header.Set("Authorization", "Bearer "+key)
					req.Header.Set("Content-Type", "application/json")
					resp, err := client.Do(req)
					if err != nil {
						failed.Add(1)
						continue
					}
					_ = resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						failed.Add(1)
					}
				}
			}()
		}
		wg.Wait()
		if f := failed.Load(); f > int64(batch)/2 {
			w.T.Fatalf("%d of %d administration calls failed", f, batch)
		}
	}
	client.CloseIdleConnections()
}
