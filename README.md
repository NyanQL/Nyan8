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
| **MCP / OAuth** | `api.json`で設定したStreamable HTTP MCPをOAuth 2.0 Authorization Code + PKCEで保護 |
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

各 `api.json` 内の `script` / `path` / `paramCheck` / `outCheck` の相対パスは、その定義を書いた `api.json` が置かれているディレクトリから解決されます。
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
  "bindAddress": "127.0.0.1",     // 待受IP。省略時は全インターフェース
  "certPath": "cert.pem",         // SSL 証明書（未使用時は空）
  "keyPath":  "key.pem",          // 秘密鍵（未使用時は空）
  "javascript_include": [           // 共通 JS をロード（任意複数可）
    "libs/common.js"
  ],
  "APIHotReload": {
    "Enabled": true,                // api.json の変更を動的反映
    "Interval": "1s"               // Go duration形式の確認間隔
  },
  "websocket": {
    "allowRoot": true,              // ルートのWebSocket upgradeを許可
    "maxConnections": 128           // WebSocket接続数の上限（1〜4096）
  },
  "proxyProtocol": {
    "enabled": false,               // PROXY protocol v2を使用する場合だけtrue
    "trustedCIDRs": []              // trueの場合に接続を許可するhost CIDR
  },
  "oauth_admin": {
    "username": "operator",        // OAuth利用者登録用の管理ユーザー
    "password": "change-me"        // 実運用では安全な方法で管理する
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

ルートの `api.json` と、そこから `include` されたすべての `api.json` は既定で1秒ごとに確認され、内容が変化した場合だけルートから再解析されます。通常API、public API、JSON-RPC、MCP、schedule、ws_clientの追加・変更・削除が再起動なしで反映されます。

不正なJSON、存在しないinclude先、不正なschedule／ws_client設定などは採用されず、直前の正常な定義で稼働を継続します。失敗した候補内で新しく見つかったinclude先も監視されるため、ファイルの作成や修正だけで自動的に再試行されます。同じファイル状態とエラーは繰り返しログ出力されません。

`Interval` は `500ms`、`1s`、`1m`、`24h` などのGo duration形式です。空の場合は `1s` になります。0以下または解析できない値は起動エラーです。ホットリロードを無効にする場合は `Enabled` を `false` にします。`APIHotReload` 全体を省略した場合は有効、1秒間隔です。

schedule変更時は同名ジョブを二重起動せず、実行中のスクリプトを完了してから最新設定へ移行します。ws_clientはscript／descriptionだけの変更では接続を維持し、`connectURL` の変更時だけ旧接続を閉じて新しい接続先へ切り替えます。

`script`、`paramCheck`、`outCheck`、publicの配信ファイル自体は監視対象ではありません。これらは従来どおり実行時またはリクエスト時に読み込まれます。`paramCheck` / `outCheck` 内の公開用スキーマも `/nyan/{API名}` のリクエストごとに読み直されるため、スキーマだけを変更した場合はNyan8の再起動や `api.json` の更新は不要です。

#### `api.json` の分割と多段include

API定義は `type: "include"` で複数の `api.json` に分割できます。includeした定義名がマウント名となり、子ファイル内のAPI名へ `/` 区切りで付加されます。includeの深さに制限はありません。

ルートの `api.json`:

```jsonc
{
  "health": {
    "script": "./javascript/health.js"
  },
  "sub": {
    "type": "include",
    "path": "./sub/api.json"
  }
}
```

`sub/api.json`:

```jsonc
{
  "getItem": {
    "script": "./javascript/get_item.js"
  },
  "admin": {
    "type": "include",
    "path": "./admin/api.json"
  }
}
```

`sub/admin/api.json`:

```jsonc
{
  "getUser": {
    "script": "./javascript/get_user.js"
  }
}
```

この構成では `health`、`sub/getItem`、`sub/admin/getUser` として公開されます。展開後の名前はHTTP、WebSocket、JSON-RPC、MCP、push、schedule、ws_clientで共通して使用されます。include定義そのものはAPIとして公開されません。

includeには次の制約があります。

- include定義に指定できるフィールドは `type` と `path` だけです。
- マウント名は空文字、`.`、`..`、前後に空白がある名前、`/` を含む名前にはできません。
- 同じ階層でマウント名と衝突するAPI名は定義できません。
- 循環参照と、展開後に重複するAPI名はエラーになります。
- includeされない既存API名に `/` を含める従来の書き方は、マウント名と衝突しない限り使用できます。

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

`type` を省略しても `"api"` として扱われます。HTTP API、WebSocket接続、JSON-RPCの対象です。MCP Toolとして公開するには、`type: "mcp"`定義の`tools`からこのAPIを明示的に参照します。

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

通常API、`type: "public"`、JSON-RPCの呼び出しで同じチェック指定を利用できます。MCP `tools/call`では`paramCheck` / `outCheck`を実行せず、MCP Toolに指定した`inputSchema` / `outputSchema`で検証します。`type: "public"`と`type: "ws_client"`はJSON-RPC / MCP Toolとしては呼び出せません。

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

### 4‑9 nyanGetRemoteIP
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
let result = nyanSendMail({
  to: ["sample@example.com"],
  subject: "Subject",
  body: "Body",
  attachments: [attachment]
});
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

## 5  API エンドポイント

### `GET /nyan`

サーバの基本情報と利用可能な通常API（`type: "api"`）の一覧を取得します。レスポンスはNyanQL・NyanPUIと共通のフラット形式です。`name`、`profile`、`version`をトップレベルに置き、`nyan`ラッパーは使用しません。

`public`、`schedule`、`ws_client`、`mcp`、include定義は一覧に含まれません。多段includeで展開された通常APIは、`sub/items/get`のような完全API名で表示されます。OAuth endpointも独立した通常APIとして定義されている場合は一覧に含まれます。

**レスポンス例**

```json
{
  "name": "Nyan8 Server",
  "profile": "dev",
  "version": "vX.Y.Z",
  "apis": {
    "add": {
      "description": "2 に足す API",
      "push": "add_push"
    },
    "add_push": {
      "description": "add の結果を push 配信"
    }
  }
}
```

`name`、`profile`、`version`は`config.json`の値です。バイナリへ埋め込まれたversionではなく、NyanQL・NyanPUIと同様に設定上のversionを返します。

`apis`には通常APIの`description`と、設定されている場合だけ`push`を掲載します。`script`、`type`、`title`、schema、認証設定などの内部項目は一覧へ掲載しません。入出力schemaなどの詳細は`GET /nyan/{API名}`で取得します。

### `GET /nyan/{API名}`

指定した通常APIの詳細情報と入出力スキーマを取得します。多段includeされたAPIも `/nyan/sub/items/get` のように完全名を指定できます。

**レスポンス例**

```json
{
  "api": "add",
  "type": "api",
  "description": "2 に足す API",
  "nyanAcceptedParams": { "num": "数値" },
  "inputSchema": {
    "type": "object",
    "properties": {
      "num": {
        "type": "string",
        "examples": ["数値"]
      }
    },
    "additionalProperties": true
  },
  "outputSchema": {},
  "schemaSource": {
    "input": "scriptLegacy",
    "output": "unknown"
  }
}
```

`schemaSource.input` は `paramCheck`、`scriptLegacy`、`unknown` のいずれか、`schemaSource.output` は `outCheck`、`unknown` のいずれかです。旧形式の `nyanOutputColumns` は廃止され、JavaScript内に宣言してもAPI詳細には表示されません。

### 入出力スキーマの公開

入力スキーマは `paramCheck` ファイルのトップレベルに `nyanInputSchema`、出力スキーマは `outCheck` ファイルのトップレベルに `nyanOutputSchema` として定義します。JSON Schema Draft 2020-12形式のオブジェクトを想定していますが、`$schema` は任意です。記載された内容をそのまま公開し、省略された項目をNyan8が補いません。

```javascript
const nyanInputSchema = {
  $schema: "https://json-schema.org/draft/2020-12/schema",
  type: "object",
  properties: {
    id: {
      type: "integer",
      minimum: 1,
      description: "取得するID"
    }
  },
  required: ["id"],
  additionalProperties: false
};

if (typeof nyanAllParams.id !== "number") {
  ({success: false, status: 400, result: {message: "idが必要です"}});
} else {
  ({success: true, status: 200, result: null});
}
```

```javascript
const nyanOutputSchema = {
  type: "object",
  properties: {
    status: {const: 200},
    payload: {
      type: "object",
      properties: {name: {type: "string"}},
      required: ["name"]
    }
  },
  required: ["status", "payload"],
  additionalProperties: false
};

const output = JSON.parse(nyanAllParams.nyan_output.body);
({success: output.status === 200, status: output.status === 200 ? 200 : 500, result: null});
```

公開スキーマと実行時の値は別のものです。Nyan8は公開したJSON Schemaによるリクエスト・レスポンスの自動検証を行いません。実際の入力・出力チェックは従来どおり `paramCheck` / `outCheck` のJavaScriptで行ってください。

また、Nyan8は `success`、`status`、`result` を明示出力スキーマへ自動追加しません。実際のAPIレスポンスも本体JavaScriptが生成する必要があり、`status` がないレスポンスは従来どおり実行エラーになります。

#### スキーマの取得優先順位

入力スキーマは次の順で決まります。

1. `paramCheck` 内の `nyanInputSchema`
2. 本体 `script` 内の旧形式 `nyanAcceptedParams` から生成
3. 型不明の空スキーマ `{}`

出力スキーマは次の順で決まります。

1. `outCheck` 内の `nyanOutputSchema`
2. 型不明の空スキーマ `{}`

明示入力スキーマがある場合、API詳細では `inputSchema` を正として `nyanAcceptedParams` を省略します。明示入力スキーマがなく、`nyanAcceptedParams` を静的に取得できる場合は、後方互換性のため `nyanAcceptedParams` と、それから生成した `inputSchema` の両方を公開します。legacyスキーマでは値から `string`、`boolean`、`integer`、`number`、`object`、`array` を推測しますが、必須項目は推測しません。

#### 静的スキーマ定義の制約

スキーマはJavaScriptを実行せず、構文木から静的に読み取ります。使用できる値はオブジェクト、配列、文字列、数値、真偽値、`null` と、それらのネストです。スキーマ定数はトップレベルで `const` 宣言してください。

関数呼び出し、識別子参照、spread、computed propertyなどを含む動的な定義は取得できません。

```javascript
// 対応していません
const nyanInputSchema = createSchema();
const nyanOutputSchema = {...commonSchema};
```

動的または不正な明示スキーマがあっても `api.json` の読み込みやホットリロードは妨げません。対象のAPI詳細を取得した時点でスキーマ解決エラーを返します。ファイルを修正すれば、次の詳細取得から反映されます。

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

## 7 MCPサーバ対応

Nyan8は、stateless Streamable HTTPとstdioのMCPサーバとして動作します。MCPも通常APIやpublic APIと同じ名前解決規則を使用します。

```json
{
  "mcp_server": {
    "type": "mcp",
    "transport": "streamable_http",
    "allowedOrigins": ["https://chatgpt.com"],
    "tools": ["mcp_example"]
  }
}
```

この定義のMCP endpointは`/mcp_server`です。`/?api=mcp_server`でも同じ定義を呼び出せますが、MCP resourceのcanonical URLは常に`/mcp_server`です。`path`や絶対URLの`resource`は設定しません。

> **API名の注意:** `nyan`、`nyan-rpc`、`nyan-*`はNyan8の組み込み機能用に予約されているため、`api.json`のAPI名には使用できません。旧MCP名`nyan-toolbox`と`/nyan-toolbox` endpointに対する後方互換はありません。必要なMCPには`mcp`や`mcp_server`など別のAPI名を指定し、そのAPI名に対応するURLを使用してください。

公開originは検証済みのrequest scheme、Host、portからrequest単位で構成します。したがって、domainを`api.json`へ固定する必要はありません。`allowedOrigins`は公開URLの設定ではなく、browserからのcross-origin requestを許可するsecurity policyです。

対応するMCP protocol versionは`2025-11-25`と`2025-06-18`、methodは`initialize`、`ping`、`tools/list`、`tools/call`です。JSON-RPC batch、MCP session、Resources、Promptsには対応していません。

`transport`は必須で、`streamable_http`または`stdio`のどちらか1つを指定します。1つのMCP定義が両方のtransportを持つことはありません。同じToolをHTTPとstdioの両方で利用する場合は、transportの異なるMCP定義を2つ作り、同じ通常APIを`tools`から参照します。旧形式の`transports`、省略、未対応値は設定エラーです。

### 7-1 実装の責務

ここでいう「責務」は、MCP/OAuthを構成する各処理について、どの要素が実装・検証を担当するかという役割分担です。ファイルをGit管理するかどうかを示すものではありません。

| 場所 | 責務 |
|------|------|
| `main.go` | API名解決、MCP/OAuthのrouting、URL生成、metadata、JSON-RPC、CORS、rate limit、同時実行制御、JSON Schema検証、Tool実行、暗号乱数・Argon2id・OAuth stateの安全なファイル操作 |
| OAuth用の独立APIが指定するJavaScript | DCR、ログイン、認可要求、authorization code、access token、refresh token、scopeとresourceの検証などOAuthポリシー |
| 通常APIのJavaScript | MCP Toolが実際に返すデータや実行する処理 |
| `api.json` | API名、許可Origin、公開する通常API名、OAuth API間の参照、通常APIのschemaとMCP固有metadata |

MCPやOAuthのdomainは`main.go`にも`api.json`にも固定されません。OAuth JavaScriptには、requestから導出したissuer、resource、endpoint pathなどが`nyanAllParams`で渡されます。

OAuth JavaScriptのファイル名は固定されていません。各OAuth APIの通常の`script`で任意のJavaScriptを指定できます。同じファイルを複数APIから使うことも、APIごとに分けることもできます。Nyan8はGit追跡状態を実行条件にしません。

### 7-2 `api.json`設定例

次の例では、API名`mcp_server`によって`https://mcp.example.com/mcp_server`をMCP URLとして公開します。domainはrequestから得られるため、JSONには記載しません。

Tool本体の例：

```javascript
// javascript/mcp_example.js
({
  ok: true,
  message: "Nyan8 MCP is ready"
});
```

`api.json`の定義例：

```json
{
  "mcp_example": {
    "type": "api",
    "script": "./javascript/mcp_example.js",
    "paramCheck": "./javascript/mcp_example_input.js",
    "outCheck": "./javascript/mcp_example_output.js",
    "websocket": false,
    "title": "接続確認データを取得",
    "description": "Nyan8 MCPの接続確認用データを返します。",
    "securitySchemes": [
      {"type": "oauth2", "scopes": ["example:read"]}
    ],
    "annotations": {
      "readOnlyHint": true,
      "destructiveHint": false,
      "openWorldHint": false
    }
  },
  ".well-known/oauth-authorization-server": {
    "type": "api",
    "description": "Authorization Server Metadata"
  },
  ".well-known/oauth-protected-resource/mcp_server": {
    "type": "api",
    "description": "Protected Resource Metadata"
  },
  "oauth/authorize": {
    "type": "api",
    "script": "./runtime/oauth_policy.js"
  },
  "oauth/token": {
    "type": "api",
    "script": "./runtime/oauth_policy.js"
  },
  "oauth/register": {
    "type": "api",
    "script": "./runtime/oauth_policy.js"
  },
  "oauth/admin/users": {
    "type": "api",
    "script": "./runtime/oauth_policy.js"
  },
  "oauth/verify_access": {
    "type": "api",
    "script": "./runtime/oauth_policy.js",
    "scopes": ["example:read"]
  },
  "mcp_server": {
    "type": "mcp",
    "transport": "streamable_http",
    "protocolVersions": [
      "2025-11-25",
      "2025-06-18"
    ],
    "allowedOrigins": [
      "https://chatgpt.com",
      "https://platform.openai.com"
    ],
    "redirectURIAllowedPrefixes": [
      "https://chatgpt.com/connector/oauth/"
    ],
    "rateLimit": {
      "requests": 120,
      "window": "1m"
    },
    "maxConcurrent": 8,
    "oauth": {
      "authorizationServerMetadata": ".well-known/oauth-authorization-server",
      "protectedResourceMetadata": ".well-known/oauth-protected-resource/mcp_server",
      "authorize": "oauth/authorize",
      "token": "oauth/token",
      "register": "oauth/register",
      "adminUser": "oauth/admin/users",
      "verifyAccess": "oauth/verify_access"
    },
    "tools": ["mcp_example"],
    "instructions": "Nyan8 MCP Server"
  }
}
```

`tools`は公開を許可する通常API名の配列です。`public`、`schedule`、`ws_client`、別のMCP定義はToolとして指定できません。Tool名、title、description、inputSchema、outputSchema、実行scriptは参照先の通常APIから構成されます。`securitySchemes`と`annotations`が必要な場合も通常APIへ指定します。

schemaの取得規則は`/nyan/API名`と共通です。`paramCheck`の`nyanInputSchema`と`outCheck`の`nyanOutputSchema`、または通常API JavaScriptの既存schema表現から解決します。JSON Schema Draft 2020として設定snapshotの作成時に検証され、外部`$ref`は使用できません。

`type: "mcp"`は設定グラフ内に複数定義できます。それぞれのAPI名が独立したMCP endpointになります。OAuthを使う複数のMCP定義では、公開OAuth API名をMCPごとに重複させないでください。

#### ホットリロード

MCP、OAuth API、Tool対象の通常APIも、既存の`api.json`とinclude設定のホットリロード対象です。設定グラフ全体の展開と検証に成功した場合だけ、1つのimmutable snapshotとしてatomicに反映します。

- MCP endpoint名、Tool allowlist、OAuth API参照の追加・変更・削除は再起動不要です。
- 不正なJSON、存在しないAPI参照、不正なschemaなどがある候補は公開しません。
- reloadに失敗した場合は、直前の有効なsnapshotで動作を続けます。
- 1つのMCP requestでは同じsnapshotを使い、reload前後の定義を混在させません。

ホットリロード後に再度`tools/list`を呼ぶと最新のTool一覧を返します。現在はstateless MCPのため、接続済みclientへTool一覧変更をserver-to-client通知する機能はありません。`initialize`では`tools.listChanged: false`を返します。

### 7-3 OAuth

OAuth endpointはそれぞれ独立したAPIです。`type: "mcp"`の`oauth`には絶対URLやJavaScript pathではなくAPI名だけを指定します。metadata、authorize、token、registrationなどの絶対URLは、request originと参照先API名から動的に生成します。

Authorization Server MetadataをOAuth clientの標準discoveryで取得できるよう、対応API名には`.well-known/oauth-authorization-server`を使用します。Protected Resource Metadataも`.well-known/oauth-protected-resource/MCPのAPI名`を使用する構成を推奨します。

次のOAuth機能に対応しています。

- Authorization Server MetadataとProtected Resource Metadata
- Dynamic Client Registration（public client、`token_endpoint_auth_method: none`）
- Authorization Code grant
- PKCE S256
- `resource`によるtokenのMCPリソースへの紐付け
- ToolごとのOAuth scope
- access token
- refresh tokenのローテーション
- 使用済みrefresh tokenの再利用検知とtoken familyの失効

ChatGPTのDCRが`grant_types`に`authorization_code`と`refresh_token`を指定した場合、Nyan8は両方を登録し、authorization code交換時にrefresh tokenも発行します。`grant_types`が`authorization_code`だけの場合はaccess tokenだけを発行します。

未認証またはscope不足の`tools/call`には、HTTPの`WWW-Authenticate`とMCP resultの`_meta["mcp/www_authenticate"]`の両方を返します。

OAuth stateは、`config.json`の`oauth_state_directory`をrootとして、その下の`MCPのAPI名`へJSONで保存されます。未指定時はMCP定義元のディレクトリにある`oauth-state/MCPのAPI名`を使用します。これはruntimeの永続保存先であり、`api.json`のMCP定義には記載しません。

```json
{
  "oauth_state_directory": "/var/lib/nyan8/oauth"
}
```

```text
oauth-state/
  mcp_server/
    users/
    clients/
    requests/
    codes/
    tokens/
    refresh_tokens/
    refresh_families/
```

利用者のpasswordはArgon2id hashで保存します。authorization code、access token、refresh token、CSRFなどのcredentialは平文のファイル名では保存しません。state directoryは公開ディレクトリやreleaseの置換対象とは分離し、Nyan8の実行ユーザーだけが読み書きできる権限にしてください。

OAuth利用者を登録する管理endpointは、`config.json`の`oauth_admin`をBasic認証として使用します。次は構成例のURLを使用した登録例です。

```bash
curl --fail-with-body \
  --user 'operator:ADMIN_PASSWORD' \
  --header 'Content-Type: application/json' \
  --data '{"username":"example-user","password":"CHANGE_TO_A_LONG_PASSWORD"}' \
  https://mcp.example.com/oauth/admin/users
```

管理credentialとOAuth利用者credentialを`api.json`やGit管理下へ保存しないでください。

通常の`go test ./...`は特定名のOAuth policy JavaScriptを必要としません。実際のOAuth policyを使うE2Eを実行する場合だけ、runtimeファイルを明示します。

```bash
NYAN8_OAUTH_HOOK_TEST_PATH=/path/to/oauth_policy.js go test ./...
```

### 7-4 ChatGPTから接続する

ChatGPTでコネクターを作成するときは、`https://公開domain/MCPのAPI名`を登録します。上の例では次のURLです。

```text
https://mcp.example.com/mcp_server
```

ChatGPTはmetadataを取得し、DCRでOAuth clientを登録してからAuthorization Code + PKCEを開始します。表示されたNyan8のログイン画面で、管理endpointから登録済みのOAuth利用者credentialを入力してください。

URL、scope、OAuth API参照が一致しない場合は接続できません。とくに次を確認してください。

- MCP URLのpathが`/MCPのAPI名`になっている
- metadata API名がOAuth clientの標準discovery pathと一致している
- MCP定義のOAuth参照先が存在する通常API名になっている
- ChatGPTのcallbackが`redirectURIAllowedPrefixes`で許可されている
- Tool対象APIの`securitySchemes`に指定したscopeが、`verifyAccess` APIの`scopes`にも存在する

### 7-5 stdio MCPとして利用する

stdio対応のMCP clientは、Nyan8を子プロセスとして起動し、stdin/stdoutで改行区切りのJSON-RPC messageを交換します。HTTP serverとは別の起動モードです。

`transport: "streamable_http"`のMCP定義はAPI名に対応するHTTP endpointを持ちますが、`transport: "stdio"`のMCP定義にはURLがありません。stdio定義のAPI名は、`--mcp-server`で子プロセスとして起動する定義を選択するために使用します。stdio定義のAPI名をHTTPでリクエストしても、MCP endpointとしては公開されません。

| `transport` | MCP requestの経路 | URL |
|---|---|---|
| `streamable_http` | HTTP/HTTPSのJSON-RPC request | `/MCPのAPI名` |
| `stdio` | 子プロセスのstdin/stdoutによるJSON-RPC message | なし |

stdio専用の最小設定例：

```json
{
  "local_tool": {
    "type": "api",
    "script": "./javascript/local_tool.js",
    "title": "Local Tool",
    "description": "ローカルの処理を実行します。"
  },
  "local_mcp": {
    "type": "mcp",
    "transport": "stdio",
    "tools": ["local_tool"]
  }
}
```

同じToolをStreamable HTTPとstdioの両方で利用する場合は、MCP定義をtransportごとに分けます。次の例では`shared_tool`を共有し、`http_mcp`をHTTP endpoint `/http_mcp`として公開します。`local_mcp`にはURLがなく、stdio起動時に`--mcp-server local_mcp`で選択します。

```json
{
  "shared_tool": {
    "type": "api",
    "script": "./javascript/shared_tool.js",
    "title": "Shared Tool",
    "description": "HTTPとstdioから共通で利用する処理です。"
  },
  "http_mcp": {
    "type": "mcp",
    "transport": "streamable_http",
    "allowedOrigins": ["https://chatgpt.com"],
    "tools": ["shared_tool"]
  },
  "local_mcp": {
    "type": "mcp",
    "transport": "stdio",
    "tools": ["shared_tool"]
  }
}
```

`transport`は必須の文字列です。指定できる値は`streamable_http`と`stdio`です。旧形式の`transports`、`transport`の省略、空文字列、未対応値は設定エラーになります。

起動コマンド：

```bash
/absolute/path/to/Nyan8 \
  --mcp-server local_mcp \
  --api /absolute/path/to/api.json \
  --config /absolute/path/to/config.json
```

`--mcp-server`を指定すると、Nyan8は通常のHTTP serverではなく、指定したMCP APIのstdio serverとして起動します。指定した定義の`transport`が`stdio`でなければ起動エラーです。`--mcp-server`を省略すると通常のHTTP serverとして起動し、`transport`が`streamable_http`のMCP定義だけをHTTP endpointとして公開します。

MCP clientの一般的な設定は次の形です。

```json
{
  "mcpServers": {
    "nyan8": {
      "command": "/absolute/path/to/Nyan8",
      "args": [
        "--mcp-server",
        "local_mcp",
        "--api",
        "/absolute/path/to/api.json",
        "--config",
        "/absolute/path/to/config.json"
      ]
    }
  }
}
```

複数のMCP定義へstdioを許可したまま、同じ`api.json`で管理できます。ただし、1つのNyan8 stdio processが提供するMCP APIは1件です。複数を同時に利用する場合は、MCP clientからNyan8を定義ごとに別processとして起動し、それぞれを`--mcp-server`で選択します。

```json
{
  "mcpServers": {
    "nyan8-local": {
      "command": "/absolute/path/to/Nyan8",
      "args": [
        "--mcp-server",
        "local_mcp",
        "--api",
        "/absolute/path/to/api.json",
        "--config",
        "/absolute/path/to/config.json"
      ]
    },
    "nyan8-another-local": {
      "command": "/absolute/path/to/Nyan8",
      "args": [
        "--mcp-server",
        "another_local_mcp",
        "--api",
        "/absolute/path/to/api.json",
        "--config",
        "/absolute/path/to/config.json"
      ]
    }
  }
}
```

stdioモードでは次の規則が適用されます。

- HTTP/HTTPS listener、OAuth endpoint、schedule、WebSocket client、API hot reloadを開始しません。
- 起動時に作成した同じAPI snapshot、Tool allowlist、JSON Schema、通常APIのJavaScriptを利用します。
- OAuth tokenは要求せず、子プロセスを起動できるOS userをsecurity boundaryとします。
- Tool JavaScriptの`nyanAllParams.mcp_principal.transport`は`"stdio"`です。
- HTTP用の`securitySchemes`はstdioの`tools/list`へ出力しません。
- stdoutにはMCP messageだけを出力し、起動ログとJavaScriptの`console.log`はlog fileまたはstderrへ出力します。
- stdinがEOFになると正常終了します。設定変更を反映するにはMCP clientからprocessを再起動します。
- `allowedOrigins`、OAuth API参照、公開domainはstdio専用定義では不要です。


---
## 8 ライセンス
[MIT License](LICENSE.md)
