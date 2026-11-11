package transport

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
)

const (
	maxSplitSessions = 4096

	maxBufferedPieces = 30

	downlinkGrace = 30 * time.Second

	sessionIdle = 5 * time.Minute
)

func (listener *XhttpListener) session(sid string, remote net.Addr) (*xhttpSession, error) {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	if s, ok := listener.sessions[sid]; ok {
		return s, nil
	}
	if len(listener.sessions) >= listener.maxSessions {
		return nil, fmt.Errorf("xhttp: session cap %d reached", listener.maxSessions)
	}
	s := newXhttpSession(sid, remote, listener)
	listener.sessions[sid] = s
	go func() {
		select {
		case listener.ch <- s:
		case <-s.done:
		case <-listener.closed:
			s.close()
		}
	}()
	go s.reap()
	return s, nil
}

func (listener *XhttpListener) forget(sid string) {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	delete(listener.sessions, sid)
}

func (listener *XhttpListener) dropSessions() {
	listener.mu.Lock()
	pending := make([]*xhttpSession, 0, len(listener.sessions))
	for _, s := range listener.sessions {
		pending = append(pending, s)
	}
	listener.sessions = make(map[string]*xhttpSession)
	listener.mu.Unlock()
	for _, s := range pending {
		s.close()
	}
}

type xhttpPiece struct {
	seq  int
	data []byte
}

type xhttpSession struct {
	id     string
	remote net.Addr
	listen *XhttpListener

	in  chan xhttpPiece
	out chan []byte

	done      chan struct{}
	closeOnce sync.Once

	next    int
	waiting map[int][]byte
	cur     []byte

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

func (s *xhttpSession) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.downClaimed.Load() {
			s.listen.forget(s.id)
		}
	})
}

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
	s.mu.Lock()
	d := s.readDead
	s.mu.Unlock()
	timeout, stop := deadlineTimer(d)
	defer stop()
	for {
		if data, ok := s.head(); ok {
			return s.take(b, data), nil
		}
		select {
		case p := <-s.in:
			s.touch()
			switch {
			case p.seq < s.next:
				logger.Debugf("xhttp: dropping stale piece %d of %s", p.seq, s.id)
			case p.seq == s.next:
				s.next++
				if len(p.data) > 0 {
					return s.take(b, p.data), nil
				}
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
				return s.take(b, data), nil
			}
			return 0, io.EOF
		}
	}
}

func (s *xhttpSession) take(b, data []byte) int {
	n := copy(b, data)
	s.cur = data[n:]
	return n
}

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
	s.mu.Lock()
	d := s.writDead
	s.mu.Unlock()
	timeout, stop := deadlineTimer(d)
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

func deadlineTimer(d time.Time) (<-chan time.Time, func()) {
	if d.IsZero() {
		return nil, func() {}
	}
	timer := time.NewTimer(time.Until(d))
	return timer.C, func() { timer.Stop() }
}

func (listener *XhttpListener) serveDownlink(w http.ResponseWriter, r *http.Request, sid string) {
	s, err := listener.session(sid, parseAddr(r.RemoteAddr))
	if err != nil {
		refuse(w, http.StatusServiceUnavailable)
		return
	}
	if !s.downClaimed.CompareAndSwap(false, true) {

		refuse(w, http.StatusNotFound)
		return
	}

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

			s.close()
			return
		}
	}
}
