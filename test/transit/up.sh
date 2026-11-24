#!/bin/sh
# Starts OpenBao or HashiCorp Vault in development mode for the key service tests and writes
# local/transit/env. Usage: up.sh openbao|vault. The root token is generated here and lives in the
# git-ignored local/transit. Safe to run again: the container is replaced.
set -eu
vendor=${1:-}
case "$vendor" in
openbao | vault) ;;
*)
    echo "usage: up.sh openbao|vault" >&2
    exit 2
    ;;
esac
cd "$(dirname "$0")/../.."
dir=local/transit
(umask 077 && mkdir -p "$dir")
chmod 0700 "$dir"

# A development-mode service keeps its data in memory: the previous one goes first.
docker compose -f test/transit/compose.yaml -p pandokos-testtransit down -v --remove-orphans >/dev/null 2>&1 || true

(umask 077 && od -An -N24 -tx1 /dev/urandom | tr -d ' \n' > "$dir/root_token")
chmod 0600 "$dir/root_token"
PANDOKOS_TEST_TRANSIT_ROOT_TOKEN=$(cat "$dir/root_token")
PANDOKOS_TEST_TRANSIT_PORT=$(python3 -c '
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
')
export PANDOKOS_TEST_TRANSIT_ROOT_TOKEN PANDOKOS_TEST_TRANSIT_PORT

docker compose -f test/transit/compose.yaml -p pandokos-testtransit --profile "$vendor" up -d

address="http://127.0.0.1:$PANDOKOS_TEST_TRANSIT_PORT"
n=0
until [ "$(curl -s -o /dev/null -w '%{http_code}' "$address/v1/sys/health" 2>/dev/null)" = 200 ]; do
    n=$((n + 1))
    if [ "$n" -ge 60 ]; then
        echo "transit: $vendor did not answer on $address in 60 s" >&2
        docker compose -f test/transit/compose.yaml -p pandokos-testtransit --profile "$vendor" logs --tail 50 >&2 || true
        exit 1
    fi
    sleep 1
done

(umask 077 && {
    printf 'PANDOKOS_TEST_TRANSIT_ADDRESS=%s\n' "$address"
    printf 'PANDOKOS_TEST_TRANSIT_VENDOR=%s\n' "$vendor"
    printf 'PANDOKOS_TEST_TRANSIT_ROOT_TOKEN_FILE=%s/%s/root_token\n' "$(pwd)" "$dir"
} > "$dir/env")
echo "$vendor is up; settings are in $dir/env"
