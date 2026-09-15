# Isolating Ryujinx — two shared host/joiner profiles

> Trimmed copy for this repository; the canonical document lives at
> `Openpak/servers/shared-docs/ryujinx-isolation.md` and governs. Last synchronised 2026-09-15.

The Ryujinx counterpart of `citron-isolation.md`, for the OpenPak ryujinx
fork (`emulators/ryujinx`). Two independent things to isolate: **data** (so
testing never corrupts your main install's saves/config/keys) and
**network** (so a title's traffic reaches your local mock, never
production).

**Convention (2026-09-02): two shared profiles, not one per game.** Every
OpenPak title is tested through just **two** Ryujinx data dirs, reused
across all games:

| Role | Data dir | Signs into account | Launcher |
|---|---|---|---|
| **host** | `~/ryujinx-instances/host` | `OutboundHost` (pid 1800000003) | `shared-docs/scripts/launch-ryujinx-host.sh` |
| **joiner** | `~/ryujinx-instances/joiner` | `OutboundJoiner` (pid 1800000004) | `shared-docs/scripts/launch-ryujinx-joiner.sh` |

The two accounts are the family-wide shared host/joiner identities (see
`conventions.md`; names are historical). BOTH emulators sign into the SAME two
accounts, so a citron host and a Ryujinx host are the same player.

(Same `shared-docs/scripts/` layout as the citron launchers — a
`launch-ryujinx.sh <host\|joiner>` engine plus the two thin persona wrappers.)

Different games' saves coexist happily in one profile (saves are keyed by
title id), so there is no reason to keep a dir per game. You sign each profile
in **once** and its route table accumulates one line per game. citron follows
the same two-profile model — see `citron-isolation.md`.

> **Migration — TODO (flagged 2026-09-02).** Done with the emulators CLOSED
> (never rename a dir under a running instance):
>
> 1. **Profile dirs.** Fresh dirs are fine — the launchers seed `host`/`joiner`
>    from `~/ryujinx/portable` on first run; then sign into the shared accounts.
>    If you want to keep Stardew's existing saves without reseeding, instead
>    rename the old per-game dirs: `mv ~/ryujinx-instances/stardew
>    ~/ryujinx-instances/host` and `mv …/stardew-join …/joiner` (but those are
>    signed into `stardewhost`/`stardewjoin`, not the shared accounts — re-sign,
>    or migrate saves into a fresh dir). Other games' old Ryujinx dirs can be
>    deleted once their saves are moved into the two shared profiles.
> 2. **Accounts — DONE (2026-09-02).** The shared accounts `OutboundHost`
>    (1800000003, `u-s5jwdwpyopejkzvxcsjq`) and `OutboundJoiner` (1800000004,
>    `u-rxroqs444xrkmhpny2na`) are **mutual friends AND now verified** on the
>    local `openpak-account` server, so Stardew's NPLN auth gate
>    (`IssuePrearrangedUserToken` → `/internal/npln-friends` verified check)
>    passes for them. To verify a local account, hit the stateless link
>    `GET http://127.0.0.1:8099/api/verify?token=<t>` where `<t>` =
>    `b64url("verify.<id>.<exp>") + "." + b64url(HMAC_SHA256(OPENPAK_SECRET,
>    "verify:verify.<id>.<exp>:<lowercased-email>"))` (id = the account's store
>    id; secret = the account server's `OPENPAK_SECRET`). Sets `email_verified`
>    on the live server, no restart.

## 1. Data isolation — the shared profile dir

If a folder named `portable/` exists next to the Ryujinx binary, Ryujinx uses
it for EVERYTHING (config, keys, NAND, saves, logs) instead of
`~/.config/Ryujinx` + `~/.local/share/Ryujinx`. It is detected next to the
BINARY only (`AppDataManager`: `AppDomain.BaseDirectory/portable`), never in
the cwd — so each shared profile passes its data dir explicitly:

```sh
~/ryujinx/Ryujinx --root-data-dir ~/ryujinx-instances/host    # (-r) same layout as portable/
```

(Source-verified 2026-09-01: `cd` + bare launch would silently use the main
portable dir. The launchers below wrap this correctly.)

A fresh profile dir starts empty — the launchers seed it once (keys, firmware,
saves, `Config.json`) from the main portable dir `~/ryujinx/portable/` (which
is linked to a **production** account — a leftover from the pre-port era; the
seed deliberately does NOT copy its account file, so you sign the shared
profile into the local account yourself).

Inside a profile dir (the `legacy_*` file names are the historical on-disk
spelling from the pre-port era — any renaming follows the fork; the OpenPak
ryujinx fork now expresses redirects through its OpenPak network profile /
atmosphere hosts entries rather than the route table):

| Path | What |
|---|---|
| `Config.json` | emulator + input + network settings |
| `bis/user/save/<account>/<saveDataId>/save.dat` | per-account saves (account dirs are `0000000000000001`… in profile order); every game's save lives here side by side |
| `bis/system/Contents/registered/` | firmware |
| `openpak_account.txt` | the linked account (`pid=`, `username=`, `friend_code=`) — `host` or `joiner` (historical name) |
| `openpak_routes.env` | the route table, one line per game (see §2; historical name) |
| `openpak_baas.pem` | id_token signing key (must equal the one `baas-jwks` publishes; historical name) |
| `Logs/Ryujinx_<ver>_<date>.log` | one log file per launch — no shared-log truncation |

**One instance per profile dir.** Two Ryujinx on the same dir clobber
`Config.json`/PTC on exit. The host and joiner dirs are separate, so host+joiner
runs side by side; a second instance on the *same* role's dir is refused by the
launcher.

**Save migration from citron** (measured, S3 2026-09-01): citron keeps
`nand/user/save/<account-hash>/<titleid>/save.dat`; Ryujinx keeps
`bis/user/save/<accountIndex>/<saveDataId>/save.dat`. Close the emulator,
copy `save.dat` into the target account's `0/` and `1/` slots (cover both
saveDataIds), keep a backup.

## 2. Network isolation — env vars + the shared route table

- `OPENPAK_SERVER_IP=<ip>` — all `*.nintendo.net/.com/.wifi.net/.co.jp`
  wildcard redirects. **Unset vars fall back to loopback** — launching without
  them silently sends Nintendo hosts to 127.0.0.1.
- `OPENPAK_NAT_IP=<ip>` — the `nncs2-*` NAT-check hosts (and MK8's NEX host).
- **`openpak_routes.env`** (in the profile dir; historical name, kept on
  disk from the pre-port era — any renaming follows the fork) — the
  declarative table: `host-pattern=ip[:port]`, first match wins, optional
  **port rewrite at connect**, reloaded on mtime change, consulted before the
  built-in rules. Because the profile is shared, this file holds **one route
  line per game** and grows as you test more titles — the launchers **append**
  a line if it is missing and never overwrite the file:

  ```ini
  # ~/ryujinx-instances/host/openpak_routes.env — shared across all games
  e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com=127.0.0.1:18448     # BAAS JWKS (added for you)
  t-9f607adf-lp1.lp1.t.npln.srv.nintendo.net=127.0.0.1:18501             # Stardew NPLN
  t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net=127.0.0.1:18500             # Splatoon 3 NPLN
  ```

  A game adds its own tenant route by passing `OPENPAK_ROUTE=...` to the
  launcher (see §3). Full format and the citron-hook → route-line mapping:
  §2 above and the fork's own docs under `emulators/ryujinx`.

- Built-in patches (certificate chain, pin/verify-flag bypasses) are keyed by
  title build ID in C# tables (`StardewPatches.cs`, …, in
  `src/Ryujinx.HLE/HOS/`) and apply automatically — mods can stay
  disabled.

## 3. The shared launchers

Family-wide launchers in `servers/shared-docs/scripts/` wrap everything
above (X11 forcing, stack preflight, Launch-Protocol guard, first-run seed,
BAAS key, route ensure): an engine `launch-ryujinx.sh <host|joiner>` plus
the two persona wrappers `launch-ryujinx-host.sh` /
`launch-ryujinx-joiner.sh`. Any game's agent runs a wrapper with the game's
NSP:

```sh
# host a game:
OPENPAK_ROUTE="t-9f607adf-lp1.lp1.t.npln.srv.nintendo.net=127.0.0.1:18501" \
  shared-docs/scripts/launch-ryujinx-host.sh "/path/to/Stardew Valley [0100e65002bb8000].nsp"

# join from the other profile:
OPENPAK_ROUTE="t-9f607adf-lp1.lp1.t.npln.srv.nintendo.net=127.0.0.1:18501" \
  shared-docs/scripts/launch-ryujinx-joiner.sh "/path/to/Stardew Valley [0100e65002bb8000].nsp"

# main window only, to sign in the first time (then load a game from the UI):
shared-docs/scripts/launch-ryujinx-host.sh --menu
```

- `OPENPAK_ROUTE` — the game's NPLN tenant → its local server; ensured in the
  shared profile's route table, kept across launches. (Launcher var; the
  historical name is factual for the scripts in this bundle.)
- `OPENPAK_ALLOW_SHARED=1` — launch alongside another running Ryujinx
  **after asking** (a kill/launch can take down another title's session — see
  `emulator-launch-protocol.md`).
- Overridable env: `RYUJINX_BIN`, `RYUJINX_SEED_DIR`, `RYUJINX_DATA_DIR`,
  `OPENPAK_STACK_DIR`. The two wrappers just `exec launch-ryujinx.sh host` /
  `… joiner`; all the logic is the one engine.

Per-repo wrappers (e.g. `servers/stardew-valley/scripts/launch-ryujinx.sh`)
just call these with their own NSP + `OPENPAK_ROUTE` baked in, so
`--menu`/`--joiner` still work per game.

Why X11 is forced: Ryujinx's Avalonia window can pick Wayland while its Vulkan
surface uses XWayland → BadMatch. The launchers run under
`env -u WAYLAND_DISPLAY XDG_SESSION_TYPE=x11 GDK_BACKEND=x11`.

Verify isolation on the running process (anchor the pattern at the actual
binary path the launcher used — the fork builds from `emulators/ryujinx`):

```sh
pgrep -af Ryujinx                                  # then confirm each cmdline
tr '\0' '\n' < /proc/<pid>/environ | grep -i -e openpak -e legacy
ls -l /proc/<pid>/cwd                           # which profile dir owns it
```

## 4. Log mining quick reference

```
Trying to resolve: <host>                       # every getaddrinfo (post-mitm value)
[OpenPak] Route table: '<host>' -> <ip:port>   # declarative route matched
[OpenPak] Route table: port 443 -> <port>      # port rewrite applied
[OpenPak] Connect target was 0.0.0.0:443 (lost address) -> substituting <ip>
[OpenPak] grpc connect completed synchronously -> SUCCESS
[OpenPak] Retenue npln terminee apres N ms (JIT calme)   # first npln resolve held until JIT calms
```

The gRPC "lost address" substitution and the synchronous-connect completion
are fork-specific repairs — if a title's connect shows `0.0.0.0` with **no**
substitution line, no redirect covered that port (peer connection? route gap?).

## Multi-game routing

Two models coexist:

- **Simple (no SSL):** each game's server publishes its own host port
  (18500, 18501, …) and the shared route table rewrites the title's hostname to
  it — `t-…=127.0.0.1:18501`. No edge, no certificates. The OpenPak workspace
  plays this role now: per-title game servers under `servers/`, account/BAAS/
  NPLN/NNCS services at the workspace root. (Historically the pre-port
  `legacy-local` bundle shipped this — its website container also proxied
  `/api/*` to the account server so browser login worked over plain HTTP on
  :8081.)
- **Named (TLS edge):** when you want hostnames/TLS (real consoles, browser
  cert validity), give every game a hostname on your Traefik and keep the
  route table uniform:

```ini
# openpak_routes.env — one line per game, all pointing at Traefik :443
t-9f607adf-lp1.lp1.t.npln.srv.nintendo.net=127.0.0.1:443   # Stardew
t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net=127.0.0.1:443   # Splatoon 3
e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com=127.0.0.1:443  # BAAS JWKS
```

Traefik then routes by the SNI each game actually sends (its real Nintendo
hostname) to that game's backend — TCP **passthrough**: the game terminates
TLS itself against its own server's certificate, exactly like `sni-router`
does in production. Backends never share ports.

Per-game **operator names** (`stardew.tobagin.eu`, `account.tobagin.eu`, …)
live on the same Traefik with real ACME certificates, for the surfaces a
browser touches (OAuth sign-in) and for direct service access.

Ready-made edge rules and the full local stack now live in the OpenPak
workspace itself (the root service dirs — account, BAAS, NPLN, NNCS — plus
`servers/<name>` game servers; see the workspace README). The pre-port
`legacy-local` bundle survives only as the frozen copies under
`~/REPOS/earlier-outbound` — a historical reference, not a live dependency.
Whatever serves the stack: the emulator's account deployment must match the
one the NPLN server validates against.
