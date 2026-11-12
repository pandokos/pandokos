package main

const usage = `Usage: pandokos <command> [flags]

Commands:
  serve [--config FILE]            Start the server
  migrate [--config FILE]          Apply pending schema migrations, then exit
  migrate status [--config FILE]   Show applied and pending migrations, then exit
  config check [--config FILE] [--for serve|migrate]
                                   Load and validate the configuration, then exit
  version                          Print the version
  help                             Print this text

The configuration file is --config, else the PANDOKOS_CONFIG environment variable, else
none (defaults and environment variables only).
`
