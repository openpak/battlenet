# Online Compatibility Playbook

> Trimmed copy for this repository; the canonical document lives at
> `Openpak/docs/playbooks/online-compatibility-playbook.md` and governs. Last synchronised 2026-09-15.


A general method for taking a Switch title running under an OpenPak test
emulator — **the citron fork (`emulators/citron`) or the ryujinx fork
(`emulators/ryujinx`)** — whose online mode
fails, for **any backend** (NEX, NPLN, NNNC, Photon, Demonware, EOS, QUIC,
proprietary UDP/TCP) and **any function** the client needs from a server
(identity/auth, certificates/config, matchmaking, presence/friends, saves,
sessions, P2P/relay, telemetry) — diagnosing **which layer** is failing, and
building the missing compatibility piece in the right order.

Written for the assistant/agent doing the work. Worked examples live in each
project repo's `next-session.md` and `docs/` (Stardew/NPLN →
`servers/stardew-valley`, CTR:NF/Demonware → `servers/crash-team-racing`,
Fall Guys/EOS → `servers/fall-guys`, MK8/NEX/Pia →
`servers/mario-kart-8-deluxe`, Outbound/Photon → `servers/outbound` — see
also `citron-isolation.md` and `ryujinx-isolation.md` in this folder for
per-title data/network isolation).

Ground rules, non-negotiable:

- **Clean-room**: proprietary binaries, dumps, certificates, keys, tokens,
  captures, and player data stay **outside every repository**. Only
  independently written code and sanitized conclusions enter. Never seek or
  store official signing keys.
- **No cross-title protocol guessing.** Another game's *payload handlers* are
  that game's. What transfers between titles is the *method*, the *tooling*,
  and standard-protocol knowledge (TCP/TLS/UDP/HTTP specs, NAT behavior).
- **One variable per experiment.** Change a single thing, retest, record.
  An experiment that fails is a result — it eliminates a layer.
- **Emulator Launch Protocol** (`emulator-launch-protocol.md`, this folder) before
  killing or restarting any emulator instance. Ryujinx instances follow the
  same courtesy: identify by full binary path
  (`pgrep -af "^~/ryujinx/Ryujinx"`), never broad-`pkill`
  (a `-f` pattern matches your own shell).

---

## 0. Choose the emulator — and always know the other one

Two test clients exist and they are **not** interchangeable:

| | citron fork (`emulators/citron`) | ryujinx fork (`emulators/ryujinx`) |
|---|---|---|
| Language/build | C++ (ninja, ~1 min incremental) | C#/.NET (`dotnet publish`, minutes) |
| NPLN titles (S3) | **works** against production (long-standing) | **works** |
| Redirects | env-gated C++ hooks per title (`sfdnsres.cpp`) + `bsd.cpp` port remaps; generic vars `OPENPAK_SERVER_IP`/`OPENPAK_NAT_IP` (per-title hook vars may still spell `OPENPAK_*`) | built-in wildcards (`OPENPAK_SERVER_IP`/`OPENPAK_NAT_IP`) + declarative `openpak_routes.env` route table (host=ip:port, port rewrite; historical name) |
| Built-in title patches | C++ loader (`nso.cpp`, IPS32 via `FileSys::PatchIPS`, original-byte fingerprints) | C# tables (`StardewPatches.cs`, …, in `src/Ryujinx.HLE/HOS/`), IPS32 via `MemPatch.Patch(program, 0x100)` — **offsets are flat-NSO file offsets (memory + 0x100)** |
| Data isolation | two shared persona profiles `~/.local/share/citron/{host,joiner}` (portable `user/` cwd; historical dir name) | two shared persona dirs `~/ryujinx-instances/{host,joiner}` (`--root-data-dir`, same layout as `portable/`) |
| Log | persona profile's `log/citron_log.txt` (truncated per start) | profile's `Logs/Ryujinx_<ver>_<date>.log` (one file per launch) |

Rules of thumb:

1. **Baseline on both when possible.** A failure that appears on one emulator
   only is an *emulator gap*, not (only) a protocol gate. Measured example:
   S3 gets online under Ryujinx and fails under citron → chase citron, not the
   protocol.
2. Pick whichever emulator already gets closest to online for your backend; do
   not fix infrastructure in two places at once.
3. Never run two instances against the same data dir (log truncation, config
   clobbering). citron: one instance per profile. Ryujinx: one per `portable/`
   dir.
4. **Trust long-standing working setups over one-off failures.** A single
   failed session on a stack that "always worked" is usually a
   session confound (region selection, account state, transient server
   state) — reproduce it before believing it. Measured example (2026-09-01):
   one citron+S3+production attempt failed 2321-4992 and briefly led the
   investigation to a phantom "citron NPLN gap"; the stack had always
   worked.

---

## 1. The layered model

A title going online passes through gates, in order. Each gate has a
characteristic symptom when it fails, and a matching tool to interrogate it.

| # | Gate | Typical symptom of failure | Interrogation tool |
|---|------|----------------------------|--------------------|
| 1 | **Name resolution** | nothing dials; wrong hosts; instant error | emulator DNS logs (citron `sfdnsres`, Ryujinx `DnsMitmResolver`); redirect hooks/route table; local DNS/hosts |
| 2 | **Transport (TCP/UDP)** | connects refused/timeout; NAT checks unanswered | bsd/sock logs; UDP responders; port taps |
| 3 | **Session security (TLS/DTLS/custom crypto)** | handshake completes or dies; chain vs pin errors | cert chain patches; pin-flag patches; local TLS terminator; cert mimicry tests |
| 4 | **Protocol handshake** | preface/SETTINGS/init exchange then close | protocol-aware probe; full reference server |
| 5 | **Identity & credentials** | connects then cancels first request; auth errors | account server; token supply; ACC/IPC trace; memory forensics |
| 6 | **Feature RPCs / payloads** | requests arrive; wrong replies; partial function | reference server handlers; replayed/synthesized payloads |
| 7 | **P2P / session data** | lobby/feature works, connect-to-peer fails | NAT responders, relay, STUN/TURN, address rewriting |

**Method**: run the title once per change; mine the host's own logs first
(they are metadata, cheap, and lawful); only then escalate to payload-level
tools. A gate "below" the failing one being *proven good* is as valuable as
finding the failing one.

---

## 2. The universal interrogation tools

These recur at every layer. Build them once, reuse per title.

- **Log mining.** The emulator's own HLE logs are the highest-value,
  lowest-risk source: DNS resolution order, connect/recv/send sizes, IPC calls
  with results. citron: the persona profile's `log/citron_log.txt` — beware,
  every citron start on the same profile truncates it; run **one instance per
  profile** and don't boot other titles mid-test. Ryujinx:
  `Logs/Ryujinx_<ver>_<date>.log` in the profile dir — one file per launch (no
  truncation), `|I| {Common} ServiceSfdnsres GetAddrInfoRequestImpl: Trying to
  resolve: …` lines give the DNS flow; grep is your friend.
- **Redirect hooks / route table.** Move one hostname (or a whole domain) to
  your infrastructure.
  - citron: env-gated C++ hooks per title (`Get<Title>RedirectIp` in
    `sfdnsres.cpp`, wired into BOTH resolution chains) + companion port remaps
    in `bsd.cpp` (`ConnectImpl`). Adding a title = editing C++ and rebuilding.
    The generic vars are `OPENPAK_*`; legacy `OPENPAK_*` spellings survive
    only as per-title hook names in the fork source.
  - Ryujinx: **`openpak_routes.env`** in the profile dir (historical name;
    any renaming follows the fork) —
    `host-pattern=ip[:port]`, first match wins, `*`/`?` wildcards, optional
    port rewrite at connect, reloaded on file mtime change. Adding a title =
    editing a text file. See `ryujinx-isolation.md` §2 in this folder.
- **Sanitized probes.** A local terminator that logs **metadata only**:
  handshake parameters, message frames (type/flags/size), safe header or field
  *names*, byte counts, timing. Redaction must be enforced by construction
  (values never copied), with tests proving it. Probes answer: *did the client
  speak, what did it say, when did it stop?*
- **Full reference servers.** The family already implements several backends
  (`servers/splatoon-3` = NPLN/gRPC, the workspace account service =
  identity/friends/BCAT, `nn-nncs` = NAT responder, NEX secure servers,
  Photon/Demonware stubs). Putting a complete reference implementation on the
  tapped port splits the world in two: if the client *still* refuses to speak,
  the gate is client-local; if it speaks, the delta is in payloads/handlers.
- **Memory forensics.** Guest RAM is readable (`/proc/<pid>/mem`, chunked —
  large mappings fail whole-read). Fastest way to answer "did the client
  actually parse/decrypt/store X": find the known plaintext (a token, a
  decoded structure) and inspect context. Derive the loaded-module base from
  an anchor string to translate addresses. Guest code is JIT-translated in
  both emulators — hardware breakpoints on guest bytes never hit; static
  analysis + memory reads are the tools. Distinguish static rodata strings
  from runtime copies: a diagnostic that *ran* appears in rw- mappings.
- **Binary analysis & build-scoped patches.** Convert the active-update module
  (NSO→ELF via `~/REPOS/nx2elf`), run full Ghidra headless analysis (hours —
  see operational rules), anchor on the game's own diagnostic strings
  (ADRP+ADD xref scanning), decompile with `pyghidra`, and derive the minimal
  patch (often a 4-byte flag/result force). Patches are exact-build,
  fingerprint-guarded:
   - citron: C++ entries in `src/core/loader/nso.cpp` (title + module + build
     ID + original-byte fingerprint).
   - Ryujinx: C# IPS32 tables (`src/Ryujinx.HLE/HOS/<Title>Patches.cs`, e.g.
     `StardewPatches.cs`). **Offsets there are
     flat-NSO file offsets = memory offset + 0x100** (`MemPatch.Patch`
     subtracts the protected offset).
- **Differential testing.** When two similar titles behave differently on the
  same infrastructure, or one title behaves differently on two servers **or on
  two emulators**, the *delta* is the finding. Change one variable per run:
  server variant, cert shape, account state, patch on/off, emulator. Measured
  example (2026-09-01): S3 online under Ryujinx, 2321-4992 under citron on the
  same day/servers ⇒ citron gap, not a protocol gate.

## 3. The kinds of "information a server must provide"

Whatever the backend, client needs fall into a few classes. Identify which
class is missing and where the client expects to get it:

1. **Identity** — who the player is on *this* service (account link, PIDs,
   user ids, friend codes). Source: the account/identity server; on
   emulators, often fabricated by HLE from the linked account. *Symptom of
   absence:* connects then cancels first request; "account unavailable"
   results; requests never constructed.
2. **Credentials/tokens** — proof of identity for each service (id_tokens,
   access tokens, device/dauth credentials, per-service scopes). Often
   **exchanged** from identity tokens via a bootstrap RPC. *Symptom:* auth
   RPC attempted with empty/invalid credential, or cancelled pre-request.
   Emulator token fabrication is per-title-sensitive: both emulators hardcode
    the BAAS id_token `aud` to S3's client id (`ed9e2f05d286f7b8`) with a
    random `sub` — a title whose SDK cross-checks claims locally will abort
    pre-send. citron exposes `OPENPAK_BAAS_SUB` to align `sub` with the
    linked uid.
3. **Trust material** — what makes the client accept the *server* (CA chains,
   pinned certs/SPKI hashes, PSKs). *Symptom:* handshake completes (or not)
   then immediate close; documented error codes tied to cert verification.
   Fix: chain-validation patches, pin-flag/verify-callback patches, and/or
   serving certificates whose verifiable properties match (test mimicry first
   — it is cheap — but expect hash-based pins).
4. **Configuration/environment** — per-title settings the client cannot
   hardcode: endpoints, tenant/environment ids, feature flags, schedules,
   BCAT/content caches. *Symptom:* connects but skips requests; feature "not
   available" errors. Sources: NSD/environment services, config files, BCAT
   responders, pre-gRPC identity gateways (NPLN: vermillion/penne REST —
   device init, login tickets, accounts/config with `online_license`).
5. **Feature state & payloads** — the actual game data (schedules, rosters,
   documents, tickets, save records). *Symptom:* RPCs arrive at your server
   and it answers wrong/empty. This is where per-title handlers are written,
   and where captured-measured bytes (if any) are least redistributable —
   synthesize from observed structure, or replay only what you lawfully hold.
6. **Session/peer coordination** — the data that lets two clients find each
   other (session ids, station URLs, external addresses, relay triggers).
   *Symptom:* matchmaking or feature RPCs work; peer connect fails. Sources:
   NAT responders, address-rewriting layers, relay/TURN.

For each class, the playbook question is the same: **what exact bytes/fields
does the client expect, where does it expect them from, and what does it do
locally when they are missing?** Answered top-down by: observing the request
(probe/server logs), reading the client's own diagnostic strings, and
memory-checking what it parsed.

## 4. Standard experiment sequence

1. **Baseline**: reproduce the failure with production redirects; capture the
   ordered flow and error code; write the sanitized summary. If a second
   emulator exists, run the same baseline there (the delta may be the finding).
2. **Host-local sanity**: account linked, config present, single instance,
   correct profile/version (an outdated or base-only install is a classic
   false lead — documented per title in the emulator worktrees' patch notes).
3. **Tap the failing service** to a local terminator/probe; observe the
   client's messages directly (metadata first). citron: env hooks + port
   remap. Ryujinx: one `openpak_routes.env` line with `:port`.
4. **Serve the minimum**: reference server or probe answering only what was
   observed — never invented. Observe what changes.
5. **Climb the credential/config chain**: when a bootstrap request is
   *attempted* with a wrong/missing input, trace where that input should come
   from (upstream service, HLE fabrication, config file) and fix the source —
   not the symptom.
6. **Patch locally** when a client-side check refuses correct behavior
   (pinning, exact-hostname compares, feature gates): find the check via
   string anchors + decompilation, patch exact-build with fingerprints
   (citron) or build-keyed IPS32 tables (Ryujinx), document it in the patch
   table.
7. **Re-baseline after every fix** — including a run against the *real*
   production service where safe, so regressions and pre-existing breaks are
   not misattributed.

## 5. Operational rules (each paid for, in real hours)

- `/tmp` is tmpfs: dies on reboot and cannot hold multi-GB databases or
  sprawling logs. Disk-backed paths for analysis projects, logs, and anything
  kept. The agent tooling's own scratch lives under `/tmp` too: three 4 GB memory
  dumps written there filled the 15 GB tmpfs (2026-09-02) and killed the
  shell tool for every session (`EDQUOT`). Multi-GB dumps, service
  logs and Ghidra projects go on the pool: `/mnt/media/research/`. Ghidra project paths also cannot contain dot-prefixed elements.
- Ghidra headless on a ~200 MB AArch64 image: hours, multi-GB heap/disk, log
  spam in the millions of lines — launch unattended, background, disk-backed
  project. The reconstructed ELF is PIE with 0-based VAs; Ghidra imports it at
  base 0x100000 (`Ghidra addr = VA + 0x100000`). The flat NSO keeps its
  0x100-byte header (text file offset = VA + 0x100); verify per-segment deltas
  from the NSO segment table instead of assuming. Ghidra's auto-analysis
  leaves coroutine/SDK regions undiscovered: create functions manually
  (`CreateFunctionCmd`, USER_DEFINED) after boundary-finding via BL-target
  scans; commit the pyghidra transaction or project save fails and leaves a
  stale `.lock`.
- `pkill -f` patterns match your own shell: anchor at the binary path
  (`pgrep -af "^~/ryujinx/Ryujinx"`), or you kill your own script
  mid-run.
- One emulator instance per data dir; concurrent instances truncate shared
  logs and clobber config. Follow the launch protocol before killing
  anything; never broad-`pkill` (self-match + other titles' sessions).
- citron's `bsdsocket` IPC is single-threaded: a blocking delay in one socket
  call blocks them all.
- Ryujinx on Wayland: force X11 for the game window
  (`env -u WAYLAND_DISPLAY XDG_SESSION_TYPE=x11 GDK_BACKEND=x11`) or Vulkan
  surfaces can BadMatch. Its NPLN init already holds the first npln
  resolution until the JIT burst calms (`MaybeDelayNplnInit`) — a startup
  race citron only has behind `OPENPAK_NPLN_DELAY_MS`.
- Rate limits exist on local test services (e.g. account registration) —
  create test accounts well before you need them.
- `GDB stub`-style guest debugging is unreliable on this stack; prefer
  reproducible offline analysis and memory inspection. Guest code is
  JIT-translated: hardware breakpoints never hit.
- Registration/test data (usernames, passwords, tokens) lives outside
  repositories; never in scripts, logs, or handoffs.

## 6. Recording results

Every experiment goes into the project's `next-session.md` / `docs/`:
hypothesis, method, decision rule, observation (with confidence labels:
CONFIRMED/HIGH/MEDIUM/LOW/SPECULATIVE), artifacts note. Rejected approaches
are recorded too — eliminated layers are progress. Sanitized summaries only;
raw logs, dumps, and certificates stay outside the repository, referenced by
local path.
