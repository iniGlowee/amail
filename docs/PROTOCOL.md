# AMail wire protocol (version 1)

Transport: TCP, canonical port **4444**, TLS 1.3 with mutual authentication
against the network CA. One request per connection. Either side may close
after the final frame.

## Frame

```
uint32 headerLen (big-endian)
headerLen bytes  JSON header
uint64 bodyLen   (big-endian)
bodyLen bytes    raw body (a file's bytes), may be 0
```

Header fields (all optional except `proto` and `type`):

| Field | Type | Meaning |
|---|---|---|
| `proto` | int | always `1` |
| `type` | string | frame type, below |
| `from` | string | node id sending this frame (informational; identity comes from the TLS certificate) |
| `to` | string | final destination of a file |
| `origin` | string | node that originally sent the file |
| `id` | string | message id (`<unix-ms>-<12 hex>`) |
| `name` | string | relative file name, `/` separated, may include sub folders |
| `size` | int64 | file size in bytes |
| `hops` | int | relays so far |
| `time` | string | RFC 3339 UTC |
| `code` | string | on `error`: `reject` (permanent) or `busy` (transient) |
| `msg` | string | human readable note |
| `status` | object | on `ok` to a `status` request |
| `peers` | array | on `ok` to a `peers` request |
| `sha256` | string | hex SHA-256 of the file (deliver, item); required since 0.3 |
| `sig` | string | base64 ASN.1 ECDSA-P256-SHA256 signature by the origin over `amail-sig-v1\n<origin>\n<to>\n<name>\n<size>\n<sha256>` (required when origin ≠ connecting peer) |
| `origin_cert` | string | PEM certificate of the origin, so any receiver can verify `sig` |
| `revoked` | array | certificate serials (lower-case hex) the sender knows to be revoked; the receiver merges them (any frame) |

## Requests

### status

```
C: {type:"status"}
S: {type:"ok", status:{node_id, network, role, server, version, os, arch,
                       uptime, time, listening, inbox, outbox, forward}}
```

`role` is `server`, `client` or `searching`; `server` is the id of the node
currently acting as server from the answerer's point of view. This is the
"status and type check" every node offers.

### peers

```
C: {type:"peers"}
S: {type:"ok", peers:[{id, host, port}, ...]}
```

The answerer's whitelist. Used by `amail peers [--merge]`.

### deliver (two phase)

```
C: {type:"deliver", to, origin, id, name, size, hops}      body: none
S: {type:"ok"}            or  {type:"error", code, msg}
C: {type:"body", id, size}                                   body: the file
S: {type:"ok", id}        or  {type:"error", code, msg}
```

The announce phase lets the receiver refuse before any bytes flow (too large,
unknown destination, too many hops, blacklisted origin, missing or invalid
origin proof). If `to` is empty or the receiver itself, the file lands in
`inbox/<origin>/<name>`. Otherwise the receiver must know `to` on its
whitelist and `hops` must be below 3; it then holds the file in
`forward/<to>/<origin>/` with `hops+1`, keeping `sha256`, `sig` and
`origin_cert` in the sidecar for the next hop.

The body is hashed as it is written; if it does not match `sha256` the
temp file is removed and the reply is `error` with code `reject`.

### Origin proof

```
canonical = "amail-sig-v1" "\n" origin "\n" to "\n" name "\n" size "\n" sha256hex
sig       = base64( ECDSA_P256_SHA256_sign(origin_key, canonical) )
```

Receivers verify, for a relayed file: `origin_cert` chains to the network
CA, its CN equals `origin`, its serial is not revoked, `sig` verifies. For a
direct file the TLS identity is the origin; a signature, if present, is
verified as well. Renaming, re-addressing or resizing a file in transit
invalidates the signature. See docs/SECURITY.md section 4.

### pull

```
C: {type:"pull"}
S: {type:"item", id, origin, to, name, size, hops}          body: the file
C: {type:"ack", id}
S: {type:"item", ...} ...
S: {type:"end"}
```

The server sends every file it holds for the caller (identified by its
certificate CN), each with its origin proof. Each item is deleted on the
server only after the `ack`. If the client cannot store an item it sends
`{type:"error", code:"busy"}` and the server keeps the rest for next time;
`code:"reject"` (bad signature, hash mismatch) makes the server move that
item to its `failed/` folder and continue with the next.

### Revocation gossip

Every request and every reply may carry `revoked`. A node merges serials
received from a whitelisted peer into its own list, saves them, and refuses
matching certificates in both directions of every later handshake.

## Access checks, in order

1. Remote IP against the blacklist, connection caps and the failed-handshake
   ban (before the TLS handshake).
2. TLS 1.3 handshake: client certificate must chain to the network CA and
   not be revoked.
3. Certificate CN against the blacklist.
4. Certificate CN must be on the whitelist (or the whitelist is empty, or it is
   the node itself).
5. Per-peer request rate limit.
6. Per request: destination known, origin not blacklisted, size limit, hop
   limit, storage room, origin proof.

## Limits

* Header at most 1 MiB.
* Body at most `max_file_mb` (default 1024 MiB) on the receiving node.
* Idle timeout 90 s (a stalled connection is closed); TLS handshake 10 s.
