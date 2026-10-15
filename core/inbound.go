package core

import (
	"errors"
	"net"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
	"github.com/wwricu/bad-proxy-core/protocols"
	"github.com/wwricu/bad-proxy-core/transport"
)

type InboundConfig struct {
	Protocol    string   `json:"protocol"`
	Host        string   `json:"host"`
	Port        string   `json:"port"`
	Secret      string   `json:"secret"`
	Secrets     []string `json:"secrets"`
	Users       []string `json:"users"` // vless ids, merged into secrets
	Transport   string   `json:"transport"`
	Path        string   `json:"path"`
	TlsCertPath string   `json:"tls_cert_path"`
	TlsKeyPath  string   `json:"tls_key_path"`
}

func (c InboundConfig) credentials() []string {
	list := make([]string, 0, len(c.Secrets)+len(c.Users)+1)
	if c.Secret != "" {
		list = append(list, c.Secret)
	}
	list = append(list, c.Secrets...)
	list = append(list, c.Users...)
	return list
}

type Inbound struct {
	listener  net.Listener
	address   string
	protocol  string
	secrets   []string
	transport transport.Transport
	handshake time.Duration
}

func NewInbound(c InboundConfig, set settings) Inbound {
	proto := c.Protocol
	if proto == "" {
		proto = protocols.BTP
	}
	address := net.JoinHostPort(c.Host, c.Port)
	return Inbound{
		protocol:  proto,
		secrets:   c.credentials(),
		handshake: set.handshakeTimeout,
		address:   address,
		transport: transport.New(
			transport.GetProtocol(c.Transport),
			transport.Options{
				Address:     address,
				Path:        c.Path,
				TlsCertPath: c.TlsCertPath,
				TlsKeyPath:  c.TlsKeyPath,
			},
		),
	}
}

func (in *Inbound) Listen() (err error) {
	in.listener, err = in.transport.Listen()
	return
}

func (in *Inbound) Accept() (inConn InboundConnect, err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("accept panic: %v", r)
			err = errors.New("panic in accept")
		}
	}()

	conn, err := in.listener.Accept()
	if err != nil {
		return
	}

	if deadlineErr := conn.SetDeadline(time.Now().Add(in.handshake)); deadlineErr != nil {
		_ = conn.Close()
		return nil, deadlineErr
	}
	// protocol matching lives in the protocols package; NewInbound owns the
	// conn and closes it if the handshake setup fails.
	inConn, err = protocols.NewInbound(in.protocol, conn, in.secrets)
	if err != nil {
		return nil, err
	}
	return inConn, nil
}

func (in *Inbound) Close() error {
	if in.listener == nil {
		return nil
	}
	return in.listener.Close()
}

// InboundConnect is the handshake+relay view of an accepted connection; the
// concrete protocol implementations and their matching live in the protocols
// package, so core depends on only this interface.
type InboundConnect = protocols.Inbound
