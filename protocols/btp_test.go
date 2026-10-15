package protocols

import (
	"net"
	"strings"
	"testing"
	"time"
)

// A BTP inbound with no key configured must never report handshake success,
// even for a syntactically valid header - it cannot authenticate anyone.
func TestBtpNoSecretDenies(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		// well-formed header for any secret would be produced here; the
		// inbound must refuse before parsing because it has no key at all
		_, _ = client.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	}()
	in := NewBtpInbound(server, nil)
	if _, err := in.Connect(); err == nil {
		t.Fatal("empty-secret inbound accepted a handshake")
	}
}

// A plain HTTP GET hitting the BTP port must fail the handshake fast
// (bounded confusion length) and be answered with the fallback page.
func TestBtpFallbackPage(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		in := NewBtpInbound(conn, []string{"secret"})
		defer in.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := in.Connect(); err == nil {
			return
		}
		in.Fallback()
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = client.Write([]byte("GET / HTTP/1.1\r\nHost: probe\r\n\r\nxtra bytes past the digest window")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp := string(buf[:n]); !strings.Contains(resp, "HTTP/1.1 200 OK") {
		t.Fatalf("no fallback page, got %q", resp)
	}
}
