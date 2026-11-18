package cli

func init() { commands = append(commands, limitCommands...) }

// notImplemented is the body of the limit commands until step C7 of sprint 5.2 writes them.
func notImplemented(*app, []string) error { return usagef("this command is not implemented yet") }

// limitCommands are the commands of the limit operations: one per operation, as for the others.
var limitCommands = []command{
	{[]string{"limit", "list"}, "listLimits", notImplemented},
	{[]string{"limit", "create"}, "createLimit", notImplemented},
	{[]string{"limit", "get"}, "getLimit", notImplemented},
	{[]string{"limit", "update"}, "updateLimit", notImplemented},
	{[]string{"limit", "delete"}, "deleteLimit", notImplemented},
	{[]string{"limit", "ceilings"}, "listTenantPlatformLimits", notImplemented},
	{[]string{"limit", "events"}, "listLimitEvents", notImplemented},
	{[]string{"platform-limit", "list"}, "listPlatformLimits", notImplemented},
	{[]string{"platform-limit", "create"}, "createPlatformLimit", notImplemented},
	{[]string{"platform-limit", "get"}, "getPlatformLimit", notImplemented},
	{[]string{"platform-limit", "update"}, "updatePlatformLimit", notImplemented},
	{[]string{"platform-limit", "delete"}, "deletePlatformLimit", notImplemented},
}
