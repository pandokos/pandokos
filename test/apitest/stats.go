package apitest

import (
	"reflect"
	"testing"
)

// AdminCounts is what the administration handler counts for the metrics (sprint 6.1): refused
// grants, support actions opened and ended by cause, and the OIDC sign-ins by result.
type AdminCounts struct {
	GrantsRefused int64
	SupportOpened int64
	SupportEnded  map[string]int64 // by cause: closed, ended
	OIDC          map[string]int64 // by result: ok, refused, unavailable
	// ExportsComplete and ExportsAborted count the exports (M46) by kind: usage, audit, platform_audit
	// and, since sprint 6.3, deleted_tenant_audit.
	ExportsComplete, ExportsAborted map[string]int64
}

// AdminStats calls Stats() of the handler admin.New returned and reads the snapshot by field name:
// GrantsRefused and SupportOpened (integers), SupportEnded (a map of integers by cause) and SignIn,
// a snapshot of the type account.SignInStats whose Attempts map has the keys Method and Result. It
// fails the test while the handler has no Stats method or the fields are not there.
func (a *API) AdminStats(tb testing.TB) AdminCounts {
	tb.Helper()
	m := reflect.ValueOf(a.handler).MethodByName("Stats")
	if !m.IsValid() || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 {
		tb.Fatalf("the administration handler (%T) has no Stats() method", a.handler)
	}
	v := m.Call(nil)[0]
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		tb.Fatalf("Stats() returns %s, want a struct", v.Type())
	}
	field := func(name string) reflect.Value {
		f := v.FieldByName(name)
		if !f.IsValid() {
			tb.Fatalf("the snapshot %s has no field %s", v.Type(), name)
		}
		return f
	}
	out := AdminCounts{GrantsRefused: field("GrantsRefused").Int(), SupportOpened: field("SupportOpened").Int(), SupportEnded: map[string]int64{}, OIDC: map[string]int64{}}
	for _, k := range field("SupportEnded").MapKeys() {
		out.SupportEnded[k.String()] = field("SupportEnded").MapIndex(k).Int()
	}
	out.ExportsComplete, out.ExportsAborted = map[string]int64{}, map[string]int64{}
	for name, into := range map[string]map[string]int64{"ExportsComplete": out.ExportsComplete, "ExportsAborted": out.ExportsAborted} {
		f := field(name)
		for _, k := range f.MapKeys() {
			into[k.String()] = f.MapIndex(k).Int()
		}
	}
	attempts := field("SignIn").FieldByName("Attempts")
	if !attempts.IsValid() {
		tb.Fatalf("the field SignIn of %s has no Attempts", v.Type())
	}
	for _, k := range attempts.MapKeys() {
		if k.FieldByName("Method").String() == "oidc" {
			out.OIDC[k.FieldByName("Result").String()] += attempts.MapIndex(k).Int()
		}
	}
	return out
}
