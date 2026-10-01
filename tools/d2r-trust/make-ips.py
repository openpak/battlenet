#!/usr/bin/env python3
"""Build the IPS32 exefs patches that make Diablo II: Resurrected (Switch) trust the OpenPak CA
for its game-server TLS.

The title's own OpenSSL trusts only a PEM bundle in .rodata (DigiCert roots); the patch
overwrites that slot with our CA PEM, newline-padded, keeping the NUL after the slot. One
patch per known build, named by build id so Atmosphere and the emulators pick the right one.
IPS offsets are NSO-header inclusive (+0x100), as both expect.

Usage: make-ips.py <ca.pem> [outdir]
"""
import struct, sys, os

# build id -> (module offset of the PEM slot, slot length). Found by searching the
# decompressed .rodata for the first "-----BEGIN CERTIFICATE-----" of the bundle.
BUILDS = {
    # base cartridge/eShop build (3 certs, \r separators)
    "4DE22400C997ED2373F0643E9FE9B079DB0E8D72": (0x1b00033, 4741),
    # update v2228224 "1.0.34.0" (8 certs, \r\n separators) -- the build consoles and emulators run
    "DB0507F36B84200AC05F75D76ED5026F903E6C9": (0x1d6539a, 13093),
}

pem = open(sys.argv[1], "rb").read().replace(b"\r\n", b"\n")
assert pem.startswith(b"-----BEGIN CERTIFICATE-----") and pem.rstrip().endswith(b"-----END CERTIFICATE-----")
outdir = sys.argv[2] if len(sys.argv) > 2 else "."
for bid, (slot, size) in BUILDS.items():
    assert len(pem) < size, bid
    data = pem + b"\n" * (size - len(pem))
    out = os.path.join(outdir, bid + ".ips")
    with open(out, "wb") as f:
        f.write(b"IPS32" + struct.pack(">IH", slot + 0x100, size) + data + b"EEOF")
    print(out, size, "bytes at", hex(slot + 0x100))
