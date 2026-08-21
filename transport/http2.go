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
	reader    io.ReadCloser
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
	c.reader.Close()
	return nil
}

func (c *H2Conn) LocalAddr() net.Addr { return nil }

func (c *H2Conn) RemoteAddr() net.Addr { return nil }

func (c *H2Conn) SetDeadline(_ time.Time) error { return nil }

func (c *H2Conn) SetReadDeadline(_ time.Time) error { return nil }

func (c *H2Conn) SetWriteDeadline(_ time.Time) error { return nil }

type Http2Manager struct {
	protocol ProtocolType
	path     string
}

func (h2m *Http2Manager) Dial(address string) (net.Conn, error) {
	transport := &http.Transport{
		Protocols: &http.Protocols{},
	}
	transport.Protocols.SetUnencryptedHTTP2(true)

	client := &http.Client{Transport: transport}

	// 用 Pipe 创建一个可控的上行流
	pr, pw := io.Pipe()

	req, err := http.NewRequest("POST", "http://"+address, pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	// 发起请求（Do 会阻塞直到收到响应头）
	resp, err := client.Do(req)
	if err != nil {
		err := pw.Close()
		if err != nil {
			return nil, err
		}
		return nil, err
	}

	// 包装成你的 http2Conn
	conn := &H2Conn{
		reader:  resp.Body,
		writer:  pw,
		closeCh: make(chan struct{}),
		//resp:    resp, // 保留 resp 用于 Close 时清理
	}

	return conn, nil
}

func (h2m *Http2Manager) Listen(address string) (net.Listener, error) {
	listener := &Http2Listener{ch: make(chan net.Conn)}
	http.Handle(h2m.path, listener)

	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:      address,
		Handler:   http.DefaultServeMux,
		Protocols: &protocols,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil {
			panic(err)
		}
	}()
	return listener, nil
}

func NewHttp2Dialer() Dialer {
	return &Http2Manager{protocol: HTTP2}
}

func NewHttp2Listener(path string) Listener {
	return &Http2Manager{protocol: HTTP2, path: path}
}
