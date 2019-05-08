package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/pion/webrtc"
)

const (
	resourcePath    string = "resources"
	modelsPath      string = "models"
	bootstrapServer string = "bootstrap.soracom.io:5683"
	stunServer      string = "stun:stun.l.google.com:19302"
)

func main() {
	const version = "0.1.0"
	dispVersion := false
	var mode string
	var endpoint string
	flag.BoolVar(&dispVersion, "v", false, "バージョン表示")
	flag.BoolVar(&dispVersion, "version", false, "バージョン表示")
	flag.StringVar(&mode, "mode", "client", "モード指定(daemon/client/device)")
	flag.StringVar(&endpoint, "endpoint", "inventory-terminal", "エンドポイント名")
	flag.Parse()

	if dispVersion {
		fmt.Printf("inventory-terminal: Ver %s\n", version)
		os.Exit(0)
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fail to get executable path")
		os.Exit(1)
	}
	rootDir := filepath.Join(exe, "..")

	switch mode {
	case "daemon":
		err = runDaemonMode(endpoint)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "client":
		err = runClientMode(endpoint)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "device":
		err = runDeviceMode(rootDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "execute":
		cmd := exec.Command(exe, "--mode", "device")
		cmd.Start()
		os.Exit(0)
	default:
		err = errors.New("Invalid mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func createPeerConnection() (*webrtc.PeerConnection, error) {
	config := webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{stunServer}}}}
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, errors.New("fail to create webrtc connection")
	}
	return peerConnection, nil
}
