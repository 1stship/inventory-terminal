package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/pion/webrtc"
	"golang.org/x/crypto/ssh/terminal"
)

type soracomCredential struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type soracomToken struct {
	ApiKey     string `json:"apiKey"`
	OperatorId string `json:"operatorId"`
	Token      string `json:"token"`
}

type inventoryDevice struct {
	DeviceId string `json:"deviceId"`
	Endpoint string `json:"endpoint"`
}

type inventoryResourceInteger struct {
	Id    int    `json:"id"`
	Type  string `json:"type"`
	Value int    `json:"value"`
}

type inventoryResourceString struct {
	Id    int    `json:"id"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type valueJson struct {
	Value string `json:"value"`
}

func runClientMode(endpoint string) error {
	peerConnection, err := createPeerConnection()
	if err != nil {
		return err
	}
	openCh := make(chan bool)
	errCh := make(chan bool)
	setupClientDataChannel(peerConnection, openCh, errCh)
	email := getInput("Input Soracom account email: ")
	password := getPasswordInput("Input Soracom account password: ")

	fmt.Print("SORACOM認証中...")
	token, err := getSoracomToken(email, password)
	if err != nil {
		return err
	}
	fmt.Println("完了")
	fmt.Print("デバイス取得中...")
	device, err := getDevice(endpoint, token)
	if err != nil {
		return err
	}
	fmt.Println("完了")
	err = startSignaling(token, device)
	if err != nil {
		return err
	}
	fmt.Print("Offer受信中...")
	err = waitRecvOffer(token, device)
	if err != nil {
		return err
	}
	err = recvOffer(peerConnection, token, device)
	if err != nil {
		return err
	}
	fmt.Println("完了")
	fmt.Print("Answer送信中...")
	err = sendAnswer(peerConnection, token, device)
	if err != nil {
		return err
	}
	err = waitFinishSignaling(token, device)
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

	oldState, _ := terminal.MakeRaw((int)(os.Stdin.Fd()))
	defer func() { _ = terminal.Restore(int(os.Stdin.Fd()), oldState) }()

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

func setupClientDataChannel(peerConnection *webrtc.PeerConnection, openCh, errCh chan bool) {
	peerConnection.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		keepAliveCh := make(chan bool)
		finishCh := make(chan bool)
		dataChannel.OnOpen(func() {

			openCh <- true
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
					case <-keepAliveCh:
					}
				}
			}()

			buf := make([]byte, 1024)
			for {
				readLen, err := os.Stdin.Read(buf)
				if err != nil {
					if err == io.EOF {
						continue
					}
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
				if string(msg.Data) == "terminate" {
					finishCh <- true
					errCh <- true
				}
				keepAliveCh <- true
			} else {
				out := bufio.NewWriter(os.Stdout)
				out.Write(msg.Data)
				out.Flush()
			}
		})
	})
}

func getInput(inst string) string {
	for {
		fmt.Print(inst)
		scanner := bufio.NewScanner(os.Stdin)
		done := scanner.Scan()
		if done {
			input := scanner.Text()
			if input != "" {
				return input
			}
		}
	}
}

func getPasswordInput(inst string) string {
	for {
		fmt.Print(inst)
		password, err := terminal.ReadPassword(int(syscall.Stdin))
		if err != nil {
			continue
		} else {
			if string(password) != "" {
				fmt.Println("")
				return string(password)
			}
		}
	}
}

func requestHttp(method, url string, data interface{}, token *soracomToken) ([]byte, error) {
	var req *http.Request
	var err error
	if data != nil {
		payloadBytes, err := json.Marshal(data)
		if err != nil {
			return nil, errors.New("fail to serialize data")
		}
		body := bytes.NewReader(payloadBytes)
		req, err = http.NewRequest(method, url, body)
		if err != nil {
			return nil, errors.New("fail to create http request")
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(method, url, nil)
		if err != nil {
			return nil, errors.New("fail to create http request")
		}
	}
	req.Header.Set("Accept", "application/json")
	if token != nil {
		req.Header.Set("X-Soracom-Api-Key", token.ApiKey)
		req.Header.Set("X-Soracom-Token", token.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.New("fail to access via http")
	}
	defer resp.Body.Close()

	buf := make([]byte, 65536)
	readLen, err := resp.Body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return nil, errors.New("fail to read http body")
	}
	if resp.StatusCode < 300 {
		return buf[:readLen], nil
	} else {
		return nil, errors.New("fail to request API\n" + string(buf[:readLen]))
	}
}

func getSoracomToken(email, password string) (*soracomToken, error) {
	data := soracomCredential{Email: email, Password: password}
	buf, err := requestHttp("POST", "https://api.soracom.io/v1/auth", data, nil)
	if err != nil {
		return nil, err
	}
	var token = &soracomToken{}
	err = json.Unmarshal(buf, token)
	if err != nil {
		return nil, errors.New("fail to parse token")
	}
	return token, nil
}

func getDevice(endpoint string, token *soracomToken) (*inventoryDevice, error) {
	buf, err := requestHttp("GET", "https://api.soracom.io/v1/devices", nil, token)
	if err != nil {
		return nil, err
	}
	var devices []inventoryDevice
	err = json.Unmarshal(buf, &devices)
	if err != nil {
		return nil, errors.New("fail to parse devices")
	}
	for _, device := range devices {
		if device.Endpoint == endpoint {
			return &device, nil
		}
	}
	return nil, errors.New("device not found")
}

func startSignaling(token *soracomToken, device *inventoryDevice) error {
	_, err := requestHttp("POST", "https://api.soracom.io/v1/devices/"+device.DeviceId+"/9/0/4/execute", nil, token)
	if err != nil {
		return err
	}
	return nil
}

func waitRecvOffer(token *soracomToken, device *inventoryDevice) error {
	for i := 0; i < 60; i++ {
		time.Sleep(1 * time.Second)
		status, err := checkSignalingStatus(token, device)
		if err == nil && status == 1 {
			return nil
		}
	}
	return errors.New("timeout wait signaling")
}

func checkSignalingStatus(token *soracomToken, device *inventoryDevice) (int, error) {
	buf, err := requestHttp("GET", "https://api.soracom.io/v1/devices/"+device.DeviceId+"/9/0/7?model=false", nil, token)
	if err != nil {
		return 0, err
	}
	var status = &inventoryResourceInteger{}
	err = json.Unmarshal(buf, status)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fail to parse status")
		os.Exit(1)
	}
	return status.Value, nil
}

func recvOffer(peerConnection *webrtc.PeerConnection, token *soracomToken, device *inventoryDevice) error {
	offerDescriptionString := ""
	for i := 0; i < 4; i++ {
		description, err := readOfferDescription(token, device, i)
		if err != nil {
			return err
		}
		offerDescriptionString = offerDescriptionString + description
		if len(description) < 800 {
			break
		}
	}
	var offer webrtc.SessionDescription
	err := json.Unmarshal([]byte(offerDescriptionString), &offer)
	if err != nil {
		return errors.New("fail to parse offer")
	}
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		return errors.New("fail to set client remote description")
	}
	return nil
}

func readOfferDescription(token *soracomToken, device *inventoryDevice, instanceID int) (string, error) {
	buf, err := requestHttp("GET", "https://api.soracom.io/v1/devices/"+device.DeviceId+"/9/"+strconv.Itoa(instanceID)+"/0?model=false", nil, token)
	if err != nil {
		return "", err
	}
	var description = &inventoryResourceString{}
	err = json.Unmarshal(buf, description)
	if err != nil {
		return "", errors.New("fail to parse offer description")
	}
	return description.Value, nil
}

func sendAnswer(peerConnection *webrtc.PeerConnection, token *soracomToken, device *inventoryDevice) error {
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		return errors.New("fail to create answer")
	}
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		return errors.New("fail to set client local description")
	}
	answerDescriptionBytes, err := json.Marshal(answer)
	if err != nil {
		return errors.New("fail to serialize answer description")
	}
	for i := 0; i < 4; i++ {
		if len(answerDescriptionBytes) < (i+1)*800 {
			err := writeAnswerDescription(token, device, i, string(answerDescriptionBytes[(i*800):]))
			if err != nil {
				return err
			}
			break
		} else {
			err := writeAnswerDescription(token, device, i, string(answerDescriptionBytes[(i*800):((i+1)*800)]))
			if err != nil {
				return err
			}
		}
	}
	notifySendDescription(token, device)
	return nil
}

func writeAnswerDescription(token *soracomToken, device *inventoryDevice, instanceID int, description string) error {
	value := &valueJson{Value: description}
	_, err := requestHttp("PUT", "https://api.soracom.io/v1/devices/"+device.DeviceId+"/9/"+strconv.Itoa(instanceID)+"/3", value, token)
	if err != nil {
		return err
	}
	return nil
}

func notifySendDescription(token *soracomToken, device *inventoryDevice) error {
	value := &valueJson{Value: "done"}
	_, err := requestHttp("PUT", "https://api.soracom.io/v1/devices/"+device.DeviceId+"/9/0/14", value, token)
	if err != nil {
		return err
	}
	return nil
}

func waitFinishSignaling(token *soracomToken, device *inventoryDevice) error {
	for i := 0; i < 60; i++ {
		time.Sleep(1 * time.Second)
		status, err := checkSignalingStatus(token, device)
		if err == nil && status == 2 {
			return nil
		}
	}
	return errors.New("timeout to wait finish signaling")
}
