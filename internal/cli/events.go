package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/outbox/schema"
)

func init() {
	commands = append(commands, eventCommands...)
	composites = append(composites, eventComposites...)
}

// defaultEventPage is the page size of the lists of this file when --limit is not given.
const defaultEventPage = 50

// maxSecretOverlap is the longest time the previous secret may keep signing (section 5.12).
const maxSecretOverlap = 7 * 24 * time.Hour

// eventTypes turns the values of a repeatable --event-type or --type flag into types, dropping
// empty ones.
func eventTypes(values []string) []adminapi.EventType {
	var out []adminapi.EventType
	for _, v := range values {
		if v != "" {
			out = append(out, adminapi.EventType(v))
		}
	}
	return out
}

// parseOverlap reads the duration of --previous-valid-for: a Go duration, or whole days as "7d".
func parseOverlap(v string) (int, error) {
	var d time.Duration
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, usagef("--previous-valid-for must be a duration such as 90m, 24h or 2d")
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(v); err != nil {
			return 0, usagef("--previous-valid-for must be a duration such as 90m, 24h or 2d")
		}
	}
	if d < 0 || d > maxSecretOverlap {
		return 0, usagef("--previous-valid-for must be between 0s and 7 days")
	}
	return int(d / time.Second), nil
}

// secretNotice is what a command that shows a signing secret says on standard error.
const secretNotice = "the signing secret is shown only now and is not shown again; store it in the receiver's configuration\n"

// printDestinationCreated prints a destination with its secret, once.
func (a *app) printDestinationCreated(res *adminapi.EventDestinationCreated) error {
	if a.output == "json" {
		if res.Secret != "" {
			fmt.Fprint(a.stderr, secretNotice)
		}
		return a.printJSON(res)
	}
	if err := a.printResource(res.Destination); err != nil {
		return err
	}
	if res.Secret != "" {
		fmt.Fprintf(a.stdout, "secret: %s\n", res.Secret)
		fmt.Fprint(a.stderr, secretNotice)
	}
	return nil
}

// destinationTarget resolves the tenant and checks the destination ID, which every command on one
// destination takes.
func (a *app) destinationTarget(tenantFlag string, id string) (string, string, error) {
	if !reUUID.MatchString(id) {
		return "", "", usagef("the destination must be given by its ID")
	}
	tid, err := a.resolveTenant(tenantFlag)
	return tid, id, err
}

var eventCommands = []command{
	{[]string{"event-destination", "list"}, "listEventDestinations", func(a *app, args []string) error {
		fs := a.flags("event-destination list")
		var l listFlags
		l.registerPaging(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		if l.limit == 0 {
			l.limit = defaultEventPage
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListEventDestinations(a.ctx, adminapi.ListEventDestinationsInput{Tenant: tid, Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"event-destination", "create"}, "createEventDestination", func(a *app, args []string) error {
		fs := a.flags("event-destination create")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		url := fs.String("url", "", "the HTTPS URL that receives the events (required)")
		description := fs.String("description", "", "description")
		var types multi
		fs.Var(&types, "event-type", "an event type to deliver, repeatable (default: every type)")
		disabled := fs.Bool("disabled", false, "create the destination disabled")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if *url == "" {
			return usagef("event-destination create needs --url")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		res, err := a.client.CreateEventDestination(a.ctx, adminapi.CreateEventDestinationInput{Tenant: tid, Body: adminapi.EventDestinationCreate{
			Name: pos[0], URL: *url, Description: *description, EventTypes: eventTypes(types), Enabled: !*disabled}})
		if err != nil {
			return err
		}
		return a.printDestinationCreated(res)
	}},

	{[]string{"event-destination", "get"}, "getEventDestination", func(a *app, args []string) error {
		fs := a.flags("event-destination get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, id, err := a.destinationTarget(*tenantFlag, pos[0])
		if err != nil {
			return err
		}
		d, err := a.client.GetEventDestination(a.ctx, adminapi.GetEventDestinationInput{Tenant: tid, Destination: id})
		if err != nil {
			return err
		}
		return a.printResource(d)
	}},

	{[]string{"event-destination", "update"}, "updateEventDestination", func(a *app, args []string) error {
		fs := a.flags("event-destination update")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		name := fs.String("name", "", "new name")
		url := fs.String("url", "", "new URL")
		description := fs.String("description", "", "new description; empty clears it")
		var types multi
		fs.Var(&types, "event-type", "the event types to deliver, repeatable; an empty value means every type")
		enabled := fs.Bool("enabled", true, "enable or disable the destination (--enabled=false)")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		var body adminapi.EventDestinationUpdate
		changed := false
		if given(fs, "name") {
			body.Name, changed = adminapi.Some(*name), true
		}
		if given(fs, "url") {
			body.URL, changed = adminapi.Some(*url), true
		}
		if given(fs, "description") {
			body.Description, changed = adminapi.Some(*description), true
		}
		if given(fs, "event-type") {
			list := eventTypes(types)
			if list == nil {
				list = []adminapi.EventType{}
			}
			body.EventTypes, changed = adminapi.Some(list), true
		}
		if given(fs, "enabled") {
			body.Enabled, changed = adminapi.Some(*enabled), true
		}
		if !changed {
			return usagef("event-destination update needs --name, --url, --description, --event-type or --enabled")
		}
		tid, id, err := a.destinationTarget(*tenantFlag, pos[0])
		if err != nil {
			return err
		}
		d, err := a.client.UpdateEventDestination(a.ctx, adminapi.UpdateEventDestinationInput{Tenant: tid, Destination: id, Body: body})
		if err != nil {
			return err
		}
		return a.printResource(d)
	}},

	{[]string{"event-destination", "delete"}, "deleteEventDestination", func(a *app, args []string) error {
		fs := a.flags("event-destination delete")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, id, err := a.destinationTarget(*tenantFlag, pos[0])
		if err != nil {
			return err
		}
		if _, err := a.client.DeleteEventDestination(a.ctx, adminapi.DeleteEventDestinationInput{Tenant: tid, Destination: id}); err != nil {
			return err
		}
		if a.output != "json" {
			fmt.Fprintf(a.stdout, "destination %s deleted\n", id)
		}
		return nil
	}},

	{[]string{"event-destination", "rotate-secret"}, "rotateEventDestinationSecret", func(a *app, args []string) error {
		fs := a.flags("event-destination rotate-secret")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		overlap := fs.String("previous-valid-for", "", "how long the previous secret keeps signing, up to 7d (default 24h; 0s stops it at once)")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		var body adminapi.EventDestinationSecretRotate
		if given(fs, "previous-valid-for") {
			secs, err := parseOverlap(*overlap)
			if err != nil {
				return err
			}
			body.PreviousValidForSeconds = adminapi.Some(secs)
		}
		tid, id, err := a.destinationTarget(*tenantFlag, pos[0])
		if err != nil {
			return err
		}
		res, err := a.client.RotateEventDestinationSecret(a.ctx, adminapi.RotateEventDestinationSecretInput{Tenant: tid, Destination: id, Body: body})
		if err != nil {
			return err
		}
		return a.printDestinationCreated(res)
	}},

	{[]string{"event-destination", "test"}, "testEventDestination", func(a *app, args []string) error {
		fs := a.flags("event-destination test")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		wait := fs.Bool("wait", false, "wait for the worker to send the test event and print the result")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, id, err := a.destinationTarget(*tenantFlag, pos[0])
		if err != nil {
			return err
		}
		req, err := a.client.TestEventDestination(a.ctx, adminapi.TestEventDestinationInput{Tenant: tid, Destination: id})
		if err != nil {
			return err
		}
		if !*wait {
			return a.printResource(req)
		}
		res, err := a.awaitTest(tid, id, req.EventID)
		if err != nil {
			return err
		}
		if err := a.printResource(res); err != nil {
			return err
		}
		if res.Outcome != adminapi.DeliveryOutcomeDelivered {
			return fmt.Errorf("the test request failed: outcome %s, status %d", res.Outcome, res.Status)
		}
		return nil
	}},

	{[]string{"event-destination", "rewind"}, "rewindEventDestination", func(a *app, args []string) error {
		fs := a.flags("event-destination rewind")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		after := fs.Int("after", 0, "deliver again every event after this sequence number (0: from the start)")
		since := fs.String("since", "", "deliver again every event since this time (RFC 3339)")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		if given(fs, "after") == given(fs, "since") {
			return usagef("event-destination rewind needs exactly one of --after and --since")
		}
		var body adminapi.EventDestinationRewind
		if given(fs, "after") {
			if *after < 0 {
				return usagef("--after must not be negative")
			}
			body.After = adminapi.Some(*after)
		} else {
			t, err := parseTime("--since", *since)
			if err != nil {
				return err
			}
			if t == nil {
				return usagef("--since must be an RFC 3339 time")
			}
			body.Since = adminapi.Some(t)
		}
		tid, id, err := a.destinationTarget(*tenantFlag, pos[0])
		if err != nil {
			return err
		}
		d, err := a.client.RewindEventDestination(a.ctx, adminapi.RewindEventDestinationInput{Tenant: tid, Destination: id, Body: body})
		if err != nil {
			return err
		}
		return a.printResource(d)
	}},

	{[]string{"event-destination", "dead-letters"}, "listEventDeadLetters", func(a *app, args []string) error {
		fs := a.flags("event-destination dead-letters")
		var l listFlags
		l.registerPaging(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		if l.limit == 0 {
			l.limit = defaultEventPage
		}
		tid, id, err := a.destinationTarget(*tenantFlag, pos[0])
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListEventDeadLetters(a.ctx, adminapi.ListEventDeadLettersInput{Tenant: tid, Destination: id, Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"event-destination", "replay"}, "replayEventDeadLetters", func(a *app, args []string) error {
		fs := a.flags("event-destination replay")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		var seqs multi
		fs.Var(&seqs, "seq", "the sequence number of a dead letter to send again, repeatable (default: every dead letter)")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		var body adminapi.EventDeadLetterReplay
		for _, s := range seqs {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return usagef("--seq must be a sequence number, got %q", s)
			}
			body.Seqs = append(body.Seqs, n)
		}
		tid, id, err := a.destinationTarget(*tenantFlag, pos[0])
		if err != nil {
			return err
		}
		res, err := a.client.ReplayEventDeadLetters(a.ctx, adminapi.ReplayEventDeadLettersInput{Tenant: tid, Destination: id, Body: body})
		if err != nil {
			return err
		}
		return a.printResource(res)
	}},

	{[]string{"event", "export"}, "exportEvents", func(a *app, args []string) error {
		fs := a.flags("event export")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		after := fs.Int("after", 0, "only events after this sequence number")
		limit := fs.Int("limit", 0, "at most this many events")
		var types multi
		fs.Var(&types, "type", "only events of this type, repeatable")
		out := fs.String("out", "", "write to this file (through FILE.partial) instead of standard output")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		if *after < 0 || *limit < 0 {
			return usagef("--after and --limit must not be negative")
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.writeExport(*out, func(w io.Writer) error {
			body, err := a.client.ExportEvents(a.ctx, adminapi.ExportEventsInput{Tenant: tid, After: *after, Limit: *limit, Type: eventTypes(types)})
			if err != nil {
				return err
			}
			defer body.Body.Close()
			_, err = io.Copy(w, body.Body)
			return err
		})
	}},

	{[]string{"platform-event", "export"}, "exportPlatformEvents", func(a *app, args []string) error {
		fs := a.flags("platform-event export")
		after := fs.Int("after", 0, "only events after this sequence number")
		limit := fs.Int("limit", 0, "at most this many events")
		var types multi
		fs.Var(&types, "type", "only events of this type, repeatable")
		out := fs.String("out", "", "write to this file (through FILE.partial) instead of standard output")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		if *after < 0 || *limit < 0 {
			return usagef("--after and --limit must not be negative")
		}
		var list []adminapi.PlatformEventType
		for _, v := range types {
			if v != "" {
				list = append(list, adminapi.PlatformEventType(v))
			}
		}
		return a.writeExport(*out, func(w io.Writer) error {
			body, err := a.client.ExportPlatformEvents(a.ctx, adminapi.ExportPlatformEventsInput{After: *after, Limit: *limit, Type: list})
			if err != nil {
				return err
			}
			defer body.Body.Close()
			_, err = io.Copy(w, body.Body)
			return err
		})
	}},
}

var eventComposites = []command{
	{[]string{"event", "schema"}, "", func(a *app, args []string) error { return a.eventSchema(args) }},
}

// awaitTest reads the destination until the test request of eventID has a result.
func (a *app) awaitTest(tenantID, id, eventID string) (*adminapi.EventDestinationTest, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		d, err := a.client.GetEventDestination(a.ctx, adminapi.GetEventDestinationInput{Tenant: tenantID, Destination: id})
		if err != nil {
			return nil, err
		}
		if t := d.Test; t != nil && t.EventID == eventID && t.CompletedAt != nil {
			return t, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the test event %s was not sent within two minutes: is the worker running?", eventID)
		}
		select {
		case <-a.ctx.Done():
			return nil, a.ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// eventSchema is pandokos event schema [TYPE]: the embedded JSON Schema of a type, or the list of
// types, from no server.
func (a *app) eventSchema(args []string) error {
	fs := a.flags("event schema")
	pos, err := parseRange(fs, args, 0, 1)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		for _, t := range schema.Types() {
			fmt.Fprintln(a.stdout, t)
		}
		return nil
	}
	b, ok := schema.Get(pos[0])
	if !ok {
		return usagef("no event type %q; \"pandokos event schema\" lists them", pos[0])
	}
	_, err = a.stdout.Write(b)
	return err
}
