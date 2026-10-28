package transport

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func dialHTTP2(o Options) (net.Conn, error) { return New("http2", o).Dial() }

// echoHTTP2 relays bytes back through one accepted tunnel until n have made
// the round trip. The buffer is small on purpose: a short read at one end must
// not be mistaken for the end of the tunnel.
func echoHTTP2(t *testing.T, ln net.Listener, n int) {
	t.Helper()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		if _, err := io.CopyBuffer(conn, io.LimitReader(conn, int64(n)), buf); err != nil {
			t.Errorf("relay copy: %v", err)
		}
	}()
}

// payload builds a repeating pattern, so a swapped or dropped block shows up as
// a mismatch instead of as a file of the wrong length.
func h2Payload(n int) []byte {
	pattern := []byte("http2-through-one-stream-")
	data := make([]byte, 0, n)
	for len(data) < n {
		data = append(data, pattern...)
	}
	return data[:n]
}

// roundTrip pushes n bytes up a tunnel and compares what comes back.
func roundTrip(t *testing.T, conn net.Conn, n int) {
	t.Helper()
	want := h2Payload(n)
	done := make(chan error, 1)
	go func() {
		if _, err := conn.Write(want); err != nil {
			done <- err
			return
		}
		got := make([]byte, n)
		if _, err := io.ReadFull(conn, got); err != nil {
			done <- err
			return
		}
		if !bytes.Equal(got, want) {
			done <- io.ErrUnexpectedEOF
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("round trip: %v", err)
		}
	case <-time.After(15 * time.Second):
		_ = conn.Close()
		t.Fatal("round trip did not finish")
	}
}

// The plain hop both endpoints speak: h2c with prior knowledge. The client
// refuses anything that did not negotiate HTTP/2, so a pass proves the request
// really rode one h2 stream in both directions.
func TestHttp2SelfTunnel(t *testing.T) {
	addr := freeAddr(t)
	ln, err := New("http2", Options{Address: addr, Path: "/h2"}).Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	echoHTTP2(t, ln, 300<<10)

	conn, err := dialHTTP2(Options{Address: addr, Path: "/h2"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	roundTrip(t, conn, 300<<10)
}

// The deployment hop: Caddy terminates TLS and reverse-proxies downward over
// HTTP/1.1, so the handler must stream duplex there too. A plain HTTP/1.1
// client stands in for the proxy.
func TestHttp2ServerStreamsOverHTTP1(t *testing.T) {
	addr := freeAddr(t)
	ln, err := New("http2", Options{Address: addr, Path: "/h2"}).Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	echoHTTP2(t, ln, 64<<10)

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPut, "http://"+addr+"/h2", pr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	respCh := make(chan *http.Response, 1)
	go func() {
		resp, err := client.Do(req)
		if err != nil {
			respCh <- nil
			return
		}
		respCh <- resp
	}()

	want := h2Payload(64 << 10)
	go func() { _, _ = pw.Write(want) }()

	var resp *http.Response
	select {
	case resp = <-respCh:
	case <-time.After(10 * time.Second):
		_ = pw.Close()
		t.Fatal("no response head over HTTP/1.1")
	}
	if resp == nil {
		t.Fatal("request failed")
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 1 {
		t.Fatalf("expected the proxy hop to be HTTP/1.1, got %s", resp.Proto)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(resp.Body, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("echo did not match")
	}
	_ = pw.Close()
}

// readWithTimeout reports whatever a rejected tunnel does first. An endpoint
// that wrongly accepted it would park this read forever, so the wait is bounded
// and that timeout is the failure being looked for.
func readWithTimeout(t *testing.T, conn net.Conn) error {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := conn.Read(buf)
		errc <- err
	}()
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel accepted where a rejection was due")
		return nil
	}
}

// A foreign path or a Host the endpoint was not configured for must be a 404,
// not a tunnel; this is how Xray's hub refuses them too.
func TestHttp2RefusesForeignRequest(t *testing.T) {
	addr := freeAddr(t)
	ln, err := New("http2", Options{Address: addr, Path: "/h2", Authority: "right.example"}).Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	for _, tc := range []struct {
		name      string
		path      string
		authority string
	}{
		{"wrong path", "/other", "right.example"},
		{"wrong host", "/h2", "wrong.example"},
	} {
		conn, err := dialHTTP2(Options{Address: addr, Path: tc.path, Authority: tc.authority})
		if err != nil {
			t.Fatalf("%s: dial: %v", tc.name, err)
		}
		// The rejection shows up on the first read, after the response arrives.
		if err := readWithTimeout(t, conn); err == nil {
			t.Errorf("%s: request was accepted as a tunnel", tc.name)
		}
		_ = conn.Close()
	}
}

// The cap exists because each tunnel costs a stream and a relay before any
// credential is checked. With the limit pinned to one, the second request must
// be refused rather than parked.
func TestHttp2TunnelCap(t *testing.T) {
	addr := freeAddr(t)
	ln, err := http2Transport{address: addr, path: "/h2", maxTunnels: 1}.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	first, err := dialHTTP2(Options{Address: addr, Path: "/h2"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// Hold the first tunnel open so its slot stays taken.
	if _, err := ln.Accept(); err != nil {
		t.Fatal(err)
	}

	second, err := dialHTTP2(Options{Address: addr, Path: "/h2"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := readWithTimeout(t, second); err == nil {
		t.Fatal("second tunnel was accepted past the cap")
	}
}

func TestHttp2PathNormalization(t *testing.T) {
	for in, want := range map[string]string{
		"":       "/",
		"h2":     "/h2",
		"/h2":    "/h2",
		"/h2/":   "/h2/", // unlike xhttp the trailing slash is kept
		"/a/b//": "/a/b//",
	} {
		if got := http2Path(in); got != want {
			t.Errorf("http2Path(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHttp2HostMatch(t *testing.T) {
	for _, tc := range []struct {
		request string
		want    string
		ok      bool
	}{
		{"anything.example", "", true}, // no host configured: anything goes
		{"right.example", "right.example", true},
		{"RIGHT.example:443", "right.example", true},
		{"wrong.example", "right.example", false},
	} {
		if got := hostMatches(tc.request, tc.want); got != tc.ok {
			t.Errorf("hostMatches(%q, %q) = %v, want %v", tc.request, tc.want, got, tc.ok)
		}
	}
}
