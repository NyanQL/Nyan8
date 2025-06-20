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

## 4  ランタイムユーティリティ（JavaScript 側）

| 関数 | 概要 |
|------|------|
| `nyanAllParams` | GET/POST/JSON 受信パラメータをまとめたオブジェクト |
| `console.log()` | Go ログ or コンソールへ出力 |
| `nyanGetCookie` / `nyanSetCookie` | Cookie 操作 |
| `nyanGetItem` / `nyanSetItem` | メモリ内 key‑value ストレージ |
| `nyanGetAPI` | HTTP GET ｜
| `nyanJsonAPI` | HTTP POST（JSON） |
| `nyanHostExec` | ホスト OS でシェル実行し結果取得 |
| `nyanGetFile` | サーバー上のファイルを読み込み |
| **`nyanSendMail`** | メール送信（添付可）|
| **`nyanFileToBase64`** | ファイル → Base64 変換 |

### 4‑1  パラメータ取得
```javascript
console.log(nyanAllParams);
```

### 4‑2  外部 API 例（POST JSON）

```javascript
// (1) ヘッダー無し – 必須 4 引数
var res = nyanJsonAPI(
  "https://example.com/api",
  JSON.stringify({ key: "value" }),
  "id",
  "pass"
);
var obj = JSON.parse(res);

// (2) ヘッダー付き – 5 番目の引数にオブジェクト or JSON 文字列
var headers = {
  "X-Custom-Token": "abcd1234",
  "Content-Language": "ja"
};

// オブジェクトをそのまま渡す
var res2 = nyanJsonAPI(
  "https://example.com/api",
  JSON.stringify({ foo: "bar" }),
  "id",
  "pass",
  headers
);

// JSON 文字列で渡すことも可能
var res3 = nyanJsonAPI(
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

## 5  メール送信 & Base64 ユーティリティ

> **詳細な使い方・トラブルシューティングは** [「メール送信 & Base64 ユーティリティ利用ガイド」](#メール送信--base64-ユーティリティ利用ガイド) を参照してください。

### 5‑1  最小例
```javascript
nyanSendMail({
  to: ["user@example.com"],
  subject: "テスト",
  body: "hello",
  html: false
});
```

### 5‑2  ファイル添付（Base64 変換ユーティリティ利用）
```javascript
var png = nyanFileToBase64("./image/cat.png");
nyanSendMail({
  to: ["dest@example.com"],
  subject: "画像",
  body: "<p>ネコです</p>",
  html: true,
  attachments: [{
    filename: "cat.png",
    contentType: png.contentType,
    dataBase64: png.base64
  }]
});
```

---

## 6  JSON‑RPC 2.0 エンドポイント

`POST /nyan-rpc` に以下の形式でリクエストを送信します。

```json
{
  "jsonrpc": "2.0",
  "method": "add",
  "params": { "addNumber": 5 },
  "id": 1
}
```

※ Batch（配列リクエスト）は現在未実装です。

---

## 7  API サーバー情報取得

* **サーバー全体** : `GET /nyan`
* **個別 API**    : `GET /nyan/<API 名>`

---

## 8  予約語

`api`, `nyan` で始まるキー・パスは **予約語** として扱われます。パラメータ名や API 名に使用しないでください。

---

## 9  ライセンス

本プロジェクトは **MIT License** で配布されています。詳細は [`LICENSE.md`](LICENSE.md) をご覧ください。

---

# メール送信 & Base64 ユーティリティ利用ガイド

*(以下、既存のガイドを再掲しています。必要に応じて省略・別ファイル化してください)*

