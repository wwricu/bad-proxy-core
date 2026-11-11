package transport

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
)

// streamOneTransport carries the tunnel as one duplex POST: body up, streamed 200 down.
type streamOneTransport struct{ xhttpEndpoint }

func (t streamOneTransport) Listen() (net.Listener, error) {
	return t.listen(func(method string, depth int) bool {
		return depth == 0 && method == http.MethodPost
	})
}

func (t streamOneTransport) Dial() (net.Conn, error) {
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

	tr := t.flowTransport()     // shared client transport: one source for the budgets
	tr.DisableKeepAlives = true // one request, one connection: it goes away with the tunnel

	respCh, errCh := startFlow(tr, req, pw.CloseWithError)

	conn := &XhttpConn{writer: pw, remote: parseAddr(t.address)}
	conn.reader = &lazyBody{
		respCh: respCh,
		errCh:  errCh,
		name:   "xhttp",
		// A rejected request must unblock the handshake write parked on the pipe.
		onFail: func(error) { conn.Close() },
	}
	conn.onClose = func() {
		pw.Close()
		cancel()
		tr.CloseIdleConnections()
	}
	return conn, nil
}

func (listener *XhttpListener) serveStreamOne(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// Clear server-inherited deadlines: tunnels must survive idle keep-alive.
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	// Without this net/http closes the request body once the response header is
	// written, killing the duplex stream when the first upload chunk is small.
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
	// Drain the upload before returning: closing on a body the client is still
	// streaming sends a reset that throws away unread response bytes, losing the
	// download's tail. Bounded, since a happy client streams forever.
	_ = rc.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, r.Body)
}
