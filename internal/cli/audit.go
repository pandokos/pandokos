package cli

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"iter"
	"os"
	"strings"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/auditchain"
)

func init() { commands = append(commands, auditCommands...) }

// auditFilters are the flags of the two audit lists.
type auditFilters struct {
	from, to, action, resourceType, resourceID, actorType, actorID, outcome, requestID, supportActionID string
}

func (f *auditFilters) register(fs *flag.FlagSet, tenant bool) {
	fs.StringVar(&f.from, "from", "", "start of the window (RFC 3339)")
	fs.StringVar(&f.to, "to", "", "end of the window, excluded (RFC 3339)")
	fs.StringVar(&f.action, "action", "", "an action, or a prefix ending in a dot")
	fs.StringVar(&f.resourceType, "resource-type", "", "only events on this type of resource")
	fs.StringVar(&f.resourceID, "resource-id", "", "only events on this resource")
	fs.StringVar(&f.actorType, "actor-type", "", "only events of this type of actor")
	fs.StringVar(&f.actorID, "actor-id", "", "only events of this actor")
	fs.StringVar(&f.outcome, "outcome", "", "success or failure")
	fs.StringVar(&f.requestID, "request-id", "", "only events of this request")
	if tenant {
		fs.StringVar(&f.supportActionID, "support-action-id", "", "only events written under this support action")
	}
}

func (f *auditFilters) check() error {
	if f.actorType != "" && !adminapi.AuditActorType(f.actorType).Valid() {
		return usagef("--actor-type is not a type of actor")
	}
	if f.outcome != "" && !adminapi.AuditOutcome(f.outcome).Valid() {
		return usagef("--outcome must be success or failure")
	}
	return nil
}

// auditCommands are the commands that read the two audit logs.
var auditCommands = []command{
	{[]string{"audit", "list"}, "listAuditEvents", func(a *app, args []string) error {
		fs := a.flags("audit list")
		var l listFlags
		l.registerPaging(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		var f auditFilters
		f.register(fs, true)
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		if err := f.check(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListAuditEvents(a.ctx, adminapi.ListAuditEventsInput{Tenant: tid, Limit: l.limit, Cursor: cursor, From: f.from, To: f.to,
				Action: f.action, ResourceType: f.resourceType, ResourceID: f.resourceID, ActorType: adminapi.AuditActorType(f.actorType), ActorID: f.actorID,
				Outcome: adminapi.AuditOutcome(f.outcome), RequestID: f.requestID, SupportActionID: f.supportActionID})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"audit", "head"}, "getAuditHead", func(a *app, args []string) error {
		fs := a.flags("audit head")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		h, err := a.client.GetAuditHead(a.ctx, adminapi.GetAuditHeadInput{Tenant: tid})
		if err != nil {
			return err
		}
		return a.printResource(h)
	}},

	{[]string{"audit", "export"}, "exportAuditEvents", func(a *app, args []string) error {
		fs := a.flags("audit export")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		fromSeq := fs.Int("from-seq", 0, "first sequence number (default: the first row kept)")
		toSeq := fs.Int("to-seq", 0, "last sequence number (default: the head when the export starts)")
		out := fs.String("out", "", "write to this file (through FILE.partial) instead of standard output")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		if *fromSeq < 0 || *toSeq < 0 {
			return usagef("--from-seq and --to-seq must be positive")
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.writeExport(*out, func(w io.Writer) error {
			body, err := a.client.ExportAuditEvents(a.ctx, adminapi.ExportAuditEventsInput{Tenant: tid, FromSeq: *fromSeq, ToSeq: *toSeq})
			if err != nil {
				return err
			}
			defer body.Body.Close()
			_, err = io.Copy(w, body.Body)
			return err
		})
	}},

	{[]string{"platform-audit", "list"}, "listPlatformAuditEvents", func(a *app, args []string) error {
		fs := a.flags("platform-audit list")
		var l listFlags
		l.registerPaging(fs)
		var f auditFilters
		f.register(fs, false)
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		if err := f.check(); err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListPlatformAuditEvents(a.ctx, adminapi.ListPlatformAuditEventsInput{Limit: l.limit, Cursor: cursor, From: f.from, To: f.to,
				Action: f.action, ResourceType: f.resourceType, ResourceID: f.resourceID, ActorType: adminapi.AuditActorType(f.actorType), ActorID: f.actorID,
				Outcome: adminapi.AuditOutcome(f.outcome), RequestID: f.requestID})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"platform-audit", "head"}, "getPlatformAuditHead", func(a *app, args []string) error {
		if _, err := a.begin(a.flags("platform-audit head"), args, 0); err != nil {
			return err
		}
		h, err := a.client.GetPlatformAuditHead(a.ctx, adminapi.GetPlatformAuditHeadInput{})
		if err != nil {
			return err
		}
		return a.printResource(h)
	}},

	{[]string{"platform-audit", "export"}, "exportPlatformAuditEvents", func(a *app, args []string) error {
		fs := a.flags("platform-audit export")
		fromSeq := fs.Int("from-seq", 0, "first sequence number (default: the first row kept)")
		toSeq := fs.Int("to-seq", 0, "last sequence number (default: the head when the export starts)")
		out := fs.String("out", "", "write to this file (through FILE.partial) instead of standard output")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		if *fromSeq < 0 || *toSeq < 0 {
			return usagef("--from-seq and --to-seq must be positive")
		}
		return a.writeExport(*out, func(w io.Writer) error {
			body, err := a.client.ExportPlatformAuditEvents(a.ctx, adminapi.ExportPlatformAuditEventsInput{FromSeq: *fromSeq, ToSeq: *toSeq})
			if err != nil {
				return err
			}
			defer body.Body.Close()
			_, err = io.Copy(w, body.Body)
			return err
		})
	}},
}

// composites are the commands made of several operations. They are matched before commands, and
// are outside the table that T-219 checks against the operations.
var composites = []command{
	{[]string{"audit", "verify"}, "", func(a *app, args []string) error { return a.verify(args, false) }},
	{[]string{"platform-audit", "verify"}, "", func(a *app, args []string) error { return a.verify(args, true) }},
}

// errBreaks is returned when the chain does not verify; the lines were printed.
var errBreaks = errors.New("the chain does not verify")

// verify is pandokos audit verify and platform-audit verify (section 5.9).
func (a *app) verify(args []string, platform bool) error {
	name := "audit verify"
	if platform {
		name = "platform-audit verify"
	}
	fs := a.flags(name)
	tenantFlag := fs.String("tenant", "", "tenant ID")
	file := fs.String("file", "", "verify this NDJSON export offline instead of reading the server")
	expect := fs.String("expect-head", "", "the hash the head must have (a head taken earlier)")
	fromSeq := fs.Int("from-seq", 0, "start the export at this sequence number")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if platform && *tenantFlag != "" {
		return usagef("platform-audit verify takes no --tenant")
	}
	if *fromSeq < 0 {
		return usagef("--from-seq must be positive")
	}
	if *file != "" && *fromSeq != 0 {
		return usagef("--from-seq reads the server; it cannot be combined with --file")
	}
	var want []byte
	if *expect != "" {
		var err error
		if want, err = hex.DecodeString(*expect); err != nil || len(want) != 32 {
			return usagef("--expect-head must be 64 hexadecimal digits")
		}
	}
	if *file != "" {
		scope := "platform"
		if !platform {
			if !reUUID.MatchString(*tenantFlag) {
				return usagef("an offline verification needs --tenant with the tenant ID")
			}
			scope = "tenant:" + *tenantFlag
		}
		f, err := os.Open(*file) //nolint:gosec // the path is a flag of the operator
		if err != nil {
			return fmt.Errorf("cannot read the export: %w", err)
		}
		defer f.Close()
		return a.verifyStream(f, scope, nil, want)
	}
	if err := a.connect(); err != nil {
		return err
	}
	scope := "platform"
	var head *adminapi.AuditHead
	var err error
	if platform {
		head, err = a.client.GetPlatformAuditHead(a.ctx, adminapi.GetPlatformAuditHeadInput{})
	} else {
		var tid string
		if tid, err = a.resolveTenant(*tenantFlag); err != nil {
			return err
		}
		scope = "tenant:" + tid
		head, err = a.client.GetAuditHead(a.ctx, adminapi.GetAuditHeadInput{Tenant: tid})
	}
	if err != nil {
		return err
	}
	h, err := headOf(head)
	if err != nil {
		return fmt.Errorf("invalid head: %w", err)
	}
	if head.Seq == 0 {
		return a.verifyStream(strings.NewReader(""), scope, h, want)
	}
	var body *adminapi.StreamBody
	if platform {
		body, err = a.client.ExportPlatformAuditEvents(a.ctx, adminapi.ExportPlatformAuditEventsInput{FromSeq: *fromSeq, ToSeq: head.Seq})
	} else {
		body, err = a.client.ExportAuditEvents(a.ctx, adminapi.ExportAuditEventsInput{Tenant: strings.TrimPrefix(scope, "tenant:"), FromSeq: *fromSeq, ToSeq: head.Seq})
	}
	if err != nil {
		return err
	}
	defer body.Body.Close()
	return a.verifyStream(body.Body, scope, h, want)
}

func headOf(h *adminapi.AuditHead) (*auditchain.Head, error) {
	hash, err := hex.DecodeString(h.Hash)
	if err != nil {
		return nil, errors.New("hash is not hexadecimal")
	}
	anchor, err := hex.DecodeString(h.AnchorHash)
	if err != nil {
		return nil, errors.New("anchor_hash is not hexadecimal")
	}
	return &auditchain.Head{Seq: int64(h.Seq), Hash: hash, AnchorSeq: int64(h.AnchorSeq), AnchorHash: anchor}, nil
}

// verifyStream verifies the rows of an NDJSON export and prints the answer of section 5.9.
func (a *app) verifyStream(r io.Reader, scope string, head *auditchain.Head, expect []byte) error {
	var readErr error
	rows := func(yield func(auditchain.Row, error) bool) {
		readErr = readLines(r, func(line []byte) error {
			row, err := rowOf(scope, line)
			if err != nil {
				return err
			}
			if !yield(row, nil) {
				return errStop
			}
			return nil
		})
	}
	res := auditchain.Verify(iter.Seq2[auditchain.Row, error](rows), head, expect)
	if readErr != nil && !errors.Is(readErr, errStop) {
		for _, b := range res.Breaks {
			fmt.Fprintf(a.stdout, "break at seq %d: %s\n", b.Seq, b.Reason)
		}
		return fmt.Errorf("invalid export: %w", readErr)
	}
	for _, b := range res.Breaks {
		fmt.Fprintf(a.stdout, "break at seq %d: %s\n", b.Seq, b.Reason)
	}
	if len(res.Breaks) > 0 {
		return errBreaks
	}
	if res.Rows == 0 {
		fmt.Fprintln(a.stdout, "verified 0 rows")
		return nil
	}
	fmt.Fprintf(a.stdout, "verified %d rows, seq %d..%d, head %s\n", res.Rows, res.First, res.Last, hex.EncodeToString(res.LastHash))
	if res.LinkUnverified {
		fmt.Fprintf(a.stdout, "note: the link of seq %d to the rows before it is not verified\n", res.First)
	}
	return nil
}

var errStop = errors.New("stop")

// rowOf decodes one line of an export into the row the chain hashes.
func rowOf(scope string, line []byte) (auditchain.Row, error) {
	var e adminapi.AuditEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return auditchain.Row{}, errors.New("a line is not an audit event")
	}
	prev, err1 := hex.DecodeString(e.PrevHash)
	hash, err2 := hex.DecodeString(e.Hash)
	if err1 != nil || err2 != nil || len(prev) != 32 || len(hash) != 32 {
		return auditchain.Row{}, fmt.Errorf("seq %d: prev_hash or hash is not 64 hexadecimal digits", e.Seq)
	}
	r := auditchain.Row{Scope: scope, Seq: int64(e.Seq), ID: e.ID, OccurredAt: e.OccurredAt, ActorType: e.ActorType, ActorID: e.ActorID, ActorName: e.ActorName,
		KeyID: e.KeyID, Action: e.Action, ResourceType: e.ResourceType, ResourceID: e.ResourceID, Outcome: e.Outcome, RequestID: e.RequestID, Reason: e.Reason,
		SupportActionID: e.SupportActionID, Detail: e.Detail, PrevHash: prev, Hash: hash}
	if e.DetailWithheld {
		d, err := hex.DecodeString(e.DetailDigest)
		if err != nil || len(d) != 32 {
			return auditchain.Row{}, fmt.Errorf("seq %d: detail_digest is not 64 hexadecimal digits", e.Seq)
		}
		r.DetailDigest = d
	}
	return r, nil
}
