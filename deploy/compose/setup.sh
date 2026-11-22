#!/bin/sh
# Prepares ./secrets for the Compose reference deployment: database passwords, the vault's
# key-encryption key, the Grafana admin password of the observability overlay and a self-signed TLS
# certificate. Run once from this directory. Needs sh,
# head, base64, tr and openssl. Never overwrites a file that exists. Guide: docs/operations/compose.md.
set -eu
umask 077
dir=${PANDOKOS_COMPOSE_SECRETS:-./secrets}
command -v openssl >/dev/null 2>&1 || { echo "setup.sh: openssl is required to create the TLS certificate" >&2; exit 1; }
mkdir -p "$dir"
random() { head -c 32 /dev/urandom | base64 | tr -d '\n'; }
for name in db_superuser db_owner db_gateway db_admin db_worker pandokos_kek grafana_admin; do
  if [ ! -e "$dir/$name" ]; then
    random > "$dir/$name"
    echo >> "$dir/$name"
  fi
done
if [ ! -e "$dir/tls_cert" ]; then
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 365 \
    -subj "/CN=pandokos-compose" \
    -addext "subjectAltName=DNS:localhost,DNS:pandokos,DNS:postgres,IP:127.0.0.1,IP:::1" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -keyout "$dir/tls_key" -out "$dir/tls_cert" 2>/dev/null
fi
# The containers run as other users (postgres 70, pandokos 65532) and read the files through bind
# mounts, so the files are readable by everyone and the directory by its owner only.
chmod 0700 "$dir"
chmod 0444 "$dir"/*
echo "secrets ready in $dir; the TLS certificate to trust is $dir/tls_cert"
