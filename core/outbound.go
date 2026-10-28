package core

import (
	"cmp"
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
	protocol  string
	secret    string
	transport transport.Transport
	handshake time.Duration
	dial      time.Duration
}

func NewOutbound(c OutboundConfig, set settings) Outbound {
	proto := cmp.Or(c.Protocol, protocols.BTP)
	address := ""
	if c.Host != "" {
		address = net.JoinHostPort(c.Host, c.Port)
	}
	return Outbound{
		protocol:  proto,
		secret:    c.Secret,
		handshake: set.handshakeTimeout,
		dial:      set.dialTimeout,
		transport: transport.New(
			c.Transport.Type,
			transport.Options{
				Address:          address,
				Path:             c.Transport.Path,
				Authority:        c.Transport.Authority,
				Mode:             c.Transport.Mode,
				UseTLS:           c.Transport.Tls,
				HandshakeTimeout: set.handshakeTimeout,
			},
		),
	}
}

func (ob *Outbound) Dial(targetAddr string) (net.Conn, error) {
	// direct relay to the target, no upstream
	if ob.protocol == FREEDOM {
		return net.DialTimeout("tcp", targetAddr, ob.dial)
	}

	conn, err := ob.transport.Dial()
	if err != nil {
		return nil, err
	}
	if err = conn.SetDeadline(time.Now().Add(ob.handshake)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// protocol matching lives in the protocols package; NewOutbound owns the
	// conn and closes it if the handshake setup fails.
	out, err := protocols.NewOutbound(ob.protocol, ob.secret, conn)
	if err != nil {
		return nil, err
	}
	if err = out.Connect(targetAddr); err != nil {
		_ = out.Close()
		return nil, err
	}
	_ = out.SetDeadline(time.Time{}) // handshake done, relay has no deadline
	return out, nil
}
