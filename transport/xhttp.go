package transport

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwricu/bad-proxy-core/common"
	"github.com/wwricu/bad-proxy-core/logger"
)

const (
	xhttpContentType = "application/octet-stream"

	PaddingParam = "x_padding"
)

const (
	XhttpModeAuto      = "auto"
	XhttpModeStreamOne = "stream-one"
	XhttpModeStreamUp  = "stream-up"
	XhttpModePacketUp  = "packet-up"
)

func ResolveXhttpMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case XhttpModeAuto:
		return XhttpModeAuto
	case XhttpModeStreamUp:
		return XhttpModeStreamUp
	case XhttpModePacketUp:
		return XhttpModePacketUp
	case XhttpModeStreamOne:
		return XhttpModeStreamOne
	case "":
		return ""
	}
	logger.Warnf("xhttp: unknown mode %q, using the endpoint default", mode)
	return ""
}

type xhttpEndpoint struct {
	address   string
	path      string
	authority string
	useTLS    bool
	certPath  string
	keyPath   string
	timeout   time.Duration
}

func newXhttp(o Options, timeout time.Duration) Transport {
	endpoint := xhttpEndpoint{
		address:   o.Address,
		path:      o.Path,
		authority: o.Authority,
		useTLS:    o.UseTLS,
		certPath:  o.TlsCertPath,
		keyPath:   o.TlsKeyPath,
		timeout:   timeout,
	}
	switch ResolveXhttpMode(o.Mode) {
	case XhttpModeStreamUp:
		return streamUpTransport{endpoint}
	case XhttpModePacketUp:
		return packetUpTransport{endpoint}
	case XhttpModeAuto:
		return autoTransport{packetUpTransport{endpoint}}
	case XhttpModeStreamOne:
		return streamOneTransport{endpoint}
	}
	return defaultTransport{streamOneTransport{endpoint}}
}

type autoTransport struct{ packetUpTransport }

func (t autoTransport) Listen() (net.Listener, error) {
	return t.listen(xhttpAnswersAny)
}

type defaultTransport struct{ streamOneTransport }

func (t defaultTransport) Listen() (net.Listener, error) {
	return t.listen(xhttpAnswersAny)
}

func xhttpAnswersAny(_ string, depth int) bool { return depth <= 2 }

func (t xhttpEndpoint) tunnelURL(sid string, seq int) (string, error) {
	host, _, err := net.SplitHostPort(t.address)
	if err != nil {
		return "", err
	}
	authority := t.authority
	if authority == "" {
		authority = host
	}
	scheme := "http"
	if t.useTLS {
		scheme = "https"
	}
	url := scheme + "://" + authority + xhttpPath(t.path)
	if sid != "" {
		url += sid
		if seq >= 0 {
			url += "/" + strconv.Itoa(seq)
		}
	}
	return url, nil
}

func (t xhttpEndpoint) newTunnelRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if padding := randomPadding(common.DefaultPadding); padding != "" {
		req.URL.RawQuery = PaddingParam + "=" + padding
	}
	req.Header.Set("Content-Type", xhttpContentType)
	return req, nil
}

type XhttpListener struct {
	path string

	answers   func(method string, depth int) bool
	addr      net.Addr
	ch        chan net.Conn
	server    *http.Server
	closed    chan struct{}
	closeOnce sync.Once

	mu          sync.Mutex
	sessions    map[string]*xhttpSession
	maxSessions int
}

func (t xhttpEndpoint) listen(answers func(method string, depth int) bool) (net.Listener, error) {
	listener := &XhttpListener{
		path:        xhttpPath(t.path),
		answers:     answers,
		addr:        parseAddr(t.address),
		ch:          make(chan net.Conn, 32),
		closed:      make(chan struct{}),
		sessions:    make(map[string]*xhttpSession),
		maxSessions: maxSplitSessions,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", listener.serveHTTP)

	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:   mux,
		Protocols: &protocols,

		ReadHeaderTimeout: common.DefaultHandshakeTimeout,
	}
	listener.server = server

	tcpListener, err := net.Listen("tcp", t.address)
	if err != nil {
		return nil, err
	}
	if t.certPath != "" && t.keyPath != "" {
		cert, err := tls.LoadX509KeyPair(t.certPath, t.keyPath)
		if err != nil {
			tcpListener.Close()
			return nil, err
		}
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"h2", "http/1.1"},
		}
	}

	go func() {
		var err error
		if server.TLSConfig != nil {
			err = server.ServeTLS(tcpListener, "", "")
		} else {
			err = server.Serve(tcpListener)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			logger.Errorf("xhttp server on %s dead: %v", t.address, err)
		}
		listener.Close()
	}()
	return listener, nil
}

func (listener *XhttpListener) serveHTTP(w http.ResponseWriter, r *http.Request) {
	segs, ok := xhttpSegments(listener.path, r.URL.Path)
	if !ok || (r.Method != http.MethodGet && r.Method != http.MethodPost) || !listener.answers(r.Method, len(segs)) {
		refuse(w, http.StatusNotFound)
		return
	}
	switch {
	case len(segs) == 0:
		listener.serveStreamOne(w, r)
	case len(segs) == 1:
		if r.Method == http.MethodGet {
			listener.serveDownlink(w, r, segs[0])
		} else {
			listener.serveUploadBody(w, r, segs[0])
		}
	default:
		seq, err := strconv.Atoi(segs[1])
		if err != nil || seq < 0 {
			refuse(w, http.StatusBadRequest)
			return
		}
		listener.servePiece(w, r, segs[0], seq)
	}
}

func refuse(w http.ResponseWriter, code int) {
	w.Header().Set("Connection", "close")
	w.WriteHeader(code)
}

func (listener *XhttpListener) Accept() (net.Conn, error) {
	select {
	case <-listener.closed:
		return nil, net.ErrClosed
	case conn := <-listener.ch:
		return conn, nil
	}
}

func (listener *XhttpListener) Close() error {
	listener.closeOnce.Do(func() {
		close(listener.closed)
		if listener.server != nil {
			_ = listener.server.Close()
		}
		listener.dropSessions()
	})
	return nil
}

func (listener *XhttpListener) Addr() net.Addr { return listener.addr }

func xhttpPath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		return path + "/"
	}
	return path
}

func xhttpSegments(want, got string) ([]string, bool) {
	base := strings.TrimSuffix(xhttpPath(want), "/")
	if !strings.HasPrefix(got, base) {
		return nil, false
	}
	rest := got[len(base):]
	if rest == "" || rest == "/" {
		return nil, true
	}
	if rest[0] != '/' {
		return nil, false
	}
	segs := make([]string, 0, 2)
	for _, s := range strings.Split(strings.Trim(rest, "/"), "/") {
		if s == "" {
			continue
		}
		if s == "." || s == ".." {
			return nil, false
		}
		segs = append(segs, s)
	}
	if len(segs) > 2 {
		return nil, false
	}
	return segs, true
}

func randomHex(n int) string {
	p := make([]byte, n)
	_, _ = rand.Read(p)
	return hex.EncodeToString(p)
}

func randomPadding(spec string) string {
	spec = strings.TrimSpace(spec)
	low, high := 0, 0
	if i := strings.IndexByte(spec, '-'); i >= 0 {
		low, _ = strconv.Atoi(spec[:i])
		high, _ = strconv.Atoi(spec[i+1:])
	} else {
		low, _ = strconv.Atoi(spec)
		high = low
	}
	if high < low {
		low, high = high, low
	}
	if high <= 0 {
		return ""
	}
	n := low
	if r, err := rand.Int(rand.Reader, big.NewInt(int64(high-low+1))); err == nil {
		n = low + int(r.Int64())
	}
	return strings.Repeat("X", n)
}

func parseAddr(address string) net.Addr {
	tcpAddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return nil
	}
	return tcpAddr
}
