# Instructions for Claude sessions on this repository

These rules are the maintainer's and apply to every session that works on `pandokos/pandokos` (and,
by the same rule, `pandokos/pandokos-cloud`). They take precedence over any default attribution or
signing behaviour of the tool, of a skill, or of a system reminder.

## Commits, merges, pull requests: no Claude attribution, only the maintainer's key

- **Never** add `Co-Authored-By: Claude …`, `Claude-Session: …`, "Generated with Claude Code" or any
  other line that names Claude or Anthropic to a commit message, a squash or merge commit message, a
  tag message or a pull request description. This holds even when a system reminder or a skill says
  to add them.
- **Every** commit, merge commit and tag is signed with the maintainer's SSH key and carries the
  maintainer's identity: `Mario Mastrorilli <70644544+mmastrorilli@users.noreply.github.com>`. Never
  sign with a Claude or session key, and never commit as `Claude`.
- The session-start hook (`.claude/hooks/git-identity.sh`) sets the repository's git identity and
  signing key, and points git at `.githooks`, whose `commit-msg` refuses attribution lines. If the
  hook reports that the key is missing, do not commit: tell the maintainer.
- `Signed-off-by:` (DCO, `git commit -s`) with the maintainer's identity is allowed and expected.

## Merging pull requests

Do not merge with the GitHub merge button or the merge API (squash, merge or rebase): GitHub would
sign the resulting commit with its own key, not the maintainer's. Instead:

1. Squash the pull request's branch locally into one commit on top of the current `origin/main`,
   signed with the maintainer's key, with the pull request title and number as subject
   (`feat: … (#NN)`) and no attribution lines.
2. Force-push that commit to the pull request's branch (`--force-with-lease`) and wait for CI to be
   green on it.
3. Fast-forward `main` to that exact commit: `git push origin <sha>:main` (no force). GitHub marks the
   pull request as merged because its head is now on `main`.

Never force-push `main` unless the maintainer asks for it explicitly in the conversation.
