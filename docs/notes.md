# Notes: battlenet

## Role

OpenPak's Battle.net tier: the web front every redirected Blizzard title hits
first, and the native gateway that answers its WebSocket RPC logon. One Go
binary, two listeners, driven by the Diablo II Resurrected logon research in
private notes.

## Ground rules

- Clean room. Public protocol documentation, our own captures, and
  compatibly licensed code are the only inputs; see
  [clean-room policy](https://docs.openpak.org/contributors/clean-room/).
- AGPL-3.0-only, English only, Go 1.26, no secrets in the repository.

This project is not affiliated with or endorsed by Nintendo or Blizzard
Entertainment. It contains no proprietary game code, assets, SDK material,
binaries, certificates, keys, firmware, extracted files, credentials, or raw
packet captures. Battle.net and Diablo II: Resurrected are trademarks of
Blizzard Entertainment, Inc.
