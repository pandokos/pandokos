package main

const usage = `Usage: pandokos <command> [flags]

Commands:
  serve [--config FILE]         Start the server
  config check [--config FILE]  Load and validate the configuration, then exit
  version                       Print the version
  help                          Print this text

The configuration file is --config, else the PANDOKOS_CONFIG environment variable, else
none (defaults and environment variables only).
`
