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

type streamOneTransport struct{ xhttpEndpoint }

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

	tr := t.flowTransport()
	tr.DisableKeepAlives = true

	respCh, errCh := startFlow(tr, req, pw.CloseWithError)

	conn := &XhttpConn{writer: pw, remote: parseAddr(t.address)}
	conn.reader = &lazyBody{
		respCh: respCh,
		errCh:  errCh,
		name:   "xhttp",

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

	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

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
	<-done

	_ = rc.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, r.Body)
}
