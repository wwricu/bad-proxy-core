package protocols

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
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
	IPv4             = 0x01
	Domain           = 0x03
	IPv6             = 0x04
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
	if atyp == IPv4 {
		addr = net.IP(payload[ptr : ptr+net.IPv4len]).String()
		ptr += net.IPv4len
	} else if atyp == Domain {
		domainLen := int(payload[ptr])
		ptr += 1
		addr = string(payload[ptr : ptr+domainLen])
		ptr += domainLen
	} else if atyp == IPv6 {
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

type TrojanOutbound struct {
	Conn     net.Conn
	Password string
}

func (outbound *TrojanOutbound) Connect(targetAddr string, payload []byte) (err error) {
	bytesBuffer := bytes.NewBuffer([]byte{})

	hasher := sha256.New224()
	hasher.Write([]byte(outbound.Password))
	hash := hasher.Sum(nil)

	_ = binary.Write(bytesBuffer, binary.BigEndian, []byte(hex.EncodeToString(hash)))
	_ = binary.Write(bytesBuffer, binary.BigEndian, []byte("\r\n"))

	hnp := strings.Split(targetAddr, ":")
	host := []byte(hnp[0])
	port, err := strconv.ParseUint(hnp[1], 10, 16)
	if err != nil {
		return
	}

	atyp := IPv6
	ip := net.ParseIP(string(host))
	if ip == nil {
		atyp = Domain
	} else if ip.To4() != nil {
		atyp = IPv4
	}

	_ = binary.Write(bytesBuffer, binary.BigEndian, uint8(1))    // cmd
	_ = binary.Write(bytesBuffer, binary.BigEndian, uint8(atyp)) // atyp
	if atyp == Domain {
		_ = binary.Write(bytesBuffer, binary.BigEndian, uint8(len(host)))
	}
	_ = binary.Write(bytesBuffer, binary.BigEndian, host)
	_ = binary.Write(bytesBuffer, binary.BigEndian, uint16(port))
	_ = binary.Write(bytesBuffer, binary.BigEndian, []byte("\r\n"))

	_ = binary.Write(bytesBuffer, binary.BigEndian, payload)

	_, err = outbound.Conn.Write(bytesBuffer.Bytes())
	return
}

func (outbound *TrojanOutbound) Read(b []byte) (int, error) {
	return outbound.Conn.Read(b)
}

func (outbound *TrojanOutbound) Write(b []byte) (int, error) {
	return outbound.Conn.Write(b)
}

func (outbound *TrojanOutbound) Close() error {
	return outbound.Conn.Close()
}
