/*
A basic support for simple socks5 protocol
limitations:
	1. Only support version 5
	2. Only support no-auth
	3. The BND.ADDR and BND.PORT is fixed to ipv4 0.0.0.0:0
	4. The server replies the socks5 success before the outbound connection
	   is built, because we relay to an outbound instead of a direct tcp dial
	5. UDP unsupported
*/

package protocols

import (
	"errors"
	"log"
	"net"
)

const (
	Version = 0x05

	MethodNoAuth = 0x00

	CmdConnect = 0x01
	CmdUdp     = 0x03

	RepSucceeded = 0x00
	RepFailure   = 0x01

	maxMethods = 255
)

var socksReply = []byte{Version, RepSucceeded, 0x00, IPv4, 0, 0, 0, 0, 0, 0}

// readSocksGreeting consumes the version/nmethods/methods fields and answers
// the chosen method; only no-auth is offered.
func readSocksGreeting(in *Buffered) error {
	head, err := in.ReadFull(2)
	if err != nil {
		return err
	}
	if head[0] != Version {
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
		if m == MethodNoAuth {
			_, err := in.Write([]byte{Version, MethodNoAuth})
			return err
		}
	}
	_, _ = in.Write([]byte{Version, 0xFF})
	return errors.New("no acceptable auth method")
}

func readSocksRequest(in *Buffered) (string, error) {
	head, err := in.ReadFull(3)
	if err != nil {
		return "", err
	}
	if head[0] != Version {
		return "", errors.New("wrong socks version")
	}
	switch head[1] {
	case CmdConnect:
	case CmdUdp:
		return "", errors.New("udp associate unsupported")
	default:
		return "", errors.New("unsupported command")
	}
	return in.DecodeTarget(TargetCodingSocks)
}

type Socks5Outbound struct {
	*Buffered
}

func NewSocks5Outbound(conn net.Conn) *Socks5Outbound {
	return &Socks5Outbound{Buffered: NewBuffered(conn)}
}

func (out *Socks5Outbound) Connect(targetAddr string) error {
	if _, err := out.Write([]byte{Version, 0x01, MethodNoAuth}); err != nil {
		return err
	}
	chosen, err := out.ReadFull(2)
	if err != nil {
		return err
	}
	if chosen[0] != Version || chosen[1] != MethodNoAuth {
		return errors.New("socks5 server refused no-auth")
	}
	request := make([]byte, 0, 64)
	request = append(request, Version, CmdConnect, 0x00)
	writer := addressWriter{w: &request}
	if err := EncodeTarget(writer, targetAddr, TargetCodingSocks); err != nil {
		return err
	}
	if _, err = out.Write(request); err != nil {
		return err
	}
	reply, err := out.ReadFull(10)
	if err != nil {
		return err
	}
	if reply[0] != Version || reply[1] != RepSucceeded {
		return errors.New("socks5 connect failed, rep " + string(rune('0'+reply[1])))
	}
	return nil
}

type Socks5Inbound struct {
	*Buffered
}

func NewSocks5Inbound(conn net.Conn) *Socks5Inbound {
	return &Socks5Inbound{Buffered: NewBuffered(conn)}
}

func (in *Socks5Inbound) Connect() (targetAddr string, err error) {
	if err = readSocksGreeting(in.Buffered); err != nil {
		return
	}
	targetAddr, err = readSocksRequest(in.Buffered)
	if err != nil {
		log.Println("socks inbound:", err)
		_, _ = in.Write([]byte{Version, RepFailure, 0x00, IPv4, 0, 0, 0, 0, 0, 0})
		return "", err
	}
	_, err = in.Write(socksReply)
	return targetAddr, err
}

func (in *Socks5Inbound) Fallback() {}
