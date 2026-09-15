# Family conventions

> Trimmed copy for this repository; the canonical document lives at
> `Openpak/docs/playbooks/conventions.md` and governs. Last synchronised 2026-09-15.


Rules and shapes shared by every OpenPak project — the `servers/<name>` game
servers and the `emulators/<name>` forks. When a repo deviates, it needs a
reason written down in that repo.

## Licensing

- **OpenPak servers and research repos** (the `servers/<name>` family):
  **AGPL-3.0-only**. The pre-port earlier repos stay **source-available
  1.0.0**, `Required Notice: Copyright 2026 earlier network` — do not
  silently relicense across repos, and never copy text or code across the
  license wall (see `../../docs/clean-room-policy.md`).
- **Emulator forks** (`emulators/citron`, `emulators/ryujinx`): keep their
  upstream licenses (GPL). Compatibility code contributed there follows the
  upstream license, never the server license.
- Every upstream repository's license is preserved; never copy code across
  licenses implicitly.

## Clean-room policy (all repos)

- The canonical statement lives in the OpenPak workspace:
  [`docs/clean-room-policy.md`](../../docs/clean-room-policy.md). Summary:
- Never commit: proprietary binaries, dumps, certificates, keys, tokens,
  raw captures, player data, decompiled source, production identifiers.
- Interoperability is expressed as **independently written code and sanitized
  conclusions**. Raw research inputs live outside every repository
  (`~/.local/share/<project>-research/`, emulator profiles) and are referenced
  by local path or env var only.
- Never seek or store official signing keys. A compatibility patch that a
  key would make unnecessary is still the correct clean-room answer.
- If unsure whether an artifact is safe to commit: keep it outside and
  document only the behavior it demonstrates.

## next-session.md (per project)

Every repo's living handoff is its root **`next-session.md`** (lowercase —
`handoff.md`/`HANDOFF.md` are legacy pre-port names). Structure:

- Project objective · Current status (dated one-liners) · Next steps
- Architecture · Environment · Pointers into the repo's own docs
- Authentication, startup, matchmaking, session, disconnect flows
  (**UNCONFIRMED HYPOTHESIS** labels until measured)
- **Experiments** (records kept in the repo's `docs/`): hypothesis → method →
  decision rule → observation (confidence-labeled
  CONFIRMED/HIGH/MEDIUM/LOW/SPECULATIVE) → artifacts.
  Rejected experiments are recorded too — eliminated layers are progress.
- Confirmed behaviors · Unconfirmed hypotheses · Known failures · Decisions
- Security/secrets notes · Useful commands · Session log

Alongside it, every repo keeps a root **`CHANGELOG.md`** (dated one-line
history), a **`docs/`** for project-specific documents — protocol notes,
hostname/endpoint inventories, trimmed docs/shared copies — and a **`prds/`**
for design documents.

## Experiments

- One variable per run; state the decision rule BEFORE running.
- Host-local sanity (account linked, config present, single instance, correct
  title version) before deep debugging.
- Sanitized summaries only in the repo's `next-session.md`/`docs/`; raw
  logs/dumps stay external.

## Test personas & accounts (shared across every title)

- Testing uses **two shared personas — host and joiner — not a profile or
  account per game** (2026-09-02 consolidation). Each emulator keeps exactly
  two profiles, reused for all titles:
  - citron: `~/.local/share/citron/{host,joiner}` (portable mode;
    the persona dir keeps its historical pre-port name), launched via
    `tools/launch-citron-{host,joiner}.sh`.
  - Ryujinx: the two `~/ryujinx-instances/{host,joiner}` portable dirs,
    launched via `tools/launch-ryujinx-{host,joiner}.sh`
    (see `ryujinx-isolation.md`).
- **Two shared accounts** back the personas for every game — host =
  `OutboundHost` (pid 1800000003), joiner = `OutboundJoiner` (pid 1800000004).
  Do not mint per-game accounts. (Names are historical from the earlier era;
  treat them as the generic host/joiner identities.)
- A title adds only its own **redirect env** on top (a two-line wrapper that
  sets `OPENPAK_<TITLE>_*` / `OPENPAK_PHOTON_IP` then calls the shared
  launcher) and, on first test, its game content into both profiles. Never
  fork a whole new profile/account set for a title.

## Env-var style (emulator hooks)

- `OPENPAK_<TITLE>_<PURPOSE>` uppercase; **unset by default** so a hook never
  affects another title's run. The generic redirect vars are `OPENPAK_ENABLE`,
  `OPENPAK_SERVER_IP`, `OPENPAK_NAT_IP`, `OPENPAK_PHOTON_IP`, …; legacy
  `OPENPAK_*` spellings survive only as per-title redirect-hook names inside
  some fork sources — check the neighbouring hooks and follow the file.
- DNS hooks: exact-host match preferred (substring/glob only for deliberate
  taps), wired into BOTH resolution paths the title uses; fail closed.
- Port remaps live next to their DNS hook and are keyed off the resolved
  host. Prefer declarative routing where the fork offers it (the historical
  `openpak_routes.env` table; see `ryujinx-isolation.md`) for new titles
  unless emulator code must change anyway.

## Repo layout & tooling

- Game servers live at `servers/<name>` (Go or C#), single responsibility per
  repo, README states the backend and what is/isn't implemented. Shared docs
  live at `docs/playbooks/` in the workspace root; emulator forks at `emulators/citron`
  and `emulators/ryujinx`.
- Each server repo keeps: `README.md` (what works/what doesn't),
  `next-session.md`, `CHANGELOG.md`, `docs/` (project-specific docs + trimmed
  docs/shared/ copies), `prds/`, `.env.example`, `LICENSE`.
- Analysis tooling: `~/REPOS/nx2elf`, `~/.local/opt/ghidra_*`. Keep those
  paths stable — docs and scripts depend on them.
- Historical note: the frozen pre-port earlier repos live under
  `~/REPOS/earlier-outbound` — reference only, not part of this workspace.

## Shared-docs hygiene

- The **canonical** versions of these docs live at `Openpak/docs/playbooks/`.
  A doc belongs here when it is true for ≥2 projects; title-specific facts
  stay in the title's repo.
- Repos do not symlink or mirror the bundle: each repo receives **trimmed,
  per-project copies** of only the docs it needs, kept in its own `docs/`
  and each starting with a `trimmed copy, canonical at docs/playbooks/`
  header pointing back here.
- When an operation costs real hours and yields a rule, add the rule to the
  canonical doc here (then refresh the affected trimmed copies) instead of
  relearning it.
