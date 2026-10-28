package protocols

import (
	"bytes"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/wwricu/bad-proxy-core/common"
)

/*
HTTP inbound (client-facing) supports the two shapes a proxy client sends:

	CONNECT host:port HTTP/1.1        (tunnel, then raw bytes)
	METHOD scheme://host/path HTTP/1.1 (plain proxy, absolute-form)

The request head is read field by field from the buffered stream, so a
header split across TCP segments no longer truncates the parse. For
non-CONNECT requests the original absolute-form request line is rewritten to
origin form and re-injected ahead of the body, as RFC 7230 5.4 requires of a
proxy. HTTP has no outbound direction in this build (direct routing uses the
freedom outbound).
*/

type HttpRequest struct {
	Method  string
	Address string
}

const maxHttpHeadBytes = 16 * 1024

func readRequestHead(in *common.Buffered) (HttpRequest, []byte, error) {
	var raw bytes.Buffer
	line, err := readCrlfLine(in, maxHttpHeadBytes)
	if err != nil {
		return HttpRequest{}, nil, err
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return HttpRequest{}, nil, errors.New("malformed request line: " + line)
	}
	request := HttpRequest{Method: parts[0]}
	if request.Address, err = targetFromRequestUri(parts[1], parts[0]); err != nil {
		return HttpRequest{}, nil, err
	}
	// the origin server must receive an origin-form request line (RFC 7230 5.4)
	raw.WriteString(parts[0] + " " + originFormTarget(parts[1]) + " " + parts[2])
	raw.WriteString("\r\n")
	// drain the remaining header lines
	for {
		l, err := readCrlfLine(in, maxHttpHeadBytes)
		if err != nil {
			return HttpRequest{}, nil, err
		}
		raw.WriteString(l)
		raw.WriteString("\r\n")
		if l == "" {
			break
		}
	}
	return request, raw.Bytes(), nil
}

// originFormTarget strips "scheme://host[:port]" from an absolute-form
// request target, as the origin server must receive path-only URIs.
func originFormTarget(target string) string {
	if !strings.Contains(target, "://") {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	return u.RequestURI()
}

func readCrlfLine(in *common.Buffered, limit int) (string, error) {
	var sb strings.Builder
	atEol := false
	for sb.Len() < limit {
		b, err := in.ReadByte()
		if err != nil {
			return "", err
		}
		switch {
		case atEol && b == '\n':
			return sb.String(), nil
		case b == '\r':
			atEol = true
			continue
		case atEol: // bare LF after non-CR, treat previous \r as data
			sb.WriteByte('\r')
			atEol = false
			fallthrough
		default:
			sb.WriteByte(b)
		}
	}
	return "", errors.New("http header line too long")
}

func targetFromRequestUri(target, method string) (string, error) {
	var host string
	if strings.Contains(target, "://") {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" {
			return "", errors.New("bad request target " + target)
		}
		host = parsed.Host
		if parsed.Port() == "" {
			host += ":" + schemePort(parsed.Scheme)
		}
	} else {
		host = target
		if !hasPort(host) {
			defaultPort := "80"
			if method == "CONNECT" {
				defaultPort = "443"
			}
			host = host + ":" + defaultPort
		}
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		return "", err
	}
	return host, nil
}

func schemePort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https", "wss":
		return "443"
	case "ftp":
		return "21"
	}
	return "80"
}

func hasPort(host string) bool {
	_, port, err := net.SplitHostPort(host)
	return err == nil && port != "" && allDigits(port)
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

type HttpInbound struct {
	*common.Buffered
}

func NewHttpInbound(conn net.Conn) *HttpInbound {
	return &HttpInbound{Buffered: common.NewBuffered(conn)}
}

func (in *HttpInbound) Connect() (targetAddr string, err error) {
	request, rawHead, err := readRequestHead(in.Buffered)
	if err != nil {
		return "", err
	}
	if request.Method == "CONNECT" {
		if _, err = in.Write([]byte(
			"HTTP/1.1 200 Connection Established\r\nProxy-agent: bad-proxy\r\n\r\n",
		)); err != nil {
			return "", err
		}
		return request.Address, nil
	}
	// Plain proxy request: re-inject the head, already rewritten to origin form,
	// ahead of the body we have not read yet.
	in.PushFront(rawHead)
	return request.Address, nil
}

func (in *HttpInbound) Fallback() {}
