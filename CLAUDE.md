# AMail - instructions for Claude sessions

AMail (Armas Mail) is a Go program: a whitelist/blacklist file-mail network
over mutual TLS on port 4444, with mailbox folders on disk and an embedded
web UI. Owner and network operator: Austin Armas. This file tells a session
how to work here without losing what was built.

## Read first

- `docs/HISTORY.md`: what exists, why, and the decision log. Start here.
- `docs/SECURITY.md`: the threat model. Do not weaken anything listed there
  without saying so explicitly.
- `docs/PROTOCOL.md`, `docs/DESIGN.md`: wire format and routing rules.
- The **live network** (addresses, paths, services, keys) is documented in
  the operator's private runbook outside this repository (in Austin's
  Server Administrator hub, `docs/AMAIL-RUNBOOK.md`). Nothing private goes
  in this repo; it is public on GitHub.

## Toolchain

- Go 1.24 or newer on PATH (the operator's machine has a user-local
  install; see the private runbook for its location).
- Standard library only. Do not add dependencies without a reason written
  in the commit message.
- `go.mod` says `go 1.24` (needs `crypto/pbkdf2`).

## Build, test, release

```bash
gofmt -l . && go vet ./... && GOOS=linux go vet ./...
go test ./...                       # ~90 s; node tests use real timers
go test ./internal/proto -fuzz=FuzzRecv -fuzztime=30s
sh scripts/build.sh <version>       # dist/ for windows/amd64, linux/amd64, linux/arm64 + SHA256SUMS
```

- Bump the version in `docs/CHANGELOG.md` with every user-visible change.
  `main.version` is set by the build script from the argument.
- All tests green before calling anything done. The node suite is the
  contract: election, relay, pull, limits, signatures, revocation.
- Commit with the attribution line the session's system reminder gives.
  Git user is Austin Armas. `dist/`, keys, `config.json`, `*.amailkey` are
  gitignored; keep it that way.
- Publishing to GitHub is Austin's call (no `gh` on this machine).

## Rules that matter

- **Upgrade every node together** when the protocol's expectations change
  (0.3.0 nodes reject files from 0.2.x senders). Check `docs/CHANGELOG.md`.
- **Server changes go through the operator's hub recorder** (procedure and
  commands in the private runbook). Verify the SHA-256 from
  `dist/SHA256SUMS` before installing a binary on any server, restart the
  service, and file a deploy doc there.
- Never read, print or copy `ca.key`, `node.key` or `.amailkey` contents.
  Refer to them by path.
- Line endings: `.gitattributes` pins LF for anything shipped to Linux.
- The UI stays loopback-only and unauthenticated by design; do not add a
  remote bind without an authenticating layer in front.
- Keep `docs/HISTORY.md` and the hub runbook current when you change the
  deployment or make a design decision.

## Layout

```
cmd/amail/          CLI (init, join, run, ui, status, send, peers, whitelist, blacklist, revoked, ca ...)
internal/config/    config.json model, whitelist/blacklist/revoked
internal/keys/      CA, bundles, TLS configs, sealing, signatures
internal/proto/     frame format + fuzz test
internal/mailbox/   folders on disk, hash-checked writes
internal/node/      daemon: listener, limiter, election, routing, relay, pull, gossip
internal/ui/        embedded web UI (static/ = HTML, CSS, JS)
scripts/            build + service install (systemd, Windows scheduled task)
docs/               HISTORY, DESIGN, PROTOCOL, SECURITY, OPERATOR, TESTING, CHANGELOG
```
