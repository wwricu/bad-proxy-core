package transport

import (
	"crypto/tls"
	"net"
)

type TlsManager struct {
	tlsCertPath string
	tlsKeyPath  string
}

func (t *TlsManager) Dial(address string) (net.Conn, error) {
	return tls.Dial(TCP.Str(), address, &tls.Config{})
}

func (t *TlsManager) Listen(address string) (net.Listener, error) {
	cert, err := tls.LoadX509KeyPair(t.tlsCertPath, t.tlsKeyPath)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{Certificates: []tls.Certificate{cert}}
	return tls.Listen(TCP.Str(), address, config)
}

func NewTlsDialer() Dialer {
	return &TlsManager{}
}

func NewTlsListener(tlsCertPath string, tlsKeyPath string) Listener {
	return &TlsManager{tlsCertPath: tlsCertPath, tlsKeyPath: tlsKeyPath}
}
