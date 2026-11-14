#!/bin/sh
# Runs once, when the data directory is created: the privilege roles of Pandokos as login users with
# the passwords of ./secrets, and the database ("Shortest" setup of docs/operations/database.md).
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres -f /pandokos/roles.sql
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<'SQL'
\set owner_pw `cat /run/secrets/db_owner`
\set gateway_pw `cat /run/secrets/db_gateway`
\set admin_pw `cat /run/secrets/db_admin`
\set worker_pw `cat /run/secrets/db_worker`
ALTER ROLE pandokos_owner   LOGIN PASSWORD :'owner_pw';
ALTER ROLE pandokos_gateway LOGIN PASSWORD :'gateway_pw';
ALTER ROLE pandokos_admin   LOGIN PASSWORD :'admin_pw';
ALTER ROLE pandokos_worker  LOGIN PASSWORD :'worker_pw';
CREATE DATABASE pandokos OWNER pandokos_owner;
REVOKE ALL ON DATABASE pandokos FROM PUBLIC;
GRANT CONNECT ON DATABASE pandokos TO pandokos_gateway, pandokos_admin, pandokos_worker;
SQL
