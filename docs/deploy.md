# Deploy: battlenet

## Image and build

- Image: `ghcr.io/openpak/battlenet`, built for amd64 and arm64.
- Dockerfile: `golang:1.26-alpine` build stage running
  `go build -trimpath -ldflags="-s -w" -o /out/battlenet ./cmd/battlenet`;
  runtime `gcr.io/distroless/static`, `USER nonroot:nonroot`,
  `ENTRYPOINT ["/battlenet"]`.
- `EXPOSE 21500 21501`. The game-server listener on `:21503` is not in
  `EXPOSE`.
- Plain HTTP inside the pod network. Traefik terminates the guest's TLS for
  the three hostnames, the same arrangement as the among-us front.
- Release: a `v*.*.*` tag publishes `:<version>`, `:<major>.<minor>` and
  `:latest`. Deployed hosts run podman-auto-update against `:latest`.
- Run locally: `go run ./cmd/battlenet` (the short form is in the README).

## Ports and listeners

| Listener | Env | Default |
| --- | --- | --- |
| Web tier | `BATTLENET_HTTP_ADDR` | `:21500` |
| Gateway | `BATTLENET_GATEWAY_ADDR` | `:21501` |
| Diablo II game server | `BATTLENET_GAME_ADDR` | `:21503` |

## The experiment surface

Every behaviour still being pinned against the client is an env knob:
candidate shapes iterate by restarting the unit, not rebuilding it. The
working configuration and its history are kept in the maintainers' private notes.

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
| `BATTLENET_PROCESSTASK_DELAY` | Probe: hold the ProcessTask (D2R login) answer this long (Go duration, e.g. `8s`) |
| `BATTLENET_METHOD12_RESULT` | OnLogonQueueUpdate position value, or `skip` to drop the frame |
| `BATTLENET_AUTH_ACK`, `BATTLENET_AUTH_ACK_REQ` | The bare 0xFE status-OK ack and the request id it addresses (`skip` drops it) |
| `BATTLENET_METHOD13` | OnLogonQueueEnd payload hex (empty = empty message), `skip` drops it |
| `BATTLENET_LOGON_RESULT`, `BATTLENET_LOGON_METHODS` | Push a LogonResult payload on the listed methods (default `4`); unknown methods are dropped silently by the client, so fan-outs are safe |
| `BATTLENET_LOGON_SUCCESS` | Push a generated successful LogonResult, then answer every request by name (`cmd/battlenet/bgs_services.go`, generated from the title's descriptor pool): SessionService.CreateSession gets a session id plus a SessionListener.OnSessionCreated push, GenerateWebCredentials a fresh credential, anything else an empty status-OK; oneway methods are never answered |
| `BATTLENET_QUEUE_END` | Push a position-0 queue update one second after the burst |
| `BATTLENET_METHOD5_HEX` | Method-5 payload (default `088a04`, the captured 522 rejection), `skip` drops it |
| `BATTLENET_METHOD7_ECHO` | Answer the client's method-7 with a 0xFE status-OK |
| `BATTLENET_GATEWAY_HOLD` | Skip the logon_timeout + close; the session stays open |
| `BATTLENET_GAME_ADDR`, `BATTLENET_GAME_HOST` | The Diablo II game-server listener (`:21503`, behind Traefik's TLS router) and the name GameJoin hands the title to dial on 443 (`d2gs.battle.net`; the title trusts it only with `../tools/d2r-trust`) |
| `BATTLENET_D2R_SERVICES_URL` | LoginResponse field 1, the D2R-services WebSocket base the title dials when going online (default `wss://us.actual.battle.net:1119`; served on the gateway listener at `/OSI/v2/rpc/client`) |
| `BATTLENET_LOGIN_RESPONSE_HEX` | Override check_token's LoginResponse (protobuf_response) with these bytes |
| `BATTLENET_CHECK_TOKEN_RESULT` | check_token `result` as a raw JSON value (default `0`; the D2 layer reads it as a number) |
| `NX_INTERNAL_URL`, `NX_INTERNAL_KEY` | nx-baas internal API (default `http://openpak-nx-baas:20070`) asked `/internal/title-version` at every gateway login; the verdict is logged as `VERSION ...`, and any failure means allowed |
| `BATTLENET_GAME_UPDATE_PUSH` | Probe: after SetGameVersion, push GameVersion.GameUpdateRequired to a player whose login was outdated (off by default; what the title shows is untested, see `d2r-protocol.md`) |
| `BATTLENET_CHECK_TOKEN_TEMPLATE` | Full check_token JSON template with `${UID}`, overriding the built-in shape |
