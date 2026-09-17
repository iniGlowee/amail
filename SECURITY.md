# Security policy

The threat model, what AMail protects and what it does not, and the
operator checklist are in [docs/SECURITY.md](docs/SECURITY.md).

## Reporting a problem

Please do not open a public issue for a security problem. Contact the
network operator privately (see the README for how members reach the
operator) with:

* the AMail version (`amail version`) on both ends,
* the relevant lines from `amail.log`,
* if possible, a reproduction against the loopback test network described
  in [docs/TESTING.md](docs/TESTING.md).

## What never belongs in this repository

* Certificate authority material (`ca/`), node keys (`keys/`), key bundles
  (`*.amailkey`), `config.json` (it holds the whitelist with real hosts and
  the revocation list). All of these are in `.gitignore`.
* Real addresses, paths or credentials of any node. Deployment records are
  kept by the operator outside the repository.
