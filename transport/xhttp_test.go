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

// The server treats its path as a prefix: BPC's own client uses the exact path,
// while Xray's XHTTP clients append a /path/<session-id> sub-path. A reverse
// proxy in front forwards the whole path, so both shapes must be served, and a
// lookalike sibling prefix must still be rejected.
func TestXhttpPathPrefix(t *testing.T) {
	const addr = "127.0.0.1:14557"
	listener, err := ListenXhttp(Options{Address: addr, Path: "/xrt"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err != nil {
					_ = c.Close()
					return
				}
				_, _ = c.Write(append([]byte("ec:"), buf...))
				_ = c.Close()
			}(conn)
		}
	}()

	for _, p := range []string{"/xrt", "/xrt/deadbeef"} {
		status, body := rawXhttpPost(t, addr, p)
		if status != http.StatusOK {
			t.Fatalf("path %q: status %d, want 200", p, status)
		}
		if body != "ec:ping" {
			t.Fatalf("path %q: body %q", p, body)
		}
	}
	for _, p := range []string{"/nope", "/xrtx", "/xrtt/deep"} {
		status, _ := rawXhttpPost(t, addr, p)
		if status != http.StatusForbidden {
			t.Fatalf("path %q: status %d, want 403", p, status)
		}
	}
}

func rawXhttpPost(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(conn, strings.Join([]string{
		"POST " + path + " HTTP/1.1",
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
	req, _ := http.NewRequest(http.MethodPost, "http://test"+path, nil)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read response for %q: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}
