package transport

import (
	"net"
	"time"
)

// tcpTransport is the raw byte stream, no framing and no encryption. It needs
// only an address and the connect budget, so that is all it carries.
type tcpTransport struct {
	address string
	timeout time.Duration
}

func (t tcpTransport) Dial() (net.Conn, error) {
	return net.DialTimeout("tcp", t.address, t.timeout)
}

func (t tcpTransport) Listen() (net.Listener, error) {
	return net.Listen("tcp", t.address)
}
