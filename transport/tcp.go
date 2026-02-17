package transport

import "net"

type TcpManager struct {
}

func (t *TcpManager) Dial(address string) (net.Conn, error) {
	return net.Dial(TCP.Str(), address)
}

func (t *TcpManager) Listen(address string) (net.Listener, error) {
	return net.Listen(TCP.Str(), address)
}

func NewTcpDialer() Dialer {
	return &TcpManager{}
}

func NewTcpListener() Listener {
	return &TcpManager{}
}
