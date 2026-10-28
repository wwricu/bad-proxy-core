package transport

import (
	"net"
	"time"

	"github.com/wwricu/bad-proxy-core/common"
	"github.com/wwricu/bad-proxy-core/logger"
)

// Options is the flat per-endpoint config mirror: it is only ever the *input*
// to New, never carried into a live connection. New picks out just the fields
// each transport reads, so tcpTransport, for one, physically cannot see a cert
// path.
type Options struct {
	Address   string // host:port
	Path      string // xhttp request path, default "/"
	Authority string // SNI / Host header, defaults to the address host
	Mode      string // xhttp mode: "", "auto", "stream-one", "stream-up", "packet-up"
	// server side
	TlsCertPath string
	TlsKeyPath  string
	// client side
	UseTLS bool
	// HandshakeTimeout bounds establishing an outbound connection: the TCP
	// connect and, over TLS, the handshake. Zero falls back to the documented
	// default; it never means "wait forever", because a transport has no other
	// way to notice a peer that accepts the socket and then goes silent.
	HandshakeTimeout time.Duration
}

// Transport is one way of moving an undirected byte stream between two
// endpoints. Each concrete transport (tcp, tls, xhttp, http2) holds exactly its
// own options as struct fields, so Dial/Listen take no arguments and a caller
// only ever handles this interface - never the grab-bag Options.
type Transport interface {
	Dial() (net.Conn, error)
	Listen() (net.Listener, error)
}

// New builds the transport named by a configured type. An empty type is the
// documented TCP default; a set-but-unknown one is a typo, and quietly
// plain-dialing it is the worst way to find out.
//
// "ws" and "wss" were removed: WebSocket was replaced by xhttp stream mode
// (same role, no third-party dependency). "http2" is Xray's network "http" -
// one PUT carried both ways over h2 - and lives in http2.go.
//
// The mode field is xhttp's alone; http2 ignores it, as Xray's own config does.
func New(protocol string, o Options) Transport {
	timeout := o.HandshakeTimeout
	if timeout <= 0 {
		timeout = common.DefaultHandshakeTimeout
	}
	switch protocol {
	case "tls":
		return tlsTransport{
			address:    o.Address,
			serverName: o.Authority,
			certPath:   o.TlsCertPath,
			keyPath:    o.TlsKeyPath,
			timeout:    timeout,
		}
	case "xhttp":
		return xhttpTransport{
			address:   o.Address,
			path:      o.Path,
			authority: o.Authority,
			// resolved once here, at config time, so a typo is reported once
			// instead of on every tunnel this endpoint opens.
			mode:     ResolveXhttpMode(o.Mode),
			useTLS:   o.UseTLS,
			certPath: o.TlsCertPath,
			keyPath:  o.TlsKeyPath,
			timeout:  timeout,
		}
	case "http2":
		return http2Transport{
			address:   o.Address,
			path:      o.Path,
			authority: o.Authority,
			useTLS:    o.UseTLS,
			certPath:  o.TlsCertPath,
			keyPath:   o.TlsKeyPath,
			timeout:   timeout,
		}
	}
	if protocol != "" {
		logger.Warnf("unknown transport type %q, using tcp", protocol)
	}
	return tcpTransport{address: o.Address, timeout: timeout}
}
