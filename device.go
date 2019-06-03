package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/kr/pty"
	"github.com/pion/webrtc"
	"golang.org/x/crypto/ssh/terminal"
)

type deviceShell struct {
	ptmx  *os.File
	state *terminal.State
}

func runDeviceMode(rootDir string) error {
	err := clearWebrtcResources(rootDir)
	if err != nil {
		return err
	}
	peerConnection, err := createPeerConnection()
	if err != nil {
		return err
	}
	openCh := make(chan bool)
	errCh := make(chan bool)
	err = setupDeviceDataChannel(peerConnection, openCh, errCh)
	if err != nil {
		return err
	}
	err = createOffer(peerConnection, rootDir)
	if err != nil {
		return err
	}
	err = waitRecvAnswer(rootDir)
	if err != nil {
		return err
	}
	err = recvAnswer(peerConnection, rootDir)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		return errors.New("timeout wait open webRTC data channel")
	case <-openCh:
	}

	trapSignals := []os.Signal{
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, trapSignals...)
	select {
	case <-sigCh:
		return nil
	case <-errCh:
		return nil
	}
}

func clearWebrtcResources(rootDir string) error {
	resources := [][4]string{
		{"9", "0", "0", ""}, {"9", "1", "0", ""}, {"9", "2", "0", ""}, {"9", "3", "0", ""},
		{"9", "0", "3", ""}, {"9", "1", "3", ""}, {"9", "2", "3", ""}, {"9", "3", "3", ""},
		{"9", "0", "7", "0"},
		{"9", "0", "14", ""}}
	for _, resource := range resources {
		fileForClear := filepath.Join(rootDir, resourcePath, resource[0], resource[1], resource[2])
		err := ioutil.WriteFile(fileForClear, []byte(resource[3]), 0644)
		if err != nil {
			return errors.New("fail to clear resource")
		}
	}
	return nil
}

func setupDeviceDataChannel(peerConnection *webrtc.PeerConnection, openCh, errCh chan bool) error {
	dataChannel, err := peerConnection.CreateDataChannel("data", nil)
	if err != nil {
		return errors.New("fail to create data channel")
	}
	shell := &deviceShell{}
	keepAliveCh := make(chan bool)
	finishCh := make(chan bool)

	dataChannel.OnOpen(func() {
		openCh <- true
		cmd := exec.Command("/bin/bash", "-l")
		shell.ptmx, _ = pty.Start(cmd)
		shell.state, _ = terminal.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			panic(err)
		}
		defer func() { _ = terminal.Restore(int(os.Stdin.Fd()), shell.state) }()
		go func() {
			io.Copy(shell.ptmx, os.Stdin)
		}()

		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-finishCh:
					return
				case <-t.C:
					dataChannel.SendText("Keep Alive")
				}
			}
		}()

		go func() {
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				select {
				case <-ctx.Done():
					finishCh <- true
					errCh <- true
					return
				case <-keepAliveCh:
				}
			}
		}()

		buf := make([]byte, 4096)
		for {
			readLen, err := shell.ptmx.Read(buf)
			if err != nil {
				if err == io.EOF {
					continue
				}
				dataChannel.SendText("terminate")

				// terminateが届くまでの間にプロセスが終了しないように一定時間待つ
				time.Sleep(5 * time.Second)
				return
			} else {
				err = dataChannel.Send(buf[:readLen])
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					return
				}
			}
		}
	})

	dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		if msg.IsString {
			keepAliveCh <- true
		} else if shell.ptmx != nil {
			shell.ptmx.Write(msg.Data)
		}
	})
	return nil
}

func createOffer(peerConnection *webrtc.PeerConnection, rootDir string) error {
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		return errors.New("fail to create offer")
	}
	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		return errors.New("fail to set device local description")
	}
	offerDescriptionBytes, err := json.Marshal(offer)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fail to serialize offer")
		os.Exit(1)
	}
	// offerを対応するリソースに保存
	for i := 0; i < 4; i++ {
		offerFile := filepath.Join(rootDir, resourcePath, "9", strconv.Itoa(i), "0")
		if len(offerDescriptionBytes) < (i+1)*800 {
			ioutil.WriteFile(offerFile, offerDescriptionBytes[(i*800):], 0644)
			break
		} else {
			ioutil.WriteFile(offerFile, offerDescriptionBytes[(i*800):((i+1)*800)], 0644)
		}
	}
	err = updateStatus("1", rootDir)
	if err != nil {
		return err
	}
	return nil
}

func waitRecvAnswer(rootDir string) error {
	notifyFile := filepath.Join(rootDir, resourcePath, "9", "0", "14")
	for i := 0; i < 120; i++ {
		time.Sleep(1 * time.Second)
		notify, err := ioutil.ReadFile(notifyFile)
		if err != nil {
			continue
		}
		if string(notify) == "done" {
			return nil
		}
	}
	return errors.New("timeout to recv answer")
}

func recvAnswer(peerConnection *webrtc.PeerConnection, rootDir string) error {
	// 対応するリソースからAnswerを読み出し
	answerDescriptionBytes := []byte{}
	for i := 0; i < 4; i++ {
		answerFile := filepath.Join(rootDir, resourcePath, "9", strconv.Itoa(i), "3")
		description, err := ioutil.ReadFile(answerFile)
		if err != nil {
			return errors.New("fail to read answer")
		}
		answerDescriptionBytes = append(answerDescriptionBytes, description...)
		if len(description) < 800 {
			break
		}
	}
	var answer webrtc.SessionDescription
	err := json.Unmarshal(answerDescriptionBytes, &answer)
	if err != nil {
		return errors.New("fail to parse answer")
	}
	err = peerConnection.SetRemoteDescription(answer)
	if err != nil {
		return errors.New("fail to set device remote description")
	}
	updateStatus("2", rootDir)
	return nil
}

func updateStatus(status, rootDir string) error {
	statusFile := filepath.Join(rootDir, resourcePath, "9", "0", "7")
	err := ioutil.WriteFile(statusFile, []byte(status), 0644)
	if err != nil {
		return errors.New("fail to update status")
	}
	return nil
}
