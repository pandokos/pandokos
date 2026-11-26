#!/bin/sh
# Starts the test PostgreSQL (16, or 18 with PANDOKOS_TEST_PG=18) and PgBouncer and writes local/testdb/env.
# The superuser password is generated here and never leaves the git-ignored directory local/.
set -eu
cd "$(dirname "$0")/../.."
dir=local/testdb
mkdir -p "$dir"
if [ ! -s "$dir/postgres_password" ]; then
    (umask 077 && od -An -N24 -tx1 /dev/urandom | tr -d ' \n' > "$dir/postgres_password")
fi
pw=$(cat "$dir/postgres_password")
# PgBouncer runs as a non-root user and must be able to read its user list.
(umask 022 && printf '"postgres" "%s"\n' "$pw" > "$dir/userlist.txt")
go run ./test/testcert/cmd/testcert -dir "$dir/tls" -dns pandokos-test-db -ip 127.0.0.1 -days 30
# PANDOKOS_TEST_PG=18 runs the server on PostgreSQL 18 (test/db/compose.pg18.yaml); the default is the floor, 16.
files="-f test/db/compose.yaml"
case "${PANDOKOS_TEST_PG:-16}" in
    16) ;;
    18) files="$files -f test/db/compose.pg18.yaml" ;;
    *) echo "up.sh: PANDOKOS_TEST_PG must be 16 or 18" >&2; exit 2 ;;
esac
# shellcheck disable=SC2086
docker compose $files up -d --wait
(umask 077 && {
    printf 'PANDOKOS_TEST_DATABASE_URL=postgres://postgres:%s@127.0.0.1:55432/postgres?sslmode=disable\n' "$pw"
    printf 'PANDOKOS_TEST_POOLER_ADDR=127.0.0.1:56432\n'
    printf 'PANDOKOS_TEST_DATABASE_CA_FILE=%s/%s/tls/ca.pem\n' "$(pwd)" "$dir"
} > "$dir/env")
echo "test database is up; settings are in $dir/env"
