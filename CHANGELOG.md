# Changelog — battlenet

Generated from git history on 2026-09-15. `git log` stays the source
of truth; this file is the readable summary.

## v0.2.0 — 2026-10-09

- **Version check at login.** Every authenticated gateway login asks nx-baas
  `/internal/title-version` with the id_token's `nintendo.ai`/`nintendo.av` (3 s timeout,
  `NX_INTERNAL_URL`/`NX_INTERNAL_KEY`) and logs the verdict as a `VERSION` line. It fails open:
  no answer, a non-200 or a token without `av` means allowed. Nothing is refused yet. The title
  ignores LoginResponse field 4 (disassembly of both builds), so the LoginResponse bytes are
  unchanged. The game's own path is the GameVersion.GameUpdateRequired push (method
  0x01b1b8b2, GameUpdateRequest {1 string, 5 u64}). It is sent after SetGameVersion only when
  `BATTLENET_GAME_UPDATE_PUSH` is set, until a run shows what the title does with it.
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

