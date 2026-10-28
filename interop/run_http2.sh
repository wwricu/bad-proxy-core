#!/usr/bin/env bash
# http/2 interop matrix: BPC <-> Xray (network "http") in both directions,
# BPC <-> BPC, and Xray <-> Xray as the control, with VLESS and Trojan as the
# application layer. Every case carries a 300 KiB download and a 300 KiB upload
# through the tunnel and compares both byte for byte.
#
# Windows only (it stops the pair with taskkill). Needs an Xray binary whose
# "http" transport still exists - v1.8.24 is the one this was proved against;
# later majors removed it:
#
#	XRAY=/path/to/xray.exe ./run_http2.sh
#
# Xray's client refuses to run the http transport without TLS, so the B and D
# groups terminate TLS at BPC / Xray with a throwaway certificate. The A group
# is plaintext h2c because Xray's server serves h2c when no TLS is configured.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="$HERE/work-http2"
XRAY="${XRAY:-$HERE/xray/xray.exe}"
UUID=01234567-89ab-4cde-8f01-23456789abcd
TROJAN=trojan-pw-0123456789
ORIGIN=18080
PATHS=/h2

if [ ! -x "$XRAY" ]; then
	echo "no Xray binary at $XRAY - set XRAY=/path/to/xray.exe"
	exit 1
fi
mkdir -p "$WORK"
cd "$WORK" || exit 1
rm -f ./*.log ./*.json ./*.bin bpc.exe origin.exe
rm -f cert.pem key.pem

MSYS_NO_PATHCONV=1 openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
	-keyout key.pem -out cert.pem -subj "/CN=localhost" \
	-addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1 || {
	echo "openssl could not issue the test certificate"
	exit 1
}

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

xray_credentials() { # $1 = app
	if [ "$1" = vless ]; then
		echo '"clients": [ { "id": "'"$UUID"'" } ], "decryption": "none"'
	else
		echo '"clients": [ { "password": "'"$TROJAN"'" } ]'
	fi
}

xray_client_settings() { # $1 = app, $2 = server port
	if [ "$1" = vless ]; then
		echo '"vnext": [ { "address": "127.0.0.1", "port": '"$2"', "users": [ { "id": "'"$UUID"'", "encryption": "none" } ] } ]'
	else
		echo '"servers": [ { "address": "127.0.0.1", "port": '"$2"', "password": "'"$TROJAN"'" } ]'
	fi
}

bpc_endpoint() { # $1 = app, $2 = server port, $3 = extra transport fields, $4 = inbound credential style
	local cred
	if [ "$1" = vless ] && [ "${4:-}" = inbound ]; then
		cred='"users": [ "'"$UUID"'" ]'
	elif [ "$1" = vless ]; then
		cred='"secret": "'"$UUID"'"'
	else
		cred='"secret": "'"$TROJAN"'"'
	fi
	echo '"protocol": "'"$1"'", "host": "127.0.0.1", "port": "'"$2"'", '"$cred"', "transport": { "type": "http2", "path": "'"$PATHS"'"'"$3"' }'
}

# case TAG SERVER_KIND CLIENT_KIND APP
case_() {
	if [ "$#" != 4 ]; then
		echo "case_ wants TAG SERVER_KIND CLIENT_KIND APP, got $#: $*"
		exit 1
	fi
	local tag=$1 srvkind=$2 clikind=$3 app=$4
	if [ -n "${ONLY:-}" ] && ! echo "$tag" | grep -q -- "$ONLY"; then
		CASE_NO=$((CASE_NO + 1))
		return
	fi
	SRV=$((18600 + CASE_NO))
	CLI=$((15600 + CASE_NO))
	CASE_NO=$((CASE_NO + 1))

	if [ "$srvkind" = xray ]; then
		# D is TLS on both sides; A is h2c, because the BPC client is the one
		# dialing and nothing on this side needs a certificate.
		local security=none tlsblock=''
		if [ "$clikind" = xray ] || [ "$tag" = D* ]; then
			security=tls
			tlsblock=', "tlsSettings": { "certificates": [ { "certificateFile": "cert.pem", "keyFile": "key.pem" } ] }'
		fi
		cat >srv_xray.json <<EOF
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    { "port": $SRV, "protocol": "$app",
      "settings": { $(xray_credentials "$app") },
      "streamSettings": { "network": "http", "security": "$security",
        "httpSettings": { "path": "$PATHS" }$tlsblock } }
  ],
  "outbounds": [ { "protocol": "freedom" } ]
}
EOF
		"$XRAY" run -config srv_xray.json >srv.log 2>&1 &
	else
		# Xray's client only dials TLS; a BPC client dials h2c unless told to
		# encrypt, and the C group is the plaintext pair.
		local certblock=''
		if [ "$clikind" = xray ]; then
			certblock=', "cert": "cert.pem", "key": "key.pem"'
		fi
		cat >srv_bpc.json <<EOF
{
  "log": { "level": "info" },
  "inbounds": [
    { $(bpc_endpoint "$app" "$SRV" "$certblock" inbound) }
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
  "log": { "loglevel": "warning" },
  "inbounds": [
    { "listen": "127.0.0.1", "port": $CLI, "protocol": "socks",
      "settings": { "auth": "noauth", "udp": false } }
  ],
  "outbounds": [
    { "protocol": "$app",
      "settings": { $(xray_client_settings "$app" "$SRV") },
      "streamSettings": { "network": "http", "security": "tls",
        "tlsSettings": { "allowInsecure": true, "serverName": "localhost" },
        "httpSettings": { "path": "$PATHS" } } }
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
    { $(bpc_endpoint "$app" "$SRV" '') }
  ]
}
EOF
		./bpc.exe -config cli_bpc.json >cli.log 2>&1 &
	fi
	sleep 1.5

	local name="$tag $app server=$srvkind client=$clikind"
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

# A: BPC client dials Xray's hub over h2c.
case_ "A vless"  xray bpc vless
case_ "A trojan" xray bpc trojan

# B: Xray's client dials BPC's hub over TLS+h2 (Caddy's hop is HTTP/1.1, which
# the transport tests cover; this is the direct TLS shape).
case_ "B vless"  bpc xray vless
case_ "B trojan" bpc xray trojan

# C: the assembled core, plaintext h2c between two BPC endpoints.
case_ "C vless"  bpc bpc vless
case_ "C trojan" bpc bpc trojan

# D: the control. If a B case fails while its D twin passes, the fault is BPC.
case_ "D vless"  xray xray vless
case_ "D trojan" xray xray trojan

MSYS_NO_PATHCONV=1 taskkill /IM origin.exe /F >/dev/null 2>&1
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" = 0 ]
