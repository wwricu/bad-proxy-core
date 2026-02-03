package proxy

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"

	"github.com/wwricu/bad-proxy-core/router"
	"github.com/wwricu/bad-proxy-core/transport"
)

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
	BTP    = "btp"
	SOCKS  = "socks"
	HTTP   = "http"
	TROJAN = "trojan"
)

func newProxy(config Config) (newProxy Proxy) {
	newProxy.outbounds = make(map[string]*Outbound, 10)
	newProxy.routers = make([]router.Router, 0)
	for _, r := range config.Router {
		rules := make([]string, 0)
		for _, rule := range r.Rules {
			rules = append(rules, rule)
		}
		newRouter, err := router.NewRouter(r.Tag, rules, config.RouterPath)
		if err != nil {
			log.Println("wrong router:", r.Tag)
			continue
		}
		newProxy.routers = append(newProxy.routers, *newRouter)
	}

	for _, in := range config.Inbounds {
		newInbound := Inbound{
			secret:      in.Secret,
			address:     in.Host + ":" + in.Port,
			protocol:    in.Protocol,
			transport:   transport.GetProtocol(in.Transport),
			wsPath:      in.WsPath,
			tlsCertPath: in.TlsCertPath,
			tlsKeyPath:  in.TlsKeyPath,
		}
		newProxy.inbounds = append(newProxy.inbounds, newInbound)
	}

	for _, out := range config.Outbounds {
		newOutbound := Outbound{
			tag:       out.Tag,
			secret:    out.Secret,
			address:   out.Host + ":" + out.Port,
			protocol:  out.Protocol,
			transport: transport.GetProtocol(out.Transport),
			wsPath:    out.WsPath,
		}
		_, exist := newProxy.outbounds[out.Tag]
		if exist == true {
			log.Fatalln("duplicate outbound tag")
		}
		newProxy.outbounds[out.Tag] = &newOutbound
	}

	if _, exist := newProxy.outbounds[""]; exist != true {
		log.Fatalln("No Default outbound!")
	}
	return
}

func (proxy Proxy) Start() {
	for _, inbound := range proxy.inbounds {
		go func(inbound Inbound) { // TODO: Why reference pass does not work.
			log.Println(inbound.protocol, "listen on", inbound.address)
			err := inbound.Listen()
			if err != nil {
				log.Fatalf("inbound on %s dead, %s\n", inbound.address, err.Error())
				return
			}
			for {
				in, err := inbound.Accept()
				if err != nil {
					log.Printf("inbound on %s failed to accept!\n", inbound.address)
					continue
				}
				go proxy.proxy(in)
			}
		}(inbound)
	}
}

func (proxy Proxy) proxy(in InboundConnect) {
	defer func() { // recover any panic to avoid quiting from main loop.
		if r := recover(); r != nil {
			log.Println(r)
		}
		_ = in.Close()
	}()

	address, payload, err := in.Connect() // handshake
	if err != nil {
		log.Println("inbound connect failed:", err)
		in.Fallback(payload)
		return
	}
	// routing to find outbound template
	outbound := proxy.route(address)
	out, err := outbound.Dial(address, payload) // handshake
	defer func() {
		_ = out.Close()
	}()
	if err != nil {
		log.Printf("outbound dial to %s failed, err=%v\n", address, err)
		return
	}

	done := make(chan struct{})
	go func() {
		if _, err := io.Copy(in, out); err != nil {
			log.Printf("copy out->in: %v\n", err)
		}
		close(done)
	}()
	if _, err = io.Copy(out, in); err != nil {
		log.Printf("copy in->out: %v\n", err)
	}

	<-done
}

func (proxy Proxy) route(address string) Outbound {
	// If s does not contain sep and sep is not empty,
	// Split returns a slice of length 1 whose only element is s
	tag := ""
	host := strings.Split(address, ":")[0]
	for _, r := range proxy.routers {
		if r.MatchAny(host) == true {
			tag = r.Tag
			break
		}
	}
	outbound, exist := proxy.outbounds[tag]
	if exist == false {
		return *proxy.outbounds[""]
	}
	return *outbound
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
	newProxy(config).Start()
	return
}
