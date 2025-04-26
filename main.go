package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/dop251/goja"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/natefinch/lumberjack"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"crypto/tls"
	"mime"
	"net/smtp"
	"reflect"

)

// ResponseData はAPIのレスポンスデータを表します。
type ResponseData struct {
	Success bool        `json:"success"`
	Error   *ErrorData  `json:"error,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// ErrorData はエラーデータを表します。
type ErrorData struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Config は設定データを表します。
type Config struct {
	Name              string    `json:"name"`
	Profile           string    `json:"profile"`
	Version           string    `json:"version"`
	Port              int       `json:"Port"`
	CertFile          string    `json:"certPath"`
	KeyFile           string    `json:"keyPath"`
	JavaScriptInclude []string  `json:"javascript_include"`
	Log               LogConfig `json:"log"`
	SMTP SMTPConfig `json:"smtp"`
}

// LogConfig はログ設定データを表します。
type LogConfig struct {
	Filename      string `json:"Filename"`
	MaxSize       int    `json:"MaxSize"`
	MaxBackups    int    `json:"MaxBackups"`
	MaxAge        int    `json:"MaxAge"`
	Compress      bool   `json:"Compress"`
	EnableLogging bool   `json:"EnableLogging"`
}

type NyanResponse struct {
	Nyan map[string]interface{} `json:"nyan"`
	Apis map[string]interface{} `json:"apis"`
}

type ExecResult struct {
	Success  bool   `json:"success"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type JSONRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	Result  interface{}      `json:"result,omitempty"`
	Error   *JSONRPCError    `json:"error,omitempty"`
	ID      interface{}      `json:"id,omitempty"`
}

type JSONRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params"`
	ID      interface{}            `json:"id"`
}

type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type SMTPConfig struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	FromEmail string `json:"from_email"`
	FromName  string `json:"from_name"`
	TLS       bool   `json:"tls"`
	DefaultBCC []string `json:"default_bcc"`
}


// config格納場所
var globalConfig Config

// ストレージ
var storage sync.Map

// WebSocketアップグレーダー
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

var ginContext *gin.Context

var logger *log.Logger

var pushConnections sync.Map

// main はメイン関数です。
func main() {
	// 実行ファイルのディレクトリを取得
	execPath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		log.Fatal("Error getting executable path:", err)
	}

	// 一時ディレクトリを除外してカレントディレクトリを使用
	if isTemporaryDirectory(execPath) {
		execPath, err = os.Getwd()
		if err != nil {
			log.Fatal("Error getting current working directory:", err)
		}
	}
	execDir := execPath
	fmt.Println("Executable directory:", execDir)

	// 環境変数から設定ファイルのパスを取得する
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = filepath.Join(execDir, "config.json")
	}
	fmt.Println("Config file path:", configPath)

	config, err := loadConfig(configPath)
	if err != nil {
		logger.Fatalf("Error loading config from %s: %v", configPath, err)
	}
	globalConfig = config

	// ロガーをセットアップ
	initLogger(execDir)

	r := gin.Default()
	r.SetTrustedProxies(nil) // 信頼するプロキシの設定を解除
	r.Use(CORSMiddleware())
	r.Use(RecoveryMiddleware())

	// 静的なルート（favicon.ico）
	r.NoRoute(func(c *gin.Context) {
		if c.Request.URL.Path == "/favicon.ico" {
			c.Status(http.StatusNoContent)
			return
		}
		// その他のリクエストの場合は、動的エンドポイントとして処理
		// ※もしルート "/" のハンドリングも必要なら、別途設定
		respondWithError(c, http.StatusNotFound, "Endpoint not found", nil)
	})

	r.POST("/nyan-rpc", handleJSONRPC)
	r.Any("/nyan", handleNyan)
	r.Any("/nyan/:apiName", handleNyanDetail)
	r.Any("/", handleRequest) // HTTPとWebSocketリクエストを同じエンドポイントで処理

	// 動的エンドポイントの登録
	execDir, _ = os.Getwd() // または、実行ファイルのディレクトリを使用
	if err := registerDynamicEndpoints(r, execDir); err != nil {
		logger.Fatalf("Failed to register dynamic endpoints: %v", err)
	}

	// HTTPSサーバーを起動するかどうかを判断
	certFilePath := filepath.Join(execDir, config.CertFile)
	keyFilePath := filepath.Join(execDir, config.KeyFile)

	if config.CertFile != "" && config.KeyFile != "" {
		// HTTPSサーバーの起動
		logger.Printf("Starting HTTPS server at %d", config.Port)
		server := &http.Server{
			Addr:    fmt.Sprintf(":%d", config.Port),
			Handler: h2c.NewHandler(r, &http2.Server{}), // h2cハンドラを使用してHTTP/2を有効化
		}
		err = server.ListenAndServeTLS(certFilePath, keyFilePath)
		if err != nil {
			logger.Fatalf("Failed to start HTTPS server: %v", err)
		}
	} else {
		// 通常のHTTPサーバーの起動
		logger.Printf("Starting HTTP server at %d", config.Port)
		server := &http.Server{
			Addr:    fmt.Sprintf(":%d", config.Port),
			Handler: h2c.NewHandler(r, &http2.Server{}), // h2cハンドラを使用してHTTP/2を有効化
		}
		err = server.ListenAndServe()
		if err != nil {
			logger.Fatalf("Failed to start HTTP server: %v", err)
		}
	}
}

// isTemporaryDirectory はディレクトリが一時ディレクトリかどうかを判定します
func isTemporaryDirectory(path string) bool {
	tempDir := os.TempDir()
	return filepath.HasPrefix(path, tempDir)
}

// loadConfig は設定ファイルを読み込みます。
func loadConfig(filename string) (Config, error) {
	var config Config

	// 設定ファイルの存在を確認
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return config, fmt.Errorf("config file does not exist: %s", filename)
	}

	// 設定ファイルを読み込む
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return config, err
	}

	// 設定ファイルの内容をConfig構造体にパースする
	if err := json.Unmarshal(data, &config); err != nil {
		return config, err
	}
	return config, nil
}

// handleRequest はHTTPとWebSocketリクエストを処理します。
func handleRequest(c *gin.Context) {
	if c.Query("api") == "nyan" || c.Request.URL.Path == "/nyan" {
		handleNyan(c)
		return
	}
	if websocket.IsWebSocketUpgrade(c.Request) {
		handleWebSocket(c)
	} else {
		handleAPIRequest(c)
	}
}

// handleAPIRequest はAPIリクエストを処理します。
func handleAPIRequest(c *gin.Context) {
	// 実行ファイルのディレクトリを取得
	fmt.Print(" handleAPIRequest 直後")
	fmt.Print(c.Request.URL.Path)
	execPath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		logger.Fatalf("Error getting executable path: %v", err)
	}

	// 一時ディレクトリを除外してカレントディレクトリを使用
	if isTemporaryDirectory(execPath) {
		execPath, err = os.Getwd()
		if err != nil {
			logger.Fatalf("Error getting current working directory: %v", err)
		}
	}
	execDir := execPath

	ginContext = c
	defer func() {
		ginContext = nil
	}()

	// スクリプトリストの取り込み
	apiJsonPath := filepath.Join(execDir, "api.json")
	scriptListData, err := loadJSONFile(apiJsonPath)
	if err != nil {
		logger.Fatalf("Error reading user JSON file: %v", err)
	}

	// 全てのパラメータをマージ
	allParams := make(map[string]interface{})
	allParams["api"] = c.Request.URL.Path[1:]

	// POSTの場合、フォームデータをパースする
	if c.Request.Method == http.MethodPost && strings.HasPrefix(c.ContentType(), "application/x-www-form-urlencoded") {
		if err := c.Request.ParseForm(); err != nil {
			respondWithError(c, http.StatusBadRequest, "Failed to parse form data", err)
			return
		}
	}

	// GETの場合はクエリパラメータでOK
	queryParams := make(map[string]interface{})
	for key, values := range c.Request.URL.Query() {
		queryParams[key] = values[0]
	}

	// POSTフォームの場合
	postFormParams := make(map[string]interface{})
	if c.Request.Method == http.MethodPost {
		for key, values := range c.Request.PostForm {
			postFormParams[key] = values[0]
		}
	}

	// JSONの場合
	jsonBodyParams := make(map[string]interface{})
	if c.ContentType() == "application/json" {
		var requestData map[string]interface{}
		if err := c.BindJSON(&requestData); err != nil {
			respondWithError(c, http.StatusBadRequest, "Invalid JSON data", err)
			return
		}
		jsonBodyParams = requestData
	}

	// 全てのパラメータをマージする
	for key, value := range queryParams {
		allParams[key] = value
	}
	for key, value := range postFormParams {
		allParams[key] = value
	}
	for key, value := range jsonBodyParams {
		allParams[key] = value
	}

	logger.Print(postFormParams)
	logger.Print(allParams)
	// スクリプトの値を取得
	scriptValue := allParams["api"]
	scriptValueKey, ok := scriptValue.(string)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Script value is not a string"})
		return
	}

	// スクリプト情報を取得
	scriptInfo, ok := scriptListData[scriptValueKey].(map[string]interface{})
	if !ok {
		logger.Print(scriptValueKey)
		logger.Printf("Script info not found for script key: %s", scriptValueKey)
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Script info not found for script key: %s", scriptValueKey)})
		return
	}
	logger.Print(scriptInfo)

	// スクリプトのパスを取得
	scriptPath, ok := scriptInfo["script"].(string)
	if !ok {
		logger.Printf("Script path not found in script info for key: %s", scriptValueKey)
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Script path not found in script info for key: %s", scriptValueKey)})
		return
	}

	// 絶対パスに変換
	scriptPath = filepath.Join(execDir, scriptPath)

	// JavaScriptを実行し、結果を取得
	result, err := runJavaScript(scriptPath, allParams, c)
	if err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to run JavaScript", err)
		return
	}

	var jsonData map[string]interface{}
	if err := json.Unmarshal([]byte(result), &jsonData); err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to parse JavaScript response", err)
		return
	}

	status, ok := jsonData["status"].(float64)
	if !ok {
		respondWithError(c, http.StatusInternalServerError, "Status field not found in JavaScript response", nil)
		return
	}

	// HTTP リクエストから push を発生させる処理
	performPush(scriptInfo, scriptListData, allParams, execDir)

	c.JSON(int(status), jsonData)
}

// handleWebSocket はWebSocketリクエストを処理します。
func handleWebSocket(c *gin.Context) {
	// WebSocket 接続をアップグレード
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Printf("Failed to upgrade WebSocket: %v", err)
		return
	}
	// 接続終了時に登録を解除
	defer conn.Close()

	// API 名の取得（ルートパラメータがなければ URL から取得）
	apiName := c.Param("api")
	if apiName == "" {
		apiName = c.Request.URL.Path[1:]
	}
	// push受信用にこの接続を登録
	pushConnections.Store(apiName, conn)
	defer pushConnections.Delete(apiName)

	// 実行ファイルのディレクトリ取得
	execPath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		logger.Fatalf("Error getting executable path: %v", err)
	}
	if isTemporaryDirectory(execPath) {
		execPath, err = os.Getwd()
		if err != nil {
			logger.Fatalf("Error getting current working directory: %v", err)
		}
	}
	execDir := execPath

	for {
		// WebSocket からメッセージを読み取る
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			logger.Printf("WebSocket read error: %v", err)
			break
		}

		// 受信メッセージをJSONとしてパース
		var receivedData map[string]interface{}
		if err := json.Unmarshal(message, &receivedData); err != nil {
			logger.Printf("Invalid JSON data: %v", err)
			sendErrorMessage(conn, "Invalid JSON data")
			continue
		}

		// "api" キーからメインAPIの識別子を取得
		scriptValue, ok := receivedData["api"].(string)
		if !ok {
			logger.Printf("Script value is not a string")
			sendErrorMessage(conn, "Invalid script value")
			continue
		}

		// api.json を読み込む
		apiJsonPath := filepath.Join(execDir, "api.json")
		scriptListData, err := loadJSONFile(apiJsonPath)
		if err != nil {
			logger.Printf("Error reading api.json file: %v", err)
			sendErrorMessage(conn, "Error reading API configuration")
			continue
		}

		// メインAPIの設定を取得
		scriptInfo, ok := scriptListData[scriptValue].(map[string]interface{})
		if !ok {
			logger.Printf("Script info not found for key: %s", scriptValue)
			sendErrorMessage(conn, "Script info not found")
			continue
		}

		scriptPath, ok := scriptInfo["script"].(string)
		if !ok {
			logger.Printf("Script path not found for key: %s", scriptValue)
			sendErrorMessage(conn, "Script path not found")
			continue
		}

		// メインAPIのスクリプトの絶対パス作成
		javascriptPath := filepath.Join(execDir, scriptPath)

		// WebSocket 用なので gin.Context は nil を渡す
		result, err := runJavaScript(javascriptPath, receivedData, nil)
		if err != nil {
			logger.Printf("Failed to run JavaScript: %v", err)
			sendErrorMessage(conn, "Failed to run JavaScript")
			continue
		}

		// メインAPIの結果をクライアントへ送信
		if err := conn.WriteMessage(messageType, []byte(result)); err != nil {
			logger.Printf("Failed to send message to WebSocket: %v", err)
			break
		}

		// push 項目が設定されている場合、push 対象APIの処理を実行
		if pushTargetRaw, exists := scriptInfo["push"]; exists {
			if pushTarget, ok := pushTargetRaw.(string); ok && pushTarget != "" {
				// push 対象APIの設定を取得
				if pushConfigRaw, exists := scriptListData[pushTarget]; exists {
					if pushConfig, ok := pushConfigRaw.(map[string]interface{}); ok {
						pushScript, ok := pushConfig["script"].(string)
						if ok && pushScript != "" {
							pushScriptPath := filepath.Join(execDir, pushScript)
							// push API を実行
							pushResult, err := runJavaScript(pushScriptPath, receivedData, nil)
							if err != nil {
								logger.Printf("Push API execution failed for key %s: %v", pushTarget, err)
							} else {
								// 先頭の "Push: " を取り除く
								pushResult = strings.TrimPrefix(pushResult, "Push: ")
								// push対象のWebSocket接続があれば、push結果を送信
								if pushConnRaw, ok := pushConnections.Load(pushTarget); ok {
									if pushConn, ok := pushConnRaw.(*websocket.Conn); ok {
										if err := pushConn.WriteMessage(messageType, []byte(pushResult)); err != nil {
											logger.Printf("Failed to push message to %s: %v", pushTarget, err)
										} else {
											logger.Printf("Push message sent successfully to %s", pushTarget)
										}
									} else {
										logger.Printf("pushConnections entry for %s is not *websocket.Conn", pushTarget)
									}
								} else {
									logger.Printf("No WebSocket connection registered for push target: %s", pushTarget)
								}
							}
						} else {
							logger.Printf("Push script not found for key: %s", pushTarget)
						}
					}
				} else {
					logger.Printf("API config not found for push target: %s", pushTarget)
				}
			}
		}
	}
}

// エラーレスポンスの送信
func sendErrorMessage(conn *websocket.Conn, message string) {
	errMessage := map[string]interface{}{
		"error": message,
	}
	jsonMessage, _ := json.Marshal(errMessage)
	conn.WriteMessage(websocket.TextMessage, jsonMessage)
}

// runJavaScript はJavaScriptを実行します。
// runJavaScript は、指定された JavaScript コードを goja で実行します。
func runJavaScript(scriptPath string, allParams map[string]interface{}, ginCtx *gin.Context) (string, error) {
	// 新たな goja の VM を生成
	vm := goja.New()
	// 必要なグローバル関数等を登録する
	setupGojaVM(vm, ginCtx)

	// globalConfig.JavaScriptInclude にある各ファイルを読み込み、連結する
	var jsCode string
	for _, includePath := range globalConfig.JavaScriptInclude {
		includePath = filepath.Join(filepath.Dir(os.Args[0]), includePath)
		code, err := ioutil.ReadFile(includePath)
		if err != nil {
			return "", fmt.Errorf("failed to read included JS file %s: %v", includePath, err)
		}
		jsCode += string(code) + "\n"
	}

	// メインスクリプトを読み込む
	mainCode, err := ioutil.ReadFile(scriptPath)
	if err != nil {
		return "", fmt.Errorf("failed to read main script file %s: %v", scriptPath, err)
	}
	jsCode += string(mainCode)

	// allParams を JSON 化して、グローバル変数 allParams としてセットする
	allParamsJSON, err := json.Marshal(allParams)
	if err != nil {
		return "", err
	}
	_, err = vm.RunString(fmt.Sprintf("let nyanAllParams = %s;", string(allParamsJSON)))
	if err != nil {
		return "", err
	}

	// 連結した JavaScript コードを実行
	value, err := vm.RunString(jsCode)
	if err != nil {
		return "", err
	}

	return value.String(), nil
}

// loadJSONFile はJSONファイルを読み込みます。
func loadJSONFile(filePath string) (map[string]interface{}, error) {
	var jsonData map[string]interface{}

	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, &jsonData); err != nil {
		return nil, err
	}

	return jsonData, nil
}

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}

func getAPI(url, username, password string) (string, error) {
	// HTTPクライアントの生成
	client := &http.Client{}

	// リクエストの生成
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	// BASIC認証ヘッダーの設定
	if username != "" {
		req.SetBasicAuth(username, password)
	}

	// リクエストの送信
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	// レスポンスの読み取り
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}
	return string(body), nil
}

// POSTリクエストを行うGo関数
func jsonAPI(url string, jsonData []byte, username, password string, headers map[string]string) (string, error) {
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	// BASIC認証のセットアップ（usernameが空でなければ）
	if username != "" {
		basicAuth := username + ":" + password
		basicAuthEncoded := base64.StdEncoding.EncodeToString([]byte(basicAuth))
		req.Header.Set("Authorization", "Basic "+basicAuthEncoded)
	}

	req.Header.Set("Content-Type", "application/json")

	// 追加のヘッダーが指定されていれば設定（複数指定可能）
	if headers != nil {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// loggerの初期化
func initLogger(execDir string) {
	logFilePath := filepath.Join(execDir, globalConfig.Log.Filename)
	if globalConfig.Log.EnableLogging {
		// EnableLogging が true の場合はファイル出力
		logger = log.New(&lumberjack.Logger{
			Filename:   logFilePath,
			MaxSize:    globalConfig.Log.MaxSize,
			MaxBackups: globalConfig.Log.MaxBackups,
			MaxAge:     globalConfig.Log.MaxAge,
			Compress:   globalConfig.Log.Compress,
		}, "", log.LstdFlags)
	} else {
		// EnableLogging が false の場合はコンソール出力
		logger = log.New(os.Stdout, "", log.LstdFlags)
	}
}

// エラーレスポンス
func respondWithError(c *gin.Context, status int, errMsg string, err error) {
	if err != nil {
		logger.Printf("ERROR: %s - %v", errMsg, err)
	} else {
		logger.Printf("ERROR: %s", errMsg)
	}
	c.JSON(status, gin.H{"error": errMsg})
}

// リカバリーミドルウェア
func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Printf("Panic recovered: %v", rec)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
			}
		}()
		c.Next()
	}
}

// registerDynamicEndpoints は api.json の内容に基づいてルート直下のエンドポイントを登録する関数です。
func registerDynamicEndpoints(r *gin.Engine, execDir string) error {
	// api.json を読み込み（例: map[string]interface{} とする）
	apiConf, err := loadJSONFile(filepath.Join(execDir, "api.json"))
	if err != nil {
		return fmt.Errorf("failed to load api.json: %v", err)
	}

	// api.json の各キーをエンドポイント名とする
	for apiName := range apiConf {
		currentAPIName := apiName // ローカル変数にコピー（クロージャ用）
		// ルーティング登録。currentAPIName の値をクロージャでキャプチャ
		r.Any("/"+currentAPIName, func(c *gin.Context) {
			// WebSocket のアップグレードリクエストかどうかをチェック
			if websocket.IsWebSocketUpgrade(c.Request) {
				// WebSocket 用のハンドラーに処理を委譲する
				handleWebSocket(c)
				return
			}
			// URLからエンドポイント名を取得。ここでは、リクエストされたパス（"/foo" 等）から先頭の "/" を除いたものを使用
			endpoint := c.FullPath()[1:]
			if endpoint == "" {
				endpoint = currentAPIName
			}
			// リクエストパラメータのマージ
			allParams := make(map[string]interface{})
			// URLクエリパラメータの収集
			for key, values := range c.Request.URL.Query() {
				allParams[key] = values[0]
			}
			// POSTフォームの収集
			for key, values := range c.Request.PostForm {
				allParams[key] = values[0]
			}
			// JSON ボディがあれば収集
			if c.ContentType() == "application/json" {
				var jsonBody map[string]interface{}
				if err := c.BindJSON(&jsonBody); err == nil {
					for key, value := range jsonBody {
						allParams[key] = value
					}
				} else {
					respondWithError(c, http.StatusBadRequest, "Invalid JSON data", err)
					return
				}
			}
			// エンドポイント名を "api" パラメータとしてセット
			allParams["api"] = endpoint

			// api.json から対象のスクリプト情報を取得
			scriptListData, err := loadJSONFile(filepath.Join(execDir, "api.json"))
			if err != nil {
				respondWithError(c, http.StatusInternalServerError, "Failed to load API configuration", err)
				return
			}
			scriptInfo, ok := scriptListData[endpoint].(map[string]interface{})
			if !ok {
				respondWithError(c, http.StatusBadRequest, fmt.Sprintf("API config not found for key: %s", endpoint), nil)
				return
			}
			scriptPath, ok := scriptInfo["script"].(string)
			if !ok {
				respondWithError(c, http.StatusBadRequest, fmt.Sprintf("Script path not found for key: %s", endpoint), nil)
				return
			}
			// 絶対パスに変換
			scriptPath = filepath.Join(execDir, scriptPath)
			// JavaScript を実行
			result, err := runJavaScript(scriptPath, allParams, c)
			if err != nil {
				respondWithError(c, http.StatusInternalServerError, "Failed to run JavaScript", err)
				return
			}
			// 結果を JSON としてレスポンス用にパース
			var jsonData map[string]interface{}
			if err := json.Unmarshal([]byte(result), &jsonData); err != nil {
				respondWithError(c, http.StatusInternalServerError, "Failed to parse JavaScript response", err)
				return
			}
			status, ok := jsonData["status"].(float64)
			if !ok {
				respondWithError(c, http.StatusInternalServerError, "Status field not found in JavaScript response", nil)
				return
			}

			performPush(scriptInfo, scriptListData, allParams, execDir)

			c.JSON(int(status), jsonData)
		})
	}
	return nil
}

// performPush は、API 設定とパラメータを元に push 対象の WebSocket 接続へメッセージを送信します。
func performPush(scriptInfo map[string]interface{}, scriptListData map[string]interface{}, allParams map[string]interface{}, execDir string) {
	if pushTargetRaw, exists := scriptInfo["push"]; exists {
		logger.Printf("Push target specified: %v", pushTargetRaw)
		if pushTarget, ok := pushTargetRaw.(string); ok && pushTarget != "" {
			// push 対象の設定を取得
			if pushConfigRaw, exists := scriptListData[pushTarget]; exists {
				if pushConfig, ok := pushConfigRaw.(map[string]interface{}); ok {
					pushScript, ok := pushConfig["script"].(string)
					if ok && pushScript != "" {
						pushScriptPath := filepath.Join(execDir, pushScript)
						// push 対象の API のスクリプトを実行
						pushResult, err := runJavaScript(pushScriptPath, allParams, nil)
						if err != nil {
							logger.Printf("Push API execution failed for key %s: %v", pushTarget, err)
						} else {
							logger.Printf("Push API execution succeeded for key %s, result: %s", pushTarget, pushResult)
							// pushConnections から対象の WebSocket 接続を取得し、pushResult を送信
							if pushConnRaw, ok := pushConnections.Load(pushTarget); ok {
								if pushConn, ok := pushConnRaw.(*websocket.Conn); ok {
									pushMessage := []byte(pushResult)

									logger.Printf("Sending push message: %s", string(pushMessage))
									if err := pushConn.WriteMessage(websocket.TextMessage, pushMessage); err != nil {
										logger.Printf("Failed to push message to %s: %v", pushTarget, err)
									} else {
										logger.Printf("Push message sent successfully to %s", pushTarget)
									}
								} else {
									logger.Printf("pushConnections entry for %s is not *websocket.Conn", pushTarget)
								}
							} else {
								logger.Printf("No WebSocket connection registered for push target: %s", pushTarget)
							}
						}
					} else {
						logger.Printf("Push script not found for key: %s", pushTarget)
					}
				}
			} else {
				logger.Printf("API config not found for push target: %s", pushTarget)
			}
		}
	}
}

// handleNyan は /nyan エンドポイントを処理します。
func handleNyan(c *gin.Context) {
	// 作業ディレクトリの取得
	execDir, err := os.Getwd()
	if err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to get working directory", err)
		return
	}

	// api.json を読み込み
	apiJsonPath := filepath.Join(execDir, "api.json")
	apiConf, err := loadJSONFile(apiJsonPath)
	if err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to load API configuration", err)
		return
	}

	// 各API設定から "script" キーを削除する（スクリプトパスは見せない）
	for key, api := range apiConf {
		if apiMap, ok := api.(map[string]interface{}); ok {
			delete(apiMap, "script")
			apiConf[key] = apiMap
		}
	}

	// config.json の値は globalConfig に保持されている想定
	nyanInfo := map[string]interface{}{
		"name":    globalConfig.Name,
		"profile": globalConfig.Profile,
		"version": globalConfig.Version,
	}

	response := NyanResponse{
		Nyan: nyanInfo,
		Apis: apiConf,
	}
	c.JSON(http.StatusOK, response)
}

// /nyan/:apiName で特定APIの詳細を返す
func handleNyanDetail(c *gin.Context) {
	// パスパラメータの取得
	apiName := c.Param("apiName")
	if apiName == "" {
		respondWithError(c, http.StatusBadRequest, "No apiName provided", nil)
		return
	}

	// カレントディレクトリ(または実行ディレクトリ)取得
	execDir, err := os.Getwd()
	if err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to get working directory", err)
		return
	}

	// api.json を読み込み
	apiJsonPath := filepath.Join(execDir, "api.json")
	apiConf, err := loadJSONFile(apiJsonPath)
	if err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to load API configuration", err)
		return
	}

	// 指定の API があるか確認
	apiDataRaw, exists := apiConf[apiName]
	if !exists {
		respondWithError(c, http.StatusNotFound, fmt.Sprintf("API not found: %s", apiName), nil)
		return
	}

	// apiDataを map[string]interface{} として扱う
	apiData, ok := apiDataRaw.(map[string]interface{})
	if !ok {
		respondWithError(c, http.StatusInternalServerError, "Invalid API data format in api.json", nil)
		return
	}

	// api.json に記載された description を取得（なければ空文字）
	description, _ := apiData["description"].(string)

	// JavaScriptのパスを取得（なければ空文字のまま）
	scriptPath, _ := apiData["script"].(string)
	nyanAcceptedParams := map[string]interface{}{}
	nyanOutputColumns := []interface{}{}

	if scriptPath != "" {
		fullScriptPath := filepath.Join(execDir, scriptPath)
		scriptContent, err := ioutil.ReadFile(fullScriptPath)
		if err == nil {
			// スクリプト内から const nyanAcceptedParams, nyanOutputColumns をパース
			nyanAcceptedParams = parseConstObject(scriptContent, "nyanAcceptedParams")
			nyanOutputColumns = parseConstArray(scriptContent, "nyanOutputColumns")
		}
	}

	// 結果JSONを作成
	result := map[string]interface{}{
		"api":               apiName,
		"description":       description,
		"nyanAcceptedParams": nyanAcceptedParams, // スクリプトに無ければ空のまま
		"nyanOutputColumns":  nyanOutputColumns,  // スクリプトに無ければ空のまま
	}

	c.JSON(http.StatusOK, result)
}

// parseConstObject は、scriptContent から「const XXX = {...};」形式のオブジェクトを抜き出してパースします
func parseConstObject(scriptContent []byte, constName string) map[string]interface{} {
	re := regexp.MustCompile(fmt.Sprintf(`(?m)const\s+%s\s*=\s*(\{[^;]*\});`, constName))
	matches := re.FindSubmatch(scriptContent)
	if len(matches) < 2 {
		// 見つからなければ空オブジェクト
		return map[string]interface{}{}
	}

	// matches[1] に { ... } の部分が入る想定
	jsonStr := matches[1]
	// 末尾のセミコロン(;)があれば除去（正規表現で「};」まで取れてる場合を想定）
	jsonStr = bytes.TrimSuffix(jsonStr, []byte(";"))

	var result map[string]interface{}
	if err := json.Unmarshal(jsonStr, &result); err != nil {
		return map[string]interface{}{} // パースできなければ空
	}
	return result
}

// parseConstArray は、scriptContent から「const XXX = [...];」形式の配列を抜き出してパースします
func parseConstArray(scriptContent []byte, constName string) []interface{} {
	re := regexp.MustCompile(fmt.Sprintf(`(?m)const\s+%s\s*=\s*(\[[^;]*\]);`, constName))
	matches := re.FindSubmatch(scriptContent)
	if len(matches) < 2 {
		// 見つからなければ空配列
		return []interface{}{}
	}

	// matches[1] に [ ... ] の部分が入る想定
	jsonStr := matches[1]
	// 末尾のセミコロン(;)があれば除去
	jsonStr = bytes.TrimSuffix(jsonStr, []byte(";"))

	var result []interface{}
	if err := json.Unmarshal(jsonStr, &result); err != nil {
		return []interface{}{} // パースできなければ空
	}
	return result
}

// gojaのVMのセットアップ
func setupGojaVM(vm *goja.Runtime, ginCtx *gin.Context) {
	// 既存の関数登録（getCookie, setCookie, setItem, getItem, console.log）はそのまま

	vm.Set("nyanGetAPI", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		username := call.Argument(1).String()
		password := call.Argument(2).String()
		result, err := getAPI(url, username, password)
		if err != nil {
			// エラーの場合は例外としてスロー（またはエラーメッセージを返す）
			panic(vm.ToValue(err.Error()))
		}
		v := vm.ToValue(result)
		return v
	})

	vm.Set("nyanGetCookie", func(name string) string {
		if ginCtx != nil {
			cookieValue, err := ginCtx.Cookie(name)
			if err != nil {
				logger.Printf("Error retrieving cookie: %v", err)
				return ""
			}
			return cookieValue
		}
		logger.Printf("Gin context is not set")
		return ""
	})

	vm.Set("nyanSetCookie", func(name, value string) {
		if ginCtx != nil {
			ginCtx.SetCookie(name, value, 3600, "/", "", false, true)
			logger.Printf("Set-Cookie: %s=%s", name, value)
		} else {
			logger.Printf("Gin context is not set")
		}
	})

	vm.Set("nyanSetItem", func(key, value string) {
		storage.Store(key, value)
	})
	vm.Set("nyanGetItem", func(key string) string {
		if v, ok := storage.Load(key); ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	})

	vm.Set("console", map[string]interface{}{
		"log": func(args ...interface{}) {
			logger.Printf(fmt.Sprint(args...))
		},
	})

	vm.Set("nyanJsonAPI", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		jsonData := call.Argument(1).String()
		username := call.Argument(2).String()
		password := call.Argument(3).String()

		// 第5引数：ヘッダー情報（オブジェクトまたはJSON文字列）
		var headers map[string]string
		if len(call.Arguments) >= 5 {
			// まずは、GojaのExportを使って直接オブジェクトとして取り出す
			if obj, ok := call.Argument(4).Export().(map[string]interface{}); ok {
				headers = make(map[string]string)
				for key, value := range obj {
					if s, ok := value.(string); ok {
						headers[key] = s
					} else {
						// 文字列以外なら fmt.Sprintで文字列化
						headers[key] = fmt.Sprint(value)
					}
				}
			} else {
				// オブジェクトとして取得できなければ、JSON文字列として処理する
				headerJSON := call.Argument(4).String()
				if err := json.Unmarshal([]byte(headerJSON), &headers); err != nil {
					panic(vm.ToValue("Invalid header JSON: " + err.Error()))
				}
			}
		}

		result, err := jsonAPI(url, []byte(jsonData), username, password, headers)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(result)
	})

	// execCommand を JavaScript から呼び出すためのラッパー関数を登録
	vm.Set("nyanHostExec", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(vm.ToValue("exec: No command provided"))
		}
		commandLine := call.Argument(0).String()
		result, err := execCommand(commandLine)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		// ExecResult を JSON に変換してから、マップに戻すことで json タグが反映される
		b, err := json.Marshal(result)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(m)
	})

	vm.Set("nyanGetFile", newNyanGetFile(vm))

	vm.Set("nyanSendMail", func(call goja.FunctionCall) goja.Value {

		// ---------- ユーティリティ -------------
		toSlice := func(v interface{}) []string {
			switch x := v.(type) {
			case nil:
				return []string{}
			case string:
				return []string{x}
			case []interface{}:
				out := make([]string, 0, len(x))
				for _, e := range x {
					if s, ok := e.(string); ok { out = append(out, s) }
				}
				return out
			case []string:
				return x
			default:
				return []string{}
			}
		}

		// ---------- A. オブジェクト呼び出し ----------
		if len(call.Arguments) == 1 && call.Argument(0).ExportType() != nil &&
			call.Argument(0).ExportType().Kind() == reflect.Map {

			obj := call.Argument(0).Export().(map[string]interface{})
			to    := toSlice(obj["to"])
			cc    := toSlice(obj["cc"])
			bcc   := toSlice(obj["bcc"])
			subj  := fmt.Sprint(obj["subject"])
			body  := fmt.Sprint(obj["body"])
			html  := false
			if v, ok := obj["html"].(bool); ok { html = v }

			if len(to) == 0 {
				panic(vm.ToValue("nyanSendMail: 'to' が空です"))
			}
			if subj == "" {
				panic(vm.ToValue("nyanSendMail: 'subject' が空です"))
			}

			if err := sendMail(to, cc, bcc, subj, body, html); err != nil {
				panic(vm.ToValue(err.Error()))
			}
			return vm.ToValue(true)
		}

		// ---------- B. 旧シグネチャ互換 (to,subject,body[,html][,cc][,bcc]) ----------
		if len(call.Arguments) < 3 {
			panic(vm.ToValue("nyanSendMail(to, subject, body [, isHtml] [, cc] [, bcc]) が必要です"))
		}

		to    := toSlice(call.Argument(0).Export())
		subj  := call.Argument(1).String()
		body  := call.Argument(2).String()
		html  := false
		cc    := []string{}
		bcc   := []string{}

		if len(call.Arguments) >= 4 { html = call.Argument(3).ToBoolean() }
		if len(call.Arguments) >= 5 { cc   = toSlice(call.Argument(4).Export()) }
		if len(call.Arguments) >= 6 { bcc  = toSlice(call.Argument(5).Export()) }

		if len(to) == 0 {
			panic(vm.ToValue("nyanSendMail: to が空です"))
		}

		if err := sendMail(to, cc, bcc, subj, body, html); err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(true)
	})

}

// convertShiftJISToUTF8 は、与えられたバイト列をShift-JIS(CP932)としてUTF-8文字列に変換する
func convertShiftJISToUTF8(b []byte) (string, error) {
	// 変換用のReaderを作る
	r := transform.NewReader(bytes.NewReader(b), japanese.ShiftJIS.NewDecoder())

	// 全部読み取ってUTF-8文字列を得る
	converted, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(converted), nil
}

// execCommand は、指定されたコマンドを実行し、結果を返す
func execCommand(commandLine string) (*ExecResult, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", commandLine)
	} else {
		cmd = exec.Command("sh", "-c", commandLine)
	}

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()

	result := &ExecResult{
		Success:  false,
		ExitCode: 0,
		Stdout:   "",
		Stderr:   "",
	}

	// (1) WindowsならShift-JIS→UTF-8変換を試みる
	//     それ以外のOSなら、そのままUTF-8として扱う
	if runtime.GOOS == "windows" {
		// stdout
		utf8Str, convErr := convertShiftJISToUTF8(stdoutBuf.Bytes())
		if convErr != nil {
			utf8Str = stdoutBuf.String() // フォールバック（変換失敗時は生バイトを流用）
		}
		result.Stdout = utf8Str

		// stderr
		utf8ErrStr, convErr2 := convertShiftJISToUTF8(stderrBuf.Bytes())
		if convErr2 != nil {
			utf8ErrStr = stderrBuf.String()
		}
		result.Stderr = utf8ErrStr
	} else {
		// Linux, macOSなどはそのままUTF-8扱い
		result.Stdout = stdoutBuf.String()
		result.Stderr = stderrBuf.String()
	}

	if err != nil {
		// 終了コードを取得
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}

		return result, fmt.Errorf("failed to exec: %w", err)
	}

	result.Success = true
	return result, nil
}

func newNyanGetFile(vm *goja.Runtime) func(call goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		// 引数のチェック
		if len(call.Arguments) < 1 {
			// vm を使ってエラーオブジェクトを生成する
			panic(vm.NewTypeError("nyanGetFileには1つの引数（ファイルパス）が必要です"))
		}
		relativePath := call.Arguments[0].String()

		// 実行中のバイナリのパスを取得し、ディレクトリ部分を取得
		exePath, err := os.Executable()
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		exeDir := filepath.Dir(exePath)

		// バイナリディレクトリからの相対パスを結合してフルパスを作成
		fullPath := filepath.Join(exeDir, relativePath)

		// ファイルを読み込み
		content, err := ioutil.ReadFile(fullPath)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}

		// 読み込んだ内容を文字列として返す
		return vm.ToValue(string(content))
	}
}

func handleJSONRPC(c *gin.Context) {
	var rpcReq JSONRPCRequest

	// JSONのパース
	if err := c.ShouldBindJSON(&rpcReq); err != nil {
		rpcResp := JSONRPCResponse{
			JSONRPC: "2.0",
			Error: &JSONRPCError{
				Code:    -32700,
				Message: "Parse error",
				Data:    err.Error(),
			},
			ID: nil, // パース失敗時はIDが取得できないためnull
		}
		c.JSON(http.StatusBadRequest, rpcResp)
		return
	}

	// jsonrpcフィールドのチェック
	if rpcReq.JSONRPC != "2.0" {
		rpcResp := JSONRPCResponse{
			JSONRPC: "2.0",
			Error: &JSONRPCError{
				Code:    -32600,
				Message: "Invalid Request: jsonrpc must be '2.0'",
			},
			ID: rpcReq.ID,
		}
		c.JSON(http.StatusBadRequest, rpcResp)
		return
	}

	// methodフィールドの存在チェック
	if rpcReq.Method == "" {
		rpcResp := JSONRPCResponse{
			JSONRPC: "2.0",
			Error: &JSONRPCError{
				Code:    -32601,
				Message: "Method not found",
			},
			ID: rpcReq.ID,
		}
		c.JSON(http.StatusNotFound, rpcResp)
		return
	}

	// 実行ディレクトリの取得
	execDir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		respondJSONRPCError(c, rpcReq.ID, -32603, "Internal error: cannot get execPath", err)
		return
	}
	if isTemporaryDirectory(execDir) {
		if execDir, err = os.Getwd(); err != nil {
			respondJSONRPCError(c, rpcReq.ID, -32603, "Internal error: cannot get working directory", err)
			return
		}
	}

	// api.jsonの読み込み
	apiJsonPath := filepath.Join(execDir, "api.json")
	scriptListData, err := loadJSONFile(apiJsonPath)
	if err != nil {
		respondJSONRPCError(c, rpcReq.ID, -32603, "Failed to read api.json", err)
		return
	}

	// method名（rpcReq.Method）からスクリプト情報を取得
	scriptInfoRaw, ok := scriptListData[rpcReq.Method]
	if !ok {
		respondJSONRPCError(c, rpcReq.ID, -32601, fmt.Sprintf("Method not found: %s", rpcReq.Method), nil)
		return
	}
	scriptInfo, ok := scriptInfoRaw.(map[string]interface{})
	if !ok {
		respondJSONRPCError(c, rpcReq.ID, -32603, "Invalid script info format in api.json", nil)
		return
	}

	// スクリプトファイルのパス取得
	scriptPathRaw, ok := scriptInfo["script"]
	if !ok {
		respondJSONRPCError(c, rpcReq.ID, -32603, fmt.Sprintf("No script path for method: %s", rpcReq.Method), nil)
		return
	}
	scriptPath, _ := scriptPathRaw.(string)
	fullPath := filepath.Join(execDir, scriptPath)

	// JSON-RPCのparamsを元にパラメータマップを構築
	allParams := make(map[string]interface{})
	for k, v := range rpcReq.Params {
		allParams[k] = v
	}
	// 既存ロジックが「api」パラメータを参照するために設定
	allParams["api"] = rpcReq.Method

	// JavaScriptの実行
	resultStr, err := runJavaScript(fullPath, allParams, c)
	if err != nil {
		respondJSONRPCError(c, rpcReq.ID, -32603, "Script execution failed", err)
		return
	}

	// JavaScriptの返却結果をJSONとしてパース
	var jsResult map[string]interface{}
	if err := json.Unmarshal([]byte(resultStr), &jsResult); err != nil {
		respondJSONRPCError(c, rpcReq.ID, -32603, "Failed to parse script response", err)
		return
	}

	// (1) success=false の場合に status を見てエラーを振り分ける
	if successVal, ok := jsResult["success"].(bool); ok {
		if !successVal {
			// "success": false の時
			if statusVal, ok := jsResult["status"].(float64); ok {
				status := int(statusVal)
				switch status {
				case 400:
					respondJSONRPCError(c, rpcReq.ID, -32602, "Invalid params", jsResult)
					return
				case 401:
					respondJSONRPCError(c, rpcReq.ID, -32001, "Unauthorized", jsResult)
					return
				case 404:
					respondJSONRPCError(c, rpcReq.ID, -32601, "Resource not found", jsResult)
					return
				case 500:
					respondJSONRPCError(c, rpcReq.ID, -32603, "Internal error", jsResult)
					return
				default:
					// その他のステータスは一旦すべてInternal error扱いなど、運用ポリシーによる
					respondJSONRPCError(c, rpcReq.ID, -32603, "Unknown error", jsResult)
					return
				}
			} else {
				// statusが数値でない or 存在しない場合もエラーにするならここで対応
				respondJSONRPCError(c, rpcReq.ID, -32603, "Missing or invalid status", jsResult)
				return
			}
		}
	}

	// JSON-RPC用に resultフィールドを作る（"status"は除くなどはお好みで）
	rpcResponseData := make(map[string]interface{})
	for k, v := range jsResult {
		if k != "status" {
			rpcResponseData[k] = v
		}
	}

	// 必要に応じてpush処理の実行
	performPush(scriptInfo, scriptListData, allParams, execDir)

	// JSON-RPC成功レスポンスの生成
	rpcResp := JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  rpcResponseData,
		ID:      rpcReq.ID,
	}

	// statusCode に従ってレスポンスを返す（通常は200のままでOK）
	c.JSON(http.StatusOK, rpcResp)
}


func respondJSONRPCError(c *gin.Context, id interface{}, code int, message string, data interface{}) {
	rpcErr := &JSONRPCError{
		Code:    code,
		Message: message,
		Data:    data,
	}
	c.JSON(http.StatusOK, JSONRPCResponse{
		JSONRPC: "2.0",
		Error:   rpcErr,
		ID:      id,
	})
}

// sendMail は config.json に定義された SMTP 経由でメールを送信する。
func sendMail(to, cc, bcc []string, subject, body string, isHTML bool) error {
	smtpCfg := globalConfig.SMTP
	if smtpCfg.Host == "" {
		return fmt.Errorf("SMTP not configured")
	}

	// ① config.default_bcc を合流
	bcc = append(bcc, smtpCfg.DefaultBCC...)

	// ② 3リスト間の重複を一括除去
	uniq := map[string]struct{}{}
	dedup := func(src []string) (out []string) {
		for _, addr := range src {
			a := strings.ToLower(strings.TrimSpace(addr))
			if a == "" { continue }
			if _, exists := uniq[a]; !exists {
				uniq[a] = struct{}{}
				out = append(out, addr) // 元の表記を保持
			}
		}
		return
	}
	to  = dedup(to)
	cc  = dedup(cc)
	bcc = dedup(bcc)

	// ---------- ヘッダー ----------
	hdr := map[string]string{
		"From":    fmt.Sprintf("%s <%s>",
			mime.QEncoding.Encode("utf-8", smtpCfg.FromName),
			smtpCfg.FromEmail),
		"To":      strings.Join(to, ","),
		"Subject": mime.QEncoding.Encode("utf-8", subject),
		"MIME-Version": "1.0",
	}
	if len(cc) > 0 {
		hdr["Cc"] = strings.Join(cc, ",")
	}
	if isHTML {
		hdr["Content-Type"] = "text/html; charset=UTF-8"
	} else {
		hdr["Content-Type"] = "text/plain; charset=UTF-8"
	}

	var msg strings.Builder
	for k, v := range hdr {
		msg.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	msg.WriteString("\r\n" + body)

	// ---------- 宛先リスト ----------
	rcpts := append(append(to, cc...), bcc...)

	addr := fmt.Sprintf("%s:%d", smtpCfg.Host, smtpCfg.Port)
	auth := smtp.PlainAuth("", smtpCfg.Username, smtpCfg.Password, smtpCfg.Host)

	if smtpCfg.TLS {                       // SMTPS (465)
		tlsCfg := &tls.Config{ServerName: smtpCfg.Host}
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil { return err }
		c, err := smtp.NewClient(conn, smtpCfg.Host)
		if err != nil { return err }
		defer c.Quit()
		if err := c.Auth(auth); err != nil { return err }
		if err := c.Mail(smtpCfg.FromEmail); err != nil { return err }
		for _, r := range rcpts { if err := c.Rcpt(r); err != nil { return err } }
		w, err := c.Data(); if err != nil { return err }
		if _, err := w.Write([]byte(msg.String())); err != nil { return err }
		return w.Close()
	}

	// SMTP + STARTTLS or 平文
	return smtp.SendMail(addr, auth, smtpCfg.FromEmail, rcpts, []byte(msg.String()))
}



