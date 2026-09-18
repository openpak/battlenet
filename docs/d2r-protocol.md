# D2R online protocol — what the title expects

Facts recovered from the Diablo II: Resurrected (Switch, 1.0.34.0) binary and
from our own gateway logs, recorded per the clean-room policy: field numbers,
wire types, flow. Addresses are text VAs in the decompressed NSO (see
`private notes` for the toolkit); the working
scripts live in `/tmp/opencode/d2r/` (`pooldump.py`, `jtscan.py`, `relocs.py` —
the NSO does carry RELA relocations, so vtables resolve exactly).

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
- Header field 5 is the payload size on every frame, pushes and responses
  alike (the captured "timeouts" 13/0/3 and the ConnectResponse's 168 are
  exactly the payload lengths); a frame without it is read with an empty
  body.
- CreateSession (SessionService 0x1e688c05) must be answered with
  CreateSessionResponse {1: session_id} and followed by the push
  SessionListener.OnSessionCreated (0x7fe36b32, method 1)
  {1: identity, 2: reason, 3: session_id, 4: session_key}; without the push
  the post-logon sequence never starts. The handler (CSessionHandler
  0xcf98f8 → 0x1ca438) with reason 0 stores field 3 as the session id and
  sets OnlineStatus 4; non-zero reason → OnlineStatus 5.
- ContentHandle (GetContentHandle's response) has required fields; an empty
  body does not parse.
- DestroySession's only sender is the Login state's stop (0x2073fc →
  0x1ca838). It fired 15 s after every logon because the Login state timed
  out (see below), carrying an empty id because our OnSessionCreated push
  had header 5 = 0.

## D2R login (inside GameUtilities.ProcessTask)

ProcessTaskRequest attributes (bgs.protocol.v2.Attribute {1 name, 2 Variant
{4 string, 5 blob}}): `server_instance` = "Release", `client_request` =
"classic.protocol.v1.d2r_connection.LoginRequest", `protobuf_request` =
LoginRequest bytes. The answer is ProcessTaskResponse {1: result attributes}
with `client_response` = "classic.protocol.v1.d2r_connection.LoginResponse"
and `protobuf_response` = the raw LoginResponse bytes, **both as
Variant.string_value (4)**: the callback (0x203d64) reads only that member,
so a blob_value arrives as "" and parses as an empty LoginResponse. A
missing or mismatched client_response fails the logon at once ("Failed to
authenticate"). This is the only place LoginResponse is read — the HTTPS
check_token call belongs to the web/NSA login (0x1eec9c), not to it.

| Message | Fields |
|---|---|
| LoginRequest | 1 rep string, 2 u32 program fourcc ('OSI'), 3 string client version, 4 u32 platform fourcc ('Swch') |
| LoginResponse | 1 string **D2R-services base URL**, 2 string **session token**, 3 bool, 4 GameUpdateRequest {1 string, 5 u64} — only 1 and 2 are read |

## D2R-services connection (going online)

- The online flow runs as NetworkStateObjects (A, B = bgs online, Login,
  Connect), each failed with error 666 if still running after 15 s (driver
  0x1c4224). Login (enter 0x207360 sends LoginRequest; update 0x207490)
  succeeds once LoginResponse fields 1 and 2 are stored; its stop sends
  DestroySession. Connect (enter 0x207b30 → dial 0x204324; update 0x207c7c)
  must see the D2R connection authenticated within 15 s.
- Dial gate 0x204324: OnlineStatus == 4 (bgs logon state 5 = OnLogonComplete
  succeeded, and CreateSession returned status 0), both LoginResponse strings
  non-empty, and the session back-pointer set.
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
| classic.protocol.Header | 1 service hash, 2 method hash, 3 request token, 4 client check value, 5 payload size, 6 status (u64, 0 = OK), 9 is_response, 12 trace label (unread) |

Framing and RPC (decoded from the send path 0x17ff050, receive path
0x17fb93c, response match 0x17fc908/0x17fcd80, all high confidence):

- Binary WebSocket messages, one RPC each: `[u16 BE header length]
  [Header][payload]`; the payload is parsed with exactly Header.5 bytes.
- A response is matched on field 3, and its parser is chosen by fields 1+2 —
  so a response echoes 1, 2 and 3, sets 9 = 1 and 5 = body length; status
  omitted means 0. Field 9 = 0 marks a server-initiated request (dispatched
  on 1+2; checked against the client's field 4 when
  `Rpc.WebSocket.UseCheckValue` is on).
- AuthSession: service 0xeeb6ab22, method 0xed028f6e. Success needs status
  0; the client then reads AuthSessionResponse field 9 (string) and field 10
  (u32) into its "session started" event. Empty-token response bytes:
  `0012 08a2d6daf50e 10ee9e8ae80e 1801 2800 4801`.
- Next the client calls GameVersion.SetGameVersion (method 0xd48de460,
  SetGameVersionRequest {1 string}, response classic.protocol.Empty; a
  failure is only logged). SessionUpdate, SessionDestroyed, CookieUpdate and
  ServerPing on D2rServicesConnection are server pushes, as are
  GameVersion's GameUpdateRequired/GameUpdateCanceled.
- Upgrade: most likely no Sec-WebSocket-Protocol from the client (the option
  feeding it is left empty); no extensions should be accepted.

Earlier names corrected by this decode: the layout once called
AuroraAccountInfo is AuthSessionResponse; the parser at 0x16d917c is
SessionUpdateRequest; the {1 string, 5 varint} parser at 0x17155d0 is
game_version.GameUpdateRequest.

## Open

- First live capture of the D2R-services WebSocket (blocked on the dial
  gate below).
- The empty-session DestroySession above. 2026-09-18 21:23–21:29: with the
  corrected LoginResponse served in-band at logon, DestroySession still
  followed at +15 s, and pressing Online five minutes later dialed nothing —
  the connect step also requires the bgs session state, so the teardown is
  the prime suspect for the missing dial.
