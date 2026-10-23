# D2R online protocol — what the title expects

> **Current Switch client (2026-10-13): Harbor**, at
> `~/REPOS/Openpak/emulators/harbor`. Harbor is an independent
> OpenPak project based on Eden. Citron, Ryujinx (desktop and Android), and
> the former OpenPak Eden checkouts have been removed. References to those
> forks below record historical evidence only: do not run their commands,
> reinstall them, or select them for current work. Use Harbor's README and
> current handoff for builds and testing; old results do not establish Harbor parity.

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
| | | GameUpdateRequired (push) | 0x01b1b8b2 |
| | | GameUpdateCanceled (push) | 0xaa1a02d0 |
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
AcceptLobbyInvite/RejectLobbyInvite hashes, Stats/Network/IGR
method hashes. None is on the solo create/join path; the Ghidra project
will settle them.

## Gateway TLS on hardware (static, 2026-09-30)

The bgs gateway (`*.actual.battle.net:1119`, 443 as the retry port) is opened by the bgs-sdk
SSL transport over `nn::ssl`, not a private OpenSSL: connect routine text 0x3856c, TLS block
0x391d8, sequence `Context::Create(Auto)` -> `Connection::Create` -> `SetSocketDescriptor` ->
`SetHostName` -> `SetIoMode(NonBlocking)` -> `SetSessionCacheMode(SessionTicket)` and nothing
else -- no `SetVerifyOption`, no `SkipDefaultVerify`, no `ImportServerPki` (the emulator log for
`eu.actual.battle.net` shows exactly that pair of calls, while geo/telemetry/account set all
three). Verification is therefore nn::ssl's default (PeerCa|HostName) against the console's
built-in store, where the NRO puts the OpenPak CA (slot 1033). The 32 KB pin JSON at ro
0x253509 (gateways, `*.bgs.battle.net`, 141 root SPKI pins) is parsed at init but its
`ImportServerPki` loop (0xfaaab4) runs zero times in a login and no server chain is ever fetched:
the pins do not gate the gateway. `nn::nsd::Resolve` (0x38828) runs on the FQDN before the dial.
Full trace: `private scratch`.

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

### Character creation and roster metadata consumer (2026-09-21)

The character path is now traced beyond the protobuf parsers and into the
runtime character builder. The wire layouts actually consumed are:

| Message | Fields actually consumed |
|---|---|
| CreateCharacterRequest | 1 name string, 2 class uint32, 3 flags uint32 |
| CreateCharacterResponse | **1 character ID uint64 only**; the generated parser ignores an extra field 2 |
| ReadCharacterListResponse | 1 uint32, 2 repeated CharacterSummary |
| CharacterSummary | **1 character ID uint64, 2 name bytes, 3 character-data UTF-8 string** |

The current server has two material encoding errors: it puts `Level` in
CharacterSummary field 1, although that field becomes the character ID, and
it leaves field 3 empty. The first character masked the ID error because both
its ID and level happened to be 1. Multiple fresh characters would all be
advertised with ID 1. The empty field 3 explains the malformed class, level,
progress text and appearance.

The complete client flow is:

1. NetworkState type 5 reads name at creation-object `+0x48`, class at
   `+0x160`, and flags at `+0x164`, then sends CreateCharacter. Its success
   callback stores response field 1 at result `+0x28` and completes.
2. `OnReadCharacterListSuccess` at `0x1ce27c` copies each CharacterSummary
   into an intermediate record without interpreting it: ID, name, and the
   field-3 data string are all preserved.
3. The type-4 list callback at `0x1cbb50` clones those values into a
   `0x540`-byte roster record: ID at `+0`, name at `+0x8`, and data string at
   `+0x120`.
4. The runtime builder at `0x6a5f08` receives those three values. If the data
   string is shorter than 51 bytes, it treats it as the legacy binary D2
   statstring and reads fixed offsets through byte 30. An empty string
   therefore selects the wrong decoder and yields meaningless defaults.
5. At 51 bytes or longer, field 3 is parsed as JSON. Recognized keys are
   `version`, `appearance`, `class`, `level`, `charflags`, `acts`, `guildTag`,
   `equipment`, and `questRecords`. The basic visible values map directly:
   JSON `class` -> runtime byte `+0x120`, `level` -> `+0x121`, `charflags` ->
   uint16 `+0x122`, and `appearance` -> the byte array beginning at `+0x110`.

A minimal fresh-character metadata body such as
`{"version":0,"appearance":[],"class":4,"level":1,"charflags":0}` is long
enough to select the JSON path and supplies the fields currently rendered
incorrectly. An empty `appearance` is filled with the client's `0xff`
defaults, after which the class-specific appearance builder runs. This needs
a live render check before adding optional act/equipment/progression data.

### Character usability and progression fields (2026-09-22)

The select UI's usability predicate is `FUN_006a8e70` at `0x6a8e70`. It
returns a six-bit mask and enables the character only when the mask is
`0x3f`. The bits cover, in order: level range, progression/difficulty,
ladder match, expansion match, hardcore match, and the online-character
record kind. The progression test compares `charflags >> 8 & 0x1f` against
`4 * difficulty` for classic characters or `5 * difficulty` for expansion
characters. Thus the high five bits of `charflags` are progression, not a
separate flag family.

The badge interpretation is also now exact. The widget is named
`ClassicIcon` and is visible only when `(charflags & 0x20) == 0`. An
expansion character therefore has no classic badge; absence of that icon is
evidence that the expansion bit reached runtime `+0x122`, not evidence that
it was lost.

`acts` is an array of integer bytes. The JSON builder stores each element's
low seven bits in the per-act array at runtime `+0xdd8`; bit 7 marks that
element as the current act and stores its index at `+0xddb`. For a new
character, omitting `acts`, sending `[]`, or sending `[128]` all leave the
effective act/progress state at act zero. Consequently the earlier `acts:[]`
hot probe could not have changed usability.

The live server now has a no-restart diagnostic override in
`/var/lib/battlenet/character.json`:
`{"charflags":7968,"acts":[128]}`. `7968 == 0x1f20` preserves expansion,
softcore and non-ladder while maxing the five-bit progression value. If the
same roster becomes selectable after a fresh `ReadCharacterList`, the failed
mask bit is progression; if it does not, capture the exact warning/highlight
state and gateway log for one select attempt rather than changing more JSON
keys.

## CORRECTION 2026-09-30: the game is server-hosted over TLS 443, and the trust is built in

The "peer-hosted" section below is wrong; it was inferred from strings that belong to a network
diagnostic (0x1685080), not to the join path. Traced end to end (full report with addresses:
`private scratch`):

- After GameJoin the category-2 update (0x1b8b04) calls `GameConnectionObject` slot 0x20 (0x6b2ed4)
  -> the SDK game-server connection 0x1635958. It builds the endpoint `{host = field 1, port = 443}`:
  **field 2 (port) and field 6 are never read**, the port is the constant 0x1bb. Field 3 becomes
  bytes 1-4 of the client's first packet, field 4 (must be non-zero) bytes 5-6. Field 7 is display
  flags, 8/9 the level range, 5 a display string, 10 unread. No self-host, loopback, STUN, UPnP,
  NAT-punch or game_proxy branch exists on this path; the local connect (slot 0x28) is only reached
  from the offline `.d2s` start (0x28f7d0).
- The connection is TCP + TLS done by the game's own OpenSSL ("cross_ssl"), trusting only the PEM
  bundle at ro 0x1b00033 (DigiCert High Assurance EV Root CA + DigiCert SHA2 High Assurance Server
  CA, valid to 2028-10-22) and checking the hostname against field 1; the cert-failure callback
  (0x163b750) returns 0. Neither nn::ssl nor the OpenPak system-store CA is consulted. DigiCert
  stopped issuing under that root in 2023 (G2 hierarchy) and the root is distrusted from April
  2026, so no public CA can issue a certificate the title will accept.
- On that stream the SERVER speaks first: `AF 00`; client `68` logon (45 bytes: f3, f4, class,
  constants, character id, name); server `01` game flags (bit 0x4 set, difficulty/expansion/ladder
  bits self-consistent); server `00`; client `6D` ping; server `02`; then pongs `8F` and in-game
  packets. "Creating game" ends only when a complete in-game packet arrives and they keep coming
  every < 15 s: that is a Diablo II game server (world simulation), not a relay.
- GameCreate: an empty body is enough, the client sets it to never time out. No server push is
  needed; 0xa605d1e4 is client-sent `GameMatchmake`.

What this means for OpenPak: a working "create game" needs (1) a certificate the title trusts,
which cannot be bought and can only come from patching the title's bundle (hardware) or
substituting it in the emulator, both against the no-game-patches rule; and (2) a Diablo II game
server. Both are owner decisions, not server-side probes. The GameCreate/GameJoin/GameDetail
answers in this service stay as they are: they are correct up to the point where the game server
would take over.

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
2. D2R-services auth and character persistence — transport works; the roster
   summary still needs the corrected ID and character-data encoding below.
3. Game management: GameCreate / GameJoin / GameDetail / GameList answered
   with real bodies, carrying the host's `AddressInfo` so peers can reach it.
4. `game_proxy` as a relay when NAT punch fails. On a LAN or a single console
   this may never be exercised, but a public deployment needs it.
5. Character persistence — done (roster on disk).

That makes "100% working" a tractable target rather than a rewrite of Diablo
II. The response consumers are now decoded: GameJoin field 1 carries the host,
field 2 the port, and field 4 the active game number/identifier; GameCreate
cannot supply a retained join pair. Remaining work is a live transport probe,
the optional player/list structures, and the corrected character-data body.

## game_management response layouts (static, 2026-09-20)

Recovered the same way as the character messages: find the type-name string,
find its `GetTypeName` xref, bound the class block by the next one, and read
the tag compares out of the parser. Blocks that still show one field number
with two wire types contain more than one function, so those rows are the
less certain ones and are marked. `GameJoinResponse` was later disambiguated
from its merge routine, which copies each presence-bit-selected member to its
known offset (rather than from the noisy parser block).

| Message | GetTypeName xref | Fields |
|---|---|---|
| GameDetailResponse | 0x16f9798 | 1 LEN, 2 LEN, 3 varint, 4 varint, 5 LEN, 6 varint, 7 varint, 8 varint, 9 LEN — no duplicates, high confidence |
| GameDetailPlayer | 0x16f7748 | 1 LEN, 2 varint, 3 varint (a 3 LEN also appears in the block) |
| GameJoinResponse | 0x16f2bfc | **1 bytes, 2 uint32 (consumer narrows to uint16), 3 uint32, 4 uint32, 5 bytes, 6 bool, 7 uint32, 8 uint32, 9 optional uint32, 10 bytes** — high confidence; merge at 0x16f2824 maps presence bits 0–9 to fields 1–10, and the RPC adapter at 0x1649cc0 proves the concrete storage/consumer types |
| GameListInfo | 0x16f5184 | 1 varint, 2 varint, 3 (both), 4 varint, 5 (both), 6 LEN, 7 LEN |
| GameCreateResponse | 0x16f02c4 | parser block is noisy, but the RPC adapter consumes **only field 5 as uint32** and discards every other response field |
| GameAttribute | 0x16ecba4 | 1 LEN, 2 LEN |

### GameJoin field-1 probe (2026-09-20 20:47 UTC)

On a freshly restarted client, GameJoin was answered with the one-field body
`0a 0a "JoinProbe1"`: field 1 was the exact display name requested by that
same GameJoin. The client accepted it and began polling that same name three
seconds later, so the response parsed. It made no visible change: GameDetail
continued at three-second intervals for 58 seconds before the ordinary game
creation timeout closed both connections. Field 1 alone is therefore not the
ready/host payload.

A subsequent headless Ghidra callback trace reached only the generated
GetTypeName wrappers, not the consuming callback: that route is indirect
through the client's RPC/vtable machinery. It adds no field attribution and
is not grounds to send a guessed varint. The local trace artifact is
`~/d2r-work/gamejoin-callback-trace.txt`.

The callback was subsequently found from its diagnostic strings rather than
from parser xrefs. `OnGameJoinSuccess` is at `0x1bb5e4`, and the RPC adapter at
`0x1649cc0` copies every parsed field into the success event. This corrects
one important type detail: field 6 is a boolean, field 2 is narrowed to 16
bits, and field 9 preserves an explicit presence bit. The three LEN fields
use the bytes parser rather than the UTF-8-validating string parser. The
adapter forwards the fields but does not reveal their names.

### GameJoin field-6 probe (2026-09-21)

A fresh client was answered with field 6 = true only (`30 01`). Unlike the
ordinary empty/field-1 attempts, it did not time out after the first minute.
It remained on the same game for more than two minutes and kept requesting
GameDetail, with the cadence changing from about three seconds to about five
seconds after the first minute. Field 6 is therefore strongly identified as
the **pending/queued** flag. True intentionally selects the wait path; false
or absent selects the non-queued path but still needs whatever connection or
token data that path consumes.

### GameJoin non-queued consumer (2026-09-21)

The non-queued path is now traced past the RPC adapter. The UI's type-9
`JoiningGame` request is only an orchestration state: `0x1d7b98` activates and
waits for NetworkState category 2. The category-2 state has vtable
`0x1e2cd18`; its entry method at `0x1b7e94` submits GameJoin through the
GameManagement method at `0x164735c`. Its per-request callback has vtable
`0x1e2cd58`, whose success slot is `0x1b985c`.

That callback proves the following non-queued data flow:

| GameJoin field | Category-2 result | Downstream use |
|---|---:|---|
| 1 bytes | `+0x68` | first argument to the `GameConnectionObject` connect call; endpoint/host string |
| 2 uint32 -> uint16 | `+0x98` | second connect argument; endpoint port |
| 3 uint32 | `+0x9c` | third connect argument; stored at GameMaster `+0xe0`, with no reader found in that class |
| 4 uint32 | `+0xa0` | fourth connect argument; active game number/identifier, stored at `+0xe4`, read by four handshake/error sites, and printed on join failure |
| 5 bytes | transient connection configuration | copied into the session configuration built by `0x1b985c` |
| 6 bool | `+0xd0` | passed to `GameConnectionObject` as its final stack argument; true is also the observed queued/pending path |
| 7 uint32 | transient flags | decoded as bit flags while building the session configuration |
| 8 uint32 | transient value | combined with optional field 9 for range arithmetic |
| 9 optional uint32 | transient optional value | when present, used with field 8 to form a bounded range |
| 10 bytes | `+0xa8` | retained by the category-2 state, but not passed in the immediate connect call |

When the category-2 state reaches result state 2/substate 1, `0x1b8b04`
invokes the global object whose own failure logs are tagged
`GameConnectionObject`. The call loads field 1 as `x1`, field 2 as `w2`, field
3 as `w3`, field 4 as `w4`, and field 6 as the stack argument. This is the
first direct proof of the response's endpoint tuple: **field 1 is the host and
field 2 is the port**. Fields 3 and 4 are passed as adjacent uint32 transport
arguments; they are not readiness flags. The concrete GameMaster connection
routine at `0x1635958` stores field 3 at object `+0xe0` and field 4 at `+0xe4`.
An exhaustive access scan of that concrete class found no reader of `+0xe0`,
while `+0xe4` is read at four handshake/error-dispatch sites (`0x1636cec`,
`0x16378b0`, `0x16386bc`, `0x163ae20`). Thus field 4 is the active game
number/identifier on this Switch build; field 3 is accepted and retained but
is inert in the traced path. This does not establish that servers may omit
field 3, only that it cannot be the missing active handshake identifier here.

### GameCreate success consumer (2026-09-21)

GameCreate cannot mint the GameJoin field-3/field-4 pair. The RPC adapter at
`0x1649b7c` reads only `GameCreateResponse` field 5 and forwards that one
uint32 to `OnGameCreateSuccess` at `0x1baec4`; every other response member is
discarded. The type-8 `CreatingGame` request installs a type-erased callback
whose invocation thunk is `0x1d7534`. The callback receives the field-5 value
but ignores all three success arguments and only removes the outstanding
request by the request ID at object `+0x30`. No GameCreate response value is
retained in the CreatingGame result. Any equality between GameCreate field 5
and GameJoin field 4 would therefore be a server-side convention, not a value
the client carries from the create response into its transport object.

### GameDetail result consumer (2026-09-21)

The GameDetail path is no longer inferred from parser layout. Its concrete
flow is:

1. `OnGameDetailSuccess` at `0x1bed84` builds the RPC success event.
2. `0x1d9460` copies it into the type-11 NetworkState result. It consumes
   fields 1, 2, 3, 4, 6, 7, 8 and the repeated field 9. It does not call the
   field-5 getter at all.
3. The GameFinder request site writes type 11 at `0x9fb938` and registers it
   at `0x9fbcec`. Callback `0x9ffb4c` copies the completed result into the
   browser model consumed by `0x9fa528`.
4. The only numeric detail fields loaded by that browser consumer are field 4
   and fields 6/7. Field 4 is formatted as elapsed time. At
   `0x9fc668`/`0x9fc710`, fields 6/7 produce the displayed level range:
   `lower=max(1, field6-field7)`, `upper=min(99, field6+field7)`; field 7 at
   least 99 displays `LevelRestrictionAny`.

Fields 6 and 7 are narrowed to bytes in the result model, but are level and
level-difference values, not boolean ready flags. Fields 3 and 8 are copied
but never loaded in this path; field 5 is discarded before it reaches the UI.
There is no ready/queue-release branch on any GameDetail field in this
consumer. The repeated GameDetail traffic seen after GameJoin field 6 = true
is details-panel refresh, not a readiness poll that can release the queue.

NSO relocation trap: text RELA addends are raw text offsets and require the
module's `+0x100` text VA bias. Applying addends as code VAs directly resolves
the wrong functions while still looking superficially plausible.

The next bounded live probe should therefore use a textual reachable address
in GameJoin field 1 and its UDP game port in field 2, with field 6 absent or
false and an explicit field-4 game number. Keep field 3 at a fixed conservative
value because it is accepted and stored, but do not treat it as the missing
active half of a pair. GameDetail field 5 is explicitly ignored by the traced
browser path and is no longer a host-address candidate.

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

## Update required (static, 2026-10-09)

Read from the update build (DB0507F3...) and the base dump; both agree. Scripts and
disassembly in `private scratch`.

- **LoginResponse field 4 does nothing.** The ProcessTask callback (update 0x1d4d50, base
  0x203c64 file / 0x203d64 Ghidra) parses LoginResponse into a stack local, copies the two
  string slots (fields 1 and 2) into the session, and destroys the message. The field-4 pointer
  and the field-3 bool are initialised and never read.
- **GameVersion pushes.** Method hashes come from the register blocks of the GameVersion init
  (update 0x19c9f24, `mov w8,#0xb8b2; movk w8,#0x1b1,lsl#16`): GameUpdateRequired 0x01b1b8b2,
  GameUpdateCanceled 0xaa1a02d0. The game-side implementation (ctor 0x17727f0, vtable
  0x2102840) copies the pushed `classic.protocol.v1.game_version.GameUpdateRequest`
  {1 string, 5 u64} into a member, then calls its primary listener and every registered
  listener (`vt+0x10`; GameUpdateCanceled resets the member and calls `vt+0x18`). Its accessors:
  "update required" = field 1 non-empty (vt+0x28), field 1 (vt+0x30), field 5 (vt+0x38).
- **Not traced:** which listener the UI registers and what it shows (dialog, go offline,
  nothing). Field 5's meaning is unknown. The gateway therefore only logs the verdict
  (`VERSION ...` lines) and sends GameUpdateRequired {1: text} after SetGameVersion only with
  `BATTLENET_GAME_UPDATE_PUSH` set. The next step is one run with an outdated build and the knob
  on, watching the title.
- The console's own gate stays first: nx-baas refuses an outdated title at aauth (0111, the
  2124-3001 dialog) before it ever gets an id_token, so this path only sees clients that got
  past aauth.


## GameList, and accounts behind one address (static + server, 2026-10-10)

Recovered from the **update** build (DB0507F3…) with RELA applied, each message from its parser
and its serializer/ByteSizeLong, so field numbers and proto types are certain:

| Message | vtable / routines | Fields |
|---|---|---|
| GameListRequest | 0x1ee2648, parse 0x1702460, serialize 0x1702670 | 1 string, 2 uint32, 3 uint32. Meanings not traced (likely search text, difficulty, game-type/desecrated filter) |
| GameListResponse | 0x1ee2720, parse 0x1702c68, serialize 0x1702d84 | 1 repeated GameListInfo, nothing else |
| GameListInfo | 0x1ee2570, merge 0x1701cc8, parse 0x1701ee8, serialize 0x1702138, size 0x17022c8 | 1 uint32 current players, 2 uint32 max players (row shows cur/max), 3 string game name (shown, and the key handed to GameJoin/GameDetail), 4 uint32 difficulty (medium), 5 string description (medium), 6 and 7 uint32 unknown |

This corrects the base-build row above: fields 3 and 5 are strings, 1/2/4/6/7 are uint32. The
client lists every element and only greys a full one; there is no id field and no gate on any
value. The server answers 1-4 only, from games other accounts created: live, not full, no
password (the quick private game uses the name as its password, so password games are never
listed), and the same SetGameVersion string as the viewer when both sent one. A game closes when
its last game-server connection drops, and one never reached within 2 minutes is dropped.
Before v0.3.0 this method was answered with an empty body ("No games found"); the D2RSVC log
printed every GameManagement and Character call as `unknown` because only the connection and
GameVersion methods were named, which is why the sweep could not tell it apart from the
unnamed 0x0b62a8a1/0x4b1c3b85 heartbeat (still unnamed, still answered empty).

**CORRECTION 2026-10-10 (v0.3.1): the table above names the wrong messages for the Game List
screen.** Read from the screen itself (request builder 0x817aec/0x816900, row builder 0xb3f534
on the update build; decompiles in `private scratch`), the client reads only
GameListResponse **field 3**, so v0.3.0's field-1 rows were dropped and the list stayed empty.

| Message | Fields the screen uses |
|---|---|
| GameListResponse | 3 repeated GameListInfo |
| GameListInfo | 2 uint32 current players; 3 uint32 max players (row hidden if max <= current or max 0); 6 string name (shown, and the GameDetail/GameJoin key); 8 string site (looked up in request field 9 for the ping icon; no match = best bucket, not hidden); 9 uint32 difficulty 0-2; 13 bool desecrated. 1, 4, 7, 10-12 only feed a change hash; 5 is ignored |
| GameListRequest | 3 string name search; 4 uint32 viewer's character flags (hardcore 0x4, ladder 0x40); 6 string preferred site; 7 and 10 uint32 difficulty bitmasks (bit 0 normal, 1 nightmare, 2 hell); 9 repeated GameSiteLatency {1 string site, 2 uint32 latency}; 11 bool desecrated filter (absent all, true only, false none); 12 uint32 game type (1 classic, 2 expansion) |

The client also hides names with 7+ ASCII digits unless they start with `_` (and parses `_%di%d`
names as "<label> | %d"), rows over its ping filter, its local hidden list, and rows past 40.
The server applies the request's filters (difficulty masks intersected, a zero result filters
nothing; hardcore/ladder and game type against the owner's character flags, stamped when the
owner GameJoins its own game; desecrated; case-insensitive name search), caps at 40, never lists
password games and keeps versions apart. Field 8 echoes the viewer's field 6 (else its first
field-9 site). OpenPak games are never desecrated: no GameCreate field is known to carry it.

Accounts: the D2R-services socket carries no identity of its own, and every console arrives
from Traefik's address. Its only link to the bgs logon is LoginResponse field 2, echoed in
AuthSession field 1. That token is now `openpak:<subject>.<mac>` (HMAC-SHA256, key from
`BATTLENET_SESSION_KEY` or derived from `ACCOUNT_INTERNAL_KEY`), and characters, game ownership
and seats are keyed by the subject it proves.
