# D2R game-server trust patch

> **Current Switch client (2026-10-13): Harbor**, at
> `~/REPOS/Openpak/emulators/harbor`. Harbor is an independent
> OpenPak project based on Eden. Citron, Ryujinx (desktop and Android), and
> the former OpenPak Eden checkouts have been removed. References to those
> forks below record historical evidence only: do not run their commands,
> reinstall them, or select them for current work. Use Harbor's README and
> current handoff for builds and testing; old results do not establish Harbor parity.

## The D2R exception to "no game patches"

OpenPak does not patch games: it changes where the console connects and puts its CA in the
console's certificate store. This patch is one of two documented exceptions. The other is the
Crash Team Racing key swap. The owner decided this on 2026-09-30 and confirmed it on 2026-10-24.
It is a data-only patch: the IPS replaces the title's embedded PEM bundle with the OpenPak CA,
and every instruction stays as shipped. It ships to players. The exception covers exactly the
two builds below. It is no precedent for any other title.

Why no console-side fix reaches it: Diablo II: Resurrected (Switch) dials the GameJoin host on
TCP 443 with its own OpenSSL, and that OpenSSL trusts only a PEM bundle in the title's
read-only segment. Neither nn::ssl nor the console certificate store is consulted. So the game
server (`d2gs.battle.net`, and the no-SNI route behind it) is reachable only with the OpenPak CA
in that slot at load time. Sign-in, the bgs gateway and the lobby all go through nn::ssl. They
need nothing from this patch.

Two builds exist, and the patch is generated for both (`BUILDS` in `make-ips.py`):

| Build | What | Slot |
|---|---|---|
| `4DE22400C997ED2373F0643E9FE9B079DB0E8D72` | base cartridge/eShop NSP; the binary all RE addresses in docs/d2r-protocol.md refer to | 0x1b00033, 4741 bytes, 3 certs (DigiCert High Assurance EV root + intermediate) |
| `DB0507F36B84200AC05F75D76ED5026F903E6C9` | update v2228224 "1.0.34.0", **the build consoles and emulators actually run** | 0x1d6539a, 13093 bytes, 8 certs (adds DigiCert Global Root CA / G2, TLS RSA SHA256 2020 CA1, and Blizzard's own `*.diablo2.blizzard.com` leaf) |

A path exists that needs no patch, and it was not taken (owner decision 2026-10-24). The update
build also trusts DigiCert Global Root G2, which DigiCert still issues under. With a bought
DigiCert-chain certificate for a hostname OpenPak owns, set as `BATTLENET_GAMEMASTER_HOST` and
served by the in-process TLS listener (`BATTLENET_GAME_TLS_CERT/KEY`, `:21504`, behind a TLS
passthrough route), that build would accept the connection unmodified. The base build would
still need the patch.

`make-ips.py <ca.pem>` writes the IPS32 exefs patch. It carries only our public CA and
newline padding, no game bytes. Offsets include the NSO header (+0x100), the convention
Atmosphere and the emulators share. Regenerate both files when the CA changes, and ship them
together.

## Install

The file name is the build id, so both files can sit in one directory and the loader picks the
one that matches:

| Where | Path |
|---|---|
| Switch (Atmosphere) | `sd:/atmosphere/exefs_patches/openpak_d2r_trust/<build id>.ips` |
| Harbor (Eden-based) | `<user>/load/0100726014352000/openpak-d2r-trust/exefs/<build id>.ips` |

How it reaches players: the CTR key's delivery, once it is wired. The OpenPak Switch Patcher
(`platforms/switch/clients/switch-patcher`) carries console-side files in its romfs and copies
them to `/atmosphere/exefs_patches/` when OpenPak is selected. Harbor builds the CTR key in
(`src/core/file_sys/openpak_builtin_patches.cpp`). As of 2026-10-24 neither carries the D2R
patch yet. The only console with it is the owner's, copied to the SD by hand. Adding both files
to the patcher's list and to Harbor's built-in table is the open delivery step, owned by those
repos. Until then, players copy the two files by hand.

Proof it took: the title's TLS handshake to the game host completes, and the battlenet log shows
`D2GS ...: join burst sent`. Without the patch, the client aborts after our certificate and the
log shows only `D2GS ... connected` and then `closed`.
