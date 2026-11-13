#!/bin/sh
# Starts the test PostgreSQL and PgBouncer and writes local/testdb/env.
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
docker compose -f test/db/compose.yaml up -d --wait
(umask 077 && {
    printf 'PANDOKOS_TEST_DATABASE_URL=postgres://postgres:%s@127.0.0.1:55432/postgres?sslmode=disable\n' "$pw"
    printf 'PANDOKOS_TEST_POOLER_ADDR=127.0.0.1:56432\n'
    printf 'PANDOKOS_TEST_DATABASE_CA_FILE=%s/%s/tls/ca.pem\n' "$(pwd)" "$dir"
} > "$dir/env")
echo "test database is up; settings are in $dir/env"
