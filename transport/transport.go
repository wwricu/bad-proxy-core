package transport

import (
	"crypto/tls"
	"errors"
	"net"
)

type ProtocolType string

const (
	TCP   ProtocolType = "tcp"
	TLS   ProtocolType = "tls"
	XHTTP ProtocolType = "xhttp"
)

func (protocol ProtocolType) Str() string { return string(protocol) }

func GetProtocol(protocol string) ProtocolType {
	p := ProtocolType(protocol)
	switch p {
	case TCP, TLS, XHTTP:
		return p
	}
	// "http2", "ws" and "wss" were removed: WebSocket was replaced by xhttp
	// stream mode (same role, no third-party dependency) and the raw h2
	// transport never had a working client.
	return ""
}

// Options fully describes one transport endpoint; no address/string
// concatenation tricks anymore.
type Options struct {
	Address   string // host:port
	Path      string // xhttp request path, default "/"
	Authority string // SNI / Host header, defaults to the address host
	// server side
	TlsCertPath string
	TlsKeyPath  string
	// client side
	UseTLS   bool
	Insecure bool
	// xhttp padding range, "min-max" or "n", empty disables
	Padding string
}

func Dial(protocol ProtocolType, opts Options) (net.Conn, error) {
	switch protocol {
	case TLS:
		return tls.Dial("tcp", opts.Address, clientTlsConfig(opts))
	case XHTTP:
		return DialXhttp(opts)
	case TCP:
		return net.Dial("tcp", opts.Address)
	}
	return nil, errors.New("unknown transport " + protocol.Str())
}

func Listen(protocol ProtocolType, opts Options) (net.Listener, error) {
	switch protocol {
	case TLS:
		cert, err := tls.LoadX509KeyPair(opts.TlsCertPath, opts.TlsKeyPath)
		if err != nil {
			return nil, err
		}
		return tls.Listen("tcp", opts.Address, &tls.Config{
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{"h2", "http/1.1"},
		})
	case XHTTP:
		return ListenXhttp(opts)
	case TCP:
		return net.Listen("tcp", opts.Address)
	}
	return nil, errors.New("unknown transport " + protocol.Str())
}

func clientTlsConfig(opts Options) *tls.Config {
	config := &tls.Config{
		ServerName:         opts.Authority,
		InsecureSkipVerify: opts.Insecure,
		NextProtos:         []string{"h2", "http/1.1"},
	}
	if config.ServerName == "" {
		host, _, err := net.SplitHostPort(opts.Address)
		if err == nil {
			config.ServerName = host
		}
	}
	return config
}
