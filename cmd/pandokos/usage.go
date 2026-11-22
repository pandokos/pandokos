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
  token-issuer list|create|get|update|delete|platform
  subject-mapping list|create|get|delete
  platform-issuer list|create|get|update|delete|tenants|add-tenant|remove-tenant
  limit list|create|get|update|delete|platform|events
  platform-limit list|create|get|update|delete
  platform-user list|create|get|update|disable|enable|reset-password|unlock|revoke-sessions
  member list|add|get|update|remove|disable|enable|reset-password|unlock|revoke-sessions
                                   Administer through the administration API;
                                   "pandokos <command> --help" lists the flags
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
