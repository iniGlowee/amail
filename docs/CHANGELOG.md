# Changelog

## 0.1.0 (2026-09-16)

First build.

* Single static binary for Windows and Linux (Go, standard library only).
* Mutual TLS 1.3 on port 4444 against an operator-run network CA;
  `amail ca init|issue|list`, `amail join <id>.amailkey`.
* Mailbox folders: inbox, outbox, sent, forward, failed; temp-then-rename
  writes, unique names, sub folders, Windows-safe name cleaning.
* Ordered whitelist election: first reachable entry is the server; automatic
  fail over and take-back. Blacklist by id, host, IP or CIDR.
* Direct delivery, relay through the server, store-and-forward with pull,
  hop limit 3, size limit, permanent vs transient failure handling.
* `amail status` / `peers` checks over the same TLS connection.
* Service scripts for systemd and Windows scheduled task; GitHub Actions
  build and release workflow; loopback multi-node test suite.
