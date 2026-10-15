package transport

import (
	"crypto/tls"
	"net"
)

// tlsTransport wraps a plain TCP connection in a TLS record layer. Server-side
// ALPN advertises h2 and http/1.1 so an xhttp tunnel can ride the same cert. It
// carries only what a TLS endpoint reads: an address, an SNI override, and the
// client/server key material.
type tlsTransport struct {
	address    string
	serverName string // SNI, empty means derive from the address host
	certPath   string // server only
	keyPath    string // server only
}

func (t tlsTransport) Dial() (net.Conn, error) {
	return tls.Dial("tcp", t.address, clientTlsConfig(t.address, t.serverName))
}

func (t tlsTransport) Listen() (net.Listener, error) {
	cert, err := tls.LoadX509KeyPair(t.certPath, t.keyPath)
	if err != nil {
		return nil, err
	}
	return tls.Listen("tcp", t.address, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	})
}

// clientTlsConfig is shared by the tls and xhttp clients: serverName falls back
// to the address host when unset. Verification is always on - there is no
// skip-verify option.
func clientTlsConfig(address, serverName string) *tls.Config {
	config := &tls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
	}
	if config.ServerName == "" {
		host, _, err := net.SplitHostPort(address)
		if err == nil {
			config.ServerName = host
		}
	}
	return config
}
