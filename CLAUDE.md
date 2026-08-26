# Agent Instructions

## Issue tracker

Issues live in GitHub Issues (`ericdahl-dev/coolify-green`). See `docs/agents/issue-tracker.md`.

## Triage labels

Default canonical label vocabulary (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

## Domain docs

Single-context repo — `CONTEXT.md` at root + `docs/adr/`. See `docs/agents/domain.md`.

## Talking to a real Coolify instance

Never hardcode a token. The studio instance's URL and token are Doppler secrets
(`COOLIFY_BASE_URL`, `COOLIFY_API_KEY` in `ericdahl-dev/prd`); run one-off probes through
`doppler run -p ericdahl-dev -c prd --command '...'`.

## Non-Interactive Shell Commands

**ALWAYS use non-interactive flags** with file operations to avoid hanging on confirmation prompts.

Shell commands like `cp`, `mv`, and `rm` may be aliased to include `-i` (interactive) mode on some systems.

```bash
cp -f source dest
mv -f source dest
rm -f file
rm -rf directory
```
