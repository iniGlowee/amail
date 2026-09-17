# Joining an AMail network (member guide)

You have been invited to a private AMail network. This page takes you from
nothing to a node that is online, sending and receiving, and (optionally)
starting by itself. You need a Windows PC or a Linux machine, the network
operator's contact (the person who invited you), and about fifteen minutes.

Nothing on your machine is exposed to the internet by default: your node
dials out to the network's server and pulls its own mail. No port
forwarding, no router changes, no firewall rules.

## Prerequisites

| | Windows | Linux |
|---|---|---|
| OS | Windows 10 or 11, 64-bit | any x86-64 or arm64 distribution with systemd (Ubuntu, Debian, Amazon Linux, Fedora…) |
| Network | outbound TCP to the server on port 4444 (home connections allow this) | same |
| Disk | a few MB for the program plus whatever you send and receive | same |
| Tools | PowerShell (built in) | a shell, `sha256sum`, `sudo` for the service step |
| To build from source (optional) | Go 1.24+ | Go 1.24+ |

## 1. Get the program

**Option A, download a release** (recommended, no build tools)

1. On the Releases page, https://github.com/iniGlowee/amail/releases, download the file for your machine
   and the checksum list:
   `amail-<version>-windows-amd64.exe`, or `amail-<version>-linux-amd64`,
   or `amail-<version>-linux-arm64`, plus `SHA256SUMS`.
2. Verify the download. The two hashes must match exactly; if they do not,
   do not run the file and tell the operator.

   ```powershell
   # Windows PowerShell, in your Downloads folder
   (Get-FileHash .\amail-<version>-windows-amd64.exe -Algorithm SHA256).Hash.ToLower()
   Select-String "windows-amd64" .\SHA256SUMS
   ```

   ```bash
   # Linux
   sha256sum -c SHA256SUMS --ignore-missing
   ```

3. Put it somewhere sensible and give it a plain name:

   ```powershell
   # Windows: install for your user and add it to PATH (new terminals see it)
   New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\Programs\AMail" | Out-Null
   Copy-Item .\amail-<version>-windows-amd64.exe "$env:LOCALAPPDATA\Programs\AMail\amail.exe"
   [Environment]::SetEnvironmentVariable('Path', ([Environment]::GetEnvironmentVariable('Path','User').TrimEnd(';') + ";$env:LOCALAPPDATA\Programs\AMail"), 'User')
   ```

   ```bash
   # Linux
   chmod +x amail-<version>-linux-amd64 && sudo install -m 755 amail-<version>-linux-amd64 /usr/local/bin/amail
   ```

   Open a **new** terminal afterwards and check: `amail version`.

   **Windows may warn you.** The program is not code-signed, so SmartScreen
   can show "Windows protected your PC" the first time, or PowerShell may
   say the file is blocked. The checksum you verified in step 2 is the real
   proof it is genuine. To clear the warning:

   ```powershell
   Unblock-File "$env:LOCALAPPDATA\Programs\AMail\amail.exe"
   ```

   or click *More info*, then *Run anyway*, once.

**Option B, build it yourself**

```bash
git clone https://github.com/iniGlowee/amail.git && cd amail
go build -o amail ./cmd/amail        # add .exe on Windows
go test ./...                        # optional, about 90 s
```

## 2. Create your node

Pick a short id for this machine: lowercase letters, digits and hyphens,
for example `james-pc` or `james-laptop`. Then:

```bash
amail init --id james-pc --listen off
```

`--listen off` means your machine opens no port at all: it sends directly to
nodes it can reach and collects its own mail from the server. Leave it that
way unless the operator asks you to host for others.

This creates your **node home** (`%APPDATA%\AMail` on Windows, `~/.amail` on
Linux; it holds `config.json`, your key and the log) and your **mailbox**
folder `AMail` in your home directory with `inbox`, `outbox`, `sent`,
`forward` and `failed` inside. `amail config` shows both paths any time.

## 3. Ask for your key

`amail init` prints a short request. Send it to the operator. You get back:

* a file `james-pc.amailkey`, sealed with a passphrase, and
* the passphrase, sent a different way (phone call, text message, in person).

Install it:

```powershell
# Windows PowerShell
$env:AMAIL_KEY_PASS = 'the passphrase you were given'
amail join .\james-pc.amailkey
Remove-Item .\james-pc.amailkey
```

```bash
# Linux
AMAIL_KEY_PASS='the passphrase you were given' amail join james-pc.amailkey && rm james-pc.amailkey
```

The file held your node's private key; once installed it is not needed and
should not exist anywhere else. Never send it back or forward it.

## 4. Tell your node who to talk to

The operator gives you the list of nodes. Add them **in the order given**;
the first entry that answers becomes the server your node uses.

```bash
amail whitelist add ausa-web <host or IP the operator gives you>
amail whitelist add austin-pc
amail whitelist list
```

Only whitelisted nodes can send you anything or receive from you. The
operator adds *your* id on the other nodes at the same time.

## 5. Get the node online

Run it in a terminal first, so you can see what it does:

```bash
amail run
```

Within a few seconds you should see:

```
listener off: client-only node ...
role: client of ausa-web
```

That is "online". If you see `role: searching`, no whitelisted server
answered: check the host you typed in step 4, and that you have internet.
`Ctrl+C` stops it.

Open http://127.0.0.1:4445 in a browser while it runs for the web view
(Overview, Inbox, Compose, Settings). Only your own machine can reach it.

From a second terminal, `amail status` shows every node it can see with
role, version and uptime.

## 6. Send your first file

* Drop any file (or a whole folder) into `AMail\outbox\ausa-web\` (Windows)
  or `~/AMail/outbox/ausa-web/` (Linux). Within about ten seconds it moves to
  `sent\ausa-web\` and appears in the recipient's inbox.
* Or open the web view, *Compose*, choose the recipient, type a note, drag
  files in, *Send*.
* Incoming files appear in `AMail\inbox\<sender-id>\`.
* If something cannot be delivered it goes to `AMail\failed\` with a
  `.error.txt` beside it saying why.

## 7. Optional: start automatically

Skip this if you are happy to run `amail run` by hand. Otherwise:

**Windows: a scheduled task that runs while you are logged in**

From the folder where you cloned or unpacked the repository (it needs the
script under `scripts\windows`):

```powershell
powershell -ExecutionPolicy Bypass -File scripts\windows\install-startup.ps1 -Binary "$env:LOCALAPPDATA\Programs\AMail\amail.exe"
```

Download `amailw-<version>-windows-amd64.exe` from the release too and keep
it next to the `amail-…` file you pass as `-Binary`: it is the same program
without a console window, and the installer uses it for the task so nothing
pops up at logon.

What this registers, deliberately:

* task name **AMail**, starts when *you* log on (not at boot, not for other
  users), runs invisibly in your session, ends when you log off;
* never wakes the computer and does not require it to be plugged in, so it
  suits a PC that is not always on;
* if the node crashes it is restarted a minute later;
* passes your node home explicitly, so it does not depend on environment
  variables.

Check it: `Get-ScheduledTask AMail` shows *Running*; `amail status` shows
your node. Re-run the same command with a new `-Binary` to upgrade. Remove
it with `scripts\windows\uninstall-startup.ps1`.

**Linux: a systemd service**

```bash
sudo scripts/linux/install.sh $USER
```

installs the unit `amail@<user>.service` (restart on failure, resource
limits, starts at boot). `journalctl -u amail@$USER -f` follows the log.
If you would rather have a dedicated account, `sudo scripts/linux/install.sh amail`
creates one and adds you to its group; see `docs/OPERATOR.md` section 6.

## 8. Everyday use and good habits

* `amail status` when in doubt; `amail config` for paths; the log lives in
  your node home (`amail.log`).
* Your key identifies you. If the machine is lost or you think the key
  leaked, tell the operator: they revoke it in one command and issue a new
  one. Nothing else on the network changes.
* Everything you send is encrypted in transit and signed by you. The server
  node keeps files for you until you collect them and can read what it
  holds. Do not send anything through AMail that the server's owner should
  not see.
* Keep `amail` current: when the operator announces a version, everyone
  upgrades together (0.3.0 nodes do not accept files from older ones).
  Windows: re-run the install script with the new file. Linux: replace
  `/usr/local/bin/amail` and `sudo systemctl restart amail@$USER`.
