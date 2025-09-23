package transport

import (
	"errors"
	"net"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

type WsListener struct {
	addr net.Addr
	ch   chan net.Conn
}

func (listener *WsListener) Accept() (conn net.Conn, err error) {
	if listener.ch == nil {
		err = errors.New("nil channel")
		return
	}
	return <-listener.ch, nil
}

func (listener *WsListener) Close() error {
	return nil // close channel
}

func (listener *WsListener) Addr() net.Addr {
	return listener.addr
}

func (listener *WsListener) handle(conn *websocket.Conn) {
	cond := sync.NewCond(&sync.Mutex{})
	ws := WsConnect{
		conn: conn,
		cond: cond,
	}
	cond.L.Lock()
	listener.ch <- ws
	cond.Wait() // handler return will release ws conn, wait for close
	cond.L.Unlock()
}

type WsConnect struct {
	conn *websocket.Conn
	cond *sync.Cond
}

func (ws WsConnect) Read(b []byte) (n int, err error) {
	return ws.conn.Read(b)
}

func (ws WsConnect) Write(b []byte) (n int, err error) {
	return ws.conn.Write(b)
}

func (ws WsConnect) Close() (err error) {
	ws.cond.L.Lock()
	err = ws.conn.Close()
	ws.cond.Broadcast()
	ws.cond.L.Unlock()
	return
}

func (ws WsConnect) LocalAddr() net.Addr {
	return ws.conn.LocalAddr()
}

func (ws WsConnect) RemoteAddr() net.Addr {
	return ws.conn.RemoteAddr()
}

func (ws WsConnect) SetDeadline(t time.Time) error {
	return ws.conn.SetDeadline(t)
}

func (ws WsConnect) SetReadDeadline(t time.Time) error {
	return ws.conn.SetReadDeadline(t)
}

func (ws WsConnect) SetWriteDeadline(t time.Time) error {
	return ws.conn.SetWriteDeadline(t)
}
