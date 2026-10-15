package transport

import "net"

// tcpTransport is the raw byte stream, no framing and no encryption. It needs
// only an address, so that is all it carries.
type tcpTransport struct {
	address string
}

func (t tcpTransport) Dial() (net.Conn, error) {
	return net.Dial("tcp", t.address)
}

func (t tcpTransport) Listen() (net.Listener, error) {
	return net.Listen("tcp", t.address)
}
