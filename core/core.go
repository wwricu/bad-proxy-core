package core

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"time"

	"github.com/wwricu/bad-proxy-core/logger"
	"github.com/wwricu/bad-proxy-core/router"
)

type Proxy struct {
	inbounds  []Inbound
	outbounds map[string]*Outbound
	routers   []*router.Router
	settings  settings
}

type Config struct {
	General    GeneralConfig    `json:"general"`
	Inbounds   []InboundConfig  `json:"inbounds"`
	Outbounds  []OutboundConfig `json:"outbounds"`
	Router     []router.Config  `json:"routers"`
	RouterPath string
}

// FREEDOM is core's direct-connect routing target, not a handshake protocol:
// it opens the target address itself instead of dialing an upstream, so the
// handshake protocol names live with the protocols package instead.
const FREEDOM = "freedom"

func newProxy(config Config, set settings) (*Proxy, error) {
	// set once, before any tunnel exists: no relay ever copies through a buffer
	// sized from a stale setting
	relayBufferSize = set.relayBufferKiB * 1024
	p := Proxy{
		settings:  set,
		outbounds: make(map[string]*Outbound, len(config.Outbounds)),
	}
	for _, r := range config.Router {
		p.routers = append(p.routers, router.NewRouter(r.Tag, r.Rules, config.RouterPath))
	}

	for _, in := range config.Inbounds {
		p.inbounds = append(p.inbounds, NewInbound(in, set))
	}

	for _, out := range config.Outbounds {
		if _, exist := p.outbounds[out.Tag]; exist {
			return nil, errors.New("duplicate outbound tag " + out.Tag)
		}
		created := NewOutbound(out, set)
		p.outbounds[out.Tag] = &created
	}

	if _, exist := p.outbounds[""]; !exist {
		return nil, errors.New("no default outbound, add one with empty \"tag\" (use \"" + FREEDOM + "\" for direct)")
	}
	return &p, nil
}

func (proxy *Proxy) Start() {
	for i := range proxy.inbounds {
		go func(inbound *Inbound) {
			if err := inbound.Listen(); err != nil {
				// one broken inbound must not take the whole process down
				logger.Errorf("inbound on %s disabled: %v", inbound.address, err)
				return
			}
			logger.Infof("%s listen on %s", inbound.protocol, inbound.address)
			proxy.serve(inbound)
		}(&proxy.inbounds[i]) // pointer into the slice so Stop can see the listener
	}
}

// Stop closes every inbound listener, which unblocks the serve loops (Accept
// returns net.ErrClosed) and stops accepting new tunnels. The Proxy is
// single-use: Start again on a fresh instance, not this one. In-flight relays
// keep their own connections and drain naturally.
func (proxy *Proxy) Stop() {
	for i := range proxy.inbounds {
		_ = proxy.inbounds[i].Close()
	}
}

func (proxy Proxy) serve(inbound *Inbound) {
	failures := 0
	for {
		in, err := inbound.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				logger.Infof("inbound on %s closed", inbound.address)
				return
			}
			failures++
			if failures > 32 {
				logger.Errorf("inbound on %s giving up after repeated errors", inbound.address)
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
			logger.Errorf("relay panic: %v", r)
		}
		_ = in.Close()
	}()

	address, err := in.Connect() // handshake
	if err != nil {
		// routine: internet probes and bad credentials land here constantly,
		// so keep it at debug to avoid flooding the console
		logger.Debugf("inbound connect failed: %v", err)
		in.Fallback()
		return
	}
	_ = in.SetDeadline(time.Time{}) // relay phase runs without handshake deadline

	outbound := proxy.route(address)
	out, err := outbound.Dial(address)
	if err != nil {
		logger.Warnf("outbound dial to %s failed: %v", address, err)
		return
	}
	defer func() {
		_ = out.Close()
	}()

	// The pair is modelled once and owns everything per-connection: both byte
	// pumps, the shared activity stamp, the reclaim timer. A quiet tunnel - an
	// origin keep-alive, a vanished peer - would otherwise pin both ends and two
	// buffers for hours. Reclaim is on by default; relay_idle_timeout "0s"
	// turns it off. See tunnel.go.
	newTunnel(in, out, proxy.settings.relayIdle).run()
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

// New builds a Proxy from an in-memory Config without touching the filesystem,
// for embedding (e.g. a desktop GUI) that wants Start/Stop around a live
// instance. It configures process-wide logging from the resolved settings and
// validates the config exactly like the CLI Startup path.
func New(cfg Config) (*Proxy, error) {
	set, err := cfg.resolveSettings()
	if err != nil {
		return nil, err
	}
	if err := logger.Configure(set.logFile, set.logLevel, set.consoleLevel); err != nil {
		logger.Warnf("log file %s unavailable, console only: %v", set.logFile, err)
	}
	return newProxy(cfg, set)
}

// Startup is the CLI path: the config comes from a file, and the engine is
// listening before this returns.
func Startup(configPath string, routerPath string) error {
	file, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var config Config
	if err = json.Unmarshal(file, &config); err != nil {
		return err
	}
	config.RouterPath = routerPath

	p, err := New(config)
	if err != nil {
		return err
	}
	p.Start()
	return nil
}
