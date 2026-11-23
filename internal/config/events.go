package config

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Events configures the event relay and the retention of events (sprint 6.4, ADR 0070 to 0072).
// Only serve reads and validates these settings.
type Events struct {
	// Source is the CloudEvents source attribute of every event of this installation.
	Source string `yaml:"source"`
	// Retention is how long events are kept, in whole hours; it also bounds how far back a
	// destination can be rewound.
	Retention Duration       `yaml:"retention"`
	Relay     EventsRelay    `yaml:"relay"`
	Delivery  EventsDelivery `yaml:"delivery"`
	Webhook   EventsWebhook  `yaml:"webhook"`
}

// EventsRelay configures the relay of the worker.
type EventsRelay struct {
	Interval  Duration `yaml:"interval"`
	Workers   int      `yaml:"workers"`
	BatchSize int      `yaml:"batch_size"`
}

// EventsDelivery configures one delivery: its timeout, the number of attempts before an event is
// parked as a dead letter, and the longest wait between two attempts.
type EventsDelivery struct {
	Timeout     Duration `yaml:"timeout"`
	MaxAttempts int      `yaml:"max_attempts"`
	BackoffMax  Duration `yaml:"backoff_max"`
}

// EventsWebhook configures where webhooks may go.
type EventsWebhook struct {
	PrivateHosts []string `yaml:"private_hosts"` // lower-cased by Load
}

// eventsKeys returns the keys of sprint 6.4, in the order of the fields of Events.
func eventsKeys() []keyDef {
	return []keyDef{
		serveDef("events.source", "string", "urn:pandokos", "The CloudEvents source of every event of this installation; a URI reference of 1 to 255 printable characters",
			setString(func(c *Config) *string { return &c.Events.Source })),
		serveDef("events.retention", "duration", "168h", "How long events are kept, and how far back a destination can be rewound; 24h to 2160h, whole hours",
			setDuration(func(c *Config) *Duration { return &c.Events.Retention })),
		serveDef("events.relay.interval", "duration", "1s", "Time between two passes of the event relay of the worker; 100ms to 1m",
			setDuration(func(c *Config) *Duration { return &c.Events.Relay.Interval })),
		serveDef("events.relay.workers", "integer", "4", "Destinations that this process delivers to at once; 1 to 64",
			setInt(func(c *Config) *int { return &c.Events.Relay.Workers })),
		serveDef("events.relay.batch_size", "integer", "100", "Events read for one destination in one round; 1 to 1000",
			setInt(func(c *Config) *int { return &c.Events.Relay.BatchSize })),
		serveDef("events.delivery.timeout", "duration", "10s", "Time allowed to one delivery request; 1s to 30s",
			setDuration(func(c *Config) *Duration { return &c.Events.Delivery.Timeout })),
		serveDef("events.delivery.max_attempts", "integer", "10", "Attempts to deliver an event before it is parked as a dead letter; 1 to 100",
			setInt(func(c *Config) *int { return &c.Events.Delivery.MaxAttempts })),
		serveDef("events.delivery.backoff_max", "duration", "15m", "Longest wait between two attempts to deliver an event; 5s to 24h",
			setDuration(func(c *Config) *Duration { return &c.Events.Delivery.BackoffMax })),
		serveDef("events.webhook.private_hosts", "list", "", "Host names, separated by commas in the environment, whose webhook URLs may resolve to private addresses, as tokens.private_hosts; at most 32 lower-case DNS names, no address literal, no wildcard",
			func(c *Config, v string) error { c.Events.Webhook.PrivateHosts = listValue(v); return nil }),
	}
}

func eventsDefaults() Events {
	return Events{
		Source:    "urn:pandokos",
		Retention: Duration(168 * time.Hour),
		Relay:     EventsRelay{Interval: Duration(time.Second), Workers: 4, BatchSize: 100},
		Delivery:  EventsDelivery{Timeout: Duration(10 * time.Second), MaxAttempts: 10, BackoffMax: Duration(15 * time.Minute)},
		Webhook:   EventsWebhook{PrivateHosts: []string{}},
	}
}

// validateEvents applies the rules of the events keys (sprint 6.4, section 5.15) and lower-cases
// the host names of events.webhook.private_hosts; only serve reads them. No message quotes a host
// name.
func validateEvents(c *Config) []Problem {
	var ps []Problem
	add := func(key, msg string) { ps = append(ps, Problem{Key: key, Message: msg}) }
	e := &c.Events
	between := func(key string, d Duration, lo, hi time.Duration, text string) {
		if v := time.Duration(d); v < lo || v > hi {
			add(key, "must be between "+text)
		}
	}
	if !validEventSource(e.Source) {
		add("events.source", "must be a URI reference of 1 to 255 printable characters")
	}
	between("events.retention", e.Retention, 24*time.Hour, 2160*time.Hour, "24h and 2160h")
	if d := time.Duration(e.Retention); d%time.Hour != 0 {
		add("events.retention", "must be a whole number of hours")
	}
	between("events.relay.interval", e.Relay.Interval, 100*time.Millisecond, time.Minute, "100ms and 1m")
	if n := e.Relay.Workers; n < 1 || n > 64 {
		add("events.relay.workers", "must be between 1 and 64")
	}
	if n := e.Relay.BatchSize; n < 1 || n > 1000 {
		add("events.relay.batch_size", "must be between 1 and 1000")
	}
	between("events.delivery.timeout", e.Delivery.Timeout, time.Second, 30*time.Second, "1s and 30s")
	if n := e.Delivery.MaxAttempts; n < 1 || n > 100 {
		add("events.delivery.max_attempts", "must be between 1 and 100")
	}
	between("events.delivery.backoff_max", e.Delivery.BackoffMax, 5*time.Second, 24*time.Hour, "5s and 24h")
	e.Webhook.PrivateHosts = validatePrivateHosts("events.webhook.private_hosts", e.Webhook.PrivateHosts, add)
	return ps
}

// validatePrivateHosts is the rule of tokens.private_hosts and of events.webhook.private_hosts: at
// most 32 lower-case DNS names, no address literal, no wildcard, no port. It returns the list with
// every entry trimmed and lower-cased.
func validatePrivateHosts(key string, in []string, add func(key, msg string)) []string {
	if len(in) > maxPrivateHosts {
		add(key, "must have at most 32 entries")
	}
	hosts := make([]string, len(in))
	for i, h := range in {
		hosts[i] = strings.ToLower(strings.TrimSpace(h))
		if !validDNSName(hosts[i]) {
			add(key, "entry "+strconv.Itoa(i+1)+" must be a lower-case DNS name: no address literal, no wildcard, no port")
		}
	}
	return hosts
}

// validEventSource reports whether s is a URI reference of 1 to 255 printable characters.
func validEventSource(s string) bool {
	if s == "" || len(s) > 255 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	_, err := url.Parse(s)
	return err == nil
}
