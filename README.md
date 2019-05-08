# inventory-terminal

inventory-terminalはWebRTCを利用したリモートアクセスツールです。

シグナリングにソラコム社のデバイス管理サービスSORACOM Inventoryを用いることにより、自動的に通信経路を確保します。

SORACOM Inventoryの説明はこちら

https://soracom.jp/services/inventory/

## 取得方法
go getコマンドで取得できます。
```sh
go get -u github.com/1stship/inventory-terminal
```

## 使用方法
デバイス側で以下を実行します。デバイス側の回線はSORACOM Airを使用している必要があります。
```sh
inventory-terminal --mode daemon
```

PC側で以下を実行します。
```sh
inventory-terminal
```

SORACOMのアカウントとパスワードを入力すると接続されます。

PC側で入力したコマンドがデバイス側で実行され、コマンドの実行結果を表示します。

## 複数デバイス対応

デフォルト設定では、エンドポイント名：inventory-terminalのデバイスを生成し、そのデバイスに対しアクセスします。

```sh
inventory-terminal --mode daemon --endpoint <任意のエンドポイント名>
```

```sh
inventory-terminal --endpoint <任意のエンドポイント名>
```

とすることで、複数デバイスに対応できます。

## ネットワーク環境について

- デバイス側 : SORACOM Airネットワーク
- PC側 : 外向きのポートが制限されていないネットワーク(TURN非対応のため)

## TODO

- 終了通知
- アクセスID、アクセスキー認証およびSAMユーザー認証の対応
- Goのパッケージ管理
