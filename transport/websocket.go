package transport

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const wsBufferSize = 1024

var upgrader = &websocket.Upgrader{
	ReadBufferSize:  wsBufferSize,
	WriteBufferSize: wsBufferSize,
}

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

func (listener *WsListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

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
	conn   *websocket.Conn
	cond   *sync.Cond
	reader *bytes.Reader
}

func (ws WsConnect) Read(b []byte) (int, error) {
	if ws.reader == nil || ws.reader.Len() == 0 {
		_, p, err := ws.conn.ReadMessage()
		if err != nil {
			return 0, err
		}

		if len(b) >= len(p) {
			return copy(b, p), err // simply copy
		}

		ws.reader = bytes.NewReader(p)
	}

	n, err := ws.reader.Read(b)
	if ws.reader.Len() == 0 {
		ws.reader = nil
	}
	return n, err
}

func (ws WsConnect) Write(b []byte) (int, error) {
	err := ws.conn.WriteMessage(websocket.BinaryMessage, b)
	if err != nil {
		return 0, err
	}
	return len(b), err
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
	if err := ws.SetReadDeadline(t); err != nil {
		return err
	}
	return ws.SetWriteDeadline(t)
}

func (ws WsConnect) SetReadDeadline(t time.Time) error {
	return ws.conn.SetReadDeadline(t)
}

func (ws WsConnect) SetWriteDeadline(t time.Time) error {
	return ws.conn.SetWriteDeadline(t)
}
