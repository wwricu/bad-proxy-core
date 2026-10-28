package transport

/*
http2: the transport Xray calls network "http" - one HTTP/2 request carries
both directions, its request body is the uplink and its response body the
downlink. On the wire it is a single PUT (Xray's default method) with
"Accept-Encoding: identity" to the configured path:

	client                               server
	PUT <path>  ->  request body up, streamed 200 down

The server answers 200 and flushes before it has read anything, exactly like
Xray's hub, because the client does not wait for it before its handshake. A
request naming a host or a path the endpoint was not configured for gets a 404,
which is also how Xray's hub refuses one.

Encryption is the TLS layer's job: a client needs it (Xray's own refuses to run
without it), and a server serves h2c when no cert is configured and TLS with
ALPN h2 when one is. That is the deployment shape this project wants - Caddy
terminates the public connection and reaches the core over h2c - and also what
lets the two endpoints talk directly in tests.
*/

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wwricu/bad-proxy-core/common"
	"github.com/wwricu/bad-proxy-core/logger"
)

const (
	// h2ContentType is what the response body carries. The protocol does not
	// need it, but leaving the stream untyped makes it look like a bare 200.
	h2ContentType = "application/octet-stream"

	// maxHttp2Tunnels caps the requests an unauthenticated peer may hold at
	// once. Every tunnel is a stream, a goroutine and a relay before any
	// credential is checked; without the cap a peer opens them until memory is
	// gone. Xray has no equivalent because its hub hands each request to the
	// transport layer unbounded.
	maxHttp2Tunnels = 4096
)

// http2Transport is one endpoint: the address and path every request goes to,
// the Host the client names (and the server accepts), the TLS material, and the
// connect budget.
type http2Transport struct {
	address   string
	path      string
	authority string // Host header; server: the only host it answers
	useTLS    bool   // client only
	certPath  string // server only
	keyPath   string // server only
	timeout   time.Duration

	maxTunnels int // server only; 0 means maxHttp2Tunnels
}

// ---------------------------------------------------------------- client

func (t http2Transport) Dial() (net.Conn, error) {
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	scheme := "http"
	if t.useTLS {
		scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		scheme+"://"+t.address+http2Path(t.path), pr)
	if err != nil {
		cancel()
		pw.Close()
		return nil, err
	}
	// The Host the request names is the one Xray's hub checks against its
	// configured hosts; unset falls back to the address, as it does there.
	if t.authority != "" {
		req.Host = t.authority
	}
	// Xray's dialer disables server compression with the same header; a
	// compressed body would break the byte stream.
	req.Header.Set("Accept-Encoding", "identity")

	tr := t.clientTransport()

	// The same lazy response the xhttp stream mode uses: the server flushes its
	// 200 before reading the body, but a buffering front (Caddy) holds those
	// headers until the body yields bytes, and the caller only writes the
	// handshake after Dial returns. Resolving the response on the first read
	// keeps the handshake from deadlocking against such a front.
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := tr.RoundTrip(req)
		if err != nil {
			pw.CloseWithError(err)
			errCh <- err
			return
		}
		respCh <- resp
	}()

	conn := &XhttpConn{writer: pw, remote: parseAddr(t.address)}
	conn.reader = &lazyBody{
		respCh:         respCh,
		errCh:          errCh,
		name:           "http2",
		wantProtoMajor: 2,
		onFail:         func(error) { conn.Close() },
	}
	conn.onClose = func() {
		pw.Close()
		cancel()
		tr.CloseIdleConnections()
	}
	return conn, nil
}

// clientTransport is the connection pool this tunnel's request lives in. One
// request per tunnel, so the pool holds exactly that one connection and it goes
// away with the tunnel.
func (t http2Transport) clientTransport() *http.Transport {
	tr := &http.Transport{
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: responseHeaderTimeout,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: t.timeout}).DialContext(ctx, "tcp", t.address)
		},
	}
	if t.useTLS {
		tr.ForceAttemptHTTP2 = true
		tr.TLSClientConfig = clientTlsConfig(t.address, t.authority)
		tr.TLSHandshakeTimeout = t.timeout
		return tr
	}
	// Over plaintext the only HTTP/2 there is is h2c with prior knowledge,
	// which is what Xray's server speaks when it has no TLS config.
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	tr.Protocols = &protocols
	return tr
}

// ---------------------------------------------------------------- server

type http2Listener struct {
	path      string
	authority string
	addr      net.Addr
	ch        chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
	server    *http.Server
	slots     chan struct{}
}

func (t http2Transport) Listen() (net.Listener, error) {
	limit := t.maxTunnels
	if limit <= 0 {
		limit = maxHttp2Tunnels
	}
	listener := &http2Listener{
		path:      http2Path(t.path),
		authority: t.authority,
		addr:      parseAddr(t.address),
		ch:        make(chan net.Conn, 32),
		closed:    make(chan struct{}),
		slots:     make(chan struct{}, limit),
	}

	// HTTP/2 over TLS is what Xray's client insists on; HTTP/1.1 is what Caddy's
	// reverse proxy speaks upward; unencrypted HTTP/2 covers a plain hop. The
	// Protocols list, not TLSConfig.NextProtos, decides the ALPN offer.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:           http.HandlerFunc(listener.serve),
		Protocols:         &protocols,
		ReadHeaderTimeout: common.DefaultHandshakeTimeout,
	}
	if t.certPath != "" && t.keyPath != "" {
		cert, err := tls.LoadX509KeyPair(t.certPath, t.keyPath)
		if err != nil {
			return nil, err
		}
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			// h2 first: Xray's client refuses any other ALPN answer.
			NextProtos: []string{"h2", "http/1.1"},
		}
	}
	listener.server = server

	tcpListener, err := net.Listen("tcp", t.address)
	if err != nil {
		return nil, err
	}
	go func() {
		var err error
		if server.TLSConfig != nil {
			err = server.ServeTLS(tcpListener, "", "")
		} else {
			err = server.Serve(tcpListener)
		}
		if err != nil && err != http.ErrServerClosed && err != net.ErrClosed {
			logger.Errorf("http2 server on %s dead: %v", t.address, err)
		}
		listener.Close()
	}()
	return listener, nil
}

// serve answers one request. The host and the path prefix are the whole gate,
// and both are checked the way Xray's hub checks them.
func (l *http2Listener) serve(w http.ResponseWriter, r *http.Request) {
	if !hostMatches(r.Host, l.authority) || !strings.HasPrefix(r.URL.Path, l.path) {
		h2Refuse(w, r, http.StatusNotFound)
		return
	}
	select {
	case l.slots <- struct{}{}:
		defer func() { <-l.slots }()
	default:
		h2Refuse(w, r, http.StatusServiceUnavailable)
		return
	}

	rc := http.NewResponseController(w)
	// A handler clears whatever deadline the server inherited; a tunnel that
	// merely goes quiet must not be cut by the header budget.
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	if r.ProtoMajor == 1 {
		// Without this, net/http closes the request body as soon as the
		// response header is written, which kills the duplex hop from Caddy.
		if err := rc.EnableFullDuplex(); err != nil {
			logger.Warnf("http2: full duplex unavailable: %v", err)
		}
	}

	done := make(chan struct{})
	var once sync.Once
	conn := &XhttpConn{reader: r.Body, writer: w, flush: true, remote: parseAddr(r.RemoteAddr)}
	// The deadlines are wired only where the HTTP machinery enforces them: an
	// h2 stream may report them unsupported, and a conn that claimed to honour
	// a deadline it cannot enforce would mislead the core's handshake budget.
	if rc.SetReadDeadline(time.Time{}) == nil {
		conn.setRead = rc.SetReadDeadline
	}
	if rc.SetWriteDeadline(time.Time{}) == nil {
		conn.setWrite = rc.SetWriteDeadline
	}
	conn.onClose = func() {
		once.Do(func() { close(done) })
		// Unblock a relay Read parked on the request body.
		_ = rc.SetReadDeadline(time.Now())
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", h2ContentType)
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	select {
	case l.ch <- conn:
	case <-r.Context().Done():
		return
	case <-l.closed:
		return
	}
	<-done
	// Drain before returning, exactly as the xhttp stream mode does: a stream
	// reset on top of an upload the peer is still sending also throws away the
	// response bytes it has not read, and the tail of the download would go
	// missing on every tunnel that ends first from this side.
	_ = rc.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, r.Body)
}

func (l *http2Listener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case conn := <-l.ch:
		return conn, nil
	}
}

func (l *http2Listener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		if l.server != nil {
			_ = l.server.Close()
		}
	})
	return nil
}

func (l *http2Listener) Addr() net.Addr { return l.addr }

// h2Refuse rejects a request this endpoint will not carry. The Connection
// header ends an HTTP/1.1 connection before net/http drains an upload nobody
// will read; HTTP/2 forbids the header, and has no drain to skip.
func h2Refuse(w http.ResponseWriter, r *http.Request, code int) {
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "close")
	}
	w.WriteHeader(code)
}

// ---------------------------------------------------------------- helpers

// http2Path normalises a configured path the way Xray's http transport does:
// a leading slash and nothing else. The trailing slash is significant here, so
// unlike xhttp it is not added - the server compares by prefix, so "/h2" also
// matches "/h2/anything", but a client that appended one would no longer match
// a server configured with the bare path.
func http2Path(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

// hostMatches compares a request's Host with the configured one, ignoring the
// port and case: Xray's hub does the same through IsValidHTTPHost.
func hostMatches(requestHost, want string) bool {
	if want == "" {
		return true
	}
	if h, _, err := net.SplitHostPort(requestHost); err == nil {
		requestHost = h
	}
	if h, _, err := net.SplitHostPort(want); err == nil {
		want = h
	}
	return strings.EqualFold(requestHost, want)
}
