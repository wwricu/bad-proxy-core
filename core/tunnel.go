package core

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwricu/bad-proxy-core/common"
	"github.com/wwricu/bad-proxy-core/logger"
)

// tunnel models one relay session: the two peers it joins, and the state that
// belongs to that pair and to nothing else.
//
// Modelling the pair is what keeps the relay honest. Proxy is a process-level
// object built once by newProxy and shared by every accepted connection, so any
// per-tunnel field on it is either copied by accident (the value receivers in
// serve/proxy make a copy per tunnel today) or shared across tunnels. A shared
// activity stamp would mean one busy tunnel keeps every other tunnel alive, and
// a shared fired flag would mark every teardown in the process as benign. Here
// the state is allocated with the tunnel, needs no lock, no map and no key, and
// is reclaimed by the GC exactly when the session ends.
type tunnel struct {
	in, out net.Conn

	// window is how long this pair may stay completely quiet before arm
	// reclaims it. Zero disables reclamation for this tunnel.
	window time.Duration

	// activity is the last moment either direction moved a byte; fired marks a
	// teardown the watchdog caused itself. They stay separate because their
	// lifetimes differ: activity is written once per relay buffer on the hot
	// path, fired at most once per tunnel.
	activity atomic.Int64
	fired    atomic.Bool
}

func newTunnel(in, out net.Conn, window time.Duration) *tunnel {
	t := &tunnel{in: in, out: out, window: window}
	t.touch()
	return t
}

// run pumps both directions and returns when each has ended on its own.
//
// Both copies must finish before the session is over: an EOF in one direction
// says the peer has nothing more to send, it does not say the other direction
// is done. Tearing the pair down on the first one to finish would close a socket
// that still holds unread received data, which sends an RST and destroys the
// reverse flow.
func (t *tunnel) run() {
	stop := t.arm()
	defer stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := t.pump(t.in, t.out); err != nil && !t.benign(err) {
			logger.Warnf("copy out->in: %v", err)
		}
	}()
	if _, err := t.pump(t.out, t.in); err != nil && !t.benign(err) {
		logger.Warnf("copy in->out: %v", err)
	}
	<-done
}

// The pump: how bytes move once a tunnel exists.
//
// A relay parks two copies, one per direction. Each borrows a buffer from the
// pool for as long as it runs and keeps it while blocked in Read, so the real
// cost of a quiet tunnel is two buffers held until traffic or a teardown frees
// them. That is why the copy buffer default is modest, and why arm exists.
//
// The loop is written out instead of calling io.Copy for two reasons, both of
// which used to need a wrapper type each. io.Copy hands the stream to
// src.WriteTo, then to dst.ReadFrom, before it ever looks at a buffer - and a
// *net.TCPConn offers both, so a TCP->TCP tunnel would be spliced (TransmitFile
// on Windows, splice(2) on Linux) with our buffer unused and relay_buffer_kib
// inert. Writing the loop directly never reaches for those methods. The second
// reason is observability: stamping activity becomes a plain call instead of an
// io.Writer smuggled around the copy.
//
// Buffer size is a measured trade, not taste: on loopback TCP with a forced
// chunked loop, throughput climbs steeply out of small buffers then flattens
// (dev box, 128 MiB best-of-5, TCPConn->TCPConn: 4 KiB 401 MB/s, 8 KiB 717,
// 16 KiB 1073, 32 KiB 1829, 64 KiB 2261). 32 KiB sits at the knee - 70% over
// 16 KiB, while 64 KiB buys only another 24% for double the parked memory. It is
// also Go's own io.Copy default and exactly half an xhttp packet-up piece, so a
// piece still arrives in a clean number of reads.
//
// relayBufferSize is set once from general.relay_buffer_kib before any stream
// starts, so the pool New closure needs no synchronisation.
var relayBufferSize = common.DefaultRelayBufferKiB * 1024

var relayBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, relayBufferSize)
		return &buf
	},
}

// pump moves src to dst through a pooled buffer, stamping activity as it goes.
// A clean EOF from src is a finished transfer, not an error.
//
// Delegating is not an option here, and not mainly for buffer reasons: if the
// copy ever handed the stream to src.WriteTo or dst.ReadFrom, the transfer would
// run inside the kernel with no per-chunk callback at all, so touch() stops
// firing - and arm would then read a stale stamp and reclaim a tunnel that is
// moving bytes at full speed. TestTunnelPumpNeverDelegates pins that.
func (t *tunnel) pump(dst io.Writer, src io.Reader) (int64, error) {
	bufp := relayBufPool.Get().(*[]byte)
	defer relayBufPool.Put(bufp)
	buf := *bufp

	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			written, werr := dst.Write(buf[:n])
			total += int64(written)
			t.touch()
			if werr != nil {
				return total, werr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return total, nil
			}
			return total, rerr
		}
	}
}

func (t *tunnel) touch() {
	if t.window > 0 {
		t.activity.Store(time.Now().UnixNano())
	}
}

// benign reports whether a copy error is just an expected teardown: a closed
// conn, or one the watchdog deliberately reclaimed. Classifying by error value
// alone is guesswork - transports differ in what a closed conn reports - so the
// reclaim records its own hand in fired.
func (t *tunnel) benign(err error) bool {
	return errors.Is(err, net.ErrClosed) || t.fired.Load()
}

// arm starts the idle reclaim for this tunnel and returns its stop, which the
// caller must defer.
//
// What it reclaims is a pair that has gone completely quiet. Both ends can sit
// alive and silent without anyone sending a FIN or an RST: a peer that lost
// power, a NAT entry that aged out mid-session, a network switch that moved the
// client to a new address. Nothing arrives, so nothing errors, and no packet
// means no RST either. The kernel's own keepalive does notice eventually, but
// only after two idle hours, so this timer is the minute-scale version of the
// same judgement.
//
// It measures silence, not liveness or throughput: any byte in either direction
// resets the window, so an application with its own heartbeats (a push channel,
// a connection pool probe) is never reclaimed, and neither is a client that
// trickles data to keep a tunnel warm. Tunnels that legitimately idle at
// application level - a push long-poll held open for minutes with no bytes - are
// the reason the default window is generous rather than aggressive.
//
// The scan period equals the window, so a tunnel is reclaimed between one and
// two windows after its last byte. Cost while enabled is one goroutine plus one
// relaxed atomic store per relay buffer; disabled (window 0) is free: touch is a
// no-op and arm returns an empty stop.
func (t *tunnel) arm() (stop func()) {
	if t.window <= 0 {
		return func() {}
	}
	stopCh := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(stopCh) }) }
	go func() {
		ticker := time.NewTicker(t.window)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, t.activity.Load())) >= t.window {
					// mark before close so the teardown is not counted as a copy error
					t.fired.Store(true)
					_ = t.in.Close()
					_ = t.out.Close()
					return
				}
			}
		}
	}()
	return stop
}
