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
	responseHeaderTimeout = 30 * time.Second
)

func (t xhttpEndpoint) flowTransport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     clientTlsConfig(t.address, t.authority),
		TLSHandshakeTimeout: t.timeout,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {

			return (&net.Dialer{Timeout: t.timeout}).DialContext(ctx, "tcp", t.address)
		},
		MaxIdleConnsPerHost:   maxPiecesInFlight,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
}

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

type lazyBody struct {
	respCh chan *http.Response
	errCh  chan error
	name   string

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

		}
		l.ready = true
	}
	if l.err != nil {
		return 0, l.err
	}
	return l.resp.Body.Read(p)
}

type XhttpConn struct {
	reader  io.Reader
	writer  io.Writer
	onClose func()
	flush   bool
	remote  net.Addr

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
