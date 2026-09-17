# AMail: history, decisions and current state

A single place that says what AMail is, how it got here, why it is built
the way it is, and what is left. Dates are 2026. Private deployment details
(addresses, paths, key locations) live in the operator's runbook, not here.

## 1. What AMail is, in one paragraph

Machines that trust each other drop files into each other's folders. Each
machine runs one `amail` node with an id, a certificate from the network
operator, and a mailbox (`inbox/ outbox/ sent/ forward/ failed/`). Nodes
talk mutual-TLS on port 4444. The first reachable node on an ordered
whitelist acts as the server; everyone else is a client that sends
directly when it can and otherwise hands files to the server, which holds
them until the recipient pulls. Every file carries a hash and the origin's
signature, so relays can neither forge nor alter it. A loopback web UI
reads, sends, deletes and configures.

## 2. Timeline

| Version | Date | What |
|---|---|---|
| 0.1.0 | 09-16 | First build: CA + key bundles, mailbox folders, election, relay + pull, whitelist/blacklist, CLI, systemd/Task scripts, CI, 3-node loopback test suite. Live: first server node installed on the Ausa web server, PC behind NAT elected it, files both ways. |
| 0.1.1 | 09-16 | Hardening after the first deployment: dedicated sudo-less service user, group-writable mailbox, `MemoryMax` and systemd containment, pre-TLS connection limiter (concurrent + per-IP failed-handshake ban), mailbox cap and disk floor. |
| 0.1.2 | 09-16 | `listen: "off"`: client-only nodes open no port. The PC runs this way. |
| 0.2.0 | 09-17 | Embedded web UI (Geex palette): overview with live peer table, folder views, compose, settings with in-place restart, log. Guards: loopback, Host/Origin, required header. |
| 0.2.1 | 09-17 | Attachments: subject-grouped messages, folder drag-and-drop with structure kept, upload progress, audio/video preview, MIME table; 5 MB integrity test; live PNG + WAV + 7 MB round trip with matching hashes. |
| 0.3.0 | 09-17 | Security release: origin signatures + SHA-256 on every file, revocation gossip, sealed CA key and bundles, TLS tightened, per-peer rate limit, UI CSP, checksums, govulncheck, fuzzer, `SECURITY.md`. |

Every version was deployed to the live server through the operator's hub
with a recorded command and a written deploy document, and verified with a
two-way delivery.

## 3. Decisions and why

**Go, standard library only.** One static binary per OS, cross-compiled
from Windows, no runtime, TLS and crypto in the standard library, memory
safe. No dependency to audit or update. (Toolchain was installed for this
project; the MSI hung on an elevation prompt so a user-local zip is used.)

**Folders, not a database.** The user's requirement was "write to and read
from the drive like any file". Everything is a plain file the OS manages;
sidecars (`.amailmeta`, `.error.txt`) carry the little metadata needed.
Writes are temp-then-rename so nothing partial is ever visible.

**Operator CA, mutual TLS, node id = certificate CN.** "Ask Austin for a
key" became a certificate signed by a CA he runs. It gives authentication
both ways, encryption in transit, and a place to hang revocation. Host
name verification is replaced by a CN check because nodes are addressed by
IP as often as by name.

**Ordered whitelist election.** No leader protocol: every node walks the
same list and picks the first reachable entry. Deterministic, converges
within a discovery interval, fails over and takes back automatically. Nodes
without a host are never chosen; `listen: off` nodes are never eligible.

**Store-and-forward with pull.** NAT nodes cannot be reached, so the
server holds files for them and they pull. Any node will relay for a
whitelisted destination (bounded by 3 hops), which also heals mixed
reachability.

**Two-phase deliver.** The header is announced first so a receiver can
refuse (size, unknown destination, hops, blacklist, bad proof) before a
single byte of body is sent.

**Rate-limit failed handshakes, not handshakes.** The first limiter locked
out the PC because a member retries every few seconds and each file is a
connection. Members succeed the handshake and are never throttled;
strangers fail it and are banned per IP per minute. A separate per-peer
request limit covers compromised members.

**Origin signatures over (origin, to, name, size, hash).** Relaying
requires trusting the `origin` field, which a member could forge. Signing
the tuple with the origin's key and carrying its certificate lets any
receiver verify through any number of relays, and binds destination, name
and size so a relay cannot re-address or rename either.

**Revocation by gossip.** No CRL server to run. Every request and reply
carries the sender's revoked serials; nodes merge and persist what
whitelisted peers tell them. Revocation only removes trust, so accepting
it from any member is safe.

**Sealing with PBKDF2 + AES-GCM.** The standard library (Go 1.24+) has
`crypto/pbkdf2`; no third-party KDF needed. The CA key can be sealed
because issuing is interactive; node keys are not, because the daemon
must start unattended, so they rely on file mode and a dedicated user.

**UI on loopback, no login.** It is for the person at the machine. On a
server, an SSH tunnel is the login. A custom request header plus Host and
Origin checks stop other web pages in the same browser from driving it.

**Grouping by subject folder.** "Attachments" became: a note and its files
arrive together in one folder named after the subject, using the existing
sub-folder support. No new wire concept was needed.

## 4. Current state (2026-09-17)

* Two nodes live on network `armas`: the Ausa web server (Linux, server
  role, dedicated user, systemd) and the operator's PC (Windows, client-only,
  UI on loopback). Both on 0.3.0, signed delivery verified both ways.
* Repository: local git, `main`, all work committed. Not yet pushed to
  GitHub; the workflow will test, build, checksum and release on a `v*` tag.
* Tests: unit + 3-node loopback suites, all green; fuzzer and govulncheck
  clean.
* Docs: README, DESIGN, PROTOCOL, SECURITY, OPERATOR, TESTING, CHANGELOG,
  this file, CLAUDE.md for future sessions.

## 5. Open items

| Item | Owner | Why it matters |
|---|---|---|
| Firewall / security group source narrowed to the PC's address | operator | strangers should not even cost a handshake |
| `amail ca protect` with a passphrase | operator | CA key is in the clear on the operator's PC |
| Push to GitHub, tag `v0.3.0` | operator | off-site copy, CI, downloadable checksummed binaries |
| PC node as a scheduled task | operator | today it runs as a session process; `scripts/windows/install-startup.ps1` makes it persistent |
| Re-issue the two original certificates (issued with the old 10-year default) | optional | shorter life is the new norm; revoke the old serials after |
| README contact line for key requests | operator | intentionally blank until Austin chooses what to publish |

## 6. Roadmap ideas (not started)

* Per-recipient encryption (ECDH with the recipient's certificate key,
  fresh AES-GCM key per file) so the server node cannot read what it holds.
* Delivery receipts back to the origin.
* A `watch` command / UI notifications for new mail.
* Optional compression for large text.
* Hub integration: an AMail status probe in the Server Administrator monitor.
