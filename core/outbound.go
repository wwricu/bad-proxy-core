package core

import (
	"errors"
	"net"
	"time"

	"github.com/wwricu/bad-proxy-core/protocols"
	"github.com/wwricu/bad-proxy-core/transport"
)

type OutboundConfig struct {
	Tag       string `json:"tag"`
	Protocol  string `json:"protocol"`
	Secret    string `json:"secret"`
	Host      string `json:"host"`
	Port      string `json:"port"`
	Transport string `json:"transport"`
	Path      string `json:"path"`
	Authority string `json:"authority"`
	Padding   string `json:"padding"`
	Tls       bool   `json:"tls"`
	Insecure  bool   `json:"insecure"`
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
		proto = BTP
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
			transport.GetProtocol(c.Transport),
			transport.Options{
				Address:   address,
				Path:      c.Path,
				Authority: c.Authority,
				Padding:   c.Padding,
				UseTLS:    c.Tls,
				Insecure:  c.Insecure,
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
		switch ob.protocol {
		case BTP:
			out = protocols.NewBtpOutbound(conn, ob.secret)
		case TROJAN:
			out = protocols.NewTrojanOutbound(conn, ob.secret)
		case SOCKS:
			out = protocols.NewSocks5Outbound(conn)
		case VLESS:
			out, err = protocols.NewVlessOutbound(conn, ob.secret)
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
		default:
			_ = conn.Close()
			return nil, errors.New("unknown outbound protocol " + ob.protocol)
		}
	}

	if err = out.Connect(targetAddr); err != nil {
		_ = out.Close()
		return nil, err
	}
	_ = out.SetDeadline(time.Time{}) // handshake done, relay has no deadline
	return out, nil
}

type OutboundConnect interface {
	Connect(targetAddr string) error
	net.Conn
}
