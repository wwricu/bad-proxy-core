package protocols

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"

	"github.com/wwricu/bad-proxy-core/common"
)

/*
https://trojan-gfw.github.io/trojan/protocol.html

	+-----------------------+---------+----------------+---------+----------+
	| hex(SHA224(Password)) |  CRLF   | Trojan Request |  CRLF   | Payload  |
	+-----------------------+---------+----------------+---------+----------+
	|          56           | X'0D0A' |    Variable    | X'0D0A' | Variable |

Trojan Request is a SOCKS5-like request (CMD/ATYP/DST.ADDR/DST.PORT); only
CMD CONNECT is supported.
*/

// hashLen is the hex length of a SHA224 password digest in the header.
const hashLen = 56

// trojanHash is the password digest both directions compute.
func trojanHash(password string) string {
	h := sha256.New224()
	h.Write([]byte(password))
	return hex.EncodeToString(h.Sum(nil))
}

// TrojanInbound is the server side: verify the password digest then decode the
// SOCKS-like request.
type TrojanInbound struct {
	*common.Buffered
	passwords []string
}

func NewTrojanInbound(conn net.Conn, passwords []string) *TrojanInbound {
	return &TrojanInbound{Buffered: common.NewBuffered(conn), passwords: passwords}
}

func (in *TrojanInbound) Connect() (targetAddr string, err error) {
	head, err := in.ReadFull(hashLen + 2)
	if err != nil {
		return
	}
	if string(head[hashLen:]) != "\r\n" {
		return "", errors.New("malformed trojan header")
	}
	authorized := false
	for _, password := range in.passwords {
		if subtle.ConstantTimeCompare(head[:hashLen], []byte(trojanHash(password))) == 1 {
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
	if targetAddr, err = in.DecodeTarget(common.TargetCodingSocks); err != nil {
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

// TrojanOutbound is the client side: emit the digest and a SOCKS-like CONNECT.
type TrojanOutbound struct {
	*common.Buffered
	password string
}

func NewTrojanOutbound(conn net.Conn, password string) *TrojanOutbound {
	return &TrojanOutbound{Buffered: common.NewBuffered(conn), password: password}
}

func (out *TrojanOutbound) Connect(targetAddr string) error {
	request := make([]byte, 0, 96)
	request = append(request, trojanHash(out.password)...)
	request = append(request, '\r', '\n', 0x01)
	address, err := common.EncodeTarget(targetAddr, common.TargetCodingSocks)
	if err != nil {
		return err
	}
	request = append(request, address...)
	request = append(request, '\r', '\n')
	_, err = out.Write(request)
	return err
}
