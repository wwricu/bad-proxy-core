package core

import (
	"errors"
	"net"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
	"github.com/wwricu/bad-proxy-core/protocols"
	"github.com/wwricu/bad-proxy-core/transport"
)

// TransportConfig is the nested "transport" object shared by inbounds and
// outbounds. It keeps transport concerns (type, path, TLS material, padding)
// out of the application-protocol fields, so an endpoint reads as
// protocol/secret/host/port plus a transport block. Each transport picks only
// the fields it uses; the server derives plaintext-vs-TLS from cert/key being
// set, so there is no separate server "tls" toggle.
type TransportConfig struct {
	Type      string `json:"type"`      // "tcp", "tls", "xhttp"
	Path      string `json:"path"`      // xhttp request path
	Authority string `json:"authority"` // client: SNI / Host override
	Padding   string `json:"padding"`   // client xhttp: X-Padding range
	Tls       bool   `json:"tls"`       // client: encrypt the hop
	Cert      string `json:"cert"`      // server: TLS certificate path
	Key       string `json:"key"`       // server: TLS private key path
}

type InboundConfig struct {
	Protocol  string          `json:"protocol"`
	Host      string          `json:"host"`
	Port      string          `json:"port"`
	Secret    string          `json:"secret"`
	Secrets   []string        `json:"secrets"`
	Users     []string        `json:"users"` // vless ids, merged into secrets
	Transport TransportConfig `json:"transport"`
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
			transport.GetProtocol(c.Transport.Type),
			transport.Options{
				Address:     address,
				Path:        c.Transport.Path,
				TlsCertPath: c.Transport.Cert,
				TlsKeyPath:  c.Transport.Key,
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
