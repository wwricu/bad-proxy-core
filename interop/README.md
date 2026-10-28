# xhttp and http2 interop matrices

Proves the claims in [README](../README.md): that BPC's `xhttp` transport really
speaks Xray's XHTTP (both directions, all four modes) and that `http2` speaks
Xray's network `http`. Nothing else in the test suite can show this — the Go
tests exercise BPC against itself.

## Running it

Windows only (it stops the pair with `taskkill`). Needs an Xray binary; the one
this was proved against is **26.3.27**.

```sh
XRAY=C:/path/to/xray.exe ./run.sh          # all 25 cases
XRAY=... ONLY="B packet-up" ./run.sh       # one case
```

`XRAY` may be left out if the binary sits at `interop/xray/xray.exe` (ignored by
git, like the rest of that directory). The script builds `bpc.exe` and
`origin.exe` itself into `interop/work/` and writes every config, log and
fixture there.

It binds `127.0.0.1` on ports 15600-15624, 18080 and 18600-18624, so nothing may
be listening on them when a run starts. The script kills any leftover
`xray.exe`/`bpc.exe`/`origin.exe` before case 1 and after each case, and a case
that leaves a listener behind shows up as the *next* case failing to bind.

## What a case is

Each case starts a server and a client, then pushes **300 KiB down and 300 KiB
up** through the tunnel over a SOCKS port and compares both byte for byte:

* `/big` returns a repeating 26-byte pattern, so a dropped or reordered piece
  shows up as a mismatch, not as a shorter file nobody noticed;
* `/echo` returns `len=N` plus the body it got, so a truncated upload cannot
  pass by accident.

A mode that survives a 4-byte handshake and loses 300 KiB is not a working mode,
which is why the payload is large and checked rather than small and assumed.

## The four groups

| group | server | client | why it is here |
|---|---|---|---|
| A | Xray | BPC | what an Xray hub must accept |
| B | BPC | Xray | what a BPC hub must give a real Xray client |
| C | BPC | BPC | the assembled core, not only the transport |
| D | Xray | Xray | **the control** |

D is the one that makes the other three readable. Xray against itself, with no
padding configured anywhere, must pass: if a B case fails while its D twin
passes, the fault is in BPC; if both fail, the fault is in the harness or in the
mode pairing, and no core change can fix it. Skipping D is how a broken matrix
gets mistaken for a broken transport.

Each mode is tried pinned to itself and against an `auto` hub, in both
directions, plus the unset/default client mode — 25 cases, and the expected
result is `=== 25 passed, 0 failed ===` with exit 0.

## The http2 matrix

`run_http2.sh` is the same idea for the `http2` transport (Xray's network
`http`), with **VLESS and Trojan** as the application layer: `A` BPC client ->
Xray hub over h2c, `B` Xray client -> BPC hub over TLS+`h2` (Xray's client
refuses the transport without TLS, so the harness issues a throwaway
certificate and sets `allowInsecure`), `C` BPC <-> BPC, and `D` the Xray <-> Xray
control. Eight cases, expected `=== 8 passed, 0 failed ===`.

Xray **v1.8.24** is the binary this was proved against; newer Xray majors no
longer ship the `http` transport at all.

## Why this is not in CI

CI would have to download Xray on every push and run a loopback proxy matrix, and
the flake it would produce (ports, process teardown, a third-party binary's
behaviour changing under us) is worse than no gate. Run it by hand whenever the
`xhttp` transport changes, and run it three times in a row before believing a
green result.
