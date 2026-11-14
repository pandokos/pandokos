package main

const usage = `Usage: pandokos <command> [flags]

Commands:
  serve [--config FILE]            Start the server
  migrate [--config FILE]          Apply pending schema migrations, then exit
  migrate status [--config FILE]   Show applied and pending migrations, then exit
  init [--config FILE] [--ttl DURATION] [--description TEXT] [--key-out FILE] [--recover]
                                   Issue the bootstrap platform key, then exit
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
  tenant-model list
  model-restriction get|set|clear
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
