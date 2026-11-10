package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	// responseHeaderTimeout is deliberately separate from the connect budget: a
	// buffering reverse proxy may legitimately hold a response head until the
	// upload body yields bytes, which takes longer than establishing the hop.
	responseHeaderTimeout = 30 * time.Second
)

// ---------------------------------------------------------------- client

// flowTransport is the connection pool one tunnel's requests share. Over TLS
// the stdlib multiplexes them over a single HTTP/2 connection by ALPN; over
// plain TCP each flow takes its own connection, which is why idle connections
// must not be evicted after one use.
func (t xhttpTransport) flowTransport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     clientTlsConfig(t.address, t.authority),
		TLSHandshakeTimeout: t.timeout,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// The context is the tunnel's own way out: closing it cancels a
			// connect that would otherwise keep dialing after the tunnel is gone.
			return (&net.Dialer{Timeout: t.timeout}).DialContext(ctx, "tcp", t.address)
		},
		MaxIdleConnsPerHost:   maxPiecesInFlight,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
}

// startFlow issues one request of a tunnel without waiting for its answer: a
// buffering front holds the response headers until the request body yields
// bytes, and the caller only writes the handshake after Dial returns, so Dial
// must not block here; the two channels are how the answer is collected later.
// onErr, when non-nil, runs before the error reaches errCh -- the duplex dials
// pass pw.CloseWithError to unblock a handshake write parked on the pipe.
func startFlow(tr *http.Transport, req *http.Request, onErr func(error) error) (chan *http.Response, chan error) {
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := tr.RoundTrip(req)
		if err != nil {
			if onErr != nil {
				onErr(err)
			}
			errCh <- err
			return
		}
		respCh <- resp
	}()
	return respCh, errCh
}

// lazyBody waits for the HTTP response (which only arrives once the request
// body has started flowing) and then reads from it. A status other than 200 is
// surfaced as a read error and tears the tunnel down, so a request the front
// rejected fails on the first read instead of hanging the handshake.
type lazyBody struct {
	respCh chan *http.Response
	errCh  chan error
	name   string // protocol name for error messages
	// wantProtoMajor rejects a response that did not negotiate the protocol the
	// transport is named after; zero accepts whatever came back.
	wantProtoMajor int
	onFail         func(error)

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
				l.err = fmt.Errorf("%s: server answered %d: %s", l.name, l.resp.StatusCode, string(body))
				l.onFail(l.err)
			} else if l.wantProtoMajor != 0 && l.resp.ProtoMajor != l.wantProtoMajor {
				_ = l.resp.Body.Close()
				l.err = fmt.Errorf("%s: front answered %s, want HTTP/%d", l.name, l.resp.Proto, l.wantProtoMajor)
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
