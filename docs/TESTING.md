# Testing AMail

## Automated

```bash
go test ./...
```

| Package | What is covered |
|---|---|
| `internal/proto` | frame round trip, unread body draining, error frames |
| `internal/mailbox` | name cleaning (Windows-safe, no `..`), unique naming, temp-then-rename, hold/meta sidecars, outbox scan ignoring temp files, moves to sent/failed, folder pruning |
| `internal/node` | a **3-node loopback network** with its own throwaway CA: election (node0 server, others clients), direct delivery both ways, sub folders, NAT node sending out, relay through the server + pull, server holding for a NAT node, duplicate names, unknown destination to `failed/`, status and peers over TLS, **fail over** when the server stops and take-back when it returns, **blacklist** rejection, **foreign CA** rejection, **too large** rejection |

The node test takes about 15 seconds because it uses real timers (1 s poll /
discovery). Set `AMAIL_DEBUG=1` to see every delivery attempt.

## Manual smoke test on one machine

Two nodes on one PC, different homes and ports:

```bash
amail --home ./home-op  ca init --name lab
amail --home ./home-op  ca issue alpha --out alpha.amailkey
amail --home ./home-op  ca issue beta  --out beta.amailkey

amail --home ./home-a init --id alpha --listen 127.0.0.1:4444 --mailbox ./mb-a
amail --home ./home-a join alpha.amailkey
amail --home ./home-b init --id beta  --listen 127.0.0.1:4445 --mailbox ./mb-b
amail --home ./home-b join beta.amailkey

for h in home-a home-b; do
  amail --home ./$h whitelist add alpha 127.0.0.1:4444
  amail --home ./$h whitelist add beta  127.0.0.1:4445
done

amail --home ./home-a run &     # becomes server
amail --home ./home-b run &     # client of alpha
amail --home ./home-b send alpha README.md
amail --home ./home-b status
ls mb-a/inbox/beta/
```

## Live test plan: Ausa server

Target: `ausa-web` (Amazon Linux 2023, see the Server Administrator hub,
`data/servers.json`). Steps, each recorded through the hub's `sa.php run`:

1. Build: `scripts/build.ps1` → `dist/amail-<ver>-linux-amd64`.
2. Copy: `php bin/sa.php scp ausa-web dist/amail-<ver>-linux-amd64 /tmp/amail`.
3. Install: `sudo install -m 755 /tmp/amail /usr/local/bin/amail`.
4. Init on the server as the `ec2-user` (or a dedicated `amail` user):
   `amail init --id ausa-web --mailbox /home/ec2-user/AMail`.
5. On Austin's PC: `amail ca issue ausa-web`, copy the bundle up with
   `sa.php scp`, `amail join ausa-web.amailkey` on the server, delete the
   bundle file.
6. Whitelist on both sides: `ausa-web <public ip>` first, `austin-pc` (no
   host) second.
7. Open TCP 4444 inbound in the security group, source = Austin's public IP
   (Austin does this in the AWS console).
8. Start: `sudo scripts/linux/install.sh ec2-user` then
   `systemctl status amail@ec2-user`.
9. From the PC: `amail run`, then `amail status` should show ausa-web as
   `server` and the PC as `client`.
10. `amail send ausa-web somefile.pdf` → appears in
    `/home/ec2-user/AMail/inbox/austin-pc/`.
11. On the server: `cp /etc/hostname ~/AMail/outbox/austin-pc/` → appears in
    `%USERPROFILE%\AMail\inbox\ausa-web\` on the PC within ~10 s (pulled).
12. File the write-up: `php bin/sa.php doc deploys --server=ausa-web --title="AMail node" ...`.

Rollback: `sudo systemctl disable --now amail@ec2-user; sudo rm /usr/local/bin/amail /etc/systemd/system/amail@.service`; close the security group rule.

## Live test log

- **2026-09-16**: plan above executed against `ausa-web` (Amazon Linux 2023, public Elastic IP) from
  `austin-pc` (Windows 11, behind NAT). Server elected in seconds, PC -> server direct delivery in ~20 s,
  server -> PC via hold + pull in ~5 s. Recorded in the Server Administrator hub (runs `20260916-1652*`).
- **2026-09-16 (0.1.1)**: upgraded in place and migrated to the dedicated `amail` user. Flood, mailbox-cap
  and disk-floor behaviour covered by `internal/node/limits_test.go`; live two-way delivery re-verified
  with files landing group-readable for `ec2-user`.
- **2026-09-16 (0.1.2)**: `austin-pc` switched to `listen: "off"`. No port open on the PC (netstat shows
  nothing on 4444), still delivers directly to `ausa-web` and receives by pull. Covered by
  `TestClientOnlyListenOff`.
- **2026-09-17 (0.2.0)**: web UI verified in a browser on `austin-pc`: overview with live peer table,
  inbox preview modal, compose (note delivered to `ausa-web` in ~10 s), settings save with in-place node
  restart, log view, light and dark themes. API guards covered by `internal/ui/ui_test.go`.
- **2026-09-17 (0.2.1)**: attachments round trip. A PNG, a WAV and a 7 MB binary uploaded through the UI
  API as one grouped message (`Media test-20260917/` with sub folders) to `ausa-web`, copied back into the
  server's outbox, pulled by `austin-pc`; SHA-256 of all three matched the originals on both ends. Inbox
  shows the group with per-file Open / Play / Download. `TestBinaryIntegrity` covers 5 MB random data
  direct and relayed.

## Security tests (0.3.0)

| Package | What is covered |
|---|---|
| `internal/keys` | passphrase sealing round trip, wrong passphrase and tampering refused, sealed CA blocks issue without `AMAIL_CA_PASS`, sealed bundles, signature verify / wrong key / wrong id / revoked / foreign CA |
| `internal/node` | `TestForgedOriginAndTamperedContentRejected`: missing hash, altered content, forged origin without signature, forged origin with the wrong certificate, valid relayed signature accepted, stale signature after rename. `TestRevocationGossip`: revoke on the server only, a third node learns and refuses, persistence, re-issue works |
| `internal/proto` | `FuzzRecv`: 3 M inputs in 25 s with no panic on 2026-09-17; run longer with `go test ./internal/proto -fuzz=FuzzRecv -fuzztime=60s` |
| supply chain | `govulncheck ./...`: no vulnerabilities (2026-09-17, Go 1.27); runs in CI on every push |
