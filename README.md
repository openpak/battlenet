# Battle.net

OpenPak's Battle.net Server.

| | |
| --- | --- |
| Networks | Nintendo Switch |
| Protocol | Battle.net HTTP, BGS RPC over WebSocket, Diablo II game-server TCP |
| Image | `ghcr.io/openpak/battlenet` |
| Licence | AGPL-3.0-only |

## Titles

| Game | Network | Title ID | Module |
| --- | --- | --- | --- |
| [Diablo II: Resurrected](https://openpak.org/titles#t-109) | Nintendo Switch | `0100726014352000` | [D2R protocol](docs/d2r-protocol.md) |

## Networks

| Network | Reached through |
| --- | --- |
| Nintendo Switch | TLS terminated upstream (Traefik) for `geo.battle.net`, `telemetry-in.battle.net`, `account.battle.net` and `d2gs.battle.net`; the gateway is `us.actual.battle.net:1119` |

## What it serves

| Listener | What | Port |
| --- | --- | --- |
| Web tier | Host-routed HTTP: `geo.battle.net` region answer, `telemetry-in.battle.net` sink, `account.battle.net` login | TCP 21500 |
| Gateway | `v1.rpc.battle.net` WebSocket RPC logon, and D2R-services at `/OSI/v2/rpc/client` | TCP 21501 |
| Game server | Diablo II game-server handshake, dialled as `d2gs.battle.net` | TCP 21503 |

## Run

```sh
go run ./cmd/battlenet
```

| Variable | Effect |
| --- | --- |
| `BATTLENET_HTTP_ADDR` / `BATTLENET_GATEWAY_ADDR` | Listener addresses (`:21500` / `:21501`) |
| `ACCOUNT_GRPC_ADDR`, `ACCOUNT_INTERNAL_KEY` | Account-core connection for identity |

The full list of variables is in [docs/deploy.md](docs/deploy.md).

## Docs

- [protocol.md](docs/protocol.md) — hosts, endpoints and the listener behaviour
- [evidence.md](docs/evidence.md) — captures, traces and test results
- [deploy.md](docs/deploy.md) — ports, env vars, image, build and run
- [notes.md](docs/notes.md) — role, ground rules, history
- [d2r-protocol.md](docs/d2r-protocol.md) — what the D2R title expects online: bgs logon flow, D2R-services connection, message layouts
- [README.md](docs/README.md) — index of this folder

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
