package protocols

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
)

/*
https://trojan-gfw.github.io/trojan/protocol.html

+-----------------------+---------+----------------+---------+----------+
| hex(SHA224(Password)) |  CRLF   | Trojan Request |  CRLF   | Payload  |
+-----------------------+---------+----------------+---------+----------+
|          56           | X'0D0A' |    Variable    | X'0D0A' | Variable |
+-----------------------+---------+----------------+---------+----------+

Trojan Request is a SOCKS5-like request (CMD/ATYP/DST.ADDR/DST.PORT);
only CMD CONNECT is supported.
*/

const HashLen = 56

type TrojanInbound struct {
	*Buffered
	passwords []string
}

func NewTrojanInbound(conn net.Conn, passwords []string) *TrojanInbound {
	return &TrojanInbound{Buffered: NewBuffered(conn), passwords: passwords}
}

func trojanHash(password string) string {
	hasher := sha256.New224()
	hasher.Write([]byte(password))
	return hex.EncodeToString(hasher.Sum(nil))
}

func (in *TrojanInbound) Connect() (targetAddr string, err error) {
	head, err := in.ReadFull(HashLen + 2)
	if err != nil {
		return
	}
	if string(head[HashLen:]) != "\r\n" {
		return "", errors.New("malformed trojan header")
	}
	authorized := false
	want := make([]byte, HashLen)
	for _, password := range in.passwords {
		copy(want, trojanHash(password))
		if subtleEqual(head[:HashLen], want) {
			authorized = true
			break
		}
	}
	if !authorized {
		return "", errors.New("password mismatch")
	}

	cmd, err := in.ReadByte()
	if err != nil {
		return
	}
	if cmd != 0x01 {
		return "", errors.New("cmd is not connect")
	}
	if targetAddr, err = in.DecodeTarget(TargetCodingSocks); err != nil {
		return
	}
	crlf, err := in.ReadFull(2)
	if err != nil {
		return
	}
	if string(crlf) != "\r\n" {
		return "", errors.New("missing trojan request terminator")
	}
	return targetAddr, nil
}

func (in *TrojanInbound) Fallback() {}

type TrojanOutbound struct {
	*Buffered
	password string
}

func NewTrojanOutbound(conn net.Conn, password string) *TrojanOutbound {
	return &TrojanOutbound{Buffered: NewBuffered(conn), password: password}
}

func (out *TrojanOutbound) Connect(targetAddr string) error {
	request := make([]byte, 0, 96)
	request = append(request, trojanHash(out.password)...)
	request = append(request, '\r', '\n', 0x01)
	writer := addressWriter{w: &request}
	if err := EncodeTarget(writer, targetAddr, TargetCodingSocks); err != nil {
		return err
	}
	request = append(request, '\r', '\n')
	_, err := out.Write(request)
	return err
}

func subtleEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
