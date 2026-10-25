# Protocol: battlenet

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
