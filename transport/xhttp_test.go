package transport

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wwricu/bad-proxy-core/common"
)

// echoXhttpListener answers like the relay does: hand back the first n bytes
// read, then close. The copy buffer is deliberately much smaller than one
// packet-up piece, because a handshake read of 40 bytes lands inside a 64 KiB
// upload and whatever the caller did not ask for still has to reach the read
// after it.
// wantTunnel says whether a tunnel is due here. When it is not, the pair is
// refused, and then either nothing is ever accepted or the session is handed
// over empty - which is the test passing, not the relay failing on its own.
func echoXhttpListener(t *testing.T, ln net.Listener, n int, wantTunnel bool) {
	t.Helper()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			if wantTunnel {
				t.Errorf("accept: %v", err)
			}
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		copied, err := io.CopyBuffer(conn, io.LimitReader(conn, int64(n)), buf)
		if !wantTunnel {
			return
		}
		if err != nil {
			t.Errorf("relay copy: %v", err)
		} else if copied != int64(n) {
			t.Errorf("relay echoed %d of %d bytes and stopped", copied, n)
		}
	}()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// echoPayload is bigger than one packet-up piece so a tunnel has to spread it
// over several posts, and patterned so a reordered or dropped piece shows up as
// a mismatch rather than passing by luck.
func echoPayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

func TestXhttpRoundTrip(t *testing.T) {
	addr := freeAddr(t)
	listener, err := ListenXhttp(Options{Address: addr, Path: "/xrt"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	echoXhttpListener(t, listener, 4, true)

	dialed := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := DialXhttp(Options{Address: addr, Path: "/xrt"})
		if err != nil {
			errCh <- err
			return
		}
		dialed <- conn
	}()

	select {
	case err := <-errCh:
		t.Fatalf("dial: %v", err)
	case conn := <-dialed:
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		resp := make([]byte, 4)
		if _, err := io.ReadFull(conn, resp); err != nil {
			t.Fatal(err)
		}
		if string(resp) != "ping" {
			t.Fatalf("got %q", resp)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out: neither dial error nor established tunnel")
	}
}

// Each pairing that is supposed to connect has to carry a payload bigger than
// one piece and come back whole, and a server left on auto has to answer every
// mode: the mode is never sent on the wire, so restricting is the only thing a
// server-side mode can do, and auto is what lets one BPC server front whichever
// mode its clients happen to speak.
func TestXhttpModeSelfInterop(t *testing.T) {
	cases := []struct{ client, server string }{
		{"", ""},
		{"", XhttpModeStreamOne},
		{XhttpModeStreamOne, XhttpModeAuto},
		{XhttpModeStreamUp, XhttpModeAuto},
		{XhttpModeStreamUp, XhttpModeStreamUp},
		{XhttpModePacketUp, XhttpModeAuto},
		{XhttpModePacketUp, XhttpModePacketUp},
		{XhttpModeAuto, XhttpModeAuto},
		{XhttpModeAuto, XhttpModePacketUp}, // an auto client dials packet-up
	}
	for _, c := range cases {
		t.Run(xhttpModeName(c.client)+" client, "+xhttpModeName(c.server)+" server", func(t *testing.T) {
			runXhttpEcho(t, c.client, c.server, true, 200*1024)
		})
	}
}

func xhttpModeName(m string) string {
	if m == "" {
		return "(unset)"
	}
	return m
}

// A server pinned to one mode must refuse the others rather than half-answer
// them, otherwise pinning would be a decoration.
func TestXhttpModeRefused(t *testing.T) {
	for _, pair := range [][2]string{
		{XhttpModeStreamUp, XhttpModeStreamOne},
		{XhttpModePacketUp, XhttpModeStreamOne},
		{XhttpModeStreamOne, XhttpModePacketUp},
		{XhttpModeStreamUp, XhttpModePacketUp},
	} {
		t.Run("client "+pair[0]+" against "+pair[1], func(t *testing.T) {
			runXhttpEcho(t, pair[0], pair[1], false, 64)
		})
	}
}

// runXhttpEcho dials one tunnel and asks the server for the payload back. When
// the pair is not supposed to connect, the tunnel has to fail loudly: a client
// that hangs, or that reads a zero-byte "success" out of a rejected request,
// would look like a working tunnel to whatever is relaying over it.
func runXhttpEcho(t *testing.T, clientMode, serverMode string, wantOK bool, size int) {
	t.Helper()

	addr := freeAddr(t)
	listener, err := ListenXhttp(Options{Address: addr, Path: "/xrt", Mode: serverMode})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	echoXhttpListener(t, listener, size, wantOK)

	payload := echoPayload(size)
	dialed := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := DialXhttp(Options{Address: addr, Path: "/xrt", Mode: clientMode})
		if err != nil {
			errCh <- err
			return
		}
		dialed <- conn
	}()

	var conn net.Conn
	select {
	case err := <-errCh:
		if !wantOK {
			t.Logf("refused at dial: %v", err)
			return
		}
		t.Fatalf("dial: %v", err)
	case conn = <-dialed:
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for the tunnel")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	writeErr := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		writeErr <- err
	}()

	got := make([]byte, len(payload))
	readErr := func() error {
		_, err := io.ReadFull(conn, got)
		return err
	}()

	if !wantOK {
		if readErr == nil && bytesEqual(got, payload) {
			t.Fatal("a refused mode echoed the payload back")
		}
		t.Logf("refused as expected: %v", readErr)
		return
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write: %v", err)
	}
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if !bytesEqual(got, payload) {
		t.Fatalf("echo corrupted: first mismatch at %d of %d", firstMismatch(got, payload), len(payload))
	}
}

func bytesEqual(a, b []byte) bool { return len(a) == len(b) && firstMismatch(a, b) == -1 }

func firstMismatch(a, b []byte) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	return -1
}

// The reorder window is the only thing packet-up promises about lost pieces: a
// piece that lands early must wait for the one before it instead of being
// handed to the relay out of order.
func TestXhttpPacketReorder(t *testing.T) {
	addr := freeAddr(t)
	listener, err := ListenXhttp(Options{Address: addr, Path: "/xrt", Mode: XhttpModePacketUp})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- conn
	}()

	const sid = "aabbccddeeff0011"
	send := func(seq int, body string) {
		req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/xrt/"+sid+"/"+fmt.Sprint(seq),
			strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.URL.RawQuery = PaddingParam + "=" + strings.Repeat("X", 120)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post piece %d: %v", seq, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("piece %d: status %d", seq, resp.StatusCode)
		}
	}

	// Deliberately out of order, and with the gap spanning a read.
	go func() {
		send(1, "second")
		send(0, "first")
		send(2, "third")
	}()

	select {
	case conn := <-accepted:
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, len("firstsecondthird"))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatal(err)
		}
		if string(got) != "firstsecondthird" {
			t.Fatalf("got %q, want the pieces in sequence order", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no tunnel accepted")
	}
}

// A Content-Length body probe exercises the server half without the
// streaming-body client path. All three shapes Xray's client produces are
// probed: the filler it draws when configured, the empty parameter, and the
// bare path with no parameter at all - the one Xray sends by default, which
// this server used to refuse with 400.
func TestXhttpServerRawProbe(t *testing.T) {
	for _, c := range []struct {
		name  string
		query string
	}{
		{"filler", "?x_padding=" + strings.Repeat("X", 120)},
		{"empty", "?x_padding="},
		{"absent", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			addr := freeAddr(t)
			listener, err := ListenXhttp(Options{Address: addr, Path: "/xrt"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()

			errs := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					errs <- fmt.Errorf("accept: %w", err)
					return
				}
				buf := make([]byte, 4)
				if _, err := io.ReadFull(conn, buf); err != nil {
					errs <- fmt.Errorf("relay read: %w", err)
					return
				}
				if _, err := conn.Write(buf); err != nil {
					errs <- fmt.Errorf("relay write: %w", err)
					return
				}
				_ = conn.Close()
				errs <- nil
			}()

			status, body := rawXhttpPost(t, addr, "/xrt/", c.query)
			if status != http.StatusOK {
				t.Fatalf("status %d, want 200", status)
			}
			if body != "ping" {
				t.Fatalf("got %q", body)
			}
			select {
			case e := <-errs:
				if e != nil {
					t.Errorf("relay: %v", e)
				}
			case <-time.After(5 * time.Second):
				t.Error("relay never finished")
			}
		})
	}
}

// The path is the gate an Xray hub puts up: off it, a request gets the same
// answer any other location on this host would, so a probe cannot learn that a
// tunnel is listening here. Padding is not a gate - Xray 26.3.27 answers an
// unpadded request on the tunnel path exactly as it answers a padded one, which
// is what TestXhttpServerRawProbe pins down.
func TestXhttpGates(t *testing.T) {
	addr := freeAddr(t)
	listener, err := ListenXhttp(Options{Address: addr, Path: "/xrt"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	cases := []struct {
		path string
		want int
	}{
		// Anything off the tunnel path is indistinguishable from an empty host.
		{"/nope", http.StatusNotFound},
		{"/xrtx", http.StatusNotFound},
		{"/xrt/deep/er/than/that", http.StatusNotFound},
		// A piece number that is not a number is not a piece.
		{"/xrt/cccccccccccccccc/one?x_padding=XXXXXXXXXXXXXXXXXXXXXXXX", http.StatusBadRequest},
	}
	for _, c := range cases {
		got, _ := rawPathOnly(t, addr, c.path)
		if got != c.want {
			t.Errorf("path %q: status %d, want %d", c.path, got, c.want)
		}
	}
}

// xhttpPath is what turns a configured "/xrt" into the "/xrt/" an Xray server
// expects, and xhttpSegments is the reading half of that agreement.
func TestXhttpPathShape(t *testing.T) {
	if got := xhttpPath("/xrt"); got != "/xrt/" {
		t.Errorf("xhttpPath(/xrt) = %q", got)
	}
	if got := xhttpPath(""); got != "/" {
		t.Errorf("xhttpPath() = %q", got)
	}
	if got := xhttpPath("xrt/"); got != "/xrt/" {
		t.Errorf("xhttpPath(xrt/) = %q", got)
	}

	for _, ok := range []struct {
		base, got string
		depth     int
	}{
		{"/xrt/", "/xrt", 0},
		{"/xrt/", "/xrt/", 0},
		{"/", "/", 0},
		{"/xrt/", "/xrt/sid", 1},
		{"/xrt/", "/xrt/sid/", 1},
		{"/xrt/", "/xrt/sid/7", 2},
		{"/", "/sid/7", 2},
	} {
		segs, matched := xhttpSegments(ok.base, ok.got)
		if !matched {
			t.Errorf("%s under %s: no match", ok.got, ok.base)
			continue
		}
		if len(segs) != ok.depth {
			t.Errorf("%s under %s: depth %d, want %d (%q)", ok.got, ok.base, len(segs), ok.depth, segs)
		}
	}

	for _, bad := range []string{"/xrtx", "/xrt/../etc", "/xrt/a/b/c", "/"} {
		if _, matched := xhttpSegments("/xrt/", bad); matched && bad != "/" {
			t.Errorf("%s should not match /xrt/", bad)
		}
	}
	if _, matched := xhttpSegments("/", "/"); !matched {
		t.Error("the root tunnel must match itself")
	}
}

func TestXhttpModeResolution(t *testing.T) {
	for in, want := range map[string]string{
		"":              "",                 // the endpoint picks its own default
		"stream-one":    XhttpModeStreamOne, // named, it pins the server
		"auto":          XhttpModeAuto,
		"AUTO":          XhttpModeAuto,
		" stream-up ":   XhttpModeStreamUp,
		"packet-up":     XhttpModePacketUp,
		"stream_1":      "", // unknown falls back to the default, loudly
		"packet-down":   "", // not a mode Xray speaks either
		"nonexistent-1": "",
	} {
		if got := ResolveXhttpMode(in); got != want {
			t.Errorf("ResolveXhttpMode(%q) = %q, want %q", in, got, want)
		}
	}
	// The answer is used as the mode of both an inbound and an outbound, so it
	// must not blow up on the one value that carries no name.
	if got := xhttpAnswers("", http.MethodPost, 0); !got {
		t.Error("an unset server mode must answer stream-one")
	}
}

// rawXhttpPost sends one hand-written request with the query as given - which
// is the point, because the parameter's presence is what used to be tested. The
// duplex answer is read to the end of the body, which is where the echoing side
// closes.
func rawXhttpPost(t *testing.T, addr, path, query string) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(conn, strings.Join([]string{
		"POST " + path + query + " HTTP/1.1",
		"Host: test",
		"Content-Type: application/octet-stream",
		"Content-Length: 4",
		"",
		"ping",
	}, "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://test"+path, nil)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read response for %q: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// A split-flow tunnel is two requests and nothing orders them: the upload can
// arrive, be relayed and be closed before the download is even handled. The
// session and the bytes queued inside it are what that late download is owed,
// so releasing its id at teardown hands the GET an empty session under the same
// id, and the client waits forever for a response the old session already had.
func TestXhttpLateDownlinkGetsTheTail(t *testing.T) {
	addr := freeAddr(t)
	listener, err := ListenXhttp(Options{Address: addr, Path: "/xrt", Mode: XhttpModePacketUp})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	const sid = "deadbeef00000000"
	payload := []byte("the tail of a tunnel whose download has not arrived yet")
	echoXhttpListener(t, listener, len(payload), true)

	if status := rawXhttpUpload(t, addr, "/xrt/"+sid+"/0", payload); status != http.StatusOK {
		t.Fatalf("piece answered %d", status)
	}
	// Give the relay its copy and let it close the tunnel, so the download
	// below really is the late one.
	time.Sleep(300 * time.Millisecond)

	if got := rawXhttpDownload(t, addr, "/xrt/"+sid); !bytesEqual(got, payload) {
		t.Fatalf("late download got %d bytes %q, want %q", len(got), got, payload)
	}
}

// rawXhttpUpload posts one packet-up piece over its own connection.
func rawXhttpUpload(t *testing.T, addr, path string, body []byte) int {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(conn, strings.Join([]string{
		"POST " + path + "?x_padding=abcdefghij HTTP/1.1",
		"Host: test",
		"Content-Type: application/octet-stream",
		fmt.Sprintf("Content-Length: %d", len(body)),
		"",
		string(body),
	}, "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://test"+path, nil)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("piece got no response: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// rawXhttpDownload asks for a session's download and reads it to the end. The
// deadline is what turns a lost tail from a hung test into a named failure.
func rawXhttpDownload(t *testing.T, addr, path string) []byte {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(conn, strings.Join([]string{
		"GET " + path + "?x_padding=abcdefghij HTTP/1.1",
		"Host: test",
		"",
		"",
	}, "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://test"+path, nil)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("download got no response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("download truncated: %v", err)
	}
	return body
}

func rawPathOnly(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+path, strings.NewReader("ping"))
	if err != nil {
		t.Fatal(err)
	}
	// The gates are checked before the body means anything, so a short body with
	// no tunnel behind it must still produce the right status.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%q: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
	return resp.StatusCode, string(body)
}

// xhttpAnswers is the request-shape table behind the mode restriction: depth is
// how many segments follow the tunnel path, which is the only thing a server can
// read a mode off.
func TestXhttpAnswers(t *testing.T) {
	type want struct {
		mode   string
		method string
		depth  int
		ok     bool
	}
	for _, c := range []want{
		{XhttpModeAuto, http.MethodPost, 0, true},
		{XhttpModeAuto, http.MethodGet, 1, true},
		{XhttpModeAuto, http.MethodPost, 1, true},
		{XhttpModeAuto, http.MethodPost, 2, true},
		{XhttpModeAuto, http.MethodPost, 3, false},
		{XhttpModeStreamOne, http.MethodPost, 0, true},
		{XhttpModeStreamOne, http.MethodGet, 1, false},
		{XhttpModeStreamUp, http.MethodGet, 1, true},
		{XhttpModeStreamUp, http.MethodPost, 1, true},
		{XhttpModeStreamUp, http.MethodPost, 0, false},
		{XhttpModePacketUp, http.MethodGet, 1, true},
		{XhttpModePacketUp, http.MethodPost, 2, true},
		{XhttpModePacketUp, http.MethodPost, 1, false},
		{XhttpModeStreamOne, http.MethodDelete, 0, false},
	} {
		if got := xhttpAnswers(c.mode, c.method, c.depth); got != c.ok {
			t.Errorf("%s %s depth %d: answered %v, want %v", c.mode, c.method, c.depth, got, c.ok)
		}
	}
}

// Padding is not configurable any more, so this is all that stands between the
// transport and a signature: the length has to move, and the content has to be
// what the protocol says it is.
func TestPaddingLooksLikeXrays(t *testing.T) {
	lengths := map[int]bool{}
	for i := 0; i < 200; i++ {
		p := randomPadding(common.DefaultPadding)
		if len(p) < 100 || len(p) > 1000 {
			t.Fatalf("length %d is outside the default range", len(p))
		}
		if strings.Trim(p, "X") != "" {
			t.Fatalf("filler %q is not the repeat-x run an Xray server accepts", p[:20])
		}
		lengths[len(p)] = true
	}
	if len(lengths) < 20 {
		t.Fatalf("only %d distinct lengths over 200 requests; the size is not being hidden", len(lengths))
	}
	if randomPadding("0") != "" || randomPadding("") != "" {
		t.Fatal("a zero or empty spec must pad nothing")
	}
}
