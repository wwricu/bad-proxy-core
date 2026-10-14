package transport

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A Content-Length body probe exercises the server half without the
// streaming-body client path.
func TestXhttpServerRawProbe(t *testing.T) {
	const addr = "127.0.0.1:14556"
	listener, err := ListenXhttp(Options{Address: addr, Path: "/xrt"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	errs := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errs <- fmt.Errorf("accept: %w", err)
			return
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			errs <- fmt.Errorf("relay read: %w", err)
			return
		}
		if _, err := conn.Write(append([]byte("ec:"), buf...)); err != nil {
			errs <- fmt.Errorf("relay write: %w", err)
			return
		}
		_ = conn.Close()
		errs <- nil
	}()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(conn, strings.Join([]string{
		"POST /xrt HTTP/1.1",
		"Host: test",
		"Content-Type: application/octet-stream",
		"X-Session-Id: 0123456789abcdef",
		"X-Sec-Mode: stream-one",
		"Content-Length: 4",
		"",
		"ping",
	}, "\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://test/xrt", nil)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("no response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, readErr := io.ReadAll(resp.Body)
	t.Cleanup(func() {
		select {
		case e := <-errs:
			if e != nil {
				t.Errorf("relay: %v", e)
			}
		default:
		}
	})
	if readErr != nil {
		t.Fatalf("body: %v", readErr)
	}
	if string(body) != "ec:ping" {
		t.Fatalf("got %q", body)
	}
}
