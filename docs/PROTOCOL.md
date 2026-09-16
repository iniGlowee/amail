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
unknown destination, too many hops, blacklisted origin). If `to` is empty or
the receiver itself, the file lands in `inbox/<origin>/<name>`. Otherwise the
receiver must know `to` on its whitelist and `hops` must be below 3; it then
holds the file in `forward/<to>/<origin>/` with `hops+1`.

### pull

```
C: {type:"pull"}
S: {type:"item", id, origin, to, name, size, hops}          body: the file
C: {type:"ack", id}
S: {type:"item", ...} ...
S: {type:"end"}
```

The server sends every file it holds for the caller (identified by its
certificate CN). Each item is deleted on the server only after the `ack`. If
the client cannot store an item it sends `{type:"error", code:"busy"}` and the
server keeps the rest for next time.

## Access checks, in order

1. Remote IP against the blacklist (before the TLS handshake).
2. TLS handshake: client certificate must chain to the network CA.
3. Certificate CN against the blacklist.
4. Certificate CN must be on the whitelist (or the whitelist is empty, or it is
   the node itself).
5. Per request: destination known, origin not blacklisted, size limit, hop limit.

## Limits

* Header at most 1 MiB.
* Body at most `max_file_mb` (default 1024 MiB) on the receiving node.
* Connection deadline 30 minutes; TLS handshake 10 seconds.
