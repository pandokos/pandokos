#!/bin/sh
# Stops Keycloak and Ory Hydra and discards their data. The certificate and the settings in
# local/idp stay; delete the directory to start from nothing.
set -eu
cd "$(dirname "$0")/.."
docker compose -p pandokos-testidp down -v --remove-orphans
