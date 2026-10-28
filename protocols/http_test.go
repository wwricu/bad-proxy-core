package protocols

import (
	"io"
	"net"
	"strings"
	"testing"
)

// A per-line limit alone lets an unbounded number of short lines through. The
// head must be rejected once its total size passes the budget, before the relay
// ever sees it.
func TestHttpHeadTotalBounded(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	var head strings.Builder
	head.WriteString("GET http://example.com/ HTTP/1.1\r\n")
	for i := 0; i < 80; i++ {
		head.WriteString("X-Filler: ")
		head.WriteString(strings.Repeat("a", 1024))
		head.WriteString("\r\n")
	}
	head.WriteString("\r\n")
	go func() {
		_, _ = io.WriteString(client, head.String())
	}()
	// Drain anything the server writes back, so an accepted head would make the
	// test fail on its assertion instead of blocking on the synchronous pipe.
	go func() {
		_, _ = io.Copy(io.Discard, client)
	}()

	in := NewHttpInbound(server)
	defer in.Close()
	if _, err := in.Connect(); err == nil {
		t.Fatal("oversized request head was accepted")
	}
}
