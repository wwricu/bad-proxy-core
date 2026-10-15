package proxy

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// A completely quiet tunnel must be reclaimed once the idle window passes: the
// watchdog closes both relay ends. On a net.Pipe, closing the relay side makes
// the peer read return io.ErrClosedPipe - that is our liveness signal. This is
// the origin keep-alive hang the feature exists to fix.
func TestIdleWatchReclaimsQuietTunnel(t *testing.T) {
	in, inPeer := net.Pipe()
	out, _ := net.Pipe()
	defer inPeer.Close()
	defer out.Close()

	w := &idleWatch{idle: 60 * time.Millisecond}
	stop := w.start(in, out)
	defer stop()

	_ = inPeer.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadAll(inPeer)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("unexpected read err after reclaim: %v (read %d bytes)", err, n)
	}
	// watchdog closes `in`; inPeer read then returns. Prove `in` is really shut:
	if _, err := in.Write([]byte{1}); err == nil {
		t.Fatal("in still open after idle reclaim")
	}
}

// Touching the watch on activity must keep the tunnel alive across idle ticks.
func TestIdleWatchKeepsActiveTunnel(t *testing.T) {
	in, inPeer := net.Pipe()
	out, outPeer := net.Pipe()
	defer in.Close()
	defer out.Close()
	// net.Pipe is synchronous; drain the peers so relay writes never block
	go func() { _, _ = io.Copy(io.Discard, inPeer) }()
	go func() { _, _ = io.Copy(io.Discard, outPeer) }()

	w := &idleWatch{idle: 40 * time.Millisecond}
	stop := w.start(in, out)
	defer stop()

	deadline := time.Now().Add(400 * time.Millisecond) // ~10 idle windows
	for time.Now().Before(deadline) {
		w.touch()
		_ = in.SetDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := in.Write([]byte{1}); err != nil {
			t.Fatalf("active tunnel was reclaimed early: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An unset (0) idle window must never fire - the default relay-forever mode.
func TestIdleWatchDisabledByDefault(t *testing.T) {
	in, inPeer := net.Pipe()
	out, _ := net.Pipe()
	defer in.Close()
	defer out.Close()
	defer inPeer.Close()
	go func() { _, _ = io.Copy(io.Discard, inPeer) }()

	w := &idleWatch{idle: 0}
	stop := w.start(in, out)
	defer stop()
	time.Sleep(150 * time.Millisecond)
	_ = in.SetDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := in.Write([]byte{1}); err != nil {
		t.Fatalf("0-idle watch must never close the tunnel, but write failed: %v", err)
	}
}
