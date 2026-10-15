package core

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
)

func TestResolveSettingsDefaults(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"inbounds":[],"outbounds":[]}`), &c); err != nil {
		t.Fatal(err)
	}
	s, err := c.resolveSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.logLevel != logger.Debug {
		t.Errorf("log_level should default debug, got %v", s.logLevel)
	}
	if s.consoleLevel != logger.Info {
		t.Errorf("console_level should default info, got %v", s.consoleLevel)
	}
	if s.handshakeTimeout != defaultHandshakeTimeout {
		t.Errorf("handshake default = %v, want %v", s.handshakeTimeout, defaultHandshakeTimeout)
	}
	if s.dialTimeout != defaultDialTimeout {
		t.Errorf("dial default = %v, want %v", s.dialTimeout, defaultDialTimeout)
	}
	if s.relayBufferKiB != defaultRelayBufferKiB {
		t.Errorf("buffer default = %d, want %d", s.relayBufferKiB, defaultRelayBufferKiB)
	}
}

func TestResolveSettingsFromJson(t *testing.T) {
	var c Config
	body := `{"general":{"handshake_timeout":"5s","dial_timeout":"2s","relay_idle_timeout":"3m","relay_buffer_kib":128,"log_level":"warn","console_level":"error","log_file":"bpc.log"}}`
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	s, err := c.resolveSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.handshakeTimeout != 5*time.Second || s.dialTimeout != 2*time.Second {
		t.Errorf("duration parsing wrong: %+v", s)
	}
	if s.relayIdle != 3*time.Minute {
		t.Errorf("relay_idle_timeout = %v, want 3m", s.relayIdle)
	}
	if s.relayBufferKiB != 128 {
		t.Errorf("relay_buffer_kib = %d, want 128", s.relayBufferKiB)
	}
	if s.logLevel != logger.Warn || s.consoleLevel != logger.Error {
		t.Errorf("log level parsing wrong: %+v", s)
	}
	if s.logFile != "bpc.log" {
		t.Errorf("log_file = %q, want bpc.log", s.logFile)
	}
}

func TestResolveSettingsRejectsBadInput(t *testing.T) {
	cases := []string{
		`{"general":{"log_level":"nope"}}`,
		`{"general":{"handshake_timeout":"3"}}`,
		`{"general":{"dial_timeout":"xx"}}`,
		`{"general":{"relay_idle_timeout":"-5s"}}`,
		`{"general":{"relay_buffer_kib":0}}`, // treated as unset, must be valid
		`{"general":{"relay_buffer_kib":2048}}`,
	}
	for i, body := range cases {
		var c Config
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		_, err := c.resolveSettings()
		wantErr := i != 4 // relay_buffer_kib 0 falls back to default, others must error
		if wantErr && err == nil {
			t.Errorf("case %d (%s): expected error, got none", i, body)
		}
		if !wantErr && err != nil {
			t.Errorf("case %d (%s): unexpected error %v", i, body, err)
		}
	}
}

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
