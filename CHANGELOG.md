# Changelog — battlenet

Generated from git history on 2026-09-15. `git log` stays the source
of truth; this file is the readable summary.

## Unreleased

- **Game-server transport.** After GameJoin the title dials field 1 on TCP 443 with its own
  OpenSSL and expects the Diablo II game server to speak first; the peer-hosted model was wrong.
  GameJoin now answers `1 = d2gs.battle.net` (`BATTLENET_GAME_HOST`), `3`/`4` = the game id, no
  field 2 or 6. A listener on `:21503` (`BATTLENET_GAME_ADDR`, behind Traefik's `d2gs.battle.net`
  TLS router) speaks the handshake (`AF 00`, logon, flags, `00`, `02`, pongs) and logs every packet
  after it; the in-game stream is the next piece. The title trusts our certificate only through
  the IPS32 patch in `tools/d2r-trust`. The `:21502` probe sink is gone.

## v0.1.0 — 2026-09-24

First tagged release: `ghcr.io/openpak/battlenet` for amd64 and arm64. Carries the
Battle.net web tier, the native D2R gateway (bgs logon, D2R-services session, character
roster with ID/JSON encoding, GameJoin host/port path), the game probe sink on :21502,
and bans through the core's `Identity.GetAccount` at sign-in.


- Bans (website/docs/ban-lookup.md): sign-in (`/login/check_token` and the gateway's
  authenticated Connect) asks the core's `Identity.GetAccount` for the account behind the
  switch link and refuses anything not ACTIVE (banned, deletion pending, deleted, unknown)
  through the existing refusal (`INVALID_TOKEN` / connection closed). A core that cannot answer
  refuses the login. Sessions already open are not re-checked. No new environment variables.

## History before v0.1.0

- Gateway: auth-listener channel map and logon-completion knobs [e43eb56]
- Battle.net web tier and native D2R gateway [8527c68]

