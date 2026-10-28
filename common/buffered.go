package common

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
)

const (
	IPv4   = 0x01
	Domain = 0x03
	IPv6   = 0x04

	maxDomainLen = 255
	bufSize      = 4096
)

// targetCoding maps address families to their wire atyp values, which
// differ between protocol families, and fixes field order.
type targetCoding struct {
	ipv4, domain, ipv6 byte
	// portFirst mirrors Xray's VLESS addrParser (PortThenAddress).
	portFirst bool
}

var (
	// TargetCodingSocks is the SOCKS5/Trojan table: 1 IPv4, 3 domain, 4 IPv6,
	// address before port.
	TargetCodingSocks = targetCoding{IPv4, Domain, IPv6, false}
	// TargetCodingVless matches Xray's proxy/vless/encoding exactly: 1 IPv4,
	// 2 domain, 3 IPv6, port written before atyp+address.
	TargetCodingVless = targetCoding{1, 2, 3, true}
)

// Buffered wraps a net.Conn with a bufio.Reader so handshakes can read
// exact field sizes even when TCP delivers the header in several segments.
// Data peeked but not consumed by the handshake is still delivered to the
// relay on the following Reads. Write/Close/deadlines etc. delegate to Conn.
type Buffered struct {
	net.Conn
	r       *bufio.Reader
	pending []byte // bytes re-injected in front of the stream by handshakes
}

var httpRequestMethods = [...][]byte{
	[]byte("GET "), []byte("POST "), []byte("PUT "), []byte("HEAD "),
	[]byte("OPTIONS "), []byte("CONNECT "), []byte("DELETE "), []byte("TRACE "),
}

// LooksLikeHttpRequest reports whether the peeked first bytes form an HTTP
// request line, used to answer probes that hit the tunnel port directly.
func LooksLikeHttpRequest(p []byte) bool {
	for _, m := range httpRequestMethods {
		if bytes.HasPrefix(p, m) {
			return true
		}
	}
	return false
}

func ServeFallbackPage(w io.Writer) {
	const page = "<html><body><a href=\"https://wwr.icu\">Please login<a>\r\n</body></html>"
	header := "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: " +
		strconv.Itoa(len(page)) + "\r\nConnection: close\r\n\r\n"
	_, _ = io.WriteString(w, header+page)
}

func NewBuffered(conn net.Conn) *Buffered {
	return &Buffered{Conn: conn, r: bufio.NewReaderSize(conn, bufSize)}
}

func (b *Buffered) Read(p []byte) (int, error) {
	if len(b.pending) > 0 {
		n := copy(p, b.pending)
		b.pending = b.pending[n:]
		return n, nil
	}
	return b.r.Read(p)
}

func (b *Buffered) Peek(n int) []byte {
	if len(b.pending) > 0 {
		if n <= len(b.pending) {
			return b.pending[:n]
		}
		out := append([]byte{}, b.pending...)
		more, _ := b.r.Peek(n - len(b.pending))
		return append(out, more...)
	}
	p, _ := b.r.Peek(n)
	return p
}

func (b *Buffered) ReadByte() (byte, error) {
	if len(b.pending) > 0 {
		c := b.pending[0]
		b.pending = b.pending[1:]
		return c, nil
	}
	return b.r.ReadByte()
}

// PushFront re-injects bytes at the head of the stream; used by the HTTP
// inbound to replay a rewritten request line to the origin.
func (b *Buffered) PushFront(p []byte) {
	b.pending = append(append(make([]byte, 0, len(p)+len(b.pending)), p...), b.pending...)
}

func (b *Buffered) ReadFull(n int) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(b, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (b *Buffered) ReadUint16() (uint16, error) {
	p, err := b.ReadFull(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(p), nil
}

// DecodeTarget reads a SOCKS/VLESS/Trojan style address (atyp included)
// and returns "host:port", interpreting atyp through the given coding.
func (b *Buffered) DecodeTarget(c targetCoding) (string, error) {
	var port uint16
	var err error
	if c.portFirst {
		if port, err = b.ReadUint16(); err != nil {
			return "", err
		}
	}
	atyp, err := b.ReadByte()
	if err != nil {
		return "", err
	}
	var host string
	switch atyp {
	case c.ipv4:
		p, err := b.ReadFull(net.IPv4len)
		if err != nil {
			return "", err
		}
		host = net.IP(p).String()
	case c.ipv6:
		p, err := b.ReadFull(net.IPv6len)
		if err != nil {
			return "", err
		}
		host = net.IP(p).String()
	case c.domain:
		length, err := b.ReadByte()
		if err != nil {
			return "", err
		}
		p, err := b.ReadFull(int(length))
		if err != nil {
			return "", err
		}
		host = string(p)
	default:
		return "", errors.New("unknown address type " + strconv.Itoa(int(atyp)))
	}
	if c.portFirst {
		return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
	}
	port, err = b.ReadUint16()
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// EncodeTarget returns a SOCKS/VLESS/Trojan style address, atyp included.
func EncodeTarget(address string, c targetCoding) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, err
	}
	var atyp byte
	var body []byte
	if ip := net.ParseIP(host); ip == nil {
		if len(host) > maxDomainLen {
			return nil, errors.New("domain too long")
		}
		atyp = c.domain
		body = append([]byte{byte(len(host))}, host...)
	} else if ip.To4() != nil {
		atyp = c.ipv4
		body = ip.To4()
	} else {
		atyp = c.ipv6
		body = ip.To16()
	}
	var out []byte
	if c.portFirst {
		out = []byte{byte(port >> 8), byte(port), atyp}
	} else {
		out = []byte{atyp}
	}
	out = append(out, body...)
	if !c.portFirst {
		out = append(out, byte(port>>8), byte(port))
	}
	return out, nil
}
