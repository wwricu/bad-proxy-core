package core

import (
	"net"
	"time"

	"github.com/wwricu/bad-proxy-core/protocols"
	"github.com/wwricu/bad-proxy-core/transport"
)

type OutboundConfig struct {
	Tag       string          `json:"tag"`
	Protocol  string          `json:"protocol"`
	Secret    string          `json:"secret"`
	Host      string          `json:"host"`
	Port      string          `json:"port"`
	Transport TransportConfig `json:"transport"`
}

type Outbound struct {
	tag       string
	protocol  string
	secret    string
	transport transport.Transport
	handshake time.Duration
	dial      time.Duration
}

func NewOutbound(c OutboundConfig, set settings) Outbound {
	proto := c.Protocol
	if proto == "" {
		proto = protocols.BTP
	}
	address := ""
	if c.Host != "" {
		address = net.JoinHostPort(c.Host, c.Port)
	}
	return Outbound{
		tag:       c.Tag,
		protocol:  proto,
		secret:    c.Secret,
		handshake: set.handshakeTimeout,
		dial:      set.dialTimeout,
		transport: transport.New(
			transport.GetProtocol(c.Transport.Type),
			transport.Options{
				Address:   address,
				Path:      c.Transport.Path,
				Authority: c.Transport.Authority,
				UseTLS:    c.Transport.Tls,
			},
		),
	}
}

func (ob *Outbound) Dial(targetAddr string) (out OutboundConnect, err error) {
	// direct relay to the target, no upstream
	if ob.protocol == FREEDOM {
		conn, err := net.DialTimeout("tcp", targetAddr, ob.dial)
		if err != nil {
			return nil, err
		}
		out = protocols.NewFreeOutbound(conn)
		out.SetDeadline(time.Now().Add(ob.handshake))
	} else {
		var conn net.Conn
		conn, err = ob.transport.Dial()
		if err != nil {
			return nil, err
		}
		if err = conn.SetDeadline(time.Now().Add(ob.handshake)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		// protocol matching lives in the protocols package; NewOutbound owns the
		// conn and closes it if the handshake setup fails.
		out, err = protocols.NewOutbound(ob.protocol, ob.secret, conn)
		if err != nil {
			return nil, err
		}
	}

	if err = out.Connect(targetAddr); err != nil {
		_ = out.Close()
		return nil, err
	}
	_ = out.SetDeadline(time.Time{}) // handshake done, relay has no deadline
	return out, nil
}

// OutboundConnect is the handshake+relay view of an upstream connection; the
// concrete protocols and their matching live in the protocols package.
type OutboundConnect = protocols.Outbound
