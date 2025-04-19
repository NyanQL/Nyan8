# Nyan8
Nyan8(にゃんぱち)はGoLangで作られたサーバーサイドJavaScript実行環境です。

Nyan8ではjavascriptの実行エンジンは Goja( https://github.com/dop251/goja ) を採用しています。
これはECMAScript 5.1 準拠のJavaScriptインタープリターです。

# 設定
## APIサーバの設定ファイル config.json
```
{
  "name": "Nyan8の名前",
  "profile": "Nyan8の自己紹介",
  "version": "このサーバのversion",
  "Port": ポート番号,
  "certPath": "SSL証明書のパス",
  "keyPath": "SSL証明書のキーのパス",
  "javascript_include": [
    "機能追加として読み込むjavascriptファイルのパス。配列で複数ファイル定義可能"
  ],
  "log": {
    "Filename": "ログファイルのパス",
    "MaxSize": 5,
    "MaxBackups": 3,
    "MaxAge": 7,
    "Compress": true,
    "EnableLogging": false
  }
}
```


# ログ設定
config.jsonで指定するログ設定の詳細は以下の通りです。

* Filename: ログファイルの保存場所を指定します。例: "./logs/nyanql.log"
* MaxSize: ログファイルの最大サイズ（MB単位）。このサイズを超えると新しいログファイルが作成されます。例: 5（5MB）
* MaxBackups: 保持する古いログファイルの最大数。例: 3（最新の3つのログファイルを保持）
* MaxAge: ログファイルを保持する最大日数（日単位）。例: 7（7日間のログファイルを保持し、それを超えると削除）
* Compress: 古いログファイルを圧縮するかどうか。trueに設定すると、古いログファイルがgzip形式で圧縮されます。例: true
* EnableLogging: ログの出力を有効にするかどうか。falseに設定すると、ログは標準出力（コンソール）に出力されます。例: false

## API設定ファイル: api.json
api.jsonは、APIエンドポイントごとに実行するjavascriptファイルを指定するための設定ファイルです。
`http(s)://<hostname>:<port>/<APIの名前>` にアクセスすると、指定されたjavascriptファイルが実行されます。
また、WebSocket での接続にも対応しており、サーバー側からの push 通知を受け取ることができます。
pushをする必要がないapiの設定では、pushの項目を省略してください。

```json
{
  "apiの名前": {
    "script": "実行されるjavascriptファイルのパス",
    "description": "このAPIの説明",
    "push": "このAPIを実行した結果 pushを受け取れるAPIの名前"
  },
  "apiの名前2": {
    "script": "実行されるjavascriptファイルのパス",
    "description": "このAPIの説明",
  }
}
```

# このAPIサーバの情報を取得する場合
`http(s)://<hostname>:<port>/nyan` にアクセスすると、このAPIサーバの情報を取得することができます。


# アプリケーションの実行方法
config.jsonとapi.jsonを編集してください。

以下のOSで動きます。
https://github.com/NyanQL/Nyan8/releases から利用したいOS用のzipファイルをダウンロードし利用ください。
* Windows
* Mac
* Linux

# ライセンスについて
Nyan8はMITライセンスで提供されています。詳細は[LICENSE.md](LICENSE.md)を参照してください。

# GojaでうごくJavascriptのサンプル
## postやgetやjsonなどで受信されたパラメータの取得
nyanAllParamsに格納されています。

```javascript
console.log(nyanAllParams);
```

## console.log が使えます
ログファイルの設定を有効にしている場合はログファイルに出力されます。
無効にしている場合はターミナルに出力されます。

```javascript
console.log("Hello, NyanPUI!");
```

## Cookieの操作
cookieの設定が可能です。

```javascript
nyanSetCookie("nyanpui", "kawaii");
```

cookieの取得が可能です。

```javascript
console.log(nyanGetCookie("nyanpui"));
```

## localStorageの操作
localStorageの設定が可能です。

```javascript
nyanSetItem("nyanpui", "kawaii");
```

localStorageの取得が可能です。

```javascript
nyanGetItem("nyanpui");
```

## Ajaxの操作
Ajaxの操作が可能です。
取得したデータはJSON.parseでパースしてください。
getでの取得の場合は、nyanGetAPI を使用します。
jsonでの取得の場合は、nyanJsonAPI を使用します。

getでの取得の場合


```javascript
//apiのURL  apiURL
//basic認証のID  apiUser
//basic認証のパスワード apiPass
//javascript内でデータとして扱う場合、JSON.parse()で文字列から変換をする必要があります。
console.log(nyanGetAPI(apiURL,apiUser,apiPass));
```

jsonでの取得の場合

```javascript
//apiのURL  apiURL
//basic認証のID  apiUser
//basic認証のパスワード apiPass
//javascript内でデータとして扱う場合、JSON.parse()で文字列から変換をする必要があります。
const data = {
            api: "create_user",
            username: nyanAllParams.username,
            password: nyanAllParams.password,
            email: nyanAllParams.email,
            salt: saltKey
        };
const result = nyanJsonAPI(
        apiURL,
        JSON.stringify(data),
        apiUser,
        apiPass
    );
const resultData = JSON.parse(result);
```

ヘッダー情報をJSON文字列で渡す例

```js
// ヘッダー情報をJSON文字列で渡す例
let headers = JSON.stringify({
    "X-Custom-Header": "myValue",
    "X-Another-Header": "anotherValue"
});
let result = nyanJsonAPI("https://example.com/api", '{"key":"value"}', "user", "pass", headers);
```

## hostでのコマンド実行と結果の取得
hostでのコマンド実行が可能です。

```javascript
console.log(nyanHostExec("ls"));
```

実行結果は次のような構成になって取得できます。

```json
{"success":true,"exit_code":0,"stdout":"コマンドの実行結果","stderr":""}
```

* success : コマンドの実行が成功したかどうか
* exit_code : コマンドの終了コード
* stdout : 標準出力
* stderr : 標準エラー出力

## ファイルの読み込み

ファイルの読み込みができます。

```js
let text = nyanGetFile("ファイルのパス");
let data = JSON.parse(text);
```

## APIの情報に対して、受け入れ項目、出力項目を出力について

APIの情報に対して、受け入れ項目、出力項目を出力できます。
例

``` js
const nyanAcceptedParams = {"addNumber": 2};
const nyanOutputColumns =  ["result"];
```

出力例　http://localhost:8889/nyan/add
```json
{
  "api": "add",
  "description": "2に対して足し算した結果を返します。",
  "nyanAcceptedParams": {
    "addNumber": 2
  },
  "nyanOutputColumns": [
    "result"
  ]
}
```

# JSON-RPC対応
http(s)://{hostname}:{port}/nyan-rpc にアクセスすると、JSON-RPCのAPIを利用することができます。
Nyan8はJSON-RPC 2.0に準拠したAPIを提供しています。
ただし、現在 6.Batch については未実装です。
JSON-RPC 2.0の仕様については、[こちら](https://www.jsonrpc.org/specification)を参照してください。
以下のようなJSON-RPCリクエストを送信することで、APIを呼び出すことができます。

```json
{
  "jsonrpc": "2.0",
  "method": "api名",
  "params": {
    "param1": "value1",
    "param2": "value2"
  },
  "id": 1
}
```


# このAPIサーバの情報を取得する場合
http(s)://{hostname}:{port}/nyan にアクセスすると、このAPIサーバの情報を取得することができます。
http(s)://{hostname}:{port}/nyan/API名 にアクセスすると、そのAPIの情報を取得することができます。

# 予約語について
apiとnyanから始まるものは予約語となります。 
パラメータなどで使用しないようご注意ください。 
NyanQLとその仲間の共通ルールです。
