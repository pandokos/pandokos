package admin_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/outbox"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/testutil"
)

// ew is a world of the event tests: an API (with idempotency keys) whose handler accepts http for the host localhost, a
// tenant and its owner.
type ew struct {
	api    *apitest.API
	tenant adminapi.Tenant
	owner  *apitest.Caller
	tid    string
}

func newEW(t *testing.T) *ew {
	t.Helper()
	// The API has the idempotency store: the creation and the rotation take an Idempotency-Key.
	api := apitest.NewPeople(t, apitest.PeopleConfig{Admin: func(o *admin.Options) {
		o.Events.Source = "urn:pandokos"
		o.Events.PrivateHosts = []string{"localhost"}
	}})
	ten, _ := api.Tenant(t, apitest.UniqueName("events"))
	return &ew{api: api, tenant: ten, owner: api.Owner(t, ten.ID), tid: ten.ID}
}

func (w *ew) path(rest string) string { return "/v1/tenants/" + w.tid + "/event-destinations" + rest }

// create makes a destination as the owner and returns the response (201 with the secret).
func (w *ew) create(t *testing.T, name, url string, types ...string) adminapi.EventDestinationCreated {
	t.Helper()
	body := map[string]any{"name": name, "url": url, "enabled": true}
	if len(types) > 0 {
		body["event_types"] = types
	}
	return decode[adminapi.EventDestinationCreated](t, w.owner.Do(t, "POST", w.path(""), body, nil), 201)
}

func (w *ew) get(t *testing.T, id string) adminapi.EventDestination {
	t.Helper()
	return decode[adminapi.EventDestination](t, w.owner.Do(t, "GET", w.path("/"+id), nil, nil), 200)
}

// startRelay runs a relay of the worker role on the database of the API until the test ends. It
// reaches receivers on localhost.
func (w *ew) startRelay(t *testing.T, timeout time.Duration) *outbox.Relay {
	t.Helper()
	g, err := egress.New(egress.Options{ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.CloseIdleConnections)
	r := &outbox.Relay{
		DB: w.api.DB.Open(t, store.RoleWorker), Provider: w.api.KeyProvider, Source: "urn:pandokos", Interval: 20 * time.Millisecond, Workers: 4, BatchSize: 100, MaxAttempts: 10,
		BackoffMax: 15 * time.Minute, Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "warn"),
		Deliverer: &outbox.Deliverer{Transport: g.Transport, PrivateHosts: []string{"localhost"}, Timeout: timeout, UserAgent: "pandokos/test", Now: time.Now},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

func idem(key string) http.Header { return http.Header{"Idempotency-Key": {key}} }

func contains(haystack string, needles ...string) []string {
	var found []string
	for _, n := range needles {
		if n != "" && strings.Contains(haystack, n) {
			found = append(found, n)
		}
	}
	return found
}
