package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go_proxy/proxy"
	"os"
)

const (
	versionMsg = "v1.0.4"
	helpMsg    = `
	Bad proxy go is a primitive tool for breaching censorship
	Run application:
		bad_proxy <options>
	Options:
		--help              show this help message
		--version           show version message
		--config            specify config file path, default conf/config.json
		--router-path       specify binary routing file path, default conf/rules.dat
	`
)

func readConfig(path string) (config proxy.Config, err error) {
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

	config, err := readConfig(*configPath)
	if err != nil {
		fmt.Print("failed to read config\n")
		return
	}

	config.RouterPath = *routerPath
	proxy.NewProxy(config).Start()
	<-make(chan os.Signal)
}
