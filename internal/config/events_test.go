package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestEventKeys is T-1047: the nine keys of sprint 6.4, section 5.15: defaults, bounds, the
// environment names and serve only.
func TestEventKeys(t *testing.T) {
	envOf := envOfKeys()
	load := func(t *testing.T, kv ...string) (*Config, []Warning, error) {
		t.Helper()
		src := validSource(t)
		for i := 0; i < len(kv); i += 2 {
			src.Environ = append(src.Environ, envOf[kv[i]]+"="+kv[i+1])
		}
		return Load(src)
	}
	type key struct{ name, env, typ, def string }
	want := []key{
		{"events.source", "PANDOKOS_EVENTS_SOURCE", "string", "urn:pandokos"},
		{"events.retention", "PANDOKOS_EVENTS_RETENTION", "duration", "168h"},
		{"events.relay.interval", "PANDOKOS_EVENTS_RELAY_INTERVAL", "duration", "1s"},
		{"events.relay.workers", "PANDOKOS_EVENTS_RELAY_WORKERS", "integer", "4"},
		{"events.relay.batch_size", "PANDOKOS_EVENTS_RELAY_BATCH_SIZE", "integer", "100"},
		{"events.delivery.timeout", "PANDOKOS_EVENTS_DELIVERY_TIMEOUT", "duration", "10s"},
		{"events.delivery.max_attempts", "PANDOKOS_EVENTS_DELIVERY_MAX_ATTEMPTS", "integer", "10"},
		{"events.delivery.backoff_max", "PANDOKOS_EVENTS_DELIVERY_BACKOFF_MAX", "duration", "15m"},
		{"events.webhook.private_hosts", "PANDOKOS_EVENTS_WEBHOOK_PRIVATE_HOSTS", "list", ""},
	}

	t.Run("the nine keys, their environment names, types and defaults", func(t *testing.T) {
		var got []key
		for _, k := range Keys() {
			if strings.HasPrefix(k.Name, "events.") {
				if k.Description == "" {
					t.Errorf("%s has no description", k.Name)
				}
				got = append(got, key{k.Name, k.Env, k.Type, k.Default})
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the events keys are\n%v\nwant\n%v", got, want)
		}
		c, ws, err := load(t)
		if err != nil {
			t.Fatal(err)
		}
		e := c.Events
		if e.Source != "urn:pandokos" || time.Duration(e.Retention) != 168*time.Hour || time.Duration(e.Relay.Interval) != time.Second || e.Relay.Workers != 4 || e.Relay.BatchSize != 100 ||
			time.Duration(e.Delivery.Timeout) != 10*time.Second || e.Delivery.MaxAttempts != 10 || time.Duration(e.Delivery.BackoffMax) != 15*time.Minute || len(e.Webhook.PrivateHosts) != 0 {
			t.Errorf("the defaults: %+v", e)
		}
		for _, w := range ws {
			if strings.HasPrefix(w.Key, "events.") {
				t.Errorf("a warning with the defaults: %+v", w)
			}
		}
	})

	t.Run("bounds", func(t *testing.T) {
		bad := map[string][]string{
			"events.source":                {"has space", "tab\there", strings.Repeat("a", 256), "ctl\x01", "bad%zz"},
			"events.retention":             {"23h", "2161h", "25h30m", "90m", "0s", "-24h", "a week"},
			"events.relay.interval":        {"99ms", "61s", "1m1s", "0s", "-1s", "fast"},
			"events.relay.workers":         {"0", "65", "-1", "four", "1.5"},
			"events.relay.batch_size":      {"0", "1001", "-1", "many"},
			"events.delivery.timeout":      {"999ms", "31s", "0s", "-5s", "slow"},
			"events.delivery.max_attempts": {"0", "101", "-3", "ten"},
			"events.delivery.backoff_max":  {"4s", "24h1s", "0s", "-1m", "never"},
			"events.webhook.private_hosts": {"127.0.0.1", "::1", "*.example.test", "host:8080", "Mixed Case With Space", strings.Repeat("a", 64) + ".test", "a..b", ""},
		}
		for k, values := range bad {
			for _, v := range values {
				if v == "" && k == "events.webhook.private_hosts" {
					continue // an empty list is the default
				}
				_, _, err := load(t, k, v)
				if !hasProblem(err, k) {
					t.Errorf("%s=%q: %v, want a problem on the key", k, v, err)
				}
				if err != nil && v != "" && len(v) > 2 && strings.Contains(err.Error(), v) {
					t.Errorf("%s=%q: the problem quotes the value: %v", k, v, err)
				}
			}
		}
		good := map[string]map[string]func(*Config) bool{
			"events.source":                {"https://pandokos.example.test/billing": func(c *Config) bool { return c.Events.Source == "https://pandokos.example.test/billing" }, "urn:x": func(c *Config) bool { return c.Events.Source == "urn:x" }},
			"events.retention":             {"24h": func(c *Config) bool { return time.Duration(c.Events.Retention) == 24*time.Hour }, "2160h": func(c *Config) bool { return time.Duration(c.Events.Retention) == 2160*time.Hour }},
			"events.relay.interval":        {"100ms": func(c *Config) bool { return time.Duration(c.Events.Relay.Interval) == 100*time.Millisecond }, "1m": func(c *Config) bool { return time.Duration(c.Events.Relay.Interval) == time.Minute }},
			"events.relay.workers":         {"1": func(c *Config) bool { return c.Events.Relay.Workers == 1 }, "64": func(c *Config) bool { return c.Events.Relay.Workers == 64 }},
			"events.relay.batch_size":      {"1": func(c *Config) bool { return c.Events.Relay.BatchSize == 1 }, "1000": func(c *Config) bool { return c.Events.Relay.BatchSize == 1000 }},
			"events.delivery.timeout":      {"1s": func(c *Config) bool { return time.Duration(c.Events.Delivery.Timeout) == time.Second }, "30s": func(c *Config) bool { return time.Duration(c.Events.Delivery.Timeout) == 30*time.Second }},
			"events.delivery.max_attempts": {"1": func(c *Config) bool { return c.Events.Delivery.MaxAttempts == 1 }, "100": func(c *Config) bool { return c.Events.Delivery.MaxAttempts == 100 }},
			"events.delivery.backoff_max":  {"5s": func(c *Config) bool { return time.Duration(c.Events.Delivery.BackoffMax) == 5*time.Second }, "24h": func(c *Config) bool { return time.Duration(c.Events.Delivery.BackoffMax) == 24*time.Hour }},
			"events.webhook.private_hosts": {
				"hooks.internal.example, Billing.Example.TEST": func(c *Config) bool {
					return reflect.DeepEqual(c.Events.Webhook.PrivateHosts, []string{"hooks.internal.example", "billing.example.test"})
				},
			},
		}
		for k, cases := range good {
			for v, check := range cases {
				c, _, err := load(t, k, v)
				if err != nil || !check(c) {
					t.Errorf("%s=%q: %v %+v", k, v, err, c)
				}
			}
		}
		// At most 32 hosts.
		var hosts []string
		for i := 0; i < 33; i++ {
			hosts = append(hosts, "h"+string(rune('a'+i%26))+string(rune('a'+i/26))+".example.test")
		}
		if _, _, err := load(t, "events.webhook.private_hosts", strings.Join(hosts, ",")); !hasProblem(err, "events.webhook.private_hosts") {
			t.Errorf("33 hosts: %v", err)
		}
		if _, _, err := load(t, "events.webhook.private_hosts", strings.Join(hosts[:32], ",")); err != nil {
			t.Errorf("32 hosts: %v", err)
		}
	})

	t.Run("serve only: the other purposes ignore the values", func(t *testing.T) {
		kv := []string{}
		for _, k := range want {
			kv = append(kv, envOf[k.name]+"=not valid for anyone")
		}
		for name, src := range map[string]Source{"migrate": validMigrateSource(t), "init": validInitSource(t)} {
			src.Environ = append(src.Environ, kv...)
			_, ws := mustLoad(t, src)
			for _, w := range ws {
				if strings.HasPrefix(w.Key, "events.") {
					t.Errorf("%s warns about %s: %q", name, w.Key, w.Message)
				}
			}
		}
	})

	t.Run("the problems name the keys and all are reported together", func(t *testing.T) {
		_, _, err := load(t, "events.retention", "1h", "events.relay.workers", "0", "events.delivery.timeout", "1m")
		for _, k := range []string{"events.retention", "events.relay.workers", "events.delivery.timeout"} {
			if !hasProblem(err, k) {
				t.Errorf("no problem for %s: %v", k, err)
			}
		}
	})
}
