package transport

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

type Http2Listener struct {
	addr net.Addr
	ch   chan net.Conn
}

func (listener *Http2Listener) Accept() (conn net.Conn, err error) {
	if listener.ch == nil {
		err = errors.New("nil channel")
		return
	}
	return <-listener.ch, nil
}

func (listener *Http2Listener) Close() error {
	return nil
}

func (listener *Http2Listener) Addr() net.Addr {
	return listener.addr
}

func (listener *Http2Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Fatalln(errors.New("http2 server does not support flusher"))
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)

	closeCh := make(chan struct{})

	conn := &H2Conn{
		reader:  r.Body,
		writer:  w,
		flusher: flusher,
		closeCh: closeCh,
	}

	listener.ch <- conn

	<-closeCh // wait close signal
}

type H2Conn struct {
	reader    io.Reader
	writer    io.Writer
	flusher   http.Flusher
	closeCh   chan struct{}
	closeOnce sync.Once
}

func (c *H2Conn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

func (c *H2Conn) Write(b []byte) (int, error) {
	n, err := c.writer.Write(b)
	c.flusher.Flush()
	return n, err
}

func (c *H2Conn) Close() error {
	c.closeOnce.Do(func() { close(c.closeCh) })
	return nil
}

func (c *H2Conn) LocalAddr() net.Addr { return nil }

func (c *H2Conn) RemoteAddr() net.Addr { return nil }

func (c *H2Conn) SetDeadline(t time.Time) error { return nil }

func (c *H2Conn) SetReadDeadline(t time.Time) error { return nil }

func (c *H2Conn) SetWriteDeadline(t time.Time) error { return nil }
