package protocols

import (
	"errors"
	"net"

	"github.com/wwricu/bad-proxy-core/common"
	"github.com/wwricu/bad-proxy-core/logger"
)

/*
SOCKS5. The client-facing inbound and the upstream-facing outbound share the
wire constants below. Limitations: version 5 only, no-auth only, BND.ADDR/PORT
fixed to 0.0.0.0:0, and the inbound writes its success reply before the
outbound is built (we relay to an outbound rather than dial directly).
UDP is unsupported.
*/

const (
	socksVersion      = 0x05
	socksMethodNoAuth = 0x00
	socksCmdConnect   = 0x01
	socksCmdUdp       = 0x03
	socksRepSucceeded = 0x00
	socksRepFailure   = 0x01
)

var socksReply = []byte{socksVersion, socksRepSucceeded, 0x00, common.IPv4, 0, 0, 0, 0, 0, 0}

// readSocksGreeting consumes the version/nmethods/methods fields and answers
// the chosen method; only no-auth is offered.
func readSocksGreeting(in *common.Buffered) error {
	head, err := in.ReadFull(2)
	if err != nil {
		return err
	}
	if head[0] != socksVersion {
		return errors.New("wrong socks version")
	}
	n := int(head[1])
	if n < 1 {
		return errors.New("empty method list")
	}
	methods, err := in.ReadFull(n)
	if err != nil {
		return err
	}
	for _, m := range methods {
		if m == socksMethodNoAuth {
			_, err := in.Write([]byte{socksVersion, socksMethodNoAuth})
			return err
		}
	}
	_, _ = in.Write([]byte{socksVersion, 0xFF})
	return errors.New("no acceptable auth method")
}

func readSocksRequest(in *common.Buffered) (string, error) {
	head, err := in.ReadFull(3)
	if err != nil {
		return "", err
	}
	if head[0] != socksVersion {
		return "", errors.New("wrong socks version")
	}
	switch head[1] {
	case socksCmdConnect:
	case socksCmdUdp:
		return "", errors.New("udp associate unsupported")
	default:
		return "", errors.New("unsupported command")
	}
	return in.DecodeTarget(common.TargetCodingSocks)
}

// Socks5Inbound is the client-facing server: local apps point at this port.
type Socks5Inbound struct {
	*common.Buffered
}

func NewSocks5Inbound(conn net.Conn) *Socks5Inbound {
	return &Socks5Inbound{Buffered: common.NewBuffered(conn)}
}

func (in *Socks5Inbound) Connect() (targetAddr string, err error) {
	if err = readSocksGreeting(in.Buffered); err != nil {
		return
	}
	targetAddr, err = readSocksRequest(in.Buffered)
	if err != nil {
		logger.Infof("socks inbound: %v", err)
		_, _ = in.Write([]byte{socksVersion, socksRepFailure, 0x00, common.IPv4, 0, 0, 0, 0, 0, 0})
		return "", err
	}
	_, err = in.Write(socksReply)
	return targetAddr, err
}

func (in *Socks5Inbound) Fallback() {}

// Socks5Outbound dials an upstream SOCKS5 server with no-auth CONNECT.
type Socks5Outbound struct {
	*common.Buffered
}

func NewSocks5Outbound(conn net.Conn) *Socks5Outbound {
	return &Socks5Outbound{Buffered: common.NewBuffered(conn)}
}

func (out *Socks5Outbound) Connect(targetAddr string) error {
	if _, err := out.Write([]byte{socksVersion, 0x01, socksMethodNoAuth}); err != nil {
		return err
	}
	chosen, err := out.ReadFull(2)
	if err != nil {
		return err
	}
	if chosen[0] != socksVersion || chosen[1] != socksMethodNoAuth {
		return errors.New("socks5 server refused no-auth")
	}
	request := make([]byte, 0, 64)
	request = append(request, socksVersion, socksCmdConnect, 0x00)
	address, err := common.EncodeTarget(targetAddr, common.TargetCodingSocks)
	if err != nil {
		return err
	}
	request = append(request, address...)
	if _, err = out.Write(request); err != nil {
		return err
	}
	reply, err := out.ReadFull(10)
	if err != nil {
		return err
	}
	if reply[0] != socksVersion || reply[1] != socksRepSucceeded {
		return errors.New("socks5 connect failed, rep " + string(rune('0'+reply[1])))
	}
	return nil
}
