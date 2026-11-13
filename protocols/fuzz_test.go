package protocols

import (
	"io"
	"net"
	"testing"
)

// net.Pipe is synchronous: drain replies and close so a short input sees EOF.
func FuzzSocks5Handshake(f *testing.F) {
	f.Add([]byte{5, 1, 0, 5, 1, 0, 1, 127, 0, 0, 1, 0, 80})
	f.Add([]byte{5, 1, 0, 5, 1, 0, 3, 11, 'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm', 1, 187})
	f.Add([]byte{5, 2, 0, 2, 5, 1, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 80})
	f.Fuzz(func(t *testing.T, b []byte) {
		client, server := net.Pipe()
		defer client.Close()
		go func() { _, _ = client.Write(b); _ = client.Close() }()
		go func() { _, _ = io.Copy(io.Discard, client) }()
		in := NewSocks5Inbound(server)
		defer in.Close()
		_, _ = in.Connect()
	})
}

func FuzzHttpHead(f *testing.F) {
	f.Add("GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	f.Add("CONNECT example.com:443 HTTP/1.1\r\n\r\n")
	f.Add("POST http://example.com:80/x HTTP/1.1\r\nContent-Length: 0\r\n\r\n")
	f.Fuzz(func(t *testing.T, head string) {
		client, server := net.Pipe()
		defer client.Close()
		go func() { _, _ = io.WriteString(client, head); _ = client.Close() }()
		go func() { _, _ = io.Copy(io.Discard, client) }()
		in := NewHttpInbound(server)
		defer in.Close()
		_, _ = in.Connect()
	})
}