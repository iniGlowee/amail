# AMail security

This is the honest description of what AMail protects, how, and what it
does not protect. Read it before trusting the network with anything.

## 1. Summary

| Property | How | Since |
|---|---|---|
| Nobody outside the network can read traffic | TLS 1.3 only; AEAD ciphers (AES-GCM / ChaCha20-Poly1305), forward secrecy (X25519 / P-256 ephemeral keys), no session resumption | 0.1 |
| Nobody outside the network can connect | Mutual TLS: both sides must present a certificate signed by the operator's CA. The handshake fails before any AMail code runs | 0.1 |
| A node is who its certificate says | Node id = certificate CN, checked on both sides; the dialer also checks it reached the node it meant to | 0.1 |
| A stolen key can be shut out | Revocation by certificate serial, gossiped to every node on every contact and persisted | 0.3 |
| A relay cannot forge a sender | Origin signature (ECDSA P-256 over origin, destination, name, size, SHA-256) plus the origin's certificate travel with the file; receivers verify the chain, the id, revocation and the signature | 0.3 |
| A relay cannot alter a file | SHA-256 verified while writing; a mismatch never becomes visible | 0.3 |
| Strangers cannot exhaust the node | Pre-TLS connection caps, per-IP failed-handshake ban, idle timeouts, memory cap in systemd | 0.1.1 / 0.3 |
| A member cannot flood a node | Per-peer request rate limit, per-file and total mailbox caps, disk floor | 0.1.1 / 0.3 |
| The CA key is safe at rest | Optional passphrase sealing (PBKDF2-SHA256 600k + AES-256-GCM) | 0.3 |
| Key bundles can travel by email | Optional passphrase sealing of `.amailkey` | 0.3 |
| The web UI cannot be driven by other sites | Loopback only, Host/Origin checks, required custom header, CSP, no framing | 0.2 / 0.3 |

## 2. Threat model

**Assets**: the files in every node's mailbox; the ability to put files into
a node's inbox; the network's membership (CA key, node keys).

**Attackers considered**

1. *Internet stranger*: can reach port 4444 on hosted nodes, has no key.
2. *Network eavesdropper / active MITM* between two nodes.
3. *Rogue or compromised member*: holds a valid node key.
4. *Thief of a key file* (`.amailkey`, `node.key`, or the CA key).
5. *Local user or web page* on a machine running the UI.

**Not considered**: a compromised operating system or root on a node
(anything running as that node can do what the node can do), physical
access, and traffic analysis (an observer sees that two nodes talk and
roughly how much).

## 3. The tunnel

Every connection is TLS 1.3 with mutual authentication:

* `MinVersion = TLS 1.3`: only AEAD cipher suites, always forward secret.
* `CurvePreferences = X25519, P-256`.
* `SessionTicketsDisabled`, no client session cache: every connection is a
  complete, freshly authenticated handshake. Nothing to steal from a ticket
  key, nothing to replay.
* Listener: `RequireAndVerifyClientCert` against the network CA, plus a
  revocation check on the presented serial.
* Dialer: verifies the chain against the network CA, that the CN equals the
  node id it meant to reach (so a wrong or hijacked IP cannot be answered by
  a different, even valid, node), and revocation.
* Certificates are ECDSA P-256 with client and server EKUs; the CA is a
  self-signed P-256 certificate valid 20 years; node certificates 3 years by
  default.

What an attacker of type 1 or 2 gets: a TLS handshake failure. Go's
`crypto/tls` handles everything before AMail's own code runs, and the
process is memory safe.

## 4. Message authenticity and integrity (end to end)

Store-and-forward means a file may pass through one or more relays. Since
0.3 the **origin** signs each file and the signature travels with it:

```
signed  = "amail-sig-v1" \n origin \n to \n name \n size \n sha256hex
sig     = ECDSA-P256-SHA256(origin private key, signed)
carried = sha256, sig (base64 ASN.1), origin_cert (PEM)
```

A receiver accepts a file only when:

1. it carries a SHA-256 (required for every file, relayed or not);
2. if the origin is the connecting peer: TLS already proves identity, and a
   signature, if present, is verified too;
3. if the origin is someone else: `origin_cert` chains to the CA, its CN is
   the claimed origin, its serial is not revoked, and `sig` verifies over
   the tuple above, which binds the destination, the file name and the
   size, so a relay cannot re-address, rename or swap a file;
4. the content hashed while it is written equals `sha256`; otherwise the
   temp file is deleted and the sender is told `reject`.

Relays keep the proof in the sidecar next to the held file and pass it on
unchanged. A relay that tampers produces a file that every downstream node
refuses. The test `TestForgedOriginAndTamperedContentRejected` covers the
six ways this can go wrong.

**Still true**: a relay can *read* what it holds. AMail is a trusted-member
network by design; see section 9 for the option of per-recipient encryption.

## 5. Membership: keys, revocation, rotation

* The operator's CA issues one certificate per node id. Only the operator
  can add a member.
* `amail ca revoke <id>` writes the serial(s) to `ca/revoked.txt` and into
  the operator node's `revoked_serials`. Every request and reply carries the
  sender's revocation list; every node merges what it learns from a
  whitelisted peer and saves it. Within one discovery interval (30 s by
  default) the whole reachable network refuses the revoked certificate at
  the TLS layer. `TestRevocationGossip` shows a node that was never told
  directly refusing the revoked peer.
* Revocation only removes trust, so accepting it from any member is safe:
  the worst a lying member can do is deny service, which it could do anyway
  by dropping mail.
* Rotation: `amail ca issue <id>` again gives a new serial; `amail ca revoke
  <id> --serial <old>` retires the old one. The node logs a warning 30 days
  before its certificate expires.
* Blacklist and whitelist stay as the coarse controls: by id, host, IP or
  CIDR, checked before the request is read.

## 6. Keys at rest and in transit

| Secret | Where | Protection |
|---|---|---|
| CA private key | operator's `<home>/ca/ca.key` | file mode 0600; `amail ca protect` seals it with a passphrase (PBKDF2-HMAC-SHA256, 600 000 iterations, random salt, AES-256-GCM). `amail ca issue/revoke` then need `AMAIL_CA_PASS` |
| Node private key | `<home>/keys/node.key` | mode 0600; the daemon must read it unattended, so it is not sealed. On Linux the node warns at start if it is readable by others. Run the daemon as a dedicated sudo-less user (the installer does) |
| Key bundle `.amailkey` | operator → node owner | contains the node's private key. `amail ca issue --protect` seals it with `AMAIL_KEY_PASS`; send the file and the passphrase by different channels; delete the file after `amail join` |
| Config, revocation list | `<home>/config.json` | mode 0600 |

Lost or leaked node key: revoke it, issue a new one under the same id.
Lost CA key: the network cannot grow or revoke; start a new CA and re-issue
every node (a network migration, documented in OPERATOR.md). Leaked CA key:
same, immediately.

## 7. Resource protection

Before TLS (cheap, strangers pay nothing but a closed socket):

* `max_connections` concurrent (64), a quarter of that per source IP;
* after `max_per_ip_per_min` (20) failed handshakes an IP is dropped at
  accept for the rest of the minute; successful members are never counted;
* blacklist by IP / CIDR.

After TLS (members):

* `max_peer_req_per_min` (600) requests per node id;
* `max_file_mb` (1024) per file, `max_mailbox_mb` (10240) received data,
  `min_free_mb` (512) disk floor; the last two answer `busy` so nothing is
  lost;
* hop limit 3 on relays;
* idle timeout 90 s: a connection that makes no progress is closed;
  transfers of any size are fine as long as bytes flow.

Process: systemd `MemoryMax=128M`, `TasksMax`, `LimitNOFILE`,
`NoNewPrivileges`, `ProtectSystem=full`, `PrivateTmp`, `PrivateDevices`,
`RestrictAddressFamilies`, dedicated user with no sudo.

## 8. Input handling

* Frame parser: bounded header (1 MiB), bounded body (announced length),
  version check, fuzzed (`FuzzRecv`) for panics and hangs.
* File names: normalised to forward slashes, `..` and absolute paths
  refused, Windows-reserved characters and names neutralised, length
  capped; the resolved path is checked to stay inside the mailbox.
* Node ids: DNS-label grammar, used for folder names and certificate CNs.
* Files are written under a temp name and renamed; nothing partial or
  unverified is ever visible in `inbox/`.
* UI: JSON bodies size-limited, multipart streamed with the same name
  rules, path traversal covered by tests.

## 9. Web UI

* Binds loopback only; a non-loopback `ui_listen` is refused unless
  `ui_allow_remote` is set (then put an authenticating proxy in front).
* Every API call must carry `X-AMail-UI`, which cross-site forms cannot set;
  `Origin` and `Host` must match; `Content-Security-Policy` allows only
  same-origin scripts and media; `X-Frame-Options: DENY`.
* The UI has no login by design: it is for the person sitting at the
  machine. On a server, reach it through an SSH tunnel.

## 10. What AMail does not do (yet)

* **End-to-end encryption**: relays can read held files. A future option is
  per-recipient encryption (ECDH with the recipient's certificate key, a
  fresh AES-GCM key per file) for files that must stay private from the
  server node. The origin-signature plumbing already carries certificates,
  so the groundwork exists.
* **Traffic analysis resistance**: sizes and timing are visible.
* **Protection from a compromised node**: whatever runs as the node can
  read its mailbox and send in its name until the key is revoked.
* **Automatic CA rotation**.

## 11. Operational checklist

- [ ] Security group / firewall: 4444 open only from known node addresses.
- [ ] Daemon runs as a dedicated user without sudo (Linux) or as the logged
      in user's scheduled task (Windows).
- [ ] `amail ca protect` run once; passphrase stored in a password manager.
- [ ] Key bundles issued with `--protect`; file and passphrase sent apart;
      bundle deleted after join.
- [ ] Whitelist contains only nodes you know; hosts correct.
- [ ] `amail status` shows every node on the current version.
- [ ] Watch `amail.log` for `REJECTED`, `refused`, `throttling`,
      `revocation:` lines; `history.jsonl` has `rejected` events.
- [ ] Certificate expiry dates known (`amail config`, `amail ca list`).

## 12. Reporting

Security issues in AMail itself: open a private report to the operator
(Austin Armas) rather than a public issue, with the version (`amail
version`), the log lines and, if possible, a reproduction against the
loopback test network (`docs/TESTING.md`).
