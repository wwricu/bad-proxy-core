package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/wwricu/bad-proxy-core/proxy"
)

const (
	versionMsg = "v1.2.0"
	helpMsg    = `
	Bad Proxy Core (BPC) - a small multiplexed proxy, one binary for server and client
	Run application:
		bad-proxy-core <options>
	Options:
		--help              show this help message
		--version           show version message
		--config            specify config file path, default config.json
		--router-path       specify binary routing rule file path, default rules.dat

	See README.md for the config reference and an nginx deployment guide.
	`
)

func main() {
	helpFlag := flag.Bool("help", false, "show help info")
	versionFlag := flag.Bool("version", false, "show version info")
	configPath := flag.String("config", "config.json", "config file path")
	routerPath := flag.String("router-path", "rules.dat", "router data path")
	flag.Parse()

	if *versionFlag == true {
		fmt.Println("Bad Proxy Golang", versionMsg)
		return
	}
	if *helpFlag == true {
		fmt.Print(helpMsg)
		return
	}
	log.SetFlags(log.Lshortfile)
	if err := proxy.Startup(*configPath, *routerPath); err != nil {
		log.Println("error starting proxy:", err)
		os.Exit(1)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig)
	<-sig
}
