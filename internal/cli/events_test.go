package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/outbox"
)

// The fake's methods for the twelve operations of sprint 6.4.
func (f *fakeAPI) ListEventDestinations(_ context.Context, in adminapi.ListEventDestinationsInput) (*adminapi.EventDestinationList, error) {
	return pick[adminapi.EventDestinationList](f, "listEventDestinations"), f.rec("listEventDestinations", in)
}
func (f *fakeAPI) CreateEventDestination(_ context.Context, in adminapi.CreateEventDestinationInput) (*adminapi.EventDestinationCreated, error) {
	return pick[adminapi.EventDestinationCreated](f, "createEventDestination"), f.rec("createEventDestination", in)
}
func (f *fakeAPI) GetEventDestination(_ context.Context, in adminapi.GetEventDestinationInput) (*adminapi.EventDestination, error) {
	return pick[adminapi.EventDestination](f, "getEventDestination"), f.rec("getEventDestination", in)
}
func (f *fakeAPI) UpdateEventDestination(_ context.Context, in adminapi.UpdateEventDestinationInput) (*adminapi.EventDestination, error) {
	return pick[adminapi.EventDestination](f, "updateEventDestination"), f.rec("updateEventDestination", in)
}
func (f *fakeAPI) DeleteEventDestination(_ context.Context, in adminapi.DeleteEventDestinationInput) (*adminapi.NoContent, error) {
	return &adminapi.NoContent{}, f.rec("deleteEventDestination", in)
}
func (f *fakeAPI) ListEventDeadLetters(_ context.Context, in adminapi.ListEventDeadLettersInput) (*adminapi.EventDeadLetterList, error) {
	return pick[adminapi.EventDeadLetterList](f, "listEventDeadLetters"), f.rec("listEventDeadLetters", in)
}
func (f *fakeAPI) ReplayEventDeadLetters(_ context.Context, in adminapi.ReplayEventDeadLettersInput) (*adminapi.EventDeadLetterReplayed, error) {
	return pick[adminapi.EventDeadLetterReplayed](f, "replayEventDeadLetters"), f.rec("replayEventDeadLetters", in)
}
func (f *fakeAPI) RewindEventDestination(_ context.Context, in adminapi.RewindEventDestinationInput) (*adminapi.EventDestination, error) {
	return pick[adminapi.EventDestination](f, "rewindEventDestination"), f.rec("rewindEventDestination", in)
}
func (f *fakeAPI) RotateEventDestinationSecret(_ context.Context, in adminapi.RotateEventDestinationSecretInput) (*adminapi.EventDestinationCreated, error) {
	return pick[adminapi.EventDestinationCreated](f, "rotateEventDestinationSecret"), f.rec("rotateEventDestinationSecret", in)
}
func (f *fakeAPI) TestEventDestination(_ context.Context, in adminapi.TestEventDestinationInput) (*adminapi.EventDestinationTest, error) {
	return pick[adminapi.EventDestinationTest](f, "testEventDestination"), f.rec("testEventDestination", in)
}
func (f *fakeAPI) ExportEvents(_ context.Context, in adminapi.ExportEventsInput) (*adminapi.Stream, error) {
	return f.stream("exportEvents", in, "application/x-ndjson")
}
func (f *fakeAPI) ExportPlatformEvents(_ context.Context, in adminapi.ExportPlatformEventsInput) (*adminapi.Stream, error) {
	return f.stream("exportPlatformEvents", in, "application/x-ndjson")
}

const destID = "0192f0f0-0000-7000-8000-0000000000d1"

func destination(name string) adminapi.EventDestination {
	return adminapi.EventDestination{ID: destID, TenantID: tid, Name: name, URL: "https://hooks.example.test/in", EventTypes: []adminapi.EventType{}, Enabled: true,
		SecretCreatedAt: t0, CreatedAt: t0, UpdatedAt: t0, Delivery: adminapi.EventDelivery{State: adminapi.EventDeliveryStateIdle, NextAttemptAt: t0}}
}

// TestEventCommands is T-1046: every command of section 5.12 against a fake server: the arguments it
// sends and none it was not given, the output, the notice that the secret is shown once; event
// schema works offline.
func TestEventCommands(t *testing.T) {
	call := func(t *testing.T, id string, args ...string) any {
		t.Helper()
		r := newRig(t)
		mustRun(t, r, args...)
		return r.f.only(t, id)
	}
	check := func(t *testing.T, what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the API received\n %+v\nwant\n %+v", what, got, want)
		}
	}

	t.Run("event-destination list, get, delete", func(t *testing.T) {
		check(t, "list", call(t, "listEventDestinations", "event-destination", "list", "--tenant", tid, "--limit", "5", "--cursor", "abc"), adminapi.ListEventDestinationsInput{Tenant: tid, Limit: 5, Cursor: "abc"})
		check(t, "list defaults", call(t, "listEventDestinations", "event-destination", "list", "--tenant", tid), adminapi.ListEventDestinationsInput{Tenant: tid, Limit: 50})
		check(t, "get", call(t, "getEventDestination", "event-destination", "get", "--tenant", tid, destID), adminapi.GetEventDestinationInput{Tenant: tid, Destination: destID})
		check(t, "delete", call(t, "deleteEventDestination", "event-destination", "delete", "--tenant", tid, destID), adminapi.DeleteEventDestinationInput{Tenant: tid, Destination: destID})
		r := newRig(t)
		r.f.out["listEventDestinations"] = &adminapi.EventDestinationList{Items: []adminapi.EventDestination{destination("billing")}}
		out, _ := mustRun(t, r, "event-destination", "list", "--tenant", tid)
		if !strings.Contains(out, "billing") || !strings.Contains(out, destID) || !strings.Contains(strings.ToLower(strings.SplitN(out, "\n", 2)[0]), "name") {
			t.Errorf("a list is a table with a header:\n%s", out)
		}
		r.f.out["getEventDestination"] = func() *adminapi.EventDestination { d := destination("billing"); return &d }()
		got, _ := mustRun(t, r, "event-destination", "get", "--tenant", tid, destID)
		for _, want := range []string{"billing", "https://hooks.example.test/in", "idle"} {
			if !strings.Contains(got, want) {
				t.Errorf("get does not show %q:\n%s", want, got)
			}
		}
		js, _ := mustRun(t, r, "event-destination", "get", "-o", "json", "--tenant", tid, destID)
		var back adminapi.EventDestination
		if err := json.Unmarshal([]byte(js), &back); err != nil || back.ID != destID {
			t.Errorf("get -o json: %v\n%s", err, js)
		}
	})

	t.Run("event-destination create prints the secret once and says it is not shown again", func(t *testing.T) {
		check(t, "create", call(t, "createEventDestination", "event-destination", "create", "--tenant", tid, "--url", "https://hooks.example.test/in", "billing"),
			adminapi.CreateEventDestinationInput{Tenant: tid, Body: adminapi.EventDestinationCreate{Name: "billing", URL: "https://hooks.example.test/in", Enabled: true}})
		check(t, "create with every flag", call(t, "createEventDestination", "event-destination", "create", "--tenant", tid, "--url", "https://hooks.example.test/in", "--description", "the billing system",
			"--event-type", "io.pandokos.usage.recorded.v1", "--event-type", "io.pandokos.limit.exhausted.v1", "--disabled", "billing"),
			adminapi.CreateEventDestinationInput{Tenant: tid, Body: adminapi.EventDestinationCreate{Name: "billing", URL: "https://hooks.example.test/in", Description: "the billing system",
				EventTypes: []adminapi.EventType{adminapi.EventTypeIoPandokosUsageRecordedV1, adminapi.EventTypeIoPandokosLimitExhaustedV1}, Enabled: false}})
		r := newRig(t)
		const secret = "whsec_CANARYCANARYCANARYCANARYCANARYCANARYCANA="
		r.f.out["createEventDestination"] = &adminapi.EventDestinationCreated{Destination: destination("billing"), Secret: secret}
		out, errs := mustRun(t, r, "event-destination", "create", "--tenant", tid, "--url", "https://hooks.example.test/in", "billing")
		if strings.Count(out, secret) != 1 || strings.Contains(errs, secret) {
			t.Errorf("the secret is printed once, on standard output:\nstdout: %s\nstderr: %s", out, errs)
		}
		if !strings.Contains(out+errs, "not shown again") && !strings.Contains(out+errs, "not be shown again") {
			t.Errorf("no notice that the secret is not shown again:\n%s%s", out, errs)
		}
		if !strings.Contains(out, "billing") {
			t.Errorf("the destination is not printed:\n%s", out)
		}
		for name, args := range map[string][]string{
			"no URL": {"event-destination", "create", "--tenant", tid, "billing"}, "no name": {"event-destination", "create", "--tenant", tid, "--url", "https://hooks.example.test/in"},
			"no tenant": {"event-destination", "create", "--url", "https://hooks.example.test/in", "billing"},
		} {
			if code, _, errs := newRig(t).run(t, args...); code != 2 || errs == "" {
				t.Errorf("%s: exit %d (%s), want a usage error", name, code, errs)
			}
		}
	})

	t.Run("event-destination update sends what it is given", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "event-destination", "update", "--tenant", tid, "--description", "changed", "--enabled=false", destID)
		var wire map[string]any
		if err := json.Unmarshal([]byte(r.f.patchBody), &wire); err != nil || len(wire) != 2 || wire["description"] != "changed" || wire["enabled"] != false {
			t.Errorf("the body %s: want the two members given and no other (%v)", r.f.patchBody, err)
		}
		r = newRig(t)
		mustRun(t, r, "event-destination", "update", "--tenant", tid, "--url", "https://hooks.example.test/new", "--name", "renamed", "--event-type", "io.pandokos.usage.recorded.v1", destID)
		in := r.f.only(t, "updateEventDestination").(adminapi.UpdateEventDestinationInput)
		if in.Tenant != tid || in.Destination != destID || !in.Body.URL.Set || in.Body.URL.Value != "https://hooks.example.test/new" || !in.Body.Name.Set || in.Body.Name.Value != "renamed" ||
			!in.Body.EventTypes.Set || len(in.Body.EventTypes.Value) != 1 || in.Body.Enabled.Set || in.Body.Description.Set {
			t.Errorf("the API received %+v", in)
		}
		if code, _, errs := newRig(t).run(t, "event-destination", "update", "--tenant", tid, destID); code != 2 || errs == "" {
			t.Errorf("an update with nothing to change: exit %d (%s), want a usage error", code, errs)
		}
	})

	t.Run("event-destination rotate-secret prints the secret once", func(t *testing.T) {
		check(t, "rotate", call(t, "rotateEventDestinationSecret", "event-destination", "rotate-secret", "--tenant", tid, destID),
			adminapi.RotateEventDestinationSecretInput{Tenant: tid, Destination: destID})
		check(t, "rotate with an overlap", call(t, "rotateEventDestinationSecret", "event-destination", "rotate-secret", "--tenant", tid, "--previous-valid-for", "2h", destID),
			adminapi.RotateEventDestinationSecretInput{Tenant: tid, Destination: destID, Body: adminapi.EventDestinationSecretRotate{PreviousValidForSeconds: adminapi.Optional[int]{Set: true, Value: 7200}}})
		check(t, "rotate with no overlap", call(t, "rotateEventDestinationSecret", "event-destination", "rotate-secret", "--tenant", tid, "--previous-valid-for", "0s", destID),
			adminapi.RotateEventDestinationSecretInput{Tenant: tid, Destination: destID, Body: adminapi.EventDestinationSecretRotate{PreviousValidForSeconds: adminapi.Optional[int]{Set: true, Value: 0}}})
		r := newRig(t)
		const secret = "whsec_ROTATEDROTATEDROTATEDROTATEDROTATEDROTATE="
		r.f.out["rotateEventDestinationSecret"] = &adminapi.EventDestinationCreated{Destination: destination("billing"), Secret: secret}
		out, errs := mustRun(t, r, "event-destination", "rotate-secret", "--tenant", tid, destID)
		if strings.Count(out, secret) != 1 || strings.Contains(errs, secret) || !strings.Contains(out+errs, "not shown again") && !strings.Contains(out+errs, "not be shown again") {
			t.Errorf("rotate-secret prints the secret once and says it is not shown again:\nstdout: %s\nstderr: %s", out, errs)
		}
		if code, _, errs := newRig(t).run(t, "event-destination", "rotate-secret", "--tenant", tid, "--previous-valid-for", "8d", destID); code != 2 || errs == "" {
			t.Errorf("an overlap of 8 days: exit %d (%s), want a usage error", code, errs)
		}
	})

	t.Run("event-destination test, rewind, dead-letters, replay", func(t *testing.T) {
		r := newRig(t)
		r.f.out["testEventDestination"] = &adminapi.EventDestinationTest{EventID: "0192f0f0-0000-7000-8000-0000000000e1", RequestedAt: t0}
		out, _ := mustRun(t, r, "event-destination", "test", "--tenant", tid, destID)
		if got := r.f.only(t, "testEventDestination"); !reflect.DeepEqual(got, adminapi.TestEventDestinationInput{Tenant: tid, Destination: destID}) || !strings.Contains(out, "0192f0f0-0000-7000-8000-0000000000e1") {
			t.Errorf("test: %+v\n%s", got, out)
		}
		// --wait reads the destination until the result is there and prints it.
		r = newRig(t)
		r.f.out["testEventDestination"] = &adminapi.EventDestinationTest{EventID: "0192f0f0-0000-7000-8000-0000000000e1", RequestedAt: t0}
		done := destination("billing")
		completed := t0.Add(time.Second)
		done.Test = &adminapi.EventDestinationTest{EventID: "0192f0f0-0000-7000-8000-0000000000e1", RequestedAt: t0, CompletedAt: &completed, Outcome: adminapi.DeliveryOutcomeDelivered, Status: 204}
		r.f.out["getEventDestination"] = &done
		out, _ = mustRun(t, r, "event-destination", "test", "--wait", "--tenant", tid, destID)
		if len(r.f.calls("getEventDestination")) < 1 || !strings.Contains(out, "delivered") || !strings.Contains(out, "204") {
			t.Errorf("test --wait: %d reads\n%s", len(r.f.calls("getEventDestination")), out)
		}
		// A test that failed ends with a status other than 0 and says why.
		r = newRig(t)
		r.f.out["testEventDestination"] = &adminapi.EventDestinationTest{EventID: "0192f0f0-0000-7000-8000-0000000000e1", RequestedAt: t0}
		bad := destination("billing")
		bad.Test = &adminapi.EventDestinationTest{EventID: "0192f0f0-0000-7000-8000-0000000000e1", RequestedAt: t0, CompletedAt: &completed, Outcome: adminapi.DeliveryOutcomeStatus, Status: 500}
		r.f.out["getEventDestination"] = &bad
		if code, out, errs := r.run(t, "event-destination", "test", "--wait", "--tenant", tid, destID); code == 0 || !strings.Contains(out+errs, "status") || !strings.Contains(out+errs, "500") {
			t.Errorf("a failed test: exit %d\n%s%s", code, out, errs)
		}

		since := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
		check(t, "rewind to a number", call(t, "rewindEventDestination", "event-destination", "rewind", "--tenant", tid, "--after", "7", destID),
			adminapi.RewindEventDestinationInput{Tenant: tid, Destination: destID, Body: adminapi.EventDestinationRewind{After: adminapi.Optional[int]{Set: true, Value: 7}}})
		check(t, "rewind to 0", call(t, "rewindEventDestination", "event-destination", "rewind", "--tenant", tid, "--after", "0", destID),
			adminapi.RewindEventDestinationInput{Tenant: tid, Destination: destID, Body: adminapi.EventDestinationRewind{After: adminapi.Optional[int]{Set: true, Value: 0}}})
		sp := since
		check(t, "rewind to a time", call(t, "rewindEventDestination", "event-destination", "rewind", "--tenant", tid, "--since", since.Format(time.RFC3339), destID),
			adminapi.RewindEventDestinationInput{Tenant: tid, Destination: destID, Body: adminapi.EventDestinationRewind{Since: adminapi.Optional[*time.Time]{Set: true, Value: &sp}}})
		for name, args := range map[string][]string{
			"neither": {"event-destination", "rewind", "--tenant", tid, destID}, "both": {"event-destination", "rewind", "--tenant", tid, "--after", "1", "--since", since.Format(time.RFC3339), destID},
			"a negative number": {"event-destination", "rewind", "--tenant", tid, "--after", "-1", destID}, "a time that is not one": {"event-destination", "rewind", "--tenant", tid, "--since", "yesterday", destID},
		} {
			if code, _, errs := newRig(t).run(t, args...); code != 2 || errs == "" {
				t.Errorf("rewind %s: exit %d (%s), want a usage error", name, code, errs)
			}
		}

		check(t, "dead-letters", call(t, "listEventDeadLetters", "event-destination", "dead-letters", "--tenant", tid, "--limit", "3", "--cursor", "c", destID),
			adminapi.ListEventDeadLettersInput{Tenant: tid, Destination: destID, Limit: 3, Cursor: "c"})
		check(t, "dead-letters defaults", call(t, "listEventDeadLetters", "event-destination", "dead-letters", "--tenant", tid, destID),
			adminapi.ListEventDeadLettersInput{Tenant: tid, Destination: destID, Limit: 50})
		check(t, "replay all", call(t, "replayEventDeadLetters", "event-destination", "replay", "--tenant", tid, destID),
			adminapi.ReplayEventDeadLettersInput{Tenant: tid, Destination: destID})
		check(t, "replay chosen", call(t, "replayEventDeadLetters", "event-destination", "replay", "--tenant", tid, "--seq", "3", "--seq", "5", destID),
			adminapi.ReplayEventDeadLettersInput{Tenant: tid, Destination: destID, Body: adminapi.EventDeadLetterReplay{Seqs: []int{3, 5}}})
		r = newRig(t)
		r.f.out["listEventDeadLetters"] = &adminapi.EventDeadLetterList{Items: []adminapi.EventDeadLetter{{Seq: 3, EventID: "e1", Type: adminapi.EventTypeIoPandokosUsageRecordedV1, State: adminapi.EventDeadLetterStateDead,
			Attempts: 10, Reason: adminapi.DeliveryOutcomeStatus, Status: 500, ParkedAt: t0}}}
		if out, _ := mustRun(t, r, "event-destination", "dead-letters", "--tenant", tid, destID); !strings.Contains(out, "500") || !strings.Contains(out, "dead") {
			t.Errorf("dead-letters:\n%s", out)
		}
	})

	t.Run("event export and platform-event export", func(t *testing.T) {
		r := newRig(t)
		r.f.streamState().streams["exportEvents"] = "{\"sequence\":\"4\"}\n"
		out := filepath.Join(t.TempDir(), "events.ndjson")
		mustRun(t, r, "event", "export", "--tenant", tid, "--after", "3", "--limit", "10", "--type", "io.pandokos.usage.recorded.v1", "--out", out)
		if got := r.f.only(t, "exportEvents"); !reflect.DeepEqual(got, adminapi.ExportEventsInput{Tenant: tid, After: 3, Limit: 10, Type: []adminapi.EventType{adminapi.EventTypeIoPandokosUsageRecordedV1}}) {
			t.Errorf("event export: %+v", got)
		}
		if b, err := os.ReadFile(out); err != nil || string(b) != "{\"sequence\":\"4\"}\n" {
			t.Errorf("the file: %q %v", b, err)
		}
		r = newRig(t)
		r.f.streamState().streams["exportEvents"] = "{\"sequence\":\"1\"}\n{\"sequence\":\"2\"}\n"
		if stdout, _ := mustRun(t, r, "event", "export", "--tenant", tid); stdout != "{\"sequence\":\"1\"}\n{\"sequence\":\"2\"}\n" {
			t.Errorf("without --out the lines go to standard output: %q", stdout)
		}
		if got := r.f.only(t, "exportEvents"); !reflect.DeepEqual(got, adminapi.ExportEventsInput{Tenant: tid, Limit: 1000}) { // the API fills its default page size, 1000
			t.Errorf("a flag that was not given was sent: %+v", got)
		}
		r = newRig(t)
		r.f.streamState().streams["exportPlatformEvents"] = "{\"sequence\":\"9\"}\n"
		out2, _ := mustRun(t, r, "platform-event", "export", "--after", "8", "--type", "io.pandokos.tenant.deleted.v1")
		if got := r.f.only(t, "exportPlatformEvents"); !reflect.DeepEqual(got, adminapi.ExportPlatformEventsInput{After: 8, Limit: 1000, Type: []adminapi.PlatformEventType{adminapi.PlatformEventTypeIoPandokosTenantDeletedV1}}) || out2 != "{\"sequence\":\"9\"}\n" {
			t.Errorf("platform-event export: %+v %q", got, out2)
		}
	})

	t.Run("event schema runs offline and prints the embedded schema", func(t *testing.T) {
		// No server and no key: the command needs neither.
		code, out, errs := runWith(t, []string{}, "event", "schema")
		if code != 0 {
			t.Fatalf("event schema: exit %d\n%s%s", code, out, errs)
		}
		for _, typ := range append(outbox.Types(), "io.pandokos.tenant.deleted.v1", "io.pandokos.kek.version_activated.v1", "io.pandokos.destination.test.v1") {
			if !strings.Contains(out, typ) {
				t.Errorf("the list of types lacks %s:\n%s", typ, out)
			}
		}
		code, out, errs = runWith(t, []string{}, "event", "schema", "io.pandokos.usage.recorded.v1")
		want, _ := outbox.Schema("io.pandokos.usage.recorded.v1")
		if code != 0 || strings.TrimSpace(out) != strings.TrimSpace(string(want)) || len(want) == 0 {
			t.Errorf("event schema <type>: exit %d, %d bytes of schema, want the embedded one\n%s%s", code, len(want), out, errs)
		}
		if code, _, errs := runWith(t, []string{}, "event", "schema", "io.pandokos.nothing.v1"); code == 0 || errs == "" {
			t.Errorf("an unknown type: exit %d (%s)", code, errs)
		}
	})

	t.Run("tenant create and update take --external-reference", func(t *testing.T) {
		r := newRig(t)
		mustRun(t, r, "tenant", "create", "--external-reference", "cust-42", "acme")
		if got := r.f.only(t, "createTenant").(adminapi.CreateTenantInput); got.Body.Name != "acme" || got.Body.ExternalReference != "cust-42" {
			t.Errorf("create: %+v", got)
		}
		r = newRig(t)
		mustRun(t, r, "tenant", "update", "--external-reference", "cust-43", tid)
		var wire map[string]any
		if err := json.Unmarshal([]byte(r.f.patchBody), &wire); err != nil || len(wire) != 1 || wire["external_reference"] != "cust-43" {
			t.Errorf("the body %s: want the reference alone (%v)", r.f.patchBody, err)
		}
		r = newRig(t)
		mustRun(t, r, "tenant", "update", "--external-reference", "", tid)
		if err := json.Unmarshal([]byte(r.f.patchBody), &wire); err != nil || wire["external_reference"] != "" {
			t.Errorf("clearing the reference: %s (%v)", r.f.patchBody, err)
		}
		r = newRig(t)
		r.f.out["getTenant"] = &adminapi.Tenant{ID: tid, Name: "acme", State: adminapi.TenantStateActive, ExternalReference: "cust-42"}
		if out, _ := mustRun(t, r, "tenant", "get", tid); !strings.Contains(out, "cust-42") {
			t.Errorf("tenant get does not show the reference:\n%s", out)
		}
	})
}
