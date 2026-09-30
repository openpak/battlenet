#!/usr/bin/env python3
"""Build the IPS32 exefs patch that makes Diablo II: Resurrected (Switch 1.0.34.0,
build 4DE22400C997ED2373F0643E9FE9B079DB0E8D72) trust the OpenPak CA for its game-server TLS.

The title's own OpenSSL trusts only a PEM bundle in .rodata (module offset 0x1b00033,
4741 bytes: DigiCert High Assurance EV Root CA + SHA2 High Assurance Server CA). The patch
overwrites that slot with our CA PEM, newline-padded; the original NUL after the slot is kept.
IPS offsets are NSO-header inclusive (+0x100), as Atmosphere and Ryujinx both expect.

Usage: make-ips.py <ca.pem> [out.ips]
"""
import struct, sys
SLOT, SIZE = 0x1b00033, 4741
BUILD_ID = "4DE22400C997ED2373F0643E9FE9B079DB0E8D72"
pem = open(sys.argv[1], "rb").read().replace(b"\r\n", b"\n")
assert pem.startswith(b"-----BEGIN CERTIFICATE-----") and pem.rstrip().endswith(b"-----END CERTIFICATE-----")
assert len(pem) < SIZE, "CA PEM does not fit the slot"
data = pem + b"\n" * (SIZE - len(pem))
out = sys.argv[2] if len(sys.argv) > 2 else BUILD_ID + ".ips"
with open(out, "wb") as f:
    f.write(b"IPS32" + struct.pack(">IH", SLOT + 0x100, SIZE) + data + b"EEOF")
print(out, len(data), "bytes patched at", hex(SLOT + 0x100))
