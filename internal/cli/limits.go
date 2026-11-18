package cli

import (
	"flag"
	"strconv"

	"github.com/pandokos/pandokos/internal/adminapi"
)

func init() { commands = append(commands, limitCommands...) }

// intList collects the values of a repeatable flag that takes an integer.
type intList struct {
	set    bool
	values []int
}

func (l *intList) String() string { return "" }
func (l *intList) Set(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return usagef("--threshold takes a whole number of percent")
	}
	l.set = true
	l.values = append(l.values, n)
	return nil
}

// ruleFlags are the flags that write a rule; the creation of a tenant rule and of a platform rule
// share them (sprint 5.2, section 5.15).
type ruleFlags struct {
	metric, amount, currency, provider, model string
	limit                                     int
	soft                                      bool
	thresholds                                intList
}

func (r *ruleFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&r.metric, "metric", "", "the metric (required): requests_per_day, tokens_per_month, cost_per_month, ...")
	fs.IntVar(&r.limit, "limit", 0, "requests or tokens (a count metric)")
	fs.StringVar(&r.amount, "amount", "", "the budget as a decimal, at most six fraction digits (a cost metric)")
	fs.StringVar(&r.currency, "currency", "", "three capital letters (a cost metric)")
	fs.StringVar(&r.provider, "provider", "", "the rule applies to this provider only")
	fs.StringVar(&r.model, "model", "", "the rule applies to this model of the provider only")
	fs.BoolVar(&r.soft, "soft", false, "never refuse: only record the thresholds")
	fs.Var(&r.thresholds, "threshold", "percent of the limit at which an event is recorded (repeatable)")
}

// value checks that the value of the rule is given once, as a limit or as an amount.
func (r *ruleFlags) value(fs *flag.FlagSet) error {
	hasLimit, hasAmount := given(fs, "limit"), given(fs, "amount")
	switch {
	case r.metric == "":
		return usagef("--metric is required")
	case !adminapi.LimitMetric(r.metric).Valid():
		return usagef("--metric is not a metric of limits")
	case hasLimit && hasAmount:
		return usagef("--limit and --amount cannot be used together")
	case !hasLimit && !hasAmount:
		return usagef("--limit or --amount is required")
	}
	return nil
}

// listMetric reads the --metric filter of a list.
func listMetric(v string) (adminapi.LimitMetric, error) {
	m := adminapi.LimitMetric(v)
	if v != "" && !m.Valid() {
		return "", usagef("--metric is not a metric of limits")
	}
	return m, nil
}

// updateBody turns the flags that were given into the members of an update; none given is a usage
// error, as are flags that contradict each other.
func updateBody(fs *flag.FlagSet, limit int, amount string, soft, enforce, noThresholds bool, thresholds intList) (adminapi.LimitRuleUpdate, error) {
	var b adminapi.LimitRuleUpdate
	switch {
	case given(fs, "limit") && given(fs, "amount"):
		return b, usagef("--limit and --amount cannot be used together")
	case soft && enforce:
		return b, usagef("--soft and --enforce cannot be used together")
	case noThresholds && thresholds.set:
		return b, usagef("--threshold and --no-thresholds cannot be used together")
	}
	if given(fs, "limit") {
		b.Limit = adminapi.Some(limit)
	}
	if given(fs, "amount") {
		b.Amount = adminapi.Some(amount)
	}
	switch {
	case soft:
		b.Enforce = adminapi.Some(false)
	case enforce:
		b.Enforce = adminapi.Some(true)
	}
	switch {
	case noThresholds:
		b.Thresholds = adminapi.Some([]int{})
	case thresholds.set:
		b.Thresholds = adminapi.Some(thresholds.values)
	}
	if !b.Limit.Set && !b.Amount.Set && !b.Enforce.Set && !b.Thresholds.Set {
		return b, usagef("an update needs at least one flag")
	}
	return b, nil
}

// subjectOf reads --subject, --service-account and --key of the creation of a tenant rule.
func (a *app) subjectOf(tenantID, subject, account, key string) (adminapi.LimitSubjectKind, string, error) {
	switch subject {
	case "", "tenant":
		if account != "" || key != "" {
			return "", "", usagef("--service-account and --key need --subject service-account or key")
		}
		return adminapi.LimitSubjectKind(subject), "", nil
	case "service-account":
		if account == "" || key != "" {
			return "", "", usagef("--subject service-account takes --service-account and no --key")
		}
		id, err := a.resolveServiceAccount(tenantID, account)
		return adminapi.LimitSubjectKindServiceAccount, id, err
	case "key":
		if key == "" || account != "" {
			return "", "", usagef("--subject key takes --key and no --service-account")
		}
		return adminapi.LimitSubjectKindKey, key, nil
	}
	return "", "", usagef("--subject is tenant, service-account or key")
}

// limitCommands are the commands of the limit operations: one per operation, as for the others.
var limitCommands = []command{
	{[]string{"limit", "list"}, "listLimits", func(a *app, args []string) error {
		fs := a.flags("limit list")
		var l listFlags
		l.registerSorted(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		subject := fs.String("subject", "", "only rules of this subject: tenant, service-account or key")
		subjectID := fs.String("subject-id", "", "only rules of this service account, key or mapping ID")
		metric := fs.String("metric", "", "only rules of this metric")
		provider := fs.String("provider", "", "only rules scoped to this provider")
		model := fs.String("model", "", "only rules scoped to this model")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		m, err := listMetric(*metric)
		if err != nil {
			return err
		}
		kind := adminapi.LimitSubjectKind("")
		switch *subject {
		case "":
		case "tenant", "key":
			kind = adminapi.LimitSubjectKind(*subject)
		case "service-account":
			kind = adminapi.LimitSubjectKindServiceAccount
		default:
			return usagef("--subject is tenant, service-account or key")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListLimits(a.ctx, adminapi.ListLimitsInput{Tenant: tid, Limit: l.limit, Cursor: cursor, Sort: adminapi.Sort(l.sort),
				SubjectKind: kind, SubjectID: *subjectID, Metric: m, Provider: *provider, Model: *model})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"limit", "create"}, "createLimit", func(a *app, args []string) error {
		fs := a.flags("limit create")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		var r ruleFlags
		r.register(fs)
		subject := fs.String("subject", "", "tenant (default), service-account or key")
		account := fs.String("service-account", "", "the service account of --subject service-account: ID or name")
		key := fs.String("key", "", "the key ID (or subject mapping ID) of --subject key")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if err := r.value(fs); err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		kind, subjectID, err := a.subjectOf(tid, *subject, *account, *key)
		if err != nil {
			return err
		}
		out, err := a.client.CreateLimit(a.ctx, adminapi.CreateLimitInput{Tenant: tid, Body: adminapi.LimitRuleCreate{
			SubjectKind: kind, SubjectID: subjectID, Provider: r.provider, Model: r.model, Metric: adminapi.LimitMetric(r.metric), Limit: r.limit,
			Amount: r.amount, Currency: r.currency, Enforce: !r.soft, Thresholds: r.thresholds.values}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"limit", "get"}, "getLimit", func(a *app, args []string) error {
		fs := a.flags("limit get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.GetLimit(a.ctx, adminapi.GetLimitInput{Tenant: tid, Rule: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"limit", "update"}, "updateLimit", func(a *app, args []string) error {
		fs := a.flags("limit update")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		limit := fs.Int("limit", 0, "requests or tokens")
		amount := fs.String("amount", "", "the budget as a decimal")
		soft := fs.Bool("soft", false, "never refuse: only record the thresholds")
		enforce := fs.Bool("enforce", false, "refuse at the limit")
		noThresholds := fs.Bool("no-thresholds", false, "remove every threshold")
		var thresholds intList
		fs.Var(&thresholds, "threshold", "percent of the limit at which an event is recorded (repeatable; replaces the set)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		body, err := updateBody(fs, *limit, *amount, *soft, *enforce, *noThresholds, thresholds)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.UpdateLimit(a.ctx, adminapi.UpdateLimitInput{Tenant: tid, Rule: pos[0], Body: body})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"limit", "delete"}, "deleteLimit", func(a *app, args []string) error {
		fs := a.flags("limit delete")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.DeleteLimit(a.ctx, adminapi.DeleteLimitInput{Tenant: tid, Rule: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"limit", "platform"}, "listTenantPlatformLimits", func(a *app, args []string) error {
		fs := a.flags("limit platform")
		var l listFlags
		l.registerPaging(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListTenantPlatformLimits(a.ctx, adminapi.ListTenantPlatformLimitsInput{Tenant: tid, Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"limit", "events"}, "listLimitEvents", func(a *app, args []string) error {
		fs := a.flags("limit events")
		var l listFlags
		l.registerPaging(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		kind := fs.String("kind", "", "only events of this kind: threshold_reached or exhausted")
		rule := fs.String("rule", "", "only events of this rule (ID)")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListLimitEvents(a.ctx, adminapi.ListLimitEventsInput{Tenant: tid, Limit: l.limit, Cursor: cursor,
				Kind: adminapi.LimitEventKind(*kind), Rule: *rule})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"platform-limit", "list"}, "listPlatformLimits", func(a *app, args []string) error {
		fs := a.flags("platform-limit list")
		var l listFlags
		l.registerSorted(fs)
		appliesTo := fs.String("applies-to", "", `only the defaults ("default") or the rows of this tenant ID`)
		metric := fs.String("metric", "", "only rules of this metric")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		m, err := listMetric(*metric)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListPlatformLimits(a.ctx, adminapi.ListPlatformLimitsInput{Limit: l.limit, Cursor: cursor, Sort: adminapi.Sort(l.sort),
				AppliesTo: *appliesTo, Metric: m})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"platform-limit", "create"}, "createPlatformLimit", func(a *app, args []string) error {
		fs := a.flags("platform-limit create")
		var r ruleFlags
		r.register(fs)
		forTenant := fs.String("for-tenant", "", "the tenant the ceiling is for: ID or name (absent: the default of every tenant)")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if err := r.value(fs); err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid := ""
		if *forTenant != "" {
			var err error
			if tid, err = a.resolveTenant(*forTenant); err != nil {
				return err
			}
		}
		out, err := a.client.CreatePlatformLimit(a.ctx, adminapi.CreatePlatformLimitInput{Body: adminapi.PlatformLimitRuleCreate{
			Tenant: tid, Provider: r.provider, Model: r.model, Metric: adminapi.LimitMetric(r.metric), Limit: r.limit, Amount: r.amount,
			Currency: r.currency, Enforce: !r.soft, Thresholds: r.thresholds.values}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"platform-limit", "get"}, "getPlatformLimit", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("platform-limit get"), args, 1)
		if err != nil {
			return err
		}
		out, err := a.client.GetPlatformLimit(a.ctx, adminapi.GetPlatformLimitInput{Rule: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"platform-limit", "update"}, "updatePlatformLimit", func(a *app, args []string) error {
		fs := a.flags("platform-limit update")
		limit := fs.Int("limit", 0, "requests or tokens")
		amount := fs.String("amount", "", "the budget as a decimal")
		soft := fs.Bool("soft", false, "never refuse: only record the thresholds")
		enforce := fs.Bool("enforce", false, "refuse at the limit")
		noThresholds := fs.Bool("no-thresholds", false, "remove every threshold")
		var thresholds intList
		fs.Var(&thresholds, "threshold", "percent of the limit at which an event is recorded (repeatable; replaces the set)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		body, err := updateBody(fs, *limit, *amount, *soft, *enforce, *noThresholds, thresholds)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		out, err := a.client.UpdatePlatformLimit(a.ctx, adminapi.UpdatePlatformLimitInput{Rule: pos[0], Body: body})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"platform-limit", "delete"}, "deletePlatformLimit", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("platform-limit delete"), args, 1)
		if err != nil {
			return err
		}
		out, err := a.client.DeletePlatformLimit(a.ctx, adminapi.DeletePlatformLimitInput{Rule: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},
}
