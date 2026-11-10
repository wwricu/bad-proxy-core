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

What decides whether a request is a tunnel is the path, normalised the way
Xray normalises it: a leading *and* a trailing slash, so a configured "/xrt"
is sent as "/xrt/" and matched as a prefix. The ?x_padding= parameter is not
a gate - measured against Xray 26.3.27, its server answers an unpadded request
exactly as it answers a padded one. Padding is only a size bluffer: BPC's
client keeps drawing filler for it (common.DefaultPadding) because Xray's
server accepts it, and a server that demanded one here would lock out Xray's
own default configuration.

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
	// The protocol does not need a content type, but leaving the stream
	// untyped makes it look like a bare 200.
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
	timeout   time.Duration
}

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

// newTunnelRequest builds a request the way an Xray one looks: the padding
// query, redrawn per request so the request sizes never settle on one value,
// and the body content type. Xray's server does not require the parameter, but
// a request without it is a fixed-size giveaway.
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
	// over several requests and therefore outlive any one handler. The cap is
	// checked before a new session is created, because nothing about the
	// request is authenticated yet.
	mu          sync.Mutex
	sessions    map[string]*xhttpSession
	maxSessions int
}

func (t xhttpTransport) Listen() (net.Listener, error) {
	listener := &XhttpListener{
		path:        xhttpPath(t.path),
		mode:        t.mode,
		addr:        parseAddr(t.address),
		ch:          make(chan net.Conn, 32),
		closed:      make(chan struct{}),
		sessions:    make(map[string]*xhttpSession),
		maxSessions: maxSplitSessions,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", listener.serveHTTP)

	var protocols http.Protocols
	protocols.SetHTTP1(true) // HTTP/1.1 chunked duplex, the nginx-hop mode
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:   mux,
		Protocols: &protocols,
		// A handler clears the deadlines once a tunnel exists; before that, a
		// peer that sends half a request head must not pin the connection.
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

// serveHTTP is the single entry for every request the front forwards. The path
// is the whole gate: a request that is not under the tunnel path gets the same
// 404 any other location on this host would, so it cannot be probed for the
// tunnel's presence.
func (listener *XhttpListener) serveHTTP(w http.ResponseWriter, r *http.Request) {
	segs, ok := xhttpSegments(listener.path, r.URL.Path)
	if !ok || !xhttpAnswers(listener.mode, r.Method, len(segs)) {
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
