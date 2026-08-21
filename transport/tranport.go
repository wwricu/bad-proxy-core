package transport

import (
	"net"
)

const (
	HTTP2 ProtocolType = "http2"
	TCP   ProtocolType = "tcp"
	TLS   ProtocolType = "tls"
	WS    ProtocolType = "ws"
	WSS   ProtocolType = "wss"
)

type Dialer interface {
	Dial(address string) (net.Conn, error)
}

type Listener interface {
	Listen(address string) (net.Listener, error)
}

type ProtocolType string

func (protocol ProtocolType) Str() string {
	return string(protocol)
}

func GetProtocol(protocol string) ProtocolType {
	return ProtocolType(protocol)
}

func Dial(protocol ProtocolType, address string) (net.Conn, error) {
	var dialer Dialer

	switch protocol {
	case HTTP2:
		dialer = NewHttp2Dialer()
	case TLS:
		dialer = NewTlsDialer()
	case WS, WSS:
		dialer = NewWsDialer(protocol)
	default:
		dialer = NewTcpDialer()
	}

	return dialer.Dial(address)
}

func Listen(
	address string,
	protocol ProtocolType,
	wsPath string,
	tlsCertPath string,
	tlsKeyPath string,
) (net.Listener, error) {
	var listener Listener
	switch protocol {
	case HTTP2:
		listener = NewHttp2Listener(wsPath)
	case TLS:
		listener = NewTlsListener(tlsCertPath, tlsKeyPath)
	case WS:
		listener = NewWsListener(wsPath)
	default:
		listener = NewTcpListener()
	}
	return listener.Listen(address)
}
