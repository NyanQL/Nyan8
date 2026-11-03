# Nyan8 v1.1.22: 外部MCP呼び出し・共通JavaScriptキャッシュを追加し、gojaを更新

サーバー側JavaScriptから、管理者が許可した外部MCPツールを呼び出せるようにします。また、共通JavaScriptを起動時に読み込んで再利用する設定を追加し、gojaをNyanQL・NyanPUIと同じバージョンへ更新します。

## 主な変更

### 外部MCP呼び出し関数 `nyanCallMCP`

- Streamable HTTPとstdioに対応し、接続ID・ツール名・JSON互換の引数を指定して同期呼び出しできるようにします。
- `config.json`の`mcpClient.connections`に、接続先、呼び出し元スクリプト、許可ツール、資格情報ファイルの参照、通信期限、サイズ・同時実行数の上限をまとめます。接続が未設定なら機能は無効です。`NYAN_MCP_CONFIG`による別ファイルの読み込みは廃止します。
- プロトコルの許可版、入力・出力スキーマ、接続先IP、資格情報ファイルなどを検証します。
- `tools/call`の結果全体を返し、`structuredContent`の省略と明示的な`null`を区別します。ツール自身の`isError:true`と、通信・検証エラーによる例外を区別します。
- HTTP要求の取消、scheduleの削除、アプリ停止をMCP通信へ伝播します。HTTPセッション終了時のDELETEと応答本文の読み取りにも呼び出し全体の期限・取消を適用し、終了処理の待機で同時実行枠が保持され続ける問題を修正します。
- [利用ガイド](NYANCALLMCP.md)と[HTTP／stdioの設定・JavaScript例](examples/nyanCallMCP/)を追加します。

### `javascript_include_cache` の追加

- `config.json`で共通JavaScriptのソースキャッシュを切り替えられるようにします。
- `false`または省略時は、従来どおり実行ごとにファイルを読み込みます。同梱設定は`false`です。
- `true`では、起動時に`javascript_include`を設定順で読み込み、連結したソース文字列を再利用します。読み込みに失敗した場合は起動を中止します。
- キャッシュ有効時に共通JSの変更を反映するには再起動が必要です。`api.json`のホットリロードではキャッシュを更新しません。
- 各実行では新しいVMを生成します。本体・`paramCheck`・`outCheck`のファイルは引き続き毎回読み込み、OAuth専用VMは従来どおり共通JSを読み込みません。
- READMEの設定説明、schedule、MCP stdioの説明を更新します。

### 依存関係とバージョン

- gojaを`v0.0.0-20240220182346-e401ed450204`から、NyanQL・NyanPUIと同じ`v0.0.0-20261031103838-cf2ff0ec0b31`へ更新します。
- 更新に必要な`regexp2/v2`、`golang.org/x/text`、`golang.org/x/sync`を更新し、`go mod tidy`で依存関係とチェックサムを整理します。
- goja更新後はBigInt構文を解析できるため、静的設定値のBigInt拒否テストをJSON非互換エラーの期待値に調整します。BigIntを静的設定値として拒否する動作は維持します。
- 外部MCPクライアント用にMCP Go SDK `v1.7.0`を追加し、`BinaryVersion`を`v1.1.22`へ更新します。

## 検証

最終コードで次の確認が成功しています。

- `go test ./...`
- `go test -race -run '^Test(MCPClientConfig|JavaScriptIncludeCache|NyanMCP|ParseStaticJavaScript|ExtractStaticJavaScript|OAuthPhase3HookRequest)' -count=1 ./...`
- `git diff --check`
- 3製品それぞれの`go list -mod=readonly -m github.com/dop251/goja`で、使用する版の一致を確認。

追加したテストでは、MCPのHTTP／SSE／stdio、入力・結果検証、通信期限と取消、HTTP終了処理中の取消、同時実行枠の解放を確認しています。`config.json`からの外部MCP接続設定、サイズ上限の指定、未知キー・不正値の拒否、旧環境変数を使用しないことも検証しています。共通JSキャッシュは、有効／無効、起動後の変更・削除、VM分離、並行実行、起動時エラー、MCP stdioの実プロセス起動を検証しています。

## 制約・既知の課題

- 外部MCP呼び出しは自動再試行しません。送信後のタイムアウトや結果検証失敗でも外部処理が完了している可能性があり、取消は外部処理のロールバックを意味しません。
- stdioで`maxResultBytes`を初期バッファの4 KiB未満に設定した場合、設定値を超える1行を受け入れることがあります。この指摘の修正は今回見送っています。既定の8 MiB設定はこの問題の影響を受けず、1セッションの累計受信量上限（`4 × maxResultBytes`）は残ります。
