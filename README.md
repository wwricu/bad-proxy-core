# Bad Proxy GO

Bad proxy go (BPG) is the golang version of [Bad Proxy](https://github.com/HerrKKK/bad_proxy), a primitive proxy tool to penetrate firewalls.

## Features

BPG works mainly with Bad Transfer Protocol (BTP) to protect connections being detected, they have features below: 

1. Authentication and integrity
   BPG is able to verify sender's identity as well as the integrity of messages it received.
2. Anti-replay-attack (instant replay)
   BPG is able to detect a replay attack transiently after a message received.
3. Anti-replay-attack (Long-delay replay)
   BPG is able to detect a replay attack long while after a message received.
4. TLS-based encryption
   BPG uses trivial TLS to encrypt data.
5. Trivial response and Fallback
   BPG could response with trivial HTTP message on any attack detected.
6. Random message length
   A confusion is added to make the length of message random to avoid

Protocol supported by BPG is listed below:

### Application layer protocol

1. BTP ("btp")
2. HTTP ("http")
3. SOCKS5 ("socks")
4. FREEDOM ("freedom", outbound only)

#### Bad Transfer Protocol (BTP)

BTP is a simple stateless protocol running at 7th layer.

| 32 Bytes | 1 Bytes          | X Bytes                 | 4 Bytes            | 1 Bytes   | 1 Bytes     | Y Bytes        | 2 Bytes | Z bytes |
|----------|------------------|-------------------------|--------------------|-----------|-------------|----------------|---------|---------|
| digest   | confusion length | confusion (0 <= X < 64) | utc timestamp +-30 | directive | host length | host (Y < 254) | port    | payload |


### Transport layer protocol
1. TCP ("tcp")
2. TLS ("tls")
3. Websocket ("ws" and "wss")

## Compilation and deployment

```
go build -o bad_proxy.exe
```

if you want to discard windows gui, use `go build -o bad_proxy.exe -ldflags "-s -w -H=windowsgui" .`

### Run application
`./bad_proxy --config <json config path> --router-path <.dat rule file path>`

### Build domain rule file "rule.bat"
`./bad_proxy build --rule-path <rule directory of v2fly repository path> --router-path <output binary routing file path>`


### Deploy with nginx

#### Client setting
```
{
  "inbounds": [
    {
      "host": "0.0.0.0",
      "port": "8080",
      "protocol": "http"
    }
  ],
  "outbounds": [
    {
      "secret": "uuid",
      "host": "<nginx domain>",
      "port": "<nginx port>",
      "protocol": "btp",
      "transport": "wss",
      "ws_path": "/path"
    }
  ]
}
```

#### Server setting
```
{
  "inbounds": [{
    "secret": "uuid",
    "host": "0.0.0.0",
    "port": "<bad_proxy port>",
    "protocol": "btp",
    "transport": "ws",
    "ws_path": "/path"
  }],
  "outbounds": [{
    "protocol": "freedom"
  }]
}
```


#### Nginx server setting
```
location /path {
   proxy_redirect off;
   absolute_redirect off;
   proxy_pass http://<bad_proxy address>:<bad_proxy port>;
   proxy_http_version 1.1;
   proxy_set_header Upgrade $http_upgrade;
   proxy_set_header Connection "upgrade";
   proxy_set_header Host $host;
   proxy_set_header X-Real-IP $remote_addr;
   proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```


## Settings

There are two mandatory options in BPG setting: "inbounds" and "outbounds",
to set up fundamental functionalities of BPG, and "routers" is optional,
to specify which outbound BPG should route to.

### Inbounds

Inbound specify the listening address, port, protocol and others.
A basic inbound setting is show below:

Example:
```json
{
 "host": "0.0.0.0",
 "port": "8080",
 "protocol": "http"
}
```

When using "btp" as protocol, "secret" shall be specified,
when using "ws" or "wss" as transport, "ws_path" shall be specified.

Example:
```json
 {
   "secret": "uuid",
   "host": "domain",
   "port": "port",
   "protocol": "btp",
   "transport": "wss",
   "ws_path": "/path"
 }
```

When using "tls" as transport protocol, you shall specify "tls_cert_path" and "tls_key_path"

Example:
```json
{
  "host": "0.0.0.0",
  "port": "8080",
  "protocol": "tls",
  "tls_cert_path": "<tls certificate path>",
  "tls_key_path": "<tls private key path>"
}
```

After deciding all inbounds, you can compose all of them to an array,
to listening to all address/ports parallel.

Example:
```
"inbounds": [
 {
   "host": "0.0.0.0",
   "port": "8080",
   "protocol": "http"
 },
 {
   "host": "0.0.0.0",
   "port": "8081",
   "protocol": "socks"
 }
]
```

### Outbounds

Outbounds are quite same as inbound, there are two difference.
1. Outbound protocol can be specified as "freedom", which means they just forward messages directly.
2. "tag"
   outbound can be assigned a tag as a target of router, if the "tag" is omitted,
   the outbound has the tag "" (0 length string). Furthermore, "tag" shall not be duplicate.

Example:
```
"outbounds": [
 {
   "secret": "uuid",
   "host": "domain",
   "port": "port",
   "protocol": "btp",
   "transport": "wss",
   "ws_path": "/path"
 },
 {
   "tag": "direct",
   "protocol": "freedom"
 }
]
```

### router

router controls the data flow; Currently there are only global routers,
which means data from all inbounds will pass through all routers to select an outbound.
Users should set up the "tag" and "rules" in a router, "tag" means all data matches with the router will be redirected to the outbound with that tag,
"rule" has below formats:
1. "full:<full domain>" (exactly matches the full name of the domain)
2. "domain:<domain>" (matches all sub domains under this domain)
3. "regexp:<regular expression>" (matches domains with regular expression)
4. "rule:<filename in [v2fly domain list](https://github.com/v2fly/domain-list-community)"
   (specify a filename in v2fly domain list, all rules in that file will be loaded)

Example:
```
"routers": [
 {
   "tag": "direct",
   "rules": [
     "domain:github.com",
     "full:www.github.com",
     "rule:cn"
   ]
 }
]
```

### Complete examples

Typical client example:
```json
{
  "inbounds": [
    {
      "host": "0.0.0.0",
      "port": "8080",
      "protocol": "http"
    },
    {
      "host": "0.0.0.0",
      "port": "8081",
      "protocol": "socks"
    }
  ],
  "outbounds": [
    {
      "secret": "uuid",
      "host": "domain",
      "port": "port",
      "protocol": "btp",
      "transport": "wss",
      "ws_path": "/path"
    },
    {
      "tag": "relay",
      "host": "domain",
      "port": "port",
      "protocol": "tls"
    },
    {
      "tag": "direct",
      "protocol": "freedom"
    }
  ],
  "routers": [
    {
      "tag": "direct",
      "rules": [
        "domain:github.com",
        "full:www.github.com",
        "rule:cn"
      ]
    }
  ]
}
```

Typical server example:

```json
{
  "inbounds": [
    {
      "secret": "uuid",
      "host": "domain",
      "port": "port",
      "protocol": "btp",
      "transport": "wss",
      "ws_path": "/path"
    },
    {
      "host": "domain",
      "port": "port",
      "protocol": "tls",
      "tls_cert_path": "<tls certificate path>",
      "tls_key_path": "<tls private key path>"
    }
  ],
  "outbounds": [
    {
      "tag": "direct",
      "protocol": "freedom"
    }
  ]
}
```
