package main

import (

	"bytes"
	"crypto/tls"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"github.com/dop251/goja"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/natefinch/lumberjack"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"
	"time"
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

type MailAttachment struct {
	FileName    string
	ContentType string
	Data        []byte
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}


var (
	supportedProto = map[string]bool{"2025-06-18": true, "2025-03-26": true}
	sessions sync.Map // sid -> struct{created time.Time}
)
const defaultProto = "2025-03-26"



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
		// logger はまだ初期化前なので標準ログで終了
		log.Fatalf("Error loading config from %s: %v", configPath, err)
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
	r.POST("/nyan-toolbox", handleMCP)      // JSON-RPC 全メソッド
	r.GET("/nyan-toolbox", handleMCPGet)     // SSEしない場合は 405
	r.DELETE("/nyan-toolbox", handleMCPDeleteSession) // 任意: セッション明示終了

	//mcp
	r.POST("/mcp", handleMCP)
	r.GET("/mcp", handleMCPGet)                 // 405 を返すだけ
	r.DELETE("/mcp", handleMCPDeleteSession)    // セッション明示終了

	r.Any("/nyan", handleNyan)
	r.Any("/nyan/:apiName", handleNyanDetail)
	r.Any("/", handleRequest) // HTTPとWebSocketリクエストを同じエンドポイントで処理

	// 動的エンドポイントの登録
	execDir, _ = os.Getwd() // または、実行ファイルのディレクトリを使用
	if err := registerDynamicEndpoints(r, execDir); err != nil {
		logger.Fatalf("Failed to register dynamic endpoints: %v", err)
	}

	// HTTPSサーバーを起動するかどうかを判断
	// ★★★ 修正：cert/key のパス解決に resolvePath を使用 ★★★
	certFilePath, err := resolvePath(execDir, config.CertFile)
	if err != nil {
		logger.Fatalf("Invalid certPath %q: %v", config.CertFile, err)
	}
	keyFilePath, err := resolvePath(execDir, config.KeyFile)
	if err != nil {
		logger.Fatalf("Invalid keyPath %q: %v", config.KeyFile, err)
	}

	if config.CertFile != "" && config.KeyFile != "" {
		// HTTPSサーバーの起動
		logger.Printf("Starting HTTPS server at %d", config.Port)
		server := &http.Server{
			Addr:    fmt.Sprintf(":%d", config.Port),
			Handler: h2c.NewHandler(r, &http2.Server{}), // h2cハンドラを使用してHTTP/2を有効化（従来のまま）
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
// ★★★ 修正：filepath.HasPrefix は存在しないため、安全な判定に置き換え ★★★
func isTemporaryDirectory(path string) bool {
	sep := string(os.PathSeparator)
	p := filepath.Clean(path) + sep
	t := filepath.Clean(os.TempDir()) + sep
	return strings.HasPrefix(p, t)
}

// ★★★ 追加：ファイルパス解決関数 ★★★
// baseDir を起点に p を安全に解決する。
// - file:// を OSパスへ
// - ~, ~/ をホームへ
// - 環境変数を展開
// - 絶対パスならそのまま、相対なら baseDir と結合
func resolvePath(baseDir, p string) (string, error) {
	if p == "" {
		return "", nil
	}

	// 環境変数展開
	p = os.ExpandEnv(p)

	// file:// URL → ローカルパス
	if strings.HasPrefix(p, "file://") {
		u, err := url.Parse(p)
		if err != nil {
			return "", fmt.Errorf("invalid file URL: %w", err)
		}
		// / を OS の区切りに
		p = filepath.FromSlash(u.Path)
		// Windows の file://C:/... 形式調整（/C:/... → C:/...）
		if runtime.GOOS == "windows" && strings.HasPrefix(p, string(os.PathSeparator)) && len(p) >= 3 && p[1] == ':' {
			p = p[1:]
		}
	}

	// ~ と ~/ の展開
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home dir: %w", err)
		}
		if p == "~" {
			p = home
		} else if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
			p = filepath.Join(home, p[2:])
		}
	}

	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	return filepath.Join(baseDir, p), nil
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

	// 絶対パスに変換（相対なら execDir 起点）
	// ※ scriptPath は通常相対想定だが、絶対指定も許容できるよう resolvePath を使ってもよい
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

		receivedData["_remote_ip"] = getClientIP(c.Request)
		receivedData["_user_agent"] = c.Request.UserAgent()
		headersMap := make(map[string]string)
		for k, v := range c.Request.Header {
			headersMap[k] = strings.Join(v, ",")
		}
		receivedData["_headers"] = headersMap

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

	// ★★★ 追加：include の基準ディレクトリを取得（mainと同じロジック） ★★★
	basePath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		return "", fmt.Errorf("failed to get base path: %v", err)
	}
	if isTemporaryDirectory(basePath) {
		if basePath, err = os.Getwd(); err != nil {
			return "", fmt.Errorf("failed to get working directory: %v", err)
		}
	}

	// globalConfig.JavaScriptInclude にある各ファイルを読み込み、連結する
	var jsCode string
	for _, includePath := range globalConfig.JavaScriptInclude {
		// ★★★ 修正：resolvePath で絶対/相対/URL/環境変数/波線を解決 ★★★
		includeAbs, rerr := resolvePath(basePath, includePath)
		if rerr != nil {
			return "", fmt.Errorf("failed to resolve included JS file %s: %v", includePath, rerr)
		}
		code, err := ioutil.ReadFile(includeAbs)
		if err != nil {
			return "", fmt.Errorf("failed to read included JS file %s: %v", includeAbs, err)
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
	payload := gin.H{
		"error": errMsg,
	}
	if err != nil {
		// ログには詳細も出す
		logger.Printf("ERROR: %s - %v", errMsg, err)
		// クライアントにも詳細文字列を返す（原因の可視化）
		payload["detail"] = err.Error()
	} else {
		logger.Printf("ERROR: %s", errMsg)
	}
	c.JSON(status, payload)
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
// registerDynamicEndpoints は api.json の内容に基づいてルート直下のエンドポイントを登録する関数です。
func registerDynamicEndpoints(r *gin.Engine, execDir string) error {
	apiConf, err := loadJSONFile(filepath.Join(execDir, "api.json"))
	if err != nil {
		return fmt.Errorf("failed to load api.json: %v", err)
	}

	// 予約パスは動的登録しない（固定ハンドラを優先させる）
	reserved := map[string]struct{}{
		"nyan":         {},
		"nyan-rpc":     {},
		"nyan-toolbox": {},
	}

	for apiName := range apiConf {
		// 予約パスのスキップ（念のため "nyan-" プレフィックスも抑止）
		if _, ok := reserved[apiName]; ok || strings.HasPrefix(apiName, "nyan-") {
			continue
		}

		currentAPIName := apiName // クロージャ用に退避
		r.Any("/"+currentAPIName, func(c *gin.Context) {
			// WebSocket アップグレードなら WebSocket ハンドラへ
			if websocket.IsWebSocketUpgrade(c.Request) {
				handleWebSocket(c)
				return
			}

			// リクエストパラメータのマージ
			allParams := make(map[string]interface{})
			// URLクエリ
			for key, values := range c.Request.URL.Query() {
				allParams[key] = values[0]
			}
			// POSTフォーム
			for key, values := range c.Request.PostForm {
				allParams[key] = values[0]
			}
			// JSONボディ
			if c.ContentType() == "application/json" {
				var jsonBody map[string]interface{}
				if err := c.BindJSON(&jsonBody); err == nil {
					for k, v := range jsonBody {
						allParams[k] = v
					}
				} else {
					respondWithError(c, http.StatusBadRequest, "Invalid JSON data", err)
					return
				}
			}

			// エンドポイント名を "api" にセット
			endpoint := c.FullPath()[1:]
			if endpoint == "" {
				endpoint = currentAPIName
			}
			allParams["api"] = endpoint

			// api.json から対象スクリプトを取得
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

			// 実行
			fullScriptPath := filepath.Join(execDir, scriptPath)
			result, err := runJavaScript(fullScriptPath, allParams, c)
			if err != nil {
				respondWithError(c, http.StatusInternalServerError, "Failed to run JavaScript", err)
				return
			}

			// 結果のパースと返却
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

			// push 処理
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

	vm.Set("nyanGetAPI", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		user := call.Argument(1).String()
		pass := call.Argument(2).String()
		result, err := getAPI(url, user, pass)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(result)
	})

	vm.Set("nyanGetCookie", func(name string) string {
		if ginCtx == nil {
			return ""
		}
		v, _ := ginCtx.Cookie(name)
		return v
	})
	vm.Set("nyanSetCookie", func(name, value string) {
		if ginCtx != nil {
			ginCtx.SetCookie(name, value, 3600, "/", "", false, true)
		}
	})

	vm.Set("nyanSetItem", func(k, v string) { storage.Store(k, v) })
	vm.Set("nyanGetItem", func(k string) string {
		if v, ok := storage.Load(k); ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	})

	vm.Set("console", map[string]interface{}{
		"log": func(args ...interface{}) { logger.Print(args...) },
	})

	vm.Set("nyanJsonAPI", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		data := call.Argument(1).String()
		user := call.Argument(2).String()
		pass := call.Argument(3).String()

		var hdr map[string]string
		if len(call.Arguments) >= 5 {
			if m, ok := call.Argument(4).Export().(map[string]interface{}); ok {
				hdr = make(map[string]string)
				for k, v := range m {
					hdr[k] = fmt.Sprint(v)
				}
			}
		}
		res, err := jsonAPI(url, []byte(data), user, pass, hdr)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(res)
	})

	vm.Set("nyanHostExec", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			panic(vm.ToValue("command required"))
		}
		cmd := call.Argument(0).String()
		out, err := execCommand(cmd)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		js, _ := json.Marshal(out)
		var m map[string]interface{}
		_ = json.Unmarshal(js, &m)
		return vm.ToValue(m)
	})

	vm.Set("nyanGetFile", newNyanGetFile(vm))

	/* ===============================================================
	   nyanSendMail
	   - オブジェクト呼び出し {to,cc,bcc,subject,body,html,attachments}
	   - 旧シグネチャ呼び出し  (to,subject,body[,html][,cc][,bcc])
	================================================================ */

	vm.Set("nyanSendMail", func(call goja.FunctionCall) goja.Value {

		// ---- ヘルパー：任意 → []string --------------------------------
		toSlice := func(v interface{}) []string {
			switch t := v.(type) {
			case nil:
				return nil
			case string:
				return []string{t}
			case []string:
				return t
			case []interface{}:
				out := make([]string, 0, len(t))
				for _, x := range t {
					out = append(out, fmt.Sprint(x))
				}
				return out
			default:
				return []string{fmt.Sprint(t)}
			}
		}

		// ---------- A. オブジェクト形式 --------------------------------
		if len(call.Arguments) == 1 {
			obj, ok := call.Argument(0).Export().(map[string]interface{})
			if !ok {
				panic(vm.ToValue("object argument expected"))
			}

			to := toSlice(obj["to"])
			cc := toSlice(obj["cc"])
			bcc := toSlice(obj["bcc"])
			subj := fmt.Sprint(obj["subject"])
			body := fmt.Sprint(obj["body"])
			html := false
			if v, ok := obj["html"].(bool); ok {
				html = v
			}

			// ---------- 添付パース ----------
			atts := []MailAttachment{}
			if raw, ok := obj["attachments"].([]interface{}); ok {
				for _, v := range raw {
					m, ok := v.(map[string]interface{})
					if !ok {
						if o, ok := v.(*goja.Object); ok {
							m, _ = o.Export().(map[string]interface{})
						}
					}
					if m == nil {
						continue
					}
					// path
					if pv, ok := m["path"]; ok {
						p := fmt.Sprint(pv)
						if p != "" {
							abs := p
							if !filepath.IsAbs(p) {
								wd, _ := os.Getwd()
								abs = filepath.Join(wd, p)
							}
							data, err := os.ReadFile(abs)
							if err != nil {
								panic(vm.ToValue("read attach: " + err.Error()))
							}
							atts = append(atts, MailAttachment{
								FileName:    filepath.Base(abs),
								ContentType: mime.TypeByExtension(filepath.Ext(abs)),
								Data:        data,
							})
						}
					}
					// dataBase64
					if b64, ok := m["dataBase64"]; ok {
						dec, err := base64.StdEncoding.DecodeString(fmt.Sprint(b64))
						if err != nil {
							panic(vm.ToValue("base64 decode: " + err.Error()))
						}
						atts = append(atts, MailAttachment{
							FileName:    fmt.Sprint(m["filename"]),
							ContentType: fmt.Sprint(m["contentType"]),
							Data:        dec,
						})
					}
				}
			}

			if err := sendMail(to, cc, bcc, subj, body, html, atts); err != nil {
				panic(vm.ToValue(err.Error()))
			}
			return vm.ToValue(true)

		}

		// ---------- B. 旧シグネチャ ------------------------------------
		if len(call.Arguments) < 3 {
			panic(vm.ToValue("need at least 3 arguments"))
		}
		to := toSlice(call.Argument(0).Export())
		subj := call.Argument(1).String()
		body := call.Argument(2).String()
		html := false
		cc, bcc := []string{}, []string{}
		if len(call.Arguments) >= 4 {
			html = call.Argument(3).ToBoolean()
		}
		if len(call.Arguments) >= 5 {
			cc = toSlice(call.Argument(4).Export())
		}
		if len(call.Arguments) >= 6 {
			bcc = toSlice(call.Argument(5).Export())
		}

		if err := sendMail(to, cc, bcc, subj, body, html, nil); err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(true)
	})


	// --- base64--------------------------------------
	vm.Set("nyanReadFileB64", func(path string) string {
		// 相対パスならカレントディレクトリ基準で解決
		abs := path
		if !filepath.IsAbs(path) {
			wd, _ := os.Getwd()
			abs = filepath.Join(wd, path)
		}

		data, err := os.ReadFile(abs)
		if err != nil {
			// JS 側に例外として伝える
			panic(vm.ToValue(err.Error()))
		}
		return base64.StdEncoding.EncodeToString(data) // 改行無し／バイナリ OK
	})
	// --------------------------------------------------------------
	vm.Set("nyanSendMailAttachment", func(path string) map[string]interface{} {
		abs := path
		if !filepath.IsAbs(path) {
			wd, _ := os.Getwd()
			abs = filepath.Join(wd, path)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}

		// DetectContentType は 512byte まで見れば十分
		ctype := http.DetectContentType(data)
		if ctype == "application/octet-stream" {
			// 拡張子でもう一押し
			if extCT := mime.TypeByExtension(filepath.Ext(abs)); extCT != "" {
				ctype = extCT
			}
		}

		return map[string]interface{}{
			"filename":     filepath.Base(abs),
			"contentType":  ctype,
			"dataBase64":   base64.StdEncoding.EncodeToString(data),
		}
	})

	//--リモートのIP UserAgent Header情報の取得-------------------------
	vm.Set("nyanGetRemoteIP", func() string {
		if ginCtx == nil {
			return ""
		}
		return getClientIP(ginCtx.Request)
	})

	vm.Set("nyanGetUserAgent", func() string {
		if ginCtx == nil {
			return ""
		}
		return ginCtx.Request.UserAgent()
	})

	vm.Set("nyanGetRequestHeaders", func() map[string]string {
		out := map[string]string{}
		if ginCtx == nil {
			return out
		}
		for k, v := range ginCtx.Request.Header {
			out[k] = strings.Join(v, ",")
		}
		return out
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
			panic(vm.NewTypeError("nyanGetFileには1つの引数（ファイルパス）が必要です"))
		}
		relativePath := call.Arguments[0].String()

		// 実行中のバイナリのディレクトリからの相対パスに解決
		exePath, err := os.Executable()
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		exeDir := filepath.Dir(exePath)
		fullPath := filepath.Join(exeDir, relativePath)

		// ディレクトリ指定なら null
		if fi, err := os.Stat(fullPath); err == nil && fi.IsDir() {
			return goja.Null()
		}

		// 読み込み。存在しないなら null、その他はエラーを投げる
		content, err := os.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				return goja.Null()
			}
			// 権限など他のエラーはJS例外に（従来の動作）
			panic(vm.ToValue(err.Error()))
		}

		// 読み込んだ内容を文字列で返す（バイナリは Base64 を使う nyanReadFileB64 を推奨）
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

// sendMail は config.json の SMTP 設定でメールを送り、attachments があれば添付する。
func sendMail(
	to, cc, bcc []string,        // 宛先
	subject, body string,        // 件名・本文
	isHTML bool,                 // true=HTML  false=プレーン
	attachments []MailAttachment, // 添付ファイル
) error {

	/* ───── 0. 設定チェック ───────────────────────── */
	s := globalConfig.SMTP
	if s.Host == "" {
		return fmt.Errorf("SMTP not configured")
	}

	/* ───── 1. 宛先マージ & 重複除去 ───────────────── */
	bcc = append(bcc, s.DefaultBCC...)
	seen := map[string]struct{}{}
	dedupe := func(src []string) (out []string) {
		for _, addr := range src {
			if addr = strings.TrimSpace(addr); addr == "" {
				continue
			}
			key := strings.ToLower(addr)
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				out = append(out, addr) // 表示は元の大小文字
			}
		}
		return
	}
	to, cc, bcc = dedupe(to), dedupe(cc), dedupe(bcc)

	/* ───── 2. MIME ヘッダー ─────────────────────── */
	h := textproto.MIMEHeader{}
	h.Set("From",
		fmt.Sprintf("%s <%s>",
			mime.QEncoding.Encode("UTF-8", s.FromName),
			s.FromEmail))
	h.Set("To", strings.Join(to, ","))
	if len(cc) > 0 {
		h.Set("Cc", strings.Join(cc, ","))
	}
	h.Set("Subject", mime.QEncoding.Encode("UTF-8", subject))
	h.Set("MIME-Version", "1.0")

	/* ───── 3. マルチパート組み立て ──────────────── */
	var msg bytes.Buffer
	mp := multipart.NewWriter(&msg)
	h.Set("Content-Type",
		fmt.Sprintf("multipart/mixed; boundary=%q", mp.Boundary()))

	// 3-1 先頭ヘッダー出力
	for k, v := range h {
		msg.WriteString(fmt.Sprintf("%s: %s\r\n", k, v[0]))
	}
	msg.WriteString("\r\n")

	// 3-2 本文パート
	partHdr := textproto.MIMEHeader{}
	if isHTML {
		partHdr.Set("Content-Type", "text/html; charset=UTF-8")
	} else {
		partHdr.Set("Content-Type", "text/plain; charset=UTF-8")
	}
	partHdr.Set("Content-Transfer-Encoding", "base64")

	bp, _ := mp.CreatePart(partHdr)
	encBody := base64.NewEncoder(base64.StdEncoding, bp)
	encBody.Write([]byte(body))
	encBody.Close()

	// 3-3 添付パート
	for _, a := range attachments {
		if a.FileName == "" {
			a.FileName = "attachment"
		}
		a.ContentType = http.DetectContentType(a.Data)
		if a.ContentType == "application/octet-stream" {
			a.ContentType = mime.TypeByExtension(filepath.Ext(a.FileName))
			if a.ContentType == "" {
				a.ContentType = "application/octet-stream"
			}
		}
		attHdr := textproto.MIMEHeader{}
		attHdr.Set("Content-Type",
			fmt.Sprintf("%s; name=%q", a.ContentType, a.FileName))
		attHdr.Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename=%q`, a.FileName))
		attHdr.Set("Content-Transfer-Encoding", "base64")

		ap, _ := mp.CreatePart(attHdr)
		encAtt := base64.NewEncoder(base64.StdEncoding, ap)
		encAtt.Write(a.Data)
		encAtt.Close()
	}
	mp.Close() // --boundary-- を書く

	logger.Printf("DEBUG: attachments=%d, message=%d bytes", len(attachments), msg.Len())

	/* ───── 4. SMTP 送信 ────────────────────────── */
	rcpts := append(append(to, cc...), bcc...)
	addr  := fmt.Sprintf("%s:%d", s.Host, s.Port)
	auth  := smtp.PlainAuth("", s.Username, s.Password, s.Host)

	// 4-1 SMTPS / STARTTLS 直後 TLS
	if s.TLS {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.Host})
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, s.Host)
		if err != nil {
			return err
		}
		defer c.Quit()

		if err := c.Auth(auth); err != nil {
			return err
		}
		if err := c.Mail(s.FromEmail); err != nil {
			return err
		}
		for _, r := range rcpts {
			if err := c.Rcpt(r); err != nil {
				return err
			}
		}
		w, _ := c.Data()
		if _, err := w.Write(msg.Bytes()); err != nil {
			return err
		}
		return w.Close()
	}

	// 4-2 平文 or STARTTLS をサーバ側が自動要求
	return smtp.SendMail(addr, auth, s.FromEmail, rcpts, msg.Bytes())
}

func getClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}

	// X-Forwarded-For（カンマ区切りで複数入ることがある）
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for _, p := range parts {
			ip := strings.TrimSpace(p)
			if ip != "" && ip != "unknown" {
				return ip
			}
		}
	}

	// X-Real-IP
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}

	// RemoteAddr のパース（host:port）
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
		return host
	}

	// フォールバック
	return r.RemoteAddr
}

func handleMCP(c *gin.Context) {
	// 通知/応答なら 202 を返す規約（必要に応じて判定）
	var req rpcReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, map[string]any{"error":"bad json"})
		return
	}

	// initialize は特別扱い（セッション開始 & プロトコル合意）
	switch req.Method {
	case "initialize":
		// params.protocolVersion を読む
		var p struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ClientInfo      map[string]any `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &p)

		ver := p.ProtocolVersion
		if !supportedProto[ver] { ver = defaultProto } // 最低限の互換を返す

		// セッション発行（任意だが推奨）
		sid := generateSecureSessionID()
		sessions.Store(sid, time.Now())
		c.Header("Mcp-Session-Id", sid)

		// サーバの capabilities（最低限 tools）
		res := map[string]any{
			"protocolVersion": ver,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]string{
				"name":    globalConfig.Name,
				"version": globalConfig.Version,
			},
		}
		c.JSON(http.StatusOK, map[string]any{
			"jsonrpc":"2.0","id":req.ID,"result":res,
		})
		return

	case "notifications/initialized":
		c.Status(http.StatusAccepted) // 202・ボディ無し
		return

	case "ping":
		c.JSON(http.StatusOK, map[string]any{"jsonrpc":"2.0","id":req.ID,"result":map[string]any{}})
		return
	}

	// initialize 以外はセッションとプロトコルヘッダを検証
	sid := c.GetHeader("Mcp-Session-Id")
	if _, ok := sessions.Load(sid); !ok {
		c.AbortWithStatus(http.StatusNotFound) // 404 → クライアントは再 initialize
		return
	}
	proto := c.GetHeader("MCP-Protocol-Version")
	if proto == "" { proto = defaultProto }
	if !supportedProto[proto] {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	switch req.Method {
	case "tools/list":
		// api.json → Tool 定義（inputSchema は camelCase）
		result := buildToolsList() // []Tool と nextCursor を返す自前関数
		c.JSON(http.StatusOK, map[string]any{
			"jsonrpc":"2.0","id":req.ID,"result":result,
		})
		return

	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		// JS 実行
		out := callJS(p.Name, p.Arguments, c) // 既存 runJavaScript をラップして取得
		// MCP 形式の結果に整形（最低限 text）
		c.JSON(http.StatusOK, map[string]any{
			"jsonrpc":"2.0","id":req.ID,"result": map[string]any{
				"content": []map[string]any{{"type":"text","text": stringOrJSON(out)}},
			},
		})
		return

	default:
		c.JSON(http.StatusOK, map[string]any{
			"jsonrpc":"2.0","id":req.ID,
			"error": map[string]any{"code":-32601,"message":"Method not found"},
		})
		return
	}
}

func handleMCPGet(c *gin.Context) {
	c.AbortWithStatus(http.StatusMethodNotAllowed) // 405
}

// ===== ここから追加分: MCPヘルパー群 =====

// 暗号学的ランダムで URL セーフなセッションIDを生成
func generateSecureSessionID() string {
	b := make([]byte, 32) // 256bit
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("failed to generate session id: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(b) // パディング無し
}

// セッションTTL（必要に応じて利用）
const sessionTTL = 24 * time.Hour

func isSessionAlive(created time.Time) bool {
	return time.Since(created) < sessionTTL
}

// DELETE /nyan-toolbox でセッション明示終了
func handleMCPDeleteSession(c *gin.Context) {
	sid := c.GetHeader("Mcp-Session-Id")
	if sid == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if _, ok := sessions.Load(sid); ok {
		sessions.Delete(sid)
		c.Status(http.StatusNoContent) // 204
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

// tools/list の結果を api.json から構築（MCP 形式）
func buildToolsList() map[string]any {
	execDir, err := os.Getwd()
	if err != nil {
		return map[string]any{"tools": []any{}, "nextCursor": nil}
	}
	apiConfPath := filepath.Join(execDir, "api.json")
	apiConf, err := loadJSONFile(apiConfPath)
	if err != nil {
		return map[string]any{"tools": []any{}, "nextCursor": nil}
	}

	tools := make([]map[string]any, 0, len(apiConf))
	for name, raw := range apiConf {
		api, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		desc, _ := api["description"].(string)
		scriptPath, _ := api["script"].(string)

		// デフォルト schema
		inputSchema := map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		}

		// JS 内の const nyanAcceptedParams を Schema 推定に利用
		if scriptPath != "" {
			full := filepath.Join(execDir, scriptPath)
			if scriptContent, err := os.ReadFile(full); err == nil {
				params := parseConstObject(scriptContent, "nyanAcceptedParams")
				if len(params) > 0 {
					props := map[string]any{}
					required := []string{}
					for k, v := range params {
						t := "string"
						switch v.(type) {
						case float64, int, int64:
							t = "number"
						case bool:
							t = "boolean"
						}
						props[k] = map[string]any{
							"type":        t,
							"description": fmt.Sprintf("Parameter: %s", k),
						}
						required = append(required, k)
					}
					inputSchema["properties"] = props
					inputSchema["required"] = required
				}
			}
		}

		tools = append(tools, map[string]any{
			"name":        name,
			"description": desc,
			"inputSchema": inputSchema, // MCP は camelCase
		})
	}

	return map[string]any{
		"tools":      tools,
		"nextCursor": nil,
	}
}

// tools/call 用: JS 呼び出しの薄いラッパ
func callJS(toolName string, args map[string]any, c *gin.Context) string {
	execDir, err := os.Getwd()
	if err != nil {
		return `{"status":500,"error":"cwd error"}`
	}
	apiConfPath := filepath.Join(execDir, "api.json")
	apiConf, err := loadJSONFile(apiConfPath)
	if err != nil {
		return `{"status":500,"error":"api.json load error"}`
	}

	raw, ok := apiConf[toolName]
	if !ok {
		return fmt.Sprintf(`{"status":404,"error":"tool not found: %s"}`, toolName)
	}
	api, ok := raw.(map[string]any)
	if !ok {
		return `{"status":500,"error":"invalid api config"}`
	}
	scriptPath, _ := api["script"].(string)
	if scriptPath == "" {
		return `{"status":400,"error":"no script path"}`
	}

	fullScript := filepath.Join(execDir, scriptPath)

	// 引数＋メタ情報を準備
	allParams := map[string]any{}
	for k, v := range args {
		allParams[k] = v
	}
	allParams["api"] = toolName
	if c != nil {
		allParams["_remote_ip"] = getClientIP(c.Request)
		allParams["_user_agent"] = c.Request.UserAgent()
		h := map[string]string{}
		for k, v := range c.Request.Header {
			h[k] = strings.Join(v, ",")
		}
		allParams["_headers"] = h
	}

	out, err := runJavaScript(fullScript, allParams, c)
	if err != nil {
		return fmt.Sprintf(`{"status":500,"error":%q}`, err.Error())
	}
	return out
}

// text 用に見やすく整形（JSONならインデント）
func stringOrJSON(s string) string {
	t := strings.TrimSpace(s)
	if (strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}")) ||
		(strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")) {
		var buf bytes.Buffer
		if err := json.Indent(&buf, []byte(t), "", "  "); err == nil {
			return buf.String()
		}
	}
	return s
}
