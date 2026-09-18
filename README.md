# battlenet

OpenPak's Battle.net tier: the web front every redirected Blizzard title hits
first, and the native gateway that answers its WebSocket RPC logon. One Go
binary, two listeners, driven by the Diablo II Resurrected logon research in
private notes.

## What runs

- **Web tier** (`BATTLENET_HTTP_ADDR`, default `:21500`): one host-routed
  HTTP server behind Traefik TLS. `geo.battle.net` gets the region-routing
  answer, `telemetry-in.battle.net` an ETag'd sink, and `account.battle.net`
  the login — `/login/check_token` exchanges the title's BAAS id_token for
  the same person's battlenet sub-account through the account core; anything
  else on the login host relays to the real endpoint, logged on the way
  through, and shrinks as endpoints get real answers.
- **Gateway** (`BATTLENET_GATEWAY_ADDR`, default `:21501`): the title's
  `us.actual.battle.net:1119` connection — a WebSocket speaking the
  `v1.rpc.battle.net` subprotocol — answered by a native ConnectionService
  state machine. Frames are generated, never replayed: fresh session ids and
  clocks per connection, the captured schedule (ConnectResponse +0.6 s,
  KeepAlive config +30 s, the auth burst, logon_timeout) reproduced, identity
  resolved through the account core on the authenticated Connect, and every
  frame past logon captured whole.

## The experiment surface

Every behaviour still being pinned against the client is an env knob:
candidate shapes iterate by restarting the unit, not rebuilding it. The
working configuration and its history are in
private notes.

| Knob | Effect |
| --- | --- |
| `BATTLENET_HTTP_ADDR` / `BATTLENET_GATEWAY_ADDR` | Listener addresses (`:21500` / `:21501`) |
| `BATTLENET_IDENTITY_STUB` | Resolve every token to one subject — local experiment loop only, never a deployment mode |
| `ACCOUNT_GRPC_ADDR`, `ACCOUNT_INTERNAL_KEY` | Account-core connection for identity |
| `BAAS_JWKS_URL`, `BAAS_JWKS_HOST` | Where BAAS verification keys come from |
| `BATTLENET_TLS_CERT`, `BATTLENET_TLS_KEY` | Terminate gateway TLS in-process (local loop; prod terminates upstream) |
| `BATTLENET_SINK_TRACE` | Hex-dump every gateway frame, both directions |
| `GATEWAY_RELAY` | Pipe one session bidirectionally to the real gateway and log both sides |
| `BATTLENET_CR_MODE=replay` | Replay the captured ConnectResponse verbatim instead of generating fresh |
| `BATTLENET_AUTH_CR_STANDARD` | Answer the authenticated Connect with a standard 0xFE ConnectResponse (probed, not part of the dialect — unset in prod) |
| `BATTLENET_METHOD12_RESULT` | OnLogonQueueUpdate position value, or `skip` to drop the frame |
| `BATTLENET_AUTH_ACK`, `BATTLENET_AUTH_ACK_REQ` | The bare 0xFE status-OK ack and the request id it addresses (`skip` drops it) |
| `BATTLENET_METHOD13` | OnLogonQueueEnd payload hex (empty = empty message), `skip` drops it |
| `BATTLENET_LOGON_RESULT`, `BATTLENET_LOGON_METHODS` | Push a LogonResult payload on the listed methods (default `4`); unknown methods are dropped silently by the client, so fan-outs are safe |
| `BATTLENET_LOGON_SUCCESS` | Push a generated successful LogonResult, then answer every request by name (`cmd/battlenet/bgs_services.go`, generated from the title's descriptor pool): SessionService.CreateSession gets a session id plus a SessionListener.OnSessionCreated push, GenerateWebCredentials a fresh credential, anything else an empty status-OK; oneway methods are never answered |
| `BATTLENET_QUEUE_END` | Push a position-0 queue update one second after the burst |
| `BATTLENET_METHOD5_HEX` | Method-5 payload (default `088a04`, the captured 522 rejection), `skip` drops it |
| `BATTLENET_METHOD7_ECHO` | Answer the client's method-7 with a 0xFE status-OK |
| `BATTLENET_GATEWAY_HOLD` | Skip the logon_timeout + close; the session stays open |
| `BATTLENET_D2R_SERVICES_URL` | LoginResponse field 1, the D2R-services WebSocket base the title dials when going online (default `wss://us.actual.battle.net:1119`; served on the gateway listener at `/OSI/v2/rpc/client`) |
| `BATTLENET_LOGIN_RESPONSE_HEX` | Override check_token's LoginResponse (protobuf_response) with these bytes |
| `BATTLENET_CHECK_TOKEN_RESULT` | check_token `result` as a raw JSON value (default `0`; the D2 layer reads it as a number) |
| `BATTLENET_CHECK_TOKEN_TEMPLATE` | Full check_token JSON template with `${UID}`, overriding the built-in shape |

## Ground rules

- Clean room. Public protocol documentation, our own captures, and
  compatibly licensed code are the only inputs; see
  [`docs/clean-room-policy.md`](../../docs/clean-room-policy.md).
- AGPL-3.0-only, English only, Go 1.26, no secrets in the repository.

This project is not affiliated with or endorsed by Nintendo or Blizzard
Entertainment. It contains no proprietary game code, assets, SDK material,
binaries, certificates, keys, firmware, extracted files, credentials, or raw
packet captures. Battle.net and Diablo II: Resurrected are trademarks of
Blizzard Entertainment, Inc.
