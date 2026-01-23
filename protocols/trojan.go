package protocols

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
)

/*
https://trojan-gfw.github.io/trojan/protocol.html

+-----------------------+---------+----------------+---------+----------+
| hex(SHA224(Password)) |  CRLF   | Trojan Request |  CRLF   | Payload  |
+-----------------------+---------+----------------+---------+----------+
|          56           | X'0D0A' |    Variable    | X'0D0A' | Variable |
+-----------------------+---------+----------------+---------+----------+

where Trojan Request is a SOCKS5-like request:

+-----+------+----------+----------+
| CMD | ATYP | DST.ADDR | DST.PORT |
+-----+------+----------+----------+
|  1  |  1   | Variable |    2     |
+-----+------+----------+----------+

where:
o  CMD
	o  CONNECT X'01'
	o  UDP ASSOCIATE X'03'
o  ATYP address type of following address
	o  IP V4 address: X'01'
	o  DOMAINNAME: X'03'
	o  IP V6 address: X'04'
o  DST.ADDR desired destination address
o  DST.PORT desired destination port in network octet order
*/

const (
	HashLen          = 56
	trojanBufferSize = 4096
)

type TrojanInbound struct {
	Conn     net.Conn
	Password string
}

type TrojanRequest struct {
	cmd  uint8
	atyp uint8
	addr net.Addr
	port uint16
}

func (inbound *TrojanInbound) Connect() (targetAddr string, payload []byte, err error) {
	payload = make([]byte, trojanBufferSize) // return rawdata on error
	length, err := inbound.Conn.Read(payload)
	if err != nil {
		return
	}

	ptr := 0
	hasher := sha256.New224()
	hasher.Write([]byte(inbound.Password))
	hash := hasher.Sum(nil)

	if !bytes.Equal([]byte(hex.EncodeToString(hash)), payload[ptr:ptr+HashLen]) {
		err = errors.New("password mismatch")
		return
	}

	ptr += HashLen + 2 // HashLen + CRLF

	cmd := payload[ptr]
	ptr += 1
	if cmd != 0x01 {
		err = errors.New("cmd is not connect")
		return
	}

	atyp := payload[ptr]
	ptr += 1

	var addr string
	if atyp == 0x01 {
		addr = net.IP(payload[ptr : ptr+net.IPv4len]).String()
		ptr += net.IPv4len
	} else if atyp == 0x03 {
		domainLen := int(payload[ptr])
		ptr += 1
		addr = string(payload[ptr : ptr+domainLen])
		ptr += domainLen
	} else if atyp == 0x04 {
		addr = net.IP(payload[ptr : ptr+net.IPv6len]).String()
		ptr += net.IPv6len
	} else {
		err = errors.New("unknown ATYP " + strconv.Itoa(int(payload[ptr])))
		return
	}

	port := int(payload[ptr])*256 + int(payload[ptr+1])
	ptr += 4 // Port + CRLF

	// HAVE TO specify the end of payload otherwise packages would be combined,
	// then SSL_ERROR_PROTOCOL_VERSION_ALERT is shown
	// Furthermore, trojanBufferSize have to be about 2kb to make HTTPS via wss work
	// TODO: WHY HTTPS via Trojan on TLS works and HTTP via Trojan on wss does not without length or big buffer
	return addr + ":" + strconv.Itoa(port), payload[ptr:length], err
}

func (inbound *TrojanInbound) Fallback(rawData []byte) {
	_ = rawData
}

func (inbound *TrojanInbound) Read(b []byte) (int, error) {
	return inbound.Conn.Read(b)
}

func (inbound *TrojanInbound) Write(b []byte) (int, error) {
	return inbound.Conn.Write(b)
}

func (inbound *TrojanInbound) Close() error {
	return inbound.Conn.Close()
}
