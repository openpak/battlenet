# Isolating citron for per-title OpenPak research

> Trimmed copy for this repository; the canonical document lives at
> `Openpak/servers/shared-docs/citron-isolation.md` and governs. Last synchronised 2026-09-15.

Applies to any OpenPak game project (`servers/<name>`) using the OpenPak
citron fork (`emulators/citron`) as its test client. Two independent things
to isolate: **data** (so testing one title never corrupts another's
saves/config/keys) and **network** (so a title's traffic reaches your local
mock, never the real production servers).

## 1. Data isolation — two shared personas (host + joiner), portable mode

On Linux, if a folder named `user/` exists in citron's *current working
directory* at launch, citron uses it for everything (config, saves, keys,
NAND, cache) instead of `~/.local/share/citron` + `~/.config/citron`.

**Do not make a new profile per title.** The family uses exactly **two
shared citron profiles** — a **host** and a **joiner** persona — reused
across *every* OpenPak game (2026-09-02 consolidation). Each holds its
Switch keys and firmware once; you add a game's content when you first test
it. The two personas are the isolation that matters: host vs joiner never
share a data dir (so two concurrent instances never truncate each other's
log or clobber config), and each is signed into its own shared account.

| Persona | Profile (portable `user/`) | Shared account |
|---|---|---|
| host   | `~/.local/share/citron/host`   | `OutboundHost` (pid 1800000003) |
| joiner | `~/.local/share/citron/joiner` | `OutboundJoiner` (pid 1800000004) |

(The profile root `~/.local/share/citron` keeps its historical
pre-port name; the account names are historical earlier identities —
treat them as the generic host/joiner accounts.)

These live **outside any git repo** — they hold Switch keys/firmware, which
must never enter a repo tree, even gitignored. The accounts are the two
generic host/joiner identities for all titles; the Ryujinx side mirrors the
same two personas (see `ryujinx-isolation.md`).

Launch through the shared scripts in `servers/shared-docs/scripts/`, which
cd into the right persona, enforce the Citron Launch Protocol, and refuse
the real production account:

```sh
servers/shared-docs/scripts/launch-citron-host.sh    [game.nsp]   # or empty for citron's GUI list
servers/shared-docs/scripts/launch-citron-joiner.sh  [game.nsp]
```

A game adds its title-specific redirect env (below) before calling these —
e.g. Outbound (`servers/outbound`) wraps them with `OPENPAK_PHOTON_IP`.
A fresh persona starts empty; copy your keys/firmware in yourself, same as
any new citron install.

## 2. Network isolation — exact-host redirect hook, not the generic one

`OPENPAK_SERVER_IP` (with `OPENPAK_ENABLE=1`) only covers Nintendo
BAAS/NEX-style hostnames. If your title uses a different backend
(Demonware, Photon, EOS, etc.), it will not be covered, and its traffic
will silently reach the real production servers instead of your mock.

Check `emulators/citron/src/core/hle/service/sockets/sfdnsres.cpp` for a
hook already matching your title's hostname — search for functions named
like `Get<Title>RedirectIp`/`Get<Title><Service>RedirectIp` (e.g.
`GetFallGuysEosRedirectIp`, `GetPhotonRedirectIp`,
`GetCtrDemonwareAuthRedirectIp`; those function names are the real
fork-source names). Note that many existing hooks still read their env var
under the legacy `OPENPAK_<TITLE>_*` spelling — the **generic** redirect
vars are `OPENPAK_*`. If no hook exists for your hostname, add one by
mirroring an existing hook exactly:

1. A small function, exact-hostname match only, gated by its own env var
   (unset by default, so it never affects any other title's run):

   ```cpp
   static std::optional<std::string> Get<YourTitle>RedirectIp(const std::string& host) {
       if (Common::ToLower(host) != "your.exact.hostname.example.com") {
           return std::nullopt;
       }
       const char* env = std::getenv("OPENPAK_<YOURTITLE>_IP");
       if (!env || !*env) {
           return std::nullopt;
       }
       LOG_INFO(Service, "[OpenPak] Redirecting <YourTitle> host '{}' -> '{}'", host, env);
       return std::string(env);
   }
   ```

2. Wire it into the redirect chains in both
   `GetHostByNameRequestImpl` and `GetAddrInfoRequestImpl` (there are two
   near-identical resolution paths in that file — add it to both).

3. If the target port is already taken on your host (443 often is — check
   with `ss -ltnp`), add a matching port-remap block in `bsd.cpp`'s
   `ConnectImpl`, gated by its own env var, keyed off `GetLastHostForIp`
   for your exact hostname (mirror the existing Fall Guys or CTR:NF port
   blocks there).

4. Rebuild incrementally: `cd build/<your-build-dir> && ninja citron
   citron-cmd` — only the touched files and the final link need to
   rebuild, this is fast (~1 minute), not a full rebuild.

Then write a small local mock server for that endpoint. A self-signed TLS
certificate generated in memory (never written to disk — no cert/key file
to ever accidentally commit) is enough, since citron already bypasses
certificate verification for OpenPak-redirected hosts.

### Env var vs profile setting — the setting wins

`GetConfiguredIp()` in `sfdnsres.cpp` returns the profile SETTING
(`openpak_server_ip` / `openpak_nat_ip` under `[Network]` in
`qt-config.ini`) whenever it is non-empty, and only then the env var
(`OPENPAK_SERVER_IP` / `OPENPAK_NAT_IP`). A fresh profile defaults
`openpak_server_ip` to the production server IP, so the env var is silently
ignored and generic-redirect hosts reach production. Pin the settings in the
profile (Qt discards a value whose `key\default=true` marker is set; also
set `enable_openpak=true` — redirection is off by default):

```ini
[Network]
enable_openpak=true
openpak_server_ip\default=false
openpak_server_ip=127.0.0.1
openpak_nat_ip\default=false
openpak_nat_ip=127.0.0.1
```

and verify with `grep '^openpak_server_ip=' user/config/qt-config.ini`
before launch (`servers/stardew-valley/scripts/launch-citron.sh` does this
and fails closed). The per-title hooks are env-only and unaffected by the
profile settings — several still carry their legacy `OPENPAK_*` names in
the fork source (`OPENPAK_<TITLE>_IP`, `OPENPAK_S3_DEBUG_PROXY_*`,
`OPENPAK_BAAS_JWKS_PORT`, …); the account API var is `OPENPAK_API`.

## 3. Launch through the shared persona scripts

Never launch citron by hand — a bare relaunch after a crash silently drops
the isolation env and reaches the real server (this has happened more than
once). Use the shared scripts in `servers/shared-docs/scripts/`:

- `launch-citron.sh <host|joiner> [game args]` — the engine: cd into the
  persona's portable profile, enforce the Citron Launch Protocol (refuse a
  foreign citron unless `CITRON_ALLOW_FOREIGN=1`), refuse the production
  account, then exec with `OPENPAK_ENABLE=1` + `OPENPAK_API`.
- `launch-citron-host.sh` / `launch-citron-joiner.sh` — the two named
  entry points every agent calls.

A title adds its own redirect env by wrapping these in two lines, e.g.
`servers/outbound/scripts/launch-citron-host.sh` sets `OPENPAK_PHOTON_IP`
then execs the shared host script. Env overrides: `OPENPAK_CITRON_BASE`
(profile root), `CITRON_BIN`, `OPENPAK_API`.

## Verifying isolation is actually active

Before testing, on the exact running citron PID:

```sh
tr '\0' '\n' < /proc/<pid>/environ | grep -i -e openpak -e legacy
readlink -f /proc/<pid>/cwd
```

Confirm the redirect env vars are present (generic vars are `OPENPAK_*`;
legacy per-title hook vars may still spell `OPENPAK_*`) and the cwd is
your isolated profile directory. Don't assume — a bare relaunch
(double-click, bare shell command, desktop shortcut) will not carry either.
