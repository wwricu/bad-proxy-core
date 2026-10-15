package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwricu/bad-proxy-core/metrics"
	"github.com/wwricu/bad-proxy-core/router"
)

// the relay buffer size is set once from general.relay_buffer_kib before any
// stream starts, so the pool New closure needs no synchronisation.
var relayBufferSize = 64 * 1024

var relayBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, relayBufferSize)
		return &buf
	},
}

type Proxy struct {
	inbounds  []Inbound
	outbounds map[string]*Outbound
	routers   []router.Router
	settings  settings
}

type Config struct {
	General    GeneralConfig    `json:"general"`
	Inbounds   []InboundConfig  `json:"inbounds"`
	Outbounds  []OutboundConfig `json:"outbounds"`
	Router     []router.Config  `json:"routers"`
	RouterPath string
}

const (
	BTP     = "btp"
	SOCKS   = "socks"
	HTTP    = "http"
	TROJAN  = "trojan"
	VLESS   = "vless"
	FREEDOM = "freedom"
)

func newProxy(config Config) (newProxy Proxy, err error) {
	settings, err := config.resolveSettings()
	if err != nil {
		return Proxy{}, err
	}
	relayBufferSize = settings.relayBufferKiB * 1024
	newProxy.settings = settings
	newProxy.outbounds = make(map[string]*Outbound, 10)
	newProxy.routers = make([]router.Router, 0)
	for _, r := range config.Router {
		rules := make([]string, 0)
		rules = append(rules, r.Rules...)
		newRouter, err := router.NewRouter(r.Tag, rules, config.RouterPath)
		if err != nil {
			log.Println("wrong router:", r.Tag)
			continue
		}
		newProxy.routers = append(newProxy.routers, *newRouter)
	}

	for _, in := range config.Inbounds {
		newProxy.inbounds = append(newProxy.inbounds, NewInbound(in, settings))
	}

	for _, out := range config.Outbounds {
		newOutbound := NewOutbound(out, settings)
		if _, exist := newProxy.outbounds[out.Tag]; exist {
			return Proxy{}, errors.New("duplicate outbound tag " + out.Tag)
		}
		newProxy.outbounds[out.Tag] = &newOutbound
	}

	if _, exist := newProxy.outbounds[""]; exist != true {
		return Proxy{}, errors.New("no default outbound, add one with empty \"tag\" (use \"" + FREEDOM + "\" for direct)")
	}
	return
}

func (proxy Proxy) Start() {
	if proxy.settings.statsEvery > 0 {
		go func() {
			for range time.Tick(proxy.settings.statsEvery) {
				log.Println("stats:", metrics.Default.Snapshot())
			}
		}()
	}
	for i := range proxy.inbounds {
		inbound := proxy.inbounds[i]
		go func(inbound *Inbound) {
			if err := inbound.Listen(); err != nil {
				// one broken inbound must not take the whole process down
				log.Printf("inbound on %s disabled: %v\n", inbound.address, err)
				return
			}
			log.Printf("%s listen on %s\n", inbound.protocol, inbound.address)
			proxy.serve(inbound)
		}(&inbound)
	}
}

func (proxy Proxy) serve(inbound *Inbound) {
	failures := 0
	for {
		in, err := inbound.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.Printf("inbound on %s closed\n", inbound.address)
				return
			}
			failures++
			if failures > 32 {
				log.Printf("inbound on %s giving up after repeated errors\n", inbound.address)
				return
			}
			time.Sleep(50 * time.Millisecond) // transient accept error
			continue
		}
		failures = 0
		inbound.mp.Accepted.Add(1)
		go proxy.proxy(in, inbound.mp)
	}
}

func (proxy Proxy) proxy(in InboundConnect, mp *metrics.ProtoStats) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("relay panic:", r)
		}
		_ = in.Close()
	}()

	address, err := in.Connect() // handshake
	if err != nil {
		mp.HandshakeFail.Add(1)
		log.Println("inbound connect failed:", err)
		in.Fallback()
		return
	}
	_ = in.SetDeadline(time.Time{}) // relay phase runs without handshake deadline

	outbound := proxy.route(address)
	out, err := outbound.Dial(address)
	if err != nil {
		log.Printf("outbound dial to %s failed, err=%v\n", address, err)
		return
	}
	defer func() {
		_ = out.Close()
	}()

	metrics.Default.TunnelsActive.Add(1)
	defer metrics.Default.TunnelsActive.Add(-1)

	// reclaim a tunnel that goes completely quiet (idle origin keep-alive
	// otherwise pins both ends forever). No-op when relay_idle_timeout is unset.
	var idle idleWatch
	idle.idle = proxy.settings.relayIdle
	stopIdle := idle.start(in, out)
	defer stopIdle()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := relayCopy(in, &metrics.Default.BytesDown, out, &idle); err != nil && !idle.benign(err) {
			metrics.Default.CopyErr.Add(1)
			log.Printf("copy out->in: %v\n", err)
		}
	}()
	if _, err = relayCopy(out, &metrics.Default.BytesUp, in, &idle); err != nil && !idle.benign(err) {
		metrics.Default.CopyErr.Add(1)
		log.Printf("copy in->out: %v\n", err)
	}
	<-done
}

// idleWatch records the last moment a relay moved a byte in either direction.
// When the configured window passes with no activity the watchdog closes both
// ends, which unblocks the parked io.Copy loops. Cost when enabled is one
// relaxed atomic store per 64 KiB chunk; disabled (idle window 0) is free.
type idleWatch struct {
	idle     time.Duration
	activity atomic.Int64
	fired    atomic.Bool
}

func (w *idleWatch) touch() {
	if w.idle > 0 {
		w.activity.Store(time.Now().UnixNano())
	}
}

// benign reports whether a copy error is just an expected teardown: a closed
// conn, or one the idle watchdog deliberately reclaimed.
func (w *idleWatch) benign(err error) bool {
	return errors.Is(err, net.ErrClosed) || w.fired.Load()
}

func (w *idleWatch) start(in, out net.Conn) (stop func()) {
	if w.idle <= 0 {
		return func() {}
	}
	w.touch()
	done := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }
	go func() {
		ticker := time.NewTicker(w.idle)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, w.activity.Load())) >= w.idle {
					// mark before close so the teardown is not counted as a copy error
					w.fired.Store(true)
					_ = in.Close()
					_ = out.Close()
					return
				}
			}
		}
	}()
	return stop
}

// relay buffers come from a pool. The thin wrappers hide ReadFrom/WriteTo
// so io.CopyBuffer runs its plain chunked loop: on Windows the
// TCPConn->TCPConn TransmitFile "splice" measured ~80 MB/s while a 64 KiB
// buffered loop passes ~400 MB/s over the same loopback path. The counting
// writer adds one atomic per 64 KiB chunk - free next to the syscalls.
type plainWriter struct{ io.Writer }
type plainReader struct{ io.Reader }

type countingWriter struct {
	io.Writer
	n    *atomic.Int64
	idle *idleWatch
}

func (c countingWriter) Write(p []byte) (int, error) {
	written, err := c.Writer.Write(p)
	c.n.Add(int64(written))
	c.idle.touch()
	return written, err
}

func relayCopy(dst io.Writer, counter *atomic.Int64, src io.Reader, idle *idleWatch) (int64, error) {
	buf := relayBufPool.Get().(*[]byte)
	defer relayBufPool.Put(buf)
	return io.CopyBuffer(plainWriter{countingWriter{dst, counter, idle}}, plainReader{src}, *buf)
}

func (proxy Proxy) route(address string) Outbound {
	tag := ""
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = strings.Split(address, ":")[0]
	}
	for _, r := range proxy.routers {
		if r.MatchAny(host) {
			tag = r.Tag
			break
		}
	}
	if outbound, exist := proxy.outbounds[tag]; exist {
		return *outbound
	}
	return *proxy.outbounds[""]
}

func readConfig(path string) (config Config, err error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return
	}
	err = json.Unmarshal(file, &config)
	if err != nil {
		return
	}
	return config, nil
}

func Startup(configPath string, routerPath string) (err error) {
	config, err := readConfig(configPath)
	if err != nil {
		return
	}

	config.RouterPath = routerPath
	p, err := newProxy(config)
	if err != nil {
		return
	}
	p.Start()
	return
}
