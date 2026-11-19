package admin_test

import (
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
)

// pw is the world of the tests of sprint 5.4: two tenants with an administrator each, the platform key,
// and a catalogue of one provider with a model for everybody, a model granted to tenant A only, a model
// that nobody was granted and a model that is retired.
type pw struct {
	*iw
	cat     apitest.Catalogue
	granted adminapi.Model // selected, granted to A
	retired adminapi.Model // available to all and retired
	idA     tenant.ID
	idB     tenant.ID
}

func newPW(t *testing.T) *pw {
	t.Helper()
	p := &pw{iw: newIW(t)}
	p.cat = p.api.Catalog(t)
	plat := p.api.Client(p.plat)
	granted, err := plat.CreateModel(ctx(t), adminapi.CreateModelInput{Body: adminapi.ModelCreate{
		Provider: p.cat.Provider.Name, Name: apitest.UniqueName("synthetic-granted"), Availability: adminapi.ModelAvailabilitySelected}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plat.GrantModel(ctx(t), adminapi.GrantModelInput{Model: granted.ID, Tenant: p.a.ID}); err != nil {
		t.Fatal(err)
	}
	p.granted = *granted
	retired, err := plat.CreateModel(ctx(t), adminapi.CreateModelInput{Body: adminapi.ModelCreate{
		Provider: p.cat.Provider.Name, Name: apitest.UniqueName("synthetic-retired"), Availability: adminapi.ModelAvailabilityAll}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plat.UpdateModel(ctx(t), adminapi.UpdateModelInput{Model: retired.ID, Body: adminapi.ModelUpdate{Status: adminapi.Some(adminapi.ModelStatusRetired)}}); err != nil {
		t.Fatal(err)
	}
	p.retired = *retired
	if p.idA, err = tenant.ParseID(p.a.ID); err != nil {
		t.Fatal(err)
	}
	if p.idB, err = tenant.ParseID(p.b.ID); err != nil {
		t.Fatal(err)
	}
	return p
}

// tpRates builds the rates of a body from category, threshold and rate triples.
func tpRates(kv ...string) []map[string]any {
	var out []map[string]any
	for i := 0; i+3 <= len(kv); i += 3 {
		th := 0
		for _, c := range kv[i+1] {
			th = th*10 + int(c-'0')
		}
		out = append(out, map[string]any{"category": kv[i], "threshold": th, "rate": kv[i+2]})
	}
	return out
}

// priceBody is the body of createPriceListVersion for a model: input 1 and output 2 unless rates are given.
func priceBody(model adminapi.Model, provider, currency string, rates []map[string]any, extra map[string]any) map[string]any {
	if rates == nil {
		rates = tpRates("input", "0", "1", "output", "0", "2")
	}
	body := map[string]any{"provider": provider, "model": model.Name, "currency": currency, "rates": rates}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func (p *pw) pricesPath(tn adminapi.Tenant) string { return "/v1/tenants/" + tn.ID + "/prices" }

// createPrice creates a version for tenant A with its key and decodes the 201.
func (p *pw) createPrice(t *testing.T, body map[string]any) adminapi.TenantPriceVersion {
	t.Helper()
	return decodeInto[adminapi.TenantPriceVersion](t, p.do(t, p.ka, "POST", p.pricesPath(p.a), body), 201)
}

// scheduled is an instant in the future that differs per call, in RFC 3339 with seconds.
func scheduled(hours int, offsetSeconds int) string {
	return time.Now().UTC().Add(time.Duration(hours)*time.Hour + time.Duration(offsetSeconds)*time.Second).Truncate(time.Second).Format(time.RFC3339)
}

// messageOf returns the message of the error of a field.
func messageOf(p adminapi.Problem, field string) string {
	for _, e := range p.Errors {
		if e.Field == field {
			return e.Message
		}
	}
	return ""
}
