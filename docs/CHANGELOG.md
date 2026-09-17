# Changelog

## 0.7.0 (2026-09-17)

* **Styled e-mail** (gateway, AMail to e-mail): `#email style:geex Subject`
  sends text plus an HTML part rendered from a template with inline styles
  (Geex theme: Poppins, purple `#AB54DB`, card layout). The body is treated
  as light Markdown (headings, lists, fenced and inline code, bold, italic,
  links, quotes, simple tables, rules), HTML-escaped first. Own templates via
  `email_styles`, `email_default_style` to style every mail, unknown styles
  refused with a receipt. Attachments still travel as before. The processor's
  Claude reply now uses `#email style:geex Claude: {subject}`, so answers
  arrive formatted instead of as raw text.
* Config loaders (`config.json`, gateway and processor JSON) skip a UTF-8
  byte-order mark, which Windows PowerShell 5.1 writes with `-Encoding utf8`.

## 0.6.1 (2026-09-17)

* Windows: `amailw-<ver>-windows-amd64.exe`, the same program built for the
  GUI subsystem, so scheduled tasks run it with no console window. The
  installers copy it as `amailw.exe` and use it for the "AMail" and "AMail
  processor" tasks; `amail.exe` stays for the command line. Handler and
  send commands are started without a window too.

## 0.6.0 (2026-09-17)

* **Processor** (`amail process --config FILE`): runs a configured command
  on notes whose first line starts with a handler tag (e.g. `#claude ...`),
  request on stdin, stdout sent back as a note to another node with a
  configurable first line (e.g. `#email Claude: {subject}` for the mail
  gateway). Origin allow-list, per-handler timeout and size caps, exactly-once
  processing, error reports as replies. `scripts/windows/claude-headless.cmd`
  (Claude Code CLI, print mode, all tools disabled) and
  `install-processor-task.ps1` for a logon-only scheduled task. See
  `docs/PROCESSOR.md`.
* Gateway (e-mail in): a subject whose text after the node id starts with a
  `#tag` (e.g. `#amail austin-pc #claude ...`) is written as the first line
  of `message.txt`, so processors on the destination can act on it; the
  folder name no longer carries the tag; subject-only mail still produces
  `message.txt`.

## 0.5.0 (2026-09-17)

* **AMail to e-mail**: the gateway now also watches the node's inbox for
  files whose first line is `#email [to:addr] Subject`, builds an RFC 822
  message (body, plus sibling files of `message.txt` as attachments) and
  hands it to a sendmail-style `send_command`. Origin allow-list, recipient
  allow-list with `to:` override, size cap, receipts back to the origin,
  retry on command failure, optional delete after send. Either gateway
  direction can run alone.

## 0.4.0 (2026-09-17)

* **E-mail gateway** (`amail gateway --config FILE [--once] [--init]`):
  watches a Maildir and turns messages whose subject contains
  `#amail <node-id>` into an outbox message folder (`message.txt`,
  `headers.txt`, decoded attachments) for the local node to deliver.
  Sender allow-list, SPF/DKIM pass required by default, optional subject
  secret, SES spam/virus verdicts honoured, size cap, state file so mail is
  never converted twice, mail itself never touched. Standard library MIME
  parsing (multipart, quoted-printable, base64, encoded words). systemd unit
  `scripts/linux/amail-gateway.service`. See `docs/GATEWAY.md`.

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
