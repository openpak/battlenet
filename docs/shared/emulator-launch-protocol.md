## Emulator Launch Protocol (family-wide)

Applies to every test emulator the family runs — currently the OpenPak
forks `emulators/citron` (builds to `build/*/bin/citron`) and
`emulators/ryujinx`, and any future emulator fork.

- The assistant may launch/kill/restart an emulator autonomously when either
  (a) **no other emulator instance is running at all**, or (b) the only
  running instance is the project's own — identified by the project title's
  NSP path on the command line — including closing and relaunching it to test
  freshly rebuilt binaries. NOTE: both emulators now run two SHARED personas
  (host + joiner) reused across all games, so the profile dir / redirect
  vars no longer tell you which *title* is running — the **game NSP path on
  the cmdline is the only title identifier**. Always check it before
  killing anything.
- ASK FIRST when any OTHER title's instance is running, on ANY emulator:
  Among Us, Outbound, Fall Guys, CTR:NF, Stardew, MC and PvZ all share this
  machine; the emulators also share nothing else, but a kill takes the other
  title's session down.
- Detection (inspect each process's cmdline — NSP path — and environ — the
  title-specific redirect vars; generic vars are `OPENPAK_*`, legacy
  `OPENPAK_*` spellings survive only as per-title hook names):
  - citron: `pgrep -af citron`, anchored at the binary path the launcher
    used (`emulators/citron/build/.../bin/citron`)
  - Ryujinx: `pgrep -af Ryujinx`, anchored at its binary path (the fork
    builds from `emulators/ryujinx`)
- Never broad-`pkill` emulator patterns: they match the calling shell itself
  and other titles' instances. Anchor the pattern at the binary path, or
  collect PIDs first and `kill` them explicitly.
- Never run two instances against the same persona profile: citron profiles
  share one log that every start truncates; Ryujinx data dirs clobber
  `Config.json`/PTC on exit. One instance per persona dir — but the host and
  joiner personas are separate dirs, so host + joiner run side by side. The
  shared personas: citron `~/.local/share/citron/{host,joiner}`
  (historical dir name), Ryujinx `~/ryujinx-instances/{host,joiner}`.
