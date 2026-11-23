package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"testing"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
)

// The helpers of sprint 6.4: the event log of a tenant, destinations, and usage records written by
// the statement of the ledger writer.

// OutboxEvent is a row of outbox_event.
type OutboxEvent struct {
	Seq        int64
	ID         string
	Type       string
	Subject    string
	OccurredAt time.Time
	RecordedAt time.Time
	Data       json.RawMessage
}

// OutboxEvents returns the log of the tenant, oldest first, read as the superuser.
func (d *Database) OutboxEvents(tb testing.TB, id tenant.ID) []OutboxEvent {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	rows, err := d.Super(tb).Query(ctx, `SELECT seq, id::text, type, subject, occurred_at, recorded_at, data::text
		FROM pandokos.outbox_event WHERE tenant_id = $1::uuid ORDER BY seq`, id.String())
	if err != nil {
		tb.Fatalf("read the event log: %v", err)
	}
	defer rows.Close()
	var out []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		var data string
		if err := rows.Scan(&e.Seq, &e.ID, &e.Type, &e.Subject, &e.OccurredAt, &e.RecordedAt, &data); err != nil {
			tb.Fatal(err)
		}
		e.Data = json.RawMessage(data)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}
	return out
}

// AgeOutbox moves recorded_at of every event of the tenant back by by, as the superuser with the
// append-only trigger of the log disabled for the statement (the way AgeDeletion moves a deletion):
// retention reads that column. It returns the number of events moved.
func (d *Database) AgeOutbox(tb testing.TB, id tenant.ID, by time.Duration) int {
	tb.Helper()
	return d.ageOutbox(tb, id, by, "")
}

// AgeOutboxUpTo is AgeOutbox for the events whose seq is at most upTo.
func (d *Database) AgeOutboxUpTo(tb testing.TB, id tenant.ID, by time.Duration, upTo int64) int {
	tb.Helper()
	return d.ageOutbox(tb, id, by, fmt.Sprintf(" AND seq <= %d", upTo))
}

func (d *Database) ageOutbox(tb testing.TB, id tenant.ID, by time.Duration, extra string) int {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	tx, err := d.Super(tb).Begin(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE pandokos.outbox_event DISABLE TRIGGER outbox_event_append_only`); err != nil {
		tb.Fatalf("age the log: %v", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE pandokos.outbox_event SET recorded_at = recorded_at - make_interval(secs => $2::float8)
		WHERE tenant_id = $1::uuid`+extra, id.String(), by.Seconds())
	if err != nil {
		tb.Fatalf("age the log: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE pandokos.outbox_event ENABLE TRIGGER outbox_event_append_only`); err != nil {
		tb.Fatalf("age the log: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		tb.Fatal(err)
	}
	return int(tag.RowsAffected())
}

// NewWebhookSecret returns a signing secret in the text form of the design: "whsec_" and the
// standard, padded base64 of 32 random bytes. The helpers that seed a destination make it here, so
// that they do not depend on the package under test.
func NewWebhookSecret(tb testing.TB) string {
	tb.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(b)
}

// SeedEventDestination creates an enabled destination for every type with a new signing secret,
// sealed under the tenant's data key with the key of the test database, as the administration does
// (the data key is created when the tenant has none). It returns the ID of the destination and the
// secret text. The destination starts at the head of the log.
func (d *Database) SeedEventDestination(tb testing.TB, id tenant.ID, url string) (destID, secretText string) {
	tb.Helper()
	return d.SeedEventDestinationNamed(tb, id, "dest-"+seedSuffix(tb), url)
}

// SeedEventDestinationNamed is SeedEventDestination with the name of the destination.
func (d *Database) SeedEventDestinationNamed(tb testing.TB, id tenant.ID, name, url string, types ...string) (destID, secretText string) {
	tb.Helper()
	b, err := os.ReadFile(d.KEKFile(tb))
	if err != nil {
		tb.Fatal(err)
	}
	p, err := vault.NewLocalProvider(secret.New(strings.TrimSpace(string(b))))
	if err != nil {
		tb.Fatal(err)
	}
	sealer, err := vault.NewSealer(p)
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := ctx30()
	defer cancel()
	destID = newUUID(tb)
	secretText = NewWebhookSecret(tb)
	err = d.admin(tb).WithTenant(ctx, id, func(c context.Context, tx store.Tx) error {
		if _, _, err := sealer.EnsureDataKey(c, tx, id); err != nil {
			return err
		}
		sealed, err := sealer.SealNamed(c, tx, id, vault.KindWebhookSecret, destID, secret.New(secretText))
		if err != nil {
			return err
		}
		_, err = queries.New(tx).CreateEventDestination(c, queries.CreateEventDestinationParams{
			ID: destID, Name: name, Url: url, EventTypes: nonNil(types), Enabled: true,
			Secret: sealed.Secret, SecretKeyVersion: sealed.KeyVersion,
		})
		return err
	})
	if err != nil {
		tb.Fatalf("seed an event destination: %v", err)
	}
	return destID, secretText
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// SeedUsageRecords inserts n usage records of the tenant in one statement, the statement of the
// ledger writer (InsertUsageRecords) on the gateway role in a session of the tenant, and returns
// their IDs in the order of their start. The partition of the current month must exist.
func (d *Database) SeedUsageRecords(tb testing.TB, id tenant.ID, n int) []string {
	tb.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	sa := newUUID(tb)
	rows := make([]map[string]any, n)
	ids := make([]string, n)
	for i := range rows {
		ids[i] = newUUID(tb)
		rows[i] = map[string]any{
			"id": ids[i], "started_at": now.Add(time.Duration(i-n) * time.Millisecond).Format("2006-01-02T15:04:05.000000Z07:00"),
			"request_id": strings.ReplaceAll(newUUID(tb), "-", ""), "trace_id": "",
			"principal_kind": "service_account", "principal_id": sa, "key_id": "", "provider": "openai", "api_family": "openai",
			"operation": "chat.completions", "endpoint": "", "model": "gpt-4o-mini", "upstream_model": "", "outcome": "ok",
			"error_code": "", "duration_ms": 10, "input_source": "provider", "output_source": "provider",
			"input_tokens": 10, "output_tokens": 5, "other_units": map[string]int64{}, "provider_usage": map[string]int64{},
			"cost_status": "none", "cost_reason": "", "cost_fallbacks": []string{}, "metadata": map[string]string{}, "metadata_dropped": 0,
		}
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := ctx30()
	defer cancel()
	err = d.Open(tb, store.RoleGateway).WithTenant(ctx, id, func(c context.Context, tx store.Tx) error {
		got, err := queries.New(tx).InsertUsageRecords(c, string(payload))
		if err == nil && int(got) != n {
			err = fmt.Errorf("%d rows inserted, want %d", got, n)
		}
		return err
	})
	if err != nil {
		tb.Fatalf("seed usage records: %v", err)
	}
	return ids
}

// newUUID returns a random UUID (version 4) in its text form.
func newUUID(tb testing.TB) string {
	tb.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
