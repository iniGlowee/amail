# AMail (Armas Mail)

File mail between machines you trust. Drop a file into `outbox/<node>/` on one
computer and it appears in `inbox/<you>/` on another, carried over an
encrypted TLS tunnel on **port 4444**, without network shares, SMB, SFTP
accounts or any OS-specific plumbing. Works the same on Windows PCs and Linux
servers. Single static binary, no dependencies.

```
   your PC (Windows)                        ausa-web (Linux, server node)
   ~/AMail/outbox/ausa-web/report.pdf  --->  ~/AMail/inbox/austin-pc/report.pdf
   ~/AMail/inbox/ausa-web/backup.tgz   <---  ~/AMail/outbox/austin-pc/backup.tgz
```

* **Whitelist / blacklist**: a node only talks to nodes on its whitelist and
  never to anything on its blacklist.
* **Server or client, automatically**: when a node starts it looks for a
  reachable server on the whitelist. If it finds none, it *becomes* the
  server. Nodes behind NAT are clients and pull their mail from the server.
* **Store and forward**: a file for a node that cannot be reached right now is
  held by the server (in its `forward/` folder) until that node connects.
* **Status and type checks**: every node answers "who are you, what role are
  you playing" over the same TLS connection (`amail status`).
* **Keys from the operator**: to join, you ask the network operator
  (**Austin Armas**) for a key file. It carries the network's public
  certificate and your node's own certificate; every node on the network can
  then verify and decrypt traffic from every other node. This is a *tunnel*
  (encrypted in transit), not end-to-end secrecy. Every node on the network
  is trusted.

## Quick start (a node)

1. Download `amail.exe` (Windows) or `amail` (Linux) from the releases page
   or build it (see below). Put it somewhere on your PATH.
2. Initialise the node. The id defaults to your host name; pick something
   short and lowercase.

   ```bash
   amail init --id austin-pc
   ```

   This creates the config in `%APPDATA%\AMail` (Windows) or `~/.amail`
   (Linux) and the mailbox folders in `~/AMail`.
3. It prints a key request. Send it to Austin Armas. You get back
   `austin-pc.amailkey`. Install it:

   ```bash
   amail join austin-pc.amailkey
   ```

4. Add the nodes you want to talk to (the operator tells you the hosts).
   Order matters: the first *reachable* entry becomes the server.

   ```bash
   amail whitelist add ausa-web 203.0.113.10
   amail whitelist add dd-prod  203.0.113.20
   amail whitelist add laptop           # no host: it can only reach out
   ```

5. Run it:

   ```bash
   amail run
   ```

   Keep it running as a service: `scripts/windows/install-startup.ps1` or
   `scripts/linux/install.sh`.
6. Send something:

   ```bash
   amail send ausa-web C:\path\to\report.pdf
   ```

   or just copy the file into `~/AMail/outbox/ausa-web/`. Watch it move to
   `sent/ausa-web/`, and check `amail status`.

## The web UI

`amail run` also serves a small web interface on `http://127.0.0.1:4445`
(loopback only). Open it in a browser, or run `amail ui` to get the same
page without a running node. From there you can:

* see this node's role, the server, key expiry, disk and mailbox usage, and a
  live status table of every whitelisted node;
* read inbox / outbox / sent / held / failed, preview text, images and PDFs,
  download, delete, reply, or retry a failed file;
* compose: type a note (saved as a `.txt` in the recipient's inbox) and drag
  in attachments;
* edit every setting, reorder the whitelist, manage the blacklist. Saving
  restarts the node in place, the UI stays up;
* tail the log and the delivery history.

It is styled after the Geex admin theme and has a dark mode. `ui_listen` in
`config.json` moves it or turns it off (`"off"`). It never leaves loopback
unless you point it at another address on purpose; the API also refuses
requests without the `X-AMail-UI` header, so other web pages in your browser
cannot talk to it. On a server, reach it with an SSH tunnel:
`ssh -L 4445:127.0.0.1:4445 user@host` then open `http://127.0.0.1:4445`.

## Attachments: documents, images, music, video, anything

AMail moves bytes, not "mail": a PDF, a PNG, an MP3, a 900 MB video or a
ZIP all travel the same way and arrive as the same file, byte for byte (the
test suite checks a random 5 MB blob with SHA-256, direct and relayed).

* **From the folders**: copy anything into `outbox/<node>/`. A whole folder
  tree is fine; it arrives with the same structure under `inbox/<you>/`.
* **From the UI**: Compose lets you type a note and drag in files or entire
  folders, or click "Attach a folder". Give it a subject and the note plus
  its attachments arrive together in one folder named after the subject,
  for example `inbox/austin-pc/Site photos-20260917-103000/`. The inbox
  shows such a message as one group with "Delete all".
* **Preview**: text, images and PDFs open inline; audio and video play in
  the browser (with seeking); everything else downloads.
* **Limits**: `max_file_mb` on the receiving node (1 GB default). Bigger
  files get a clear "refuses files over N MB" in `failed/`, so raise the
  limit on both sides for very large transfers.

## The mailbox folders

Everything is a plain folder. Use Explorer, Finder, `cp`, a cron job, a PHP
script, whatever you like.

| Folder | Meaning |
|---|---|
| `outbox/<node-id>/` | Put a file (or a whole folder tree) here to send it to that node. |
| `sent/<node-id>/` | Where outbox files go once they have been handed off. |
| `inbox/<node-id>/` | Files that arrived, one folder per sender. Sub folders are preserved. |
| `forward/<to>/<from>/` | Files this node is holding for another node (machine managed). |
| `failed/<node-id>/` | Files that could not be delivered, with a `.error.txt` beside each. |

Rules of thumb:

* A file is picked up once it has been unmodified for 3 seconds, so
  half-copied files are never sent. Files starting with `.` or `~$`, or ending
  in `.tmp`/`.part`/`.crdownload`, are ignored.
* Names that already exist in the inbox get a ` (2)`, ` (3)` suffix. Nothing is
  ever overwritten.
* Files are written under a temporary name and renamed into place, so what
  you see in `inbox/` is always complete.
* File names are cleaned for the receiving OS (`< > : " | ? *` become `_`),
  and `..` is refused.
* Default maximum file size is 1 GB (`max_file_mb`). Received data (inbox +
  forward + failed) is capped at 10 GB (`max_mailbox_mb`) and nothing is
  accepted when the disk has less than 512 MB free (`min_free_mb`). A refused
  file waits in the sender's outbox and is retried.

## Commands

```
amail init [--id NAME] [--mailbox DIR] [--listen :4444]   create config + mailbox
amail request-key                                        print the key request again
amail join <file.amailkey>                               install the key from the operator
amail run                                                run the node (foreground) + web UI on ui_listen
amail ui [--addr 127.0.0.1:4445] [--no-open]             web UI without a running node
amail status [id ...]                                    who is up, who is the server
amail send <node-id> <file> [file ...]                   queue files in outbox/
amail peers [--merge]                                    the server's whitelist; --merge adds new ones to yours
amail whitelist list | add <id> [host[:port]] | remove <id>
amail blacklist list | add <id|host|cidr> [--reason ..] | remove <x>
amail config                                             where everything lives
amail version

amail ca init --name NETWORK                             (operator) create the network CA
amail ca issue <node-id> [--out FILE] [--days N]         (operator) issue a node key
amail ca list                                            (operator) issued keys
```

`--home DIR` before the command, or `AMAIL_HOME`, points at a different node
home (useful for running two nodes on one machine). `AMAIL_DEBUG=1` makes
delivery attempts chatty.

**Client-only nodes.** A PC that only wants to send and receive, and keep
every port closed, uses `amail init --listen off` (or sets `"listen": "off"`
in `config.json`). It never opens 4444, never triggers a firewall prompt,
can never become the server, and still works fully: it delivers straight to
hosted nodes and pulls whatever is held for it from the server.

## How it decides who is the server

The whitelist is an ordered list. Every node, every `discovery_seconds`
(default 30), walks the entries that come *before itself* and asks each one
for its status. The first one that answers is the server. If none answers and
this node is listening and `server_eligible`, this node is the server. Nodes
with no `host` in the whitelist can never be chosen by others.

Because every node applies the same rule to the same list, they agree. If the
server goes down, the next reachable node takes over within one discovery
interval; when the original comes back it takes over again. See
[docs/DESIGN.md](docs/DESIGN.md).

## How a file travels

1. Try the destination directly (if it has a host on the whitelist).
2. Otherwise hand it to the server, which holds it in `forward/<to>/`.
3. The destination pulls its held mail from the server every `poll_seconds`
   (default 10). The server also pushes directly when it can.
4. If this node *is* the server and cannot reach the destination, it holds the
   file itself. The outbox copy moves to `sent/` at that point.

A file that is refused (unknown destination, too big, blacklisted) moves to
`failed/` with the reason. A file with no route yet stays in `outbox/` and is
retried forever.

## Security model, honestly

* Every connection is TLS 1.3 with **mutual** authentication against the
  network CA. Nothing without a key issued by the operator can even complete
  a handshake. Every byte is encrypted in transit.
* Inside the network, every node is trusted. A relaying node holds files in
  the clear in its `forward/` folder. Do not use AMail for secrets you would
  not hand to every machine on the network.
* Whitelist = who this node will talk to. Blacklist = who it refuses (by node
  id, host, IP or CIDR), checked before anything else. An empty whitelist
  means "anyone holding a network key".
* The key bundle contains the node's private key. Treat `.amailkey` files
  like passwords: send them over a channel you trust and delete them after
  `amail join`.
* Flood protection runs before the TLS handshake: at most `max_connections`
  (64) open connections, a quarter of that per source IP, and
  `max_per_ip_per_min` (20) *failed* handshakes per IP per minute. Members
  are never throttled; strangers hitting the port cost the node a closed
  socket and one log line per minute.
* Inside the network, sender attribution is trust-based: relaying requires
  the `origin` field, so a member could claim another member's name. The
  history log always records the node that actually connected.
* On Linux run the daemon as a dedicated account with no sudo (the installer
  creates one); the unit file adds `MemoryMax=128M` and kernel containment.

## For the operator (Austin Armas)

```bash
amail ca init --name armas             # once; creates <home>/ca (keep ca.key private)
amail ca issue ausa-web                # -> ausa-web.amailkey, send to that machine
amail ca issue austin-pc
amail ca list
```

Full walkthrough in [docs/OPERATOR.md](docs/OPERATOR.md). Contact for key
requests: fill in your preferred address here before publishing.

## Building

Requires Go 1.22+. No other dependencies.

```bash
go build ./cmd/amail                                         # this platform
scripts/build.sh          # or scripts\build.ps1            # dist/ for windows/amd64, linux/amd64, linux/arm64
go test ./...                                                # includes a 3-node loopback network test
```

The GitHub Actions workflow in `.github/workflows/build.yml` runs the tests on
every push and attaches binaries to tagged releases (`v1.0.0`).

## Running as a service

* **Windows**: `powershell -ExecutionPolicy Bypass -File scripts\windows\install-startup.ps1`
  registers a scheduled task "AMail" that starts `amail run` at logon.
  `uninstall-startup.ps1` removes it.
* **Linux (systemd)**: `sudo scripts/linux/install.sh amail` creates a
  sudo-less system user `amail` (if missing), adds you to its group, installs
  the binary to `/usr/local/bin` and a unit `amail@amail.service`. The
  mailbox is `/home/amail/AMail`, group-writable, so your login account can
  drop and read files (`ln -s /home/amail/AMail ~/AMail` for convenience).
  Initialise the node as that user: `sudo -u amail -H amail init --id <id>`.
  Remember to open TCP 4444 inbound (firewall / security group) on server
  nodes, ideally only from the other nodes' addresses.

## Project layout

```
cmd/amail/           the CLI
internal/config/     config.json, whitelist, blacklist
internal/keys/       network CA, key bundles, TLS configs
internal/proto/      wire framing (docs/PROTOCOL.md)
internal/mailbox/    the folders on disk
internal/node/       the daemon: listener, election, delivery, relay, pull
scripts/             build + service install scripts
docs/                DESIGN, PROTOCOL, OPERATOR, TESTING, CHANGELOG
```

## Documentation

* [docs/DESIGN.md](docs/DESIGN.md): roles, election, routing, hops, folders.
* [docs/PROTOCOL.md](docs/PROTOCOL.md): the wire protocol on port 4444.
* [docs/OPERATOR.md](docs/OPERATOR.md): running the network and issuing keys.
* [docs/TESTING.md](docs/TESTING.md): automated tests and the live test plan.
* [docs/CHANGELOG.md](docs/CHANGELOG.md).

## License

MIT. See [LICENSE](LICENSE).
