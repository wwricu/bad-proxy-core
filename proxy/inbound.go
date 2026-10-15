package proxy

import (
	"errors"
	"log"
	"net"
	"time"

	"github.com/wwricu/bad-proxy-core/metrics"
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
	listener      net.Listener
	address       string
	protocol      string
	secrets       []string
	transportType transport.ProtocolType
	opts          transport.Options
	mp            *metrics.ProtoStats // cached at construction: hot path is one atomic
}

func NewInbound(c InboundConfig) Inbound {
	proto := c.Protocol
	if proto == "" {
		proto = BTP
	}
	transportType := transport.GetProtocol(c.Transport)
	if transportType == "" {
		transportType = transport.TCP
	}
	return Inbound{
		protocol: proto,
		secrets:  c.credentials(),
		mp:       metrics.Default.Proto(proto),
		address:  net.JoinHostPort(c.Host, c.Port),
		opts: transport.Options{
			Address:     net.JoinHostPort(c.Host, c.Port),
			Path:        c.Path,
			TlsCertPath: c.TlsCertPath,
			TlsKeyPath:  c.TlsKeyPath,
		},
		transportType: transportType,
	}
}

func (inbound *Inbound) Listen() (err error) {
	inbound.listener, err = transport.Listen(inbound.transportType, inbound.opts)
	return
}

func (inbound *Inbound) Accept() (inConn InboundConnect, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("accept panic:", r)
			err = errors.New("panic in accept")
		}
	}()

	conn, err := inbound.listener.Accept()
	if err != nil {
		return
	}

	if deadlineErr := conn.SetDeadline(time.Now().Add(10 * time.Second)); deadlineErr != nil {
		_ = conn.Close()
		return nil, deadlineErr
	}
	switch inbound.protocol {
	case HTTP:
		inConn = protocols.NewHttpInbound(conn)
	case BTP:
		inConn = protocols.NewBtpInbound(conn, inbound.secrets)
	case SOCKS:
		inConn = protocols.NewSocks5Inbound(conn)
	case TROJAN:
		inConn = protocols.NewTrojanInbound(conn, inbound.secrets)
	case VLESS:
		inConn, err = protocols.NewVlessInbound(conn, inbound.secrets)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
	default:
		_ = conn.Close()
		return nil, errors.New("unknown inbound protocol " + inbound.protocol)
	}
	return inConn, nil
}

func (inbound *Inbound) Close() error {
	if inbound.listener == nil {
		return nil
	}
	return inbound.listener.Close()
}

type InboundConnect interface {
	// Connect performs the handshake and returns the relay target address.
	// Any bytes read past the header stay available through Read.
	Connect() (string, error)
	// Fallback answers well-known probes (e.g. a plain HTTP GET) when the
	// handshake fails, to look like an ordinary web service.
	Fallback()
	net.Conn
}
