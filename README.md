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
| **ファイル→Base64** | `nyanFileToBase64` でファイルを Base64 文字列へ一発変換 |
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

---

## 4   Javascript 上で実行可能な関数と概要

| -  | 関数                                    | 概要                                |
|----|---------------------------------------|-----------------------------------|
| 1  | `nyanAllParams`                       | GET/POST/JSON 受信パラメータをまとめたオブジェクト  |
| 2  | `console.log()`                       | ログファイル もしくは コンソールへ出力              |
| 3  | `nyanGetCookie()` / `nyanSetCookie()` | Cookie 操作                         |
| 4  | `nyanGetItem()` / `nyanSetItem()`     | メモリ内 key‑value ストレージ              |
| 5  | `nyanGetAPI()` / `nyanPostAPI()`      | HTTP GET  / HTTP POST             |
| 6  | `nyanJsonAPI()`                       | HTTP POST（JSON）                   |
| 7  | `nyanHostExec()`                      | ホスト OS でシェル実行し結果取得                |
| 8  | `nyanGetFile()`                       | サーバー上のファイルを読み込み ファイルが存在しない場合はnull |
| 9  | `nyanGetRemoteIP()`                   | リモートIPを取得                         |
| 10 | `nyanGetUserAgent()`                  | UserAgentを取得                      |
| 11 | `nyanGetRequestHeaders()`             | Header情報を取得できます。                  |
| 12 | **`nyanSendMail()`**                  | メール送信（添付可）                        |
| 13 | **`nyanFileToBase64()`**              | ファイル → Base64 変換                  |

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
nyanSetCookie("my_cookie", "hello", 3600); // 1時間有効
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
nyanGetAPI とnyanPostAPI と nyanJsonAPI は外部 API を呼び出すためのユーティリティです。
idとpassはBASIC認証用のIDとパスワードです。必要に応じて設定してください。

```javascript
// (1) ヘッダー無しのリクエストの場合
let res = nyanGetApi(
  "https://example.com/api", 
        {key: "value"},
  "id",
  "pass"
);

let obj = JSON.parse(res);

// (2) ヘッダー付きのリクエストの場合
let res = nyanGetApi(
  "https://example.com/api",
        {key: "value"},
  "id",
  "pass",
  {
    "X-Custom-Token": "abcd1234",
    "Content-Language": "ja"
  }
);

let obj = JSON.parse(res);
```

nyanPostAPI も同様に使えます。
```javascript
// (1) ヘッダー無しのリクエストの場合
let res = nyanPostApi(
        "https://example.com/",
        {key: "value"},
        "id",
        "pass"
);

let obj = JSON.parse(res);

// (2) ヘッダー付きのリクエストの場合
let res = nyanPostApi(
        "https://example.com/api",
        {key: "value"},
        "id",
        "pass",
        {
           "X-Custom-Token": "abcd1234",
           "Content-Language": "ja"
        }
);

let obj = JSON.parse(res);

````

### 4‑6 外部APIの呼び出し nyanJsonAPI
JSONをPOSTするリクエストができます。
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

// (2) ヘッダー付き – 5 番目の引数にオブジェクト or JSON 文字列
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

// JSON 文字列で渡すことも可能
let res3 = nyanJsonAPI(
  "https://example.com/api",
  '{"foo":"bar"}',
  "id",
  "pass",
  JSON.stringify(headers)
);
```

> **ポイント**  
> 5 番目の `headers` 引数は **オブジェクト**（`{key: "val"}`）と **JSON 文字列**（`'{"key":"val"}'`）の両方を受け付けます。  
> ライブラリ側で自動判定・変換されるので、好きな書き方を選んでください。

---



### 4-7 ホストコマンド実行 nyanHostExec
ホスト OS のシェルコマンドを実行し、結果を JSON 形式で取得します。

```javascript
let result = nyanHostExec("ls -l");
console.log(result);
```

#### console.log() の出力例： 
stdout にコマンドの標準出力、 stderr に標準エラー出力が入ります。

エラーが発生した場合はexitCode が 0 以外になります。 
正常に処理が完了した場合、exitCode が 0 になります。 
```json
{
  "stdout": "total 8\ndrwxr-xr-x  4 user  staff  128 Aug 15 12:00 .\ndrwxr-xr-x 10 user  staff  320 Aug 15 11:59 ..\n-rw-r--r--  1 user  staff   0 Aug 15 12:00 file1.txt\n-rw-r--r--  1 user  staff   0 Aug 15 12:00 file2.txt\n",
  "stderr": "",
  "exitCode": 0
}
```

### 4‑8 nyanGetFile
サーバー上のファイルを読み込み、内容を文字列として取得します。

実行するNyan8バイナリーからの相対パスでも絶対パスでもファイルを指定することができます。
ファイルが存在しない場合 nullが返却されます。

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
let to = ["sample@exsample.com"];
let subject = "Test Email from Nyan8";
let body = "This is a test email sent from Nyan8.";
let attachments = [
  {
    filename: "test.txt",
    content: "Hello, this is a test file."
  }];
let result = nyanSendMail(to, subject, body, attachments);
console.log(result);
```

#### 引数
| 引数         | 型          | 説明                                      |
|--------------|-------------|-----------------------------------------|
| to           | Array       | 宛先メールアドレスの配列                         |
| subject      | String      | メール件名                                   |
| body         | String      | メール本文                                   |
| attachments  | Array       | 添付ファイルの配列。各要素はオブジェクトで、`filename` と `content` を含む。 |
| cc           | Array       | CC 宛先メールアドレスの配列（省略可）               |
| bcc          | Array       | BCC 宛先メールアドレスの配列（省略可）              |
| isHtml       | Boolean     | true で HTML メールとして送信（省略可、デフォルト false） |
| fromEmail    | String      | 送信元メールアドレス（省略可、config.json の設定が優先）   |
| fromName     | String      | 送信元名（省略可、config.json の設定が優先）         |
| replyTo      | String      | 返信先メールアドレス（省略可）                     |
| replyToName  | String      | 返信先名（省略可）                           |
   
#### 戻り値
成功時：`{ success: true, message: "Email sent successfully." }`
失敗時：`{ success: false, message: "Error message." }`

### 4‑13 ファイル→Base64 変換 nyanFileToBase64
指定したファイルを Base64 文字列に変換します。

```javascript
let base64Str = nyanFileToBase64("./image.png");
   
if (base64Str !== null) {
  console.log("Base64 String:", base64Str);
} else {
  console.log("File not found.");
}
```

### 4‑14 ファイル保存 nyanSaveFile  
指定した Base64 文字列をデコードしてファイルに保存します。

```javascript
let b64 = "SGVsbG8sIFdvcmxkIQ=="; // "Hello, World!" の Base64
nyanSaveFile(b64, "./storage/hello.txt");
```

### 5  API エンドポイント
#### `GET /nyan`
サーバの基本情報と利用可能な API 一覧を取得します。
**レスポンス例**
```json
{
  "name": "Nyan8 Server",
  "profile": "dev",
  "version": "1.0.0",
  "apis": {
    "add": { "description": "2 に足す API" },
    "add_push": { "description": "add の結果を push 配信" }
  }
}
```
#### `GET /nyan/{API名}`
指定した API の詳細情報（説明、受け入れ可能パラメータ、出力カラム）を取得します。
**レスポンス例**
```json
{
  "api": "add",
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

---   
## 7  ライセンス
[MIT License](LICENSE.md)


