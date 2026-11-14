package config

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLedgerConfiguration is T-504: the four ledger keys of sprint 3.2, section 5.11.
func TestLedgerConfiguration(t *testing.T) {
	envOf := envOfKeys()
	keys := map[string]struct{ env, typ, def string }{
		"ledger.flush_interval":         {"PANDOKOS_LEDGER_FLUSH_INTERVAL", "duration", "50ms"},
		"ledger.batch_size":             {"PANDOKOS_LEDGER_BATCH_SIZE", "integer", "500"},
		"ledger.buffer_size":            {"PANDOKOS_LEDGER_BUFFER_SIZE", "integer", "10000"},
		"ledger.shutdown_flush_timeout": {"PANDOKOS_LEDGER_SHUTDOWN_FLUSH_TIMEOUT", "duration", "10s"},
	}

	t.Run("the keys, their environment names, types and defaults", func(t *testing.T) {
		found := 0
		for _, k := range Keys() {
			w, ok := keys[k.Name]
			if !ok {
				continue
			}
			found++
			if k.Env != w.env || k.Type != w.typ || k.Default != w.def || k.Description == "" {
				t.Errorf("%s: %+v, want env %s type %s default %q", k.Name, k, w.env, w.typ, w.def)
			}
		}
		if found != len(keys) {
			t.Errorf("%d of the four keys are in Keys()", found)
		}
		c, _ := mustLoad(t, validSource(t))
		if time.Duration(c.Ledger.FlushInterval) != 50*time.Millisecond || c.Ledger.BatchSize != 500 || c.Ledger.BufferSize != 10000 ||
			time.Duration(c.Ledger.ShutdownFlushTimeout) != 10*time.Second {
			t.Errorf("a loaded configuration without the keys: %+v", c.Ledger)
		}
	})

	load := func(t *testing.T, kv ...string) (*Config, error) {
		t.Helper()
		src := validSource(t)
		for i := 0; i < len(kv); i += 2 {
			src.Environ = append(src.Environ, envOf[kv[i]]+"="+kv[i+1])
		}
		c, _, err := Load(src)
		return c, err
	}

	t.Run("flush_interval is 1 ms to 10 s", func(t *testing.T) {
		for v, ok := range map[string]bool{"1ms": true, "50ms": true, "10s": true, "999us": false, "0s": false, "-1s": false, "10001ms": false, "11s": false, "soon": false, "100": false} {
			c, err := load(t, "ledger.flush_interval", v)
			if ok != (err == nil) {
				t.Errorf("%q: %v", v, err)
			}
			if ok && err == nil {
				if got, _ := time.ParseDuration(v); time.Duration(c.Ledger.FlushInterval) != got {
					t.Errorf("%q parsed as %v", v, c.Ledger.FlushInterval)
				}
			}
			if !ok && !hasProblem(err, "ledger.flush_interval") {
				t.Errorf("%q: problems %v", v, problemKeys(err))
			}
		}
	})

	t.Run("batch_size is 1 to 5000", func(t *testing.T) {
		for v, ok := range map[string]bool{"1": true, "500": true, "5000": true, "0": false, "-1": false, "5001": false, "many": false, "1.5": false} {
			c, err := load(t, "ledger.batch_size", v)
			if ok != (err == nil) {
				t.Errorf("%q: %v", v, err)
			}
			if ok && err == nil {
				if n, _ := strconv.Atoi(v); c.Ledger.BatchSize != n {
					t.Errorf("%q parsed as %d", v, c.Ledger.BatchSize)
				}
			}
			if !ok && !hasProblem(err, "ledger.batch_size") {
				t.Errorf("%q: problems %v", v, problemKeys(err))
			}
		}
	})

	t.Run("buffer_size is 100 to 1 000 000", func(t *testing.T) {
		for v, ok := range map[string]bool{"100": true, "10000": true, "1000000": true, "99": false, "0": false, "-5": false, "1000001": false, "big": false} {
			c, err := load(t, "ledger.buffer_size", v)
			if ok != (err == nil) {
				t.Errorf("%q: %v", v, err)
			}
			if ok && err == nil {
				if n, _ := strconv.Atoi(v); c.Ledger.BufferSize != n {
					t.Errorf("%q parsed as %d", v, c.Ledger.BufferSize)
				}
			}
			if !ok && !hasProblem(err, "ledger.buffer_size") {
				t.Errorf("%q: problems %v", v, problemKeys(err))
			}
		}
	})

	t.Run("buffer_size below batch_size is refused", func(t *testing.T) {
		if _, err := load(t, "ledger.buffer_size", "200", "ledger.batch_size", "300"); !hasProblem(err, "ledger.buffer_size") {
			t.Errorf("buffer 200 with batch 300: %v", err)
		}
		if _, err := load(t, "ledger.buffer_size", "300", "ledger.batch_size", "300"); err != nil {
			t.Errorf("buffer equal to batch: %v", err)
		}
		if _, err := load(t, "ledger.buffer_size", "100", "ledger.batch_size", "5000"); !hasProblem(err, "ledger.buffer_size") {
			t.Errorf("buffer 100 with batch 5000: %v", err)
		}
		// The default batch size is 500: a buffer of 100 alone is below it.
		if _, err := load(t, "ledger.buffer_size", "100"); !hasProblem(err, "ledger.buffer_size") {
			t.Errorf("buffer 100 with the default batch size: %v", err)
		}
	})

	t.Run("shutdown_flush_timeout is 1 s to 5 m", func(t *testing.T) {
		for v, ok := range map[string]bool{"1s": true, "10s": true, "5m": true, "999ms": false, "0s": false, "301s": false, "6m": false, "x": false} {
			c, err := load(t, "ledger.shutdown_flush_timeout", v)
			if ok != (err == nil) {
				t.Errorf("%q: %v", v, err)
			}
			if ok && err == nil {
				if got, _ := time.ParseDuration(v); time.Duration(c.Ledger.ShutdownFlushTimeout) != got {
					t.Errorf("%q parsed as %v", v, c.Ledger.ShutdownFlushTimeout)
				}
			}
			if !ok && !hasProblem(err, "ledger.shutdown_flush_timeout") {
				t.Errorf("%q: problems %v", v, problemKeys(err))
			}
		}
	})

	t.Run("a YAML file sets them too, and the environment wins", func(t *testing.T) {
		src := validSource(t)
		src.File = writeFile(t, "ledger:\n  flush_interval: 20ms\n  batch_size: 100\n  buffer_size: 1000\n  shutdown_flush_timeout: 30s\n")
		src.Environ = append(src.Environ, envOf["ledger.buffer_size"]+"=2000")
		c, _ := mustLoad(t, src)
		if time.Duration(c.Ledger.FlushInterval) != 20*time.Millisecond || c.Ledger.BatchSize != 100 || c.Ledger.BufferSize != 2000 || time.Duration(c.Ledger.ShutdownFlushTimeout) != 30*time.Second {
			t.Errorf("%+v", c.Ledger)
		}
	})

	t.Run("a problem never quotes the value", func(t *testing.T) {
		_, err := load(t, "ledger.batch_size", "synthetic-bad-value")
		if err == nil || strings.Contains(err.Error(), "synthetic-bad-value") {
			t.Errorf("%v", err)
		}
	})

	t.Run("the keys are read for serve only", func(t *testing.T) {
		for name, src := range map[string]Source{"init": validInitSource(t), "migrate": validMigrateSource(t)} {
			src.Environ = append(src.Environ, envOf["ledger.buffer_size"]+"=1", envOf["ledger.batch_size"]+"=0", envOf["ledger.flush_interval"]+"=never")
			if _, _, err := Load(src); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
}
