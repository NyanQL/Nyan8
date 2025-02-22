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

## Windowsの場合
Nyan8_Win.exe をダブルクリックして起動します。

## Macの場合
Nyan8_Macをダブルクリックするか、ターミナルで以下のコマンドを実行して起動します。
```
./Nyan8_Mac
```

## Linuxの場合
ターミナルで以下のコマンドを実行して起動します。
```
./Nyan8_Linux_x64
```

# ライセンスについて
Nyan8はMITライセンスで提供されています。詳細は[LICENSE.md](LICENSE.md)を参照してください。

# Javascriptの書き方について
## console.log によるログ出力
実行中のjavascriptファイル内でログを出力する場合は、console.log を使用してください。
ターミナルにログが出力されます。

```javascript
console.log("ログ出力内容");
```

## Cookieを取得する例
```javascript
let cookie = getCookie("cookie_name");
```
## Cookieを設定する例
```javascript
setCookie("cookie_name", "cookie_value");
```
## localStorageを取得する例
```javascript
let value = getItem("key");
```
## localStorageを設定する例
```javascript
let value = setItem("key", "value");
```
## hostへのコマンドを実行し結果を取得する例
```javascript
let result = exec("実行する予定のコマンド");
result.Success; // 成功・失敗が boolean で戻ります
result.Stdout; // 成功した時の結果が格納されています
result.Stderr; // 失敗した時のエラーメッセージが格納されています
```


## 受信したパラメータについて
`allParams` に格納されています。
?id=12345の場合、allParams.id で 12345 を取得できます。

```javascript
console.log(allParams.id);
```

## Nyan8のjavascriptから外部のAPIへのリクエストについて
getとjson形式でのリクエストを用意しています。

getパラメータでの取得
```javascript
//apiのURL  apiURL
//basic認証のID  apiUser
//basic認証のパスワード apiPass
console.log(getAPI( apiURL,apiUser,apiPass));
```

jsonでの取得
```
//apiのURL  apiURL
//basic認証のID  apiUser
//basic認証のパスワード apiPass
const data = {
            api: "create_user",
            username: allParams.username,
            password: allParams.password,
            email: allParams.email,
            salt: saltKey,
            salt2: saltKey
        };
const result = jsonAPI(
        apiURL,
        JSON.stringify(data),
        apiUser,
        apiPass
    );

```

