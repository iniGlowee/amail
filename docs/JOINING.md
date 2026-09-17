# Joining an AMail network (member guide)

You have been invited to a private AMail network. This page takes you from
nothing to sending your first file. You need: a Windows PC or a Linux
machine, the network operator's contact (the person who invited you), and
about ten minutes.

Nothing on your machine is exposed to the internet by default: your node
dials out to the network's server and pulls its mail. No port forwarding,
no firewall changes.

## 1. Get the program

**Option A, download a release** (no build tools needed)

1. Open the repository's Releases page and download the file for your
   machine: `amail-<version>-windows-amd64.exe`, `amail-<version>-linux-amd64`
   or `amail-<version>-linux-arm64`, plus `SHA256SUMS`.
2. Check the download matches the checksum. Windows PowerShell:

   ```powershell
   (Get-FileHash .\amail-<version>-windows-amd64.exe -Algorithm SHA256).Hash.ToLower()
   Select-String amail-<version>-windows-amd64.exe .\SHA256SUMS
   ```

   Linux: `sha256sum -c SHA256SUMS --ignore-missing`.
   The two hashes must be identical. If not, do not run it; tell the operator.
3. Rename it to `amail.exe` (Windows) or `amail` (Linux, then `chmod +x amail`)
   and put it somewhere on your PATH, or just run it from a folder you
   remember.

**Option B, build it yourself** (Go 1.24+ installed)

```bash
git clone <repository url>
cd amail
go test ./...          # optional, ~90 s
go build -o amail ./cmd/amail
```

## 2. Create your node

Pick a short id for your machine: lowercase letters, digits and hyphens,
for example `james-pc`. Then:

```bash
amail init --id james-pc --listen off
```

`--listen off` means your machine opens no port at all; it will send
directly to reachable nodes and collect its own mail from the server. Leave
it off unless the operator asks you to host.

This creates a config folder (`%APPDATA%\AMail` on Windows, `~/.amail` on
Linux) and your mailbox folder `AMail` in your home directory with
`inbox`, `outbox`, `sent`, `forward`, `failed` inside.

## 3. Ask for your key

`amail init` prints a short request. Send it to the operator. You will get
back:

* a file `james-pc.amailkey`, usually sealed with a passphrase, and
* the passphrase, sent separately (by phone, text, in person).

Install it:

```bash
# Windows PowerShell
$env:AMAIL_KEY_PASS = 'the passphrase you were given'
amail join .\james-pc.amailkey

# Linux / macOS shell
AMAIL_KEY_PASS='the passphrase you were given' amail join james-pc.amailkey
```

Then **delete the `.amailkey` file**. It contains your node's private key
and is no longer needed. Never send it to anyone, including back to the
operator.

## 4. Tell your node who to talk to

The operator gives you the list of nodes. Add them in the order given: the
first entry that answers becomes the server your node uses.

```bash
amail whitelist add <server-id> <server host or IP>
amail whitelist add <other-id>
```

Only nodes on your whitelist can send you anything, and only they can
receive from you. The operator adds *your* id to the other nodes at the
same time.

## 5. Run it

```bash
amail run
```

You should see `role: client of <server-id>` within a few seconds. Open
http://127.0.0.1:4445 in a browser for the web view (only your own machine
can reach it).

To keep it running after you close the window:

* **Windows**: from the repository folder,
  `powershell -ExecutionPolicy Bypass -File scripts\windows\install-startup.ps1 -Binary <path to amail.exe>`
  registers a task that starts it when you log in.
* **Linux**: `sudo scripts/linux/install.sh <your user>` installs a systemd
  service.

## 6. Send and receive

* Send: drop any file or folder into `AMail\outbox\<node-id>\`, or use
  Compose in the web view. Watch it move to `sent\`.
* Receive: look in `AMail\inbox\<sender-id>\`. Files appear within about
  ten seconds of being sent.
* Something wrong: `AMail\failed\` has the file and a `.error.txt` saying
  why. `amail status` shows who is reachable.

## 7. Good habits

* Your key identifies you. If your machine is lost or you suspect the key
  leaked, tell the operator: they revoke it in one command and issue a new
  one. Nothing else on the network needs changing.
* Everything you send is encrypted in transit and signed by you. The
  server node holds files for you until you collect them, and can read
  what it holds; do not send anything through AMail that the server's owner
  should not see.
* Keep `amail` up to date. `amail status` shows every node's version; when
  the operator announces a new version, all nodes upgrade together.
