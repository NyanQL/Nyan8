# Nyan8


**Nyan8（にゃんぱち）** は Go 言語で実装されたサーバーサイド JavaScript 実行環境です。
JavaScript エンジンに [**Goja**](https://github.com/dop251/goja) を採用し、ECMAScript 5.1 準拠のスクリプトを安全かつ高速に実行できます。
javascriptを書くだけで 手軽にAPIサービスを作れます。

---

## 1  特徴

| 機能 | 概要 |
|------|------|
| **JavaScript API** | HTTP/HTTPS 経由で JS ファイルを呼び出し、JSON を返却 |
| **公開ファイル配信** | `type: "public"` で静的ファイルを API 定義から配信 |
| **入出力チェック** | `paramCheck` / `outCheck` で実行前・出力前の検査を追加 |
| **WebSocket Push** | `api.json` の `push` 設定だけで双方向通信を実現 |
| **定期実行ジョブ** | `type: "schedule"` で cron 形式の JavaScript ジョブを登録し、変更も動的反映 |
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

### 2-1  設定ファイルのパス指定

Nyan8 は起動時に `api.json` と `config.json` の読み込みパスを指定できます。
指定がない場合は、従来どおり実行ファイルと同じディレクトリにある `api.json` / `config.json` を読み込みます。

```bash
./nyan8
./nyan8 --api /path/to/api.json --config /path/to/config.json
NYAN_API_PATH=/path/to/api.json NYAN_CONFIG_PATH=/path/to/config.json ./nyan8
```

`api.json` 内の `script` / `path` / `paramCheck` / `outCheck` の相対パスは、`api.json` が置かれているディレクトリから解決されます。
`config.json` 内の `certPath` / `keyPath` / `javascript_include` / `log.Filename` の相対パスは、`config.json` が置かれているディレクトリから解決されます。

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
  "APIHotReload": {
    "Enabled": true,                // api.json の変更を動的反映
    "Interval": "1s"               // Go duration形式の確認間隔
  },
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

#### `api.json` のホットリロード

`api.json` は既定で1秒ごとに確認され、内容が変化した場合だけ再解析されます。通常API、public API、JSON-RPC、MCP、schedule、ws_clientの追加・変更・削除が再起動なしで反映されます。

不正なJSONや、不正なschedule／ws_client設定は採用されず、直前の正常な定義で稼働を継続します。同じ不正内容はファイルが再度変化するまで繰り返し解析されません。

`Interval` は `500ms`、`1s`、`1m`、`24h` などのGo duration形式です。空の場合は `1s` になります。0以下または解析できない値は起動エラーです。ホットリロードを無効にする場合は `Enabled` を `false` にします。`APIHotReload` 全体を省略した場合は有効、1秒間隔です。

schedule変更時は同名ジョブを二重起動せず、実行中のスクリプトを完了してから最新設定へ移行します。ws_clientはscript／descriptionだけの変更では接続を維持し、`connectURL` の変更時だけ旧接続を閉じて新しい接続先へ切り替えます。

### 3‑2  `api.json`

```jsonc
{
  "add": {
    "script": "apis/add.js",        // 実行する JS
    "description": "2 に足す API",
    "push": "add_push",             // 省略可
    "paramCheck": "apis/check.js",  // 実行前チェック（省略可）
    "outCheck": "apis/out_check.js" // 出力前チェック（省略可）
  },
  "add_push": {
    "script": "apis/add_push.js",
    "description": "add の結果を push 配信"
  },
  "assets": {
    "type": "public",
    "path": "./public",
    "description": "公開ファイル配信"
  },
  "schedule_debug_every_minute": {
    "type": "schedule",
    "script": "./javascript/schedule_debug.js",
    "trigger": {
      "type": "cron",
      "value": "* * * * *"
    },
    "description": "1分ごとにログへ実行時刻を出力"
  }
}
```

* `/add` に HTTP アクセス → `apis/add.js` が実行
* WebSocket 接続 `/add_push` を張っておけば、`add` 完了時に push が届きます
* `/assets/app.js` に HTTP アクセス → `./public/app.js` が配信されます
* `schedule_debug_every_minute` は起動時にジョブとして登録され、HTTP API としては公開されません

### type

- `type` を省略した場合は従来通り HTTP/WS サーバーの API (`"type": "api"`) として動作します。
- `type: "public"` を指定すると、`path` 配下のファイルを公開エンドポイントとして配信します。
- `type: "ws_client"` を指定すると Nyan8 自身が WebSocket クライアントになり、常時接続します。
- `type: "schedule"` を指定すると定期実行ジョブとして登録します。
- `connectURL` が `env:XXXX` の場合、環境変数 `XXXX` で接続 URL を解決します。

#### 通常 API

```jsonc
"hello": {
  "type": "api",
  "script": "./javascript/hello.js",
  "description": "hello API"
}
```

`type` を省略しても `"api"` として扱われます。HTTP API、WebSocket 接続、JSON-RPC、MCP tools/call の対象になります。

#### 公開ファイル配信

```jsonc
"assets": {
  "type": "public",
  "path": "./public",
  "paramCheck": "./javascript/check_login.js",
  "outCheck": "./javascript/check_output.js",
  "description": "静的ファイル配信"
}
```

この例では `/assets/test.txt` が `./public/test.txt` に対応します。リクエストされた相対パスは `nyanAllParams.nyan_public_path`、エンドポイント名は `nyanAllParams.nyan_public_endpoint` で参照できます。

`type: "public"` は JSON-RPC や MCP tools/list には公開されません。認可が必要なファイルを配信する場合は `paramCheck` を指定してください。

#### WebSocket クライアント

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

#### 定期実行ジョブ

```jsonc
"daily_job": {
  "type": "schedule",
  "script": "./javascript/daily_job.js",
  "trigger": {
    "type": "cron",
    "value": "0 10 * * *"
  },
  "description": "毎日10:00に実行"
}
```

`type: "schedule"` は指定時刻になると `script` の JavaScript を実行します。この定義は HTTP API、WebSocket 接続、JSON-RPC、MCP tools/list には公開されません。

`trigger.type` は現在 `cron` のみ対応しています。cron は5フィールド形式です。

```text
分 時 日 月 曜日
```

各フィールドでは `*`、数値、カンマ区切り、範囲、ステップ指定を使えます。

指定例:

| cron | 実行タイミング |
|------|----------------|
| `* * * * *` | 1分ごと |
| `*/10 * * * *` | 10分ごと |
| `0 10 * * *` | 毎日10:00 |
| `15,45 * * * *` | 毎時15分と45分 |
| `0 9-18 * * *` | 9時から18時まで毎時0分 |

秒単位の指定には対応していません。最短の実行間隔は1分です。`*/10` は起動時刻から10分ごとではなく、時計の分が `00, 10, 20, 30, 40, 50` のタイミングで実行されます。

schedule の `script` では通常の API と同じように `nyanAllParams`、`nyanCallMe()`、`nyanGetFile()` などを使えます。加えて、次の値が `nyanAllParams` に入ります。

| 名前 | 内容 |
|------|------|
| `nyanAllParams.nyan_job_name` | `api.json` 上のジョブ名 |
| `nyanAllParams.nyan_schedule_trigger_type` | 現在は `cron` |
| `nyanAllParams.nyan_schedule_trigger` | cron 式 |
| `nyanAllParams.nyan_schedule_time` | 実行予定時刻 |
| `nyanAllParams.nyan_schedule_description` | `api.json` に書いた説明 |

schedule は HTTP リクエストから実行されないため、`nyanGetRemoteIP()`、`nyanGetUserAgent()`、`nyanGetRequestHeaders()` などリクエスト情報に依存する関数は空の値を返します。`javascript_include` に設定した共通 JavaScript は、schedule の `script` 実行時にも毎回読み込まれます。

schedule 定義自体には `paramCheck` / `outCheck` / `push` は適用されません。必要な前処理や通知は、schedule の `script` 内で直接実装するか、`nyanCallMe()` で通常 API を呼び出してください。

このリポジトリには動作確認用として [api.json](./api.json) の `schedule_debug_every_minute` と [javascript/schedule_debug.js](./javascript/schedule_debug.js) を用意しています。起動すると1分ごとにログへ実行時刻が出力されます。

### paramCheck / outCheck

`paramCheck` は API の本体 `script` 実行前、または `type: "public"` のファイル配信前に実行されます。`outCheck` は本体実行後、または公開ファイル送信前に実行されます。

互換性のため、`paramCheck` は `paramcheck` / `check`、`outCheck` は `outcheck` でも指定できます。README では `paramCheck` / `outCheck` を推奨表記とします。

```jsonc
"secure_add": {
  "script": "./javascript/add.js",
  "paramCheck": "./javascript/check_request.js",
  "outCheck": "./javascript/check_response.js",
  "description": "入力と出力を検査する API"
}
```

#### paramCheck の戻り値

`paramCheck` / `outCheck` の JavaScript は、次の形式のオブジェクトまたは JSON 文字列を返してください。

```javascript
if (nyanAllParams.token === "secret") {
  ({ success: true, status: 200, result: { message: "ok" } });
} else {
  ({ success: false, status: 401, result: { message: "unauthorized" } });
}
```

```json
{
  "success": true,
  "status": 200,
  "result": {}
}
```

`success: true` かつ `status: 200` の場合だけ次の処理へ進みます。それ以外の場合は、本体 `script` やファイル配信を実行せず、チェック結果を JSON として返します。HTTP ステータスも `status` の値になります。

#### checkOnly

`nyan_mode=checkOnly` を指定すると、`paramCheck` だけを実行し、本体 `script` やファイル配信へ進みません。

```bash
curl "http://localhost:8080/secure_add?token=secret&nyan_mode=checkOnly"
```

`paramCheck` 未設定の API に `nyan_mode=checkOnly` を指定した場合は、次のレスポンスを返します。

```json
{
  "success": true,
  "status": 200,
  "result": null
}
```

#### outCheck の入力

`outCheck` では、本体の出力内容を `nyanAllParams.nyan_output` で参照できます。

```javascript
if (nyanAllParams.nyan_output.status === 200 &&
    nyanAllParams.nyan_output.body.indexOf("expected") >= 0) {
  ({ success: true, status: 200, result: null });
} else {
  ({ success: false, status: 409, result: { message: "output mismatch" } });
}
```

`nyan_output` には次の値が入ります。

| キー | 説明 |
|------|------|
| `status` | 本体レスポンスの HTTP ステータス |
| `contentType` | 本体レスポンスの Content-Type |
| `headers` | 本体レスポンスのヘッダー |
| `body` | 本体レスポンス本文 |
| `bodyBase64` | 本体レスポンス本文の Base64 |
| `bodyLength` | 本体レスポンス本文のバイト長 |
| `bodyLengthBytes` | `bodyLength` と同じ互換用フィールド |

互換用に `nyan_output_status`, `nyan_output_content_type`, `nyan_output_body`, `nyan_output_body_base64` も利用できます。

通常 API、`type: "public"`、JSON-RPC、MCP tools/call の呼び出しで同じチェック指定を利用できます。ただし `type: "public"` と `type: "ws_client"` は JSON-RPC / MCP tool としては呼び出せません。

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
