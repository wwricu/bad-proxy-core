# 部署：BadProxy 经 Caddy 反代提供服务（vless+xhttp、btp+xhttp）

本目录是线上部署说明的副本。真正生效的编排文件在独立仓库 `wwricu/deploy`（`docker-compose.yml` + `bad_proxy/config.json`）与 `wwricu/admin-page`（`Caddyfile`）。这里的 `*.json` 已在 `.gitignore` 中，含密钥/UUID，不会被提交；本文档也不再写任何真实凭据。

## 拓扑一览

后台只有 **Caddy 处理 HTTPS/TLS**。BadProxy（BPC）在 compose 网络里跑**明文 HTTP**，由 Caddy 在 443 上按路径反代，套在正常网站后面做隐蔽。

| 应用 | 协议/传输 | BPC 监听（明文） | 对外入口 |
|------|-----------|------------------|----------|
| BadProxy | vless / xhttp | `bad_proxy:8443`，路径 `/bp` | Caddy `:443` → `handle /bp /bp/*` |
| BadProxy | btp / xhttp | `bad_proxy:8444`，路径 `/btp` | Caddy `:443` → `handle /btp /btp/*` |
| v2ray | trojan | （既有服务） | Caddy `:443` → `handle /gp`，保持不动 |

Caddyfile 反代块（关键是 `flush_interval -1`，让响应头立即透传）：

```
@bp path /bp /bp/*
handle @bp { reverse_proxy { to http://bad_proxy:8443  flush_interval -1 } }

@btp path /btp /btp/*
handle @btp { reverse_proxy { to http://bad_proxy:8444  flush_interval -1 } }
```

BPC 侧 `config.json` 用**占位符**，部署时由 deploy 仓库的 workflow 从 GitHub Secrets 替换真实 UUID：

```json
{ "protocol": "vless", "port": "8443", "users": ["{TROJAN_UUID}"], "transport": { "type": "xhttp", "path": "/bp" } }
{ "protocol": "btp",   "port": "8444", "secret": "{BTP_UUID}",     "transport": { "type": "xhttp", "path": "/btp" } }
```

## 为什么 BPC 的 xhttp 现在能走反代

早期结论“BPC 必须自带 TLS 直连、不能过 Caddy”已被推翻。两个改动让它能在反代后服务：

1. **服务端**：响应头（`200` + `Flush()`）在读取请求体之前就写出，并把配置的 `path` 按**前缀**匹配。Xray 的 XHTTP 客户端会在会话里追加子路径（如 `/bp/<session-id>`），Caddy 会把整条路径转发过来，前缀匹配才能互通。
2. **客户端**：`Dial` 不再同步阻塞等待响应头。缓冲型反代（Caddy）会扣住上游已 flush 的 200，直到请求体开始吐字节；若客户端先等 200 再写握手就会死锁。现在握手字节立即通过管道写出，响应在首次 `Read` 时惰性解析，非 200 作为读错误拆链。

回归测试 `TestXhttpBufferingProxy`（transport 包）锁定此行为。

## 凭据与轮换

真实 UUID/HMAC secret 只存在于 GitHub Secrets（`V2RAY_UUID`）与服务器上的 `/data/deploy/.env`，不进仓库。轮换时在 deploy 侧改 secrets，重新跑部署 workflow 即可。

> 注：历史提交里曾把示例配置写成过真实 UUID。若该 UUID 仍在线上使用，建议轮换（GitHub 仓库为 public，旧 commit 已对外可见，改写当前文件不会抹去历史）。

## 互通性说明

- BPC 服务端 ↔ Xray 客户端：经 Caddy 反代时，xhttp 需满足上面的前缀匹配 + 惰性响应条件（已在现网验证 xray→`/bp`、badproxy→`/bp`/`/btp` 均通）。
- BPC↔BPC 同协议（vless/xhttp、btp/xhttp）互通，双进程矩阵已验证。
- 跨实现的纯 TCP vless 亦可用（BPC 未实现 Xray 的 XHTTP-over-HTTP/2 全部细节，跨实现优先选简单传输）。
