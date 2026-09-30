# D2R game-server trust patch

Diablo II: Resurrected (Switch 1.0.34.0, build `4DE22400C997ED2373F0643E9FE9B079DB0E8D72`)
dials the GameJoin host on TCP 443 with its own OpenSSL, trusting only a DigiCert bundle in
its read-only segment (module offset 0x1b00033, 4741 bytes). Neither nn::ssl nor the console
certificate store is consulted, and DigiCert no longer issues under that root, so the only
way to reach `d2gs.battle.net` is to put the OpenPak CA in that slot at load time.

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
