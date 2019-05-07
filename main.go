package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/1stship/inventoryd"
	"github.com/pion/webrtc"
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

const (
	resourcePath    string = "resources"
	modelsPath      string = "models"
	bootstrapServer string = "bootstrap.soracom.io:5683"
)

func main() {
	const version = "0.0.1"
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
		os.Exit(0)
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
	select {}
}

func runDaemonMode(endpoint string) error {
	exe, err := os.Executable()
	rootDir := filepath.Join(exe, "..")
	config := &inventoryd.Config{
		EndpointClientName: endpoint, RootPath: rootDir, ObserveInterval: 60, BootstrapServer: bootstrapServer}
	createDefaultFiles(config)
	handler := &inventoryd.HandlerFile{ResourceDirPath: filepath.Join(config.RootPath, resourcePath)}
	bootstrap := new(inventoryd.Inventoryd)
	err = bootstrap.Bootstrap(config, handler)
	if err != nil {
		return errors.New("ブートストラップが失敗しました。\nSORACOM Airで通信しているかをご確認の上、再度実行してください")
	}
	inventoryd := new(inventoryd.Inventoryd)
	if err := inventoryd.Initialize(config, handler); err != nil {
		return errors.New("inventorydの起動に失敗しました")
	}
	err = inventoryd.Run()
	if err != nil {
		return err
	}
	return nil
}

func runClientMode(endpoint string) error {
	peerConnection, err := createPeerConnection()
	if err != nil {
		return err
	}
	inputCh := make(chan string)
	outputCh := make(chan string)
	setupClientDataChannel(peerConnection, inputCh, outputCh)
	email := getInput("Input Soracom account email: ")
	password := getInput("Input Soracom account password: ")
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
	fmt.Println("完了")

	for {
		terminal(inputCh, outputCh)
	}
	return nil
}

func terminal(inputCh, outputCh chan string) {
	input := getInput("terminal: ")
	inputCh <- input
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
	case recv := <-outputCh:
		fmt.Print(recv)
	}
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
	err = setupDeviceDataChannel(peerConnection)
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
	return nil
}

func createPeerConnection() (*webrtc.PeerConnection, error) {
	config := webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}}
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, errors.New("fail to create webrtc connection")
	}
	return peerConnection, nil
}

// functions for client

func setupClientDataChannel(peerConnection *webrtc.PeerConnection, inputCh chan string, outputCh chan string) {
	peerConnection.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		dataChannel.OnOpen(func() {
			fmt.Println("inventory-terminal connected")
			for {
				input := <-inputCh
				err := dataChannel.SendText(input)
				if err != nil {
					fmt.Fprintln(os.Stderr, "fail to send data")
				}
			}
		})
		dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
			outputCh <- string(msg.Data)
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
	if err.Error() != "EOF" {
		return nil, errors.New("fail to read http body")
	}
	return buf[:readLen], nil
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
		return nil, errors.New("dfail to parse devices")
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

// functions for device

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

func setupDeviceDataChannel(peerConnection *webrtc.PeerConnection) error {
	dataChannel, err := peerConnection.CreateDataChannel("data", nil)
	if err != nil {
		return errors.New("fail to create data channel")
	}
	dataChannel.OnOpen(func() {
		fmt.Println("inventory-terminal connected")
	})
	dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		out, _ := exec.Command("/bin/sh", "-c", (string)(msg.Data)).CombinedOutput()
		dataChannel.Send(out)
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

// functions for daemon

func createDefaultFiles(config *inventoryd.Config) error {
	modelsDirPath := filepath.Join(config.RootPath, modelsPath)
	_, err := os.Stat(modelsDirPath)
	if os.IsNotExist(err) {
		err := os.MkdirAll(modelsDirPath, 0755)
		if err != nil {
			return err
		}
	}
	modelFiles, err := inventoryd.AssetDir(modelsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "定義ファイルが展開できませんでした")
	} else {
		for _, modelFile := range modelFiles {
			modelData, err := inventoryd.Asset(filepath.Join(modelsPath, modelFile))
			if err == nil {
				err = ioutil.WriteFile(filepath.Join(modelsDirPath, modelFile), modelData, 0644)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "定義ファイル(%s)が展開できませんでした\n", modelFile)
			}
		}
	}
	resourcesPath := filepath.Join(config.RootPath, resourcePath)
	_, err = os.Stat(resourcesPath)
	if os.IsNotExist(err) {
		err := os.MkdirAll(resourcesPath, 0755)
		if err != nil {
			return err
		}
	}
	objectDefinitions, err := inventoryd.LoadLwm2mDefinitions(filepath.Join(config.RootPath, modelsPath))
	for _, objectDefinition := range objectDefinitions {
		if objectDefinition.ID != 9 {
			continue
		}
		objectDirPath := filepath.Join(resourcesPath, "9")
		_, err := os.Stat(objectDirPath)
		if os.IsNotExist(err) {
			os.Mkdir(objectDirPath, 0755)
		}
		for i := 0; i < 4; i++ {
			instanceID := (uint16)(i)
			instanceDirPath := filepath.Join(objectDirPath, strconv.Itoa((int)(instanceID)))
			_, err := os.Stat(instanceDirPath)
			if !os.IsNotExist(err) {
				continue
			}
			os.Mkdir(instanceDirPath, 0755)
			for _, resourceDefinition := range objectDefinition.Resources {
				resourceFilePath := filepath.Join(instanceDirPath, strconv.Itoa((int)(resourceDefinition.ID)))
				_, err := os.Stat(resourceFilePath)
				if !os.IsNotExist(err) {
					continue
				}
				exe, err := os.Executable()
				if resourceDefinition.Excutable {
					var script string
					if resourceDefinition.ID == 4 {
						script = fmt.Sprintf("#/bin/bash\n%s --mode execute", exe)
					} else if resourceDefinition.ID == 6 {
						script = fmt.Sprintf("#/bin/bash\npkill -f -x \"%s --mode device\"", exe)
					} else {
						script = fmt.Sprintf("#/bin/bash\necho \"execute %s script\"", resourceDefinition.Name)
					}
					ioutil.WriteFile(resourceFilePath, []byte(script), 0755)
					continue
				}
				switch resourceDefinition.Type {
				case 0, 4:
					ioutil.WriteFile(resourceFilePath, []byte{}, 0644)
				case 1, 5:
					ioutil.WriteFile(resourceFilePath, []byte("0"), 0644)
				case 2:
					ioutil.WriteFile(resourceFilePath, []byte("0.0"), 0644)
				case 3:
					ioutil.WriteFile(resourceFilePath, []byte("false"), 0644)
				case 6:
					ioutil.WriteFile(resourceFilePath, []byte("0:0"), 0644)
				}
			}
		}
	}
	return nil
}
