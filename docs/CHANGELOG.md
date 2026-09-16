# Changelog

## 0.1.2 (2026-09-16)

* `listen: "off"` (or `amail init --listen off`): a client-only node. It
  opens no port, so no firewall prompt and nothing to scan; it can never be
  elected server; it still delivers directly to hosted nodes and pulls its
  own mail from the server. `amail status` marks it accordingly.

## 0.1.1 (2026-09-16)

Hardening after the first live deployment.

* Listener limits, applied before the TLS handshake: `max_connections`
  (default 64) concurrent inbound, a per-IP concurrent cap (a quarter of
  that, minimum 4) and `max_per_ip_per_min` *failed* handshakes (default
  20). Members that complete the handshake are never throttled; strangers
  are dropped at accept for the rest of the minute. Refusals are logged once
  per IP per minute.
* Storage limits: `max_mailbox_mb` (default 10240) caps received data in
  inbox + forward + failed; `min_free_mb` (default 512) refuses files when
  the disk is nearly full. Both answer `busy`, so the sender keeps the file
  in its outbox and retries.
* Mailbox folders are created group-writable (2775 / 664 on Linux) so a
  dedicated service account can own the mailbox while a login user reads and
  drops files.
* systemd unit: `MemoryMax=128M`, `TasksMax`, `LimitNOFILE`, `UMask=0002`
  and extra containment directives. `install.sh` creates a sudo-less system
  user and adds the installer to its group.

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
