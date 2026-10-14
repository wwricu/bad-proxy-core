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
	"time"

	"github.com/wwricu/bad-proxy-core/router"
)

const relayBufferSize = 64 * 1024

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
}

type Config struct {
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
		newProxy.inbounds = append(newProxy.inbounds, NewInbound(in))
	}

	for _, out := range config.Outbounds {
		newOutbound := NewOutbound(out)
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
		go proxy.proxy(in)
	}
}

func (proxy Proxy) proxy(in InboundConnect) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("relay panic:", r)
		}
		_ = in.Close()
	}()

	address, err := in.Connect() // handshake
	if err != nil {
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

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := relayCopy(in, out); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("copy out->in: %v\n", err)
		}
	}()
	if _, err = relayCopy(out, in); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Printf("copy in->out: %v\n", err)
	}
	<-done
}

// relay buffers come from a pool. The thin wrappers hide ReadFrom/WriteTo
// so io.CopyBuffer runs its plain chunked loop: on Windows the
// TCPConn->TCPConn TransmitFile "splice" measured ~80 MB/s while a 64 KiB
// buffered loop passes ~400 MB/s over the same loopback path.
type plainWriter struct{ io.Writer }
type plainReader struct{ io.Reader }

func relayCopy(dst io.Writer, src io.Reader) (int64, error) {
	buf := relayBufPool.Get().(*[]byte)
	defer relayBufPool.Put(buf)
	return io.CopyBuffer(plainWriter{dst}, plainReader{src}, *buf)
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
