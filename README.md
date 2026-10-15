# Bad Proxy Core (BPC)

A small, dependency-free multiplexed proxy. **One binary runs as either the
client or the server** — the role is decided entirely by the config file.
Built for deployments behind a shared web host: the recommended topology
terminates TLS in **nginx** and lets BPC speak its HTTP-based transport on a
loopback port.

* Zero third-party dependencies: pure Go standard library (`go.mod` requires
  nothing).
* Application-layer protocols: **VLESS**, **Trojan**, **BTP v2** (this
  project's own protocol), plus plain **SOCKS5** / **HTTP proxy** inbounds and
  a direct **freedom** outbound.
* Transports: **raw TCP**, **TLS**, and **xhttp** (long-lived duplex HTTP
  POST, in the spirit of Xray's XHTTP stream mode).
* Rule-based routing (full / domain / regexp lists, optionally the
  `rules.dat` GeoIP-style dataset) selecting different outbounds per target.

WebSocket (`ws`/`wss`) and the old half-implemented `http2` transport were
removed: WebSocket duplicated what xhttp does while adding a third-party
dependency and per-message framing overhead, and the old `http2` transport
had no working client at all. See [protocol notes](#protocol-notes).

## Requirements

Go **1.24 or newer** (the transport relies on `http.Protocols`,
`ResponseController.EnableFullDuplex` and `ForceAttemptHTTP2`).

```sh
go build -o bad-proxy-core .
./bad-proxy-core --config config.json
```

The same binary is used on both ends; only the config differs.

## Quick start

### Server (standalone, TLS terminated by BPC itself)

```json
{
  "inbounds": [
    {
      "protocol": "vless",
      "host": "0.0.0.0",
      "port": "443",
      "users": ["a29e010d-e2a5-456b-becd-b811e0970d36"],
      "transport": "xhttp",
      "path": "/xrt",
      "tls_cert_path": "/etc/bad-proxy/server.crt",
      "tls_key_path": "/etc/bad-proxy/server.key"
    }
  ],
  "outbounds": [
    { "protocol": "freedom" }
  ]
}
```

### Client

```json
{
  "inbounds": [
    { "protocol": "socks", "host": "127.0.0.1", "port": "1080" },
    { "protocol": "http",  "host": "127.0.0.1", "port": "1081" }
  ],
  "outbounds": [
    {
      "protocol": "vless",
      "secret": "a29e010d-e2a5-456b-becd-b811e0970d36",
      "host": "proxy.example.com",
      "port": "443",
      "transport": "xhttp",
      "path": "/xrt",
      "tls": true,
      "authority": "proxy.example.com",
      "padding": "64-256"
    }
  ]
}
```

Point applications at `SOCKS5 127.0.0.1:1080` (remote DNS via
`socks5h`) or `HTTP 127.0.0.1:1081`. Smoke test:

```sh
curl -s --socks5-hostname 127.0.0.1:1080 https://example.com
```

### Server behind nginx

Listen plainly on loopback and let nginx do TLS — see
[Deploying behind nginx](#deploying-behind-nginx).

## Configuration reference

Top level: `general` (object, optional), `inbounds` (list), `outbounds`
(list), `routers` (list, optional). There must always be an outbound with an
empty `"tag"` — it is the default.

### General fields

Process-wide settings that belong to no inbound or outbound; everything here
is optional and falls back to the shown default.

| field | default | meaning |
|---|---|---|
| `stats` | off | duration string (`"30s"`, `"5m"`); logs one metrics line per interval |
| `handshake_timeout` | `10s` | deadline for connect+protocol handshake, both sides |
| `dial_timeout` | `15s` | deadline for a `freedom` direct connect to the target |
| `relay_idle_timeout` | off | reclaim a tunnel that moves zero bytes (either way) for this long; `0`/unset never times out |
| `relay_buffer_kib` | `64` | per-stream copy buffer, 1-1024 KiB |

```json
"general": { "stats": "30s", "handshake_timeout": "10s", "relay_buffer_kib": 64 }
```

### Inbound fields

| field | applies to | meaning |
|---|---|---|
| `protocol` | all | `vless`, `trojan`, `btp`, `socks`, `http` (default `btp`) |
| `host`, `port` | all | listen address |
| `users` | vless | list of UUIDs (also accepted: `secret` / `secrets`) |
| `secret` / `secrets` | trojan, btp | password(s) / HMAC keys; several may be active at once |
| `transport` | all | `tcp` (default), `tls`, `xhttp` |
| `path` | xhttp | request path this inbound answers (others get 403) |
| `tls_cert_path`, `tls_key_path` | tls, xhttp | enables TLS server-side |

`socks` and `http` are client-facing inbounds (no credentials; bind them to
loopback or protect them yourself). `http` accepts both `CONNECT` and
absolute-form plain proxying; for the latter the request line is rewritten to
origin form before relaying, as RFC 7230 §5.4 requires of a proxy.

### Outbound fields

| field | meaning |
|---|---|
| `tag` | routing target name; the empty tag is the default outbound |
| `protocol` | `vless`, `trojan`, `btp`, `socks`, `freedom` (default `btp`) |
| `secret` | UUID (vless) / password (trojan) / HMAC key (btp) |
| `host`, `port` | upstream address (ignored by `freedom`) |
| `transport` | `tcp`, `tls`, `xhttp` |
| `path` | xhttp request path |
| `authority` | TLS SNI / `Host` header (defaults to `host`) |
| `padding` | xhttp `X-Padding` random byte range, `"min-max"`; empty disables |
| `tls` | client-side TLS (use with `xhttp` directly, or implies SNI handling) |
| `insecure` | skip TLS certificate verification (testing only) |

`freedom` dials targets directly — the outbound a server config uses.

### Routing

```json
"routers": [
  { "tag": "proxy-us", "rules": ["domain:example.org", "keyword:bing", "regexp:^.*\\.cn$", "geosite:cn"] }
]
```

Each rule is either `full:<host>`, `domain:<suffix>`, `keyword:<substring>`,
`regexp:<pattern>`, or a bare name resolved from `rules.dat` (`--router-path`,
default `rules.dat`; a missing file only disables named lists). The first
matching router selects that tag's outbound; otherwise the default (empty tag)
is used. `rules.dat` is the gob-encoded domain-list dataset produced by
`build.sh`. `domain:<suffix>` matches on label boundaries — `example.org`
matches `example.org` and `a.example.org` but not `notexample.org`; for plain
substring matching use `keyword:<substring>` (Aho-Corasick, one scan over all
keywords), which ignores boundaries — `bing` also matches `www.bing.com`.

## Protocol notes

### VLESS (`vless`)

Byte-for-byte compatible with current Xray
(`proxy/vless/encoding.EncodeRequestHeader`, verified against Xray
v26.3.27 by capturing both sides):

```
request:  ver(1=0) | uuid(16) | addonLen(1=0) | addons | cmd(1=TCP) | port(2) | atyp | addr
response: ver(1=0) | addonLen(1=0)
```

Two traps versus the old v2fly docs: the version byte is **0**, and the
**port precedes the address** (Xray's addrParser is PortThenAddress; atyp
table 1=IPv4, 2=Domain, 3=IPv6). There is no status byte anymore - a
failed handshake simply closes. Also note Xray flushes the 2-byte
response only together with the first downstream data, so BPC reads it
lazily in the relay path instead of blocking on it during the handshake.

Only TCP command and an empty `flow` are supported — xtls-rprx-vision needs
an inner TLS stream and is rejected with an error rather than silently
misbehaving. VLESS authenticates with the UUID alone and carries no own
encryption: run it over `tls`/`xhttp+tls`, or behind nginx.

### Trojan (`trojan`)

Standard shape: `hex(SHA224(password)) + CRLF + 0x01 + atyp/addr/port +
CRLF`, then the raw stream. Passwords are compared in constant time. UDP
associate is not supported (returns an error, unlike some clients that
hang).

### BTP v2 (`btp`) — Bad-Proxy-Transport, this project's native protocol

```
+--------+-----------+-----------+-----------+-----+-----+------+------+---------+
| HMAC   | confusion | confusion | timestamp | dir | len | host | port | payload |
| digest | length    | bytes     |           |     |     |      | (2B) | (raw)   |
+--------+-----------+-----------+-----------+-----+-----+------+------+---------+
|   32   |     1     |   0-64    |     4     |  1  |  1  | var  |  2   | stream  |
```

The HMAC-SHA256 covers the fixed header only; a timestamp window (±210 s)
plus an LRU digest cache (210 k entries) stop replays; the confusion field
randomizes the first segment's length. Payload bytes stream unauthenticated,
integrity being delegated to the transport (TLS) — the same trade-off
VLESS/Trojan make. **v2 is not compatible with v1**: v1 authenticated
"header + whatever arrived in the first read", which broke whenever TCP
fragmented the segment. Fallback: a failed handshake that looks like a plain
HTTP GET is answered with an innocuous page.

### xhttp (`xhttp`) — duplex stream over one HTTP POST

```
client ->  POST <path>      Content-Type: application/octet-stream
                            X-Session-Id: <8 random bytes, hex>
                            X-Sec-Mode: stream-one
                            X-Padding: <optional "min-max" resolved>
                            body: upstream stream   (chunked / h2 stream)
server ->  200 OK, streamed body: downstream stream
```

One long-lived request is one tunnel: the request body carries client→server
bytes, the response body server→client. Over plain TCP (the nginx loopback
hop) it is HTTP/1.1 chunked duplex; over TLS the stdlib negotiates HTTP/2 by
ALPN and every tunnel is one h2 stream on a shared connection. The server
answers 403 to any other path/method, so it coexists with static content.

Notes on behaviour found the hard way: `WriteHeader` normally drains and
closes the request body, so the handler must call
`ResponseController.EnableFullDuplex()` before relaying; `http.Protocols`
needs `SetHTTP1(true)` **and** `SetUnencryptedHTTP2(true)` or plain-HTTP h2
probes RST existing HTTP/1.1 connections.

xhttp follows Xray's *design*, not its byte-level handshake: sessions are
identified by `X-Session-Id`, and there is no `__xhttp_mode` sub-path
negotiation, so it is **not wire-interoperable with xray's xhttp**. VLESS,
Trojan and BTP payloads ride it fine between two BPC endpoints.

## Deploying behind nginx

Topology: nginx owns port 443/TLS (one vhost shared with your site); BPC
listens on loopback with a **plain xhttp** inbound; nginx routes only your
secret path to it and streams bytes both ways.

Server config (see `config_server.json`):

```json
{
  "inbounds": [
    {
      "protocol": "vless",
      "host": "127.0.0.1",
      "port": "8081",
      "users": ["a29e010d-e2a5-456b-becd-b811e0970d36"],
      "transport": "xhttp",
      "path": "/xrt"
    }
  ],
  "outbounds": [ { "protocol": "freedom" } ]
}
```

nginx server block:

```nginx
server {
    listen 443 ssl;
    http2 on;                       # client side may use h2 or h1
    server_name proxy.example.com;
    ssl_certificate     /etc/letsencrypt/live/example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/example.com/privkey.pem;

    # your normal site ...
    location / { ... }

    # tunnel endpoint — the only part BPC sees
    location = /xrt {
        client_max_body_size 0;     # unbounded request body = upstream stream
        proxy_buffering off;        # mandatory: stream both directions
        proxy_request_buffering off;
        proxy_http_version 1.1;     # chunked duplex to the backend
        proxy_read_timeout  3600s;  # idle tunnels are closed after this
        proxy_send_timeout  3600s;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_pass http://127.0.0.1:8081;
    }
}
```

Client config: `transport: xhttp`, `tls: true`, `host: proxy.example.com`,
`port: "443"`, `path: /xrt`, matching `authority`. The client↔nginx hop is
real TLS (h2 or h1 by ALPN); nginx↔BPC is plaintext loopback, so
`X-Sec-Mode`/`X-Session-Id` headers terminate cleanly and VLESS/Trojan/BTP
handshakes run over the tunnel body.

Checklist:

* `proxy_buffering off` **and** `proxy_request_buffering off` — with either
  on, nginx fully buffers the POST body and the upstream half of the stream
  deadlocks.
* `location = /xrt` (exact match) so nothing else proxies through it.
* Keep `proxy_http_version 1.1`; the backend duplex mode relies on chunked
  request bodies and streamed responses.
* Long-lived tunnels: raise `proxy_read_timeout`; idle clients reconnect
  transparently.
* Multiple protocols coexist on one host by using several secret paths and
  several `location` blocks with different `proxy_pass` ports (see
  `config_server.json`).
* Docker: the provided `Dockerfile` builds from source; mount
  `/etc/bad-proxy/config.json` (and `rules.dat` if used). In container-mode
  nginx the backend address is the container name, not `127.0.0.1`.

## Repository layout

```
main.go             CLI entry (one binary, role from config)
proxy/              engine: inbounds, outbounds, routing, relay loop
protocols/          handshake + stream framing per protocol
  common.go         Buffered conn helper: exact header-field reads that
                    never truncate on TCP fragmentation, target codec
  vless.go trojan.go btp.go socks.go http.go free.go
transport/          how bytes reach the peer: tcp / tls / xhttp
metrics/            atomic counters + periodic stats line
router/ structure/  rule matching (gob rules.dat, Aho-Corasick domain
                    matcher, LRU replay cache, ring queue)
```

Relay lifecycle: accept → 10 s handshake deadline → protocol `Connect()`
(success: target address; failure: `Fallback()` answers probes) → deadline
cleared → two copy directions pumping through 64 KiB buffers from a
`sync.Pool`. The copy deliberately hides the `ReadFrom`/`WriteTo` splice
interfaces: on Windows the `TCPConn→TCPConn` TransmitFile path measured
~80 MB/s on loopback where the plain buffered loop passes ~400 MB/s (and
the pooled buffer means zero relay allocations per tunnel).

## Observability

The `metrics` package (stdlib-only) keeps every counter in an
`atomic.Int64`; call sites cache their `*ProtoStats` pointer at startup, so
a hot-path event costs one atomic add and nothing else. With `general.stats`
set to `"30s"` the engine logs one line like:

```
stats: tunnels=12 up=1.1MiB down=48.2MiB copy_err=0 | vless acc=340 hs_fail=2 dial_ok=0 dial_fail=0 | freedom acc=0 hs_fail=0 dial_ok=338 dial_fail=4
```

`tunnels` is the live relay gauge, `up`/`down` count relay bytes per chunk,
and each configured protocol reports accepts, failed handshakes (probes and
wrong credentials show up here) and upstream dial outcomes. Genuine errors
(failing inbounds, copy errors, panics) still log as before; the old
per-connection `vless connect to …` stderr line is gone - it serialized
every accept through the log lock and is now a counter.

## Testing

Unit/integration tests cover the xhttp duplex round trip and a raw HTTP
probe of the server.

```sh
go test ./...
```

The repo root contains a ready-made end-to-end matrix (loopback origin on
`127.0.0.1:18000`, no external network needed):

```sh
go build -o bad-proxy-server.exe .
go build -o bad-proxy-client.exe .
./bad-proxy-server.exe --config config_test_server.json &   # vless/btp/trojan
./bad-proxy-client.exe --config config_test_client.json &   # socks 18081, http 18080 (vless+xhttp)
./bad-proxy-client.exe --config config_test_btp.json &      # socks 18091, http 18094 (btp+tcp)
./bad-proxy-client.exe --config config_test_trojan.json &   # socks 18092, http 18095 (trojan+xhttp)
curl -s --socks5-hostname 127.0.0.1:18081 http://127.0.0.1:18000/small.txt
curl -s --socks5-hostname 127.0.0.1:18091 http://127.0.0.1:18000/small.txt   # btp
curl -s --socks5-hostname 127.0.0.1:18092 http://127.0.0.1:18000/small.txt   # trojan
curl -s --proxy http://127.0.0.1:18080 http://127.0.0.1:18000/small.txt      # vless+xhttp, http inbound
curl -s --proxy http://127.0.0.1:18094 http://127.0.0.1:18000/small.txt      # btp, http inbound
curl -s --proxy http://127.0.0.1:18095 http://127.0.0.1:18000/small.txt      # trojan, http inbound
```

Verified matrix (1 MB transfer, md5-checked end to end):

| app protocol | transport | socks5h | http proxy |
|---|---|---|---|
| vless | xhttp (plain, chunked duplex) | ✅ | ✅ |
| trojan | xhttp | ✅ | ✅ |
| btp v2 | tcp | ✅ | ✅ |

### Interoperability with real Xray

Tested against Xray v26.3.27 on loopback (origin on `127.0.0.1:18000`):

| direction | app protocol | transport | result |
|---|---|---|---|
| BPC client → Xray server | vless | tcp | ✅ md5-checked |
| Xray client → BPC server | vless | tcp | ✅ md5-checked |
| BPC client → Xray server | vless | tls | ✅ md5-checked |
| Xray client → BPC server | vless | tls | ✅ md5-checked |

Xray's access log shows it authenticating BPC's requests, and BPC accepts
Xray's. VLESS carries no encryption itself, so plain `tcp` is cleartext —
prefer `tls` or nginx in real deployments. One caveat on current Xray
builds: `allowInsecure` has been removed in favor of
`pinnedPeerCertSha256`, so with a self-signed test cert the Xray side must
pin the certificate hash while BPC uses its `insecure` flag. Trojan
follows Xray's byte layout too (SOCKS atyp table, address before port —
note this differs from VLESS's port-before-address), but BTP and BPC's
xhttp are native protocols with no Xray counterpart.

### Throughput vs Xray (measured)

Windows loopback, origin `python -m http.server`, 200 MiB file per stream,
BPC↔BPC vs Xray↔Xray client/server pairs on the same machine, same UUID
plain VLESS. Single stream: 5 runs, max and median of curl's bytes/s.
Concurrent: 4 parallel streams, aggregate.

| path | single max | single median | 4-stream aggregate |
|---|---|---|---|
| (no proxy, ceiling) | 1503 MB/s | 1436 MB/s | 2100 MB/s |
| BPC vless/tcp | **134** | **111** | **624** |
| Xray vless/tcp | 117 | 82 | 453 |
| BPC vless/tls | **414** | **360** | **1359** |
| Xray vless/tls | 322 | 316 | 1082 |

BPC stays ahead of Xray on every measured path (~15-38% higher throughput);
TLS beats plain TCP in both products on this machine - cleartext loopback
hops appear to pay an inspection cost that opaque TLS traffic skips.

Wrong-credential handshakes are rejected (`password mismatch`, unknown UUID,
bad HMAC) and never relay origin bytes. A plain HTTP GET against the BTP port
is answered immediately with the innocuous fallback page (the probe is
detected before any header parsing), and the xhttp inbounds reply `403` to
every path other than their own.

## Notes and limitations

* No UDP relay (SOCKS `UDP ASSOCIATE`, VLESS `cmd=2`, Trojan `0x03` return
  errors). QUIC/Hysteria-style datagram transports are out of scope.
* xhttp is BPC↔BPC only (see the xhttp note above); for xray interop use
  app protocol VLESS over the `tcp` or `tls` transport — verified both
  directions against Xray v26.3.27 in the matrix above.
* Bind `socks`/`http` inbounds to loopback — they are unauthenticated by
  design.
* BTP v1 clients cannot connect to BTP v2 servers (see above).
