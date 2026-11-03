# nyanCallMCP — 外部MCPツール呼出し

サーバー側Gojaから、管理者が登録した接続ID・ツールを同期呼出しします。機能は既定で無効です。`nyanCallMe` と既存のMCP **server** 機能は変更しません。

```javascript
const result = nyanCallMCP({
  connection: "inventory-read",
  tool: "lookup_stock",
  arguments: { sku: "CAT-001" },
  options: { timeoutMs: 3000 }
});
if (result.resultType === "input_required") {
  throw new Error("追加入力が必要です。自動継続しません。");
}
if (result.isError === true) {
  // ツール自身の失敗。外部処理の取消しを意味しません。
  throw new Error("参照処理を完了できませんでした。");
}
if (!Object.prototype.hasOwnProperty.call(result, "structuredContent")) {
  throw new Error("構造化結果がありません。");
}
// false / 0 / null / 空文字列も存在する値として扱います。
const data = result.structuredContent;
```

## 契約

- 引数は1つのオブジェクト。`connection` と `tool` は必須の文字列です。URL・コマンド・ヘッダー・資格情報をJSから指定できません。
- `arguments` はJSON互換objectで、省略時は `{}`。トップレベルarray/null、関数、undefined、BigInt、NaN/Infinity、安全な整数範囲を超える数、循環参照、getter、toJSON、Proxy、疎配列、Symbol、Dateなどは拒否します。通常のJS Numberの小数精度は変わりません。
- `options` は省略可。`timeoutMs` は正の整数のみ。未知キーを拒否します。指定が管理者上限を超える場合はエラーです。
- 戻り値は `tools/call` の **result全体** をJSON解析したJSオブジェクトです。`content` の順序・image/audio/resource等・annotations・`_meta`・拡張フィールドを保持します。`content[].text` のJSON文字列を自動解析しません。resource URIを自動取得しません。
- `structuredContent` の省略と明示的nullを区別します。2025-06-18接続はobjectのみ、2026-07-28接続は任意のJSON値を受け付けます。
- `isError:true` は例外にせず結果として返します。`input_required` も未完了の結果として返し、自動回答・継続・再送はしません。
- 通信/JSON-RPC/schema/許可/上限違反は `NyanMCPError` をthrowします。`code`、`executionState`、`connection`、`tool`、`correlationId`、必要に応じて `rpcCode` / `httpStatus` を持ちます。メッセージに外部エラー本文や資格情報を含めません。
- `executionState:"not_sent"` はtools/call送信前の拒否、`"unknown"` は送信を試みた後です。timeoutや結果検証失敗でも外部処理は成功している可能性があります。**自動再試行は0回**です。

主なcode: `INVALID_ARGUMENT`, `CONNECTION_NOT_ALLOWED`, `CALLER_NOT_ALLOWED`, `TOOL_NOT_ALLOWED`, `INPUT_SCHEMA`, `OUTPUT_SCHEMA`, `AUTH_REQUIRED`, `TRANSPORT`, `MCP_RPC`, `PROTOCOL_VERSION`, `UNSUPPORTED_RESULT`, `NUMBER_RANGE`, `RESULT_TOO_LARGE`, `TIMEOUT`, `CANCELLED`, `BUSY`, `QL_TRANSACTION`, `RENDER_WRITE_FORBIDDEN`。

## 起動設定

接続設定は、Nyan8本体の **`config.json` の `mcpClient.connections`** に記述します。MCP専用の設定ファイルは使用しません。設定は起動時に一度だけ読み、`mcpClient`が未指定・null、または`connections`が空なら全接続を拒否します。`mcpClient`内の未知キー・不正値・存在しない許可スクリプト・不適切な秘密ファイルは起動エラーです。既存のapi.jsonのhot reloadと独立しており、MCP接続設定の変更は再起動が必要です。

同梱の`config.json`では、次のように接続を空にしてあります。

```json
{
  "mcpClient": {
    "connections": {}
  }
}
```

このリポジトリの `examples/nyanCallMCP/` にHTTP/stdio別の`mcpClient`設定例と参照用JSがあります。例の`mcpClient`項目を既存の`config.json`へ統合し、パス・tool名・接続先を環境に合わせて編集してください。例のJSONファイル自体を別途読み込む仕組みはありません。

以前の`NYAN_MCP_CONFIG`環境変数は使用しません。既存の専用設定を移行する場合は、その`connections`を`config.json`の`mcpClient.connections`へ移してください。

| フィールド | 意味 |
|---|---|
| connections | `mcpClient`直下に置く、接続IDをキーとするobject（最大64接続） |
| transport | `streamable_http` または `stdio` |
| allowedScripts | 呼出し元本体JSの絶対パス。symlink解決後のパスを照合。API名や引数のuserIdで権限を選ばない |
| tools | 正確なtool名 → `{ "readOnly": true/false }`。実行権限と参照/更新の管理者宣言 |
| protocolVersions | `2025-06-18` / `2026-07-28` の明示許可リスト |
| credentialMode | 初期版は `service` のみ。利用者別資格情報を黙ってサービス主体へ代替しない |
| timeoutMs / maxTimeoutMs | 既定30,000 / 上限120,000ミリ秒。親contextの期限を優先 |
| maxInputBytes / maxResultBytes | 接続ごとに整数バイト数で指定。既定1 MiB / 8 MiB。指定範囲はそれぞれ1〜1,048,576 / 256〜8,388,608。省略・0は既定値 |
| maxConcurrent | 接続単位で既定4、最大32。同一プロセス全体でも32。待機キューなし、超過はBUSY |
| allowQLRead | QLから参照を許可する明示フラグ。既定false |

`allowedScripts` はサービス主体の**スクリプト単位の権限**です。エンドユーザーの認可は既存のAPI認証・paramCheck等で実施してください。同じスクリプトが複数APIから利用される場合、その全経路が許可対象になります。includeは本体スクリプトの権限で実行されます。利用者別の資格情報選択・自動OAuthは未対応です。

例えば接続IDが`inventory-read`の場合、結果サイズの上限は`config.json`の`mcpClient.connections.inventory-read.maxResultBytes`で指定します。`1048576`なら1 MiBです。

### Streamable HTTP

- `url`: 固定のscheme/host/port/path。HTTPSが既定。userinfo/query/fragmentは拒否します。
- `allowHTTP:true`: 管理者がHTTPを明示許可する場合のみ。ローカル試験例以外ではHTTPSを使用してください。
- `allowedCIDRs`: 省略時は公開宛先のみ。設定すると**そのCIDRに含まれるIPだけ**に接続します。loopback/privateネットワークには明示許可が必要です。metadata/link-local/unspecified/multicastはCIDRで許可しても拒否します。
- DNSの全解決結果を検証し、そのIPへ直接接続するため検証後に再解決しません。TLS検証は有効。redirect・環境proxyは使いません。ブラウザーから受けたAuthorization/Cookieを転送しません。
- `bearerTokenFile`: 任意。絶対パスの通常ファイルでowner以外の権限がないもの（例0600）。最大8 KiB、1行の既存トークンを参照します。呼出しごとに再読込するので次回呼出しから更新が有効です。設定例に実トークンを書かないでください。
- JSONとSSEの応答に対応。初期版はSSEの応答ストリーム完了までバッファし、進捗UIは提供しません。展開後の応答全体に上限を適用します。永続GET/SSE、再接続、旧HTTP+SSE transportは使いません。

### stdio

- `executable`: 管理者管理の実行可能ファイルの絶対パス。通常ファイル・group/other書込不可。symlinkは起動時に固定します。
- `args`: 固定引数のみ（最大64）。`directory`: 固定した絶対作業ディレクトリ。
- shell・パッケージrunner・主要な汎用インタープリターを実行対象に指定できません。シェル文字列を組み立てません。
- 親プロセスの環境変数を引き継ぎません。`environmentFiles` は環境変数名→秘密ファイルの絶対パス。必要な値だけを設定し、PATH/HOME/LD_*/DYLD_*などを拒否します。
- 呼出しごとに専用プロセスを起動し、完了/取消時に終了してWaitします。Unixでは専用プロセスグループも終了させます。stdoutはMCP専用、stderrは保存せず破棄します。stdio入力にはフレーム上限と1セッション最大4×maxResultBytesの受信量上限があります。
- この機能はOSサンドボックスではありません。信頼するMCP実行ファイルを低権限アカウントで動かし、必要なCPU/メモリ/ファイル/ネットワーク制限はOSやコンテナーで設定してください。任意の子プログラムを安全化する保証はありません。

## 検証・互換性

公式 [Go SDK v1.7.0](https://github.com/modelcontextprotocol/go-sdk/tree/v1.7.0) を固定依存に追加しています（SDKの最小Goは1.25、製品は既存どおりGo 1.26.9）。SDKの対応版全てをこの製品で許可するわけではありません。

2026-07-28は `server/discover` と要求メタデータ、2025-06-18はSDKによる旧版 `initialize` → `notifications/initialized` を使います。交渉版が接続設定の許可版でなければ `PROTOCOL_VERSION` でtools/call前に停止します。SDKが先に選んだ未許可版から、独自の再交渉・暗黙ダウングレードはしません。

呼出しごとにtools/listをページングし、管理者許可と入力schemaを両方確認します。成功結果にoutputSchemaがあればstructuredContentの存在と型を検証します。isErrorには成功schemaを適用しません。schemaの外部URL/file参照は取得しません。JSON Schemaは既存のsanthosh-tekuri/jsonschema v6.0.3を使用します。

接続・認証・schemaの共有キャッシュはありません。Goja VMを別goroutineから操作しません。JSONの深さ64・ノード数100,000を制限し、数値範囲の逸脱は丸めず拒否します。入力はUTF-8 JSON化後、結果は展開済みwire応答で上限を確認します。

監査ログは製品・設定済み接続/ツール・呼出し元・版・分類・経過時間、例外ログは相関IDを記録します。引数/結果全文・資格情報・サーバーエラー本文・stderrは記録しません。アプリ側が結果をconsole.logする場合は別扱いです。ログ保持期間は既存ログ設定・運用方針に従います。

## 期限と未対応事項

親HTTP要求の取消・schedule削除・アプリのSIGINT/SIGTERMをMCP I/Oに伝えます。アプリ終了時は最大2秒だけMCPの解放を待ち、元のシグナル終了へ戻ります。取消は外部処理のrollbackを意味しません。JS全体の任意処理やschemaコンパイルを強制中断する機能ではありません。

未対応: 利用者別資格情報、OAuth同意/更新UI、sampling、elicitation自動回答、input_requiredの継続UI、進捗UI、共有session/cache、キュー・outbox・永続worker・結果照会・冪等性キー管理・自動補償、OS資源制限の自動設定。WebSocketバックグラウンド処理の切断/設定変更は専用の親MCP取消に未接続で、呼出しの期限とアプリ停止で終了します。既存のHTTP/ファイル/host実行関数を含め、信頼できないJSを隔離する仕組みではありません。

外部実行とDB commitは一体ではなく、障害後の結果不明・重複は接続先の契約と業務設計で扱います。scheduleの存在やこのクライアントをもってexactly-onceを保証しません。

## 開発・試験

`main.go` の `BEGIN shared nyanCallMCP` ブロックと `main_test.go` の共通適合テストは、関連製品との契約確認に使用します。設定読み込みは本製品の`config.json`へ統合しています。独立リポジトリとNyan8の単一ファイル方針に従い、初期版は共有モジュールへの物理分割を行っていません。

```sh
go test -run '^TestNyanMCP' -count=1 ./...
go test -race -run '^TestNyanMCP' -count=1 ./...
go test ./...
go build -o /tmp/nyan-mcp-check main.go
```

試験はhttptestのランダムloopbackポートとテストバイナリのstdio子プロセスを利用し、実MCPサービスや資格情報を必要としません。テストバイナリを既存の実行ファイルへ上書きしないでください。

## Nyan8での利用

APIのscriptやscheduleのJS本体をallowedScriptsに登録します。JS全体を包むDBトランザクションはありません。更新系を使う場合はAPI認可・利用者の明示操作・接続先の冪等性/結果照会を組み合わせてください。scheduleの削除は進行中MCPを取消しますが、外部実行の取り消しや重複防止を保証しません。OAuth専用の制限付きVMには追加していません。
