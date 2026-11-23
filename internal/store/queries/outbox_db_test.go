package queries_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// outboxStatements exercises the twenty statements of sprint 6.4 (plan A2) in the roles and
// scopes they belong to: the administration's in the tenant's session and, for the platform's
// events, in the platform scope; the worker's in the tenant's session and, for the claim and the
// state of the relay, in the platform scope.
func outboxStatements(c context.Context, t *testing.T, d *dbtest.Database, admin *store.DB, tid tenant.ID, called map[string]bool) {
	t.Helper()
	mark := func(name string) { called[name] = true }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	worker := d.Open(t, store.RoleWorker)
	sealed := func(b byte) []byte {
		out := make([]byte, 40)
		for i := range out {
			out[i] = b
		}
		return out
	}
	var dest, testID string // testID: the event ID of the test request
	must("an ID", d.Super(t).QueryRow(c, `SELECT pandokos.uuid_v7()::text`).Scan(&dest))
	// The administration chooses the ID of a destination before its row, to seal the secret for it.
	must("an ID of the administration", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		mark("NewEventDestinationID")
		id, err := queries.New(tx).NewEventDestinationID(c)
		if err == nil && len(id) != 36 {
			t.Errorf("NewEventDestinationID: %q", id)
		}
		return err
	}))

	// The administration creates and reads a destination; it starts at the head of the log.
	var head int64
	must("the head", d.Super(t).QueryRow(c, `SELECT COALESCE((SELECT last_seq FROM pandokos.outbox_stream WHERE tenant_id = $1::uuid), 0)`, tid.String()).Scan(&head))
	must("create a destination", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("CreateEventDestination")
		id, err := q.CreateEventDestination(c, queries.CreateEventDestinationParams{ID: dest, Name: "hook-q", Description: "d", Url: "https://hooks.example.test/in",
			EventTypes: []string{"io.pandokos.usage.recorded.v1"}, Enabled: true, Secret: sealed(1), SecretKeyVersion: 1})
		if err != nil || id != dest {
			t.Errorf("CreateEventDestination: %q, %v", id, err)
		}
		mark("GetEventDestination")
		row, err := q.GetEventDestination(c, dest)
		if err != nil || row.Name != "hook-q" || row.Url != "https://hooks.example.test/in" || !row.Enabled || row.CursorSeq != head || row.HeadSeq != head || row.DeadLetters != 0 ||
			len(row.EventTypes) != 1 || row.TestRequestedAt != nil || row.PreviousSecretExpiresAt != nil || row.ExpiredEvents != 0 {
			t.Errorf("GetEventDestination: %+v, %v", row, err)
		}
		mark("ListEventDestinations")
		if rows, err := q.ListEventDestinations(c, queries.ListEventDestinationsParams{AfterID: dest, PageSize: 10}); err != nil || len(rows) != 1 || rows[0].ID != dest {
			t.Errorf("ListEventDestinations: %+v, %v", rows, err)
		}
		if rows, err := q.ListEventDestinations(c, queries.ListEventDestinationsParams{After: true, AfterID: dest, PageSize: 10}); err != nil || len(rows) != 0 {
			t.Errorf("ListEventDestinations after the only one: %+v, %v", rows, err)
		}
		return nil
	}))
	// Events after the destination: work for the relay.
	d.SeedUsageRecords(t, tid, 3)

	must("change the destination", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("UpdateEventDestination")
		desc, off := "changed", false
		if n, err := q.UpdateEventDestination(c, queries.UpdateEventDestinationParams{ID: dest, Description: &desc, Enabled: &off}); err != nil || n != 1 {
			t.Errorf("UpdateEventDestination: %d, %v", n, err)
		}
		on := true
		if n, err := q.UpdateEventDestination(c, queries.UpdateEventDestinationParams{ID: dest, Enabled: &on, EventTypes: []string{}}); err != nil || n != 1 {
			t.Errorf("UpdateEventDestination (enable, every type): %d, %v", n, err)
		}
		if n, err := q.UpdateEventDestination(c, queries.UpdateEventDestinationParams{ID: tid.String(), Description: &desc}); err != nil || n != 0 {
			t.Errorf("UpdateEventDestination of nothing: %d, %v", n, err)
		}
		row, err := q.GetEventDestination(c, dest)
		if err != nil || row.Description != "changed" || !row.Enabled || len(row.EventTypes) != 0 || row.HeadSeq != head+3 {
			t.Errorf("after the update: %+v, %v", row, err)
		}
		mark("RotateEventDestinationSecret")
		if n, err := q.RotateEventDestinationSecret(c, queries.RotateEventDestinationSecretParams{ID: dest, Secret: sealed(2), SecretKeyVersion: 1, PreviousSeconds: 3600}); err != nil || n != 1 {
			t.Errorf("RotateEventDestinationSecret: %d, %v", n, err)
		}
		mark("RequestEventDestinationTest")
		testID = dest[:len(dest)-1] + "f"
		if n, err := q.RequestEventDestinationTest(c, queries.RequestEventDestinationTestParams{ID: dest, EventID: testID}); err != nil || n != 1 {
			t.Errorf("RequestEventDestinationTest: %d, %v", n, err)
		}
		row, err = q.GetEventDestination(c, dest)
		if err != nil || row.PreviousSecretExpiresAt == nil || time.Until(*row.PreviousSecretExpiresAt) < 59*time.Minute || row.TestRequestedAt == nil || row.TestEventID == nil || *row.TestEventID != testID {
			t.Errorf("after the rotation and the test request: %+v, %v", row, err)
		}
		return nil
	}))

	// The worker claims it (platform scope) and reads the state of the relay.
	const token = "0192f0f0-0000-7000-8000-0000000000f1"
	must("the claim", worker.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("ClaimOutboxDestinations")
		rows, err := q.ClaimOutboxDestinations(c, queries.ClaimOutboxDestinationsParams{Token: token, LeaseSeconds: 60, Max: 100})
		found := false
		for _, r := range rows {
			found = found || (r.DestinationID == dest && r.TenantID == tid.String())
		}
		if err != nil || !found {
			t.Errorf("ClaimOutboxDestinations: %+v, %v, want the destination", rows, err)
		}
		mark("GetOutboxRelayState")
		st, err := q.GetOutboxRelayState(c)
		if err != nil || st.SDestinations < 1 || st.SOldestPendingSeconds < 0 {
			t.Errorf("GetOutboxRelayState: %+v, %v", st, err)
		}
		return nil
	}))

	var firstEvent queries.ListOutboxEventsAfterRow
	must("a round", worker.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("GetEventDestinationForRelay")
		row, err := q.GetEventDestinationForRelay(c, queries.GetEventDestinationForRelayParams{ID: dest, Token: token})
		if err != nil || row.Name != "hook-q" || !row.Enabled || len(row.Secret) != 40 || row.Secret[0] != 2 || row.PreviousSecret == nil || row.PreviousSecret[0] != 1 ||
			row.PreviousSecretExpiresAt == nil || row.CursorSeq != head || row.HeadSeq != head+3 || !row.TestPending || row.TestEventID == nil {
			t.Errorf("GetEventDestinationForRelay: %+v, %v", row, err)
		}
		if _, err := q.GetEventDestinationForRelay(c, queries.GetEventDestinationForRelayParams{ID: dest, Token: "0192f0f0-0000-7000-8000-0000000000f2"}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetEventDestinationForRelay with another token: %v, want no row", err)
		}
		mark("ListOutboxEventsAfter")
		events, err := q.ListOutboxEventsAfter(c, queries.ListOutboxEventsAfterParams{After: head, Types: []string{}, Max: 10})
		if err != nil || len(events) != 3 || events[0].Seq != head+1 || events[2].Seq != head+3 || events[0].Type != "io.pandokos.usage.recorded.v1" {
			t.Errorf("ListOutboxEventsAfter: %+v, %v", events, err)
		} else {
			firstEvent = events[0]
		}
		if only, err := q.ListOutboxEventsAfter(c, queries.ListOutboxEventsAfterParams{After: 0, Types: []string{"io.pandokos.limit.exhausted.v1"}, Max: 10}); err != nil || len(only) != 0 {
			t.Errorf("ListOutboxEventsAfter of another type: %d rows, %v", len(only), err)
		}
		if two, err := q.ListOutboxEventsAfter(c, queries.ListOutboxEventsAfterParams{After: head, Types: []string{"io.pandokos.usage.recorded.v1"}, Max: 2}); err != nil || len(two) != 2 {
			t.Errorf("ListOutboxEventsAfter with a maximum: %d rows, %v", len(two), err)
		}
		mark("ListReplayDeadLetters")
		if rows, err := q.ListReplayDeadLetters(c, queries.ListReplayDeadLettersParams{DestinationID: dest, Max: 10}); err != nil || len(rows) != 0 {
			t.Errorf("ListReplayDeadLetters: %d rows, %v", len(rows), err)
		}
		// A dead letter: parked, and read by the administration below.
		mark("ParkDeadLetter")
		status := int32(500)
		for i := 0; i < 2; i++ { // the second call is the conflict clause: the same letter parked again
			if err := q.ParkDeadLetter(c, queries.ParkDeadLetterParams{DestinationID: dest, Seq: firstEvent.Seq, EventID: firstEvent.ID, Type: firstEvent.Type, Attempts: int32(10 + i), Reason: "status", Status: &status}); err != nil {
				t.Errorf("ParkDeadLetter %d: %v", i, err)
			}
		}
		return nil
	}))

	must("the dead letters, for the administration", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("ListEventDeadLetters")
		rows, err := q.ListEventDeadLetters(c, queries.ListEventDeadLettersParams{DestinationID: dest, After: 0, Max: 10})
		if err != nil || len(rows) != 1 || rows[0].Seq != firstEvent.Seq || rows[0].State != "dead" || rows[0].Attempts != 11 || rows[0].Reason != "status" || rows[0].Status == nil || *rows[0].Status != 500 ||
			rows[0].EventID != firstEvent.ID || rows[0].ReplayRequestedAt != nil {
			t.Errorf("ListEventDeadLetters: %+v, %v", rows, err)
		}
		if none, err := q.ListEventDeadLetters(c, queries.ListEventDeadLettersParams{DestinationID: dest, After: firstEvent.Seq, Max: 10}); err != nil || len(none) != 0 {
			t.Errorf("ListEventDeadLetters after the letter: %d rows, %v", len(none), err)
		}
		mark("ReplayEventDeadLetters")
		if n, err := q.ReplayEventDeadLetters(c, queries.ReplayEventDeadLettersParams{DestinationID: dest, Seqs: []int64{firstEvent.Seq + 9}}); err != nil || n != 0 {
			t.Errorf("ReplayEventDeadLetters of a letter that is not there: %d, %v", n, err)
		}
		if n, err := q.ReplayEventDeadLetters(c, queries.ReplayEventDeadLettersParams{DestinationID: dest, Seqs: []int64{}}); err != nil || n != 1 {
			t.Errorf("ReplayEventDeadLetters: %d, %v", n, err)
		}
		return nil
	}))

	must("the replay", worker.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		rows, err := q.ListReplayDeadLetters(c, queries.ListReplayDeadLettersParams{DestinationID: dest, Max: 10})
		if err != nil || len(rows) != 1 || rows[0].Seq != firstEvent.Seq || rows[0].ID != firstEvent.ID || rows[0].Attempts != 11 {
			t.Errorf("ListReplayDeadLetters: %+v, %v", rows, err)
		}
		mark("ReturnDeadLetter")
		status := int32(503)
		if err := q.ReturnDeadLetter(c, queries.ReturnDeadLetterParams{DestinationID: dest, Seq: firstEvent.Seq, Reason: "timeout", Status: &status}); err != nil {
			t.Errorf("ReturnDeadLetter: %v", err)
		}
		if rows, err := q.ListReplayDeadLetters(c, queries.ListReplayDeadLettersParams{DestinationID: dest, Max: 10}); err != nil || len(rows) != 0 {
			t.Errorf("a letter that came back is still in replay: %d rows, %v", len(rows), err)
		}
		return nil
	}))
	must("a returned letter", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		rows, err := q.ListEventDeadLetters(c, queries.ListEventDeadLettersParams{DestinationID: dest, After: 0, Max: 10})
		if err != nil || len(rows) != 1 || rows[0].State != "dead" || rows[0].Attempts != 12 || rows[0].Reason != "timeout" || rows[0].Status == nil || *rows[0].Status != 503 {
			t.Errorf("the returned letter: %+v, %v", rows, err)
		}
		_, err = q.ReplayEventDeadLetters(c, queries.ReplayEventDeadLettersParams{DestinationID: dest, Seqs: []int64{firstEvent.Seq}})
		return err
	}))

	must("the settle", worker.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("DeleteDeadLetter")
		if err := q.DeleteDeadLetter(c, queries.DeleteDeadLetterParams{DestinationID: dest, Seq: firstEvent.Seq}); err != nil {
			t.Errorf("DeleteDeadLetter: %v", err)
		}
		mark("SettleEventDestination")
		now := time.Now().UTC()
		status := int32(200)
		p := queries.SettleEventDestinationParams{ID: dest, Token: "0192f0f0-0000-7000-8000-0000000000f2", CursorSeq: head + 2, NextAttemptAt: now, LastSuccessAt: &now, LastStatus: &status, TestOutcome: "delivered", TestSentID: &testID, TestStatus: &status}
		if n, err := q.SettleEventDestination(c, p); err != nil || n != 0 {
			t.Errorf("a settle with another token: %d rows, %v, want none", n, err)
		}
		// The outcome of a test is recorded only for the test event that was sent: a settle that
		// sent another one leaves the request pending (and releases the lease).
		other := dest[:len(dest)-1] + "e"
		stale := p
		stale.Token, stale.TestSentID = token, &other
		if n, err := q.SettleEventDestination(c, stale); err != nil || n != 1 {
			t.Errorf("a settle for the test event of an earlier request: %d, %v", n, err)
		}
		return nil
	}))
	must("the request that was not settled", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		row, err := queries.New(tx).GetEventDestination(c, dest)
		if err != nil || row.TestOutcome != "" || row.TestCompletedAt != nil || row.TestRequestedAt == nil {
			t.Errorf("a test settled with another event ID: %+v, %v, want the request still pending", row, err)
		}
		return nil
	}))
	const token2 = "0192f0f0-0000-7000-8000-0000000000f3"
	must("the second claim", worker.Platform(c, func(c context.Context, tx store.Tx) error {
		if rows, err := queries.New(tx).ClaimOutboxDestinations(c, queries.ClaimOutboxDestinationsParams{Token: token2, LeaseSeconds: 60, Max: 100}); err != nil || len(rows) == 0 {
			t.Errorf("the second claim: %+v, %v, want the destination", rows, err)
		}
		return nil
	}))
	must("the settle of the test that was sent", worker.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		now := time.Now().UTC()
		status := int32(200)
		p := queries.SettleEventDestinationParams{ID: dest, Token: token2, CursorSeq: head + 2, NextAttemptAt: now, LastSuccessAt: &now, LastStatus: &status, TestOutcome: "delivered", TestSentID: &testID, TestStatus: &status}
		if n, err := q.SettleEventDestination(c, p); err != nil || n != 1 {
			t.Errorf("SettleEventDestination: %d, %v", n, err)
		}
		// A destination that is not leased any more cannot be settled again: the lease was released.
		if n, err := q.SettleEventDestination(c, p); err != nil || n != 0 {
			t.Errorf("a second settle: %d, %v, want none", n, err)
		}
		mark("TenantOutboxRetention")
		r, err := q.TenantOutboxRetention(c, queries.TenantOutboxRetentionParams{EventHours: 168, UsageMonths: 0, Batch: 100})
		if err != nil || r.REvents != 0 || r.RExpired != 0 || r.RLimitEvents != 0 {
			t.Errorf("TenantOutboxRetention: %+v, %v", r, err)
		}
		return nil
	}))

	must("what the settle wrote", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		row, err := q.GetEventDestination(c, dest)
		if err != nil || row.CursorSeq != head+2 || row.Failures != 0 || row.LastSuccessAt == nil || row.LastStatus == nil || *row.LastStatus != 200 || row.TestOutcome != "delivered" || row.TestCompletedAt == nil {
			t.Errorf("after the settle: %+v, %v", row, err)
		}
		rows, err := q.ListEventDeadLetters(c, queries.ListEventDeadLettersParams{DestinationID: dest, After: 0, Max: 10})
		if err != nil || len(rows) != 0 {
			t.Errorf("DeleteDeadLetter left the letter of a replay that succeeded: %+v, %v", rows, err)
		}
		mark("RewindEventDestination")
		zero := int64(0)
		cursor, err := q.RewindEventDestination(c, queries.RewindEventDestinationParams{ID: dest, After: &zero})
		if err != nil || cursor != 0 {
			t.Errorf("RewindEventDestination to a number: %d, %v", cursor, err)
		}
		future := time.Now().Add(time.Hour)
		if cursor, err = q.RewindEventDestination(c, queries.RewindEventDestinationParams{ID: dest, Since: &future}); err != nil || cursor != 0 {
			t.Errorf("RewindEventDestination to a time: %d, %v (a rewind never moves the cursor forward)", cursor, err)
		}
		return nil
	}))

	must("the platform's events", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		mark("ListPlatformOutboxEventsAfter")
		rows, err := queries.New(tx).ListPlatformOutboxEventsAfter(c, queries.ListPlatformOutboxEventsAfterParams{After: 0, Types: []string{}, Max: 100})
		if err != nil {
			t.Errorf("ListPlatformOutboxEventsAfter: %v", err)
		}
		for i := 1; i < len(rows); i++ {
			if rows[i].Seq <= rows[i-1].Seq {
				t.Errorf("ListPlatformOutboxEventsAfter: seq %d after %d", rows[i].Seq, rows[i-1].Seq)
			}
		}
		if only, err := queries.New(tx).ListPlatformOutboxEventsAfter(c, queries.ListPlatformOutboxEventsAfterParams{After: 0, Types: []string{"io.pandokos.kek.version_retired.v1"}, Max: 100}); err != nil || len(only) > len(rows) {
			t.Errorf("ListPlatformOutboxEventsAfter of one type: %d rows, %v", len(only), err)
		}
		return nil
	}))

	must("delete the destination", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("DeleteEventDestination")
		if n, err := q.DeleteEventDestination(c, dest); err != nil || n != 1 {
			t.Errorf("DeleteEventDestination: %d, %v", n, err)
		}
		if n, err := q.DeleteEventDestination(c, dest); err != nil || n != 0 {
			t.Errorf("DeleteEventDestination again: %d, %v", n, err)
		}
		if _, err := q.GetEventDestination(c, dest); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetEventDestination of a deleted destination: %v", err)
		}
		return nil
	}))
}
