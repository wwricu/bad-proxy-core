package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/wwricu/bad-proxy-core/core"
	"github.com/wwricu/bad-proxy-core/logger"
)

const (
	versionMsg = "v2.0.0-beta2"
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
	if err := core.Startup(*configPath, *routerPath); err != nil {
		logger.Errorf("error starting proxy: %v", err)
		os.Exit(1)
	}
	sig := make(chan os.Signal, 1)
	// Only stop on intentional termination signals. Subscribing to every
	// signal (signal.Notify with no list) also captures SIGPIPE, and Go
	// delivers SIGPIPE to a Notify channel instead of turning a write to a
	// disconnected peer into a plain EPIPE. A proxy hits that constantly
	// (clients hang up mid-relay), so it would tear down the whole process
	// here. Restricting the set keeps per-connection breaks per-connection.
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
