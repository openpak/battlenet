# D2R game-server trust patch

> **Current Switch client (2026-10-13): Harbor**, at
> `~/REPOS/Openpak/emulators/harbor`. Harbor is an independent
> OpenPak project based on Eden. Citron, Ryujinx (desktop and Android), and
> the former OpenPak Eden checkouts have been removed. References to those
> forks below record historical evidence only: do not run their commands,
> reinstall them, or select them for current work. Use Harbor's README and
> current handoff for builds and testing; old results do not establish Harbor parity.

Diablo II: Resurrected (Switch) dials the GameJoin host on TCP 443 with its own OpenSSL,
trusting only a PEM bundle in its read-only segment. Neither nn::ssl nor the console
certificate store is consulted, so `d2gs.battle.net` is reachable only with the OpenPak CA in
that slot at load time. Two builds exist and the patch is generated for both (`BUILDS` in
`make-ips.py`):

| Build | What | Slot |
|---|---|---|
| `4DE22400C997ED2373F0643E9FE9B079DB0E8D72` | base cartridge/eShop NSP; the binary all RE addresses in docs/d2r-protocol.md refer to | 0x1b00033, 4741 bytes, 3 certs (DigiCert High Assurance EV root + intermediate) |
| `DB0507F36B84200AC05F75D76ED5026F903E6C9` | update v2228224 "1.0.34.0", **the build consoles and emulators actually run** | 0x1d6539a, 13093 bytes, 8 certs (adds DigiCert Global Root CA / G2, TLS RSA SHA256 2020 CA1, and Blizzard's own `*.diablo2.blizzard.com` leaf) |

No-patch alternative for the update build: it trusts DigiCert Global Root G2 and Global Root CA,
which DigiCert still issues under, so a bought DigiCert certificate for a hostname OpenPak owns
would be accepted as-is (the base build would still need the patch). Not pursued yet.

`make-ips.py <ca.pem>` writes the IPS32 exefs patch. It carries only our public CA and
newline padding; no game bytes. Offsets are NSO-header inclusive (+0x100), the convention
Atmosphere and the emulators share. Regenerate when the CA changes.

Install (same file everywhere):

| Where | Path |
|---|---|
| Switch (Atmosphere) | `sd:/atmosphere/exefs_patches/openpak-d2r/4DE22400C997ED2373F0643E9FE9B079DB0E8D72.ips` |
| Ryujinx | `<config>/mods/contents/0100726014352000/openpak-d2r-trust/exefs/4DE22400C997ED2373F0643E9FE9B079DB0E8D72.ips` |
| Citron / Eden | `<user>/load/0100726014352000/openpak-d2r-trust/exefs/4DE22400C997ED2373F0643E9FE9B079DB0E8D72.ips` |

Proof it took: the title's TLS handshake to `d2gs.battle.net` completes and the gateway log
shows `D2GS ...: logon hash=... name=...`. Without it the client aborts after our certificate
and the log shows nothing.
