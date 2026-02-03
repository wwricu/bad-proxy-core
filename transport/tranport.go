package transport

import (
	"crypto/tls"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

type ProtocolType string

const (
	TCP ProtocolType = "tcp"
	TLS ProtocolType = "tls"
	WS  ProtocolType = "ws"
	WSS ProtocolType = "wss"
)

func (protocol ProtocolType) Str() string {
	return string(protocol)
}

func GetProtocol(protocol string) ProtocolType {
	return ProtocolType(protocol)
}

func Dial(protocol ProtocolType, address string) (net.Conn, error) {
	switch protocol {
	case TLS:
		return tls.Dial(TCP.Str(), address, &tls.Config{})
	case WS, WSS:
		conn, _, err := websocket.DefaultDialer.Dial(protocol.Str()+"://"+address, nil)
		return WsConn{conn: conn}, err // DO NOT return conn.NetConn()
	default:
		return net.Dial(TCP.Str(), address)
	}
}

func Listen(
	address string,
	protocol ProtocolType,
	wsPath string,
	tlsCertPath string,
	tlsKeyPath string,
) (listener net.Listener, err error) {
	switch protocol {
	case TLS:
		cert, err := tls.LoadX509KeyPair(tlsCertPath, tlsKeyPath)
		if err != nil {
			return listener, err
		}
		config := &tls.Config{Certificates: []tls.Certificate{cert}}
		return tls.Listen(TCP.Str(), address, config)
	case WS:
		listener := &WsListener{ch: make(chan net.Conn)}
		http.Handle(wsPath, listener)
		go func() {
			if err := http.ListenAndServe(address, nil); err != nil {
				panic(err)
			}
		}()
		return listener, nil
	default:
		return net.Listen(TCP.Str(), address)
	}
}
