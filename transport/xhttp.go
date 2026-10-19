package transport

/*
xhttp: a byte stream carried over plain HTTP requests, shaped exactly like
Xray's XHTTP (package splithttp) so the two interoperate in both directions.
The three modes carry Xray's own names:

	stream-one  POST <path>            duplex: request body up, streamed 200 down
	stream-up   GET  <path>/<sid>      down, and POST <path>/<sid> up
	packet-up   GET  <path>/<sid>      down, and POST <path>/<sid>/<seq> up in pieces

The mode never appears on the wire - not for Xray, so not for us either: a
server configured for one mode only restricts which request shapes it answers.

Two gates decide whether a request is a tunnel at all, and both are Xray's:
the path must sit under the normalised one (leading *and* trailing slash, so
"/xrt" is sent as "/xrt/"), and every request must carry ?x_padding= filler.
An Xray hub rejects a request without it with 400 before looking at anything
else, which is also what keeps a passing-by probe from getting a tunnel.

seq is decimal and starts at 0. Pieces carry no framing, no acknowledgement and
no retransmission: the server reorders them by seq and a lost piece stalls the
stream, in Xray as in us.

Over TLS the stdlib negotiates HTTP/2 by ALPN and each flow is one h2 stream,
so a tunnel multiplexes its requests over one connection. Over plain TCP - the
loopback hop behind nginx - stream-one runs as HTTP/1.1 chunked duplex
streaming, and the other two use two connections like any other HTTP client.

Why not WebSocket: same duplex-over-HTTP semantics, but framed messages add
bytes and it needed a third-party library; raw HTTP/1.1 chunked passes through
any proxy that permits streaming, which is exactly the nginx setup this project
targets (proxy_buffering off).

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

	"github.com/wwricu/bad-proxy-core/common"
	"github.com/wwricu/bad-proxy-core/logger"
)

const (
	xhttpContentType = "application/octet-stream"

	// PaddingParam is where the filler travels: a query parameter on every
	// request, not a header. Xray never looks at a header for it.
	PaddingParam = "x_padding"
)

// xhttp mode names, shared with Xray's config.
const (
	XhttpModeAuto      = "auto"
	XhttpModeStreamOne = "stream-one"
	XhttpModeStreamUp  = "stream-up"
	XhttpModePacketUp  = "packet-up"
)

// ResolveXhttpMode turns a configured mode into a name both endpoints speak.
// Unset stays unset rather than picking a side's default, because the two
// directions want different things from it: a client with no mode dials
// stream-one, while a server with no mode answers every shape, exactly like the
// auto it does not have to name. Writing stream-one down is therefore not the
// same as leaving it out - named, it pins the server to that one shape.
//
// Unset on the client is deliberately not Xray's "auto": every existing BPC
// config predates this field, and one long-lived POST is the shape that survives
// a buffering reverse proxy, which is what this project is usually deployed
// behind. auto is the deliberate choice to look like everybody else's gRPC
// traffic and resolves to packet-up, like Xray's.
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

var errListenerClosed = fmt.Errorf("xhttp listener closed: %w", net.ErrClosed)

// xhttpTransport is the whole xhttp endpoint: the address and path every
// request goes to, the mode that picks which requests a tunnel makes, and the
// TLS material. Padding is not among these fields: how much filler a request
// carries is a property of the transport, not a choice an endpoint makes, so
// it comes from common.DefaultPadding and no caller can set it.
type xhttpTransport struct {
	address   string
	path      string
	authority string // SNI / Host, empty means derive from the address host
	mode      string // resolved: stream-one, stream-up or packet-up
	useTLS    bool   // client only: https scheme + TLS config
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
	switch t.mode {
	case XhttpModeStreamUp:
		return t.dialStreamUp()
	case XhttpModeAuto, XhttpModePacketUp:
		return t.dialPacketUp()
	}
	// unset, and stream-one: the curated default
	return t.dialStreamOne()
}

// clientBase resolves the scheme and the Host/SNI authority, which is the
// address host unless the endpoint names a different one.
func (t xhttpTransport) clientBase() (string, string, error) {
	authorityHost, _, err := net.SplitHostPort(t.address)
	if err != nil {
		return "", "", err
	}
	authority := t.authority
	if authority == "" {
		authority = authorityHost
	}
	scheme := "http"
	if t.useTLS {
		scheme = "https"
	}
	return scheme, authority, nil
}

// tunnelURL builds one request URL for this tunnel. The path keeps its trailing
// slash and a session appends its id, and then its sequence number, as further
// segments - the same layout an Xray client puts on the wire.
func (t xhttpTransport) tunnelURL(sid string, seq int) (string, error) {
	scheme, authority, err := t.clientBase()
	if err != nil {
		return "", err
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

// newTunnelRequest builds a request with the two things every xhttp request
// must carry: the padding query, redrawn per request so the request sizes
// never settle on one value, and the body content type.
func (t xhttpTransport) newTunnelRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
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

func (t xhttpTransport) dialStreamOne() (net.Conn, error) {
	url, err := t.tunnelURL("", -1)
	if err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := t.newTunnelRequest(ctx, http.MethodPost, url, pr)
	if err != nil {
		cancel()
		pw.Close()
		return nil, err
	}

	tr := &http.Transport{
		DisableKeepAlives:     true, // one transport, one tunnel
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       clientTlsConfig(t.address, t.authority),
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return net.Dial("tcp", t.address) },
		ResponseHeaderTimeout: 30 * time.Second,
	}

	// Fire the request without blocking on the response. The server flushes its
	// 200 before reading the body, but a buffering reverse proxy (Caddy) holds
	// those response headers until the request body yields bytes -- and our
	// caller only writes the handshake after Dial returns. Waiting here would
	// deadlock the handshake against such a proxy, so resolve the response
	// lazily on the first read and let the handshake flow immediately.
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := tr.RoundTrip(req)
		if err != nil {
			// Unblock any handshake write already parked on the pipe.
			pw.CloseWithError(err)
			errCh <- err
			return
		}
		respCh <- resp
	}()

	conn := &XhttpConn{writer: pw, remote: parseAddr(t.address)}
	conn.reader = &lazyBody{
		respCh: respCh,
		errCh:  errCh,
		// A rejected request has to unblock the handshake write parked on the
		// pipe, or the tunnel dies with a timeout instead of an error.
		onFail: func(error) { conn.Close() },
	}
	conn.onClose = func() {
		pw.Close()
		cancel()
		tr.CloseIdleConnections()
	}
	return conn, nil
}

// lazyBody waits for the HTTP response (which only arrives once the request
// body has started flowing) and then reads from it. A status other than 200 is
// surfaced as a read error and tears the tunnel down, so a request the front
// rejected fails on the first read instead of hanging the handshake.
type lazyBody struct {
	respCh chan *http.Response
	errCh  chan error
	onFail func(error)

	resp  *http.Response
	err   error
	ready bool
}

func (l *lazyBody) Read(p []byte) (int, error) {
	if !l.ready {
		select {
		case l.resp = <-l.respCh:
			if l.resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(l.resp.Body, 512))
				_ = l.resp.Body.Close()
				l.err = fmt.Errorf("xhttp: server answered %d: %s", l.resp.StatusCode, string(body))
				l.onFail(l.err)
			}
		case l.err = <-l.errCh:
			// The RoundTrip goroutine already closed the pipe writer.
		}
		l.ready = true
	}
	if l.err != nil {
		return 0, l.err
	}
	return l.resp.Body.Read(p)
}

// ---------------------------------------------------------------- server

type XhttpListener struct {
	path      string
	mode      string
	addr      net.Addr
	ch        chan net.Conn
	server    *http.Server
	closed    chan struct{}
	closeOnce sync.Once

	// sessions holds the stream-up and packet-up tunnels, which are spread
	// over several requests and therefore outlive any one handler.
	mu       sync.Mutex
	sessions map[string]*xhttpSession
}

func (t xhttpTransport) Listen() (net.Listener, error) {
	listener := &XhttpListener{
		path:     xhttpPath(t.path),
		mode:     t.mode,
		addr:     parseAddr(t.address),
		ch:       make(chan net.Conn, 32),
		closed:   make(chan struct{}),
		sessions: make(map[string]*xhttpSession),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", listener.serveHTTP)

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

// serveHTTP is the single entry for every request the front forwards. Path
// first, then padding: a request that is not under the tunnel path gets the
// same 404 any other location on this host would, so the gate cannot be
// probed for the tunnel's presence.
func (listener *XhttpListener) serveHTTP(w http.ResponseWriter, r *http.Request) {
	segs, ok := xhttpSegments(listener.path, r.URL.Path)
	if !ok || !xhttpAnswers(listener.mode, r.Method, len(segs)) {
		refuse(w, http.StatusNotFound)
		return
	}
	if r.URL.Query().Get(PaddingParam) == "" {
		refuse(w, http.StatusBadRequest)
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

// refuse answers a request this endpoint will not carry, and ends the
// connection with it. A rejected upload is still streaming its body, and
// net/http drains whatever a handler left unread *before* it flushes the
// response, so the client would wait for a rejection it has already been given.
// Naming the connection closed skips that drain.
func refuse(w http.ResponseWriter, code int) {
	w.Header().Set("Connection", "close")
	w.WriteHeader(code)
}

// xhttpAnswers is the server half of the mode setting. Since the mode is never
// sent, "auto" (and an unset mode) accepts every shape the transport can speak;
// a server pinned to one mode answers only that mode's requests, which is the
// only thing pinning can usefully do.
func xhttpAnswers(mode string, method string, depth int) bool {
	if method != http.MethodGet && method != http.MethodPost {
		return false
	}
	switch mode {
	case XhttpModeStreamOne:
		return depth == 0 && method == http.MethodPost
	case XhttpModeStreamUp:
		return depth == 1
	case XhttpModePacketUp:
		return (depth == 1 && method == http.MethodGet) || (depth == 2 && method == http.MethodPost)
	}
	return depth <= 2
}

func (listener *XhttpListener) serveStreamOne(w http.ResponseWriter, r *http.Request) {
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
	conn.setRead = rc.SetReadDeadline
	conn.setWrite = rc.SetWriteDeadline
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
	// Drain the upload before returning. Closing on top of a body the client is
	// still streaming sends a reset, and a reset throws away the response bytes
	// the client has not read yet, so the tail of the download would go missing
	// on every tunnel that ends while its peer is still writing. Bounded, because
	// a client that likes the tunnel will keep streaming forever.
	_ = rc.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, r.Body)
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
		listener.dropSessions()
	})
	return nil
}

func (listener *XhttpListener) Addr() net.Addr { return listener.addr }

// session returns the tunnel for sid, starting one if this is the first of its
// requests. Whichever flow arrives first is the one that hands the connection to
// Accept, so a client that posts before it opens its download is not stalled.
func (listener *XhttpListener) session(sid string, remote net.Addr) *xhttpSession {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	if s, ok := listener.sessions[sid]; ok {
		return s
	}
	s := newXhttpSession(sid, remote, listener)
	listener.sessions[sid] = s
	go func() {
		select {
		case listener.ch <- s:
		case <-s.done:
		case <-listener.closed:
			s.close()
		}
	}()
	go s.reap()
	return s
}

func (listener *XhttpListener) forget(sid string) {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	delete(listener.sessions, sid)
}

// dropSessions tears every split-flow tunnel down. Closing one wants the map
// lock to remove itself, so the list is taken first and closed outside it.
func (listener *XhttpListener) dropSessions() {
	listener.mu.Lock()
	pending := make([]*xhttpSession, 0, len(listener.sessions))
	for _, s := range listener.sessions {
		pending = append(pending, s)
	}
	listener.sessions = make(map[string]*xhttpSession)
	listener.mu.Unlock()
	for _, s := range pending {
		s.close()
	}
}

// ---------------------------------------------------------------- shared conn

// XhttpConn adapts one duplex HTTP POST (request body up, response body down)
// to net.Conn. The session modes use xhttpSession instead, which carries the
// same shape.
type XhttpConn struct {
	reader  io.Reader
	writer  io.Writer
	onClose func()
	flush   bool
	remote  net.Addr

	// deadline hooks, populated only where the underlying HTTP machinery can
	// enforce them (the server side, via the ResponseController). They let the
	// core's handshake timeout bound a stalled request body read; the client's
	// response wait is bounded by ResponseHeaderTimeout instead, so these stay
	// nil there and the deadline calls are inert no-ops.
	setRead  func(time.Time) error
	setWrite func(time.Time) error

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

func (c *XhttpConn) SetDeadline(t time.Time) error {
	errR, errW := c.SetReadDeadline(t), c.SetWriteDeadline(t)
	if errR != nil {
		return errR
	}
	return errW
}

func (c *XhttpConn) SetReadDeadline(t time.Time) error {
	if c.setRead == nil {
		return nil
	}
	return c.setRead(t)
}

func (c *XhttpConn) SetWriteDeadline(t time.Time) error {
	if c.setWrite == nil {
		return nil
	}
	return c.setWrite(t)
}

// ---------------------------------------------------------------- helpers

// xhttpPath normalises a configured path the way Xray does: leading and
// trailing slash. The trailing one is not cosmetics - an Xray hub compares
// paths by prefix against its own normalised path, so a client that posts to
// "/xrt" while the server serves "/xrt/" is a 404 away from connecting.
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

// xhttpSegments splits what follows the tunnel path into at most two segments:
// the session id and, for packet-up, the sequence number. The path matches with
// or without its trailing slash on either side, because a reverse proxy in
// front may add or eat it.
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

// paddingLength draws a filler length from the "min-max" spec, to blur the
// request-size fingerprint. An empty or non-positive spec yields 0, which is
// how the transport stays quiet if the default ever becomes one that says so.
func paddingLength(spec string) int {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0
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
		return 0
	}
	n := low
	if r, err := rand.Int(rand.Reader, big.NewInt(int64(high-low+1))); err == nil {
		n = low + int(r.Int64())
	}
	return n
}

// randomPadding returns Xray's repeat-x filler: a run of 'X' of a random
// length. The content is fixed on purpose - it is what the protocol asks the
// server to echo back, and a client that sent something else would advertise
// itself. What carries the information is the length, which is redrawn for
// every request.
func randomPadding(spec string) string {
	n := paddingLength(spec)
	if n <= 0 {
		return ""
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
