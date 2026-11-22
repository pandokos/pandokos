package admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/test/apitest"
)

// dbNow is the clock of the database: the instant of a change is computed from it.
func dbNow(t *testing.T, api *apitest.API) time.Time {
	t.Helper()
	var now time.Time
	if err := api.Super(t).QueryRow(ctx(t), `SELECT now()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}

// monthEnd is the end of the month of the zone that contains now: the instant a change made now takes effect.
func monthEnd(t *testing.T, zone string, now time.Time) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	cal, err := limits.NewCalendar(loc)
	if err != nil {
		t.Fatal(err)
	}
	return cal.Of(limits.PerMonth, now).End
}

// TestTenantTimeZone is T-717: the routes of both scopes, the change rule of section 5.7, the audit
// rows and the notification of the rule cache.
func TestTenantTimeZone(t *testing.T) {
	w := newIW(t)
	api := w.api
	path := "/v1/tenants/" + w.a.ID + "/time-zone"
	seen := seenIDs(t, api)
	actor := *me(t, api, w.ka)
	super := api.Super(t)
	listener := api.DB.Super(t)
	if _, err := listener.Exec(ctx(t), "LISTEN pandokos_cache"); err != nil {
		t.Fatal(err)
	}
	notified := func() []string {
		var got []string
		for {
			c, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			n, err := listener.WaitForNotification(c)
			cancel()
			if err != nil {
				return got
			}
			got = append(got, n.Payload)
		}
	}
	notified()
	get := func(t *testing.T, key string) adminapi.TenantTimeZone {
		t.Helper()
		return decodeInto[adminapi.TenantTimeZone](t, w.do(t, key, "GET", path, nil), 200)
	}
	put := func(t *testing.T, key, zone string) *apitest.Response {
		t.Helper()
		return w.do(t, key, "PUT", path, map[string]any{"time_zone": zone})
	}
	column := func() string {
		t.Helper()
		var s string
		if err := super.QueryRow(ctx(t), `SELECT time_zone || '|' || time_zone_previous || '|' || COALESCE(time_zone_effective_from::text, '') FROM pandokos.tenant WHERE id = $1::uuid`, w.a.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("the default: no zone of its own, the zone of the installation in force", func(t *testing.T) {
		for _, key := range []string{w.ka, w.plat} {
			if key == w.plat {
				continue // the platform key has no limit:read; it uses the tenant resource below
			}
			z := get(t, key)
			if z.TimeZone != "" || z.InForce != "UTC" || z.EffectiveFrom != nil {
				t.Errorf("%+v, want the default zone (UTC) in force and nothing pending", z)
			}
		}
		tn := decodeInto[adminapi.Tenant](t, w.do(t, w.plat, "GET", "/v1/tenants/"+w.a.ID, nil), 200)
		if tn.TimeZone != "" || tn.TimeZoneInForce != "UTC" || tn.TimeZoneEffectiveFrom != nil {
			t.Errorf("tenant %+v", tn)
		}
	})

	t.Run("a change takes effect at the start of the next month of the zone in force", func(t *testing.T) {
		now := dbNow(t, api)
		want := monthEnd(t, "UTC", now)
		r := put(t, w.ka, "Europe/Rome")
		z := decodeInto[adminapi.TenantTimeZone](t, r, 200)
		if z.TimeZone != "Europe/Rome" || z.InForce != "UTC" || z.EffectiveFrom == nil || !z.EffectiveFrom.Equal(want) {
			t.Fatalf("%+v, want Europe/Rome from %s, UTC in force", z, want)
		}
		if got := get(t, w.ka); got.TimeZone != "Europe/Rome" || got.InForce != "UTC" || got.EffectiveFrom == nil || !got.EffectiveFrom.Equal(want) {
			t.Errorf("get: %+v", got)
		}
		if got := notified(); len(got) != 1 || got[0] != "limits:"+w.a.ID {
			t.Errorf("notifications %v, want limits:%s", got, w.a.ID)
		}
		rows := newRows(t, api, seen)
		if len(rows) != 1 {
			t.Fatalf("%d audit rows, want 1: %+v", len(rows), rows)
		}
		row := rows[0]
		if row.Log != "tenant" || row.TenantID != w.a.ID || row.Action != "tenant.time_zone_update" || row.ResourceType != "tenant" || row.ResourceID != w.a.ID ||
			row.ActorType != "service_account" || row.ActorID != actor.ID || row.ActorName != actor.Name || row.KeyID != actor.KeyID || row.Outcome != "success" ||
			row.RequestID != r.Header.Get("X-Pandokos-Request-Id") {
			t.Errorf("audit row %+v", row)
		}
		if row.Detail["from"] != "" || row.Detail["to"] != "Europe/Rome" {
			t.Errorf("detail %v, want from the default to Europe/Rome", row.Detail)
		}
		if _, ok := row.Detail["in_force"]; !ok {
			t.Errorf("detail %v has no in_force", row.Detail)
		}
		if row.Detail["effective_from"] == nil {
			t.Errorf("detail %v has no effective_from", row.Detail)
		}
	})

	t.Run("a second change before the instant replaces the zone and keeps the instant", func(t *testing.T) {
		before := get(t, w.ka)
		z := decodeInto[adminapi.TenantTimeZone](t, put(t, w.ka, "Asia/Tokyo"), 200)
		if z.TimeZone != "Asia/Tokyo" || z.InForce != "UTC" || z.EffectiveFrom == nil || !z.EffectiveFrom.Equal(*before.EffectiveFrom) {
			t.Errorf("%+v, want Asia/Tokyo from %s", z, before.EffectiveFrom)
		}
		rows := newRows(t, api, seen)
		if len(rows) != 1 || rows[0].Action != "tenant.time_zone_update" || rows[0].Detail["from"] != "Europe/Rome" || rows[0].Detail["to"] != "Asia/Tokyo" {
			t.Errorf("rows %+v", rows)
		}
		notified()
	})

	t.Run("setting the value already set changes and audits nothing", func(t *testing.T) {
		before := column()
		snap := api.SnapshotTenant(t, w.a.ID)
		if z := decodeInto[adminapi.TenantTimeZone](t, put(t, w.ka, "Asia/Tokyo"), 200); z.TimeZone != "Asia/Tokyo" {
			t.Errorf("%+v", z)
		}
		if column() != before || api.SnapshotTenant(t, w.a.ID) != snap {
			t.Error("the row changed")
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("wrote %+v", rows)
		}
		if got := notified(); len(got) != 0 {
			t.Errorf("notified %v", got)
		}
	})

	t.Run("the zone in force again withdraws the pending change", func(t *testing.T) {
		z := decodeInto[adminapi.TenantTimeZone](t, put(t, w.ka, ""), 200) // the default is the zone in force
		if z.TimeZone != "" || z.InForce != "UTC" || z.EffectiveFrom != nil {
			t.Errorf("%+v", z)
		}
		if got := column(); got != "||" {
			t.Errorf("columns %q after the withdrawal", got)
		}
		rows := newRows(t, api, seen)
		if len(rows) != 1 || rows[0].Action != "tenant.time_zone_update" || rows[0].Detail["from"] != "Asia/Tokyo" || rows[0].Detail["to"] != "" {
			t.Errorf("rows %+v", rows)
		}
		notified()
	})

	t.Run("after the instant the zone in force is the new one and a change waits for the next month of it", func(t *testing.T) {
		api.DB.SetTenantZone(t, tenantID(t, w.a.ID), "Europe/Rome", "", time.Now().Add(-24*time.Hour)) // a change that took effect yesterday
		if z := get(t, w.ka); z.TimeZone != "Europe/Rome" || z.InForce != "Europe/Rome" || z.EffectiveFrom != nil && z.EffectiveFrom.After(time.Now()) {
			t.Errorf("after the instant: %+v, want Europe/Rome in force and nothing pending", z)
		}
		newRows(t, api, seen)
		now := dbNow(t, api)
		want := monthEnd(t, "Europe/Rome", now)
		z := decodeInto[adminapi.TenantTimeZone](t, put(t, w.ka, "America/New_York"), 200)
		if z.TimeZone != "America/New_York" || z.InForce != "Europe/Rome" || z.EffectiveFrom == nil || !z.EffectiveFrom.Equal(want) {
			t.Errorf("%+v, want New York from %s, Rome in force", z, want)
		}
		if got := column(); got[:len("America/New_York|Europe/Rome|")] != "America/New_York|Europe/Rome|" {
			t.Errorf("columns %q", got)
		}
		// Back to the zone in force: withdrawn, not a change to a zone that is already in force.
		if z := decodeInto[adminapi.TenantTimeZone](t, put(t, w.ka, "Europe/Rome"), 200); z.TimeZone != "Europe/Rome" || z.InForce != "Europe/Rome" || z.EffectiveFrom != nil {
			t.Errorf("withdrawn: %+v", z)
		}
		newRows(t, api, seen)
	})

	t.Run("the instant for zones whose clocks change around a month start", func(t *testing.T) {
		for _, zone := range []string{"Atlantic/Azores", "America/Havana", "Africa/Casablanca", "Australia/Lord_Howe", "America/Sao_Paulo", "Pacific/Kiritimati"} {
			c, key := api.Tenant(t, "zone-"+time.Now().Format("150405.000")[7:]+"-"+zone[len(zone)-3:])
			_ = c
			// The tenant starts in the zone and asks for the default: the change is pending from the end of the month of the zone.
			api.DB.SetTenantZone(t, tenantID(t, c.ID), zone, "", time.Time{})
			now := dbNow(t, api)
			want := monthEnd(t, zone, now)
			z := decodeInto[adminapi.TenantTimeZone](t, w.do(t, key, "PUT", "/v1/tenants/"+c.ID+"/time-zone", map[string]any{"time_zone": "UTC"}), 200)
			if z.EffectiveFrom == nil || !z.EffectiveFrom.Equal(want) || z.InForce != zone {
				t.Errorf("%s: %+v, want in force %s until %s", zone, z, zone, want)
			}
		}
		newRows(t, api, seen) // the rows of the tenants created above are not what the next subtest looks at
	})

	t.Run("an unknown zone and a malformed name are refused", func(t *testing.T) {
		snap := api.SnapshotTenant(t, w.a.ID)
		for _, bad := range []string{"Mars/Olympus", "europe/rome", "Europe/Rom", "Europe Rome", "../etc/passwd", "UTC+3", "Local", "x", "EST5EDT/extra/path/too/long"} {
			p := expect(t, put(t, w.ka, bad), 400, adminapi.ProblemCodeInvalidRequest)
			if !namesMember(p, "time_zone") {
				t.Errorf("%q: %+v", bad, p)
			}
		}
		for what, body := range map[string]any{"no member": map[string]any{}, "a number": map[string]any{"time_zone": 3}, "null": nil} {
			if r := w.do(t, w.ka, "PUT", path, body); r.Status != 400 && r.Status != 415 {
				t.Errorf("%s: %d\n%s", what, r.Status, r.Body)
			}
		}
		if api.SnapshotTenant(t, w.a.ID) != snap {
			t.Error("a refused change altered the tenant")
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("wrote %+v", rows)
		}
	})

	t.Run("the platform: createTenant and updateTenant", func(t *testing.T) {
		out := decodeInto[adminapi.TenantCreated](t, w.do(t, w.plat, "POST", "/v1/tenants", map[string]any{"name": "zoned", "time_zone": "Asia/Tokyo"}), 201)
		api := w.api
		if tn := out.Tenant; tn.TimeZone != "Asia/Tokyo" || tn.TimeZoneInForce != "Asia/Tokyo" || tn.TimeZoneEffectiveFrom != nil {
			t.Errorf("created with a zone: %+v, want it in force at once", tn)
		}
		rows := newRows(t, api, seen)
		var create *auditRow
		for i := range rows {
			if rows[i].Action == "tenant.create" {
				create = &rows[i]
			}
		}
		if create == nil || create.Log != "platform" || create.Detail["time_zone"] != "Asia/Tokyo" {
			t.Errorf("tenant.create rows %+v", rows)
		}
		expect(t, w.do(t, w.plat, "POST", "/v1/tenants", map[string]any{"name": "zoned-bad", "time_zone": "Mars/Olympus"}), 400, adminapi.ProblemCodeInvalidRequest)
		if l := decodeInto[adminapi.TenantList](t, w.do(t, w.plat, "GET", "/v1/tenants?name=zoned-bad", nil), 200); len(l.Items) != 0 {
			t.Errorf("a refused creation left a tenant")
		}

		now := dbNow(t, api)
		want := monthEnd(t, "Asia/Tokyo", now)
		newRows(t, api, seen)
		r := w.do(t, w.plat, "PATCH", "/v1/tenants/"+out.Tenant.ID, map[string]any{"time_zone": "Europe/Rome"})
		tn := decodeInto[adminapi.Tenant](t, r, 200)
		if tn.TimeZone != "Europe/Rome" || tn.TimeZoneInForce != "Asia/Tokyo" || tn.TimeZoneEffectiveFrom == nil || !tn.TimeZoneEffectiveFrom.Equal(want) {
			t.Errorf("updated: %+v, want Rome from %s", tn, want)
		}
		rows = newRows(t, api, seen)
		if len(rows) != 1 || rows[0].Action != "tenant.update" || rows[0].Log != "platform" {
			t.Fatalf("rows %+v", rows)
		}
		tz, _ := rows[0].Detail["time_zone"].(map[string]any)
		if tz["from"] != "Asia/Tokyo" || tz["to"] != "Europe/Rome" || rows[0].Detail["time_zone_effective_from"] == nil {
			t.Errorf("detail %v", rows[0].Detail)
		}
		// An update that leaves the zone as it is writes nothing about it.
		r = w.do(t, w.plat, "PATCH", "/v1/tenants/"+out.Tenant.ID, map[string]any{"time_zone": "Europe/Rome"})
		decodeInto[adminapi.Tenant](t, r, 200)
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("an unchanged zone wrote %+v", rows)
		}
		expect(t, w.do(t, w.plat, "PATCH", "/v1/tenants/"+out.Tenant.ID, map[string]any{"time_zone": "Mars/Olympus"}), 400, adminapi.ProblemCodeInvalidRequest)
	})
}

// TestTimeZonePermissions is T-718: both operations need their permission; they are refused to an
// inference key, to a key scoped without it and to a platform key; updateTenant with a zone needs
// tenant:write.
func TestTimeZonePermissions(t *testing.T) {
	w := newIW(t)
	api := w.api
	path := "/v1/tenants/" + w.a.ID + "/time-zone"
	mkKey := func(roles []string, scopes []string) string {
		sa := decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/service-accounts", map[string]any{"name": apitest.UniqueName("sa"), "roles": roles}), 201)
		body := map[string]any{"service_account_id": sa.ID}
		if scopes != nil {
			body["scopes"] = scopes
		}
		return decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/virtual-keys", body), 201).Secret
	}
	inference := mkKey([]string{"inference"}, nil)
	scopedNoWrite := mkKey([]string{"tenant-admin"}, []string{"limit:read"})
	scopedNoRead := mkKey([]string{"tenant-admin"}, []string{"limit:write"})
	scopedSetting := mkKey([]string{"tenant-admin"}, []string{"tenant_setting:write"}) // sprint 4.3: setting the time zone has a permission of its own
	snap := api.SnapshotTenant(t, w.a.ID)

	t.Run("getTenantTimeZone needs limit:read", func(t *testing.T) {
		for name, key := range map[string]string{"an inference key": inference, "a key scoped without it": scopedNoRead, "a platform key": w.plat} {
			expect(t, w.do(t, key, "GET", path, nil), 403, adminapi.ProblemCodeForbidden)
			_ = name
		}
		if r := w.do(t, scopedNoWrite, "GET", path, nil); r.Status != 200 {
			t.Errorf("a key scoped to limit:read: %d\n%s", r.Status, r.Body)
		}
		if r := w.do(t, w.ka, "GET", path, nil); r.Status != 200 {
			t.Errorf("the tenant administrator: %d", r.Status)
		}
	})

	t.Run("setTenantTimeZone needs tenant_setting:write", func(t *testing.T) {
		for _, key := range []string{inference, scopedNoWrite, scopedNoRead, w.plat} { // a key scoped to limit:write loses the setting (release note of sprint 4.3)
			expect(t, w.do(t, key, "PUT", path, map[string]any{"time_zone": "Asia/Tokyo"}), 403, adminapi.ProblemCodeForbidden)
		}
		if api.SnapshotTenant(t, w.a.ID) != snap {
			t.Error("a refused change altered the tenant")
		}
		if r := w.do(t, scopedSetting, "PUT", path, map[string]any{"time_zone": "Asia/Tokyo"}); r.Status != 200 {
			t.Errorf("a key scoped to tenant_setting:write: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("updateTenant with a zone needs tenant:write", func(t *testing.T) {
		for _, key := range []string{w.ka, inference} { // a tenant key has no tenant:write
			r := w.do(t, key, "PATCH", "/v1/tenants/"+w.a.ID, map[string]any{"time_zone": "Asia/Tokyo"})
			if r.Status != 403 && r.Status != 404 {
				t.Errorf("a tenant key on updateTenant: %d\n%s", r.Status, r.Body)
			}
		}
		if r := w.do(t, w.plat, "PATCH", "/v1/tenants/"+w.a.ID, map[string]any{"time_zone": "Europe/Rome"}); r.Status != 200 {
			t.Errorf("the platform key: %d\n%s", r.Status, r.Body)
		}
	})
}
