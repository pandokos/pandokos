package main

const usage = `Usage: pandokos <command> [flags]

Commands:
  serve [--config FILE]            Start the server
  migrate [--config FILE]          Apply pending schema migrations, then exit
  migrate status [--config FILE]   Show applied and pending migrations, then exit
  init [--config FILE] [--ttl DURATION] [--description TEXT] [--key-out FILE] [--recover] [--admin-username NAME] [--password-out FILE]
                                   Issue the bootstrap platform key, then exit; with
                                   --admin-username also create the first administrator
  user reset-password USERNAME [--config FILE] [--password-out FILE]
                                   Give a local account a new generated password, then exit
  user unlock USERNAME [--config FILE]
                                   Remove the lock of a local account, then exit
  whoami                           Show the identity of the key in use
  tenant list|create|get|update|suspend|reactivate
  tenant delete TENANT --reason R --confirm-name NAME
  tenant deletion get|cancel|list|audit-head|audit-export|verify
  kek-version list
  service-account list|create|get|update|disable|enable
  key list|create|get|revoke
  platform-key list|create|get|revoke
  credential list|create|get|update|replace|disable|enable|set-default|revoke|delete
  tenant-credential list|get|disable|revoke
  provider list|create|get|update|delete
  endpoint list|create|get|update|delete
  model list|create|get|update|delete|grants|grant|revoke-grant
  price list|override|delete|clear-override
  price-import list|get|run
  price-list lists|list|get|set|delete|currency get|currency set
  tenant-model list
  model-restriction get|set|clear
  capability check --provider P --model M
  usage list [--from T] [--to T] [--provider P]
  usage report [--month YYYY-MM] [--period day|month|total] [--group-by DIMS]
  usage export [--from T] [--to T] [--format ndjson|csv] [--out FILE]
  metadata-keys get|set|clear
  audit list|head|export|verify
  platform-audit list|head|export|verify
  token-issuer list|create|get|update|delete|platform
  subject-mapping list|create|get|delete
  platform-issuer list|create|get|update|delete|tenants|add-tenant|remove-tenant
  limit list|create|get|update|delete|platform|events|usage
  platform-limit list|create|get|update|delete
  platform-user list|create|get|update|disable|enable|reset-password|unlock|revoke-sessions
  member list|add|get|update|remove|disable|enable|reset-password|unlock|revoke-sessions
  permission list
  role builtin|list|create|get|update|deactivate|activate|delete
  event-destination list|create|get|update|delete|rotate-secret|test|rewind|dead-letters|replay
  event export --tenant ID [--after N] [--limit N] [--type T] [--out FILE]
  event schema [TYPE]              Print the JSON Schema of an event type (needs no server)
  platform-event export [--after N] [--limit N] [--type T] [--out FILE]
  tenant-support open|list|get|close
  support-action list|get|end
                                   Administer through the administration API;
                                   "pandokos <command> --help" lists the flags
  export (--platform | --tenant ID) [--out FILE]
                                   Write the configuration as a declarative document
  apply --file FILE (--platform | --tenant ID) [--dry-run]
                                   Create and update what a document lists; never deletes
  tenant-export --tenant ID --out DIR [--usage-from T]
                                   Write the tenant's own data to a new directory, through the API
  tenant-export schema             Print the JSON Schema of the export (needs no server)
  import-prices [--config FILE] (--file FILE | --embedded) [--dry-run]
                                   Import a price dataset from a file or the embedded snapshot, then exit
  kek generate --out FILE          Write a new key-encryption key to a new file (mode 0600)
  config check [--config FILE] [--for serve|migrate|init]
                                   Load and validate the configuration, then exit
  version                          Print the version
  help                             Print this text

The configuration file is --config, else the PANDOKOS_CONFIG environment variable, else
none (defaults and environment variables only).
API commands read PANDOKOS_CLI_SERVER, PANDOKOS_CLI_KEY_FILE and PANDOKOS_CLI_CA_FILE.
`
