# Bad Proxy Core (BPC)

A small, dependency-free multiplexed proxy. **One binary runs as either the
client or the server** — the role is decided entirely by the config file.
Built for deployments behind a shared web host: the usual topology terminates
TLS in a reverse proxy (**nginx** or **Caddy**) and lets BPC speak its
HTTP-based transport on a loopback port, so BPC never needs to know whether it
sits behind a proxy.

* Zero third-party dependencies: pure Go standard library (`go.mod` requires
  nothing).
* Application-layer protocols: **VLESS**, **Trojan**, **BTP v2** (this
  project's own protocol), plus plain **SOCKS5** / **HTTP proxy** inbounds and
  a direct **freedom** outbound.
* Transports: **raw TCP**, **TLS**, and **xhttp** (long-lived duplex HTTP
  POST, in the spirit of Xray's XHTTP stream mode).
* Rule-based routing (full / domain / keyword / regexp lists, optionally the
  `rules.dat` GeoIP-style dataset) selecting different outbounds per target.

As of **v2.0.0-beta2** the per-endpoint transport options live in a nested
`"transport": { … }` object (they used to be flat keys on the endpoint), and
the client-side `insecure` toggle was removed — the client always verifies the
server certificate. See [Transport object](#transport-object).

WebSocket (`ws`/`wss`) and the old half-implemented `http2` transport were
removed: WebSocket duplicated what xhttp does while adding a third-party
dependency and per-message framing overhead, and the old `http2` transport had
no working client at all. See [protocol notes](#protocol-notes).

## Supported protocols at a glance

There are two independent layers, both chosen per endpoint:

* **Application-layer protocol** — the handshake and framing *inside* the
  tunnel. This is what authenticates the connection and carries the target
  address.
* **Transport** — how the tunnel's bytes reach the peer (raw TCP, TLS, or an
  xhttp POST). It says nothing about who is allowed to connect.

Any app protocol rides any transport; the app-protocol fields (`protocol`,
credential) stay separate from the `transport` object.

### Application-layer protocols

| app protocol | inbound | outbound | credential | wire-compatible with Xray |
|---|:-:|:-:|---|---|
| **VLESS** (`vless`) | ✅ | ✅ | `users`/`secret` = UUID | **yes** — verified both directions over tcp/tls |
| **Trojan** (`trojan`) | ✅ | ✅ | `secret` = password (SHA224) | follows Xray's byte layout (not separately verified) |
| **BTP v2** (`btp`) | ✅ | ✅ | `secret` = HMAC key | no — native to BPC |
| **SOCKS5** (`socks`) | ✅ (client-facing) | ✅ (upstream) | none (bind to loopback) | n/a |
| **HTTP proxy** (`http`) | ✅ (client-facing) | — | none (bind to loopback) | n/a |
| **freedom** | — | ✅ (direct dial) | n/a | n/a |

`socks` / `http` inbounds are how your browser or `curl` enters BPC; they are
unauthenticated, so keep them on loopback. `freedom` is the outbound a server
uses to hand the decoded target straight to the internet — it opens the target
address itself rather than dialing an upstream.

### Transport layer

| transport | endpoint fields | notes |
|---|---|---|
| **tcp** (default) | — | raw TCP; cleartext, so pair with an encrypted app protocol or a proxy |
| **tls** | server `cert`+`key`; client `tls: true`+`authority` | stdlib TLS (≥1.2); server TLS is on when `cert`/`key` are set |
| **xhttp** | `path` (both); client `tls`, `authority`, `padding`; server `cert`+`key` | duplex stream over one long-lived HTTP POST; runs under nginx or Caddy |

Every app protocol above (`vless`, `trojan`, `btp`) can use any of these three
transports. `tls` and `xhttp` combine — xhttp over real TLS.

## Requirements

Go **1.24 or newer** (`go.mod` pins `go 1.24.12`; the xhttp transport relies on
`http.Protocols`, `ResponseController.EnableFullDuplex`, and
`ForceAttemptHTTP2`). The same binary runs on both ends; only the config
differs.

```sh
go build -o bad-proxy-core .
./bad-proxy-core --config config.json
```

## Quick start

### Server (standalone, TLS terminated by BPC itself)

```json
{
  "inbounds": [
    {
      "protocol": "vless",
      "host": "0.0.0.0",
      "port": "443",
      "users": ["01234567-89ab-4cde-8f01-23456789abcd"],
      "transport": {
        "type": "xhttp",
        "path": "/xrt",
        "cert": "/etc/bad-proxy/server.crt",
        "key": "/etc/bad-proxy/server.key"
      }
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
      "secret": "01234567-89ab-4cde-8f01-23456789abcd",
      "host": "proxy.example.com",
      "port": "443",
      "transport": {
        "type": "xhttp",
        "path": "/xrt",
        "tls": true,
        "authority": "proxy.example.com",
        "padding": "64-256"
      }
    }
  ]
}
```

Point applications at `SOCKS5 127.0.0.1:1080` (remote DNS via `socks5h`) or
`HTTP 127.0.0.1:1081`. Smoke test:

```sh
curl -s --socks5-hostname 127.0.0.1:1080 https://example.com
```

### Switching to Trojan or BTP

The structure is identical — change `protocol` and the credential (keep the
transport you like) on **both** ends:

| | VLESS | Trojan | BTP v2 |
|---|---|---|---|
| `protocol` | `vless` | `trojan` | `btp` |
| server credential | `users: ["<uuid>"]` | `secret: "<password>"` | `secret: "<hmac-key>"` |
| client credential | `secret: "<uuid>"` | `secret: "<password>"` | `secret: "<hmac-key>"` |

Trojan over `tls` (no xhttp), server inbound:

```json
{ "protocol": "trojan", "host": "0.0.0.0", "port": "443",
  "secret": "your-password",
  "transport": { "type": "tls",
    "cert": "/etc/bad-proxy/server.crt", "key": "/etc/bad-proxy/server.key" } }
```

BTP is BPC's native protocol: the secret is an HMAC key that is **never put on
the wire** (the header carries a keyed digest), and a failed handshake that
looks like an HTTP GET is answered with a decoy page.

### Server behind a reverse proxy

Listen plainly on loopback and let nginx or Caddy do TLS — see
[Deploying behind nginx](#deploying-behind-nginx) and
[Deploying behind Caddy](#deploying-behind-caddy). The inbound is identical
either way: `"transport": { "type": "xhttp", "path": "/xrt" }` with **no**
`cert`/`key` (the proxy terminates TLS; BPC stays plaintext on loopback).

## Configuration reference

Top level: `general` (object, optional), `inbounds` (list), `outbounds`
(list), `routers` (list, optional). There must always be an outbound with an
empty `"tag"` — it is the default.

### General fields

Process-wide settings that belong to no inbound or outbound; everything here
is optional and falls back to the shown default.

| field | default | meaning |
|---|---|---|
| `handshake_timeout` | `10s` | deadline for connect+protocol handshake, both sides |
| `dial_timeout` | `15s` | deadline for a `freedom` direct connect to the target |
| `relay_idle_timeout` | off | reclaim a tunnel that moves zero bytes (either way) for this long; `0`/unset never times out |
| `relay_buffer_kib` | `64` | per-stream copy buffer, 1-1024 KiB |
| `log_file` | (console only) | path the detailed log is appended to; empty means no file |
| `log_level` | `debug` | minimum level written to `log_file`: `debug`/`info`/`warn`/`error`/`off` |
| `console_level` | `info` | minimum level echoed to stdout; raise to `warn`/`error` to keep only critical lines |

```json
"general": {
  "handshake_timeout": "10s",
  "relay_buffer_kib": 64,
  "log_file": "/var/log/bad-proxy.log",
  "log_level": "debug",
  "console_level": "info"
}
```

### Inbound fields

| field | applies to | meaning |
|---|---|---|
| `protocol` | all | `vless`, `trojan`, `btp`, `socks`, `http` (default `btp`) |
| `host`, `port` | all | listen address |
| `users` | vless | list of UUIDs (also accepted: `secret` / `secrets`) |
| `secret` / `secrets` | trojan, btp | password(s) / HMAC keys; several may be active at once |
| `transport` | all | an object describing the hop — see [Transport object](#transport-object) |

`socks` and `http` are client-facing inbounds (no credentials; bind them to
loopback or protect them yourself). `http` accepts both `CONNECT` and
absolute-form plain proxying; for the latter the request line is rewritten to
origin form before relaying, as RFC 7230 §5.4 requires of a proxy. A `btp`
inbound with **no** secret denies every connection rather than accepting them.

### Outbound fields

| field | meaning |
|---|---|
| `tag` | routing target name; the empty tag is the default outbound |
| `protocol` | `vless`, `trojan`, `btp`, `socks`, `freedom` (default `btp`) |
| `secret` | UUID (vless) / password (trojan) / HMAC key (btp) |
| `host`, `port` | upstream address (ignored by `freedom`) |
| `transport` | an object describing the hop — see [Transport object](#transport-object) |

`freedom` dials targets directly — the outbound a server config uses. A `socks`
outbound upstreams to another SOCKS5 proxy.

### Transport object

The `transport` block is the one place that describes how bytes reach the peer;
application-protocol fields stay separate from it. Each transport reads only
the fields it uses; unknown ones are ignored. There is no `insecure` — the
client always verifies the server certificate.

```json
"transport": { "type": "xhttp", "path": "/xrt", "tls": true, "authority": "host", "padding": "64-256" }
```

| field | applies to | meaning |
|---|---|---|
| `type` | all | `tcp` (default), `tls`, `xhttp` |
| `path` | xhttp | request path; the server matches it as a **prefix** (others get 403), the client sends it |
| `cert`, `key` | server, tls/xhttp | TLS cert + private key paths; presence is what enables server TLS (a server has no separate `tls` toggle) |
| `tls` | client, xhttp | encrypt the hop (https scheme + TLS). Plaintext to a TLS port and vice-versa both fail, so the client must state it |
| `authority` | client, tls/xhttp | SNI / `Host` override; defaults to `host` |
| `padding` | client, xhttp | `X-Padding` random byte range `"min-max"`; unset uses the default `"50-200"`, an explicit `"0"` disables |

Defaults that are not configured live in the `common` package: padding
`"50-200"`, relay buffer 64 KiB (max 1024), handshake timeout 10s, dial timeout
15s.

### Routing

```json
"routers": [
  { "tag": "proxy-us", "rules": ["domain:example.org", "keyword:bing", "regexp:^.*\\.cn$", "geosite:cn"] }
]
```

Each rule is either `full:<host>`, `domain:<suffix>`, `keyword:<substring>`,
`regexp:<pattern>`, or a bare name resolved from `rule.dat` (`--router-path`,
default `rule.dat`; a missing file only disables named lists). The first
matching router selects that tag's outbound; otherwise the default (empty tag)
is used. `rule.dat` is a gob-encoded domain-list dataset that `build.sh`
builds from the `v2fly/domain-list-community` git submodule and bundles into the
artifacts; it is optional — without it only the named `geosite:`/`geoip:`
lists are unavailable. `domain:<suffix>` matches on label boundaries —
`example.org` matches `example.org` and `a.example.org` but not
`notexample.org`; for plain substring matching use `keyword:<substring>`
(Aho-Corasick, one scan over all keywords), which ignores boundaries — `bing`
also matches `www.bing.com`.

## Protocol notes

### VLESS (`vless`)

Byte-for-byte compatible with current Xray
(`proxy/vless/encoding.EncodeRequestHeader`, verified against Xray v26.3.27 by
capturing both sides):

```
request:  ver(1=0) | uuid(16) | addonLen(1=0) | addons | cmd(1=TCP) | port(2) | atyp | addr
response: ver(1=0) | addonLen(1=0)
```

Two traps versus the old v2fly docs: the version byte is **0**, and the **port
precedes the address** (Xray's addrParser is PortThenAddress; atyp table
1=IPv4, 2=Domain, 3=IPv6). There is no status byte anymore — a failed
handshake simply closes. Also note Xray flushes the 2-byte response only
together with the first downstream data, so BPC reads it lazily in the relay
path (`VlessOutbound.Read` / `WriteTo`) instead of blocking on it during the
handshake — which is what lets the tunnel survive a buffering proxy.

Only TCP (`cmd=1`) and an empty `flow` are supported; xtls-rprx-vision needs an
inner TLS stream and is rejected with an error (`unsupported flow`) rather than
silently misbehaving. VLESS authenticates with the UUID alone and carries no own
encryption: run it over `tls`/`xhttp+tls`, or behind a TLS-terminating proxy.

### Trojan (`trojan`)

```
hex(SHA224(password))[56] | CRLF | 0x01 | SOCKS5 atyp/addr/port | CRLF | raw stream
```

The 56-byte digest of the password opens the request, then a SOCKS5-style
`CONNECT` (atyp 1=IPv4 / 3=Domain / 4=IPv6, address **before** port — note this
differs from VLESS's port-before-address) names the target. Passwords are
compared in constant time so a wrong one cannot be recovered byte by byte
through timing. Only `CMD=CONNECT` is supported; UDP associate returns an error
(unlike some clients that hang). Trojan relies on the transport for secrecy, so
run it over `tls`/`xhttp+tls` or behind a proxy — never plaintext to the
internet.

### BTP v2 (`btp`) — Bad-Proxy-Transport, this project's native protocol

The private protocol. It authenticates the header with HMAC-SHA256 and
randomizes the first segment's length, then streams the payload. Exact wire
layout both directions agree on:

```
+--------+-----------+-----------+-----------+-----+-----+------+------+---------+
| HMAC   | confusion | confusion | timestamp | dir | len | host | port | payload |
| digest | length    | bytes     |           |     |     |      | (2B) | (raw)   |
+--------+-----------+-----------+-----------+-----+-----+------+------+---------+
|   32   |     1     |   0-64    |     4     |  1  |  1  | var  |  2   | stream  |
```

| field | size | meaning |
|---|---|---|
| HMAC digest | 32 | `HMAC-SHA256(secret, signed region)`; prefix so it can be checked before the rest is trusted |
| confusion length | 1 | `0`-`64`, chosen uniformly at random; bounds how many filler bytes follow |
| confusion bytes | 0-64 | random filler; **included** in the signed region |
| timestamp | 4 | unix seconds, big-endian; the client jitters it ±30 s so clock skew is not a fingerprint |
| dir | 1 | direction byte; carried inside the signed region but not acted on (BPC relays both ways) |
| host length | 1 | `1`-`255` |
| host | var | target hostname |
| port | 2 | target port, big-endian |
| payload | stream | the relayed bytes, after the header |

* **Authentication.** The signed region is everything after the 32-byte digest
  (confusion length + confusion + timestamp + dir + host length + host + port).
  The receiver recomputes `HMAC-SHA256(secret, signed region)` and compares in
  constant time. The secret itself never appears on the wire.
* **Replay protection.** The timestamp must fall within ±210 s of the server
  clock, and the hex digest is stored in a shared LRU cache of 210 000 entries;
  a digest that repeats is rejected. So a captured handshake fails once it ages
  out of the window or is seen twice.
* **Confusion.** The random `0-64` filler makes the first segment's length vary
  per connection, so the header does not have a fixed byte signature.
* **Payload integrity.** After the header, bytes stream unauthenticated —
  integrity is delegated to the transport (TLS), the same trade-off
  VLESS and Trojan make.
* **Deny by default.** An inbound configured with no secret authenticates
  nobody, so it never reports a successful handshake.
* **v2 is not compatible with v1.** v1 authenticated "header + whatever arrived
  in the first read", which broke whenever TCP fragmented the segment (and made
  the receiver's buffer length part of the authentication input). v2 binds the
  header only and streams cleanly.
* **Fallback.** A failed handshake whose first bytes look like a plain HTTP GET
  is answered with an innocuous decoy page; the probe is detected before any
  header parsing, so the server recognises a web scanner on its port without
  blocking.

Client outbound: `"protocol": "btp"` with `secret` = the HMAC key, and any
transport. See [protocol source](protocols/btp.go) for the reference
implementation.

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
bytes, the response body server→client. Over plain TCP (the loopback hop behind
a proxy) it is HTTP/1.1 chunked duplex; over TLS the stdlib negotiates HTTP/2 by
ALPN and every tunnel is one h2 stream on a shared connection. The server
answers 403 to any other path/method, so it coexists with static content.

Notes on behaviour found the hard way:

* `WriteHeader` normally drains and closes the request body, so the handler
  must call `ResponseController.EnableFullDuplex()` before relaying.
* `http.Protocols` needs `SetHTTP1(true)` **and** `SetUnencryptedHTTP2(true)`
  or plain-HTTP h2 probes RST existing HTTP/1.1 connections.
* The conn's `SetDeadline` is wired to the response controller's
  `SetReadDeadline`/`SetWriteDeadline`, so the handshake timeout applies to an
  xhttp connection exactly as it does to a raw TCP one.
* The client sends its downstream read lazily (the round trip runs in its own
  goroutine and the response is resolved on first read), so a proxy that
  buffers response headers until the first byte does not deadlock the
  handshake.

xhttp follows Xray's *design*, not its byte-level handshake: sessions are
identified by `X-Session-Id`, there is no `__xhttp_mode` sub-path negotiation,
and the path is matched as a prefix, so it is **not interoperable with xray's
`packet-up`/`auto` modes** (those send a GET session-probe that a BPC server
answers `403`). It *is* usable from a real Xray client, but only when the client
pins **`"mode": "stream-one"`**, the one long-lived full-duplex POST that BPC
implements — verified against Xray v26.3.27 (`XHTTP is dialing … mode stream-one`
→ 200 relay). VLESS, Trojan and BTP payloads ride it fine between two BPC
endpoints, and the prefix match is what lets a prefix-stripping reverse proxy
(`/bp` → `/bp/<session-id>`) reach it.

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
      "users": ["01234567-89ab-4cde-8f01-23456789abcd"],
      "transport": { "type": "xhttp", "path": "/xrt" }
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

Client config: `transport: { type: xhttp, tls: true, path: /xrt, authority:
proxy.example.com }`, `host: proxy.example.com`, `port: "443"`. The
client↔nginx hop is real TLS (h2 or h1 by ALPN); nginx↔BPC is plaintext
loopback, so `X-Sec-Mode`/`X-Session-Id` headers terminate cleanly and
VLESS/Trojan/BTP handshakes run over the tunnel body.

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

## Deploying behind Caddy

Caddy works the same way and is the topology actually in production here. The
one non-obvious setting is `flush_interval -1`, which tells Caddy's
`reverse_proxy` not to buffer the response — the equivalent of nginx's
`proxy_buffering off` — so the duplex POST streams in both directions.

BPC inbounds, plain xhttp on loopback (no `cert`/`key`; Caddy terminates TLS):

```json
{
  "inbounds": [
    { "protocol": "vless", "host": "0.0.0.0", "port": "8443",
      "users": ["01234567-89ab-4cde-8f01-23456789abcd"],
      "transport": { "type": "xhttp", "path": "/bp" } },
    { "protocol": "btp", "host": "0.0.0.0", "port": "8444",
      "secret": "change-me-btp-secret",
      "transport": { "type": "xhttp", "path": "/btp" } }
  ],
  "outbounds": [ { "protocol": "freedom" } ]
}
```

Caddyfile site block (the proxy shares a host with a normal site, so the tunnel
paths are matched before the catch-all):

```caddy
admin.example.com {
    tls me@example.com
    encode gzip

    # tunnel endpoints — BPC listens plain HTTP inside the compose network;
    # Caddy terminates TLS and streams the single duplex POST with
    # flush_interval -1. The matcher also covers the sub-path so Xray's
    # /bp/<session-id> requests route here too (BPC matches the prefix).
    @bp  path /bp /bp/*
    handle @bp {
        reverse_proxy http://bad_proxy:8443 { flush_interval -1 }
    }

    @btp path /btp /btp/*
    handle @btp {
        reverse_proxy http://bad_proxy:8444 { flush_interval -1 }
    }

    handle_path /* {          # your real site, served last
        root * /srv
        file_server
        try_files {path} {path}/ /index.html
    }
}
```

The same `flush_interval -1` applies whether the backend hop is plaintext (as
above, over the compose network) or TLS. Client config matches the nginx
client: `transport: { type: xhttp, tls: true, path: /bp, authority:
admin.example.com }` pointing at the host and port 443.

## Repository layout

```
main.go             CLI entry: flags, startup, signal handling (one binary,
                    role from config; only SIGINT/SIGTERM stop it)
core/               engine: settings.go (GeneralConfig + defaults), inbound.go
                    and outbound.go (config structs, TransportConfig, handshake
                    wiring), core.go (relay loop, routing, idle watchdog)
protocols/          one file per handshake, inbound + outbound together:
                    protocol.go (NewInbound/NewOutbound + protocol names),
                    vless.go trojan.go btp.go socks.go http.go (client-facing)
                    free.go (freedom direct outbound)
transport/          how bytes reach the peer: tcp.go, tls.go, xhttp.go,
                    transport.go (options + factory)
router/             rule matching: full / domain / keyword / regexp, and
                    named geosite/geoip lists loaded from rules.dat
common/             cross-cutting helpers: Buffered + target codec,
                    Aho-Corasick automaton, LRU replay cache, ring queue, and
                    the process-wide defaults (timeouts, buffer, padding)
logger/             levelled logging to a file sink + console sink
deploy/             extra example configs (two-host btp/tls, vless/xhttp)
config*.json        runnable examples: nginx/Caddy server, client, the
                    end-to-end test matrix, and Xray interop
```

Relay lifecycle: accept → handshake deadline (`handshake_timeout`) → protocol
`Connect()` (success: target address; failure: `Fallback()` answers probes) →
deadline cleared → two copy directions pumping through pooled `relay_buffer_kib`
buffers. The copy deliberately hides the `ReadFrom`/`WriteTo` splice interfaces:
on Windows the `TCPConn→TCPConn` TransmitFile path measured ~80 MB/s on loopback
where the plain buffered loop passes ~400 MB/s (and the pooled buffer means zero
relay allocations per tunnel).

## Logging

Every line carries a timestamp, the caller's `file:line`, the function name, and
a `[LEVEL]` tag, and is filtered into two sinks set from `general`:

* **file** (`log_file`, level `log_level`, default `debug`) — the detailed
  record; no file is opened unless `log_file` is set.
* **console/stdout** (level `console_level`, default `info`) — the live view;
  raise it to `warn`/`error` to keep only critical lines.

`debug` routine lines: a failed inbound handshake — probes, wrong password,
unknown UUID, bad HMAC, timeouts — logs at debug so port scanners cannot flood
the console. `info`: startup and listener lifecycle. `warn`: a failed upstream
dial, a copy error that is not an expected teardown. `error`: a broken inbound
that disabled itself, a relay panic. VLESS failures never log the UUID.

There are deliberately **no metrics counters**: BPC does not instrument itself;
what you need is in the levelled log.

## Testing

`go test ./...` covers the per-package units plus the xhttp duplex round trip
and a raw HTTP probe of the server (all green). The repo root also contains a
ready-made end-to-end matrix (loopback origin on `127.0.0.1:18000`, no external
network needed):

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

Verified matrix (payload checked end to end):

| app protocol | transport | socks5h | http proxy |
|---|---|:-:|:-:|
| vless | xhttp (plain, chunked duplex) | ✅ | ✅ |
| trojan | xhttp | ✅ | ✅ |
| btp v2 | tcp | ✅ | ✅ |

Wrong-credential handshakes are rejected (`password mismatch`, unknown UUID,
bad HMAC) and never relay origin bytes. A plain HTTP GET against the BTP port
is answered immediately with the innocuous fallback page (the probe is detected
before any header parsing), and the xhttp inbounds reply `403` to every path
other than their own.

### Interoperability with real Xray

BPC's VLESS and Trojan handshakes are Xray-shaped, so the two interoperate over
the `tcp` and `tls` transports (xhttp is BPC-only). Tested against Xray
v26.3.27 on loopback (origin on `127.0.0.1:18000`):

| direction | app protocol | transport | result |
|---|---|---|---|
| BPC client → Xray server | vless | tcp | ✅ payload-checked |
| Xray client → BPC server | vless | tcp | ✅ payload-checked |
| BPC client → Xray server | vless | tls | ✅ payload-checked |
| Xray client → BPC server | vless | tls | ✅ payload-checked |

Below are concrete configs for both directions and both protocols. The UUID /
password values must match across the two ends.

#### VLESS: Xray client → BPC server

BPC server (`config_xray_server_b.json` shape, tcp):

```json
{
  "inbounds": [
    { "protocol": "vless", "host": "127.0.0.1", "port": "15444",
      "users": ["01234567-89ab-4cde-8f01-23456789abcd"],
      "transport": { "type": "tcp" } }
  ],
  "outbounds": [ { "protocol": "freedom" } ]
}
```

Matching Xray client (`inbounds` = a local SOCKS for your apps, `outbounds` =
VLESS to BPC):

```json
{
  "inbounds": [
    { "listen": "127.0.0.1", "port": 1080, "protocol": "socks",
      "settings": { "udp": true } }
  ],
  "outbounds": [
    { "protocol": "vless",
      "settings": { "vnext": [ {
        "address": "127.0.0.1", "port": 15444,
        "users": [ { "id": "01234567-89ab-4cde-8f01-23456789abcd", "encryption": "none" } ]
      } ] },
      "streamSettings": { "network": "tcp", "security": "none" } }
  ]
}
```

#### VLESS: BPC client → Xray server

Xray server — inbound is VLESS over a TLS `tcp` transport:

```json
{
  "inbounds": [
    { "listen": "127.0.0.1", "port": 15446, "protocol": "vless",
      "settings": { "clients": [ { "id": "01234567-89ab-4cde-8f01-23456789abcd" } ], "decryption": "none" },
      "streamSettings": {
        "network": "tcp", "security": "tls",
        "tlsSettings": { "certificates": [
          { "certificateFile": "/xray/tls/cert.pem", "keyFile": "/xray/tls/key.pem" } ] } } }
  ],
  "outbounds": [ { "protocol": "freedom" } ]
}
```

BPC client (`config_xray_client_tls.json`):

```json
{
  "inbounds": [ { "protocol": "socks", "host": "127.0.0.1", "port": "15086" } ],
  "outbounds": [
    { "protocol": "vless",
      "secret": "01234567-89ab-4cde-8f01-23456789abcd",
      "host": "127.0.0.1", "port": "15446",
      "transport": { "type": "tls", "authority": "localhost" } }
  ]
}
```

Note the `"type": "tls"` on the client already means "dial with TLS" — there is
**no** `insecure`, so the certificate Xray presents must be trusted by the OS
(or connect over plaintext `tcp` for a throwaway test). `authority` sets the SNI
and must match a name in that certificate.

#### Trojan: BPC server ← Xray client

Trojan needs real TLS (the password digest would otherwise leak in clear), so
the shortest safe path is Xray-terminating-TLS in front of, or BPC serving TLS
with a trusted cert. BPC server inbound:

```json
{
  "inbounds": [
    { "protocol": "trojan", "host": "0.0.0.0", "port": "443",
      "secret": "change-me-trojan-password",
      "transport": { "type": "tls",
        "cert": "/etc/bad-proxy/server.crt", "key": "/etc/bad-proxy/server.key" } }
  ],
  "outbounds": [ { "protocol": "freedom" } ]
}
```

Xray client:

```json
{
  "inbounds": [ { "listen": "127.0.0.1", "port": 1080, "protocol": "socks" } ],
  "outbounds": [
    { "protocol": "trojan",
      "settings": { "password": "change-me-trojan-password", "email": "u", "level": 0 },
      "streamSettings": { "network": "tcp", "security": "tls",
        "tlsSettings": { "serverName": "proxy.example.com" } },
      "mux": { "enabled": false } }
  ]
}
```

Set the Xray `address`/`port` on the outbound to the BPC host and 443 (the
`vnext`-style fields for trojan live on the outbound as `"address"`/`"port"`
beside `settings`).

#### Trojan: Xray server ← BPC client

Xray server (trojan over its own TLS):

```json
{
  "inbounds": [
    { "protocol": "trojan", "port": 443,
      "settings": { "clients": [ { "password": "change-me-trojan-password" } ] },
      "streamSettings": { "network": "tcp", "security": "tls",
        "tlsSettings": { "certificates": [
          { "certificateFile": "/xray/tls/cert.pem", "keyFile": "/xray/tls/key.pem" } ] } } }
  ],
  "outbounds": [ { "protocol": "freedom" } ]
}
```

BPC client — `type: tls`, `authority` = a name in the cert, trusted cert (no
`insecure`):

```json
{
  "inbounds": [ { "protocol": "socks", "host": "127.0.0.1", "port": "1080" } ],
  "outbounds": [
    { "protocol": "trojan", "secret": "change-me-trojan-password",
      "host": "proxy.example.com", "port": "443",
      "transport": { "type": "tls", "authority": "proxy.example.com" } }
  ]
}
```

#### Interop caveats

* **VLESS carries no encryption of its own.** Plain `tcp` is cleartext (the UUID
  and target are readable on the wire), so for real use pick `tls` or put it
  behind a TLS-terminating proxy.
* **No `insecure` on the BPC side.** Current Xray builds removed `allowInsecure`
  in favor of `pinnedPeerCertSha256`, and BPC likewise dropped its `insecure`
  option — the client always verifies the server certificate. So a `tls` interop
  test needs a CA the client trusts (or a pin on the Xray side); self-signed
  setups are easiest to drive over `tcp`.
* **Address/port ordering.** Trojan uses the SOCKS5 layout (atyp / address /
  port), whereas VLESS puts the port **before** the address. Both directions
  match Xray.
* **UDP** is unsupported in both protocols (see [limitations](#notes-and-limitations)).
* **BTP and xhttp are native** — there is no Xray counterpart for either, so
  they only interop BPC↔BPC.

### Throughput vs Xray (measured)

Windows loopback, origin `python -m http.server`, 200 MiB file per stream,
BPC↔BPC vs Xray↔Xray client/server pairs on the same machine, same UUID, plain
VLESS. Single stream: 5 runs, max and median of curl's bytes/s. Concurrent: 4
parallel streams, aggregate.

| path | single max | single median | 4-stream aggregate |
|---|---|---|---|
| (no proxy, ceiling) | 1503 MB/s | 1436 MB/s | 2100 MB/s |
| BPC vless/tcp | **134** | **111** | **624** |
| Xray vless/tcp | 117 | 82 | 453 |
| BPC vless/tls | **414** | **360** | **1359** |
| Xray vless/tls | 322 | 316 | 1082 |

BPC stays ahead of Xray on every measured path (~15-38% higher throughput); TLS
beats plain TCP in both products on this machine — cleartext loopback hops appear
to pay an inspection cost that opaque TLS traffic skips.

## Notes and limitations

* No UDP relay (SOCKS `UDP ASSOCIATE`, VLESS `cmd=2`, Trojan `0x03` return
  errors). QUIC/Hysteria-style datagram transports are out of scope.
* xhttp is BPC↔BPC only (see the xhttp note above); for Xray interop use app
  protocol VLESS (or Trojan) over the `tcp` or `tls` transport — verified both
  directions against Xray v26.3.27 in the matrix above.
* Bind `socks`/`http` inbounds to loopback — they are unauthenticated by design.
* BTP v1 clients cannot connect to BTP v2 servers (see above).
