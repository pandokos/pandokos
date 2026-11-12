package store

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func quoteValue(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

func tlsConfig(o ConnOptions) (*tls.Config, error) {
	switch o.TLSMode {
	case "disable":
		return nil, nil
	case "verify-full":
		c := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: o.Host}
		if o.TLSCAFile != "" {
			pem, err := os.ReadFile(o.TLSCAFile)
			if err != nil {
				return nil, fmt.Errorf("store: read the CA file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("store: the CA file contains no PEM certificate")
			}
			c.RootCAs = pool
		}
		return c, nil
	default:
		return nil, errors.New("store: the TLS mode must be verify-full or disable")
	}
}

func dsn(o ConnOptions) string {
	seconds := int(o.ConnectTimeout / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return "host=" + quoteValue(o.Host) +
		" port=" + strconv.Itoa(o.Port) +
		" dbname=" + quoteValue(o.Database) +
		" user=" + quoteValue(o.User) +
		" sslmode=disable connect_timeout=" + strconv.Itoa(seconds)
}

// connConfig builds the driver configuration. The password is never part of a string, and
// nothing that libpq environment variables can set survives.
func connConfig(o ConnOptions, applicationName string) (*pgx.ConnConfig, error) {
	tc, err := tlsConfig(o)
	if err != nil {
		return nil, err
	}
	cfg, err := pgx.ParseConfig(dsn(o))
	if err != nil {
		return nil, errors.New("store: invalid connection options") // the driver's text is not passed on
	}
	cfg.Host = o.Host
	cfg.Port = uint16(o.Port) //nolint:gosec // range checked by validate
	cfg.Database = o.Database
	cfg.User = o.User
	cfg.Password = o.Password.Reveal()
	cfg.TLSConfig = tc
	cfg.ConnectTimeout = o.ConnectTimeout
	cfg.Fallbacks = nil
	cfg.RuntimeParams = map[string]string{"application_name": applicationName}
	cfg.ValidateConnect = nil
	cfg.AfterConnect = nil
	cfg.KerberosSrvName = ""
	cfg.KerberosSpn = ""
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.StatementCacheCapacity = 0
	cfg.DescriptionCacheCapacity = 0
	return cfg, nil
}

func poolConfig(o Options) (*pgxpool.Config, error) {
	cc, err := connConfig(o.Conn, "pandokos-"+string(o.Role))
	if err != nil {
		return nil, err
	}
	pc, err := pgxpool.ParseConfig(dsn(o.Conn))
	if err != nil {
		return nil, errors.New("store: invalid connection options")
	}
	pc.ConnConfig = cc
	pc.MaxConns = int32(o.MaxConns) //nolint:gosec // range checked by validate
	pc.MinConns = 0
	pc.MaxConnLifetime = 30 * time.Minute
	pc.MaxConnIdleTime = 5 * time.Minute
	pc.HealthCheckPeriod = 30 * time.Second
	return pc, nil
}
