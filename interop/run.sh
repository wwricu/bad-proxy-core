#!/usr/bin/env bash
# xhttp interop matrix: BPC <-> Xray in both directions, BPC <-> BPC, and Xray
# <-> Xray as the control. Every case carries a 300 KiB download and a 300 KiB
# upload through the tunnel and compares both byte for byte, because a mode that
# only works for a 4-byte handshake is not a mode.
#
# Windows only (it stops the pair with taskkill). Needs an Xray binary:
#
#	XRAY=/path/to/xray.exe ./run.sh
#
# Put one at interop/xray/xray.exe and it is found without the variable. The
# version this matrix was proved against is 26.3.27.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="$HERE/work"
XRAY="${XRAY:-$HERE/xray/xray.exe}"
UUID=01234567-89ab-4cde-8f01-23456789abcd
ORIGIN=18080

if [ ! -x "$XRAY" ]; then
	echo "no Xray binary at $XRAY - set XRAY=/path/to/xray.exe"
	exit 1
fi
mkdir -p "$WORK"
cd "$WORK" || exit 1
rm -f *.log *.json *.bin bpc.exe origin.exe

# One Xray instance per case, killed by image name between cases; the same is
# true of bpc, so nothing may be listening on these ports when the run starts.
MSYS_NO_PATHCONV=1 taskkill /IM xray.exe /F >/dev/null 2>&1
MSYS_NO_PATHCONV=1 taskkill /IM bpc.exe /F >/dev/null 2>&1
MSYS_NO_PATHCONV=1 taskkill /IM origin.exe /F >/dev/null 2>&1

echo "building bpc and origin"
(cd "$HERE/.." && go build -o "$WORK/bpc.exe" .) || exit 1
(go build -o "$WORK/origin.exe" "$HERE/") || exit 1

kill_procs() {
	MSYS_NO_PATHCONV=1 taskkill /IM xray.exe /F >/dev/null 2>&1
	MSYS_NO_PATHCONV=1 taskkill /IM bpc.exe /F >/dev/null 2>&1
	sleep 0.5
}

# ---------------------------------------------------------------- fixtures
./origin.exe $ORIGIN >origin.log 2>&1 &
sleep 1
curl -s --max-time 10 "http://127.0.0.1:$ORIGIN/big" -o ref_big.bin || { echo "origin down"; exit 1; }
head -c 307200 /dev/zero | tr '\0' 'q' >up.bin
{ printf 'len=%d\n' "$(wc -c <up.bin)"; cat up.bin; } >ref_echo.bin
echo "fixtures: big=$(wc -c <ref_big.bin) echo=$(wc -c <ref_echo.bin)"

probe() { # $1 = socks port
	local port=$1
	curl -s --max-time 25 -x "socks5://127.0.0.1:$port" "http://127.0.0.1:$ORIGIN/big" -o got_big.bin || { echo "download failed"; return 1; }
	cmp -s got_big.bin ref_big.bin || { echo "download corrupted"; return 1; }
	curl -s --max-time 25 -x "socks5://127.0.0.1:$port" "http://127.0.0.1:$ORIGIN/echo" --data-binary @up.bin -o got_echo.bin || { echo "upload failed"; return 1; }
	cmp -s got_echo.bin ref_echo.bin || { echo "upload corrupted: $(wc -c <got_echo.bin) bytes back, $(md5sum <got_echo.bin | cut -c1-8) want $(md5sum <ref_echo.bin | cut -c1-8)"; return 1; }
	return 0
}

# ---------------------------------------------------------------- cases
# case TAG SERVER_KIND CLIENT_KIND SERVER_MODE CLIENT_MODE
case_() {
	# A shifted argument once ran the whole matrix against the wrong endpoints
	# and still reported results, so the arity is checked before anything starts.
	if [ "$#" != 5 ]; then
		echo "case_ wants TAG SERVER_KIND CLIENT_KIND SERVER_MODE CLIENT_MODE, got $#: $*"
		exit 1
	fi
	local tag=$1 srvkind=$2 clikind=$3 smode=$4 cmode=$5
	if [ -n "${ONLY:-}" ] && ! echo "$tag" | grep -q -- "$ONLY"; then
		CASE_NO=$((CASE_NO + 1))
		return
	fi
	SRV=$((18600 + CASE_NO))
	CLI=$((15600 + CASE_NO))
	CASE_NO=$((CASE_NO + 1))

	if [ "$srvkind" = xray ]; then
		cat >srv_xray.json <<EOF
{
  "log": { "loglevel": "info" },
  "inbounds": [
    { "port": $SRV, "protocol": "vless",
      "settings": { "clients": [ { "id": "$UUID" } ], "decryption": "none" },
      "streamSettings": { "network": "xhttp", "security": "none",
        "xhttpSettings": { "path": "/xrt", "mode": "$smode" } } }
  ],
  "outbounds": [ { "protocol": "freedom" } ]
}
EOF
		"$XRAY" run -config srv_xray.json >srv.log 2>&1 &
	else
		cat >srv_bpc.json <<EOF
{
  "log": { "level": "info" },
  "inbounds": [
    { "protocol": "vless", "host": "127.0.0.1", "port": "$SRV", "users": [ "$UUID" ],
      "transport": { "type": "xhttp", "path": "/xrt", "mode": "$smode" } }
  ],
  "outbounds": [ { "protocol": "freedom" } ]
}
EOF
		./bpc.exe -config srv_bpc.json >srv.log 2>&1 &
	fi
	sleep 1

	if [ "$clikind" = xray ]; then
		cat >cli_xray.json <<EOF
{
  "log": { "loglevel": "info" },
  "inbounds": [
    { "listen": "127.0.0.1", "port": $CLI, "protocol": "socks",
      "settings": { "auth": "noauth", "udp": false } }
  ],
  "outbounds": [
    { "protocol": "vless",
      "settings": { "vnext": [ { "address": "127.0.0.1", "port": $SRV,
        "users": [ { "id": "$UUID", "encryption": "none" } ] } ] },
      "streamSettings": { "network": "xhttp", "security": "none",
        "xhttpSettings": { "path": "/xrt", "mode": "$cmode" } } }
  ]
}
EOF
		"$XRAY" run -config cli_xray.json >cli.log 2>&1 &
	else
		cat >cli_bpc.json <<EOF
{
  "log": { "level": "info" },
  "inbounds": [ { "protocol": "socks", "host": "127.0.0.1", "port": "$CLI" } ],
  "outbounds": [
    { "protocol": "vless", "host": "127.0.0.1", "port": "$SRV", "secret": "$UUID",
      "transport": { "type": "xhttp", "path": "/xrt", "mode": "$cmode" } }
  ]
}
EOF
		./bpc.exe -config cli_bpc.json >cli.log 2>&1 &
	fi
	sleep 1.5

	local name="$tag server=$smode/$srvkind client=$cmode/$clikind"
	if probe $CLI; then
		echo "PASS  $name"
		PASS=$((PASS + 1))
	else
		echo "FAIL  $name"
		FAIL=$((FAIL + 1))
		echo "---- server log"; tail -6 srv.log | sed 's/^/    /'
		echo "---- client log"; tail -6 cli.log | sed 's/^/    /'
	fi
	kill_procs
}

CASE_NO=0
PASS=0
FAIL=0

# BPC client -> Xray server: TAG, xray server, bpc client, server mode, client mode
case_ "A stream-one vs pinned"    xray bpc stream-one stream-one
case_ "A unset default vs pinned" xray bpc stream-one ""
case_ "A stream-one vs auto"      xray bpc auto       stream-one
case_ "A stream-up vs pinned"     xray bpc stream-up  stream-up
case_ "A stream-up vs auto"       xray bpc auto       stream-up
case_ "A packet-up vs pinned"     xray bpc packet-up  packet-up
case_ "A packet-up vs auto"       xray bpc auto       packet-up
case_ "A auto vs auto"            xray bpc auto       auto

# Xray client -> BPC server: TAG, bpc server, xray client, server mode, client mode
case_ "B stream-one vs pinned"    bpc xray stream-one stream-one
case_ "B stream-one vs auto"      bpc xray auto       stream-one
case_ "B stream-up vs pinned"     bpc xray stream-up  stream-up
case_ "B stream-up vs auto"       bpc xray auto       stream-up
case_ "B packet-up vs pinned"     bpc xray packet-up  packet-up
case_ "B packet-up vs auto"       bpc xray auto       packet-up
case_ "B auto vs auto"            bpc xray auto       auto

# BPC <-> BPC through the assembled core, not only the transport
case_ "C self stream-up"          bpc bpc  auto       stream-up
case_ "C self packet-up"          bpc bpc  packet-up  packet-up
case_ "C self auto"               bpc bpc  auto       auto

# The control: Xray against itself, no padding configured anywhere. If a B case
# fails while its D twin passes, the fault is in BPC; if both fail, the harness
# or the mode pairing is the fault and no core change can fix it.
case_ "D stream-one vs pinned"    xray xray stream-one stream-one
case_ "D stream-one vs auto"      xray xray auto       stream-one
case_ "D stream-up vs pinned"     xray xray stream-up  stream-up
case_ "D stream-up vs auto"       xray xray auto       stream-up
case_ "D packet-up vs pinned"     xray xray packet-up  packet-up
case_ "D packet-up vs auto"       xray xray auto       packet-up
case_ "D auto vs auto"            xray xray auto       auto

MSYS_NO_PATHCONV=1 taskkill /IM origin.exe /F >/dev/null 2>&1
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" = 0 ]
