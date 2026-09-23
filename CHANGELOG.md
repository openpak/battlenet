# Changelog — battlenet

Generated from git history on 2026-09-15. `git log` stays the source
of truth; this file is the readable summary.

## Unreleased

- Bans (website/docs/ban-lookup.md): sign-in (`/login/check_token` and the gateway's
  authenticated Connect) asks the core's `Identity.GetAccount` for the account behind the
  switch link and refuses anything not ACTIVE (banned, deletion pending, deleted, unknown)
  through the existing refusal (`INVALID_TOKEN` / connection closed). A core that cannot answer
  refuses the login. Sessions already open are not re-checked. No new environment variables.

## History — no releases tagged yet

- Gateway: auth-listener channel map and logon-completion knobs [e43eb56]
- Battle.net web tier and native D2R gateway [8527c68]

