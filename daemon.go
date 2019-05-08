package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"

	"github.com/1stship/inventoryd"
)

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
