# D2R online protocol — what the title expects

Facts recovered from the Diablo II: Resurrected (Switch, 1.0.34.0) binary and
from our own gateway logs, recorded per the clean-room policy: field numbers,
wire types, flow. Addresses are text VAs in the decompressed NSO (see
`private notes` for the toolkit); the working
scripts live in `/tmp/opencode/d2r/` (`pooldump.py`, `jtscan.py`).

## Sources

| Fact family | Source |
|---|---|
| bgs services, hashes, method ids, message fields | Descriptor pool embedded in the binary (FULL runtime); service name from option extension 90000 field 1, method id from the method option. Generated into `cmd/battlenet/bgs_services.go`. |
| classic.protocol.v1.d2r_connection message layouts | LITE parse functions (jump-table dispatch), tied to type names by class block order and vtable use; cross-checked against captured LoginRequest bytes. |
| Online flow | Disassembly of the ProcessTask callback (0x203d64), state machine (0x207490), connect step (0x204324), URL builder (0x162785c), AuthSession builder (0x162a850), result check (0x162b87c). |
| Wire behaviour | Dev-server gateway logs, 2026-09-18. |

## bgs layer (the `*.actual.battle.net:1119` gateway)

- RPC header field 11 (fixed32) is the target service hash: FNV-1a of the
  service's original `bnet.protocol.*` name. Every server push captured from
  Blizzard went to `AuthenticationClient` (0x71240e35).
- Post-logon sequence, as sent by the title on 2026-09-18:
  AuthenticationServer.GenerateWebCredentials (8), SessionService.CreateSession
  (1), GenerateWebCredentials again, GameUtilities.ProcessTask (1),
  Presence.BatchSubscribe (8), ChannelMembership.Subscribe (1) ×2,
  Resources.GetContentHandle (1), Account.GetAccountState (30),
  Presence.BatchSubscribe.
- CreateSession (SessionService 0x1e688c05) must be answered with
  CreateSessionResponse {1: session_id} and followed by the push
  SessionListener.OnSessionCreated (0x7fe36b32, method 1)
  {1: identity, 2: reason, 3: session_id, 4: session_key}; without the push
  the post-logon sequence never starts.
- ContentHandle (GetContentHandle's response) has required fields; an empty
  body does not parse.
- Unexplained: ~15 s after logon the title calls DestroySession with an empty
  session_id (send path 0xcf925c → 0xf4e060).

## D2R login (inside GameUtilities.ProcessTask)

ProcessTaskRequest attributes (bgs.protocol.v2.Attribute {1 name, 2 Variant
{4 string, 5 blob}}): `server_instance` = "Release", `client_request` =
"classic.protocol.v1.d2r_connection.LoginRequest", `protobuf_request` =
LoginRequest bytes. The answer is ProcessTaskResponse {1: result attributes}
with `client_response` = "classic.protocol.v1.d2r_connection.LoginResponse"
and `protobuf_response` = LoginResponse bytes; a missing LoginResponse fails
the logon with "Failed to authenticate". The web `check_token` answer carries
the same pair as JSON (`protobuf_response` base64), and `result` is read as a
number (0 = proceed).

| Message | Fields |
|---|---|
| LoginRequest | 1 rep string, 2 u32 program fourcc ('OSI'), 3 string client version, 4 u32 platform fourcc ('Swch') |
| LoginResponse | 1 string **D2R-services base URL**, 2 string **session token**, 3 bool, 4 GameUpdateRequest {1 string, 5 u64} — only 1 and 2 are read |

## D2R-services connection (going online)

- Gate: the online state machine dials only when LoginResponse fields 1 and 2
  are both non-empty and the bgs session is up.
- URL: field 1 with any `v1/rpc/client` removed, `/` ensured, then
  `OSI/v2/rpc/client` appended. Scheme `ws:` is plain (default port 8080),
  anything else TLS (default port 443); the host is the third `/`-separated
  part. `wss://us.actual.battle.net:1119` → `…:1119/OSI/v2/rpc/client`.
- Transport: bgs-sdk `WebsocketRpcClient` (connect at 0x17eb77c).
- First call: D2rServicesConnection.AuthSession — D2rServicesConnection is an
  RPC service (stub 0x16de3e4: AuthSession, SessionUpdate, SessionDestroyed,
  CookieUpdate, ServerPing), not a message. The request carries field 1 =
  LoginResponse field 2 (the token).
- Success needs only RPC status 0 on the AuthSession response; non-zero →
  "Failed to authenticate session with D2R services", no connection →
  "Failed to start session with D2R-Services [Connection Failed]".

| Message | Fields |
|---|---|
| AuthSessionRequest | 1 string token, 2 SessionInfo, 3 u32 ('OSI'), 4 bytes, 5 bytes, 6 u32 (0x30100), 7 u32 ('Swch'), 8 bool, 9 rep string, 10 string |
| AuthSessionResponse | 1 rep string, 3 bytes, 4 varint, 5 varint, 6 varint, 7 OnlineStats, 8 varint, 9 bytes, 10 varint |
| SessionInfo | 1 string, 2 u32, 3 u32, 4 u32 ('Swch'), 5 bytes, 6 AuroraAccountInfo, 7 varint, 8 varint |
| AuroraAccountInfo | 1–4 u64 (1 = 0x0100000000000000, 2–4 account/game-account id words) |
| SessionUpdateRequest | 1 string, 2 AuthSessionResponse |
| OnlineStats | 1–3 varint |
| OfflineCookie / OfflineCookies / CookieUpdateRequest | {1 bytes, 2 string, 3 string, 4 varint} / {5 rep OfflineCookie} / {1 OfflineCookie} |
| SessionDestroyedRequest, ClientPing, ServerPingRequest, ServerPingResponse | {1 varint} |
| classic.protocol.Header | 1–5 u32, 6 u64, 9 varint, 12 string (meanings pending) |

Earlier names corrected by this decode: the layout once called
AuroraAccountInfo is AuthSessionResponse; the parser at 0x16d917c is
SessionUpdateRequest; the {1 string, 5 varint} parser at 0x17155d0 is
game_version.GameUpdateRequest.

## Open

- Frame format and Header field meanings on the D2R-services WebSocket, and
  the exact AuthSession response bytes (decode in progress; first capture
  pending).
- The empty-session DestroySession above.
