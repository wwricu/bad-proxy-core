package core

import (
	"net"
	"testing"
	"time"
)

// freePort grabs an ephemeral port by binding :0 and releasing it immediately.
// Slightly racy but adequate for a loopback lifecycle test.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe port: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()
	return addr.String()
}

func canDial(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// TestProxyStartStop verifies the embedded lifecycle API: New builds without a
// config file, Start binds the inbound, and Stop closes it so the port stops
// accepting.
func TestProxyStartStop(t *testing.T) {
	addr := freePort(t)
	cfg := Config{
		Inbounds: []InboundConfig{{Protocol: "socks", Host: "127.0.0.1", Port: portOf(t, addr)}},
		// a default outbound with an empty tag is required by newProxy.
		Outbounds: []OutboundConfig{{Tag: "", Protocol: FREEDOM}},
	}

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.Start()
	defer p.Stop()

	if !waitFor(func() bool { return canDial(addr) }, time.Second) {
		t.Fatalf("inbound never bound %s after Start", addr)
	}

	p.Stop()
	if waitFor(func() bool { return canDial(addr) }, 500*time.Millisecond) {
		t.Fatalf("port %s still accepting after Stop", addr)
	}
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %s: %v", addr, err)
	}
	return port
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
