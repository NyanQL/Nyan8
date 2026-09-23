# Nyan8 18項目の検討結果とNyanPUI・NyanQL機能調査資料

作成日時：2026-09-23 18:13:45 JST

対象：Nyan8の作成時点の作業ツリー（HEAD：`54b5135`、その後の未コミットの修正を含む）

用途：NyanPUI・NyanQLで、同名関数の意味と各実行経路の挙動を調査するための比較基準

## この資料の位置付け

同名関数が各製品内で同じような意味の結果を返し、チェック・Pushなどの仕組みも意図した挙動になることを確認する。JavaScript・設定の相互置換や、全製品への同一機能の追加は目的としない。HTML・JSONなど用途による違いも、それだけで修正対象にしない。

この資料は、18項目の検討を経た**現在のNyan8の挙動**をまとめたもの。NyanPUIを一律に正解とはせず、維持を選んだ差も明記する。NyanPUI・NyanQLの現在の全機能を再調査した結果ではなく、両製品への変更指示でもない。これまでの対応ではNyanPUI側を変更していない。NyanQLの対応状況は、この資料作成では判定していない。

番号は既存の1～18を維持する。各項目の「調査ケース」は、調査対象製品の実装・呼び出し方に合わせて再現する。機能が存在しなければ「対象外／相当機能なし」と記録し、未調査と区別する。

**項目6の注意：** サーバーメモリとして利用する方針とREADMEへの説明追記は確定済み。ただし、未登録キーの戻り値を空文字列のままにするか、`null` にするかは、既存一覧では方針確認が残っている。現在の実装は空文字列であり、`null` への変更は行っていない。

参照元：

- [固定番号の確認・対応一覧](../docs/20260922_204654_nyan8_nyanpui_checklist.md)：検討経緯と作業記録。
- [README.md](../README.md)：現在の利用仕様。
- [main.go](../main.go)：現在の実装。
- [main_test.go](../main_test.go)：具体的な入力・期待値を含む検証コード。
- [agent.md](../agent.md)：比較の目的、固定番号、Goファイルを分割しない方針。

過去の作業記録には「項目18は未対応」など、その作業当時の記述もある。本資料では後続の修正を反映した最終状態を記載する。相対リンクはNyan8リポジトリ内で本資料を開いた場合に使用できる。

## 18項目の判断結果

| 番号 | 項目 | 判断・対応結果 | 現在のNyan8の要点 |
|---|---|---|---|
| 1 | ファイルの相対パス | 修正済み | 実行時のファイル操作は最上位の`api.json`を基準にする |
| 2 | `nyanCallMe`のAPI名省略 | 修正済み | 非空の文字列`api`が必須。省略は例外 |
| 3 | `nyanCallMe`のチェック拒否 | 現状維持 | 拒否はチェック結果を返す。実行エラーとは区別する |
| 4 | `nyanHostExec`の失敗 | 修正済み | コマンドの非0終了も実行結果として返す |
| 5 | `nyanGetAPI`の通信失敗 | 現状維持 | 通信・読み取り失敗は例外。HTTPエラー本文は取得可能 |
| 6 | `nyanGetItem`の未登録キー | 説明追記済み・戻り値の方針確認あり | 共有サーバーメモリ。未登録は現在`""` |
| 7 | `nyanSetCookie`のHTTPS対応 | 修正済み | Nyan8がTLSで受信した場合に`Secure`を付ける |
| 8 | `nyanBase64Decode`の不正入力 | 現状維持 | OAuth用関数は不正Base64で`""`を返す |
| 9 | `nyanRandomBase64URL` | 修正済み | 既定32バイト、指定範囲1～1024バイト |
| 10 | ルートHTTPのチェック | 修正済み | `/?api=API名`でも前後チェック・`checkOnly`を適用 |
| 11 | MCPのチェック | 修正済み | HTTP・stdioとも前後チェック・`checkOnly`を適用 |
| 12 | OAuthのチェック | 修正済み | HTTP処理・メタデータ・内部トークン検証に前後チェックを適用 |
| 13 | PUIのWebSocket `checkOnly` | 差を許容・修正不要 | 成功時の独自`result`が失われても通過判定が分かればよい |
| 14 | Pushの複数接続 | 修正済み | 同じAPIの全購読接続へ同じ結果を送る |
| 15 | Push接続の解除 | 修正済み | 終了・送信失敗した接続だけを除去する |
| 16 | Pushの実行条件 | 修正済み | 呼び出し元のエラー結果ではPushを開始しない |
| 17 | 入力パラメータの優先順位 | 現状維持 | 通常HTTPはクエリより本文を優先する |
| 18 | リクエスト情報の引き継ぎ | 修正済み | WS・Push・HTTP MCPのJavaScriptへ実際のリクエスト情報を渡す |

## 追加判断：nyanCallMeからのPush（2026-09-23）

項目14・16・18に関連する追加対応として、内部呼び出し先APIの`push`も実行するよう変更した。呼び出し先の入力チェック・本体・出力チェック通過後、通常と同じ成功条件でPushを開始する。チェック拒否・例外・エラー結果・`checkOnly`では起動しない。Push先の前後チェックも行い、全購読接続へtextフレームで送る。

Pushには内部呼び出し先のAPI名と引数、元のリクエスト情報、同じ設定スナップショットを渡す。Push先の拒否・実行エラーでも`nyanCallMe`の戻り値は維持する。子の配信は内部呼び出しが戻る前に行い、親APIのPushとは別に実行する。後から親が拒否・エラーになっても子の配信は取り消さない。

調査では、直接HTTP実行と内部呼び出しの両方で配信されること、チェック未設定でも配信すること、親子のPushが各1回であること、エラーと`checkOnly`では配信しないことを追加確認する。Nyan8のテストは`TestNyanCallMePush`と`TestNyanCallMePushBeforeParentCompletes`。

MCPはToolの参照先API自身の`push`を自動実行しないが、Tool内で`nyanCallMe`を実行した場合はその呼び出し先のPushが動作する。

## 共通のチェック仕様と調査の観点

チェックの結果は`{success, status, result}`。通過条件は**真偽値の`success: true`かつ数値の`status: 200`**である。`success: true, status: 201`でもチェックは通過しない。通常の成功応答やPush開始条件と混同しない。

通常実行は`paramCheck → 本体 → outCheck`。入力チェックの拒否では本体・出力チェックを実行しない。出力チェックの拒否では本体は既に実行済みで、その副作用を取り消さない。

`nyan_mode: "checkOnly"`では入力チェックだけを実行し、本体・出力チェック・Pushへ進まない。入力チェックがなければ`{success: true, status: 200, result: null}`を返す。認証、HTTP入力検証、MCP入力スキーマ検証まで省略する指定ではない。

有効な拒否結果と、チェックの例外・形式不正・ファイル欠落は区別する。チェック結果をどこに格納し、どのHTTPステータスで返すかは経路ごとに異なる。

| 経路 | 現在の適用範囲・特徴 |
|---|---|
| 通常HTTP・ルートHTTP | 前後チェック・`checkOnly`。拒否結果の`status`をHTTPステータスに使用 |
| publicのHTTP配信 | 配信前の入力・出力チェックと`checkOnly` |
| JSON-RPC | 前後チェック・`checkOnly`。通常の入力拒否はHTTP 200、`error.code: -32602`、`error.data`にチェック結果。`checkOnly`／出力拒否は`result`にチェック結果、HTTPステータスはその`status` |
| WebSocket接続前 | API指定時に入力チェックのみ。接続時の`checkOnly`はHTTPで結果を返し、接続を確立しない |
| WebSocketメッセージ | メッセージの対象APIで前後チェック・`checkOnly` |
| `nyanCallMe` | 前後チェック・`checkOnly`。チェック結果を呼び出し元へ返す |
| Push先 | 前後チェック。拒否・エラー・`checkOnly`では配信しない |
| MCP HTTP・stdio | 入力スキーマ検証後に前後チェック・`checkOnly`。MCP結果として返す |
| OAuth | HTTP用API・メタデータ・内部トークン検証で前後チェック。認証判定は専用の扱い |
| schedule・ws_client | 定義自体には適用しない。内部で`nyanCallMe`を使う場合は呼び出し先のチェックを適用 |

各チェックには別名設定もある。`paramCheck`の別名は`paramcheck`／`check`、`outCheck`の別名は`outcheck`。

`outCheck`の`nyanAllParams.nyan_output.body`は、本体の返却内容全体を表す文字列。JSONの`result`だけではない。`status`・`contentType`・`headers`なども渡すが、経路によって意味が異なる。検査用情報を書き換えて送信内容を加工する仕組みではない。

## 項目別の仕様と調査ケース

### 1. ファイルの相対パス

**判断：最上位の`api.json`を基準にするよう修正済み。**

対象は`nyanGetFile`、`nyanReadFileB64`、`nyanSendMailAttachment`、`nyanSendMail`の添付`path`。多段include先で定義したAPIでも、実行時の相対パスは最上位の`api.json`があるディレクトリから解決する。本体、前後チェック、`nyanCallMe`先、Push先で共通。絶対パスはそのまま扱う。

例：`/srv/app/api.json`が`/srv/app/child/api.json`を読み込み、そのAPIが`nyanGetFile("data/a.txt")`を実行した場合、参照先は`/srv/app/data/a.txt`。

設定JSON内の`script`・チェックファイル・include先のパス解決は今回の変更対象外。実行中は開始時に取得した設定の基準ディレクトリを維持する。

**調査ケース：** 子・孫のinclude、本体と前後チェック、内部呼び出し、Push、メール添付、絶対パス、未存在ファイル、実行途中の設定再読み込みを確認する。起動時の作業ディレクトリや、実行APIを定義した子JSONを基準にしていないかを記録する。

**Nyan8の検証参照：** `TestRuntimeFilePathsUseRootAPIConfigDirectory`、`TestRuntimeFilePathsNyanCallMeAndChecksUseRootConfig`、`TestRuntimeFilePathsSendMailAttachmentPath`、`TestRuntimeFilePathsPushUsesRootConfigAndPreservesSourceAPI`、`TestRuntimeFilePathsKeepCapturedRootDespiteReloadAndAPIChange`。

### 2. `nyanCallMe`のAPI名省略

**判断：API名の省略を廃止するよう修正済み。**

`nyanCallMe({api: "対象API"})`のように、空白だけではない文字列の`api`が必要。引数省略、`{}`、空文字列、空白のみ、文字列以外はJavaScript例外とし、暗黙の既定APIを呼ばない。未捕捉なら通常HTTPでは500になる。JavaScript側で`try`／`catch`することは可能。

**調査ケース：** 上記の不正入力ごとに例外・戻り値・HTTP結果を確認し、別のAPIが実行されていないことを副作用で確認する。正しい明示指定では通常どおり実行できることも確認する。

**Nyan8の検証参照：** `TestNyanCallMeRequiresExplicitAPI`。

### 3. `nyanCallMe`のチェック拒否

**判断：拒否結果を返す現行仕様を維持。**

呼び出し先の`paramCheck`・`outCheck`が有効な拒否結果を返した場合、その`{success, status, result}`を呼び出し元へ返す。拒否だけを理由に例外へ変換しない。`checkOnly`も成功・拒否ともチェック結果を返す。一方、チェック自身の実行エラー・形式不正は例外となる。関数が呼び出し元のHTTP応答を直接書き込むことはない。

**調査ケース：** 入力拒否、出力拒否、成功／拒否の`checkOnly`、チェック内の例外、形式不正を個別に実行する。結果の返却と例外を区別し、どこまで本体が実行されたかも確認する。

**既存の比較記録：** NyanPUIはチェック拒否を例外にする差があった。Nyan8をそれに合わせる判断はしていない。

**Nyan8の検証参照：** `TestNyanCallMeRunsChecksInOrder`、`TestNyanCallMeOutCheckPreservesResultFormats`。

### 4. `nyanHostExec`の失敗

**判断：コマンドの非0終了も結果として返すよう修正済み。**

戻り値は`success`・`exit_code`・`stdout`・`stderr`を持つオブジェクト。終了コード0なら`success: true`、非0なら`success: false`。非0終了だけではJavaScript例外にしない。標準エラー出力に文字列があっても終了コード0なら成功扱い。

引数未指定や実行用シェル自体を起動できない場合は例外。シェルが起動してから指定コマンドが見つからなかった場合は、シェルの非0終了結果を返す。

**調査ケース：** 終了コード0・1・7、両出力への書き込み、コマンド未発見、引数未指定、シェル起動失敗。オブジェクトかJSON文字列かも記録するが、まず失敗時に終了コードと出力を取得できるかを比較する。

**Nyan8の検証参照：** `TestNyanHostExecReturnsCommandResults`、`TestNyanHostExecInvocationErrorsRemainExceptions`。

### 5. `nyanGetAPI`の通信失敗

**判断：通信失敗を例外にする現行仕様を維持。**

URL不正・接続失敗・応答本文の読み取り失敗はJavaScript例外。HTTP 404・500でも本文を取得できれば本文を返す。正常な空の本文は空文字列を返す。HTTP上のエラーステータスと通信失敗は別の扱い。

**調査ケース：** 本文あり200、本文なし200、本文あり404・500、不正URL、接続拒否、本文の途中切断。空文字列が正常な空本文なのか、通信失敗を隠したものなのかを判別できるか確認する。

**既存の比較記録：** NyanPUIとの通信失敗時の差は維持する判断。NyanPUIを修正する指示はない。

**Nyan8の実装参照：** `setupGojaVMForAPI`の`nyanGetAPI`登録、`getAPI`。この判断ではコード変更・追加テストを行っていない。

### 6. `nyanGetItem`の未登録キーと共有範囲

**判断：共有サーバーメモリとして利用し、READMEへ説明を追記済み。未登録の戻り値は方針確認が残る。**

`nyanSetItem`／`nyanGetItem`は、同一Nyan8プロセス内の利用者・接続・API間で共有する文字列のkey-valueストレージ。同じキーへの保存は後から読む利用者や別APIにも見える。ファイルへは保存せず、プロセス再起動で失われる。

| 状態 | 現在のNyan8 | 既存調査時のNyanPUI |
|---|---|---|
| 未登録キー | `""` | `null` |
| 空文字列を保存済み | `""` | `""` |
| 通常の文字列を保存済み | 保存した文字列 | 保存した文字列 |

Nyan8では戻り値だけで「未登録」と「保存した空文字列」を区別できない。この差を解消する変更は行っていない。

**調査ケース：** 未登録、空文字列保存、通常値保存、上書き、別API・別接続・別利用者からの参照、プロセス再起動。共有単位がプロセス・接続・利用者のどれかを明記する。未登録戻り値の変更要否は、既に決定済みとは扱わない。

**Nyan8の実装参照：** `setupGojaVMForAPI`の`nyanSetItem`／`nyanGetItem`登録、共有変数`storage`。README「nyanGetItem / nyanSetItem」。

### 7. `nyanSetCookie`のHTTPS対応

**判断：Nyan8がHTTPSで受信した場合に`Secure`を付けるよう修正済み。**

判定は受信リクエストのTLS情報を使う。HTTP受信なら付けない。プロキシがHTTPSを終端してNyan8へHTTPで転送した場合、`X-Forwarded-Proto: https`や`Forwarded`があっても`Secure`は付かない。有効期間1時間、`Path=/`、`HttpOnly`は維持。

**調査ケース：** HTTP、直接HTTPS、HTTPS終端プロキシからのHTTP、転送ヘッダーだけを付けたHTTP。項目18の読み取り専用経路ではCookieを書き込まない点も併せて確認する。

**Nyan8の検証参照：** `TestNyanSetCookieSecureFollowsReceivedTLS`。

### 8. `nyanBase64Decode`の不正入力

**判断：不正入力で空文字列を返す現行仕様を維持。**

Nyan8ではOAuth専用環境の関数。標準Base64文字列をデコードし、不正なBase64入力は`""`を返す。正常な空文字列のデコード結果も`""`なので、両者は戻り値だけでは区別しない。Base64URLとは区別して調査する。

**調査ケース：** 正常な標準Base64、空文字列、不正文字や不正パディングを含む文字列。関数が通常API・OAuthのどちらで利用可能かも記録する。

**Nyan8の実装参照：** `setupOAuthGojaVM`の`nyanBase64Decode`登録。この判断ではコード変更・追加テストを行っていない。

### 9. `nyanRandomBase64URL`

**判断：NyanPUIに合わせ、既定32バイト・指定範囲1～1024バイトへ修正済み。**

暗号乱数を生成し、パディングなしのBase64URL文字列を返す。引数は出力文字数ではなく、エンコード前の乱数のバイト数。引数省略は32バイトで、出力は43文字。Nyan8ではOAuth専用環境の関数。

範囲外はJavaScript例外。明示的な`undefined`・`null`は省略とは異なり、整数変換後の範囲検査で拒否する。

**調査ケース：** 省略、1・32・1024、0・負数・1025、明示的な`undefined`・`null`。結果をBase64URLで復号してバイト数を確認し、`=`・`+`・`/`を含まないことを確認する。乱数の文字列そのものの一致は比較しない。

**Nyan8の検証参照：** `TestOAuthRandomBase64URLDefaultAndSizes`。

### 10. ルートHTTP `/?api=API名`のチェック

**判断：通常パスと同じ前後チェック・`checkOnly`を適用するよう修正済み。**

`paramCheck → 本体 → outCheck → 条件を満たす場合にPush → 応答`の順。入力拒否では本体以降、出力拒否ではPushを停止する。拒否はチェック結果をHTTPで返す。`checkOnly`は入力チェックのみで、入力チェック未設定時の成功結果も共通仕様に従う。

**調査ケース：** `/API名`と`/?api=API名`について、同じ入力・チェック設定で結果と実行順序を比較する。クエリ・JSON本文・フォーム本文、入力拒否・出力拒否・例外・ファイル欠落・`checkOnly`を含める。Pushの開始条件は項目16も参照する。

**Nyan8の検証参照：** `TestRootHTTPChecksMatchNamedEndpoint`。

### 11. MCP HTTP・stdioのチェック

**判断：両トランスポートで前後チェック・`checkOnly`を適用するよう修正済み。**

通常は`入力スキーマ検証 → paramCheck → 本体 → outCheck → 出力スキーマ検証`。`arguments.nyan_mode: "checkOnly"`では、入力スキーマ検証と入力チェックまでを実行する。本体用の出力スキーマをチェック結果へ適用しない。

拒否・`checkOnly`の結果はMCPの`structuredContent`と`content`のtextへ格納する。拒否は`isError: true`、成功した`checkOnly`は`isError: false`。チェック結果の`status`を、そのままMCP HTTP応答のステータスにはしない。チェック実行エラー・形式不正は詳細を含まないToolエラー。

入力スキーマは通常どおり適用するため、`additionalProperties: false`などを使う場合は`nyan_mode`を許可する必要がある。必須項目も省略できない。MCPは参照先API自身のPushを自動実行しない。ただし内部の`nyanCallMe`先のPushは実行対象。

**調査ケース：** HTTP・stdioそれぞれで通常、両チェックの拒否・例外、入力チェックあり／なしの`checkOnly`、入力スキーマ違反、出力スキーマ違反、サイズ制限を確認する。認証ありの`checkOnly`も認証を省略しないことを確認する。

**Nyan8の検証参照：** `TestMCPToolChecksAcrossHTTPAndStdio`、`TestMCPOutCheckUsesReturnedJSONMetadata`。リクエスト情報は項目18。

### 12. OAuthのチェック

**判断：HTTP処理・Go生成メタデータ・内部トークン検証へ前後チェックを追加済み。**

| 対象 | 通常実行の順序 |
|---|---|
| `authorize`・`token`・`register`・`adminUser` | 入力チェック → 本体スクリプト → 出力チェック → HTTP応答 |
| `authorizationServerMetadata`・`protectedResourceMetadata` | 入力チェック → Goによるメタデータ生成 → 出力チェック。参照先の本体スクリプトは実行しない |
| `verifyAccess` | 入力チェック → トークン検証本体 → 出力チェック → 認証判定 |

チェックと本体はそれぞれ新しいOAuth専用VMで動作し、各実行のタイムアウトは15秒。通常API用の全関数が使えるわけではなく、`javascript_include`も読み込まない。

HTTPの拒否はチェック結果をその`status`で返す。実行エラー・形式不正は詳細を含まない500。`checkOnly`は本体・メタデータ生成・出力チェックを行わないが、HTTPメソッド・入力形式などの検証は維持する。OPTIONSではチェックも本体も実行しない。

HTTPの出力チェックには、送信予定の本文・ステータス・Content-Type・検証済みの許可ヘッダーを渡す。拒否時には本体が返したCookie・Locationを送信しない。state保存など本体実行済みの副作用は取り消さない。チェック結果も1 MiBの本文上限の対象。

`verifyAccess`では認証判定全体を出力検査し、チェック拒否・エラーなら401／`invalid_token`としてToolを実行しない。Tool側の`checkOnly`は認証側へ引き継がず、認証を最後まで行う。メタデータもOAuthのレート・同時実行数制限の対象。

**調査ケース：** 上表の各処理で通常・入力拒否・出力拒否・例外・`checkOnly`を確認する。HTTP応答のCookie・リダイレクト抑止、検査用ヘッダーの書き換えが送信内容を変えないこと、チェック応答のサイズ制限、認証失敗時のTool非実行、認証付きToolの`checkOnly`を含める。

**Nyan8の検証参照：** `TestOAuthChecksHTTPRoutes`、`TestOAuthCheckOnlyBodyAndRequestValidation`、`TestOAuthChecksResponseBoundaries`、`TestOAuthVerifyAccessChecksBeforeMCPTool`。

### 13. WebSocket `checkOnly`の成功時の`result`

**判断：NyanPUIの成功時`result: null`を許容し、両製品とも修正不要。**

検討時のNyanPUIでは、入力チェックが成功時に独自の`result`を返しても、WebSocketの`checkOnly`応答では`null`になる。Nyan8は独自の`result`を保持する。確認したいのはチェックを通過するかどうかなので、この差は許容した。

**調査ケース：** 入力チェックが`{success: true, status: 200, result: {message: "ok"}}`を返す場合と拒否する場合を確認する。成功時の独自`result`保持だけを必須条件にしない。一方、本体・出力チェックを実行しないことと、通過／拒否が分かることは確認する。

**Nyan8の検証参照：** `TestWebSocketChecksMessageFlow`。項目13の判断自体では追加修正・追加テストを行っていない。

### 14. Pushの複数接続

**判断：同じAPIの全購読接続へ配信するよう修正済み。**

同じAPIにA・B・Cが接続していれば、配信開始時点の登録一覧に含まれる全接続へ同じ結果を送る。Push先の`paramCheck`・本体・`outCheck`はPushごとに各1回で、受信者数だけ繰り返さない。別APIの購読接続へは送らない。受信者別の認可チェックは実行しない。

HTTP・ルートHTTP・JSON-RPC・WebSocketを起点に適用する。接続していない間のPushを保存・再送する機能はない。

**調査ケース：** 同じAPIへの3接続と別APIへの1接続を作り、各起点からPushする。配信先、重複配信、チェック・本体の実行回数を確認する。入力／出力拒否時に誰にも配信しないことも確認する。

**Nyan8の検証参照：** `TestPushMultipleSubscribersAcrossTransports`、`TestPushTargetChecksAcrossTransports`。

### 15. Push接続の解除

**判断：終了・送信失敗した接続だけを除去するよう修正済み。**

1接続の終了を理由にAPI全体の登録を削除しない。Aが切断してもB・Cや後から追加した接続は維持する。送信失敗した接続は閉じて除去し、残りへの送信を続ける。最後の接続が終了したときにAPIの登録を削除する。

通常のWebSocket応答とPushの同時書き込みは接続ごとに直列化している。配信する接続一覧はコピーし、登録一覧のロックを保持したままネットワーク送信しない。

**調査ケース：** 最古・途中・最後の接続の切断、再接続直後の旧接続終了、先頭の配信先での送信失敗、登録と削除の並行実行。生きている接続の登録や配信が失われないことを確認する。

**Nyan8の検証参照：** `TestPushMultipleSubscribersAcrossTransports`、`TestPushContinuesAfterFailedSubscriber`、`TestPushConcurrentSubscriptionChanges`。

### 16. 呼び出し元の結果によるPushの実行条件

**判断：呼び出し元がエラー結果を返した場合はPushしないよう修正済み。**

Push開始条件は、呼び出し元の結果の`status`が200～399で、トップレベルの`success`が真偽値の`false`ではないこと。呼び出し元のチェックを通過していることも前提となる。

| 呼び出し元の結果 | Push開始 |
|---|---|
| `status: 200`、`success: true` | 可 |
| `status: 201`／`302`／`399`、`success`省略 | 可 |
| `status: 200`、`success: false` | 不可 |
| `status: 400`／`409`／`500`／`503` | 不可 |
| 例外・チェック拒否・`checkOnly` | 不可 |

WebSocket・JSON-RPCで`status`を省略した結果、およびWebSocketのプレーンテキストは判定上200。Push不可の場合、Push先のチェック・本体・配信をすべて実行しない。元の呼び出し元への応答を、この判定だけで変更することはない。

HTTP・WebSocketは本体のエラー結果も出力チェックする。JSON-RPCの本体が`success: false`を返した場合は従来どおりJSON-RPCエラー応答で終了し、出力チェック・Pushへ進まない。この経路差は現在も存在する。

**調査ケース：** 表の境界値を4起点で実行し、元の応答、Push先の実行回数、配信有無を確認する。`success: false`と`status`の組み合わせも確認する。

**適用範囲：** ここで追加した条件は「呼び出し元の結果」に対するもの。Push先自身の本体結果へ同じ条件を一律適用した修正ではない。Push先では前後チェックの通過・実行エラーの有無で配信を制御する。

**Nyan8の検証参照：** `TestPushSourceResultAcrossTransports`、実装`responseAllowsPush`。

### 17. 入力パラメータの優先順位

**判断：本文の値を優先するNyan8の現行仕様を維持。**

通常HTTPでは、URLクエリとJSON／フォーム本文に同名項目があると本文を優先する。通常パス・ルート経由とも対象。`nyan_mode`にも同じ優先順位を適用する。OAuth HTTPの`nyan_mode`も本文を優先する。

例：クエリが`value=query`、本文が`{"value":"body"}`なら、通常HTTPの`nyanAllParams.value`は`"body"`。ただし通常の名前付きAPIでは、対象API名は経路から決める。JSON-RPCは`params`を使用し、この通常HTTPのクエリ・本文統合と同じ扱いではない。

**調査ケース：** 同名項目あり／なしのJSON・フォーム、クエリと本文で異なる`nyan_mode`、通常パスとルート経由を確認する。通常の入力値の優先順位と、呼び出し対象APIの決定を分けて記録する。

**既存の比較記録：** NyanPUIのクエリ優先との差は許容し、NyanPUIを変更しない。項目17の判断ではNyan8のコード変更・追加テストを行っていない。

**Nyan8の参照：** READMEの`nyanAllParams`・チェック・OAuth入力説明。関連ケースは`TestRootHTTPChecksMatchNamedEndpoint`、`TestOAuthCheckOnlyBodyAndRequestValidation`。

### 18. JavaScriptへのリクエスト情報の引き継ぎ

**判断：WebSocket接続後・Push先・HTTP MCPでも取得関数を使えるよう修正済み。**

対象関数は`nyanGetCookie`・`nyanGetRemoteIP`・`nyanGetUserAgent`・`nyanGetRequestHeaders`。前後チェック・本体・そこからの`nyanCallMe`で同じ情報を参照できる。

| 実行経路 | 取得元 |
|---|---|
| 通常HTTP | そのHTTPリクエスト |
| WebSocketメッセージ | 接続を確立した時点のHTTPリクエスト |
| HTTP・ルートHTTP・JSON-RPC起点のPush | Pushを発生させた呼び出し元のHTTPリクエスト |
| WebSocket起点のPush | 呼び出し元WebSocketの接続時のHTTPリクエスト |
| HTTP MCP | 現在の`tools/call`のHTTPリクエスト |
| stdio・HTTPリクエストのない実行 | Cookie・IP・User-Agentは`""`、ヘッダーは`{}` |

Pushで使うのは送信元の情報であり、A・B・C各受信者の情報ではない。WebSocket接続後にブラウザー側のCookieを変更しても、既存接続の情報は更新されない。

サーバーが受け取ったリクエストをコピーして参照する。入力パラメータに`_headers`・`_remote_ip`・`_user_agent`などを指定しても取得関数の情報を上書きしない。IP取得は実際の接続情報を使用し、`X-Forwarded-For`などのヘッダーで上書きしない。サーバーが別途受け付けるPROXY protocolの処理とは区別する。

WebSocket接続後・Push先・HTTP MCPへ渡すコンテキストは読み取り専用。この経路の`nyanSetCookie`は何も変更せず、内部の`nyanCallMe`先にも同じ制約を引き継ぐ。通常HTTPとWebSocketの接続前チェックのCookie設定は従来の扱いを維持する。

**調査ケース：** 送信元と複数受信者へ異なるCookie・User-Agent・ヘッダーを設定し、各経路の入力チェック・本体・出力チェック・内部呼び出しから取得する。取得元の取り違え、偽装パラメータの影響、接続後のCookie変更、Cookie書き込みの無効化、HTTP MCP直後のstdioに情報が残らないことを確認する。

**Nyan8の検証参照：** `TestScriptReadOnlyRequestSnapshot`、`TestWebSocketAndPushRequestInformation`、`TestMCPRequestInformationHTTPAndStdio`。実装`readOnlyScriptRequestContext`。

## 固定番号以外にも引き継ぐ関連判断

18項目の検討前に実施した次の対応も、経路ごとの調査から漏らさない。新しい項目番号は付けない。

- **Push先のチェック：** 呼び出し元とは別にPush先の入力・出力チェックを実行する。拒否・エラーでは受信者へ拒否結果を送らず、呼び出し元の応答も置き換えない。Push先に`checkOnly`が渡された場合も本体・出力チェック・配信を実行しない。
- **WebSocketのチェック：** 接続前は入力チェック、接続後はメッセージごとの前後チェックを実行する。API未指定のルート接続では、メッセージの対象APIでチェックする。接続時の`checkOnly`ではWebSocketへ切り替えない。
- **Push先の入力：** `nyanAllParams.api`は呼び出し元のAPI名を維持するが、実行するチェック・本体はPush先の定義を使用する。同じ設定スナップショットを使用する。
- **配信形式：** HTTP・JSON-RPC起点のPushはtextフレーム、WebSocket起点は受信したtext／binaryの種別。WebSocket起点では既存の`Push: `接頭辞除去後の本文を出力チェックする。

関連検証：`TestPushTargetChecksAcrossTransports`、`TestPushCheckOnlyStopsBeforeMain`、`TestPushChecksKeepCapturedSnapshot`、`TestWebSocketHandshakeChecks`、`TestWebSocketChecksHandshakeThenEachMessage`、`TestWebSocketRootCheckOnlyDoesNotUpgrade`。

## NyanPUI・NyanQL調査時の記録方法

最初に対象リポジトリ・コミット・未コミット変更の有無・OS・起動方法を記録する。同名関数の有無だけでなく、登録されるJavaScript環境と呼び出し経路を調べる。OAuth専用、通常API専用などの提供範囲も比較対象とする。

各ケースでは戻り値の型・内容、例外、HTTPステータス、プロトコル上の応答、実行された処理、副作用、配信先を分けて記録する。チェックの実行順序はログだけで推測せず、可能ならテスト用の実行マーカーやカウンターでも確認する。認証情報は調査用の値を使用する。

調査結果の状態は「一致」「差あり」「対象外」「未調査」で記録する。「差あり」から直ちに修正へ進まず、今回許容した差なのか、別途判断が必要なのかを分類する。項目6の未登録戻り値は方針未確定として扱う。

以下は製品ごとに複製して使用できる記録欄。

| 番号 | 対象機能・経路の有無 | 観測した値・例外・副作用 | Nyan8との差／許容済みか | 根拠となる実装・再現手順 | 判定・次の判断 |
|---|---|---|---|---|---|
| 1 | 未調査 | | | | |
| 2 | 未調査 | | | | |
| 3 | 未調査 | | | | |
| 4 | 未調査 | | | | |
| 5 | 未調査 | | | | |
| 6 | 未調査 | | 未登録戻り値は方針確認あり | | |
| 7 | 未調査 | | | | |
| 8 | 未調査 | | | | |
| 9 | 未調査 | | | | |
| 10 | 未調査 | | | | |
| 11 | 未調査 | | | | |
| 12 | 未調査 | | | | |
| 13 | 未調査 | | 成功時の独自result喪失は許容済み | | |
| 14 | 未調査 | | | | |
| 15 | 未調査 | | | | |
| 16 | 未調査 | | | | |
| 17 | 未調査 | | NyanPUIとの優先順位の差は許容済み | | |
| 18 | 未調査 | | | | |

## 検証の記録と本資料の限界

実装対応時の記録では、Nyan8の`go test ./... -count=1`と関連する`go test -race`が通過している。項目18までの対応時に確認した競合検査の対象は次のとおり。

```sh
go test -race ./... -run '^(TestScriptReadOnlyRequestSnapshot|TestWebSocket|TestPush|TestMCP)' -count=1
```

初版作成では、既存の確認一覧・現在のREADME・関連実装・テスト名を照合した。資料のみの追加であり、コード変更やGoテストの再実行は行っていない。上記の通過記録はNyan8についてのもので、NyanPUI・NyanQLの検証済みを意味しない。これからの調査では、各製品の実装時点と再現結果を別に記録する。

追補のnyanCallMe Push対応ではコード・テストも更新し、全通常テストと`TestNyanCallMe`を含む関連race検査を再実行して通過した。初版の作成時点とは区別して扱う。
