package transport

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// A peer that completes the TCP handshake and then never speaks TLS used to
// hold tls.Dial for as long as the operating system allowed. The configured
// budget has to cover the handshake too, not only the connect.
func TestTlsDialHonorsHandshakeBudget(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	go func() {
		conn, err := raw.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(2 * time.Second) // accept, then stay silent
	}()

	start := time.Now()
	// The peer never sends a ServerHello, so certificate settings never come
	// into play; the dial has to give up before any of that.
	_, err = dialTLS(raw.Addr().String(), &tls.Config{}, 100*time.Millisecond)
	if err == nil {
		t.Fatal("TLS dial against a silent peer succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("handshake budget not enforced, dial took %v", elapsed)
	}
}
