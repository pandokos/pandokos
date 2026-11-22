#!/bin/sh
# Starts Keycloak, Ory Hydra and Authentik for the trusted issuer tests and writes local/idp/env.
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
: "${PANDOKOS_TEST_IDP_KEYCLOAK_PEOPLE_SECRET:=$(secret)}"
: "${PANDOKOS_TEST_IDP_KEYCLOAK_ALICE_PASSWORD:=$(secret)}"
: "${PANDOKOS_TEST_IDP_KEYCLOAK_BOB_PASSWORD:=$(secret)}"
: "${PANDOKOS_TEST_IDP_KEYCLOAK_ADMIN_PASSWORD:=$(secret)}"
: "${PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT:=$(secret)}"
: "${PANDOKOS_TEST_IDP_HYDRA_SECRET_UNMAPPED:=$(secret)}"
: "${PANDOKOS_IDP_HYDRA_SYSTEM_SECRET:=$(secret)}"
: "${PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A:=$(secret)}"
: "${PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_B:=$(secret)}"
: "${PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_C:=$(secret)}"
: "${PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_UNMAPPED:=$(secret)}"
: "${PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_SECRET:=$(secret)}"
: "${PANDOKOS_IDP_AUTHENTIK_SECRET_KEY:=$(secret)$(secret)}"
: "${PANDOKOS_IDP_AUTHENTIK_DB_PASSWORD:=$(secret)}"
: "${PANDOKOS_IDP_AUTHENTIK_ADMIN_PASSWORD:=$(secret)}"
: "${PANDOKOS_IDP_AUTHENTIK_ADMIN_TOKEN:=$(secret)$(secret)}"

# Four free ports on the loopback; the fourth is the one the tests' Pandokos process serves on: the
# callback of the client pandokos-admin is on it.
ports=$(python3 -c '
import socket
s = [socket.socket() for _ in range(4)]
for x in s:
    x.bind(("127.0.0.1", 0))
print(*[x.getsockname()[1] for x in s])
')
set -- $ports
PANDOKOS_IDP_KEYCLOAK_PORT=$1
PANDOKOS_IDP_HYDRA_PORT=$2
PANDOKOS_IDP_AUTHENTIK_PORT=$3
PANDOKOS_TEST_IDP_ORIGIN=http://127.0.0.1:$4
export PANDOKOS_IDP_KEYCLOAK_PORT PANDOKOS_IDP_HYDRA_PORT PANDOKOS_IDP_HYDRA_SYSTEM_SECRET
export PANDOKOS_TEST_IDP_KEYCLOAK_ADMIN_PASSWORD
export PANDOKOS_IDP_AUTHENTIK_PORT PANDOKOS_IDP_AUTHENTIK_SECRET_KEY PANDOKOS_IDP_AUTHENTIK_DB_PASSWORD
export PANDOKOS_IDP_AUTHENTIK_ADMIN_PASSWORD PANDOKOS_IDP_AUTHENTIK_ADMIN_TOKEN
export PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_B PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_C
export PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_UNMAPPED PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_SECRET

# The realm of Keycloak, from its template. The secrets are hexadecimal, so sed is safe.
(umask 077 && sed \
    -e "s/@SECRET_A@/$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A/" \
    -e "s/@SECRET_B@/$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_B/" \
    -e "s/@SECRET_C@/$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_C/" \
    -e "s/@SECRET_OTHER@/$PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_OTHER/" \
    -e "s/@PEOPLE_SECRET@/$PANDOKOS_TEST_IDP_KEYCLOAK_PEOPLE_SECRET/" \
    -e "s/@ALICE_PASSWORD@/$PANDOKOS_TEST_IDP_KEYCLOAK_ALICE_PASSWORD/" \
    -e "s/@BOB_PASSWORD@/$PANDOKOS_TEST_IDP_KEYCLOAK_BOB_PASSWORD/" \
    -e "s,@ORIGIN@,$PANDOKOS_TEST_IDP_ORIGIN," \
    test/idp/keycloak-realm.json.tmpl > "$dir/keycloak-realm.json")
chmod 0644 "$dir/keycloak-realm.json" # read by the unprivileged user of the container

docker compose -f test/idp/compose.yaml up -d --wait

ca="$dir/tls/ca.pem"
kc_issuer="https://localhost:$PANDOKOS_IDP_KEYCLOAK_PORT/realms/pandokos"
hy_issuer="https://localhost:$PANDOKOS_IDP_HYDRA_PORT/"
ak_issuer="https://localhost:$PANDOKOS_IDP_AUTHENTIK_PORT/application/o/pandokos-tests/"
ak_token_url="https://localhost:$PANDOKOS_IDP_AUTHENTIK_PORT/application/o/token/"

# Neither image has a tool a health check could use: wait from here.
wait_for() {
    n=0
    until curl -fsS --cacert "$ca" -o /dev/null "$1" 2>/dev/null; do
        n=$((n + 1))
        if [ "$n" -ge "${2:-120}" ]; then
            echo "idp: $1 did not answer in ${2:-120} s" >&2
            docker compose -p pandokos-testidp logs --tail 50 >&2 || true
            exit 1
        fi
        sleep 1
    done
}
wait_for "$kc_issuer/.well-known/openid-configuration"
wait_for "${hy_issuer}health/ready"
# Authentik migrates its database first (a minute or two), then the worker applies the blueprint
# (test/idp/authentik-blueprint.yaml) in one transaction: the web certificate of the brand is its last
# visible effect: the certificate is on the default brand and the provider has its signing key
# (without one the tokens are HS256). The server reads that certificate when it starts (its watcher
# notices a later change only after minutes), so it is restarted once the blueprint is in.
n=0
until [ "$(docker compose -f test/idp/compose.yaml exec -T authentik-db psql -U authentik -d authentik -Atc \
    "SELECT (SELECT count(*) FROM authentik_brands_brand b JOIN authentik_crypto_certificatekeypair c ON c.kp_uuid = b.web_certificate_id WHERE b.\"default\" AND c.name = 'pandokos-web')::text || (SELECT count(*) FROM authentik_providers_oauth2_oauth2provider WHERE signing_key_id IS NOT NULL)" 2>/dev/null)" = 11 ]; do
    n=$((n + 1))
    if [ "$n" -ge 300 ]; then
        echo "idp: Authentik did not apply the blueprint in 300 s" >&2
        docker compose -p pandokos-testidp logs --tail 50 authentik-worker >&2 || true
        exit 1
    fi
    sleep 1
done
docker compose -f test/idp/compose.yaml restart authentik-server >/dev/null
# An application that exists answers its discovery document over the certificate of up.sh, and a
# workload then gets a token (the secrets are those of the blueprint).
wait_for "${ak_issuer}.well-known/openid-configuration" 300
n=0
until curl -fsS --cacert "$ca" "$ak_token_url" \
    -d grant_type=client_credentials -d client_id=pandokos-tests \
    -d "client_secret=$PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_SECRET" \
    -d username=workload-a -d "password=$PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A" 2>/dev/null \
    | python3 -c '
import base64, json, sys
t = json.load(sys.stdin)["access_token"].split(".")[0]
sys.exit(0 if json.loads(base64.urlsafe_b64decode(t + "=" * (-len(t) % 4)))["alg"] == "RS256" else 1)
' 2>/dev/null; do
    n=$((n + 1))
    if [ "$n" -ge 120 ]; then
        echo "idp: Authentik did not issue a token in 120 s" >&2
        docker compose -p pandokos-testidp logs --tail 50 authentik-worker authentik-server >&2 || true
        exit 1
    fi
    sleep 1
done

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
    printf 'PANDOKOS_TEST_IDP_ORIGIN=%s\n' "$PANDOKOS_TEST_IDP_ORIGIN"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_PEOPLE_CLIENT=pandokos-admin\n'
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_PEOPLE_SECRET=%s\n' "$PANDOKOS_TEST_IDP_KEYCLOAK_PEOPLE_SECRET"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_ALICE_PASSWORD=%s\n' "$PANDOKOS_TEST_IDP_KEYCLOAK_ALICE_PASSWORD"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_BOB_PASSWORD=%s\n' "$PANDOKOS_TEST_IDP_KEYCLOAK_BOB_PASSWORD"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_ADMIN_URL=https://localhost:%s\n' "$PANDOKOS_IDP_KEYCLOAK_PORT"
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_ADMIN_USER=admin\n'
    printf 'PANDOKOS_TEST_IDP_KEYCLOAK_ADMIN_PASSWORD=%s\n' "$PANDOKOS_TEST_IDP_KEYCLOAK_ADMIN_PASSWORD"
    printf 'PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT=tenant-workload\n'
    printf 'PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT=%s\n' "$PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT"
    printf 'PANDOKOS_TEST_IDP_HYDRA_CLIENT_UNMAPPED=unmapped-workload\n'
    printf 'PANDOKOS_TEST_IDP_HYDRA_SECRET_UNMAPPED=%s\n' "$PANDOKOS_TEST_IDP_HYDRA_SECRET_UNMAPPED"
    printf 'PANDOKOS_IDP_HYDRA_SYSTEM_SECRET=%s\n' "$PANDOKOS_IDP_HYDRA_SYSTEM_SECRET"
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_ISSUER=%s\n' "$ak_issuer"
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_TOKEN_URL=%s\n' "$ak_token_url"
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_ID=pandokos-tests\n'
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_SECRET=%s\n' "$PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_SECRET"
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_USER_A=workload-a\n'
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A=%s\n' "$PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A"
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_USER_B=workload-b\n'
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_B=%s\n' "$PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_B"
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_USER_C=workload-c\n'
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_C=%s\n' "$PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_C"
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_USER_UNMAPPED=workload-unmapped\n'
    printf 'PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_UNMAPPED=%s\n' "$PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_UNMAPPED"
    printf 'PANDOKOS_IDP_AUTHENTIK_SECRET_KEY=%s\n' "$PANDOKOS_IDP_AUTHENTIK_SECRET_KEY"
    printf 'PANDOKOS_IDP_AUTHENTIK_DB_PASSWORD=%s\n' "$PANDOKOS_IDP_AUTHENTIK_DB_PASSWORD"
    printf 'PANDOKOS_IDP_AUTHENTIK_ADMIN_PASSWORD=%s\n' "$PANDOKOS_IDP_AUTHENTIK_ADMIN_PASSWORD"
    printf 'PANDOKOS_IDP_AUTHENTIK_ADMIN_TOKEN=%s\n' "$PANDOKOS_IDP_AUTHENTIK_ADMIN_TOKEN"
} > "$dir/env")
echo "identity providers are up; settings are in $dir/env"
