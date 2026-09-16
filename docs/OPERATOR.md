# Running an AMail network (operator guide)

The operator is the person who owns the network's certificate authority and
hands out keys. For the Armas network that is Austin Armas.

## 1. Create the network once

On the machine you will keep the CA on (your PC is fine; back up the folder):

```bash
amail ca init --name armas
```

This writes `<home>/ca/ca.key` (private, never leaves this machine) and
`<home>/ca/ca.crt` (public, embedded in every key bundle). `<home>` is
`%APPDATA%\AMail` on Windows or `~/.amail` on Linux, or `--home DIR`.

## 2. Issue a key when someone asks

A node owner runs `amail init` and sends you the printed request: a node id, a
machine name, and whether the machine is reachable on port 4444 (and at what
host or IP).

Check the id is sensible (lowercase, short, unique on the network), then:

```bash
amail ca issue ausa-web
# -> Issued key for "ausa-web" on network "armas" -> ausa-web.amailkey
```

Send `ausa-web.amailkey` to the node owner over a channel you trust (it
contains that node's private key). They run `amail join ausa-web.amailkey`.

Record the node in your own whitelist and tell the other node owners to add
it too:

```bash
amail whitelist add ausa-web 203.0.113.10
```

`amail ca list` shows everything issued.

## 3. Keep the whitelist consistent

The whitelist order decides who is server: the first reachable entry wins.
Put the always-on server (ausa-web) first on everyone's list, other hosted
servers next, PCs and laptops last (usually without a host).

Nodes can copy the server's list: `amail peers --merge` on any node adds the
ids and hosts the server knows about.

## 4. Reissue, revoke, remove

* **Lost or leaked key**: there is no revocation list in v1. Blacklist the id
  on every node (`amail blacklist add <id> --reason leaked`), then issue a
  new key under a *new* id. The old certificate will still handshake but is
  refused at the blacklist check on every node that has the entry.
* **Expiry**: node keys last 10 years by default (`--days`). The node logs a
  warning 30 days before expiry. Issue a new bundle with the same id; `amail
  join` overwrites the old one.
* **Retire a node**: remove it from every whitelist. Nothing else to do.

## 5. Opening the port

Only nodes that should be reachable need TCP 4444 inbound:

* AWS security group: inbound rule, TCP 4444, source = the other nodes' IPs
  (or 0.0.0.0/0 if they roam; mutual TLS still blocks everyone without a key).
* Linux firewalld: `sudo firewall-cmd --permanent --add-port=4444/tcp && sudo firewall-cmd --reload`
* Windows Defender Firewall (only if a PC should be reachable):
  `New-NetFirewallRule -DisplayName AMail -Direction Inbound -Protocol TCP -LocalPort 4444 -Action Allow`

Client-only nodes need nothing opened: they dial out and pull.

## 6. Where things are on a node

| Path | What |
|---|---|
| `<home>/config.json` | node id, listen address, mailbox path, whitelist, blacklist |
| `<home>/keys/` | `ca.crt`, `node.crt`, `node.key` |
| `<home>/amail.log` | daemon log (append only; rotate it yourself) |
| `<home>/state/history.jsonl` | one line per event, for scripts and dashboards |
| `~/AMail/` | the mailbox: inbox, outbox, sent, forward, failed |

## 7. Health checks from anywhere

`amail status` from any node with a key asks every whitelisted host for its
role, version, uptime and folder counts. `amail status --json` is script
friendly. The Server Administrator hub can wrap it as a probe.
