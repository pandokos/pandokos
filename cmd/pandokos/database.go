package main

import (
	"time"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
)

// connOptions turns the database configuration into the plain values the store takes, for one
// database user.
func connOptions(c *config.Config, user string, password secret.Secret) store.ConnOptions {
	d := c.Database
	return store.ConnOptions{
		Host: d.Host, Port: d.Port, Database: d.Name, User: user, Password: password,
		TLSMode: d.TLS.Mode, TLSCAFile: d.TLS.CAFile, ConnectTimeout: time.Duration(d.ConnectTimeout),
	}
}
