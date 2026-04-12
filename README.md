# Nyan8


**Nyan8（にゃんぱち）** は Go 言語で実装されたサーバーサイド JavaScript 実行環境です。
JavaScript エンジンに [**Goja**](https://github.com/dop251/goja) を採用し、ECMAScript 5.1 準拠のスクリプトを安全かつ高速に実行できます。
javascriptを書くだけで 手軽にAPIサービスを作れます。

---

## 1  特徴

| 機能 | 概要 |
|------|------|
| **JavaScript API** | HTTP/HTTPS 経由で JS ファイルを呼び出し、JSON を返却 |
| **WebSocket Push** | `api.json` の `push` 設定だけで双方向通信を実現 |
| **JSON‑RPC 2.0** | `/nyan‑rpc` エンドポイントで RPC を提供（Batch は今後対応） |
| **メール送信** | `nyanSendMail` で CC/BCC・添付ファイルを含むメールを送信可能 |
| **ファイル→Base64** | `nyanReadFileB64` でファイルを Base64 文字列へ変換 |
| **ホストコマンド実行** | `nyanHostExec` でシェルコマンドを呼び出し、結果を JSON 取得 |
| **ログローテーション** | `lumberjack` による自動ローテーション／圧縮対応 |

---

## 2  インストール

1. [Releases](https://github.com/NyanQL/Nyan8/releases) から OS 向け zip を取得
2. 展開して実行ファイル（`nyan8` / `nyan8.exe`）を配置
3. `config.json` と `api.json` をプロジェクトルートに用意
4. 実行：
   ```bash
   ./nyan8   # Windows は nyan8.exe
   ```

---

## 3  設定ファイル

### 3‑1  `config.json`

```jsonc
{
  "name": "Nyan8 Server",          // サーバー名
  "profile": "dev",               // 自己紹介や環境名
  "version": "1.0.0",             // バージョン
  "Port": 8080,                     // HTTP/HTTPS 待受ポート
  "certPath": "cert.pem",         // SSL 証明書（未使用時は空）
  "keyPath":  "key.pem",          // 秘密鍵（未使用時は空）
  "javascript_include": [           // 共通 JS をロード（任意複数可）
    "libs/common.js"
  ],
  "log": {
    "Filename": "nyan.log",        // ログファイル
    "MaxSize": 10,                  // MB
    "MaxBackups": 5,                // 世代数
    "MaxAge": 30,                   // 日数
    "Compress": true,               // 圧縮
    "EnableLogging": true           // false=コンソールのみ
  },
  "smtp": {
    "host": "smtp.example.com",
    "port": 465,
    "username": "user@example.com",
    "password": "passw0rd",
    "from_email": "noreply@example.com",
    "from_name": "にゃん送信係",
    "tls": true,
    "default_bcc": ["archive@example.com"]
  }
}
```

<details>
<summary>ログ設定項目の説明</summary>

* **Filename** – 出力先ファイルパス
* **MaxSize** – 1 ファイルの上限サイズ（MB）
* **MaxBackups** – 保持世代数
* **MaxAge** – 保持日数
* **Compress** – 過去ファイルを gzip 圧縮
* **EnableLogging** – false で標準出力のみ

</details>

### 3‑2  `api.json`

```jsonc
{
  "add": {
    "script": "apis/add.js",        // 実行する JS
    "description": "2 に足す API",
    "push": "add_push"              // 省略可
  },
  "add_push": {
    "script": "apis/add_push.js",
    "description": "add の結果を push 配信"
  }
}
```

* `/add` に HTTP アクセス → `apis/add.js` が実行
* WebSocket 接続 `/add_push` を張っておけば、`add` 完了時に push が届きます

### type と WebSocket クライアント

- `type` を省略した場合は従来通り HTTP/WS サーバーの API (`"type": "api"`) として動作します。
- `type: "ws_client"` を指定すると Nyan8 自身が WebSocket クライアントになり、起動時に常時接続します。
- `connectURL` が `env:XXXX` の場合、環境変数 `XXXX` で接続 URL を解決します。

```jsonc
"websocket_clients_local": {
  "type": "ws_client",
  "script": "./javascript/ws_client_handler.js",
  "connectURL": "ws://localhost:8889/hello",
  "description": "ローカル動作確認用（自身の /hello に接続）"
}
```

受信したメッセージは `script` で指定した JavaScript に `nyanAllParams` として渡され、戻り値がそのまま上流の WebSocket へ送信されます（空文字を返すと返信しません）。

動かし方の例:
- まずはローカルで挙動を見る場合: 上記 `websocket_clients_local` を有効のままにして `./nyan8`（ソースから試す場合は `sh testrun.sh`）を起動します。別ターミナルで手持ちの WebSocket クライアントから `ws://localhost:8889/hello` に送ると、指定した `script` の応答が見えます。

---

## 4   Javascript 上で実行可能な関数と概要

| -  | 関数                                | 概要                                |
|----|-----------------------------------|-----------------------------------|
| 1  | `nyanAllParams`                   | GET/POST/JSON 受信パラメータをまとめたオブジェクト  |
| 2  | `console.log()`                       | ログファイル もしくは コンソールへ出力              |
| 3  | `nyanGetCookie()` / `nyanSetCookie()` | Cookie 操作                         |
| 4  | `nyanGetItem()` / `nyanSetItem()`     | メモリ内 key‑value ストレージ              |
| 5  | `nyanGetAPI()`                        | HTTP GET                          |
| 6  | `nyanJsonAPI()` / `nyanCallAPI()`    | HTTP POST（JSON）                   |
| 7  | `nyanHostExec()`                      | ホスト OS でシェル実行し結果取得                |
| 8  | `nyanGetFile()`                       | サーバー上のファイルを読み込み ファイルが存在しない場合はnull |
| 9  | `nyanGetRemoteIP()`                   | リモートIPを取得                         |
| 10 | `nyanGetUserAgent()`                  | UserAgentを取得                      |
| 11 | `nyanGetRequestHeaders()`             | Header情報を取得できます。                  |
| 12 | **`nyanCallMe()`**                     | 自分自身のAPIを内部実行で呼び出す                      |
| 13 | **`nyanSendMail()`**                  | メール送信（添付可）                        |
| 14 | **`nyanReadFileB64()`**               | ファイル → Base64 変換                  |

### 4‑1 nyanAllParams
GET/POST/JSON 受信パラメータをまとめたオブジェクトです。
このオブジェクトから受信した情報をすべて取得することができます。

```javascript
console.log("nyanAllParams");
```

### 4‑2 console.log
console.logはコンソールもしくはログファイルへ内容が出力されます。

```javascript
console.log("Hello, Nyan8!");
```
### 4-3 nyanGetCookie / nyanSetCookie
cookieの取得と設定ができます。

```javascript
// (1) 取得
let val = nyanGetCookie("my_cookie");
console.log("my_cookie:", val);
// (2) 設定
nyanSetCookie("my_cookie", "hello");
```

### 4‑4 nyanGetItem / nyanSetItem
ローカルストレージへの保存と取得が可能です。

```javascript
// (1) 取得
let val = nyanGetItem("my_key");
console.log("my_key:", val);
// (2) 設定
nyanSetItem("my_key", "hello");
```
### 4‑5 外部APIの呼び出し nyanGetAPI
nyanGetAPI と nyanJsonAPI と nyanCallAPI は外部 API を呼び出すためのユーティリティです。
`nyanGetAPI(url, username, password)` は GET リクエストを送信します。
idとpassはBASIC認証用のIDとパスワードです。必要に応じて設定してください。

```javascript
let res = nyanGetAPI(
  "https://example.com/api",
  "id",
  "pass"
);

let obj = JSON.parse(res);
```

### 4‑6 外部APIの呼び出し nyanJsonAPI / nyanCallAPI
JSON を POST するリクエストができます。`nyanCallAPI()` は `nyanJsonAPI()` のラッパーで、引数と挙動は同じです。
idとpassはBASIC認証用のIDとパスワードです。必要に応じて設定してください。

```javascript
// (1) ヘッダー無し – 必須 4 引数
let res = nyanJsonAPI(
  "https://example.com/api",
  JSON.stringify({ key: "value" }),
  "id",
  "pass"
);
let obj = JSON.parse(res);

// (2) ヘッダー付き – 5 番目の引数にオブジェクト
let headers = {
  "X-Custom-Token": "abcd1234",
  "Content-Language": "ja"
};

// オブジェクトをそのまま渡す
let res2 = nyanJsonAPI(
  "https://example.com/api",
  JSON.stringify({ foo: "bar" }),
  "id",
  "pass",
  headers
);

// nyanCallAPI でも同じように呼び出せる
let res3 = nyanCallAPI(
  "https://example.com/api",
  JSON.stringify({ foo: "bar" }),
  "id",
  "pass",
  headers
);
```

> **ポイント**  
> 5 番目の `headers` 引数は **オブジェクト**（`{key: "val"}`）のみ受け付けます。  
> JSON文字列を渡したい場合は、上位側で文字列をオブジェクト化してください。

---



### 4-7 ホストコマンド実行 nyanHostExec
ホスト OS のシェルコマンドを実行し、結果を JSON 形式で取得します。

```javascript
let result = nyanHostExec("ls -l");
console.log(result);
```

#### console.log() の出力例： 
stdout にコマンドの標準出力、 stderr に標準エラー出力が入ります。

コマンドの実行に失敗した場合や終了コードが 0 以外の場合は、JavaScript 側で例外が投げられます。
正常に処理が完了した場合、`success` が `true`、`exit_code` が `0` になります。
```json
{
  "success": true,
  "stdout": "total 8\ndrwxr-xr-x  4 user  staff  128 Aug 15 12:00 .\ndrwxr-xr-x 10 user  staff  320 Aug 15 11:59 ..\n-rw-r--r--  1 user  staff   0 Aug 15 12:00 file1.txt\n-rw-r--r--  1 user  staff   0 Aug 15 12:00 file2.txt\n",
  "stderr": "",
  "exit_code": 0
}
```

### 4‑8 nyanGetFile
サーバー上のファイルを読み込み、内容を文字列として取得します。

実行中の Nyan8 バイナリのディレクトリからの相対パスでファイルを指定します。
ファイルが存在しない場合やディレクトリを指定した場合は `null` が返却されます。権限エラーなどその他の失敗時は JavaScript 側で例外が投げられます。

```javascript
let content = nyanGetFile("./data.txt");
if (content !== null) {
  console.log("File content:", content);
} else {
  console.log("File not found.");
}
```

## 4‑9 nyanGetRemoteIP
リクエスト元のリモートIPアドレスを取得します。

```javascript  
let ip = nyanGetRemoteIP();
console.log("Remote IP:", ip);
```
### 4‑10 nyanGetUserAgent
リクエスト元のUserAgentを取得します。

```javascript
let ua = nyanGetUserAgent();
console.log("UserAgent:", ua);
```
### 4‑11 nyanGetRequestHeaders
リクエストヘッダーをオブジェクト形式で取得します。

```javascript
let headers = nyanGetRequestHeaders();
console.log("Request Headers:", headers);
```

### 4‑12 メール送信 nyanSendMail
強力なメール送信機能を備えています。CC/BCC、添付ファイルもサポートしています。

```javascript
let result = nyanSendMail({
  to: ["sample@example.com"],
  subject: "Test Email from Nyan8",
  body: "This is a test email sent from Nyan8.",
  attachments: [
    nyanSendMailAttachment("./mail-body.txt")
  ]
});
console.log(result);
```

#### オブジェクト形式のキー
| キー         | 型          | 説明                                      |
|--------------|-------------|-----------------------------------------|
| to           | Array       | 宛先メールアドレスの配列                         |
| subject      | String      | メール件名                                   |
| body         | String      | メール本文                                   |
| attachments  | Array       | 添付ファイルの配列。各要素は `path` または `dataBase64` を持つ。|
| cc           | Array       | CC 宛先メールアドレスの配列（省略可）               |
| bcc          | Array       | BCC 宛先メールアドレスの配列（省略可）              |
| html         | Boolean     | true で HTML メールとして送信（省略可、デフォルト false） |

#### 旧シグネチャ
`nyanSendMail(to, subject, body, html, cc, bcc)` も利用できます。こちらは添付ファイルを受け取りません。

#### 戻り値
成功時：`true`
失敗時：JavaScript 側で例外（`Error` 相当）が投げられます。

### 4‑13 添付ヘルパー nyanSendMailAttachment
ファイルパスを渡すと、`nyanSendMail` 用の添付オブジェクトを返します。

```javascript
let attachment = nyanSendMailAttachment("./mail-body.txt");
let result = nyanSendMail(["sample@example.com"], "Subject", "Body", [attachment]);
console.log(result);
```

### 4‑14 ファイル→Base64 変換 nyanReadFileB64
指定したファイルを Base64 文字列に変換します。

```javascript
try {
  let base64Str = nyanReadFileB64("./image.png");
  console.log("Base64 String:", base64Str);
} catch (e) {
  console.log("read error:", String(e));
}
```

### 4‑15 nyanCallMe
`nyanCallMe` は同一 Nyan8 プロセス内で、自身のAPIを直接実行します。  
既存の `nyanGetAPI` / `nyanJsonAPI` / `nyanCallAPI` と異なり、HTTP/HTTPS 経由を使わないため、証明書や `port` に依存しません。
`nyanCallMe` は呼び出した API の結果をそのまま返すため、通常は `JSON.parse` は不要です（必要なら型安全のために `typeof` チェックしてください）。

```javascript
let result = nyanCallMe({ api: "hello2" });
console.log(result); // { success: true, status: 200, data: ...}
```

#### 挙動
- `api` でAPI名を指定します。指定が無い場合は `hello2` が呼ばれます。
- 引数オブジェクトは、そのまま呼び出し先 API の `nyanAllParams` に渡されます。
- 実行に失敗すると JavaScript 側で例外が投げられます。

#### よくある使い方
自分自身の API から別 API を呼び出して結果をマージする用途です。

```javascript
function main() {
  let child = nyanCallMe({ api: "hello2", name: "Nyan" });
  return JSON.stringify({
    success: true,
    status: 200,
    data: {
      message: "wrapper",
      child: child
    }
  });
}
main();
```

### 5  API エンドポイント
#### `GET /nyan`
サーバの基本情報と利用可能な API 一覧を取得します。
**レスポンス例**
```json
{
  "nyan": {
    "name": "Nyan8 Server",
    "profile": "dev",
    "version": "vX.Y.Z"
  },
  "apis": {
    "add": {
      "description": "2 に足す API",
      "push": "add_push",
      "type": "api"
    },
    "add_push": {
      "description": "add の結果を push 配信",
      "type": "api"
    }
  }
}
```
#### `GET /nyan/{API名}`
指定した API の詳細情報（説明、受け入れ可能パラメータ、出力カラム）を取得します。
**レスポンス例**
```json
{
  "api": "add",
  "type": "api",
  "description": "2 に足す API",
  "nyanAcceptedParams": { "num": "数値" },
  "nyanOutputColumns": ["result"]
}
```
---
## 6  レスポンス形式
### 成功時

```json
{
  "success": true,
  "status": 200,
  "result": [...]
}
```
### エラー時

```json
{
  "success": false,
  "status": 400,
  "error": "Error message"
}
```

# 7 MCPサーバ対応
Nyan8はMCPサーバに対応しています。
エンドポイント /nyan-toolbox にアクセスすることでMCPサーバの機能を利用できます。
chatGPTでの利用について、sslの設定をすれば利用可能な状態となっています。認証の設定を認証なしとして、コネクター登録を行なってください。

認証の設定 OAuth での利用については 今後対応の予定です。


---   
## 8 ライセンス
[MIT License](LICENSE.md)
