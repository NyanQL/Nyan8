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
| **JSON‑RPC 2.0** | `/nyan-rpc` エンドポイントで RPC を提供（Batch は未対応） |
| **MCP / OAuth** | `api.json`で設定したStreamable HTTP MCPをOAuth 2.0 Authorization Code + PKCEで保護 |
| **メール送信** | `nyanSendMail` で CC/BCC・添付ファイルを含むメールを送信可能 |
| **ファイル→Base64** | `nyanReadFileB64` でファイルを Base64 文字列へ変換 |
| **ホストコマンド実行** | `nyanHostExec` でシェルコマンドを呼び出し、結果を JSON 取得 |
| **ログローテーション** | `lumberjack` による自動ローテーション／圧縮対応 |

---

## 2  インストール

1. [Releases](https://github.com/NyanQL/Nyan8/releases) から OS 向け zip を取得
2. 展開して実行ファイル（`Nyan8` / `Nyan8.exe`）を配置
3. `config.json` と `api.json` をプロジェクトルートに用意
4. 実行：
   ```bash
   ./Nyan8   # Windows は Nyan8.exe
   ```

### 2-1  設定ファイルのパス指定

Nyan8 は起動時に `api.json` と `config.json` の読み込みパスを指定できます。
指定がない場合は、従来どおり実行ファイルと同じディレクトリにある `api.json` / `config.json` を読み込みます。

```bash
./Nyan8
./Nyan8 --api /path/to/api.json --config /path/to/config.json
NYAN_API_PATH=/path/to/api.json NYAN_CONFIG_PATH=/path/to/config.json ./Nyan8
```

各 `api.json` 内の `script` / `path` / `paramCheck` / `outCheck` の相対パスは、その定義を書いた `api.json` が置かれているディレクトリから解決されます。
JavaScriptの `nyanGetFile` / `nyanReadFileB64` / `nyanSendMailAttachment` と `nyanSendMail` の添付 `path` も、そのAPIを定義したJSONのディレクトリを基準にします。include先のAPIはinclude先JSON、`nyanCallMe` は呼び出し先API、PushはPush先APIの定義場所が基準です。絶対パスはそのまま使用できます。
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
    "EnableLogging": true,          // false=標準エラー出力
    "Level": "info"                 // debug / info / warn / error
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
* **EnableLogging** – true はファイル、false は標準エラーへ出力（ログ停止ではありません）
* **Level** – `debug` / `info` / `warn` / `error`。省略時は `info`。指定以上の重大度のログを出力し、不正な値では起動を中止します。

</details>

サービスログはNyanQLと同じ、1行1件のJSON形式です。`time`、`level`、`msg`（処理名）と、API名・ファイル名・件数などを記録します。ファイル出力時のローテーション・圧縮設定は従来どおりです。標準出力にはサービスログを出さず、MCP stdioではJSON-RPC応答専用にします。設定読み込み前の起動エラーは標準エラーへ出力します。

通常の `info` では、起動、設定変更、ジョブ完了、接続状態、警告、エラーを記録します。リクエストのパラメータ、API設定全体、Push・ジョブ結果の本文は自動出力しません。WebSocket接続先はschemeとhostだけを記録します。Ginの標準アクセスログは使用せず、全リクエストのURL・ステータス・処理時間は記録しません。

`debug` では、Pushのバイト数、ジョブの次回実行時刻などに加え、**エラーの詳細文字列とJavaScriptの `console.log(...)` の本文**を出力します。詳細・consoleメッセージは4096バイトまでとし、超過時は省略マーカーを付け、改行はJSON内でエスケープします。これらには入力値や認証情報が含まれ得るため、調査時に使用してください。ジョブ完了の `result_bytes` は結果文字列のバイト数です。

```json
{"time":"2026-09-18T09:00:00+09:00","level":"INFO","msg":"schedule_completed","job":"daily_update","result_bytes":128}
```

従来のテキストログ解析はJSON形式への対応が必要です。`log.Level` を含む `config.json` の変更には再起動が必要です。

#### `api.json` のホットリロード

HTTPサーバーモードでは、ルートの `api.json` と、そこから `include` されたすべての `api.json` は既定で1秒ごとに確認され、変更を検知するとルートから再解析されます。通常API、public API、JSON-RPC、MCP、schedule、ws_clientの追加・変更・削除を反映します。ただし、HTTPエンドポイントには以下の制約があります。stdioモードではホットリロードを行いません。

起動時に登録したHTTPルートは再構築されません。そのURLを別のAPI名で再利用する変更などでは、設定のリロードが成功しても新しいAPIへ到達できない場合があり、再起動が必要です。たとえば、起動時の `x` を削除して `api/x` を追加すると、`/api/x` は旧API名 `x` を参照したままとなり、再起動するまで404を返します。

不正なJSON、存在しないinclude先、不正なschedule／ws_client設定などは採用されず、直前の正常な定義で稼働を継続します。失敗した候補内で新しく見つかったinclude先も監視されるため、ファイルの作成や修正だけで自動的に再試行されます。同じファイル状態とエラーは繰り返しログ出力されません。

通常APIは `/API名` と、API名が `api/` で始まらない場合の `/api/API名` に登録されます。`x` と `api/x` のように同じURLへ登録される通常APIの組み合わせは、include展開後の検証で設定エラーになります。初回起動では起動を中止し、ホットリロードでは直前の正常な定義を維持します。

`Interval` は `500ms`、`1s`、`1m`、`24h` などのGo duration形式です。空の場合は `1s` になります。0以下または解析できない値は起動エラーです。ホットリロードを無効にする場合は `Enabled` を `false` にします。`APIHotReload` 全体を省略した場合は有効、1秒間隔です。

schedule変更時は同名ジョブを二重起動せず、実行中のスクリプトを完了してから最新設定へ移行します。ws_clientはscript／descriptionだけの変更では接続を維持し、`connectURL` の変更時だけ旧接続を閉じて新しい接続先へ切り替えます。

`script`、`paramCheck`、`outCheck`、publicの配信ファイル自体は監視対象ではありません。これらは実行時またはリクエスト時に読み込まれます。`paramCheck` / `outCheck` 内の公開用スキーマも `/nyan/{API名}` のリクエストごとに読み直されるため、この詳細表示への反映には再起動や `api.json` の更新は不要です。

MCPの `tools/list` と `tools/call` は、設定スナップショット作成時に解決したスキーマを使います。スキーマファイルだけを変更してもMCPへは反映されません。HTTPモードでは `api.json` の変更を伴う正常なリロード、または再起動が必要です。stdioモードではプロセスを再起動してください。

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
  "script": "./javascript/ws/receiver_main.js",
  "connectURL": "ws://localhost:8889/hello",
  "description": "ローカル動作確認用（自身の /hello に接続）"
}
```

受信したメッセージは `nyanAllParams.ws_message_text` に入り、`script` で指定した JavaScript へ渡されます。戻り値は文字列化され、前後の空白を除去してから上流の WebSocket へテキストメッセージとして送信されます。空文字または空白だけの結果では返信しません。

動作確認には、`api.json` の `add` に `push: "hello"` を指定したサンプル構成を使います。

1. `config.json` の `Port` を `8889`、`log.Level` を `debug` にし、上記の `websocket_clients_local` を含む構成で `./Nyan8` を起動します。
2. `client: "websocket_clients_local"` の `ws_client_connected` ログで接続完了を確認した後、別ターミナルから次を実行します。

   ```bash
   curl "http://localhost:8889/add?addNumber=3"
   ```

3. `add` の実行で `hello` へのpushが発生し、Nyan8内の `ws_client` が受信します。`receiver_main.js` の `console.log` による受信内容を、ログの `script_console` で確認します。`EnableLogging: true` ならログファイル、`false` なら標準エラーへ出力されます。このサンプルは空文字を返すため、WebSocketへの返信は行いません。

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

schedule 定義自体には `paramCheck` / `outCheck` / `push` は適用されません。`nyanCallMe()` で通常APIを呼び出す場合は、呼び出し先の `paramCheck` / `outCheck` が実行されます。通知は schedule の `script` 内、または呼び出し先APIの本体に実装してください。

動作確認例の [api.json](./api.json) にある `schedule_debug_every_minute` と [javascript/schedule_debug.js](./javascript/schedule_debug.js) は、HTTPサーバーモードで1分ごとに実行されます。`info` ではジョブ名と結果のバイト数を含む完了ログを記録します。スクリプトが `console.log` へ渡す実行時刻などの本文を確認する場合は、`log.Level` を `debug` に設定してください。

### paramCheck / outCheck

対応する呼び出し経路では、`paramCheck` は本体 `script` 実行前または公開ファイル配信前、`outCheck` は本体実行後または公開ファイル送信前に実行されます。

| 呼び出し経路 | `paramCheck` / `outCheck` / `checkOnly` |
|---|---|
| 通常APIのHTTPエンドポイント `/API名` | 適用する |
| `type: "public"` のHTTPファイル配信 | 適用する |
| JSON-RPC `/nyan-rpc` | 適用する（レスポンスはJSON-RPC形式） |
| ルート経由の通常API `/?api=API名` | 適用しない |
| WebSocket経由の通常API | 受信メッセージごとに適用する（チェック結果をJSONフレームで返信） |
| `nyanCallMe()` | 適用する（チェック結果を呼び出し元に返す） |
| schedule、ws_client | 適用しない |
| MCP `tools/call` | 適用しない。JSON Schemaによる検証を行う |

`paramCheck` に認可処理を実装しても、適用しない経路からの実行は保護されません。認可を設計する際は、使用する呼び出し経路を確認してください。

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

`success: true` かつ `status: 200` の場合だけチェックを通過します。通常HTTP APIとpublic配信では、`paramCheck` で拒否すると本体や配信を実行せず、`outCheck` で拒否すると本体実行済みの結果や配信予定のファイルを送信しません。代わりにチェック結果をJSONで返し、HTTPステータスを `status` の値にします。

JSON-RPCの通常実行では `paramCheck` による拒否を `error.code: -32602`、`error.data` にチェック結果を入れたレスポンスとしてHTTP 200で返します。`checkOnly` または `outCheck` による応答では、チェック結果を `result` に入れ、HTTPステータスをチェック結果の `status` にします。

`nyanCallMe()` では、拒否された場合や `checkOnly` の場合にチェック結果のオブジェクトを呼び出し元へ返します。呼び出し元は `success` と `status` を確認してください。呼び出し元のHTTPレスポンスへ直接書き込むことはありません。

#### checkOnly

上表の対応経路で `nyan_mode=checkOnly` を指定すると、`paramCheck` だけを実行し、本体 `script` やファイル配信へ進みません。JSON-RPCでは `params.nyan_mode`、WebSocketでは送信するJSONの `nyan_mode`、内部呼び出しでは `nyanCallMe({ api: "secure_add", nyan_mode: "checkOnly" })` のように指定します。

```bash
curl "http://localhost:8080/secure_add?token=secret&nyan_mode=checkOnly"
```

対応するHTTPエンドポイントで `paramCheck` 未設定の場合は、次のレスポンスを返します。JSON-RPCでは同じオブジェクトが `result` に入ります。

```json
{
  "success": true,
  "status": 200,
  "result": null
}
```

#### outCheck の入力

`outCheck` は、本体の実行結果を出力する前に検査するためのチェックです。主に `result` など、本体が返すデータの値や構造が期待どおりかを確認します。

`nyanAllParams.nyan_output.body` には、本体の返却内容全体が文字列で入ります。本体がJSONを返す場合は、`JSON.parse()` で解析して `result` などを参照します。次は、本体が `{ success: true, status: 200, result: { message: "expected" } }` の形式のJSON文字列を返すAPIを想定した例です。

```javascript
const output = JSON.parse(nyanAllParams.nyan_output.body);
if (output && output.result && output.result.message === "expected") {
  ({ success: true, status: 200, result: null });
} else {
  ({ success: false, status: 409, result: { message: "output mismatch" } });
}
```

この例の `output.result` は本体の返却データです。チェック自身が返す `{ success, status, result }` は、チェックの合否や拒否理由を表します。チェック通過時には、本体の返却データが出力に使われます。

`nyan_output` には次の検査用データが入ります。

| キー | 説明 |
|------|------|
| `status` | 呼び出し経路に応じて設定する検査用ステータス |
| `contentType` | 検査対象の内容に対して設定する形式情報 |
| `headers` | 現在は空のオブジェクト `{}`。実際の応答ヘッダーは含まれない |
| `body` | 本体の返却内容全体、または公開ファイル全体の内容を文字列にしたもの |
| `bodyBase64` | 検査対象の内容をBase64にしたもの |
| `bodyLength` | 検査対象の内容のバイト長 |
| `bodyLengthBytes` | `bodyLength` と同じ互換用フィールド |

`status`、`contentType`、`headers` は付随情報で、通常API、JSON-RPCなどの呼び出し経路によって意味が異なります。`nyan_output` は出力前の検査用データであり、本文やバイト長を含め、最終的なHTTPレスポンスとの一致を保証するものではありません。

- 通常APIのHTTPエンドポイントでは、本体が返したJSON文字列を検査します。`status` は本体の数値 `status`、`contentType` は `application/json` です。検査通過後にJSONを再生成して送信するため、空白やキーの順序、バイト長が変わる場合があります。
- JSON-RPCでは、JSON-RPC形式に整形する前の本体のJSON文字列を検査します。`status` は本体の数値 `status`（数値がなければ `200`）、`contentType` は `application/json` です。たとえば本体の `status` が `201` なら検査時も `201` ですが、検査通過後の成功応答はHTTP `200` で、本体の `status` を除いたデータがJSON-RPCの `result` に入ります。
- `type: "public"` では、ファイル全体を検査します。`status` は `200`、`contentType` はファイル内容から推定した値です。検査通過後にHEAD、範囲指定、条件付きリクエストなどの処理を行うため、実際には本文なし、部分配信（`206`）、未更新（`304`）になる場合があります。配信時のContent-Typeも、拡張子などによって異なる場合があります。
- WebSocketと `nyanCallMe()` では、本体の返却内容を検査します。`status` は本体のJSONオブジェクトに数値で指定された値（それ以外は `200`）、`contentType` は有効なJSONなら `application/json`、それ以外は `text/plain` です。これらはHTTPレスポンスのステータスやヘッダーを表しません。

互換用に `nyan_output_status`, `nyan_output_content_type`, `nyan_output_body`, `nyan_output_body_base64` も利用できます。

通常APIのHTTPエンドポイント、`type: "public"`、JSON-RPC、WebSocket、`nyanCallMe()` では上表の範囲で同じチェック指定を利用できます。MCP `tools/call` ではチェック用JavaScriptを実行せず、解決済みの `inputSchema` と、定義されている場合の `outputSchema` で検証します。`public`、`schedule`、`ws_client` はJSON-RPC / MCP Toolとしては呼び出せません。

#### WebSocketでのチェック

受信したJSONメッセージの `api` に対応するAPIで、`paramCheck` → 本体 `script` → `outCheck` → 応答送信 → `push` の順に処理します。接続URLとメッセージの `api` が異なる場合も、チェック対象はメッセージの `api` です。ルート接続、`/API名`、`/api/API名` のいずれでも同じ処理を行います。

`paramCheck` で拒否された場合は本体・`outCheck`・`push` を実行せず、チェック結果を返します。`outCheck` で拒否された場合は本体の出力を送信せず、チェック結果を返して `push` も止めます。チェック結果は `{ success, status, result }` のJSONで、返信フレームは受信したtext/binaryの種別を維持します。`status` はフレーム内の値であり、接続済みのHTTPステータスを変更しません。チェックの実行・解析や結果のJSON化に失敗すると `success: false`、`status: 500` の結果を返します。これらの応答後も接続を維持し、次のメッセージを処理します。

`checkOnly` では本体・`outCheck`・`push` を実行しません。`paramCheck` が未設定なら `{ success: true, status: 200, result: null }` を返信します。

```json
{"api":"secure_add","token":"secret","nyan_mode":"checkOnly"}
```

`outCheck` の `nyan_output` は `nyanCallMe()` と同じ形式です。`body` は本体の返却内容そのもので、JSONオブジェクトの数値 `status` を使用し、省略時は `200` とします。有効なJSONなら `contentType` は `application/json`、それ以外は `text/plain`、`headers` は空のオブジェクトです。本体が `success: false` を返す場合も検査します。

接続情報は `nyanAllParams._headers`、`_remote_ip`、`_user_agent` で参照できます。これらはサーバーが接続時の情報で上書きします。WebSocketでは `nyanGetCookie()`、`nyanGetRemoteIP()`、`nyanGetUserAgent()`、`nyanGetRequestHeaders()` は空の値を返すため、チェックでも `nyanAllParams` の接続情報を使用してください。

チェックはAPIを実行するメッセージに適用します。WebSocket接続の確立やPushの購読登録時には実行しません。また、Push先API自体の `paramCheck` / `outCheck` と `type: "ws_client"` の受信スクリプトには適用されません。

---

## 4   Javascript 上で実行可能な関数と概要

| -  | 関数                                | 概要                                |
|----|-----------------------------------|-----------------------------------|
| 1  | `nyanAllParams`                   | GET/POST/JSON 受信パラメータをまとめたオブジェクト  |
| 2  | `console.log()`                       | debug時にJSONログとしてファイルまたは標準エラーへ出力 |
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
console.log(nyanAllParams);
```

### 4‑2 console.log

`console.log` は `log.Level: "debug"` の場合だけ出力されます。既定の `info`、`warn`、`error` では出力されません。`EnableLogging: true` ならログファイル、`false` なら標準エラーが出力先です。

ログは `level: "DEBUG"`、`msg: "script_console"` のJSONで、引数をまとめた文字列が `message` に入ります。本文は4096バイトまでとし、超過時は省略マーカーを付けます。

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

同じNyan8プロセス内で共有するメモリ上のkey-valueストレージです。キーと値は文字列で、未登録のキーを取得すると空文字列を返します。ファイルには保存されず、再起動すると内容は失われます。

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

#### 戻り値の例

返却オブジェクトの `stdout` にコマンドの標準出力、`stderr` に標準エラー出力が入ります。`console.log(result)` はdebug時だけ出力され、このオブジェクトをJSON文字列化した内容がログの `message` に入ります。

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

実行対象のAPIを定義した `api.json` のディレクトリからの相対パス、または絶対パスでファイルを指定します。たとえば `/srv/service/api.json` に定義したAPIの `nyanGetFile("./data.txt")` は `/srv/service/data.txt` を読み込みます。include先のAPIでは、そのAPIを定義したJSONのディレクトリが基準です。本体・`paramCheck`・`outCheck` で同じ基準を使用します。
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
| attachments  | Array       | 添付ファイルの配列。各要素は `path` または `dataBase64` を持つ。相対 `path` は実行対象APIの定義JSONがあるディレクトリ基準。|
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
相対パスは実行対象APIの定義JSONがあるディレクトリ基準です。絶対パスも指定できます。

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
相対パスは実行対象APIの定義JSONがあるディレクトリ基準です。絶対パスも指定できます。

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
- 呼び出し先の `paramCheck` → 本体 `script` → `outCheck` の順に実行します。チェックは `success: true` かつ `status: 200` の場合だけ通過します。
- `paramCheck` で拒否されると本体・`outCheck` は実行せず、チェック結果を返します。`outCheck` で拒否されると本体の結果の代わりにチェック結果を返します。
- `nyan_mode: "checkOnly"` では本体・`outCheck` を実行せず、`paramCheck` の結果を返します。`paramCheck` が未設定の場合は `{ success: true, status: 200, result: null }` を返します。
- `outCheck` には本体の返却内容を `nyan_output.body` などで渡します。JSONオブジェクトに数値の `status` があれば使用し、それ以外は `200` とします。`contentType` は有効なJSONなら `application/json`、それ以外は `text/plain`、`headers` は空のオブジェクトです。本体が `success: false` を返す場合も検査します。チェック通過時の戻り値は従来どおりです。
- `push` は実行しません。
- 本体やチェックの実行、チェックの戻り値の解析に失敗すると JavaScript 側で例外が投げられます。

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

通常HTTP APIやJSON-RPCでは、公開したJSON Schemaによる入力・出力の自動検証は行いません。対応する呼び出し経路で `paramCheck` / `outCheck` のJavaScriptを使って検査してください。MCP `tools/call` では、入力を `inputSchema`、出力を定義済みの `outputSchema` で自動検証します。

Nyan8は `success`、`status`、`result` を明示出力スキーマへ自動追加しません。実際のAPIレスポンスも本体JavaScriptが生成します。通常HTTP APIのレスポンスには数値の `status` が必要ですが、MCP Toolの結果には必須ではありません。JSON-RPCでは `success: false` のエラー判定時に数値の `status` を必要とし、成功結果では省略できます。

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

MCP Toolとして参照されていない通常APIでは、動的な定義など静的に解決できない明示スキーマがあっても `api.json` の読み込みやホットリロードは妨げません。対象のAPI詳細を取得した時点でスキーマ解決エラーを返し、ファイルを修正すれば次の詳細取得から反映されます。この詳細取得ではJSON Schemaとしての妥当性までは検証しません。

MCP Toolとして参照されるAPIは、設定スナップショットの作成時にスキーマの静的解決とJSON Schemaの妥当性検証を行います。ここで失敗すると初回起動はエラーになり、ホットリロードでは候補を採用せず直前の設定を維持します。

---
## 6  レスポンス形式

レスポンス形式は呼び出し経路によって異なります。

### 通常HTTP API

本体JavaScriptは数値の `status` を含むJSON文字列を返してください。Nyan8はその値をHTTPステータスに使い、JSON本文を返します。`success` や `result` は自動追加されません。たとえば次のスクリプトはHTTP 200を返します。

```javascript
JSON.stringify({status: 200, success: true, result: []});
```

```json
{
  "status": 200,
  "success": true,
  "result": []
}
```

API本体の実行失敗などでNyan8が返すエラーは次の形式です。`detail` は元のエラーがある場合だけ付加され、HTTPステータスはエラーの種類によって異なります。ログレベルによる詳細出力の制御はログに適用され、HTTPレスポンスの `detail` は制御しません。

```json
{
  "error": "Failed to run JavaScript",
  "detail": "ReferenceError: example is not defined"
}
```

### チェック結果

通常HTTP APIとpublic配信で `paramCheck` / `outCheck` が拒否した場合や、`checkOnly` で結果を返す場合は、次の形式になります。HTTPステータスは `status` の値です。

```json
{
  "success": false,
  "status": 401,
  "result": {"message": "unauthorized"}
}
```

### JSON-RPCとMCP

JSON-RPC `/nyan-rpc` は `jsonrpc`、`id` と `result` または `error` を持つ形式で返します。成功時は本体が返したJSONオブジェクトから `status` を除いた内容を `result` に入れ、HTTP 200を返します。チェック処理による応答は「paramCheck / outCheck」の説明を参照してください。

```json
{
  "jsonrpc": "2.0",
  "result": {"success": true, "result": []},
  "id": 1
}
```

MCP `tools/call` はMCPのJSON-RPC形式で返します。Tool実行成功時の `result` には `content`、`structuredContent`、`isError: false` が入ります。本体JavaScriptはJSONとして扱えるオブジェクトなど、またはJSON文字列を返せます。通常HTTP API用の `status` は不要です。Toolの入力・出力スキーマ検証や実行の失敗は `isError: true`、不正なJSON-RPCメソッドや `params` の構造などはJSON-RPCの `error` として返します。

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

OAuthのAPI参照名は前後の空白を除去して検証・使用します。`oauth` が未設定、または参照名がすべて空文字の場合は認証不要です。空白だけの参照名は認証不要とは扱わず、不正な設定として拒否します。

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

Go側はAuthorization Server MetadataとProtected Resource Metadata、OAuth endpointへのルーティング、hookの実行、暗号処理とstate保存のヘルパーを提供します。metadataにはAuthorization Code、refresh token、PKCE S256、`token_endpoint_auth_method: none` への対応を掲載します。

次の処理は、設定したOAuth JavaScriptで実装する必要があります。Go側が自動的に実装・保証するものではありません。

- Dynamic Client Registrationと許可するgrant typeの判定
- ログイン画面、利用者認証、Authorization Codeの発行・交換
- PKCE S256の検証
- access tokenの発行と、`resource`・有効期限・Toolごとのscopeの検証
- refresh tokenの発行・ローテーション
- 使用済みrefresh tokenの再利用検知とtoken familyの失効

たとえばDCRで `authorization_code` と `refresh_token` を受け付けた場合のrefresh token発行や、`authorization_code` だけの場合の発行抑止は、OAuth JavaScript側で制御します。利用するpolicyは、Go側が公開するmetadataと整合させてください。

未認証またはscope不足の`tools/call`には、HTTPの`WWW-Authenticate`とMCP resultの`_meta["mcp/www_authenticate"]`の両方を返します。

OAuth state用ヘルパーの保存先は、`config.json` の `oauth_state_directory` をrootとした、その下の `MCPのAPI名` です。未指定時はMCP定義元のディレクトリにある `oauth-state/MCPのAPI名` を使用します。保存する内容やキーはOAuth JavaScriptが決め、書き込む値は有効なJSONである必要があります。これはruntimeの永続保存先であり、`api.json` のMCP定義には記載しません。

```json
{
  "oauth_state_directory": "/var/lib/nyan8/oauth"
}
```

OAuth JavaScript側で構成する保存先の例：

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

OAuth JavaScriptでは、提供される `nyanArgon2idHash` / `nyanArgon2idVerify` を使って利用者のpasswordを扱い、authorization code、access token、refresh token、CSRFなどのcredentialを平文のファイル名として保存しないよう実装してください。state directoryは公開ディレクトリやreleaseの置換対象とは分離し、Nyan8の実行ユーザーだけが読み書きできる権限にしてください。

管理用OAuth JavaScriptは `nyanOAuthAdminAuthorized` を呼び出すことで、`config.json` の `oauth_admin` に対するBasic認証を検証できます。Go側が管理hookの実行前に自動認証するわけではありません。次は、この認証と利用者登録処理を実装したpolicyに対するリクエスト例です。

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

OAuth接続には、metadataに対応するDCR、Authorization Code + PKCE、利用者認証をOAuth JavaScriptに実装しておく必要があります。認可時に表示するログイン画面や、管理endpointで登録した利用者の検証も、このJavaScriptが担当します。

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
- API snapshot、Tool allowlist、JSON Schemaは起動時に作成したものを利用します。本体JavaScriptと共通の `javascript_include` ファイルはTool実行ごとに読み込みます。
- OAuth tokenは要求せず、子プロセスを起動できるOS userをsecurity boundaryとします。
- Tool JavaScriptの`nyanAllParams.mcp_principal.transport`は`"stdio"`です。
- HTTP用の`securitySchemes`はstdioの`tools/list`へ出力しません。
- stdoutにはMCP messageだけを出力します。ログは設定したレベルに従ってlog fileまたはstderrへ出力し、JavaScriptの `console.log` は `log.Level: "debug"` の場合だけ記録します。
- stdinがEOFになると正常終了します。設定変更を反映するにはMCP clientからprocessを再起動します。
- `allowedOrigins`、OAuth API参照、公開domainはstdio専用定義では不要です。


---
## 8 ライセンス
[MIT License](LICENSE.md)
