# Changelog — battlenet

Generated from git history on 2026-09-15. `git log` stays the source
of truth; this file is the readable summary.

## v0.3.2 — 2026-10-10

- **No personal data in logs.** Battlenet subjects, game names and session addresses appear
  only as short per-process tags (`redact.Tag`, keyed HMAC with a key random per start). The
  web tier logs paths, header names and body sizes, not query strings, header values or
  bodies; frame and packet hex, relay bodies and RPC bodies are off unless
  `BATTLENET_TRACE_BODIES=1` (local debugging only). Net and URL errors are logged without
  their addresses and URLs. `TestLogsCarryNoPersonalData` fails if a known id, token, name
  or address reaches the log.

## v0.3.1 — 2026-10-10

- **The Game List actually lists.** The client reads only GameListResponse field 3; v0.3.0 sent
  its rows as field 1, so the screen still said "No games found". Rows are now GameListInfo
  {2 players, 3 max, 6 name, 8 site, 9 difficulty, 13 desecrated}, recovered from the list screen
  itself (docs/d2r-protocol.md, "CORRECTION 2026-10-10 (v0.3.1)").
- **The request's filters are applied:** difficulty masks (fields 7 and 10), the viewer's
  hardcore/ladder flags (4) and game type (12) against the owner's character, desecrated (11) and
  name search (3); at most 40 rows. Field 8 echoes the viewer's preferred site so the row lands in
  its best ping bucket. The log line names the filters, never the search text.

## v0.3.0 — 2026-10-10

- **Characters belong to accounts.** The character roster was one list for the whole box, so a
  second account (two consoles behind one address, or two emulators on one PC, all arriving
  through Traefik) saw, played and could delete the first account's characters. Rosters are now
  keyed by the battlenet subject the D2R-services session proves in AuthSession; an account with
  no characters gets an empty list. `characters.json` gains an `accounts` map. A v0.2.0 store's
  shared roster is kept but served to nobody until `BATTLENET_CHARACTER_LEGACY_OWNER` names its
  owner's battlenet subject (read once at startup, then written into that account's roster).
- **Signed D2R-services token.** LoginResponse field 2 (echoed in AuthSession) was the bare
  `openpak:<subject>`, so a client could claim any account. It is now
  `openpak:<subject>.<mac>` (HMAC-SHA256; key `BATTLENET_SESSION_KEY`, else derived from
  `ACCOUNT_INTERNAL_KEY`, else random per process). A token that does not verify gives the
  session no account: empty roster, no create, no join. Clients log in again after the deploy.
- **Game List.** GameManagement.GameList (0xa5ea9f6d) was answered empty ("No games found").
  It now returns `GameListResponse {1 repeated GameListInfo {1 players, 2 max, 3 name, 4
  difficulty}}`, layout recovered from the update build (docs/d2r-protocol.md). Listed: live
  games with a free seat, no password, and the viewer's SetGameVersion. A game is seated by
  its game-server connections, closes when the last one drops, and is dropped if nobody reaches
  it within 2 minutes. GameJoin refuses full and closed games; GameCreate no longer lets another
  account take over a live game's name.
- **Game server.** Pending joins queue per account (oldest first, 30 s) instead of one box-wide
  slot, and resolve the character on the joining account's own roster. The 2-minute connection
  deadline is now an idle timeout; it used to end every game at the 2-minute mark.
- **Logs.** D2RSVC names Character and GameManagement methods instead of `unknown`. AuthSession,
  Character and GameCreate/GameJoin bodies, and character names, are no longer logged.

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

