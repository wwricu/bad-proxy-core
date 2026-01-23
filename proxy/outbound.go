package proxy

import (
	"log"

	"github.com/wwricu/bad-proxy-core/protocols"
	"github.com/wwricu/bad-proxy-core/transport"
)

type OutboundConfig struct {
	Tag      string `json:"tag"`
	Secret   string `json:"secret"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Protocol string `json:"protocol"`
	Transmit string `json:"transmit"`
	WsPath   string `json:"ws_path"`
}

type Outbound struct {
	tag      string
	secret   string
	address  string
	protocol string
	transmit transport.ProtocolType
	wsPath   string
}

func (outbound *Outbound) Dial(targetAddr string, payload []byte) (out OutboundConnect, err error) {
	switch outbound.protocol {
	// *BtpOutbound implemented OutboundConnect,
	// here we return the pointer of BtpOutbound, which is an OutboundConnect
	// simply, *BtpOutbound is OutboundConnect
	case BTP:
		var conn, err = transport.Dial(outbound.transmit, outbound.address+outbound.wsPath)
		if err != nil {
			return nil, err
		}
		out = &protocols.BtpOutbound{Conn: conn, Secret: outbound.secret}
	case SOCKS:
		var conn, err = transport.Dial(transport.TCP, outbound.address)
		if err != nil {
			return nil, err
		}
		out = &protocols.Socks5Outbound{Conn: conn}
	case TROJAN:
		var conn, err = transport.Dial(outbound.transmit, outbound.address+outbound.wsPath)
		if err != nil {
			return nil, err
		}
		out = &protocols.TrojanOutbound{Conn: conn, Password: outbound.secret}
	default: // free
		var conn, err = transport.Dial(outbound.transmit, targetAddr+outbound.wsPath)
		if err != nil {
			return nil, err
		}
		out = &protocols.FreeOutbound{Conn: conn}
	}
	log.Println(outbound.protocol, "connect to", outbound.address)
	err = out.Connect(targetAddr, payload)
	return
}

type OutboundConnect interface {
	Connect(targetAddr string, payload []byte) error
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Close() error
}
