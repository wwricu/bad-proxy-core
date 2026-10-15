package transport

import (
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

// Options is the flat per-endpoint config mirror: it is only ever the *input*
// to New, never carried into a live connection. New picks out just the fields
// each transport reads, so tcpTransport, for one, physically cannot see padding
// or a cert path.
type Options struct {
	Address   string // host:port
	Path      string // xhttp request path, default "/"
	Authority string // SNI / Host header, defaults to the address host
	// server side
	TlsCertPath string
	TlsKeyPath  string
	// client side
	UseTLS bool
	// xhttp padding range, "min-max" or "n"; empty means use the default
	Padding string
}

// Transport is one way of moving an undirected byte stream between two
// endpoints. Each concrete transport (tcp, tls, xhttp) holds exactly its own
// options as struct fields, so Dial/Listen take no arguments and a caller only
// ever handles this interface - never the grab-bag Options.
type Transport interface {
	Dial() (net.Conn, error)
	Listen() (net.Listener, error)
}

// New builds the transport for p out of the flat options. An unrecognised p
// falls back to raw TCP, so callers may pass a not-yet-defaulted ProtocolType
// safely. This is the single place that knows which fields each transport uses.
func New(p ProtocolType, o Options) Transport {
	switch p {
	case TLS:
		return tlsTransport{
			address:    o.Address,
			serverName: o.Authority,
			certPath:   o.TlsCertPath,
			keyPath:    o.TlsKeyPath,
		}
	case XHTTP:
		return xhttpTransport{
			address:   o.Address,
			path:      o.Path,
			authority: o.Authority,
			padding:   o.Padding,
			useTLS:    o.UseTLS,
			certPath:  o.TlsCertPath,
			keyPath:   o.TlsKeyPath,
		}
	default:
		return tcpTransport{address: o.Address}
	}
}

// Dial and Listen are one-call shorthands over the flat options, for the
// "I just want a conn/listener now" case (tests, quick setup). Normal code
// builds a Transport once with New and reuses it.
func Dial(p ProtocolType, o Options) (net.Conn, error) { return New(p, o).Dial() }

func Listen(p ProtocolType, o Options) (net.Listener, error) { return New(p, o).Listen() }
