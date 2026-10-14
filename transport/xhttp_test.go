package transport

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestXhttpRoundTrip(t *testing.T) {
	const addr = "127.0.0.1:14555"
	listener, err := ListenXhttp(Options{Address: addr, Path: "/xrt"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Error(err)
			return
		}
		_, _ = conn.Write(append([]byte("ec:"), buf...))
		_ = conn.Close()
	}()

	dialed := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := DialXhttp(Options{Address: addr, Path: "/xrt", Padding: "30-50"})
		if err != nil {
			errCh <- err
			return
		}
		dialed <- conn
	}()

	select {
	case err := <-errCh:
		t.Fatalf("dial: %v", err)
	case conn := <-dialed:
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		resp := make([]byte, 7)
		if _, err := io.ReadFull(conn, resp); err != nil {
			t.Fatal(err)
		}
		if string(resp) != "ec:ping" {
			t.Fatalf("got %q", resp)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out: neither dial error nor established tunnel")
	}
}
