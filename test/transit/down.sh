#!/bin/sh
# Stops OpenBao and HashiCorp Vault and discards their data. The settings in local/transit stay;
# delete the directory to start from nothing.
set -eu
cd "$(dirname "$0")/../.."
for profile in openbao vault; do
    PANDOKOS_TEST_TRANSIT_ROOT_TOKEN=unused PANDOKOS_TEST_TRANSIT_PORT=0 \
        docker compose -f test/transit/compose.yaml -p pandokos-testtransit --profile "$profile" down -v --remove-orphans
done
