package transport

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
)

type streamUpTransport struct{ xhttpEndpoint }

func (t streamUpTransport) Listen() (net.Listener, error) {
	return t.listen(func(_ string, depth int) bool { return depth == 1 })
}

func (t streamUpTransport) Dial() (net.Conn, error) {
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

	pr, pw := io.Pipe()
	req, err = t.newTunnelRequest(ctx, http.MethodPost, flow, pr)
	if err != nil {
		cancel()
		pw.Close()
		return nil, err
	}
	upResp, upErr := startFlow(tr, req, nil)

	conn := &XhttpConn{writer: pw, remote: parseAddr(t.address)}
	down := &lazyBody{respCh: downResp, errCh: downErr, name: "xhttp"}
	conn.reader = down
	conn.onClose = func() {
		pw.Close()
		cancel()
		tr.CloseIdleConnections()
	}
	down.onFail = func(error) { conn.Close() }
	go supervise(upResp, upErr, conn)
	return conn, nil
}

func supervise(respCh chan *http.Response, errCh chan error, conn *XhttpConn) {
	select {
	case err := <-errCh:
		logger.Debugf("xhttp: upload flow died: %v", err)
		_ = conn.Close()
	case resp := <-respCh:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			logger.Debugf("xhttp: upload flow answered %d", resp.StatusCode)
			_ = conn.Close()
		}
	}
}

func (listener *XhttpListener) serveUploadBody(w http.ResponseWriter, r *http.Request, sid string) {
	s, err := listener.session(sid, parseAddr(r.RemoteAddr))
	if err != nil {
		refuse(w, http.StatusServiceUnavailable)
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	buf := make([]byte, clientPieceBytes)
	for seq := 0; ; seq++ {
		n, err := r.Body.Read(buf)
		if n > 0 {
			piece := make([]byte, n)
			copy(piece, buf[:n])
			if derr := s.deliver(xhttpPiece{seq: seq, data: piece}); derr != nil {
				return
			}
		}
		if err != nil {
			break
		}
	}

	w.WriteHeader(http.StatusOK)
}
