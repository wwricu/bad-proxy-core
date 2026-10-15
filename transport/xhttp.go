package transport

/*
xhttp: a bidirectional byte stream over one long-lived HTTP POST, in the
spirit of Xray's XHTTP "stream" mode.

	client  ->  POST <path>            Content-Type: application/octet-stream
	                                    X-Session-Id: <8 random bytes hex>
	                                    X-Sec-Mode: stream-one
	                                    X-Padding: <optional random filler>
	                                    body: upstream stream
	server  ->  200 OK, streamed body: downstream stream

Over TLS the stdlib negotiates HTTP/2 by ALPN and each tunnel is one h2
stream (headers are HPACK-deduplicated, one connection multiplexes many
tunnels). Over plain TCP - the loopback hop behind nginx - it runs as
HTTP/1.1 chunked duplex streaming.

Why not WebSocket: same duplex-over-HTTP semantics, but framed messages add
bytes and it needed a third-party library; raw HTTP/1.1 chunked passes
through any proxy that permits streaming, which is exactly the nginx setup
this project targets (proxy_buffering off).

Why the old "http2" transport is gone: it was server-side only, the client
silently fell back to raw TCP, so BTP/Trojan over it never worked.
*/

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
)

const (
	xhttpContentType = "application/octet-stream"
	HeaderSessionId  = "X-Session-Id"
	HeaderPadding    = "X-Padding"
	HeaderSecMode    = "X-Sec-Mode"
	SecModeStream    = "stream-one"
)

var errListenerClosed = fmt.Errorf("xhttp listener closed: %w", net.ErrClosed)

// xhttpTransport is a full-duplex byte stream carried over one long-lived HTTP
// POST. It genuinely reads every endpoint field (path, authority, padding, TLS
// material), so it carries the whole set - but as named struct fields, not as a
// grab-bag handed to each call.
type xhttpTransport struct {
	address   string
	path      string
	authority string // SNI / Host, empty means derive from the address host
	padding   string
	useTLS    bool   // client only: https scheme + TLS config
	insecure  bool   // client only: skip verification
	certPath  string // server only
	keyPath   string // server only
}

// DialXhttp and ListenXhttp are package-level shorthands over the flat Options
// so tests and quick call sites need no struct; they build the transport via New
// and run its method.
func DialXhttp(o Options) (net.Conn, error) { return New(XHTTP, o).Dial() }

func ListenXhttp(o Options) (net.Listener, error) { return New(XHTTP, o).Listen() }

// ---------------------------------------------------------------- client

func (t xhttpTransport) Dial() (net.Conn, error) {
	authorityHost, _, err := net.SplitHostPort(t.address)
	if err != nil {
		return nil, err
	}
	authority := t.authority
	if authority == "" {
		authority = authorityHost
	}
	scheme := "http"
	if t.useTLS {
		scheme = "https"
	}

	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, scheme+"://"+authority+pathOrSlash(t.path), pr,
	)
	if err != nil {
		cancel()
		pw.Close()
		return nil, err
	}
	req.Header.Set(HeaderSessionId, randomHex(8))
	req.Header.Set(HeaderSecMode, SecModeStream)
	req.Header.Set("Content-Type", xhttpContentType)
	if padding := randomPadding(t.padding); padding != "" {
		req.Header.Set(HeaderPadding, padding)
	}

	tr := &http.Transport{
		DisableKeepAlives:     true, // one transport, one tunnel
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       clientTlsConfig(t.address, t.authority, t.insecure),
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return net.Dial("tcp", t.address) },
		ResponseHeaderTimeout: 30 * time.Second,
	}
	// RoundTrip returns as soon as response headers arrive; the server sends
	// them before the body is ever read, so the open stream cannot deadlock.
	resp, err := tr.RoundTrip(req)
	if err != nil {
		pw.Close()
		cancel()
		tr.CloseIdleConnections()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		pw.Close()
		cancel()
		tr.CloseIdleConnections()
		return nil, fmt.Errorf("xhttp: server answered %d: %s", resp.StatusCode, string(body))
	}

	conn := &XhttpConn{reader: resp.Body, remote: parseAddr(t.address)}
	conn.writer = pw
	conn.onClose = func() {
		pw.Close()
		cancel()
		tr.CloseIdleConnections()
	}
	return conn, nil
}

// ---------------------------------------------------------------- server

type XhttpListener struct {
	path      string
	addr      net.Addr
	ch        chan net.Conn
	server    *http.Server
	closed    chan struct{}
	closeOnce sync.Once
}

func (t xhttpTransport) Listen() (net.Listener, error) {
	listener := &XhttpListener{
		path:   pathOrSlash(t.path),
		addr:   parseAddr(t.address),
		ch:     make(chan net.Conn, 32),
		closed: make(chan struct{}),
	}

	mux := http.NewServeMux()
	handler := listener.serve
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == listener.path && r.Method == http.MethodPost {
			handler(w, r)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})

	var protocols http.Protocols
	protocols.SetHTTP1(true) // HTTP/1.1 chunked duplex, the nginx-hop mode
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{Handler: mux, Protocols: &protocols}
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

func (listener *XhttpListener) serve(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// Clear any server-inherited deadlines: tunnels must survive idle keep
	// alive, the nginx guide covers the matching proxy_*_timeout settings.
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	// Without this, net/http consumes and closes the request body as soon as
	// the handler starts writing the response header, which kills the duplex
	// stream whenever the first upload chunk is small (Content-Length case).
	if err := rc.EnableFullDuplex(); err != nil {
		logger.Warnf("xhttp: full duplex unavailable: %v", err)
	}

	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	conn := &XhttpConn{reader: r.Body, writer: w, flush: true, remote: parseAddr(r.RemoteAddr)}
	conn.onClose = func() {
		closeDone()
		// unblock a relay Read parked on the request body
		_ = rc.SetReadDeadline(time.Now())
	}

	w.Header().Set("Content-Type", xhttpContentType)
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	select {
	case listener.ch <- conn:
	case <-r.Context().Done():
		return
	case <-listener.closed:
		return
	}
	<-done // relay closed the tunnel (or peer vanished)
}

func (listener *XhttpListener) Accept() (net.Conn, error) {
	select {
	case <-listener.closed:
		return nil, errListenerClosed
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
	})
	return nil
}

func (listener *XhttpListener) Addr() net.Addr { return listener.addr }

// ---------------------------------------------------------------- shared conn

// XhttpConn adapts one HTTP POST request/response pair to net.Conn.
type XhttpConn struct {
	reader  io.Reader
	writer  io.Writer
	onClose func()
	flush   bool
	remote  net.Addr

	closeOnce sync.Once
}

func (c *XhttpConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func (c *XhttpConn) Write(b []byte) (int, error) {
	n, err := c.writer.Write(b)
	if err == nil && c.flush {
		if f, ok := c.writer.(http.Flusher); ok {
			f.Flush()
		}
	}
	return n, err
}

func (c *XhttpConn) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}

func (c *XhttpConn) LocalAddr() net.Addr  { return c.remote }
func (c *XhttpConn) RemoteAddr() net.Addr { return c.remote }

func (c *XhttpConn) SetDeadline(t time.Time) error { return nil }

func (c *XhttpConn) SetReadDeadline(t time.Time) error { return nil }

func (c *XhttpConn) SetWriteDeadline(t time.Time) error { return nil }

// ---------------------------------------------------------------- helpers

func pathOrSlash(path string) string {
	if path == "" || !strings.HasPrefix(path, "/") {
		return "/"
	}
	return path
}

func randomHex(n int) string {
	p := make([]byte, n)
	_, _ = rand.Read(p)
	return hex.EncodeToString(p)
}

// randomPadding produces a header value of random length within [min,max],
// e.g. "100-1000", to blur the request-size fingerprint.
func randomPadding(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
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
	p := make([]byte, n)
	for i := range p {
		p[i] = byte('a' + i%26)
	}
	return string(p)
}

func parseAddr(address string) net.Addr {
	tcpAddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return nil
	}
	return tcpAddr
}
