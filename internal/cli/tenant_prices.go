package cli

import (
	"errors"
	"flag"
	"fmt"

	"github.com/pandokos/pandokos/internal/adminapi"
)

func init() { commands = append(commands, priceListCommands...) }

// parseAnyArgs is parse for a command whose number of positional arguments depends on its flags:
// it returns the arguments and leaves the count to the caller.
func parseAnyArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, &usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// priceListCommands are the commands of the tenant price list operations (sprint 5.4, section 5.14).
var priceListCommands = []command{
	{[]string{"price-list", "lists"}, "listPriceLists", func(a *app, args []string) error {
		fs := a.flags("price-list lists")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.ListPriceLists(a.ctx, adminapi.ListPriceListsInput{Tenant: tid})
		if err != nil {
			return err
		}
		if a.output == "json" {
			return a.printJSON(out)
		}
		a.printTable(toAny(out.Items))
		return nil
	}},

	{[]string{"price-list", "list"}, "listPriceListVersions", func(a *app, args []string) error {
		fs := a.flags("price-list list")
		var l listFlags
		l.registerPaging(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		currency := fs.String("currency", "", "only versions of the list in this currency")
		model := fs.String("model", "", "only versions for this model, as provider/name")
		provider := fs.String("provider", "", "only versions for models of this provider")
		at := fs.String("at", "", "only the versions in force at this time (RFC 3339)")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		in := adminapi.ListPriceListVersionsInput{Currency: *currency, Provider: *provider, At: *at, Limit: l.limit}
		if *model != "" {
			if *provider != "" {
				return usagef("--model and --provider cannot be used together")
			}
			p, n, ok := splitRef(*model)
			if !ok {
				return usagef("--model takes a model reference provider/name")
			}
			in.Provider, in.Model = p, n
		}
		if err := a.connect(); err != nil {
			return err
		}
		var err error
		if in.Tenant, err = a.resolveTenant(*tenantFlag); err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			in.Cursor = cursor
			out, err := a.client.ListPriceListVersions(a.ctx, in)
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},

	{[]string{"price-list", "get"}, "getPriceListVersion", func(a *app, args []string) error {
		fs := a.flags("price-list get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		v, err := a.client.GetPriceListVersion(a.ctx, adminapi.GetPriceListVersionInput{Tenant: tid, Price: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(v)
	}},

	{[]string{"price-list", "set"}, "createPriceListVersion", func(a *app, args []string) error {
		fs := a.flags("price-list set")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		currency := fs.String("currency", "", "ISO 4217 currency of the list (required)")
		model := fs.String("model", "", "the model, as provider/name (required)")
		validFrom := fs.String("valid-from", "", "start of validity (RFC 3339; default now)")
		note := fs.String("note", "", "note recorded with the version")
		end := fs.Bool("end", false, "end the list for the model: from valid-from it does not price it")
		var rates multi
		fs.Var(&rates, "rate", "category[@threshold]=price per million units, as decimal text (repeatable)")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		switch {
		case *currency == "":
			return usagef("price-list set needs --currency")
		case *model == "":
			return usagef("price-list set needs --model")
		case *end && len(rates) > 0:
			return usagef("--end cannot be combined with --rate")
		case !*end && len(rates) == 0:
			return usagef("price-list set needs at least one --rate, or --end")
		}
		p, n, ok := splitRef(*model)
		if !ok {
			return usagef("--model takes a model reference provider/name")
		}
		body := adminapi.TenantPriceCreate{Provider: p, Model: n, Currency: *currency, Note: *note}
		if *end {
			body.Kind = adminapi.TenantPriceKindEnd
		}
		for _, r := range rates {
			pr, err := parseRate(r)
			if err != nil {
				return err
			}
			body.Rates = append(body.Rates, pr)
		}
		var err error
		if body.ValidFrom, err = parseTime("--valid-from", *validFrom); err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		v, err := a.client.CreatePriceListVersion(a.ctx, adminapi.CreatePriceListVersionInput{Tenant: tid, Body: body})
		if err != nil {
			return err
		}
		return a.printResource(v)
	}},

	{[]string{"price-list", "delete"}, "deletePriceListVersion", func(a *app, args []string) error {
		fs := a.flags("price-list delete")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		v, err := a.client.DeletePriceListVersion(a.ctx, adminapi.DeletePriceListVersionInput{Tenant: tid, Price: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(v)
	}},

	{[]string{"price-list", "currency", "get"}, "getActiveCurrency", func(a *app, args []string) error {
		fs := a.flags("price-list currency get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.GetActiveCurrency(a.ctx, adminapi.GetActiveCurrencyInput{Tenant: tid})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"price-list", "currency", "set"}, "setActiveCurrency", func(a *app, args []string) error {
		fs := a.flags("price-list currency set")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		def := fs.Bool("default", false, "the default currency of the installation")
		pos, err := parseAnyArgs(fs, args)
		if err != nil {
			return err
		}
		switch {
		case *def && len(pos) > 0:
			return usagef("--default and a currency cannot be used together")
		case !*def && len(pos) != 1:
			return usagef("price-list currency set takes a currency or --default")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		body := adminapi.ActiveCurrencySet{}
		if !*def {
			body.Currency = pos[0]
		}
		c, err := a.client.SetActiveCurrency(a.ctx, adminapi.SetActiveCurrencyInput{Tenant: tid, Body: body})
		if err != nil {
			return err
		}
		if a.output == "json" {
			return a.printJSON(c)
		}
		inactive := c.InactiveLimits
		shown := *c
		shown.InactiveLimits = nil
		if err := a.printResource(shown); err != nil {
			return err
		}
		if len(inactive) > 0 {
			fmt.Fprintln(a.stdout, "limits that will be inactive:")
			for _, l := range inactive {
				fmt.Fprintf(a.stdout, "  %s %s %s %s\n", l.Level, l.RuleID, l.Metric, l.Currency)
			}
		}
		return nil
	}},
}
