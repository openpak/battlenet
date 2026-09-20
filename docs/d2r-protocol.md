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
  0xcf98f8 → 0x1ca438; parser 0xee8b84) with reason 0 stores **field 5
  (client_id)** as the session id — not field 3 — sets OnlineStatus 4 and
  clears the pending flag; non-zero reason → OnlineStatus 5. That id then
  rides as bgs header client_id (13) on ProcessTask and DestroySession.
- A 0xFE response is matched by 0xf564c4 on service_id 254 + token alone,
  per connection (no is_response/object_id/method check). Pushes resolve
  the service by header field 11 and the method by field 2.
- ContentHandle (GetContentHandle's response) has required fields; an empty
  body does not parse.
- AccountService.GetAccountState (method 30, service 0x62da0891) must be
  answered with a populated GetAccountStateResponse {1: AccountState, 2:
  AccountFieldTags} (layouts from the embedded `account_service.proto` /
  `account_types.proto` descriptors): AccountState {1: AccountLevelInfo
  {5: country, 6: preferred_region, 8: battle_tag}, 5: GameLevelInfo
  {8: name, 9: fixed32 program}, 7: GameAccountList {3: region, 4:
  GameAccountHandle {1: fixed32 id, 2: fixed32 program, 3: region}}}, tags
  marking each section present. An empty body parses but the D2R layer logs
  "Failed to retrieve account level session due to reason 4" and never
  starts the game-server stage.
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

## Full RPC surface (static map, 2026-09-20)

Recovered from the Switch 1.0.34.0 binary after the /tmp toolkit was lost
to a reboot: re-extracted with hactool from the surviving program NCA
(ExeFS `main` + NSO0 LZ4 segments, byte-identical to the previous dump),
then enumerated all 167 callers of the method-register function
(`bl 0x1693770`). Each service has one init that stores its hash
(`str w8,[x0,#8]` for classic services, `str w8,[x0]` for external ones)
and registers (service, method, name) descriptors — classic layout
`stp w8,wR,[sp,#0x30]` (method in w9, sometimes w10), external layout
`stp w8,w9,[sp]` with the service reloaded (`ldr w25,[x19]`). Method names
are corroborated by bare Ro strings and inline ASCII tails. Scripts live in
`~/d2r-work/` (`analyze.py`, `sigscan.py`, `enum_methods.py`,
`blockdump.py`, `constscan.py`, `resolve_names.py`, `methods.txt`);
the Ghidra headless full analysis runs from `~/d2r-work/ghidra_proj/`.

| Service | Hash | Method | Hash |
|---|---|---|---|
| D2rServicesConnection (init 0x16de3e4) | 0xeeb6ab22 | AuthSession | 0xed028f6e |
| | | SessionUpdate | 0x6e824a4f |
| | | SessionDestroyed | 0xf0652897 |
| | | CookieUpdate | 0x4ae685fd |
| | | ClientPing | 0xf1127ff6 |
| | | ServerPing | 0x0644b9e5 |
| GameVersion | 0x3d930f0e | SetGameVersion | 0xd48de460 |
| Character (init 0x16c31d4) | 0x607fd196 | CreateCharacter | 0x043afeb5 |
| | | DeleteCharacter | 0xa1d52098 |
| | | ReadCharacterList | 0x47821811 |
| | | UseCharacter | 0x6a706ac6 |
| GameManagement (init 0x16fea74) | 0x110f2741 | GameCreate | 0x930fbb99 |
| | | GameJoin | 0xa5cc1951 |
| | | GameList | 0xa5ea9f6d |
| | | GameDetail | 0xb5cd1354 |
| | | GameCreateCancel | 0x38274117 |
| | | GameCreateQueueTime | 0x5f2ddb05 |
| | | GameGetSitePingList | 0xbb6dcaa4 |
| Lobby (init 0x1730a64) | 0x7af1afbf | CreateLobby | 0x346da52d |
| | | DisbandLobby | 0xb45831a2 |
| | | LeaveLobby | 0xe5b584b2 |
| | | KickFromLobby | 0x7203f2bb |
| | | PromoteToLeader | 0x72c4d55f |
| | | InviteToLobby | 0x2e8b08b7 |
| | | NewInvite | 0xb94690de |
| | | LobbyUpdate | 0x031762f8 |
| | | LobbyKicked | 0x5f1c4880 |
| | | SetLobbyMeta | 0xc4ebdab0 |
| | | SetSelfMeta | 0x01e0592e |
| | | SendChatMessage | 0x09733c0c |
| | | ChatMessageRecv | 0x9ac3f052 |

Confirmed along the way: ServerPing really is 0x0644b9e5 (bare
`ServerPing` string referenced at 0x16e0038, inside that method's block),
so 0xf1127ff6 is ClientPing — the pushed ServerPing already deployed in
`d2r_services.go` names the right method. There is no JoinLobby: joining
is GameManagement.GameJoin. The UI `bl 0x92872c` dispatcher hashes command
names with FNV-1a64 (offset basis 0xcbf29ce484222325, prime
0x100000001b3 — both visible as immediates) for the UI CommandDefinition
map (`Could not find ExecuteCommandCallback: Online`); it is NOT the RPC
layer (FNV of names matches no wire hash).

Still open: ConvertCharacter's hash, GameManagement 0xa605d1e4's name,
AcceptLobbyInvite/RejectLobbyInvite hashes, GameVersion's
GameUpdateRequired/GameUpdateCanceled push hashes, Stats/Network/IGR
method hashes. None is on the solo create/join path; the Ghidra project
will settle them.

## The blocker is in the emulator, not the gateway (2026-09-20 night)

The Login-never-succeeds model is **wrong**. The 2026-09-20 15:58 UTC run
(Ryujinx log `..._2026-09-20_16-58-22.log`, gateway session
`10.89.2.14:51974`) lines up exactly:

| emu time | wall (UTC) | event |
|---|---|---|
| 33.60 | 15:59:07.8 | bgs dial `eu.actual:1119`, poll → writable → TLS |
| 33.74 | 15:59:07.9 | ProcessTask answered (149-byte body) |
| 35.22 | 15:59:09.4 | **`us.actual:1119` dialled, fd 8, EINPROGRESS, SO_ERROR 0** |
| 48.95 | 15:59:23.2 | DestroySession (= ProcessTask +15.2 s) |
| 50.09 | 15:59:24.3 | `[D2BackendServices]: error: 666` |

The dial only happens if the dial gate passed, so Login **does** succeed and
stores both LoginResponse strings. What times out is Connect: the socket is
opened and then never driven — no poll, no `ssl::CreateContext`, no
ClientHello (matching the pcap).

Why it is never driven: the D2R-services transport is **`nn::websocket`**
(the Nintendo SDK library, not bgs-sdk's own TCP path that `eu.actual` uses).
Its worker thread is one `select()` loop:

- wakeup eventfd created at text 0x1060fe4 (`nn::socket::EventFd(0, 4)`),
  stored in the global 0x1ea97e8; 22 `EventFdWrite` call sites kick it.
- worker loop 0x106475c: FD_SET(wakeupfd) into readfds, add each connection
  whose "already queued" flag (conn+0x23c) is clear and whose state
  (conn+0x14) is in [3,8], then **`nn::socket::Select` at 0x1064cf8 with a
  1 s timeval** (the constant pair at 0x1b3d800 is {1,0}).
- 0x1064cfc–0x1064d34: only if `FD_ISSET(wakeupfd, readfds)` does it call
  `nn::socket::EventFdRead` — the **only** drain of that fd in the binary.

Ryujinx's `EventFileDescriptorPollManager.Select` is an unimplemented stub
that returns `EOPNOTSUPP` without waiting and without ever setting
`OutputEvents`. So:

1. the wakeup fd is never reported readable → never drained → the worker
   never learns it was woken;
2. a select whose set is *only* the wakeup fd (every round in which the one
   connection is already queued) returns **instantly** instead of sleeping
   1 s, so the worker free-runs.

The signature in the log is unmistakable: **1,050,789 `Write IPC fd=0`
(eventfd) IPCs in ~62 s (~18 k/s), zero `Poll with eventfd`, zero deferred
polls**, starting at the exact instant the `us.actual` socket is created and
stopping when it is disposed. Ryujinx's Bsd service has one server thread, so
that storm also starves every other socket IPC — which is why nothing else
ever touches fd 8.

The fork's existing deferred-poll machinery (`PollDeferRequested`,
`PollInputSnapshot`, `CompleteReadyDeferredPolls`) fixes exactly this class of
bug, but it lives only in `IClient.Poll`. `IClient.Select` has none of it, and
`nn::websocket` uses select. A blocking eventfd select cannot simply be added:
the `EventFdWrite` that would make it ready is an IPC on the same single Bsd
thread, so it would deadlock — the fix has to defer, as Poll does.

Gateway-side nothing is wrong on this path: LogonResult, CreateSession,
ProcessTask (149 bytes) and GetAccountState (109 bytes) are all answered and
accepted.

## Why the D2R-services TLS never completed (2026-09-20 night)

The title puts every other connection through **`nn::ssl`** — geo, telemetry,
account and the bgs gateway all log `ssl::CreateContext` + `SetHostName`, and
OpenPak injects its CA there (`[OpenPak] Supplying the configured CA through
system certificate 1033`), so they all verify.

The D2R-services websocket does **not**: there is no `ssl::CreateContext` for
`us.actual:1119` in any run. bgs-sdk carries **its own OpenSSL** with its own
embedded CA list (`BEGIN CERTIFICATE` ×15 in .rodata, full X509 verify
machinery, `ERROR_NETWORK_MODULE_OPENSSL_X509_*` strings) and does that
handshake in-guest. A certificate signed by `O=OpenPak, CN=OpenPak Network CA`
cannot validate against it, and there is no way to add to that bundle without
patching the title.

Observed exactly, with the Bsd packet tap kept alive past the first frame:

    48.263  BsdTap TX PROD_HOST:1119 (293 bytes)  16030101200100011C0303…  ClientHello
    48.303  BsdTap RX PROD_HOST:1119 (1550 bytes) 160303007A02000076…      ServerHello+Cert+SKE+Done
    48.303→ select r=[0e,7] e=[7] -> ready=0, parked every 100 ms, forever

The client consumes the server's complete flight, never sends
ClientKeyExchange, and never sends an alert — it simply stops. Connect then
times out at +15 s with error 666.

**The fix is ours to make, because we hand the client the URL.** LoginResponse
field 1 selects the scheme, and the URL builder (0x162785c) treats `ws:` as
plain and anything else as TLS. Serving the D2R-services connection over
`ws://` removes the certificate from the path entirely:

- `BATTLENET_D2R_SERVICES_URL=ws://us.actual.battle.net:1119`
- Traefik gains `battlenet-gateway-plain`, a TCP router on the same `gateway`
  entrypoint with `HostSNI(*)` and **no** `tls` block, so it matches only
  non-TLS connections and cannot shadow the SNI router. Both point at the same
  plaintext backend `openpak-battlenet:21501` — the one Traefik already
  forwards to after terminating TLS.
- Port 1119 is reused deliberately: it is the only port already open in the
  host firewall (21081 is not), so no new exposure.

Verified: `HTTP/1.1 101 Web Socket Protocol Handshake` on plain
`http://PROD_HOST:1119/OSI/v2/rpc/client`, and TLS on 1119 still
terminates for the bgs logon.

## First live D2R-services session (2026-09-20 17:22 UTC)

With `ws://`, the connection the project had never reached came up and stayed
up. Gateway log, in order:

    D2rServicesConnection.AuthSession        answered (36-byte body)
    pushed SessionUpdate, CookieUpdate, ServerPing
    GameVersion.SetGameVersion               answered
    unknown 0x44432e9b / 0x7d47c51f          answered (empty)
    D2rServicesConnection.SessionUpdate      client answered our push
    D2rServicesConnection.ServerPing         client answered our push
    unknown 0xeeb6ab22 / 0xa2f2c246          answered (empty)
    GameManagement.GameGetSitePingList       answered EMPTY  <- needs a real body
    Character.ReadCharacterList              answered EMPTY  <- needs a real body
    D2rServicesConnection.CookieUpdate       answered
    ... then a 3 s heartbeat on 0x0b62a8a1 / 0x4b1c3b85, indefinitely

No error 666, no DestroySession, no reconnect: the session holds. The frontier
moved from "the connection never opens" to "the connection is open and every
response body is empty".

Next decodes, in the order the client asks for them:

1. `ReadCharacterListResponse` (Character 0x607fd196 / 0x47821811) — the
   character list; the request body starts `08 0...`.
2. `GameGetSitePingListResponse` (GameManagement 0x110f2741 / 0xbb6dcaa4).
3. The three unnamed hashes. 0x44432e9b has a service init at text 0x1749454
   and 0x7d47c51f is referenced at 0x1687eb8 / 0x168ed24 / 0x17496c4;
   0x0b62a8a1 and 0x4b1c3b85 are not built as MOVZ/MOVK immediates, so they
   come from .rodata — chase them through the register tables, not a constant
   scan.

## Character select and game creation, from live traffic (2026-09-20 19:1x UTC)

With the roster answered, the title walks all the way to game creation. Every
layout below is read off bytes the client actually sent, not inferred.

| RPC | service/method | request bytes | layout |
|---|---|---|---|
| Character.ConvertCharacter | 0x607fd196 / 0xb0a7dad2 | `08 01` | {1 varint character id} |
| GameManagement.GameCreate | 0x110f2741 / 0x930fbb99 | `12 0e "game1628292938" 1a 0e "Game1628292938" 32 00 48 ff01 50 04 58 80…` | {2 string key (lowercased name), 3 string display name, 6 string password (empty), 9 varint 255, 10 varint 4, 11 varint} |
| GameManagement.GameJoin | 0x110f2741 / 0xa5cc1951 | `08 01 12 0e "game…" 1a 0e "Game…" 22 00 28 00 30 00 38 00 40 …` | {1 varint character id, 2 string key, 3 string display name, 4 string password, 5-8 varint} |
| GameManagement.GameDetail | 0x110f2741 / 0xb5cd1354 | `0a 0e "Game1628292938"` | {1 string display name} |

`ConvertCharacter` also confirms the hash the static pass left open:
**0xb0a7dad2**, and its field 1 is the character id we handed out in
`CreateCharacterResponse`.

Sequence on "create private game": GameCreate once, GameJoin once, then
**GameDetail every 3 s forever**. All three are answered with an empty body
today, so the client sits on "creating game" waiting for a game that never
becomes ready. `UseCharacter` (0x6a706ac6) is never called — the title goes
from the roster straight to game creation.

The frontier is no longer the RPC layer. GameDetail has to describe a game
that exists, and a game the client can actually enter means a D2 game server
speaking the in-game protocol — a far bigger piece than any response body
decoded so far. Nothing in this repo serves it yet.

## The game itself is peer-hosted, not server-hosted (2026-09-20)

This decides how much of a D2R server there is to write, so it is worth being
precise. The title carries, in .rodata:

- `Bind.Success|port=6112` and `Checking binding UDP to port 6112...` — the
  client BINDS the classic Diablo II game port. It listens; it does not only
  dial. `nn::socket::Listen` is an import.
- `Client.GameMaster.NatPunch.Enabled`, `NatPunch.Commencing`,
  `NatPunch.Failure`, `Checking Nat Punch Type 1...` — the host is called the
  GameMaster and peers NAT-punch to it.
- `classic.protocol.local.v1.upnp.UPNPService` / `UPNPAttribute` — the host
  opens its own port via UPnP.
- STUN ("Sending STUN packet...", `Upnp.ExternalIp|address=%s`) — the host
  discovers its external address to publish.
- `classic.protocol.v1.game_proxy.*` — `NewClientRequest`, `DelClientRequest`,
  `DisconnectClientRequest`, `AddressInfo`, `Packet`, `UDPPacket`,
  `NatPacket`, `EchoPacket`: a relay for peers that cannot punch through.
- `ERROR_MATCHMAKING_GAMESERVER_NOT_FOUND` / `_FULL` /
  `_ALREADY_REGISTERED`, `[INTERNAL] Not ready to connect to a game server.`

So a game runs **on the hosting console**. The world simulation, which would
have been the enormous piece, is not ours to write. What a complete D2R server
owes the title is:

1. bgs logon and session — done.
2. D2R-services auth and the character roster — done.
3. Game management: GameCreate / GameJoin / GameDetail / GameList answered
   with real bodies, carrying the host's `AddressInfo` so peers can reach it.
4. `game_proxy` as a relay when NAT punch fails. On a LAN or a single console
   this may never be exercised, but a public deployment needs it.
5. Character persistence — done (roster on disk).

That makes "100% working" a tractable target rather than a rewrite of Diablo
II. The next decodes are `GameCreateResponse`, `GameJoinResponse` and
`GameDetailResponse` (type names at ro 0x1d29fd, 0x1887aa, 0x1a8148), plus
`GameDetailPlayer` (0x18c029) and `GameListInfo` (0x1e0c85) — specifically
which field carries the host address.

## game_management response layouts (static, 2026-09-20)

Recovered the same way as the character messages: find the type-name string,
find its `GetTypeName` xref, bound the class block by the next one, and read
the tag compares out of the parser. Blocks that still show one field number
with two wire types contain more than one function, so those rows are the
less certain ones and are marked.

| Message | GetTypeName xref | Fields |
|---|---|---|
| GameDetailResponse | 0x16f9798 | 1 LEN, 2 LEN, 3 varint, 4 varint, 5 LEN, 6 varint, 7 varint, 8 varint, 9 LEN — no duplicates, high confidence |
| GameDetailPlayer | 0x16f7748 | 1 LEN, 2 varint, 3 varint (a 3 LEN also appears in the block) |
| GameJoinResponse | 0x16f2bfc | 1-4 ambiguous (varint and LEN both present), 5 LEN, 6-9 varint, 10 LEN |
| GameListInfo | 0x16f5184 | 1 varint, 2 varint, 3 (both), 4 varint, 5 (both), 6 LEN, 7 LEN |
| GameCreateResponse | 0x16f02c4 | 1 (both), 2 (both), 3 LEN, 4 LEN, 5 varint, 6 LEN, 7 LEN |
| GameAttribute | 0x16ecba4 | 1 LEN, 2 LEN |

Still unattributed: which field carries the host's address. The title is
peer-hosted, so something in GameDetailResponse (or GameJoinResponse) must
name the GameMaster — most likely one of the LEN fields holding
`classic.protocol.v1.game_proxy.AddressInfo` or a host string. Do not fill any
of these in by guessing; the client polls GameDetail every 3 s and a wrong
answer is indistinguishable from no answer on screen.

## GameCreateRequest / GameJoinRequest, pinned by known inputs (2026-09-20)

A custom game was created with every option chosen deliberately — name
"<user-a>'s game", password "abc123", max 3 players, level difference 8,
Normal difficulty — so these field meanings are read off matching values, not
inferred from wire types.

GameCreate (0x110f2741 / 0x930fbb99):

| field | wire | value sent | meaning |
|---|---|---|---|
| 2 | string | `"<user-a>'s game"` | game name, **lowercased** by the client |
| 3 | string | `"abc123"` | **password** |
| 6 | string | `""` | unknown, empty in both captures |
| 9 | varint | `8` | **level difference** (an auto private game sent 255 = unrestricted) |
| 10 | varint | `3` | **max players** (an auto private game sent 4) |
| 11 | varint | `4325376` | flags/bitfield, same value in both captures |
| 12, 13 | string | `""` | unknown |
| 14 | varint | `0` | **difficulty**, Normal = 0 |
| 15 | varint | `1` | unknown, 1 in both |

GameJoin (0xa5cc1951) reuses the same names: `{1 character id, 2 game name,
3 password, 4 string "", 5-7 varint 0, 8 varint 1, 9 varint 0}` — field 3 is
the password in both messages, which is what makes the mapping safe.

Note on the earlier auto-generated private game: its field 3 was
`"Game1628292938"`, the display-cased game name, where the custom game put the
password. So for a quick private game the client fills the password with the
game name rather than leaving it empty. Field 2 is the lowercased form in both.

This is enough to implement a real lobby: a game record can now be built from
what the client actually asks for — name, password, capacity, level spread and
difficulty — instead of echoing a string back.

## Open

- First live capture of the D2R-services WebSocket (blocked on the dial
  gate below).
- 2026-09-20 pcap verdict: the game-network path is clean — the D2R dial's
  TCP handshake completes, Traefik's 1551-byte TLS ServerHello flight is
  fully delivered and ACKed (one segment reordered; SACK + retransmit fixed
  it in microseconds), and then the client never sends its handshake half
  (60 s idle, server FIN). The stall is the game never driving the socket:
  created non-blocking, never polled, never written, across every run —
  while eu.actual in the same process goes poll → writable → TLS normally.
  Not a socket, emulator-networking, MTU or certificate problem.
- Login never succeeds, so Connect never starts, so nothing pumps the
  socket. Login update 0x207490 (decompiled) needs poll 0x202830()==4 plus
  its session-pointer combination (struct base 0x24E5000: +0x4E2418 /
  +0x4E2448 — Ghidra folds these to absolute `DAT_029c7c18/48`, same
  addresses). The ProcessTask callback consumes our response cleanly (none
  of its four failure strings ever log; URL/token stored at session
  +0xf8/+0x128, flag +0x158 set) yet DestroySession fires at +15 s every
  run. Next: enumerate every writer to Login's poll word and pointer slots
  and find which input only a server push can provide (dial-gate inputs
  +0x100/+0x130/+0xf0 live in the same neighborhood).
- Online still fails after the LoginResponse, size and client_id fixes
  (client_id confirmed live 2026-09-19: the id now rides on ProcessTask and
  DestroySession, which still fires at +15 s): the
  ProcessTask response bytes were walked byte by byte and pass every check
  in the callback (status 0, client_response match, protobuf_response
  present, parse OK → strings stored). A remaining +15 s DestroySession can
  come from Login timing out OR from Connect timing out (both stop Login).
  The client prints its failure reasons to stderr ("Received non-zero game
  util response…", "Failed to find/parse LoginResponse…", "Error %lu: %s|
  account=…" via 0x205224; error 666 = the 15 s state timeout), but the
  Ryujinx guest log does not show them yet — capturing guest stderr would
  settle which state fails.
