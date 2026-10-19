package transport

/*
The two split-flow xhttp modes: stream-up and packet-up. Both spread one byte
stream over two HTTP flows - a GET that carries the download and a POST that
carries the upload - so neither side can be expressed with the single duplex
XhttpConn. What the core gets is still one net.Conn:

	client                server
	GET  <path>/<sid>  ->  downstream, written as it arrives
	POST <path>/<sid>  ->  upstream, one endless body        (stream-up)
	POST <path>/<sid>/<seq> -> upstream, one piece per POST  (packet-up)

On the server the two flows are separate requests that may even arrive on
separate connections, so the session outlives its handlers and has to own the
reordering, the buffering, and the teardown that happens when either flow dies.
*/

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
)

const (
	// clientPieceBytes is what one packet-up POST carries. It matches the
	// default relay buffer, so a tunnel does not split one copied chunk into
	// several posts for no reason.
	clientPieceBytes = 64 * 1024

	// maxPieceBytes is Xray's cap for one post body (scMaxEachPostBytes); a
	// piece beyond it is answered 413, the same as there.
	maxPieceBytes = 1000000

	// maxPiecesInFlight bounds how many uploads one client may have parked at
	// once. Past that a write waits for a slot instead of queueing memory, which
	// is the same backpressure the window gives on the downlink.
	maxPiecesInFlight = 16

	// maxBufferedPieces is Xray's scMaxBufferedPosts: if the reorder queue grows
	// this large the gap is not a reordering hiccup but a dead tunnel, so the
	// session goes away rather than absorbing traffic forever.
	maxBufferedPieces = 30

	// downlinkGrace is how long a finished session keeps its id reserved for a
	// download that has not arrived yet.
	downlinkGrace = 30 * time.Second

	// sessionIdle is how long a server-side session may see no traffic on either
	// flow before it is reaped. Without this a client that vanishes mid-tunnel
	// (no FIN, no reset) would leave its session, its blocked handlers and its
	// map entry behind for good.
	sessionIdle = 5 * time.Minute
)

// ---------------------------------------------------------------- client

// flowTransport is the connection pool one tunnel's requests share. Over TLS
// the stdlib multiplexes them over a single HTTP/2 connection by ALPN; over
// plain TCP each flow takes its own connection, which is why idle connections
// must not be evicted after one use.
func (t xhttpTransport) flowTransport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       clientTlsConfig(t.address, t.authority),
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return net.Dial("tcp", t.address) },
		MaxIdleConnsPerHost:   maxPiecesInFlight,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// startFlow issues one request of a tunnel without waiting for its answer. The
// download GET is only flushed once the server is talking and the upload POST
// is only answered once the tunnel ends, so Dial must not block on either; the
// two channels are how whoever needs the answer collects it later.
func startFlow(ctx context.Context, tr *http.Transport, req *http.Request) (chan *http.Response, chan error) {
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := tr.RoundTrip(req)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()
	return respCh, errCh
}

func (t xhttpTransport) dialStreamUp() (net.Conn, error) {
	sid := randomHex(8)
	// Both flows go to the same URL: the path plus the session id, as a GET for
	// the download and a POST for the upload.
	flow, err := t.tunnelURL(sid, -1)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	tr := t.flowTransport()

	req, err := t.newTunnelRequest(ctx, http.MethodGet, flow, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	downResp, downErr := startFlow(ctx, tr, req)

	// The upload is one body that never ends until the tunnel closes, so the
	// pipe is the conn's writer and nothing here waits for its answer.
	pr, pw := io.Pipe()
	req, err = t.newTunnelRequest(ctx, http.MethodPost, flow, pr)
	if err != nil {
		cancel()
		pw.Close()
		return nil, err
	}
	upResp, upErr := startFlow(ctx, tr, req)

	conn := &XhttpConn{writer: pw, remote: parseAddr(t.address)}
	down := &lazyBody{respCh: downResp, errCh: downErr}
	conn.reader = down
	conn.onClose = func() {
		pw.Close()
		cancel()
		tr.CloseIdleConnections()
	}
	down.onFail = func(error) { conn.Close() }
	go supervise(upResp, upErr, conn)
	return conn, nil
}

func (t xhttpTransport) dialPacketUp() (net.Conn, error) {
	sid := randomHex(8)
	flow, err := t.tunnelURL(sid, -1)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	tr := t.flowTransport()

	req, err := t.newTunnelRequest(ctx, http.MethodGet, flow, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	downResp, downErr := startFlow(ctx, tr, req)

	pieces := &packetWriter{window: make(chan struct{}, maxPiecesInFlight), dead: make(chan struct{})}
	pieces.send = func(seq int, body io.Reader) (*http.Response, error) {
		req, err := t.newTunnelRequest(ctx, http.MethodPost, flow+"/"+strconv.Itoa(seq), body)
		if err != nil {
			return nil, err
		}
		return tr.RoundTrip(req)
	}

	conn := &XhttpConn{writer: pieces, remote: parseAddr(t.address)}
	down := &lazyBody{respCh: downResp, errCh: downErr}
	conn.reader = down
	pieces.onFail = func(error) { conn.Close() }
	conn.onClose = func() {
		pieces.shutdown(nil)
		cancel()
		tr.CloseIdleConnections()
	}
	down.onFail = func(error) { conn.Close() }
	return conn, nil
}

// supervise watches a flow whose answer means something on its own. A POST the
// front rejected ends the tunnel; without this the relay would sit reading a
// download that never has anything to say.
func supervise(respCh chan *http.Response, errCh chan error, conn *XhttpConn) {
	select {
	case err := <-errCh:
		logger.Debugf("xhttp: upload flow died: %v", err)
		_ = conn.Close()
	case resp := <-respCh:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			logger.Debugf("xhttp: upload flow answered %d", resp.StatusCode)
			_ = conn.Close()
		}
	}
}

// packetWriter turns the upload half of a packet-up tunnel into a run of
// sequence-numbered POSTs. Fire and forget, as Xray's is: no acknowledgement,
// no retransmission, and a piece the server rejects takes the tunnel down with
// it. The bytes are copied out of the caller's buffer because the relay reuses
// it as soon as Write returns, and posts finish whenever they like.
type packetWriter struct {
	send   func(seq int, body io.Reader) (*http.Response, error)
	onFail func(error)

	window chan struct{}
	dead   chan struct{}

	mu       sync.Mutex
	seq      int
	err      error
	stopped  sync.Once
	notified sync.Once
}

// shutdown stops the writer: the first failure is the one kept, and everything
// parked on the upload window is released. It never calls back, so the conn's
// Close can reach it from the teardown it started.
func (w *packetWriter) shutdown(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
	w.stopped.Do(func() { close(w.dead) })
}

// kill is what a rejected piece does: stop the upload, then take the tunnel
// down. That teardown comes back here as shutdown(nil), so the two steps cannot
// share one Once - the callback would re-enter the Once running on the same
// goroutine and the tunnel would hang with its own close held.
func (w *packetWriter) kill(err error) {
	w.shutdown(err)
	if err != nil {
		w.notified.Do(func() {
			if w.onFail != nil {
				w.onFail(err)
			}
		})
	}
}

func (w *packetWriter) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *packetWriter) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		if err := w.failure(); err != nil {
			return total, err
		}
		n := len(b)
		if n > clientPieceBytes {
			n = clientPieceBytes
		}
		piece := make([]byte, n)
		copy(piece, b[:n])
		if err := w.post(piece); err != nil {
			return total, err
		}
		total += n
		b = b[n:]
	}
	return total, nil
}

// post hands one piece off. The window is claimed before the sequence number
// is, so a piece that has to wait cannot overtake one already in flight and the
// server never has to bridge a gap it would have to wait on.
func (w *packetWriter) post(piece []byte) error {
	select {
	case w.window <- struct{}{}:
	case <-w.dead:
		return w.failure()
	}

	w.mu.Lock()
	seq, err := w.seq, w.err
	if err == nil {
		w.seq++
	}
	w.mu.Unlock()
	if err != nil {
		<-w.window
		return err
	}

	go func() {
		defer func() { <-w.window }()
		resp, err := w.send(seq, bytes.NewReader(piece))
		if err != nil {
			w.kill(fmt.Errorf("xhttp: piece %d: %w", seq, err))
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			w.kill(fmt.Errorf("xhttp: piece %d answered %d", seq, resp.StatusCode))
		}
	}()
	return nil
}

// ---------------------------------------------------------------- server

// xhttpPiece is one packet-up upload post, or one slice of a stream-up body
// handed to the same queue.
type xhttpPiece struct {
	seq  int
	data []byte
}

// xhttpSession is a server-side split-flow tunnel, and the net.Conn the core
// relays over. in carries the uplink and is fed by whichever request happened
// to deliver it; out is the downlink waiting for the client's GET.
type xhttpSession struct {
	id     string
	remote net.Addr
	listen *XhttpListener

	in  chan xhttpPiece
	out chan []byte

	done      chan struct{}
	closeOnce sync.Once

	// The reorder window belongs to the reader alone - one goroutine reads a
	// session, so it needs no lock. A piece is released only once every earlier
	// sequence number has landed, which is the whole of what packet-up promises.
	next    int
	waiting map[int][]byte
	// cur is what a caller's short read left of the piece it took: a handshake
	// of 40 bytes and a copy buffer of 32 KiB both arrive inside a 64 KiB piece,
	// and the rest of it must still reach the next Read.
	cur []byte

	downClaimed atomic.Bool
	lastSeen    atomic.Int64

	mu       sync.Mutex
	readDead time.Time
	writDead time.Time
}

func newXhttpSession(sid string, remote net.Addr, listener *XhttpListener) *xhttpSession {
	s := &xhttpSession{
		id:      sid,
		remote:  remote,
		listen:  listener,
		in:      make(chan xhttpPiece, maxBufferedPieces),
		out:     make(chan []byte, 64),
		done:    make(chan struct{}),
		waiting: make(map[int][]byte),
	}
	s.lastSeen.Store(time.Now().Unix())
	return s
}

func (s *xhttpSession) touch() { s.lastSeen.Store(time.Now().Unix()) }

// close ends the tunnel. Its id is only released once a download has claimed
// the session: the two split flows are separate requests, and the GET may still
// be on its way with bytes already queued for it. Releasing the id first hands
// that GET a brand new, empty session under the same id, and the client waits
// forever for a response the old session already had.
func (s *xhttpSession) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.downClaimed.Load() {
			s.listen.forget(s.id)
		}
	})
}

// reap is the idle guard the transport cannot do without: a client that
// disappears without closing its TCP connections leaves no error for the
// handlers to notice, and only a clock can tell that the tunnel is gone.
func (s *xhttpSession) reap() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			if s.downClaimed.Load() {
				return
			}
			timer := time.NewTimer(downlinkGrace)
			defer timer.Stop()
			<-timer.C
			s.listen.forget(s.id)
			return
		case <-ticker.C:
			if time.Since(time.Unix(s.lastSeen.Load(), 0)) > sessionIdle {
				logger.Debugf("xhttp: session %s idle, dropping", s.id)
				s.close()
				return
			}
		}
	}
}

func (s *xhttpSession) deliver(p xhttpPiece) error {
	select {
	case s.in <- p:
		s.touch()
		return nil
	case <-s.done:
		return fmt.Errorf("xhttp: session %s gone", s.id)
	}
}

func (s *xhttpSession) Read(b []byte) (int, error) {
	timeout, stop := s.readTimeout()
	defer stop()
	for {
		if data, ok := s.head(); ok {
			n := copy(b, data)
			s.cur = data[n:]
			if n > 0 {
				return n, nil
			}
			return 0, nil
		}
		select {
		case p := <-s.in:
			s.touch()
			switch {
			case p.seq < s.next:
				// late or repeated: the stream already moved past it, and there
				// is no retransmission in this protocol to wait for.
				logger.Debugf("xhttp: dropping stale piece %d of %s", p.seq, s.id)
			case p.seq == s.next:
				s.next++
				if len(p.data) == 0 {
					continue
				}
				n := copy(b, p.data)
				s.cur = p.data[n:]
				return n, nil
			default:
				s.waiting[p.seq] = p.data
				if len(s.waiting) > maxBufferedPieces {
					return 0, fmt.Errorf("xhttp: session %s lost piece %d", s.id, s.next)
				}
			}
		case <-timeout:
			return 0, os.ErrDeadlineExceeded
		case <-s.done:
			if data, ok := s.head(); ok {
				n := copy(b, data)
				s.cur = data[n:]
				return n, nil
			}
			return 0, io.EOF
		}
	}
}

// head returns the buffered piece the stream is waiting on, if it has arrived.
func (s *xhttpSession) head() ([]byte, bool) {
	if len(s.cur) > 0 {
		return s.cur, true
	}
	data, ok := s.waiting[s.next]
	if !ok {
		return nil, false
	}
	delete(s.waiting, s.next)
	s.next++
	if len(data) == 0 {
		return s.head()
	}
	return data, true
}

func (s *xhttpSession) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	chunk := make([]byte, len(b))
	copy(chunk, b)
	timeout, stop := s.writeTimeout()
	defer stop()
	select {
	case s.out <- chunk:
		s.touch()
		return len(b), nil
	case <-timeout:
		return 0, os.ErrDeadlineExceeded
	case <-s.done:
		return 0, io.ErrClosedPipe
	}
}

func (s *xhttpSession) Close() error { s.close(); return nil }

func (s *xhttpSession) LocalAddr() net.Addr  { return s.remote }
func (s *xhttpSession) RemoteAddr() net.Addr { return s.remote }

func (s *xhttpSession) SetDeadline(t time.Time) error {
	s.SetReadDeadline(t)
	return s.SetWriteDeadline(t)
}

func (s *xhttpSession) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readDead = t
	return nil
}

func (s *xhttpSession) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writDead = t
	return nil
}

// readTimeout arms the deadline the core set for this tunnel. An unset one is a
// nil channel, which never fires, so a relay parks in the select until traffic
// or a teardown arrives.
func (s *xhttpSession) readTimeout() (<-chan time.Time, func()) {
	s.mu.Lock()
	d := s.readDead
	s.mu.Unlock()
	return deadlineTimer(d)
}

func (s *xhttpSession) writeTimeout() (<-chan time.Time, func()) {
	s.mu.Lock()
	d := s.writDead
	s.mu.Unlock()
	return deadlineTimer(d)
}

func deadlineTimer(d time.Time) (<-chan time.Time, func()) {
	if d.IsZero() {
		return nil, func() {}
	}
	timer := time.NewTimer(time.Until(d))
	return timer.C, func() { timer.Stop() }
}

// serveDownlink answers the GET that carries a session's downstream. Its 200 is
// flushed before there is anything to send: the client's Dial does not wait for
// it, but a reverse proxy in front may hold the whole response until then.
func (listener *XhttpListener) serveDownlink(w http.ResponseWriter, r *http.Request, sid string) {
	s := listener.session(sid, parseAddr(r.RemoteAddr))
	if !s.downClaimed.CompareAndSwap(false, true) {
		// A second download for a session that already has one would interleave
		// two readers over one stream.
		refuse(w, http.StatusNotFound)
		return
	}
	// Serving this download is what makes the id disposable; whenever this
	// handler leaves, nothing else can be owed to that id.
	defer listener.forget(s.id)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", xhttpContentType)
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	for {
		select {
		case chunk := <-s.out:
			if _, err := w.Write(chunk); err != nil {
				s.close()
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			s.touch()
		case <-s.done:
			// The tunnel is going away, but whatever is already queued for the
			// download still has to leave. Dropping it truncates the response and
			// the client reads an EOF in the middle of a stream, so a tunnel that
			// ends while its peer is still downloading would lose the tail.
			for {
				select {
				case leftover := <-s.out:
					if _, err := w.Write(leftover); err != nil {
						return
					}
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				default:
					return
				}
			}
		case <-r.Context().Done():
			// The download is the only way out of this tunnel; with it gone the
			// session has nowhere to send anything.
			s.close()
			return
		}
	}
}

// serveUploadBody is stream-up's upload: one POST whose body is the whole
// upstream. Cutting it into the session's queue as numbered pieces is what
// lets one reader path serve both split-flow modes; a single producer hands out
// the sequence numbers itself, so the reorder window stays empty by construction.
func (listener *XhttpListener) serveUploadBody(w http.ResponseWriter, r *http.Request, sid string) {
	s := listener.session(sid, parseAddr(r.RemoteAddr))
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	buf := make([]byte, clientPieceBytes)
	for seq := 0; ; seq++ {
		n, err := r.Body.Read(buf)
		if n > 0 {
			piece := make([]byte, n)
			copy(piece, buf[:n])
			if derr := s.deliver(xhttpPiece{seq: seq, data: piece}); derr != nil {
				return
			}
		}
		if err != nil {
			break
		}
	}
	// Nothing earlier: the answer is only owed once the upload has ended, and
	// writing it before that would take the response headers away from the
	// duplex stream net/http is still reading the body of.
	w.WriteHeader(http.StatusOK)
}

// servePiece is one packet-up upload post.
func (listener *XhttpListener) servePiece(w http.ResponseWriter, r *http.Request, sid string, seq int) {
	s := listener.session(sid, parseAddr(r.RemoteAddr))
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPieceBytes+1))
	if err != nil {
		s.close()
		return
	}
	if len(body) > maxPieceBytes {
		refuse(w, http.StatusRequestEntityTooLarge)
		return
	}
	if len(body) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := s.deliver(xhttpPiece{seq: seq, data: body}); err != nil {
		logger.Debugf("%v", err)
		refuse(w, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
