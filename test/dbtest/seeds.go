package dbtest

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/tenant"
)

// The seeds below write the subjects and the catalogue entries that a limit rule names, as the
// superuser, in plain SQL: a rule refers to them by foreign key (migration 0011), and the tests
// that build rules by hand need real ones.

func (d *Database) seedExec(tb testing.TB, what, sql string, args ...any) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	if _, err := d.Super(tb).Exec(ctx, sql, args...); err != nil {
		tb.Fatalf("%s: %v", what, err)
	}
}

func (d *Database) seedScalar(tb testing.TB, what, sql string, args ...any) string {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	var v string
	if err := d.Super(tb).QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		tb.Fatalf("%s: %v", what, err)
	}
	return v
}

// SeedServiceAccount inserts an active service account and returns its ID.
func (d *Database) SeedServiceAccount(tb testing.TB, id tenant.ID, name string) string {
	tb.Helper()
	return d.seedScalar(tb, "seed service account", `INSERT INTO pandokos.service_account (tenant_id, name)
		VALUES ($1::uuid, $2) RETURNING id::text`, id.String(), name)
}

// KeyOption changes what SeedVirtualKey inserts.
type KeyOption func(*keySeed)

type keySeed struct{ revoked bool }

// KeyRevoked revokes the key after it is inserted.
func KeyRevoked() KeyOption { return func(k *keySeed) { k.revoked = true } }

const base32Lower = "abcdefghijklmnopqrstuvwxyz234567"

// NewKeyID returns a well-formed key ID that is unique in practice: "t" and sixteen characters of
// the lower-case base32 alphabet.
func NewKeyID(tb testing.TB) string {
	tb.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	out := make([]byte, 0, 17)
	out = append(out, 't')
	for _, c := range b {
		out = append(out, base32Lower[int(c)%len(base32Lower)])
	}
	return string(out)
}

// SeedVirtualKey inserts a virtual key of a service account and returns its key ID (what a
// principal carries and what a rule's API subject_id is) and its row ID (what the rule's
// virtual_key_id holds). The secret is not usable.
func (d *Database) SeedVirtualKey(tb testing.TB, id tenant.ID, serviceAccountID string, opts ...KeyOption) (keyID, rowID string) {
	tb.Helper()
	var o keySeed
	for _, f := range opts {
		f(&o)
	}
	keyID = NewKeyID(tb)
	rowID = d.seedScalar(tb, "seed virtual key", `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash)
		VALUES ($1::uuid, $2::uuid, $3, decode(repeat('ab', 32), 'hex')) RETURNING id::text`, id.String(), serviceAccountID, keyID)
	if o.revoked {
		d.seedExec(tb, "revoke seeded key", `UPDATE pandokos.virtual_key SET revoked_at = now() WHERE id = $1::uuid`, rowID)
	}
	return keyID, rowID
}

var seedCounter atomic.Int64

func seedSuffix(tb testing.TB) string {
	tb.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return fmt.Sprintf("%x%d", b, seedCounter.Add(1))
}

// SeedTrustedIssuer inserts a trusted issuer of the tenant and returns its ID.
func (d *Database) SeedTrustedIssuer(tb testing.TB, id tenant.ID, name string) string {
	tb.Helper()
	s := seedSuffix(tb)
	return d.seedScalar(tb, "seed trusted issuer", `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience)
		VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`, id.String(), name, "https://idp-"+s+".example.test", "aud-"+s)
}

// SeedPlatformIssuer inserts a platform-level issuer, lists the tenants for it and returns its ID.
func (d *Database) SeedPlatformIssuer(tb testing.TB, name string, tenants ...tenant.ID) string {
	tb.Helper()
	s := seedSuffix(tb)
	pid := d.seedScalar(tb, "seed platform issuer", `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim)
		VALUES ($1, $2, $3, 'org') RETURNING id::text`, name, "https://platform-idp-"+s+".example.test", "aud-"+s)
	for _, t := range tenants {
		d.seedExec(tb, "seed platform issuer tenant", `INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value)
			VALUES ($1::uuid, $2::uuid, $2)`, pid, t.String())
	}
	return pid
}

// SeedSubjectMapping inserts a mapping of a subject of a trusted issuer of the tenant (or, when
// platform is true, of a platform issuer) to a service account, and returns its ID.
func (d *Database) SeedSubjectMapping(tb testing.TB, id tenant.ID, issuerID string, platform bool, serviceAccountID, subject string) string {
	tb.Helper()
	col := "trusted_issuer_id"
	if platform {
		col = "platform_issuer_id"
	}
	return d.seedScalar(tb, "seed subject mapping", `INSERT INTO pandokos.subject_mapping (tenant_id, `+col+`, subject, service_account_id)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid) RETURNING id::text`, id.String(), issuerID, subject, serviceAccountID)
}

// SeedProvider inserts an enabled provider of the OpenAI family and returns its ID.
func (d *Database) SeedProvider(tb testing.TB, name string) string {
	tb.Helper()
	return d.seedScalar(tb, "seed provider", `INSERT INTO pandokos.provider (name, api_family)
		VALUES ($1, 'openai') RETURNING id::text`, name)
}

// SeedModel inserts a model (available to all tenants) of a provider of the catalogue and returns
// its ID.
func (d *Database) SeedModel(tb testing.TB, provider, name string) string {
	tb.Helper()
	return d.seedScalar(tb, "seed model", `INSERT INTO pandokos.model (provider_id, name, upstream_id, availability)
		SELECT id, $2, $2, 'all' FROM pandokos.provider WHERE name = $1 RETURNING id::text`, provider, name)
}

// ModelID returns the ID of a model of the catalogue; ProviderID that of a provider.
func (d *Database) ModelID(tb testing.TB, provider, name string) string {
	tb.Helper()
	return d.seedScalar(tb, "model id", `SELECT m.id::text FROM pandokos.model m JOIN pandokos.provider p ON p.id = m.provider_id
		WHERE p.name = $1 AND m.name = $2`, provider, name)
}

// ProviderID returns the ID of a provider of the catalogue.
func (d *Database) ProviderID(tb testing.TB, name string) string {
	tb.Helper()
	return d.seedScalar(tb, "provider id", `SELECT id::text FROM pandokos.provider WHERE name = $1`, name)
}

// SeedPrice inserts a price version of a model with an input and an output rate. A zero validFrom
// is "now": the version is in force at once (a version cannot start in the past).
func (d *Database) SeedPrice(tb testing.TB, provider, model, currency string, validFrom time.Time) string {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	conn := d.Super(tb)
	var vid string
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var from any
		if !validFrom.IsZero() {
			from = validFrom.UTC()
		}
		if err := tx.QueryRow(ctx, `INSERT INTO pandokos.price_version (model_id, valid_from, currency, source)
			SELECT m.id, COALESCE($3::timestamptz, now()), $4, 'override'
			  FROM pandokos.model m JOIN pandokos.provider p ON p.id = m.provider_id WHERE p.name = $1 AND m.name = $2
			RETURNING id::text`, provider, model, from, currency).Scan(&vid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO pandokos.price_rate (price_version_id, category, threshold, rate)
			VALUES ($1::uuid, 'input', 0, 1), ($1::uuid, 'output', 0, 2)`, vid)
		return err
	})
	if err != nil {
		tb.Fatalf("seed price: %v", err)
	}
	return vid
}

// Rate is one rate of a seeded tenant price: per million units, as decimal text.
type Rate struct {
	Category  string
	Threshold int64
	Rate      string
}

// SeedTenantPrice inserts a price version of a tenant's list (the tenant's versions in one
// currency) for a model, with its rates in the same transaction, and returns its ID. Without rates
// it has `input` 1 and `output` 2, as SeedPrice does. A zero validFrom is "now": the version is in
// force at once (a version cannot start in the past).
func (d *Database) SeedTenantPrice(tb testing.TB, id tenant.ID, currency, provider, model string, validFrom time.Time, rates ...Rate) string {
	tb.Helper()
	if len(rates) == 0 {
		rates = []Rate{{Category: "input", Rate: "1"}, {Category: "output", Rate: "2"}}
	}
	ctx, cancel := ctx30()
	defer cancel()
	var vid string
	err := pgx.BeginFunc(ctx, d.Super(tb), func(tx pgx.Tx) error {
		var from any
		if !validFrom.IsZero() {
			from = validFrom.UTC()
		}
		if err := tx.QueryRow(ctx, `INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency)
			SELECT $1::uuid, m.id, COALESCE($4::timestamptz, now()), $5
			  FROM pandokos.model m JOIN pandokos.provider p ON p.id = m.provider_id WHERE p.name = $2 AND m.name = $3
			RETURNING id::text`, id.String(), provider, model, from, currency).Scan(&vid); err != nil {
			return err
		}
		for _, r := range rates {
			if _, err := tx.Exec(ctx, `INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, threshold, rate)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5::numeric)`, id.String(), vid, r.Category, r.Threshold, r.Rate); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatalf("seed tenant price: %v", err)
	}
	return vid
}

// SeedTenantPriceEnd inserts a version of kind end into a tenant's list: from validFrom the list
// no longer prices the model. It has no rates. A zero validFrom is "now".
func (d *Database) SeedTenantPriceEnd(tb testing.TB, id tenant.ID, currency, provider, model string, validFrom time.Time) string {
	tb.Helper()
	var from any
	if !validFrom.IsZero() {
		from = validFrom.UTC()
	}
	return d.seedScalar(tb, "seed tenant price end", `INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, kind, currency)
		SELECT $1::uuid, m.id, COALESCE($4::timestamptz, now()), 'end', $5
		  FROM pandokos.model m JOIN pandokos.provider p ON p.id = m.provider_id WHERE p.name = $2 AND m.name = $3
		RETURNING id::text`, id.String(), provider, model, from, currency)
}

// SetTenantCurrency changes the three active currency columns of a tenant on a superuser
// connection, in the platform scope. A zero effectiveFrom clears the pending instant; an instant in
// the past is a change that took effect. As SetTenantZone does, it writes the row with the triggers
// off and then notifies catalog:<tenant> and limits:<tenant>, which the triggers would have.
func (d *Database) SetTenantCurrency(tb testing.TB, id tenant.ID, currency, previous string, effectiveFrom time.Time) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	err := pgx.BeginFunc(ctx, d.Super(tb), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('app.scope', 'platform', true)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		var at any
		if !effectiveFrom.IsZero() {
			at = effectiveFrom.UTC()
		}
		if _, err := tx.Exec(ctx, `UPDATE pandokos.tenant SET active_currency = $2, active_currency_previous = $3, active_currency_effective_from = $4::timestamptz
			WHERE id = $1::uuid`, id.String(), currency, previous, at); err != nil {
			return err
		}
		for _, ch := range []string{"catalog:", "limits:"} {
			if _, err := tx.Exec(ctx, `SELECT pg_notify('pandokos_cache', $1)`, ch+id.String()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatalf("set tenant currency: %v", err)
	}
}

// SetTenantZone changes the three time zone columns of a tenant on a superuser connection, in the
// platform scope. A zero effectiveFrom clears the pending instant; an instant in the past is a change
// that took effect. The row is written with the triggers off (see below).
func (d *Database) SetTenantZone(tb testing.TB, id tenant.ID, zone, previous string, effectiveFrom time.Time) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	err := pgx.BeginFunc(ctx, d.Super(tb), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('app.scope', 'platform', true)`); err != nil {
			return err
		}
		// The row is written with the triggers off, as the state a sequence of changes and the passing
		// of time leave it in: the trigger of the table admits a change of the zone in force only
		// through a pending change whose instant lies in the future (T-697 tests that path).
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		var at any
		if !effectiveFrom.IsZero() {
			at = effectiveFrom.UTC()
		}
		_, err := tx.Exec(ctx, `UPDATE pandokos.tenant SET time_zone = $2, time_zone_previous = $3, time_zone_effective_from = $4::timestamptz
			WHERE id = $1::uuid`, id.String(), zone, previous, at)
		return err
	})
	if err != nil {
		tb.Fatalf("set tenant zone: %v", err)
	}
}

// Count runs a query that returns one integer, as the superuser, on a connection the database keeps
// for these helpers: a test that polls needs no connection of its own per call.
func (d *Database) Count(tb testing.TB, sql string, args ...any) int {
	tb.Helper()
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if d.s.helper == nil {
		d.s.helper = d.Super(d.s.root) // closed with the test that made the database, not with a subtest
	}
	ctx, cancel := ctx30()
	defer cancel()
	var n int
	if err := d.s.helper.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		tb.Fatalf("count: %v\n%s", err, sql)
	}
	return n
}

// SharedSuper returns the connection of the superuser that the database keeps for the helpers that
// poll (Count): one connection for the life of the database, for the tests that read or write as the
// owner many times. Do not close it.
func (d *Database) SharedSuper(tb testing.TB) *pgx.Conn {
	tb.Helper()
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if d.s.helper == nil {
		d.s.helper = d.Super(d.s.root)
	}
	return d.s.helper
}

// SeedTenants inserts n active tenants named prefix-1 to prefix-n in one statement and returns
// their IDs.
func (d *Database) SeedTenants(tb testing.TB, prefix string, n int) []tenant.ID {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	conn := d.Super(tb)
	rows, err := conn.Query(ctx, `INSERT INTO pandokos.tenant (name) SELECT $1 || '-' || g FROM generate_series(1, $2::int) g RETURNING id::text`, prefix, n)
	if err != nil {
		tb.Fatalf("seed tenants: %v", err)
	}
	var ids []tenant.ID
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			tb.Fatal(err)
		}
		id, err := tenant.ParseID(s)
		if err != nil {
			tb.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if _, err := conn.Exec(ctx, `UPDATE pandokos.tenant SET state = 'active' WHERE name LIKE $1 || '-%'`, prefix); err != nil {
		tb.Fatalf("activate tenants: %v", err)
	}
	return ids
}

// LockTable takes an ACCESS EXCLUSIVE lock on a table of schema pandokos in a transaction of its
// own, so that every statement that touches the table waits, and returns the function that releases
// it (also called at cleanup).
func (d *Database) LockTable(tb testing.TB, table string) (release func()) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	tx, err := d.Super(tb).Begin(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE `+pgx.Identifier{"pandokos", table}.Sanitize()+` IN ACCESS EXCLUSIVE MODE`); err != nil {
		tb.Fatalf("lock %s: %v", table, err)
	}
	var once sync.Once
	release = func() { once.Do(func() { _ = tx.Rollback(context.Background()) }) }
	tb.Cleanup(release)
	return release
}

// SeedTenantPriceStarted inserts a version of a tenant's list that started `ago` before now, with the
// triggers off: the state the passing of time leaves, which the guard of the table refuses for every
// role. Without rates it has `input` 1 and `output` 2.
func (d *Database) SeedTenantPriceStarted(tb testing.TB, id tenant.ID, currency, provider, model string, ago time.Duration, rates ...Rate) string {
	tb.Helper()
	if len(rates) == 0 {
		rates = []Rate{{Category: "input", Rate: "1"}, {Category: "output", Rate: "2"}}
	}
	ctx, cancel := ctx30()
	defer cancel()
	var vid string
	err := pgx.BeginFunc(ctx, d.Super(tb), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency)
			SELECT $1::uuid, m.id, now() - make_interval(secs => $4::float8), $5
			  FROM pandokos.model m JOIN pandokos.provider p ON p.id = m.provider_id WHERE p.name = $2 AND m.name = $3
			RETURNING id::text`, id.String(), provider, model, ago.Seconds(), currency).Scan(&vid); err != nil {
			return err
		}
		for _, r := range rates {
			if _, err := tx.Exec(ctx, `INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, threshold, rate)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5::numeric)`, id.String(), vid, r.Category, r.Threshold, r.Rate); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatalf("seed a started tenant price: %v", err)
	}
	return vid
}
