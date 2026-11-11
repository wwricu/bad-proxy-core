package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/wwricu/bad-proxy-core/logger"
)

const (
	clientPieceBytes = 64 * 1024

	maxPieceBytes = 1000000

	maxPiecesInFlight = 16
)

type packetUpTransport struct{ xhttpEndpoint }

func (t packetUpTransport) Dial() (net.Conn, error) {
	sid := randomHex(8)
	flow, err := t.tunnelURL(sid, -1)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	tr := t.flowTransport()

	req, err := t.newTunnelRequest(ctx, http.MethodGet, flow, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	downResp, downErr := startFlow(tr, req, nil)

	pieces := &packetWriter{window: make(chan struct{}, maxPiecesInFlight), dead: make(chan struct{})}
	pieces.send = func(seq int, body io.Reader) (*http.Response, error) {
		req, err := t.newTunnelRequest(ctx, http.MethodPost, flow+"/"+strconv.Itoa(seq), body)
		if err != nil {
			return nil, err
		}
		return tr.RoundTrip(req)
	}

	conn := &XhttpConn{writer: pieces, remote: parseAddr(t.address)}
	down := &lazyBody{respCh: downResp, errCh: downErr, name: "xhttp"}
	conn.reader = down
	pieces.onFail = func(error) { conn.Close() }
	conn.onClose = func() {
		pieces.shutdown(nil)
		cancel()
		tr.CloseIdleConnections()
	}
	down.onFail = func(error) { conn.Close() }
	return conn, nil
}

type packetWriter struct {
	send   func(seq int, body io.Reader) (*http.Response, error)
	onFail func(error)

	window chan struct{}
	dead   chan struct{}

	mu       sync.Mutex
	seq      int
	err      error
	stopped  sync.Once
	notified sync.Once
}

func (w *packetWriter) shutdown(err error) {
	w.mu.Lock()
	if w.err == nil {
		if err == nil {

			err = io.ErrClosedPipe
		}
		w.err = err
	}
	w.mu.Unlock()
	w.stopped.Do(func() { close(w.dead) })
}

func (w *packetWriter) kill(err error) {
	w.shutdown(err)
	if err != nil {
		w.notified.Do(func() {
			if w.onFail != nil {
				w.onFail(err)
			}
		})
	}
}

func (w *packetWriter) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *packetWriter) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		if err := w.failure(); err != nil {
			return total, err
		}
		n := len(b)
		if n > clientPieceBytes {
			n = clientPieceBytes
		}
		piece := make([]byte, n)
		copy(piece, b[:n])
		if err := w.post(piece); err != nil {
			return total, err
		}
		total += n
		b = b[n:]
	}
	return total, nil
}

func (w *packetWriter) post(piece []byte) error {
	select {
	case w.window <- struct{}{}:
	case <-w.dead:
		return w.failure()
	}

	w.mu.Lock()
	seq, err := w.seq, w.err
	if err == nil {
		w.seq++
	}
	w.mu.Unlock()
	if err != nil {
		<-w.window
		return err
	}

	go func() {
		defer func() { <-w.window }()
		resp, err := w.send(seq, bytes.NewReader(piece))
		if err != nil {
			w.kill(fmt.Errorf("xhttp: piece %d: %w", seq, err))
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			w.kill(fmt.Errorf("xhttp: piece %d answered %d", seq, resp.StatusCode))
		}
	}()
	return nil
}

func (listener *XhttpListener) servePiece(w http.ResponseWriter, r *http.Request, sid string, seq int) {
	s, err := listener.session(sid, parseAddr(r.RemoteAddr))
	if err != nil {
		refuse(w, http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPieceBytes+1))
	if err != nil {
		s.close()
		return
	}
	if len(body) > maxPieceBytes {
		refuse(w, http.StatusRequestEntityTooLarge)
		return
	}
	if len(body) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := s.deliver(xhttpPiece{seq: seq, data: body}); err != nil {
		logger.Debugf("%v", err)
		refuse(w, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
