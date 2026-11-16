#!/bin/sh
# Starts Keycloak and Ory Hydra for the trusted issuer tests and writes local/idp/env.
# Everything generated here (certificate, client secrets, ports) lives in the git-ignored local/.
# Safe to run again: the containers are replaced, the certificate and the secrets are kept.
set -eu
cd "$(dirname "$0")/../.."
dir=local/idp
(umask 077 && mkdir -p "$dir")
chmod 0700 "$dir"

# Containers that held the previous data go first: Hydra keeps its clients in memory only.
docker compose -p pandokos-testidp down -v --remove-orphans >/dev/null 2>&1 || true

# A throw-away authority and a certificate for localhost. The containers run as unprivileged
# users that are not the owner of the files, so the key is readable by them; the directory above
# is 0700 and the key is worth nothing outside this machine's loopback.
go run ./test/testcert/cmd/testcert -dir "$dir/tls" -dns localhost -ip 127.0.0.1 -days 30
chmod 0644 "$dir/tls/server.key"

secret() { od -An -N24 -tx1 /dev/urandom | tr -d ' \n'; }

# Keep what an earlier run generated (so that a second run changes nothing a test run holds).
if [ -f "$dir/env" ]; then
    set -a
    . "./$dir/env"
    set +a
fi
: "${PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A:=$(secret)}"
: "${PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_B:=$(secret)}"
: "${PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_C:=$(secret)}"
: "${PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_OTHER:=$(secret)}"
: "${PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT:=$(secret)}"
: "${PANDOKOS_TEST_IDP_HYDRA_SECRET_UNMAPPED:=$(secret)}"
: "${PANDOKOS_IDP_HYDRA_SYSTEM_SECRET:=$(secret)}"

# Two free ports on the loopback.
ports=$(python3 -c '
import socket
s = [socket.socket() for _ in range(2)]
for x in s:
    x.bind(("127.0.0.1", 0))
print(*[x.getsockname()[1] for x in s])
')
set -- $ports
PANDOKOS_IDP_KEYCLOAK_PORT=$1
PANDOKOS_IDP_HYDRA_PORT=$2
export PANDOKOS_IDP_KEYCLOAK_PORT PANDOKOS_IDP_HYDRA_PORT PANDOKOS_IDP_HYDRA_SYSTEM_SECRET

# The realm of Keycloak, from its template. The secrets are hexadecimal, so sed is safe.
(umask 077 && sed \
    -e "s/@SECRET_A@/$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A/" \
    -e "s/@SECRET_B@/$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_B/" \
    -e "s/@SECRET_C@/$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_C/" \
    -e "s/@SECRET_OTHER@/$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_OTHER/" \
    test/idp/keycloak-realm.json.tmpl > "$dir/keycloak-realm.json")
chmod 0644 "$dir/keycloak-realm.json" # read by the unprivileged user of the container

docker compose -f test/idp/compose.yaml up -d --wait

ca="$dir/tls/ca.pem"
kc_issuer="https://localhost:$PANDOKOS_IDP_KEYCLOAK_PORT/realms/pandokos"
hy_issuer="https://localhost:$PANDOKOS_IDP_HYDRA_PORT/"

# Neither image has a tool a health check could use: wait from here.
wait_for() {
    n=0
    until curl -fsS --cacert "$ca" -o /dev/null "$1" 2>/dev/null; do
        n=$((n + 1))
        if [ "$n" -ge 120 ]; then
            echo "idp: $1 did not answer in 120 s" >&2
            docker compose -p pandokos-testidp logs --tail 50 >&2 || true
            exit 1
        fi
        sleep 1
    done
}
wait_for "$kc_issuer/.well-known/openid-configuration"
wait_for "${hy_issuer}health/ready"

# The Hydra clients, through the administration API inside the container (it is not published).
hydra_client() {
    docker compose -f test/idp/compose.yaml exec -T hydra hydra create client \
        --endpoint http://127.0.0.1:4445 --id "$1" --name "$1" --secret "$2" \
        --grant-type client_credentials --token-endpoint-auth-method client_secret_post \
        --audience https://pandokos.test/t1 --audience https://other.test --format json >/dev/null
}
hydra_client tenant-workload "$PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT"
hydra_client unmapped-workload "$PANDOKOS_TEST_IDP_HYDRA_SECRET_UNMAPPED"

(umask 077 && {
    printf 'PANDOKOS_TEST_IDP_CA_FILE=%s/%s\n' "$(pwd)" "$ca"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER=%s\n' "$kc_issuer"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_TOKEN_URL=%s/protocol/openid-connect/token\n' "$kc_issuer"
    printf 'PANDOKOS_TEST_IDP_HYDRA_ISSUER=%s\n' "$hy_issuer"
    printf 'PANDOKOS_TEST_IDP_HYDRA_TOKEN_URL=%soauth2/token\n' "$hy_issuer"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_A=workload-a\n'
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A=%s\n' "$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_B=workload-b\n'
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_B=%s\n' "$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_B"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_C=workload-c\n'
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_C=%s\n' "$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_C"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_OTHER=workload-other\n'
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_OTHER=%s\n' "$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_OTHER"
    printf 'PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT=tenant-workload\n'
    printf 'PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT=%s\n' "$PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT"
    printf 'PANDOKOS_TEST_IDP_HYDRA_CLIENT_UNMAPPED=unmapped-workload\n'
    printf 'PANDOKOS_TEST_IDP_HYDRA_SECRET_UNMAPPED=%s\n' "$PANDOKOS_TEST_IDP_HYDRA_SECRET_UNMAPPED"
    printf 'PANDOKOS_IDP_HYDRA_SYSTEM_SECRET=%s\n' "$PANDOKOS_IDP_HYDRA_SYSTEM_SECRET"
} > "$dir/env")
echo "identity providers are up; settings are in $dir/env"
