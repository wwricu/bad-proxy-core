package proxy

import (
	"errors"
	"log"
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
	tag           string
	protocol      string
	secret        string
	transportType transport.ProtocolType
	opts          transport.Options
}

func NewOutbound(c OutboundConfig) Outbound {
	proto := c.Protocol
	if proto == "" {
		proto = BTP
	}
	address := ""
	if c.Host != "" {
		address = net.JoinHostPort(c.Host, c.Port)
	}
	return Outbound{
		tag:           c.Tag,
		protocol:      proto,
		secret:        c.Secret,
		transportType: transport.GetProtocol(c.Transport),
		opts: transport.Options{
			Address:   address,
			Path:      c.Path,
			Authority: c.Authority,
			Padding:   c.Padding,
			UseTLS:    c.Tls,
			Insecure:  c.Insecure,
		},
	}
}

func (outbound *Outbound) Dial(targetAddr string) (out OutboundConnect, err error) {
	// direct relay to the target, no upstream
	if outbound.protocol == FREEDOM {
		conn, err := net.DialTimeout("tcp", targetAddr, 15*time.Second)
		if err != nil {
			return nil, err
		}
		out = protocols.NewFreeOutbound(conn)
		out.SetDeadline(time.Now().Add(10 * time.Second))
	} else {
		transportType := outbound.transportType
		if transportType == "" {
			transportType = transport.TCP
		}
		var conn net.Conn
		conn, err = transport.Dial(transportType, outbound.opts)
		if err != nil {
			return nil, err
		}
		if err = conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		switch outbound.protocol {
		case BTP:
			out = protocols.NewBtpOutbound(conn, outbound.secret)
		case TROJAN:
			out = protocols.NewTrojanOutbound(conn, outbound.secret)
		case SOCKS:
			out = protocols.NewSocks5Outbound(conn)
		case VLESS:
			out, err = protocols.NewVlessOutbound(conn, outbound.secret)
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
		default:
			_ = conn.Close()
			return nil, errors.New("unknown outbound protocol " + outbound.protocol)
		}
		log.Println(outbound.protocol, "connect to", outbound.opts.Address)
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
