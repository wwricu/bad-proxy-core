# 部署配置：Xray(v2ray) + BadProxy，VLESS+xhttp 与 BTP

本目录是 admin.wwr.icu 的实际部署配置副本。`*.json` 已在 `.gitignore` 中，含密钥/UUID，不会被提交。

## 拓扑一览

| 应用 | 协议/传输 | 服务监听 | 对外入口 | 客户端连接 |
|------|-----------|----------|----------|------------|
| Xray (v2ray) | vless / xhttp | `127.0.0.1:8446` (明文，路径 `/vx`) | Caddy `admin.wwr.icu:443` → `handle /vx/*` | `https://admin.wwr.icu/vx` (TLS 由 Caddy 终结) |
| BadProxy | vless / xhttp | `0.0.0.0:8447` (自带 TLS，路径 `/bx`) | 直连 `admin.wwr.icu:8447` | `admin.wwr.icu:8447` (TLS/ALPN h2) |
| BadProxy | btp / tls | `0.0.0.0:8448` (自带 TLS) | 直连 `admin.wwr.icu:8448` | `admin.wwr.icu:8448` (TLS/ALPN h2) |

关键点：**为什么 BPC 不走 Caddy**。之前实测 Xray↔Xray 的 xhttp 经 Caddy `/vx` 正常（返回 204），但 BPC 的 xhttp 是“一条无限 duplex POST、响应头先于请求体 flush”的模式，Caddy 会卡住请求体不转发（`dial_ok=0`）。所以 BPC 的 xhttp 与 btp 都用自带的 `tls` 传输直接监听独立端口，绕开 Caddy。Xray 保持走 Caddy。

## 服务端（在 admin.wwr.icu）

先准备 BPC 的证书（需要一张对 `admin.wwr.icu` 有效的证书+私钥，可从 Caddy 的证书目录复制，或用 certbot 另签一份；确保 bpc 进程可读）：

```
mkdir -p /home/wwr/bp/tls
# 放置 /home/wwr/bp/tls/fullchain.pem 和 /home/wwr/bp/tls/privkey.pem
```

三个服务进程：

```
# 1. Xray vless/xhttp，Caddy 反代 /vx/* → 127.0.0.1:8446
xray run -c xray_vless_xhttp_server.json

# 2. BadProxy vless/xhttp，自带 TLS，直连 :8447
bad-proxy-core --config bpc_vless_xhttp_server.json

# 3. BadProxy btp/tls，自带 TLS，直连 :8448
bad-proxy-core --config bpc_btp_server.json
```

Caddyfile 需要（沿用现有 `/gp` v2ray trojan 不动）：

```
admin.wwr.icu {
    handle /vx/* {
        reverse_proxy 172.18.0.1:8446 { flush_interval -1 }
    }
    # ... 现有 /gp 等保持不变
}
```

防火墙/安全组放行 8447、8448（这两个是 BPC 直连端口，不经 443）。

## 客户端（本地）

```
# Xray 客户端：本地 socks 1080 / http 1081 → https://admin.wwr.icu/vx
xray run -c xray_vless_xhttp_client.json

# BadProxy 客户端：本地 socks 1082 / http 1083 → admin.wwr.icu:8447
bad-proxy-core --config bpc_vless_xhttp_client.json

# BadProxy 客户端：本地 socks 1084 / http 1085 → admin.wwr.icu:8448
bad-proxy-core --config bpc_btp_client.json
```

三个客户端的本地入站端口已错开，可同时运行。

## 互通性说明

- Xray 客户端 ↔ BPC 服务端：**xhttp 不互通**（BPC 的 xhttp 与 Xray 的 XHTTP 线格式不同）。要跨实现，用 `tcp` 传输的 vless（实测 xray.exe → BPC vless server 经 plain TCP 成功）。
- BPC↔BPC 同协议（vless/xhttp、btp/tls）互通，已在重构后的双进程矩阵中验证。

## 密钥

| 项 | 值 |
|----|----|
| Xray VLESS UUID | `0164b316-8c83-46df-a134-7e684b45c0fa` |
| BPC VLESS UUID | `7c9eafe2-3901-490a-9c7b-9e3e8cb0881f` |
| BPC BTP secret | `39b64e0ca901db1f94009e5129031128e3a0` |

如需轮换，服务端和对应客户端两边同改。
