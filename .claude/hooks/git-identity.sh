#!/bin/sh
# Session-start hook: commits in this repository carry the maintainer's identity and are signed
# with the maintainer's SSH key, never with a Claude or session key (see CLAUDE.md).
set -u
cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}" || exit 0

key="${HOME}/.ssh/pandokos_signing"
if [ ! -s "$key" ] && [ -n "${SIGNING_KEY_B64:-}" ]; then
	mkdir -p "${HOME}/.ssh" && chmod 700 "${HOME}/.ssh"
	umask 077
	printf '%s' "$SIGNING_KEY_B64" | base64 -d >"$key" 2>/dev/null || rm -f "$key"
fi

git config --local user.name "Mario Mastrorilli"
git config --local user.email "70644544+mmastrorilli@users.noreply.github.com"
git config --local core.hooksPath .githooks
git config --local gpg.format ssh
git config --local gpg.ssh.program ssh-keygen
git config --local commit.gpgsign true
git config --local tag.gpgsign true

if [ -s "$key" ] && ssh-keygen -y -f "$key" >/dev/null 2>&1; then
	chmod 600 "$key"
	git config --local user.signingkey "$key"
	echo "git: commits are signed with the maintainer's key ($key) as Mario Mastrorilli; no Claude attribution (CLAUDE.md)."
else
	git config --local user.signingkey "/nonexistent/maintainer-key-missing"
	echo "git: the maintainer's signing key is missing (~/.ssh/pandokos_signing or SIGNING_KEY_B64). Do NOT commit; tell the maintainer (CLAUDE.md)."
fi
exit 0
