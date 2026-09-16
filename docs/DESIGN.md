# AMail design

## Goal

Let machines that trust each other drop files into each other's folders,
across Windows and Linux, over one encrypted port, without network shares,
SFTP accounts or per-OS configuration. Everything a user touches is a plain
folder; everything on the wire is TLS.

## Vocabulary

| Term | Meaning |
|---|---|
| **node** | One running `amail` process with an id, a key and a mailbox. A PC, a server, a laptop. |
| **network** | Everyone holding a key issued by the same CA. Named by the operator (`--name armas`). |
| **operator** | The person who runs the CA and hands out keys. Austin Armas. |
| **whitelist** | Ordered list of nodes this node will talk to, with optional host/port. |
| **blacklist** | Node ids, hosts, IPs or CIDRs this node refuses, checked first. |
| **server** | The one node everyone currently routes through. Elected, not configured. |
| **client** | Every other node. Sends directly when it can, otherwise through the server, and pulls its held mail from the server. |
| **held** | A file sitting in a node's `forward/<to>/<from>/` waiting for `<to>`. |

## Identity and keys

* The operator runs `amail ca init --name <network>`: an ECDSA P-256
  self-signed CA, valid 20 years, stored in `<home>/ca/`.
* `amail ca issue <id>` creates a node key pair and a certificate with
  `CN=<id>`, `DNS SAN=<id>`, `O=<network>`, client+server EKU, valid 10 years,
  and writes everything into one JSON file `<id>.amailkey` (CA cert, node
  cert, node private key).
* `amail join <file>` verifies the bundle (cert chains to the CA, key matches
  cert, CN matches the declared id) and installs it into `<home>/keys/`.
* Node ids follow DNS label rules (`[a-z0-9-]`, 1-63 chars) because they are
  folder names, certificate names and TLS server names.

Both sides of every connection present their certificate. The listener
requires a client certificate signed by the CA (`RequireAndVerifyClientCert`).
The dialer verifies the server certificate chains to the CA **and** that its
CN equals the node id it meant to reach, so a whitelist entry with the wrong
host cannot be silently answered by a different node. IP addresses are used
freely because host-name verification is replaced by this CN check.

## Roles and election

Every node, at start and every `discovery_seconds`:

```
walk whitelist entries that come BEFORE my own entry (or all of them if I am
not listed), skipping ones without a host:
    first one that answers a status request  ->  it is the server
none answered and I am listening and server_eligible  ->  I am the server
otherwise walk the entries AFTER me: first one that answers  ->  server
nobody  ->  "searching" (keep trying)
```

Properties:

* Deterministic: nodes sharing the same whitelist order agree on the server.
* Fail over: if the server dies, the next reachable entry takes over within
  one discovery interval. When the original returns it takes over again.
* Nodes without a host can never be picked by others, so a laptop behind NAT
  never becomes everyone's server. It can still be *its own* server when it is
  alone, which is harmless.
* A node with `listen: "off"` is client-only: no port, not eligible, role
  `searching` until a whitelisted server answers. Everything it receives
  arrives by pull.
* Mixed reachability (A sees B but C cannot) resolves itself because a node
  that receives a file for someone else forwards it on through its own
  server, bounded by the hop limit.

## Routing a file

`route(item)`:

1. Destination must be on the whitelist and not blacklisted, else **fail**
   (moved to `failed/` with a note).
2. If the destination has a host: try to deliver directly. Success:
   **delivered**. Remote rejection (`reject` code): **fail**. Network error:
   continue.
3. If I am the server: **hold** it in my own `forward/<to>/<me>/` and move the
   outbox copy to `sent/`.
4. If I am a client and the server is not the destination and did not hand me
   this item: deliver it to the server with `to=<destination>`. Success:
   **relayed** (server holds it). Rejection: **fail**.
5. Otherwise **pending**: leave it where it is and retry next poll.

Held items (`forward/`) go through the same routine every poll, and are also
handed out when their destination sends a `pull`. Whoever succeeds first
deletes the copy; an in-flight set prevents the poll loop and a pull from
sending the same file twice.

`hops` counts relays. The origin sends 0; a node that holds a file records
`hops+1` in the sidecar and sends that value onward; a node refuses to hold a
file that has already made 3 hops.

## Folders

```
<mailbox>/
  inbox/<from>/...             arrived files (sub folders preserved)
  outbox/<to>/...              to send; picked up when unmodified for StableAge (3 s)
  sent/<to>/...                handed off (delivered, relayed or held by me)
  forward/<to>/<from>/...      held for <to>, each with a <name>.amailmeta sidecar
  failed/<to>/...              refused, with <name>.error.txt
  README.txt
```

Writes go to `.amail-tmp-*` in the target folder and are renamed into place.
Existing names get ` (2)`, ` (3)` suffixes. Empty sub folders are pruned after
a move, but `outbox/<to>/` itself is kept so users can keep dropping files.

`<home>/` (config, keys, log, history):

```
config.json           node_id, listen, mailbox, whitelist, blacklist, intervals
keys/ca.crt node.crt node.key
ca/                   only on the operator's machine
amail.log             what the daemon did
state/history.jsonl   one JSON line per received / delivered / relayed / held / failed event
```

## Limits

Applied in the accept loop, before TLS, so they cost almost nothing:

| Limit | Default | Effect when hit |
|---|---|---|
| `max_connections` | 64 | socket closed, logged once |
| per-IP concurrent (`max_connections/4`, min 4) | 16 | socket closed, logged once |
| `max_per_ip_per_min` failed handshakes | 20 | socket closed for the rest of the minute, logged once |

Only *failed* handshakes count toward the per-minute rule, so a member that
reconnects every second (one connection per file, plus pulls and status
checks) is never locked out, while a scanner that cannot present a network
certificate is.

Applied when a file is announced (`deliver`) or offered (`pull` item):

| Limit | Default | Effect when hit |
|---|---|---|
| `max_file_mb` | 1024 | `reject`: sender moves the file to `failed/` |
| `max_mailbox_mb` (inbox + forward + failed) | 10240 | `busy`: sender keeps retrying |
| `min_free_mb` on the mailbox volume | 512 | `busy`: sender keeps retrying |

Mailbox usage is rescanned at most every 30 s and bumped by each accepted
file in between, so a burst cannot slip past the cap.

## What AMail is not

* Not end-to-end encrypted. The tunnel is encrypted; nodes are trusted.
* Not a queue with delivery guarantees beyond "a file is deleted from the
  sender only after the receiver acknowledged it".
* Not a discovery protocol. Hosts come from the whitelist (and `amail peers
  --merge` to copy the server's list).
* Not multi-tenant. One network per CA; one node per home directory.

## Future ideas

* Per-file receipts back to the origin (`sent/<to>/<name>.receipt`).
* Compression for large text files.
* A `watch` command that tails `history.jsonl`.
* Optional per-recipient encryption for the few files that need it.
