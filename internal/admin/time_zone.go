package admin

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// zoneInForce returns the zone name that is in force at now: the previous one while a change is
// pending, the current one otherwise ("" is the default of the installation).
func zoneInForce(current, previous string, from *time.Time, now time.Time) string {
	if from != nil && now.Before(*from) {
		return previous
	}
	return current
}

// zoneName resolves a zone name to the name of its calendar: the default zone is named.
func (s *server) zoneName(name string) string {
	if cal, err := s.limits.Zones.Calendar(name); err == nil {
		return cal.Zone()
	}
	return name
}

// tenantTimeZoneOut builds the resource of the time zone of a tenant.
func (s *server) tenantTimeZoneOut(zone, previous string, from *time.Time) adminapi.TenantTimeZone {
	now := s.now()
	out := adminapi.TenantTimeZone{TimeZone: zone, InForce: s.zoneName(zoneInForce(zone, previous, from, now))}
	if from != nil && now.Before(*from) {
		out.EffectiveFrom = utcPtr(from)
	}
	return out
}

// zoneChange is the result of setTimeZone.
type zoneChange struct {
	From, To  string     // the zone as set, before and after
	InForce   string     // the name of the zone in force
	Effective *time.Time // the instant of a pending change, nil when none
	Changed   bool
}

// setTimeZone applies the change rule of section 5.7 to the row of a tenant, in the session that
// holds it. The clock is the one of the database, which the trigger of the table compares with.
func (s *server) setTimeZone(ctx context.Context, q *queries.Queries, tenantID, zone string) (zoneChange, error) {
	zones := s.limits.Zones
	if zones == nil {
		return zoneChange{}, errors.New("admin: the time zone needs the zones of the installation")
	}
	cur, err := q.LockTenantTimeZone(ctx, tenantID)
	if isNoRowsErr(err) {
		return zoneChange{}, errNotFound
	}
	if err != nil {
		return zoneChange{}, err
	}
	res := zoneChange{From: cur.TimeZone, To: cur.TimeZone}
	pending := cur.TimeZoneEffectiveFrom != nil && cur.DbNow.Before(*cur.TimeZoneEffectiveFrom)
	inForce := cur.TimeZone
	if pending {
		inForce = cur.TimeZonePrevious
	}
	set := func(to, previous string, from *time.Time) error {
		if _, err := q.SetTenantTimeZone(ctx, queries.SetTenantTimeZoneParams{ID: tenantID, TimeZone: to, TimeZonePrevious: previous, TimeZoneEffectiveFrom: from}); err != nil {
			return err
		}
		res.Changed, res.To, res.Effective = true, to, utcPtr(from)
		return nil
	}
	switch {
	case zone == cur.TimeZone:
		if pending {
			res.Effective = utcPtr(cur.TimeZoneEffectiveFrom)
		}
	case zone == inForce:
		err = set(zone, "", nil)
	case pending:
		err = set(zone, cur.TimeZonePrevious, cur.TimeZoneEffectiveFrom)
	default:
		var cal *limits.Calendar
		if cal, err = zones.Calendar(inForce); err == nil {
			at := limits.NextChange(cal, cur.DbNow)
			err = set(zone, inForce, &at)
		}
	}
	if err != nil {
		return zoneChange{}, err
	}
	res.InForce = s.zoneName(inForce)
	return res, nil
}

// checkZone validates a zone name (400 on time_zone): "" is the default.
func (s *server) checkZone(zone string) error {
	if s.limits.Zones == nil {
		return errors.New("admin: the time zone needs the zones of the installation")
	}
	if _, err := s.limits.Zones.Calendar(zone); err != nil {
		return invalid("time_zone", "must name a time zone of the IANA database whose offsets are whole minutes")
	}
	return nil
}

// GetTenantTimeZone reads the time zone of a tenant.
func (s *server) GetTenantTimeZone(ctx context.Context, in adminapi.GetTenantTimeZoneInput) (*adminapi.TenantTimeZone, error) {
	var out adminapi.TenantTimeZone
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, _ store.Tx, q *queries.Queries, _ tenant.ID) error {
		row, err := q.GetTenantTimeZone(ctx)
		if err != nil {
			return err
		}
		out = s.tenantTimeZoneOut(row.TimeZone, row.TimeZonePrevious, row.TimeZoneEffectiveFrom)
		return nil
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SetTenantTimeZone sets the time zone of a tenant, in the session of the tenant.
func (s *server) SetTenantTimeZone(ctx context.Context, in adminapi.SetTenantTimeZoneInput) (*adminapi.TenantTimeZone, error) {
	if err := s.checkZone(in.Body.TimeZone); err != nil {
		return nil, err
	}
	var out adminapi.TenantTimeZone
	err := s.inTenantPath(ctx, in.Tenant, func(ctx context.Context, tx store.Tx, q *queries.Queries, tid tenant.ID) error {
		ch, err := s.setTimeZone(ctx, q, tid.String(), in.Body.TimeZone)
		if err != nil {
			return err
		}
		out = adminapi.TenantTimeZone{TimeZone: ch.To, InForce: ch.InForce, EffectiveFrom: ch.Effective}
		if !ch.Changed {
			return nil
		}
		return audit.RecordTenant(ctx, tx, tid, tenantEvent(ctx, "tenant.time_zone_update", "tenant", tid.String(), zoneDetail(ch)))
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// zoneDetail is the audit detail of a change of zone.
func zoneDetail(ch zoneChange) map[string]any {
	return map[string]any{"from": ch.From, "to": ch.To, "in_force": ch.InForce, "effective_from": fmtTime(ch.Effective)}
}
