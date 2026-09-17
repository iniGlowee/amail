# Changelog

## 0.3.0 (2026-09-17)

Module path is `github.com/iniGlowee/amail` (a dot-less module name made the
Go toolchain on CI look for `amail/internal/...` in the standard library).
`go install github.com/iniGlowee/amail/cmd/amail@v0.3.0` now works.

Security release. Wire format is additive (protocol 1) but 0.3 nodes
require the origin proof, so upgrade every node together.

* **Origin signatures and content hashes**: every file carries its SHA-256,
  an ECDSA signature by the origin over (origin, to, name, size, hash) and
  the origin's certificate. Receivers verify chain, id, revocation and
  signature, and hash the content while writing. Relays can no longer forge
  a sender or alter a file. Rejected items go to the sender's `failed/`.
* **Revocation**: `amail ca revoke <id>`; serials gossip on every request
  and reply, are persisted in `revoked_serials`, and are enforced at the
  TLS layer in both directions. `amail revoked list|add|remove`.
* **Keys at rest / in transit**: `amail ca protect` seals the CA key;
  `amail ca issue --protect` seals bundles (PBKDF2-HMAC-SHA256 600k +
  AES-256-GCM, standard library). Node key permission warning on Linux.
  Default certificate validity 3 years (was 10).
* **TLS**: X25519 / P-256 only, session tickets disabled, server-side
  revocation check, 90 s idle timeout instead of a 30 min deadline.
* **Members**: `max_peer_req_per_min` (600) request limit per node id.
* **UI**: Content-Security-Policy, `X-Frame-Options: DENY`, refuses
  non-loopback binding unless `ui_allow_remote`; revoked list shown in
  Settings.
* **Build / CI**: `dist/SHA256SUMS`; govulncheck in CI; Go 1.24 minimum.
* **Tests**: protocol fuzzer, sealing round trips, signature suite, forged
  origin / tampered content (6 cases), revocation gossip with re-issue.
* **Docs**: `docs/SECURITY.md` threat model and checklist; PROTOCOL and
  OPERATOR updated.

## 0.2.1 (2026-09-17)

* Attachments in the UI: drag in files **or folders** (structure kept),
  "Attach a folder" picker, upload progress bar, a Subject that groups the
  note and its attachments into one folder on the receiving side. Folder
  views show grouped messages with "Delete all".
* Audio and video preview with seeking (range requests); a built-in MIME
  table for media so Linux servers without `/etc/mime.types` still serve
  the right type.
* Integrity test: a 5 MB random blob delivered direct and relayed must
  match by SHA-256.

## 0.2.0 (2026-09-17)

* **Local web UI**, embedded in the binary, styled after the Geex admin
  theme (Poppins, purple primary, card layout, dark mode). `amail run` serves
  it at `ui_listen` (default `http://127.0.0.1:4445`, loopback only;
  `"off"` disables); `amail ui` serves it without a running node.
  Overview with live peer status, folder views for inbox / outbox / sent /
  held / failed with open, download, delete, reply and retry; compose with
  typed notes and drag-and-drop attachments; settings editor for every
  config key including an ordered whitelist and the blacklist; log and
  history tails. Saving settings restarts the node in place.
* Guards: Host and Origin checks plus a required `X-AMail-UI` header on the
  API, so a web page in the same browser cannot drive the node.
* `mailbox.Enqueue` / `Delete` helpers.

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
