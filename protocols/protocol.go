package protocols

import (
	"errors"
	"net"
)

// The application-layer protocols this core speaks. These names are the config
// vocabulary for handshakes; the "freedom" direct-connect mode is a core routing
// concept, not a handshake protocol, so it deliberately lives outside this list.
const (
	HTTP   = "http"
	BTP    = "btp"
	SOCKS  = "socks"
	TROJAN = "trojan"
	VLESS  = "vless"
)

// Inbound is one server-side handshake. The transport conn is already accepted
// and deadline-bounded by the caller; Connect performs the handshake and returns
// the target address to relay to. Any bytes read past the header stay available
// through the embedded net.Conn. Fallback answers well-known probes (e.g. a plain
// HTTP GET) when the handshake fails, to look like an ordinary web service.
type Inbound interface {
	Connect() (string, error)
	Fallback()
	net.Conn
}

// Outbound is one client-side handshake. Connect dials-then-handshakes the
// upstream for a given target; the conn is already opened by the transport.
type Outbound interface {
	Connect(targetAddr string) error
	net.Conn
}

// NewInbound matches a protocol name to its inbound handshake. creds carries the
// per-protocol secrets (btp secrets, trojan passwords, vless ids - all just
// strings; http and socks ignore them). conn is closed on any error so callers do
// not have to. This is the single place that knows which handshakes exist, so
// adding a protocol never touches core again.
func NewInbound(protocol string, conn net.Conn, creds []string) (Inbound, error) {
	switch protocol {
	case HTTP:
		return NewHttpInbound(conn), nil
	case BTP:
		return NewBtpInbound(conn, creds), nil
	case SOCKS:
		return NewSocks5Inbound(conn), nil
	case TROJAN:
		return NewTrojanInbound(conn, creds), nil
	case VLESS:
		in, err := NewVlessInbound(conn, creds)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return in, nil
	default:
		_ = conn.Close()
		return nil, errors.New("unknown inbound protocol " + protocol)
	}
}

// NewOutbound matches a protocol name to its outbound handshake. secret is that
// protocol's single credential (socks ignores it). conn is closed on error.
func NewOutbound(protocol string, secret string, conn net.Conn) (Outbound, error) {
	switch protocol {
	case BTP:
		return NewBtpOutbound(conn, secret), nil
	case TROJAN:
		return NewTrojanOutbound(conn, secret), nil
	case SOCKS:
		return NewSocks5Outbound(conn), nil
	case VLESS:
		out, err := NewVlessOutbound(conn, secret)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return out, nil
	default:
		_ = conn.Close()
		return nil, errors.New("unknown outbound protocol " + protocol)
	}
}
