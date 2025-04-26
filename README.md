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

## 外部APIの利用
外部APIの利用が可能です。
取得したデータがjsonの場合はJSON.parseでパースしてください。
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

# メール送信 & Base64 ユーティリティについて

**`nyanSendMail`**（添付ファイル送信対応）および **`nyanFileToBase64`**（ファイル → Base64 変換）を用いた開発手順をまとめたものです。

---

## 事前準備
| 手順 | 内容 |
|------|------|
|①|`config.json` に SMTP 情報を設定する <br>（`host`, `port`, `username`, `password`, `from_email`, `from_name`, `tls`, `default_bcc`）|
|②|`main.go` をビルド／実行し、サーバーを起動する|
|③|JavaScript ファイルを作成し、`api.json` にエンドポイントを登録する|

> **メモ**: SMTP 認証方式は PLAIN を想定しています。別方式を使う場合は `sendMail()` 内で調整してください。

---

## nyanSendMail の使い方
### 1. 最小構成
```javascript
nyanSendMail({
  to: ["user@example.com"],
  subject: "テストメール",
  body: "こんにちは！",
  html: false
});
```

### 2. CC / BCC / デフォルト BCC
```javascript
nyanSendMail({
  to:   ["to1@example.com", "to2@example.com"],
  cc:   ["cc@example.com"],
  bcc:  ["bcc@example.com"], // config.json の default_bcc と自動マージされる
  subject: "CC/BCC テスト",
  body:    "本文です",
});
```

### 3. 添付ファイル（パス指定 & Base64 指定併用）
```javascript
// 文字列を Base64 化する簡易関数（UTF‑8 前提）
function base64Encode(str) {
  var tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=";
  var out="", i=0, c1, c2, c3;
  while(i < str.length){
    c1 = str.charCodeAt(i++) & 0xff;
    if(i === str.length){out += tbl.charAt(c1>>2)+tbl.charAt((c1&3)<<4)+"=="; break;}
    c2 = str.charCodeAt(i++);
    if(i === str.length){out += tbl.charAt(c1>>2)+tbl.charAt(((c1&3)<<4)|((c2&0xF0)>>4))+tbl.charAt((c2&0xF)<<2)+"="; break;}
    c3 = str.charCodeAt(i++);
    out += tbl.charAt(c1>>2)+tbl.charAt(((c1&3)<<4)|((c2&0xF0)>>4))+tbl.charAt(((c2&0xF)<<2)|((c3&0xC0)>>6))+tbl.charAt(c3&0x3F);
  }
  return out;
}

var msg = "これは Base64 でエンコードした添付テキストです。";

nyanSendMail({
  to: ["dest@example.com"],
  subject: "添付テスト",
  body: "<p>こんにちは！</p>",
  html: true,
  attachments: [
    { path: "./README.md" },                  // ローカルファイルを添付
    {                                         // 文字列 → Base64 で添付
      filename: "hello.txt",
      contentType: "text/plain",
      dataBase64: base64Encode(msg)
    }
  ]
});
```

---

## 4. nyanFileToBase64 の使い方
Go 側に追加されたユーティリティを呼び出し、ファイルを Base64 文字列として取得できます。

### 4‑1. 例：画像をメール添付
```javascript
// PNG を Base64 で読み取り、コンテンツタイプを自動判定
var pngInfo = nyanFileToBase64("./images/cat.png");
/* 返り値例
{
  base64: "iVBORw0KGgoAAAANSUhEUgA...",  // 改行なし
  contentType: "image/png"
}
*/

nyanSendMail({
  to: ["dest@example.com"],
  subject: "画像添付テスト",
  body: "<p>ネコ画像を送ります！</p>",
  html: true,
  attachments: [
    {
      filename: "cat.png",
      contentType: pngInfo.contentType, // 自動判定された MIME タイプ
      dataBase64: pngInfo.base64
    }
  ]
});
```

### 4‑2. 返却オブジェクト仕様
| フィールド | 型 | 説明 |
|-----------|----|------|
| `base64` | `string` | 改行を含まない Base64 文字列 |
| `contentType` | `string` | 拡張子から推測した `image/png` などの MIME タイプ（不明時は `application/octet-stream`） |
| `size` *(optional)* | `number` | バイトサイズ（実装例によっては追加可） |

---

## 5. トラブルシューティング
| 症状 | 原因 / 対処 |
|------|-------------|
| **添付ファイルが文字化け** | UTF‑8 で `base64Encode` しているか確認。Shift‑JIS など別エンコーディングのファイルは Go 側で読み取って `nyanFileToBase64` を利用するのがおすすめ。|
| **`attachments=0` とログに出る** | `attachments` 配列の中身が `path` / `dataBase64` いずれも空。キー名のスペルも確認する。|
| **SMTP 認証失敗** | `config.json` の `username` / `password`、`tls` フラグを確認。STARTTLS が必要な場合は Go 側 `sendMail` を修正。|


## 7. `config.json` のサンプルと各項目の説明

以下はーるを送信する際に必要な `config.json` の一例です。ご利用の環境に合わせて編集してください。

```json
{
  ...省略
  "smtp": {
    "host": "smtp.example.com",
    "port": 465,               // SMTPS の場合は 465、STARTTLS は 587 など
    "username": "user@example.com",
    "password": "passw0rd",
    "from_email": "noreply@example.com",
    "from_name":  "にゃん送信係",
    "tls": true,               // true=SMTPS/STARTTLS で暗号化接続
    "default_bcc": [           // すべてのメールに自動で BCC 追加（任意）
      "archive@example.com"
    ]
  }
}
```

### 項目解説
| キー | 必須 | 説明 |
|------|------|------|
| `host` / `port` | ○ | SMTP サーバーのホスト名とポート番号 |
| `username` / `password` | △ | 認証が必要な場合に設定。不要なら空文字で可 |
| `from_email` | ○ | 送信元メールアドレス（エンベロープ & ヘッダー） |
| `from_name`  | △ | 送信者名。UTF‑8 を想定（自動でエンコードされる） |
| `tls` | ○ | `true`: 暗号化接続（SMTPS/STARTTLS）を利用<br>`false`: 平文 SMTP |
| `default_bcc` | △ | ここに設定したアドレスは **nyanSendMail** 実行時に指定しなくても自動で BCC 追加されます。重複は `sendMail()` 内で除去されるため安心です。 |

> **ヒント:** STARTTLS が必要な SMTP でポート 587 を使う場合も `tls` を `true` にしておけば、自動でネゴシエーションされます（ライブラリ依存）。問題がある場合は `sendMail()` の TLS ハンドリングを調整してください。

---



# このAPIサーバの情報を取得する場合
http(s)://{hostname}:{port}/nyan にアクセスすると、このAPIサーバの情報を取得することができます。
http(s)://{hostname}:{port}/nyan/API名 にアクセスすると、そのAPIの情報を取得することができます。

# 予約語について
apiとnyanから始まるものは予約語となります。 
パラメータなどで使用しないようご注意ください。 
NyanQLとその仲間の共通ルールです。
