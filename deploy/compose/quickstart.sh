#!/bin/sh
# Pandokos quick start: prepares and starts the Compose reference, then creates a tenant "demo", stores
# its OpenAI key in the vault and issues a virtual key into ./quickstart.key. Explains each step and,
# on a terminal, waits for Enter before it. A step already done is skipped, so a rerun is safe.
# A local trial, not a production setup. Guide: docs/operations/compose.md.
#
# Usage: ./quickstart.sh [--yes]
#   --yes  do not wait between steps (also the case when stdin or stdout is not a terminal)
# The OpenAI key is read from OPENAI_API_KEY, or asked for on the terminal without echo. It goes to
# Pandokos through standard input and is never written to disk.
set -eu
cd "$(dirname "$0")"

boot=/home/nonroot/bootstrap.key
admin=/home/nonroot/demo-admin.key
adminpw=/home/nonroot/admin.password
keyfile=quickstart.key
total=5

pause=1
for arg in "$@"; do
  case $arg in
    -y | --yes) pause=0 ;;
    -h | --help) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "quickstart.sh: unknown argument $arg (try --help)" >&2; exit 2 ;;
  esac
done
tty=0
if [ -t 0 ] && [ -t 1 ]; then tty=1; fi
if [ "$tty" = 0 ]; then pause=0; fi

die() { printf '\nquickstart.sh: %s\n' "$*" >&2; exit 1; }
say() { for line in "$@"; do printf '      %s\n' "$line"; done; }
step() {
  printf '\n[%s/%s] %s\n' "$1" "$total" "$2"
  shift 2
  say "$@"
}
show() { printf '      $ %s\n' "$*"; }
wait_enter() {
  if [ "$pause" = 1 ]; then
    printf '      Press Enter to continue, Ctrl-C to stop... '
    read -r _
  fi
}
done_() { printf '      ok\n'; }
# quiet MESSAGE CMD...: runs CMD and shows its output only when it fails, then stops with MESSAGE.
quiet() {
  msg=$1
  shift
  if ! out=$("$@" 2>&1); then
    printf '%s\n' "$out" >&2
    die "$msg"
  fi
}
skipped() { printf '      already done, skipped\n'; }
# pdk KEYFILE ARGS...: the Pandokos CLI inside the container, authenticated by a key file there.
pdk() {
  k=$1
  shift
  docker compose exec -T -e PANDOKOS_CLI_KEY_FILE="$k" pandokos /pandokos "$@"
}

printf 'Pandokos quick start: a local trial, not a production setup.\n'
printf 'It runs Pandokos and PostgreSQL with Docker Compose in %s.\n' "$(pwd)"

command -v docker >/dev/null 2>&1 || die "docker is required: https://docs.docker.com/get-docker/"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required (the \"docker compose\" command)"
command -v openssl >/dev/null 2>&1 || die "openssl is required to create the TLS certificate"

step 1 "Create the secrets" \
  "Five database passwords, the vault's key-encryption key and a self-signed TLS" \
  "certificate, as files in ./secrets. Containers read them as files, never as" \
  "environment values. Existing files are kept."
show ./setup.sh
wait_enter
./setup.sh >/dev/null
done_

step 2 "Start PostgreSQL and Pandokos" \
  "PostgreSQL runs on an internal network with no route out and is not published." \
  "The schema is migrated, then Pandokos serves TLS on 127.0.0.1:8080 (inference," \
  "for applications) and 127.0.0.1:8081 (administration, for the CLI)."
show docker compose up -d --wait
wait_enter
docker compose up -d --wait </dev/null
done_

step 3 "Create the tenant demo" \
  "init issues the first platform key (a bootstrap key, valid for one day) and the first" \
  "administrator, a person who signs in with a generated password that must be changed;" \
  "with the key the tenant demo is created, with an administration key that acts only on demo." \
  "The keys and the password stay in the container's volume, /home/nonroot."
show docker compose exec pandokos /pandokos init --key-out $boot --admin-username admin --password-out $adminpw
show docker compose exec -e PANDOKOS_CLI_KEY_FILE=$boot pandokos /pandokos tenant create demo --admin-key-out $admin
wait_enter
if pdk $admin service-account list </dev/null >/dev/null 2>&1; then
  skipped
else
  # A new installation: a key file left by an earlier one (docker compose down -v) is useless.
  rm -f "$keyfile"
  reset="start again from nothing: docker compose down -v && ./quickstart.sh"
  quiet "init failed; if an earlier run stopped half-way, $reset" \
    docker compose exec -T pandokos /pandokos init --key-out $boot --admin-username admin --password-out $adminpw </dev/null
  quiet "tenant create failed; $reset" pdk $boot tenant create demo --admin-key-out $admin </dev/null
  done_
fi

step 4 "Store the tenant's OpenAI key in the vault" \
  "The key goes through standard input, is encrypted in the database and can never" \
  "be read back, by the API or by the CLI. Pandokos injects it into each call: BYOK," \
  "the tenant uses its own provider account."
# shellcheck disable=SC2016 # the command is shown, not run
show 'printf %s "$OPENAI_API_KEY" | pdk credential create --provider openai --secret-file /dev/stdin openai-main'
wait_enter
if pdk $admin credential list -o json </dev/null 2>/dev/null | grep -q '"label": *"openai-main"'; then
  skipped
else
  if [ -z "${OPENAI_API_KEY:-}" ]; then
    [ "$tty" = 1 ] || die "set OPENAI_API_KEY to your OpenAI API key, or run on a terminal to be asked for it"
    printf '      OpenAI API key (not shown): '
    trap 'stty echo 2>/dev/null' EXIT INT TERM
    stty -echo
    IFS= read -r OPENAI_API_KEY
    stty echo
    trap - EXIT INT TERM
    printf '\n'
    [ -n "$OPENAI_API_KEY" ] || die "no key given"
  fi
  printf %s "$OPENAI_API_KEY" | quiet "credential create failed" \
    pdk $admin credential create --provider openai --secret-file /dev/stdin openai-main
  done_
fi

step 5 "Issue a virtual key for an application" \
  "A service account with the inference role, and a key for it. Applications call" \
  "Pandokos with this key; they never see the OpenAI key. It is written to" \
  "./$keyfile and shown only once by Pandokos."
show pdk service-account create --role inference app
show "pdk key create --service-account app > $keyfile"
wait_enter
if pdk $admin service-account get app </dev/null >/dev/null 2>&1; then
  :
else
  quiet "service-account create failed" pdk $admin service-account create --role inference app </dev/null
fi
if [ -s "$keyfile" ]; then
  skipped
else
  (
    umask 077
    pdk $admin key create --service-account app </dev/null | awk '/^key:/ {print $2}' >"$keyfile"
  )
  grep -q '^pdk_' "$keyfile" || { rm -f "$keyfile"; die "key create did not return a key"; }
  done_
fi

cat <<EOF

Pandokos is ready. Call OpenAI through it; only the address and the key change:

  curl -sS --cacert secrets/tls_cert https://localhost:8080/openai/v1/chat/completions \\
    -H "Authorization: Bearer \$(cat $keyfile)" -H "Content-Type: application/json" \\
    -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Say hello from Pandokos."}]}'

The usage record of each call (tokens, estimated cost, no content):

  docker compose exec -T -e PANDOKOS_CLI_KEY_FILE=$admin pandokos /pandokos usage list

Remove everything:  docker compose down -v && rm $keyfile
EOF
