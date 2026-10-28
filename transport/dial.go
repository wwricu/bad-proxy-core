package transport

import (
	"crypto/tls"
	"net"
	"time"
)

// dialTLS is tls.Dial with the configured budget covering both steps: the TCP
// connect and the handshake on the connection it returned. tls.Dial leaves the
// handshake to the operating system's patience, so a peer that accepts the
// socket and never sends a ServerHello parks the tunnel for minutes.
func dialTLS(address string, config *tls.Config, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	conn, err := (&net.Dialer{Deadline: deadline}).Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	// One deadline across both steps: a peer that withholds its ServerHello has
	// to fail within the same budget as one that never answers the SYN.
	if err = conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}
	client := tls.Client(conn, config)
	if err = client.Handshake(); err != nil {
		_ = client.Close()
		return nil, err
	}
	// The budget belongs to establishing the connection. Left armed, it would go
	// on cutting tunnels that merely go quiet for longer than it.
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}
