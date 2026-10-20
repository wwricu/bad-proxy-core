package core

import (
	"bytes"
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
	if s.relayIdle != defaultRelayIdle {
		t.Errorf("relay idle default = %v, want %v", s.relayIdle, defaultRelayIdle)
	}
	if s.relayBufferKiB != defaultRelayBufferKiB {
		t.Errorf("buffer default = %d, want %d", s.relayBufferKiB, defaultRelayBufferKiB)
	}
}

// The idle watchdog is on by default, so "0s" is the only way to turn it off,
// and it has to survive resolveSettings as a zero duration.
func TestResolveSettingsRelayIdleSwitch(t *testing.T) {
	for _, body := range []string{
		`{"general":{"relay_idle_timeout":"0s"}}`,
		`{"general":{"relay_idle_timeout":"0"}}`,
	} {
		var c Config
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			t.Fatal(err)
		}
		s, err := c.resolveSettings()
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if s.relayIdle != 0 {
			t.Errorf("%s: relayIdle = %v, want 0 (disabled)", body, s.relayIdle)
		}
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
	cases := []struct {
		body    string
		wantErr bool
	}{
		{`{"general":{"log_level":"nope"}}`, true},
		{`{"general":{"handshake_timeout":"3"}}`, true},
		{`{"general":{"handshake_timeout":"-1s"}}`, true},
		{`{"general":{"dial_timeout":"xx"}}`, true},
		{`{"general":{"relay_idle_timeout":"-5s"}}`, true},
		{`{"general":{"relay_idle_timeout":"0s"}}`, false}, // explicit off, must be valid
		{`{"general":{"relay_buffer_kib":0}}`, false},      // treated as unset, must be valid
		{`{"general":{"relay_buffer_kib":2048}}`, true},
	}
	for _, tc := range cases {
		var c Config
		if err := json.Unmarshal([]byte(tc.body), &c); err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		_, err := c.resolveSettings()
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got none", tc.body)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error %v", tc.body, err)
		}
	}
}

// A completely quiet pair must be reclaimed once the idle window passes: arm
// closes both relay ends. On a net.Pipe, closing the relay side makes the peer
// read return io.ErrClosedPipe - that is our liveness signal. This is the origin
// keep-alive hang the feature exists to fix.
func TestTunnelReclaimsQuietPair(t *testing.T) {
	in, inPeer := net.Pipe()
	out, _ := net.Pipe()
	defer inPeer.Close()
	defer out.Close()

	tr := newTunnel(in, out, 60*time.Millisecond)
	stop := tr.arm()
	defer stop()

	_ = inPeer.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadAll(inPeer)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("unexpected read err after reclaim: %v (read %d bytes)", err, n)
	}
	// arm closes `in`; inPeer read then returns. Prove `in` is really shut:
	if _, err := in.Write([]byte{1}); err == nil {
		t.Fatal("in still open after idle reclaim")
	}
}

// Touching the pair on activity must keep it alive across idle ticks.
func TestTunnelKeepsActivePair(t *testing.T) {
	in, inPeer := net.Pipe()
	out, outPeer := net.Pipe()
	defer in.Close()
	defer out.Close()
	// net.Pipe is synchronous; drain the peers so relay writes never block
	go func() { _, _ = io.Copy(io.Discard, inPeer) }()
	go func() { _, _ = io.Copy(io.Discard, outPeer) }()

	tr := newTunnel(in, out, 40*time.Millisecond)
	stop := tr.arm()
	defer stop()

	deadline := time.Now().Add(400 * time.Millisecond) // ~10 idle windows
	for time.Now().Before(deadline) {
		tr.touch()
		_ = in.SetDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := in.Write([]byte{1}); err != nil {
			t.Fatalf("active tunnel was reclaimed early: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A zero window (the explicit "0s" off switch) must never fire.
func TestTunnelZeroWindowNeverFires(t *testing.T) {
	in, inPeer := net.Pipe()
	out, _ := net.Pipe()
	defer in.Close()
	defer out.Close()
	defer inPeer.Close()
	go func() { _, _ = io.Copy(io.Discard, inPeer) }()

	tr := newTunnel(in, out, 0)
	stop := tr.arm()
	defer stop()
	time.Sleep(150 * time.Millisecond)
	_ = in.SetDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := in.Write([]byte{1}); err != nil {
		t.Fatalf("0-window tunnel must never be closed, but write failed: %v", err)
	}
}

// A clean EOF from the source is a finished transfer, not a failure: the pump
// must return nil so a normal tunnel ending does not log a warning.
func TestTunnelPumpTreatsEOFAsCleanEnd(t *testing.T) {
	// pump only needs the activity stamp and a pooled buffer, not live peers
	tr := &tunnel{window: time.Minute}
	var dst bytes.Buffer
	payload := bytes.Repeat([]byte("x"), relayBufferSize+7)
	n, err := tr.pump(&dst, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("EOF must be a clean end, got %v", err)
	}
	if n != int64(len(payload)) || dst.Len() != len(payload) {
		t.Fatalf("pump moved %d bytes, buffer holds %d, want %d", n, dst.Len(), len(payload))
	}
}

// A failing write stops the pump and reports the cause - the parked copy must
// not spin on a peer that cannot take bytes.
func TestTunnelPumpReportsWriteError(t *testing.T) {
	tr := &tunnel{window: time.Minute}
	wantErr := errors.New("target exploded")
	_, err := tr.pump(failWriter{wantErr}, bytes.NewReader([]byte("data")))
	if !errors.Is(err, wantErr) {
		t.Fatalf("pump err = %v, want %v", err, wantErr)
	}
}

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

// The pump must do the copying itself. io.Copy prefers src.WriteTo and then
// dst.ReadFrom - exactly the two methods *net.TCPConn offers - and either one
// moves the transfer into the kernel with no per-chunk callback, so touch()
// stops firing and arm reclaims a tunnel that is copying at full speed.
func TestTunnelPumpNeverDelegates(t *testing.T) {
	tr := &tunnel{window: time.Minute}
	var sink bytes.Buffer
	src := &spliceReader{r: bytes.NewReader([]byte("payload that must be copied a chunk at a time"))}
	dst := &spliceWriter{w: &sink}

	n, err := tr.pump(dst, src)
	if err != nil {
		t.Fatalf("pump: %v", err)
	}
	if n != int64(sink.Len()) || sink.Len() == 0 {
		t.Fatalf("moved %d bytes, dst holds %d", n, sink.Len())
	}
	if src.to != 0 || dst.from != 0 {
		t.Fatalf("pump delegated: src.WriteTo %d times, dst.ReadFrom %d", src.to, dst.from)
	}
}

// spliceReader is a source that offers WriteTo and spliceWriter is a
// destination that offers ReadFrom, one per side of the io.Copy preference
// order. Both refuse to actually copy, so a pump that ever delegates shows up as
// a counted call plus an error instead of a silent kernel splice.
type spliceReader struct {
	r  io.Reader
	to int
}

func (s *spliceReader) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *spliceReader) WriteTo(io.Writer) (int64, error) {
	s.to++
	return 0, errors.New("pump must not delegate to src.WriteTo")
}

type spliceWriter struct {
	w    io.Writer
	from int
}

func (s *spliceWriter) Write(p []byte) (int, error) { return s.w.Write(p) }

func (s *spliceWriter) ReadFrom(io.Reader) (int64, error) {
	s.from++
	return 0, errors.New("pump must not delegate to dst.ReadFrom")
}

// One direction ending must not tear the pair down: the other side may still
// have data to move. run returns only once both pumps have finished.
func TestTunnelRunsUntilBothDirectionsEnd(t *testing.T) {
	in, inPeer := net.Pipe()
	out, outPeer := net.Pipe()
	defer in.Close()
	defer out.Close()
	defer inPeer.Close()
	defer outPeer.Close()

	tr := newTunnel(in, out, 0)
	returned := make(chan struct{})
	go func() { tr.run(); close(returned) }()

	inPeer.Close() // one direction's peer goes away
	time.Sleep(200 * time.Millisecond)
	select {
	case <-returned:
		t.Fatal("run returned after only one direction ended, dropping the other")
	default:
	}

	outPeer.Close() // now both are finished
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("run never returned after both directions ended")
	}
}
