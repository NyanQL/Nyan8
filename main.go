package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
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
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/natefinch/lumberjack"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"
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
	Name              string             `json:"name"`
	Profile           string             `json:"profile"`
	Version           string             `json:"version"`
	Port              int                `json:"Port"`
	CertFile          string             `json:"certPath"`
	KeyFile           string             `json:"keyPath"`
	JavaScriptInclude []string           `json:"javascript_include"`
	Log               LogConfig          `json:"log"`
	SMTP              SMTPConfig         `json:"smtp"`
	APIHotReload      APIHotReloadConfig `json:"APIHotReload"`
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
	JSONRPC string        `json:"jsonrpc"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
	ID      interface{}   `json:"id,omitempty"`
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

type ParamCheckResponse struct {
	Success bool        `json:"success"`
	Status  int         `json:"status"`
	Result  interface{} `json:"result"`
}

type APIResponse struct {
	Status      int
	ContentType string
	Headers     map[string]string
	Body        []byte
}

type SMTPConfig struct {
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	Username   string   `json:"username"`
	Password   string   `json:"password"`
	FromEmail  string   `json:"from_email"`
	FromName   string   `json:"from_name"`
	TLS        bool     `json:"tls"`
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
	// BinaryVersion can be set at build time with:
	// go build -ldflags "-X main.BinaryVersion=vX.Y.Z"
	BinaryVersion  = "v0.0.18"
	supportedProto = map[string]bool{"2025-06-18": true, "2025-03-26": true}
	sessions       sync.Map // sid -> struct{created time.Time}
)

const defaultProto = "2025-03-26"
const (
	apiTypeAPI      = "api"
	apiTypeInclude  = "include"
	apiTypeWSClient = "ws_client"
	apiTypePublic   = "public"
	apiTypeSchedule = "schedule"
)

// config格納場所
var globalConfig Config

type serviceFilePath struct {
	Path   string
	Source string
}

type serviceFilePaths struct {
	API    serviceFilePath
	Config serviceFilePath
}

var servicePaths serviceFilePaths

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

	paths, err := resolveServiceFilePaths(execDir, os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	servicePaths = paths
	fmt.Printf("Config file path: %s (source: %s)\n", paths.Config.Path, paths.Config.Source)
	fmt.Printf("API file path: %s (source: %s)\n", paths.API.Path, paths.API.Source)

	config, err := loadConfig(paths.Config.Path)
	if err != nil {
		// logger はまだ初期化前なので標準ログで終了
		log.Fatalf("Error loading config from %s: %v", paths.Config.Path, err)
	}
	configBaseDir := filepath.Dir(paths.Config.Path)
	apiBaseDir := filepath.Dir(paths.API.Path)
	adjustConfigPaths(configBaseDir, &config)
	globalConfig = config
	apiHotReloadInterval, err := parseAPIHotReloadInterval(config.APIHotReload.Interval)
	if err != nil {
		log.Fatalf("Invalid APIHotReload.Interval %q: %v", config.APIHotReload.Interval, err)
	}

	// ロガーをセットアップ
	initLogger(configBaseDir)
	binaryVersion := BinaryVersion
	if strings.TrimSpace(binaryVersion) == "" {
		binaryVersion = "unset"
	}
	configVersion := strings.TrimSpace(globalConfig.Version)
	if configVersion == "" {
		configVersion = "unset"
	}
	logger.Printf("Binary version: %s", binaryVersion)
	logger.Printf("Go runtime version: %s", runtime.Version())
	logger.Printf("Config file: %s (source: %s)", paths.Config.Path, paths.Config.Source)
	logger.Printf("API file: %s (source: %s)", paths.API.Path, paths.API.Source)
	logger.Printf("Config version: %s", configVersion)

	initialAPIConfig, err := readAPIConfigFile(paths.API.Path, apiBaseDir)
	if err != nil {
		logger.Fatalf("Failed to load api.json: %v", err)
	}
	publishAPISnapshot(initialAPIConfig.Snapshot)
	backgroundRuntimes = newBackgroundRuntimeManager()
	backgroundRuntimes.reconcile(initialAPIConfig.Snapshot.Schedules, initialAPIConfig.Snapshot.WSClients)
	if config.APIHotReload.Enabled {
		logger.Printf("API hot reload enabled: interval=%s", apiHotReloadInterval)
		go watchAPIFile(paths.API.Path, apiBaseDir, apiHotReloadInterval, initialAPIConfig.Hash)
	} else {
		logger.Printf("API hot reload disabled")
	}

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
		if dispatchDynamicEndpoint(c, apiBaseDir) {
			return
		}
		respondWithError(c, http.StatusNotFound, "Endpoint not found", nil)
	})

	r.POST("/nyan-rpc", handleJSONRPC)
	r.POST("/nyan-toolbox", handleMCP)                // JSON-RPC 全メソッド
	r.GET("/nyan-toolbox", handleMCPGet)              // SSEしない場合は 405
	r.DELETE("/nyan-toolbox", handleMCPDeleteSession) // 任意: セッション明示終了

	r.Any("/nyan", handleNyan)
	r.Any("/nyan/:apiName", handleNyanDetail)
	r.Any("/", handleRequest) // HTTPとWebSocketリクエストを同じエンドポイントで処理

	// 動的エンドポイントの登録
	if err := registerDynamicEndpoints(r, apiBaseDir); err != nil {
		logger.Fatalf("Failed to register dynamic endpoints: %v", err)
	}

	// HTTPSサーバーを起動するかどうかを判断
	// ★★★ 修正：cert/key のパス解決に resolvePath を使用 ★★★
	certFilePath, err := resolvePath(configBaseDir, config.CertFile)
	if err != nil {
		logger.Fatalf("Invalid certPath %q: %v", config.CertFile, err)
	}
	keyFilePath, err := resolvePath(configBaseDir, config.KeyFile)
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

func resolveServiceFilePaths(execDir string, args []string) (serviceFilePaths, error) {
	flags := flag.NewFlagSet("Nyan8", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	apiFlag := flags.String("api", "", "path to api.json")
	configFlag := flags.String("config", "", "path to config.json")
	if err := flags.Parse(args); err != nil {
		return serviceFilePaths{}, err
	}

	apiPath, apiSource := chooseServiceFilePath(*apiFlag, "NYAN_API_PATH", filepath.Join(execDir, "api.json"), "--api")
	configPath, configSource := chooseServiceFilePath(*configFlag, "NYAN_CONFIG_PATH", filepath.Join(execDir, "config.json"), "--config")

	resolvedAPIPath, err := resolveExistingServiceFilePath(apiPath, "api", apiSource)
	if err != nil {
		return serviceFilePaths{}, err
	}
	resolvedConfigPath, err := resolveExistingServiceFilePath(configPath, "config", configSource)
	if err != nil {
		return serviceFilePaths{}, err
	}

	return serviceFilePaths{
		API:    serviceFilePath{Path: resolvedAPIPath, Source: apiSource},
		Config: serviceFilePath{Path: resolvedConfigPath, Source: configSource},
	}, nil
}

func chooseServiceFilePath(cliValue, envName, defaultPath, cliSource string) (string, string) {
	if strings.TrimSpace(cliValue) != "" {
		return cliValue, cliSource
	}
	if envValue := strings.TrimSpace(os.Getenv(envName)); envValue != "" {
		return envValue, envName
	}
	return defaultPath, "default"
}

func resolveExistingServiceFilePath(pathValue, label, source string) (string, error) {
	resolvedPath, err := filepath.Abs(pathValue)
	if err != nil {
		return "", fmt.Errorf("%s file path could not be resolved: %s (source: %s): %w", label, pathValue, source, err)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s file not found: %s (source: %s)", label, resolvedPath, source)
		}
		return "", fmt.Errorf("%s file cannot be accessed: %s (source: %s): %w", label, resolvedPath, source, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s file is a directory: %s (source: %s)", label, resolvedPath, source)
	}
	return resolvedPath, nil
}

func apiJSONPath(fallbackBaseDir string) string {
	if strings.TrimSpace(servicePaths.API.Path) != "" {
		return servicePaths.API.Path
	}
	return filepath.Join(fallbackBaseDir, "api.json")
}

func apiBaseDir(fallbackBaseDir string) string {
	if strings.TrimSpace(servicePaths.API.Path) != "" {
		return filepath.Dir(servicePaths.API.Path)
	}
	return fallbackBaseDir
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

// APIエントリの type を取得する。未指定なら "api" とする。
func getAPIType(entry map[string]interface{}) string {
	if entry == nil {
		return apiTypeAPI
	}
	if t, ok := entry["type"].(string); ok {
		t = strings.TrimSpace(t)
		if t != "" {
			return t
		}
	}
	return apiTypeAPI
}

func getAPIString(entry map[string]interface{}, keys ...string) string {
	if entry == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := entry[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// connectURL が env:XXXX 形式なら環境変数 XXXX で解決する。空や未設定はエラー。
func resolveConnectURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("connectURL is empty")
	}
	if strings.HasPrefix(raw, "env:") {
		key := strings.TrimPrefix(raw, "env:")
		if key == "" {
			return "", fmt.Errorf("connectURL env: prefix is empty")
		}
		val := os.Getenv(key)
		if val == "" {
			return "", fmt.Errorf("environment variable %s is empty", key)
		}
		return val, nil
	}
	return raw, nil
}

// loadConfig は設定ファイルを読み込みます。
func loadConfig(filename string) (Config, error) {
	var config Config
	applyConfigDefaults(&config)

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

func adjustConfigPaths(configBaseDir string, config *Config) {
	config.CertFile = resolvePathFromBase(configBaseDir, config.CertFile)
	config.KeyFile = resolvePathFromBase(configBaseDir, config.KeyFile)
	config.Log.Filename = resolvePathFromBase(configBaseDir, config.Log.Filename)
	for i, includePath := range config.JavaScriptInclude {
		config.JavaScriptInclude[i] = resolvePathFromBase(configBaseDir, includePath)
	}
}

func resolvePathFromBase(baseDir, pathValue string) string {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return pathValue
	}
	pathValue = os.ExpandEnv(pathValue)
	if strings.HasPrefix(pathValue, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			if pathValue == "~" {
				pathValue = home
			} else if strings.HasPrefix(pathValue, "~/") || strings.HasPrefix(pathValue, `~\`) {
				pathValue = filepath.Join(home, pathValue[2:])
			}
		}
	}
	if filepath.IsAbs(pathValue) {
		return pathValue
	}
	return filepath.Join(baseDir, pathValue)
}

func getVersion() string {
	if strings.TrimSpace(BinaryVersion) != "" {
		return BinaryVersion
	}
	if strings.TrimSpace(globalConfig.Version) != "" {
		return globalConfig.Version
	}
	return "dev"
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
	fmt.Print("handleAPIRequest: ")
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
	execDir := apiBaseDir(execPath)

	ginContext = c
	defer func() {
		ginContext = nil
	}()

	// スクリプトリストの取り込み
	apiJsonPath := apiJSONPath(execDir)
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

	if getAPIType(scriptInfo) != apiTypeAPI {
		respondWithError(c, http.StatusBadRequest, fmt.Sprintf("API %s is not an HTTP/WebSocket endpoint", scriptValueKey), nil)
		return
	}

	// スクリプトのパスを取得
	scriptPath, ok := scriptInfo["script"].(string)
	if !ok {
		logger.Printf("Script path not found in script info for key: %s", scriptValueKey)
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Script path not found in script info for key: %s", scriptValueKey)})
		return
	}

	// 絶対パスに変換（相対なら execDir 起点）
	// ※ scriptPath は通常相対想定だが、絶対指定も許容できるよう resolvePath を使ってもよい
	scriptPath = resolvePathFromBase(execDir, scriptPath)

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
	apiNameValue, _ := c.Get("nyan_api_name")
	apiNameString, _ := apiNameValue.(string)
	if apiNameString == "" {
		apiNameString = c.Param("api")
	}
	if apiNameString == "" {
		apiNameString = strings.TrimPrefix(c.Request.URL.Path, "/")
	}
	// push受信用にこの接続を登録
	pushConnections.Store(apiNameString, conn)
	defer pushConnections.Delete(apiNameString)

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
	execDir := apiBaseDir(execPath)

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
		apiJsonPath := apiJSONPath(execDir)
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

		if getAPIType(scriptInfo) != apiTypeAPI {
			logger.Printf("Script type for %s is not api", scriptValue)
			sendErrorMessage(conn, "Script type not supported")
			continue
		}

		scriptPath, ok := scriptInfo["script"].(string)
		if !ok {
			logger.Printf("Script path not found for key: %s", scriptValue)
			sendErrorMessage(conn, "Script path not found")
			continue
		}

		// メインAPIのスクリプトの絶対パス作成
		javascriptPath := resolvePathFromBase(execDir, scriptPath)

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
							pushScriptPath := resolvePathFromBase(execDir, pushScript)
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
	return runJavaScriptWithSnapshot(currentAPISnapshot(), scriptPath, allParams, ginCtx)
}

func runJavaScriptWithSnapshot(snapshot *APIConfigSnapshot, scriptPath string, allParams map[string]interface{}, ginCtx *gin.Context) (string, error) {
	value, err := runJavaScriptValueWithSnapshot(snapshot, scriptPath, allParams, ginCtx)
	if err != nil {
		return "", err
	}
	return value.String(), nil
}

func runJavaScriptValue(scriptPath string, allParams map[string]interface{}, ginCtx *gin.Context) (goja.Value, error) {
	return runJavaScriptValueWithSnapshot(currentAPISnapshot(), scriptPath, allParams, ginCtx)
}

func runJavaScriptValueWithSnapshot(snapshot *APIConfigSnapshot, scriptPath string, allParams map[string]interface{}, ginCtx *gin.Context) (goja.Value, error) {
	// 新たな goja の VM を生成
	vm := goja.New()
	// 必要なグローバル関数等を登録する
	setupGojaVMWithSnapshot(vm, snapshot, ginCtx)

	// ★★★ 追加：include の基準ディレクトリを取得（mainと同じロジック） ★★★
	basePath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		return nil, fmt.Errorf("failed to get base path: %v", err)
	}
	if isTemporaryDirectory(basePath) {
		if basePath, err = os.Getwd(); err != nil {
			return nil, fmt.Errorf("failed to get working directory: %v", err)
		}
	}

	// globalConfig.JavaScriptInclude にある各ファイルを読み込み、連結する
	var jsCode string
	for _, includePath := range globalConfig.JavaScriptInclude {
		// ★★★ 修正：resolvePath で絶対/相対/URL/環境変数/波線を解決 ★★★
		includeAbs, rerr := resolvePath(basePath, includePath)
		if rerr != nil {
			return nil, fmt.Errorf("failed to resolve included JS file %s: %v", includePath, rerr)
		}
		code, err := ioutil.ReadFile(includeAbs)
		if err != nil {
			return nil, fmt.Errorf("failed to read included JS file %s: %v", includeAbs, err)
		}
		jsCode += string(code) + "\n"
	}

	// メインスクリプトを読み込む
	mainCode, err := ioutil.ReadFile(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read main script file %s: %v", scriptPath, err)
	}
	jsCode += string(mainCode)

	// allParams を JSON 化して、グローバル変数 allParams としてセットする
	allParamsJSON, err := json.Marshal(allParams)
	if err != nil {
		return nil, err
	}
	_, err = vm.RunString(fmt.Sprintf("let nyanAllParams = %s;", string(allParamsJSON)))
	if err != nil {
		return nil, err
	}

	// 連結した JavaScript コードを実行
	value, err := vm.RunString(jsCode)
	if err != nil {
		return nil, err
	}

	return value, nil
}

// callNyanAPIFromVM は、main.go 側から自身のAPI定義(js)を直接実行します。
func callNyanAPIFromVM(apiName string, allParams map[string]interface{}, ginCtx *gin.Context) (string, error) {
	return callNyanAPIFromVMWithSnapshot(currentAPISnapshot(), apiName, allParams, ginCtx)
}

func callNyanAPIFromVMWithSnapshot(snapshot *APIConfigSnapshot, apiName string, allParams map[string]interface{}, ginCtx *gin.Context) (string, error) {
	if strings.TrimSpace(apiName) == "" {
		return "", fmt.Errorf("api name is required")
	}

	execDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %v", err)
	}
	execDir = apiBaseDir(execDir)

	if snapshot == nil {
		return "", fmt.Errorf("API configuration is not loaded")
	}
	apiRaw, ok := snapshot.Definitions[apiName]
	if !ok {
		return "", fmt.Errorf("API config not found: %s", apiName)
	}

	apiMap, ok := apiRaw.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid API config format: %s", apiName)
	}
	if getAPIType(apiMap) != apiTypeAPI {
		return "", fmt.Errorf("API %s is not an HTTP endpoint", apiName)
	}

	scriptPath, ok := apiMap["script"].(string)
	if !ok || strings.TrimSpace(scriptPath) == "" {
		return "", fmt.Errorf("script not found for API %s", apiName)
	}

	params := map[string]interface{}{}
	for k, v := range allParams {
		params[k] = v
	}
	params["api"] = apiName

	fullScriptPath := resolvePathFromBase(execDir, scriptPath)
	result, err := runJavaScriptWithSnapshot(snapshot, fullScriptPath, params, ginCtx)
	if err != nil {
		return "", fmt.Errorf("failed to run API %s: %v", apiName, err)
	}
	return result, nil
}

// loadJSONFile はJSONファイルを読み込みます。
func loadJSONFile(filePath string) (map[string]interface{}, error) {
	if files, ok := cachedAPIFilesFor(filePath); ok {
		return files, nil
	}
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

func extractHeaders(arg goja.Value) map[string]string {
	if m, ok := arg.Export().(map[string]interface{}); ok {
		hdr := make(map[string]string, len(m))
		for k, v := range m {
			hdr[k] = fmt.Sprint(v)
		}
		return hdr
	}
	return nil
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
	logFilePath := globalConfig.Log.Filename
	if strings.TrimSpace(logFilePath) != "" && !filepath.IsAbs(logFilePath) {
		logFilePath = filepath.Join(execDir, logFilePath)
	}
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

func collectRequestParams(c *gin.Context) (map[string]interface{}, error) {
	params := make(map[string]interface{})
	for key, values := range c.Request.URL.Query() {
		if len(values) > 1 {
			params[key] = values
			continue
		}
		if len(values) == 1 {
			params[key] = values[0]
		}
	}

	contentType := c.ContentType()
	if strings.HasPrefix(contentType, "application/json") {
		var jsonBody map[string]interface{}
		if err := c.ShouldBindJSON(&jsonBody); err != nil && err != io.EOF {
			return nil, err
		}
		for key, value := range jsonBody {
			params[key] = value
		}
		return params, nil
	}

	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
			return nil, err
		}
	} else if err := c.Request.ParseForm(); err != nil {
		return nil, err
	}
	for key, values := range c.Request.PostForm {
		if len(values) > 1 {
			params[key] = values
			continue
		}
		if len(values) == 1 {
			params[key] = values[0]
		}
	}
	return params, nil
}

func isCheckOnlyMode(params map[string]interface{}) bool {
	if params == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(fmt.Sprint(params["nyan_mode"])), "checkOnly")
}

func cloneParams(params map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(params))
	for key, value := range params {
		cloned[key] = value
	}
	return cloned
}

func parseStatusCode(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

func parseCheckResponse(value goja.Value, checkName string) (ParamCheckResponse, error) {
	if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return ParamCheckResponse{}, fmt.Errorf("%s must return an object", checkName)
	}

	exported := value.Export()
	respMap, ok := exported.(map[string]interface{})
	if !ok {
		text, isString := exported.(string)
		if !isString {
			return ParamCheckResponse{}, fmt.Errorf("%s must return an object", checkName)
		}
		if err := json.Unmarshal([]byte(text), &respMap); err != nil {
			return ParamCheckResponse{}, fmt.Errorf("%s string response must be JSON: %w", checkName, err)
		}
	}

	success, ok := respMap["success"].(bool)
	if !ok {
		return ParamCheckResponse{}, fmt.Errorf("%s response success must be boolean", checkName)
	}

	status, ok := parseStatusCode(respMap["status"])
	if !ok {
		return ParamCheckResponse{}, fmt.Errorf("%s response status must be a number", checkName)
	}
	if status < 100 || status > 599 {
		return ParamCheckResponse{}, fmt.Errorf("%s response status is out of range: %d", checkName, status)
	}

	return ParamCheckResponse{
		Success: success,
		Status:  status,
		Result:  respMap["result"],
	}, nil
}

func newParamCheckError(status int, message string) ParamCheckResponse {
	return ParamCheckResponse{
		Success: false,
		Status:  status,
		Result: map[string]interface{}{
			"message": message,
		},
	}
}

func writeParamCheckResponse(c *gin.Context, resp ParamCheckResponse) {
	status := resp.Status
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
		resp.Status = status
	}
	c.JSON(status, resp)
}

func runParamCheck(c *gin.Context, apiMap map[string]interface{}, execDir string, allParams map[string]interface{}) (bool, bool) {
	checkOnly := isCheckOnlyMode(allParams)
	paramCheckPath := getAPIString(apiMap, "paramCheck", "paramcheck", "check")
	if paramCheckPath == "" {
		if checkOnly {
			writeParamCheckResponse(c, ParamCheckResponse{
				Success: true,
				Status:  http.StatusOK,
				Result:  nil,
			})
			return false, true
		}
		return true, false
	}

	c.Writer.Header().Set("Cache-Control", "no-store")
	c.Writer.Header().Set("Pragma", "no-cache")

	fullPath, err := resolvePath(execDir, paramCheckPath)
	if err != nil {
		writeParamCheckResponse(c, newParamCheckError(http.StatusInternalServerError, err.Error()))
		return false, true
	}
	resultValue, err := runJavaScriptValue(fullPath, allParams, c)
	if err != nil {
		writeParamCheckResponse(c, newParamCheckError(http.StatusInternalServerError, err.Error()))
		return false, true
	}
	checkResponse, err := parseCheckResponse(resultValue, "paramCheck")
	if err != nil {
		writeParamCheckResponse(c, newParamCheckError(http.StatusInternalServerError, err.Error()))
		return false, true
	}

	allowed := checkResponse.Success && checkResponse.Status == http.StatusOK
	if checkOnly || !allowed {
		writeParamCheckResponse(c, checkResponse)
		return allowed, true
	}
	return true, false
}

func runOutCheck(c *gin.Context, apiMap map[string]interface{}, execDir string, allParams map[string]interface{}, response APIResponse) bool {
	handled, checkResponse, err := runOutCheckResponse(apiMap, execDir, allParams, response, c)
	if !handled {
		return false
	}
	if err != nil {
		writeParamCheckResponse(c, newParamCheckError(http.StatusInternalServerError, err.Error()))
		return true
	}
	writeParamCheckResponse(c, checkResponse)
	return true
}

func runOutCheckResponse(apiMap map[string]interface{}, execDir string, allParams map[string]interface{}, response APIResponse, ginCtx *gin.Context) (bool, ParamCheckResponse, error) {
	outCheckPath := getAPIString(apiMap, "outCheck", "outcheck")
	if outCheckPath == "" {
		return false, ParamCheckResponse{}, nil
	}

	checkParams := cloneParams(allParams)
	bodyString := string(response.Body)
	bodyBase64 := base64.StdEncoding.EncodeToString(response.Body)
	checkParams["nyan_output"] = map[string]interface{}{
		"status":          response.Status,
		"contentType":     response.ContentType,
		"headers":         response.Headers,
		"body":            bodyString,
		"bodyBase64":      bodyBase64,
		"bodyLength":      len(response.Body),
		"bodyLengthBytes": len(response.Body),
	}
	checkParams["nyan_output_status"] = response.Status
	checkParams["nyan_output_content_type"] = response.ContentType
	checkParams["nyan_output_body"] = bodyString
	checkParams["nyan_output_body_base64"] = bodyBase64

	fullPath, err := resolvePath(execDir, outCheckPath)
	if err != nil {
		return true, ParamCheckResponse{}, err
	}
	resultValue, err := runJavaScriptValue(fullPath, checkParams, ginCtx)
	if err != nil {
		return true, ParamCheckResponse{}, err
	}
	checkResponse, err := parseCheckResponse(resultValue, "outCheck")
	if err != nil {
		return true, ParamCheckResponse{}, err
	}
	if checkResponse.Success && checkResponse.Status == http.StatusOK {
		return false, ParamCheckResponse{}, nil
	}
	return true, checkResponse, nil
}

func registerPublicEndpoint(r *gin.Engine, endpoint string, apiMap map[string]interface{}, execDir string) {
	routePath := "/" + strings.Trim(strings.TrimSpace(endpoint), "/")
	if routePath == "/" {
		logger.Printf("public endpoint %q is invalid: endpoint name must not be empty", endpoint)
		return
	}

	publicPath := getAPIString(apiMap, "path")
	if publicPath == "" {
		logger.Printf("public endpoint %s: path is missing", endpoint)
	}
	_, pathErr := resolvePath(execDir, publicPath)
	if pathErr != nil {
		logger.Printf("public endpoint %s: invalid path %q: %v", endpoint, publicPath, pathErr)
	}

	handler := func(c *gin.Context) {
		requestedPath := strings.TrimPrefix(c.Param("filepath"), "/")
		if files, err := loadJSONFile(apiJSONPath(execDir)); err == nil {
			if latest, ok := files[endpoint].(map[string]interface{}); ok && getAPIType(latest) == apiTypeAPI && requestedPath == "" {
				executeAPIEndpoint(c, endpoint, execDir)
				return
			}
		}
		servePublicEndpoint(c, endpoint, requestedPath, execDir, apiMap)
	}

	r.GET(routePath, handler)
	r.HEAD(routePath, handler)
	r.GET(routePath+"/*filepath", handler)
	r.HEAD(routePath+"/*filepath", handler)
}

func servePublicEndpoint(c *gin.Context, endpoint, requestedPath, execDir string, fallback map[string]interface{}) {
	files, err := loadJSONFile(apiJSONPath(execDir))
	if err != nil {
		if fallback == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load API configuration", "detail": err.Error()})
			return
		}
		files = map[string]interface{}{endpoint: fallback}
	}
	latestRaw, exists := files[endpoint]
	latest, ok := latestRaw.(map[string]interface{})
	if !exists || !ok || getAPIType(latest) != apiTypePublic {
		c.Status(http.StatusNotFound)
		return
	}
	publicPath := getAPIString(latest, "path")
	if publicPath == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "public path is missing"})
		return
	}
	basePath, err := resolvePath(execDir, publicPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "public path is invalid", "detail": err.Error()})
		return
	}
	allParams, err := collectRequestParams(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON data", "detail": err.Error()})
		return
	}
	allParams["api"] = endpoint
	allParams["nyan_public_endpoint"] = endpoint
	allParams["nyan_public_path"] = requestedPath
	ginContext = c
	defer func() { ginContext = nil }()
	if allowed, handled := runParamCheck(c, latest, execDir, allParams); handled || !allowed {
		return
	}
	if requestedPath == "" || !filepath.IsLocal(requestedPath) {
		c.Status(http.StatusNotFound)
		return
	}
	filePath := filepath.Join(basePath, requestedPath)
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			c.Status(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read public file"})
		return
	}
	if fileInfo.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	if getAPIString(latest, "outCheck", "outcheck") != "" {
		fileContent, err := os.ReadFile(filePath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read public file"})
			return
		}
		response := APIResponse{Status: http.StatusOK, ContentType: http.DetectContentType(fileContent), Headers: map[string]string{}, Body: fileContent}
		if handled := runOutCheck(c, latest, execDir, allParams, response); handled {
			return
		}
		http.ServeContent(c.Writer, c.Request, fileInfo.Name(), fileInfo.ModTime(), bytes.NewReader(fileContent))
		return
	}
	c.File(filePath)
}

// registerDynamicEndpoints は api.json の内容に基づいてルート直下のエンドポイントを登録する関数です。
// registerDynamicEndpoints は api.json の内容に基づいてルート直下のエンドポイントを登録する関数です。
func registerDynamicEndpoints(r *gin.Engine, execDir string) error {
	apiConf, err := loadJSONFile(apiJSONPath(execDir))
	if err != nil {
		return fmt.Errorf("failed to load api.json: %v", err)
	}

	// 予約パスは動的登録しない（固定ハンドラを優先させる）
	reserved := map[string]struct{}{
		"nyan":         {},
		"nyan-rpc":     {},
		"nyan-toolbox": {},
	}

	for apiName, apiRaw := range apiConf {
		apiMap, ok := apiRaw.(map[string]interface{})
		if !ok {
			continue
		}

		// 予約パスのスキップ（念のため "nyan-" プレフィックスも抑止）
		if _, ok := reserved[apiName]; ok || strings.HasPrefix(apiName, "nyan-") {
			continue
		}

		apiType := getAPIType(apiMap)
		if apiType == apiTypePublic {
			registerPublicEndpoint(r, apiName, apiMap, execDir)
			continue
		}
		if apiType != apiTypeAPI {
			continue
		}

		currentAPIName := apiName
		r.Any("/"+currentAPIName, func(c *gin.Context) { executeAPIEndpoint(c, currentAPIName, execDir) })
		if !strings.HasPrefix(currentAPIName, "api/") {
			r.Any("/api/"+currentAPIName, func(c *gin.Context) { executeAPIEndpoint(c, currentAPIName, execDir) })
		}
	}
	return nil
}

func executeAPIEndpoint(c *gin.Context, apiName, execDir string) {
	if websocket.IsWebSocketUpgrade(c.Request) {
		c.Set("nyan_api_name", apiName)
		handleWebSocket(c)
		return
	}
	allParams, err := collectRequestParams(c)
	if err != nil {
		respondWithError(c, http.StatusBadRequest, "Invalid JSON data", err)
		return
	}
	allParams["api"] = apiName
	scriptListData, err := loadJSONFile(apiJSONPath(execDir))
	if err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to load API configuration", err)
		return
	}
	scriptInfo, ok := scriptListData[apiName].(map[string]interface{})
	if !ok || getAPIType(scriptInfo) != apiTypeAPI {
		respondWithError(c, http.StatusNotFound, fmt.Sprintf("API config not found for key: %s", apiName), nil)
		return
	}
	if allowed, handled := runParamCheck(c, scriptInfo, execDir, allParams); handled || !allowed {
		return
	}
	scriptPath := getAPIString(scriptInfo, "script")
	if scriptPath == "" {
		respondWithError(c, http.StatusBadRequest, fmt.Sprintf("Script path not found for key: %s", apiName), nil)
		return
	}
	fullScriptPath, err := resolvePath(execDir, scriptPath)
	if err != nil {
		respondWithError(c, http.StatusBadRequest, fmt.Sprintf("Invalid script path for key: %s", apiName), err)
		return
	}
	result, err := runJavaScript(fullScriptPath, allParams, c)
	if err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to run JavaScript", err)
		return
	}
	responseBody := []byte(result)
	response := APIResponse{Status: http.StatusOK, ContentType: "application/json", Headers: map[string]string{}, Body: responseBody}
	var jsonData map[string]interface{}
	if err := json.Unmarshal(responseBody, &jsonData); err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to parse JavaScript response", err)
		return
	}
	status, ok := jsonData["status"].(float64)
	if !ok {
		respondWithError(c, http.StatusInternalServerError, "Status field not found in JavaScript response", nil)
		return
	}
	response.Status = int(status)
	if handled := runOutCheck(c, scriptInfo, execDir, allParams, response); handled {
		return
	}
	performPush(scriptInfo, scriptListData, allParams, execDir)
	c.JSON(response.Status, jsonData)
}

// dispatchDynamicEndpoint handles API/public definitions added after startup.
// Routes that existed at startup continue to use their registered Gin handlers.
func dispatchDynamicEndpoint(c *gin.Context, execDir string) bool {
	requestPath := strings.Trim(strings.TrimSpace(c.Request.URL.Path), "/")
	if requestPath == "" {
		return false
	}
	files := currentAPIFiles()
	apiName := requestPath
	if raw, ok := files[apiName].(map[string]interface{}); ok && getAPIType(raw) == apiTypeAPI {
		executeAPIEndpoint(c, apiName, execDir)
		return true
	}
	if strings.HasPrefix(requestPath, "api/") {
		apiName = strings.TrimPrefix(requestPath, "api/")
		if raw, ok := files[apiName].(map[string]interface{}); ok && getAPIType(raw) == apiTypeAPI {
			executeAPIEndpoint(c, apiName, execDir)
			return true
		}
	}

	matchedName := ""
	matchedPath := ""
	for name, raw := range files {
		entry, ok := raw.(map[string]interface{})
		if !ok || getAPIType(entry) != apiTypePublic {
			continue
		}
		cleanName := strings.Trim(name, "/")
		if requestPath == cleanName || strings.HasPrefix(requestPath, cleanName+"/") {
			if len(cleanName) > len(matchedPath) {
				matchedName = name
				matchedPath = cleanName
			}
		}
	}
	if matchedName == "" {
		return false
	}
	requestedPath := strings.TrimPrefix(requestPath, matchedPath)
	requestedPath = strings.TrimPrefix(requestedPath, "/")
	servePublicEndpoint(c, matchedName, requestedPath, execDir, nil)
	return true
}

type wsClientConfig struct {
	name        string
	scriptPath  string
	connectURL  string
	description string
}

type triggerConfig struct {
	Type  string
	Value string
}

type scheduleJobConfig struct {
	name        string
	scriptPath  string
	trigger     triggerConfig
	description string
	schedule    cronSchedule
}

type cronSchedule struct {
	minutes     cronField
	hours       cronField
	days        cronField
	months      cronField
	weekdays    cronField
	dayStar     bool
	weekdayStar bool
}

type cronField map[int]bool

func parseCronSchedule(expr string) (cronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronSchedule{}, fmt.Errorf("cron expression must have 5 fields")
	}

	minutes, _, err := parseCronField(fields[0], 0, 59, false)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("minute field: %w", err)
	}
	hours, _, err := parseCronField(fields[1], 0, 23, false)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("hour field: %w", err)
	}
	days, dayStar, err := parseCronField(fields[2], 1, 31, false)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("day field: %w", err)
	}
	months, _, err := parseCronField(fields[3], 1, 12, false)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("month field: %w", err)
	}
	weekdays, weekdayStar, err := parseCronField(fields[4], 0, 7, true)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("weekday field: %w", err)
	}

	return cronSchedule{
		minutes:     minutes,
		hours:       hours,
		days:        days,
		months:      months,
		weekdays:    weekdays,
		dayStar:     dayStar,
		weekdayStar: weekdayStar,
	}, nil
}

func parseCronField(field string, minValue, maxValue int, normalizeSunday bool) (cronField, bool, error) {
	values := make(cronField)
	isStar := field == "*"
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return nil, false, fmt.Errorf("empty list item")
		}

		step := 1
		base := part
		if strings.Contains(part, "/") {
			stepParts := strings.Split(part, "/")
			if len(stepParts) != 2 || stepParts[0] == "" || stepParts[1] == "" {
				return nil, false, fmt.Errorf("invalid step %q", part)
			}
			base = stepParts[0]
			parsedStep, err := strconv.Atoi(stepParts[1])
			if err != nil || parsedStep <= 0 {
				return nil, false, fmt.Errorf("invalid step %q", part)
			}
			step = parsedStep
		}

		start, end, err := cronRange(base, minValue, maxValue)
		if err != nil {
			return nil, false, err
		}
		for value := start; value <= end; value += step {
			normalized := value
			if normalizeSunday && normalized == 7 {
				normalized = 0
			}
			values[normalized] = true
		}
	}

	return values, isStar, nil
}

func cronRange(base string, minValue, maxValue int) (int, int, error) {
	if base == "*" {
		return minValue, maxValue, nil
	}
	if strings.Contains(base, "-") {
		rangeParts := strings.Split(base, "-")
		if len(rangeParts) != 2 || rangeParts[0] == "" || rangeParts[1] == "" {
			return 0, 0, fmt.Errorf("invalid range %q", base)
		}
		start, err := strconv.Atoi(rangeParts[0])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid range start %q", base)
		}
		end, err := strconv.Atoi(rangeParts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid range end %q", base)
		}
		if start > end {
			return 0, 0, fmt.Errorf("range start is greater than end %q", base)
		}
		if start < minValue || end > maxValue {
			return 0, 0, fmt.Errorf("range %q is out of bounds %d-%d", base, minValue, maxValue)
		}
		return start, end, nil
	}

	value, err := strconv.Atoi(base)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid value %q", base)
	}
	if value < minValue || value > maxValue {
		return 0, 0, fmt.Errorf("value %q is out of bounds %d-%d", base, minValue, maxValue)
	}
	return value, value, nil
}

func (s cronSchedule) next(after time.Time) time.Time {
	next := after.Truncate(time.Minute).Add(time.Minute)
	limit := next.AddDate(5, 0, 0)
	for next.Before(limit) {
		if s.matches(next) {
			return next
		}
		next = next.Add(time.Minute)
	}
	return time.Time{}
}

func (s cronSchedule) matches(t time.Time) bool {
	weekday := int(t.Weekday())
	dayMatches := s.days[t.Day()]
	weekdayMatches := s.weekdays[weekday]
	switch {
	case !s.dayStar && !s.weekdayStar:
		if !dayMatches && !weekdayMatches {
			return false
		}
	case !dayMatches || !weekdayMatches:
		return false
	}

	return s.minutes[t.Minute()] &&
		s.hours[t.Hour()] &&
		s.months[int(t.Month())]
}

func getTriggerConfig(entry map[string]interface{}) triggerConfig {
	triggerRaw, ok := entry["trigger"].(map[string]interface{})
	if !ok {
		return triggerConfig{}
	}
	triggerString := func(key string) string {
		if value, ok := triggerRaw[key].(string); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	return triggerConfig{
		Type:  triggerString("type"),
		Value: triggerString("value"),
	}
}

func websocketMessageTypeLabel(t int) string {
	switch t {
	case websocket.TextMessage:
		return "text"
	case websocket.BinaryMessage:
		return "binary"
	case websocket.CloseMessage:
		return "close"
	case websocket.PingMessage:
		return "ping"
	case websocket.PongMessage:
		return "pong"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
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
						pushScriptPath := resolvePathFromBase(execDir, pushScript)
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
	execDir = apiBaseDir(execDir)

	// api.json を読み込み
	apiJsonPath := apiJSONPath(execDir)
	apiConf, err := loadJSONFile(apiJsonPath)
	if err != nil {
		respondWithError(c, http.StatusInternalServerError, "Failed to load API configuration", err)
		return
	}

	// 公開済みのAPI定義mapは変更せず、レスポンス用のコピーからscriptを除外する。
	responseAPIs := make(map[string]interface{}, len(apiConf))
	for key, api := range apiConf {
		if apiMap, ok := api.(map[string]interface{}); ok {
			responseMap := make(map[string]interface{}, len(apiMap))
			for field, value := range apiMap {
				if field != "script" {
					responseMap[field] = value
				}
			}
			responseMap["type"] = getAPIType(apiMap)
			responseAPIs[key] = responseMap
			continue
		}
		responseAPIs[key] = api
	}

	// config.json の値は globalConfig に保持されている想定
	nyanInfo := map[string]interface{}{
		"name":    globalConfig.Name,
		"profile": globalConfig.Profile,
		"version": getVersion(),
	}

	response := NyanResponse{
		Nyan: nyanInfo,
		Apis: responseAPIs,
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
	execDir = apiBaseDir(execDir)

	// api.json を読み込み
	apiJsonPath := apiJSONPath(execDir)
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
	apiType := getAPIType(apiData)

	// JavaScriptのパスを取得（なければ空文字のまま）
	scriptPath, _ := apiData["script"].(string)
	nyanAcceptedParams := map[string]interface{}{}
	nyanOutputColumns := []interface{}{}

	if scriptPath != "" {
		fullScriptPath := resolvePathFromBase(execDir, scriptPath)
		scriptContent, err := ioutil.ReadFile(fullScriptPath)
		if err == nil {
			// スクリプト内から const nyanAcceptedParams, nyanOutputColumns をパース
			nyanAcceptedParams = parseConstObject(scriptContent, "nyanAcceptedParams")
			nyanOutputColumns = parseConstArray(scriptContent, "nyanOutputColumns")
		}
	}

	// 結果JSONを作成
	result := map[string]interface{}{
		"api":                apiName,
		"type":               apiType,
		"description":        description,
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
	setupGojaVMWithSnapshot(vm, currentAPISnapshot(), ginCtx)
}

func setupGojaVMWithSnapshot(vm *goja.Runtime, snapshot *APIConfigSnapshot, ginCtx *gin.Context) {

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

	jsonAPIFunc := func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		data := call.Argument(1).String()
		user := call.Argument(2).String()
		pass := call.Argument(3).String()

		var hdr map[string]string
		if len(call.Arguments) >= 5 {
			hdr = extractHeaders(call.Argument(4))
		}
		res, err := jsonAPI(url, []byte(data), user, pass, hdr)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(res)
	}
	vm.Set("nyanJsonAPI", jsonAPIFunc)
	vm.Set("nyanCallAPI", jsonAPIFunc)

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

	vm.Set("nyanCallMe", func(call goja.FunctionCall) goja.Value {
		apiName := "hello2"
		params := map[string]interface{}{}

		if len(call.Arguments) >= 1 {
			raw := call.Argument(0).Export()
			if raw != nil {
				if m, ok := raw.(map[string]interface{}); ok {
					params = m
				} else if obj, ok := call.Argument(0).(*goja.Object); ok {
					exported := obj.Export()
					if m, ok := exported.(map[string]interface{}); ok {
						params = m
					}
				}
			}
		}

		if v, ok := params["api"]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				apiName = s
			}
		}
		params["api"] = apiName

		result, err := callNyanAPIFromVMWithSnapshot(snapshot, apiName, params, ginCtx)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		var parsed interface{}
		if err := json.Unmarshal([]byte(result), &parsed); err == nil {
			return vm.ToValue(parsed)
		}
		return vm.ToValue(result)
	})

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
			"filename":    filepath.Base(abs),
			"contentType": ctype,
			"dataBase64":  base64.StdEncoding.EncodeToString(data),
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
	execDir = apiBaseDir(execDir)

	// api.jsonの読み込み
	apiJsonPath := apiJSONPath(execDir)
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
	if getAPIType(scriptInfo) != apiTypeAPI {
		respondJSONRPCError(c, rpcReq.ID, -32601, fmt.Sprintf("Method is not an API endpoint: %s", rpcReq.Method), nil)
		return
	}

	// JSON-RPCのparamsを元にパラメータマップを構築
	allParams := make(map[string]interface{})
	for k, v := range rpcReq.Params {
		allParams[k] = v
	}
	// 既存ロジックが「api」パラメータを参照するために設定
	allParams["api"] = rpcReq.Method

	checkOnly := isCheckOnlyMode(allParams)
	paramCheckPath := getAPIString(scriptInfo, "paramCheck", "paramcheck", "check")
	if paramCheckPath == "" && checkOnly {
		c.JSON(http.StatusOK, JSONRPCResponse{
			JSONRPC: "2.0",
			Result: ParamCheckResponse{
				Success: true,
				Status:  http.StatusOK,
				Result:  nil,
			},
			ID: rpcReq.ID,
		})
		return
	}
	if paramCheckPath != "" {
		fullCheckPath, err := resolvePath(execDir, paramCheckPath)
		if err != nil {
			respondJSONRPCError(c, rpcReq.ID, -32603, "Check script error", err.Error())
			return
		}
		resultValue, err := runJavaScriptValue(fullCheckPath, allParams, c)
		if err != nil {
			respondJSONRPCError(c, rpcReq.ID, -32603, "Check script error", err.Error())
			return
		}
		checkResponse, err := parseCheckResponse(resultValue, "paramCheck")
		if err != nil {
			respondJSONRPCError(c, rpcReq.ID, -32603, "Check script error", err.Error())
			return
		}
		allowed := checkResponse.Success && checkResponse.Status == http.StatusOK
		if checkOnly {
			c.JSON(checkResponse.Status, JSONRPCResponse{
				JSONRPC: "2.0",
				Result:  checkResponse,
				ID:      rpcReq.ID,
			})
			return
		}
		if !allowed {
			respondJSONRPCError(c, rpcReq.ID, -32602, "Invalid params", checkResponse)
			return
		}
	}

	// スクリプトファイルのパス取得
	scriptPath := getAPIString(scriptInfo, "script")
	if scriptPath == "" {
		respondJSONRPCError(c, rpcReq.ID, -32603, fmt.Sprintf("No script path for method: %s", rpcReq.Method), nil)
		return
	}
	fullPath, err := resolvePath(execDir, scriptPath)
	if err != nil {
		respondJSONRPCError(c, rpcReq.ID, -32603, fmt.Sprintf("Invalid script path for method: %s", rpcReq.Method), err.Error())
		return
	}

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

	statusCode := http.StatusOK
	if statusVal, ok := jsResult["status"]; ok {
		if parsed, ok := parseStatusCode(statusVal); ok {
			statusCode = parsed
		}
	}
	if handled, checkResponse, err := runOutCheckResponse(scriptInfo, execDir, allParams, APIResponse{
		Status:      statusCode,
		ContentType: "application/json",
		Headers:     map[string]string{},
		Body:        []byte(resultStr),
	}, c); handled {
		if err != nil {
			respondJSONRPCError(c, rpcReq.ID, -32603, "outCheck script error", err.Error())
			return
		}
		c.JSON(checkResponse.Status, JSONRPCResponse{
			JSONRPC: "2.0",
			Result:  checkResponse,
			ID:      rpcReq.ID,
		})
		return
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
	to, cc, bcc []string, // 宛先
	subject, body string, // 件名・本文
	isHTML bool, // true=HTML  false=プレーン
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
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)

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
		c.AbortWithStatusJSON(http.StatusBadRequest, map[string]any{"error": "bad json"})
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
		if !supportedProto[ver] {
			ver = defaultProto
		} // 最低限の互換を返す

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
				"version": getVersion(),
			},
		}
		c.JSON(http.StatusOK, map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": res,
		})
		return

	case "notifications/initialized":
		c.Status(http.StatusAccepted) // 202・ボディ無し
		return

	case "ping":
		c.JSON(http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		return
	}

	// initialize 以外はセッションとプロトコルヘッダを検証
	sid := c.GetHeader("Mcp-Session-Id")
	if _, ok := sessions.Load(sid); !ok {
		c.AbortWithStatus(http.StatusNotFound) // 404 → クライアントは再 initialize
		return
	}
	proto := c.GetHeader("MCP-Protocol-Version")
	if proto == "" {
		proto = defaultProto
	}
	if !supportedProto[proto] {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	switch req.Method {
	case "tools/list":
		// api.json → Tool 定義（inputSchema は camelCase）
		result := buildToolsList() // []Tool と nextCursor を返す自前関数
		c.JSON(http.StatusOK, map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": result,
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
			"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": stringOrJSON(out)}},
			},
		})
		return

	default:
		c.JSON(http.StatusOK, map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": -32601, "message": "Method not found"},
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
	execDir = apiBaseDir(execDir)
	apiConfPath := apiJSONPath(execDir)
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
		if getAPIType(api) != apiTypeAPI {
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
			full := resolvePathFromBase(execDir, scriptPath)
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
	execDir = apiBaseDir(execDir)
	apiConfPath := apiJSONPath(execDir)
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
	if getAPIType(api) != apiTypeAPI {
		return fmt.Sprintf(`{"status":400,"error":"tool is not an API endpoint: %s"}`, toolName)
	}
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

	checkOnly := isCheckOnlyMode(allParams)
	paramCheckPath := getAPIString(api, "paramCheck", "paramcheck", "check")
	if paramCheckPath == "" && checkOnly {
		out, _ := json.Marshal(ParamCheckResponse{Success: true, Status: http.StatusOK, Result: nil})
		return string(out)
	}
	if paramCheckPath != "" {
		fullCheckPath, err := resolvePath(execDir, paramCheckPath)
		if err != nil {
			return fmt.Sprintf(`{"status":500,"error":%q}`, err.Error())
		}
		resultValue, err := runJavaScriptValue(fullCheckPath, allParams, c)
		if err != nil {
			return fmt.Sprintf(`{"status":500,"error":%q}`, err.Error())
		}
		checkResponse, err := parseCheckResponse(resultValue, "paramCheck")
		if err != nil {
			return fmt.Sprintf(`{"status":500,"error":%q}`, err.Error())
		}
		allowed := checkResponse.Success && checkResponse.Status == http.StatusOK
		if checkOnly || !allowed {
			out, _ := json.Marshal(checkResponse)
			return string(out)
		}
	}

	scriptPath := getAPIString(api, "script")
	if scriptPath == "" {
		return `{"status":400,"error":"no script path"}`
	}

	fullScript, err := resolvePath(execDir, scriptPath)
	if err != nil {
		return fmt.Sprintf(`{"status":500,"error":%q}`, err.Error())
	}

	out, err := runJavaScript(fullScript, allParams, c)
	if err != nil {
		return fmt.Sprintf(`{"status":500,"error":%q}`, err.Error())
	}

	statusCode := http.StatusOK
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(out), &body); err == nil {
		if parsed, ok := parseStatusCode(body["status"]); ok {
			statusCode = parsed
		}
	}
	if handled, checkResponse, err := runOutCheckResponse(api, execDir, allParams, APIResponse{
		Status:      statusCode,
		ContentType: "application/json",
		Headers:     map[string]string{},
		Body:        []byte(out),
	}, c); handled {
		if err != nil {
			return fmt.Sprintf(`{"status":500,"error":%q}`, err.Error())
		}
		out, _ := json.Marshal(checkResponse)
		return string(out)
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

const defaultAPIHotReloadCheckInterval = time.Second

type APIHotReloadConfig struct {
	Enabled  bool   `json:"Enabled"`
	Interval string `json:"Interval"`
}

var (
	apiSnapshot        atomic.Pointer[APIConfigSnapshot]
	backgroundRuntimes *backgroundRuntimeManager
)

type APIConfigSnapshot struct {
	RootPath    string
	Definitions map[string]interface{}
	Sources     map[string]string
	FileStates  map[string]APIFileState
	Schedules   map[string]scheduleJobConfig
	WSClients   map[string]wsClientConfig
}

type APIFileState struct {
	Path   string
	Exists bool
	Hash   [sha256.Size]byte
	Error  string
}

type apiConfigLoadResult struct {
	Snapshot *APIConfigSnapshot
	Hash     [sha256.Size]byte
}

func applyConfigDefaults(target *Config) {
	target.APIHotReload.Enabled = true
	target.APIHotReload.Interval = defaultAPIHotReloadCheckInterval.String()
}

func parseAPIHotReloadInterval(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultAPIHotReloadCheckInterval, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if interval <= 0 {
		return 0, fmt.Errorf("must be greater than zero")
	}
	return interval, nil
}

func readAPIFile(apiFilePath string) (map[string]interface{}, [sha256.Size]byte, error) {
	result, err := readAPIConfigFile(apiFilePath, filepath.Dir(apiFilePath))
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	return result.Snapshot.Definitions, result.Hash, nil
}

func readAPIConfigFile(apiFilePath, apiBaseDir string) (*apiConfigLoadResult, error) {
	result, _, err := readAPIConfigFileAttempt(apiFilePath, apiBaseDir)
	if err != nil {
		return nil, err
	}
	if err := verifyAPIFileStates(result.Snapshot.FileStates); err != nil {
		return nil, err
	}
	return result, nil
}

func loadAPIConfigData(apiFilePath, apiBaseDir string, data []byte) (*apiConfigLoadResult, error) {
	result, _, err := loadAPIConfigDataAttempt(apiFilePath, apiBaseDir, data)
	return result, err
}

func readAPIConfigFileAttempt(apiFilePath, apiBaseDir string) (*apiConfigLoadResult, map[string]APIFileState, error) {
	normalizedPath, err := normalizeAPIFilePath(apiFilePath)
	if err != nil {
		return nil, nil, err
	}
	identity, state, data, err := inspectAPIFile(normalizedPath)
	discovered := map[string]APIFileState{identity: state}
	if err != nil {
		return nil, discovered, fmt.Errorf("read api file %s: %w", normalizedPath, err)
	}
	result, states, err := loadAPIConfigDataAttempt(normalizedPath, apiBaseDir, data)
	return result, mergeAPIFileStates(discovered, states), err
}

func loadAPIConfigDataAttempt(apiFilePath, apiBaseDir string, data []byte) (*apiConfigLoadResult, map[string]APIFileState, error) {
	definitions, sources, fileStates, err := expandAPIFileGraphWithMetadata(apiFilePath, data)
	if err != nil {
		return nil, fileStates, err
	}
	schedules, err := buildScheduleJobConfigs(definitions, apiBaseDir)
	if err != nil {
		return nil, fileStates, err
	}
	wsClients, err := buildWSClientConfigs(definitions, apiBaseDir)
	if err != nil {
		return nil, fileStates, err
	}
	return &apiConfigLoadResult{
		Snapshot: newAPIConfigSnapshot(apiFilePath, definitions, sources, fileStates, schedules, wsClients),
		Hash:     sha256.Sum256(data),
	}, fileStates, nil
}

func decodeAPIFile(data []byte) (map[string]interface{}, error) {
	rawDefinitions, err := decodeRawAPIDefinitions(data)
	if err != nil {
		return nil, err
	}
	files := make(map[string]interface{}, len(rawDefinitions))
	for name, rawDefinition := range rawDefinitions {
		definitionType, err := rawAPIDefinitionType(rawDefinition)
		if err != nil {
			return nil, fmt.Errorf("decode api JSON: definition %q: %w", name, err)
		}
		if definitionType == apiTypeInclude {
			return nil, fmt.Errorf("decode api JSON: include definition %q requires an API file path", name)
		}
		definition, err := decodeAPIDefinition(name, rawDefinition, "")
		if err != nil {
			return nil, err
		}
		files[name] = definition
	}
	return files, nil
}

type apiIncludeDefinition struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

type apiIncludeFrame struct {
	path     string
	identity string
}

type apiGraphLoader struct {
	definitions map[string]interface{}
	sources     map[string]string
	fileStates  map[string]APIFileState
}

func expandAPIFileGraph(rootPath string, rootData []byte) (map[string]interface{}, error) {
	definitions, _, err := expandAPIFileGraphWithSources(rootPath, rootData)
	return definitions, err
}

func expandAPIFileGraphWithSources(rootPath string, rootData []byte) (map[string]interface{}, map[string]string, error) {
	definitions, sources, _, err := expandAPIFileGraphWithMetadata(rootPath, rootData)
	return definitions, sources, err
}

func expandAPIFileGraphWithMetadata(rootPath string, rootData []byte) (map[string]interface{}, map[string]string, map[string]APIFileState, error) {
	normalizedRoot, err := normalizeAPIFilePath(rootPath)
	if err != nil {
		return nil, nil, nil, err
	}
	rootIdentity, err := canonicalExistingAPIFilePath(normalizedRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	loader := &apiGraphLoader{
		definitions: make(map[string]interface{}),
		sources:     make(map[string]string),
		fileStates:  make(map[string]APIFileState),
	}
	if err := loader.loadFile(normalizedRoot, rootIdentity, rootData, "", nil); err != nil {
		return nil, nil, loader.fileStates, err
	}
	return loader.definitions, loader.sources, loader.fileStates, nil
}

func (loader *apiGraphLoader) loadFile(filePath, identity string, data []byte, mountPrefix string, stack []apiIncludeFrame) error {
	if includeFrameIndex(stack, identity) >= 0 {
		return includeCycleError(stack, filePath)
	}
	stack = append(stack, apiIncludeFrame{path: filePath, identity: identity})
	loader.fileStates[identity] = APIFileState{Path: identity, Exists: true, Hash: sha256.Sum256(data)}
	rawDefinitions, err := decodeRawAPIDefinitions(data)
	if err != nil {
		return fmt.Errorf("API file %s: %w", filePath, err)
	}
	definitionTypes, mounts, err := inspectAPIDefinitionTypes(rawDefinitions, filePath)
	if err != nil {
		return err
	}
	if err := validateMountNamespaceConflicts(rawDefinitions, definitionTypes, mounts, filePath); err != nil {
		return err
	}

	for _, name := range sortedRawDefinitionNames(rawDefinitions) {
		rawDefinition := rawDefinitions[name]
		if definitionTypes[name] != apiTypeInclude {
			fullName := joinAPIName(mountPrefix, name)
			definition, err := decodeAPIDefinition(fullName, rawDefinition, filepath.Dir(filePath))
			if err != nil {
				return err
			}
			if previousSource, exists := loader.sources[fullName]; exists {
				return fmt.Errorf("duplicate expanded API name %q from %s and %s", fullName, previousSource, filePath)
			}
			loader.definitions[fullName] = definition
			loader.sources[fullName] = filePath
			continue
		}

		include, err := decodeIncludeDefinition(name, rawDefinition)
		if err != nil {
			return fmt.Errorf("API file %s: %w", filePath, err)
		}
		includePath, err := resolveIncludePath(filePath, include.Path)
		if err != nil {
			return fmt.Errorf("include %q in %s: %w", name, filePath, err)
		}
		includeIdentity, includeState, includeData, err := inspectAPIFile(includePath)
		loader.fileStates[includeIdentity] = includeState
		if err != nil {
			return fmt.Errorf("include %q in %s: %w", name, filePath, err)
		}
		if includeFrameIndex(stack, includeIdentity) >= 0 {
			return includeCycleError(stack, includePath)
		}
		if err := loader.loadFile(includePath, includeIdentity, includeData, joinAPIName(mountPrefix, name), stack); err != nil {
			return err
		}
	}
	return nil
}

func decodeRawAPIDefinitions(data []byte) (map[string]json.RawMessage, error) {
	if err := validateNoDuplicateJSONKeys(data); err != nil {
		return nil, fmt.Errorf("decode api JSON: %w", err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("decode api JSON: top-level value must be an object")
	}
	var definitions map[string]json.RawMessage
	if err := json.Unmarshal(data, &definitions); err != nil {
		return nil, fmt.Errorf("decode api JSON: %w", err)
	}
	if definitions == nil {
		return nil, fmt.Errorf("decode api JSON: top-level value must be an object")
	}
	for name, rawDefinition := range definitions {
		definition := bytes.TrimSpace(rawDefinition)
		if len(definition) == 0 || definition[0] != '{' {
			return nil, fmt.Errorf("decode api JSON: definition %q must be an object", name)
		}
	}
	return definitions, nil
}

func rawAPIDefinitionType(rawDefinition json.RawMessage) (string, error) {
	var typeOnly struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rawDefinition, &typeOnly); err != nil {
		return "", err
	}
	return strings.TrimSpace(typeOnly.Type), nil
}

func decodeAPIDefinition(name string, rawDefinition json.RawMessage, sourceDir string) (map[string]interface{}, error) {
	var definition map[string]interface{}
	if err := json.Unmarshal(rawDefinition, &definition); err != nil {
		return nil, fmt.Errorf("decode api JSON: definition %q: %w", name, err)
	}
	if sourceDir == "" {
		return definition, nil
	}
	for _, key := range []string{"script", "path", "paramCheck", "paramcheck", "check", "outCheck", "outcheck"} {
		rawPath, exists := definition[key]
		if !exists {
			continue
		}
		pathValue, ok := rawPath.(string)
		if !ok || strings.TrimSpace(pathValue) == "" {
			continue
		}
		resolved, err := resolvePath(sourceDir, pathValue)
		if err != nil {
			return nil, fmt.Errorf("decode api JSON: definition %q: invalid %s path %q: %w", name, key, pathValue, err)
		}
		definition[key] = resolved
	}
	return definition, nil
}

func inspectAPIDefinitionTypes(rawDefinitions map[string]json.RawMessage, filePath string) (map[string]string, map[string]struct{}, error) {
	definitionTypes := make(map[string]string, len(rawDefinitions))
	mounts := make(map[string]struct{})
	for _, name := range sortedRawDefinitionNames(rawDefinitions) {
		definitionType, err := rawAPIDefinitionType(rawDefinitions[name])
		if err != nil {
			return nil, nil, fmt.Errorf("API file %s: definition %q: %w", filePath, name, err)
		}
		definitionTypes[name] = definitionType
		if definitionType == apiTypeInclude {
			if err := validateMountName(name); err != nil {
				return nil, nil, fmt.Errorf("API file %s: %w", filePath, err)
			}
			mounts[name] = struct{}{}
		}
	}
	return definitionTypes, mounts, nil
}

func validateMountNamespaceConflicts(rawDefinitions map[string]json.RawMessage, definitionTypes map[string]string, mounts map[string]struct{}, filePath string) error {
	for _, name := range sortedRawDefinitionNames(rawDefinitions) {
		if definitionTypes[name] == apiTypeInclude {
			continue
		}
		for _, mountName := range sortedStringSet(mounts) {
			if name == mountName || strings.HasPrefix(name, mountName+"/") {
				return fmt.Errorf("API file %s: API name %q conflicts with mount namespace %q", filePath, name, mountName)
			}
		}
	}
	return nil
}

func validateMountName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || name == "." || name == ".." || strings.Contains(name, "/") {
		return fmt.Errorf("invalid include mount name %q", name)
	}
	return nil
}

func decodeIncludeDefinition(mountName string, rawDefinition json.RawMessage) (apiIncludeDefinition, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawDefinition, &fields); err != nil {
		return apiIncludeDefinition{}, fmt.Errorf("include %q: %w", mountName, err)
	}
	for field := range fields {
		if field != "type" && field != "path" {
			return apiIncludeDefinition{}, fmt.Errorf("include %q: unsupported field %q; only type and path are allowed", mountName, field)
		}
	}
	var include apiIncludeDefinition
	if err := json.Unmarshal(rawDefinition, &include); err != nil {
		return apiIncludeDefinition{}, fmt.Errorf("include %q: %w", mountName, err)
	}
	if strings.TrimSpace(include.Type) != apiTypeInclude {
		return apiIncludeDefinition{}, fmt.Errorf("include %q: type must be %q", mountName, apiTypeInclude)
	}
	if strings.TrimSpace(include.Path) == "" {
		return apiIncludeDefinition{}, fmt.Errorf("include %q: path is empty", mountName)
	}
	return include, nil
}

func sortedRawDefinitionNames(definitions map[string]json.RawMessage) []string {
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedStringSet(values map[string]struct{}) []string {
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func joinAPIName(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

func includeFrameIndex(stack []apiIncludeFrame, identity string) int {
	for index, frame := range stack {
		if frame.identity == identity {
			return index
		}
	}
	return -1
}

func includeCycleError(stack []apiIncludeFrame, repeatedPath string) error {
	cycle := make([]string, 0, len(stack)+1)
	for _, frame := range stack {
		cycle = append(cycle, frame.path)
	}
	cycle = append(cycle, repeatedPath)
	return fmt.Errorf("include cycle detected:\n%s", strings.Join(cycle, "\n-> "))
}

func normalizeAPIFilePath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve api file path %q: %w", path, err)
	}
	return filepath.Clean(absolutePath), nil
}

func inspectAPIFile(path string) (string, APIFileState, []byte, error) {
	normalizedPath, err := normalizeAPIFilePath(path)
	if err != nil {
		state := APIFileState{Path: path, Error: "invalid_path"}
		return path, state, nil, err
	}
	info, err := os.Stat(normalizedPath)
	if err != nil {
		state := APIFileState{Path: normalizedPath}
		if os.IsNotExist(err) {
			state.Error = "not_found"
			return normalizedPath, state, nil, fmt.Errorf("file not found: %s", normalizedPath)
		}
		state.Error = "stat_error"
		return normalizedPath, state, nil, fmt.Errorf("file cannot be accessed: %s: %w", normalizedPath, err)
	}
	if !info.Mode().IsRegular() {
		state := APIFileState{Path: normalizedPath, Exists: true, Error: "not_regular"}
		return normalizedPath, state, nil, fmt.Errorf("path is not a regular file: %s", normalizedPath)
	}
	evaluatedPath, err := filepath.EvalSymlinks(normalizedPath)
	if err != nil {
		state := APIFileState{Path: normalizedPath, Exists: true, Error: "symlink_error"}
		return normalizedPath, state, nil, fmt.Errorf("resolve symlinks for %s: %w", normalizedPath, err)
	}
	identity, err := normalizeAPIFilePath(evaluatedPath)
	if err != nil {
		state := APIFileState{Path: normalizedPath, Exists: true, Error: "invalid_path"}
		return normalizedPath, state, nil, err
	}
	data, err := os.ReadFile(normalizedPath)
	if err != nil {
		state := APIFileState{Path: identity, Exists: true, Error: "read_error"}
		return identity, state, nil, fmt.Errorf("file cannot be read: %s: %w", normalizedPath, err)
	}
	state := APIFileState{Path: identity, Exists: true, Hash: sha256.Sum256(data)}
	return identity, state, data, nil
}

func canonicalExistingAPIFilePath(path string) (string, error) {
	normalizedPath, err := normalizeAPIFilePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(normalizedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", normalizedPath)
		}
		return "", fmt.Errorf("file cannot be accessed: %s: %w", normalizedPath, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file: %s", normalizedPath)
	}
	evaluatedPath, err := filepath.EvalSymlinks(normalizedPath)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks for %s: %w", normalizedPath, err)
	}
	return normalizeAPIFilePath(evaluatedPath)
}

func resolveIncludePath(parentAPIPath, includePath string) (string, error) {
	resolvedPath := includePath
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(filepath.Dir(parentAPIPath), resolvedPath)
	}
	return normalizeAPIFilePath(resolvedPath)
}

func validateNoDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder, "$"); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected token after top-level value: %v", token)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key at %s is not a string", path)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate key %q at %s", key, path)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder, path+"["+strconv.Quote(key)+"]"); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("object at %s is not closed", path)
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := consumeJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("array at %s is not closed", path)
		}
	default:
		return fmt.Errorf("unexpected delimiter %q at %s", delimiter, path)
	}
	return nil
}

func newAPIConfigSnapshot(rootPath string, definitions map[string]interface{}, sources map[string]string, fileStates map[string]APIFileState, schedules map[string]scheduleJobConfig, wsClients map[string]wsClientConfig) *APIConfigSnapshot {
	normalizedRoot, err := normalizeAPIFilePath(rootPath)
	if err != nil {
		normalizedRoot = filepath.Clean(rootPath)
	}
	return &APIConfigSnapshot{
		RootPath:    normalizedRoot,
		Definitions: cloneAPIDefinitions(definitions),
		Sources:     cloneStringMap(sources),
		FileStates:  cloneAPIFileStates(fileStates),
		Schedules:   cloneScheduleJobConfigs(schedules),
		WSClients:   cloneWSClientConfigs(wsClients),
	}
}

func cloneAPIDefinitions(definitions map[string]interface{}) map[string]interface{} {
	if definitions == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(definitions))
	for name, definition := range definitions {
		cloned[name] = cloneJSONCompatibleValue(definition)
	}
	return cloned
}

func cloneJSONCompatibleValue(value interface{}) interface{} {
	switch value := value.(type) {
	case map[string]interface{}:
		cloned := make(map[string]interface{}, len(value))
		for key, item := range value {
			cloned[key] = cloneJSONCompatibleValue(item)
		}
		return cloned
	case []interface{}:
		cloned := make([]interface{}, len(value))
		for index, item := range value {
			cloned[index] = cloneJSONCompatibleValue(item)
		}
		return cloned
	default:
		return value
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneAPIFileStates(states map[string]APIFileState) map[string]APIFileState {
	if states == nil {
		return nil
	}
	cloned := make(map[string]APIFileState, len(states))
	for path, state := range states {
		cloned[path] = state
	}
	return cloned
}

func cloneScheduleJobConfigs(configs map[string]scheduleJobConfig) map[string]scheduleJobConfig {
	if configs == nil {
		return nil
	}
	cloned := make(map[string]scheduleJobConfig, len(configs))
	for name, config := range configs {
		cloned[name] = cloneScheduleJobConfig(config)
	}
	return cloned
}

func cloneScheduleJobConfig(config scheduleJobConfig) scheduleJobConfig {
	cloned := config
	cloned.schedule.minutes = cloneCronField(config.schedule.minutes)
	cloned.schedule.hours = cloneCronField(config.schedule.hours)
	cloned.schedule.days = cloneCronField(config.schedule.days)
	cloned.schedule.months = cloneCronField(config.schedule.months)
	cloned.schedule.weekdays = cloneCronField(config.schedule.weekdays)
	return cloned
}

func cloneCronField(field cronField) cronField {
	if field == nil {
		return nil
	}
	cloned := make(cronField, len(field))
	for value, included := range field {
		cloned[value] = included
	}
	return cloned
}

func cloneWSClientConfigs(configs map[string]wsClientConfig) map[string]wsClientConfig {
	if configs == nil {
		return nil
	}
	cloned := make(map[string]wsClientConfig, len(configs))
	for name, config := range configs {
		cloned[name] = config
	}
	return cloned
}

func currentAPISnapshot() *APIConfigSnapshot {
	return apiSnapshot.Load()
}

func publishAPISnapshot(snapshot *APIConfigSnapshot) {
	apiSnapshot.Store(snapshot)
}

func currentAPIFiles() map[string]interface{} {
	snapshot := currentAPISnapshot()
	if snapshot == nil {
		return nil
	}
	return snapshot.Definitions
}

// setAPIFiles is retained for focused tests and callers that only need API definitions.
// Production config loads publish a complete snapshot through publishAPISnapshot.
func setAPIFiles(path string, files map[string]interface{}) {
	if files == nil && strings.TrimSpace(path) == "" {
		publishAPISnapshot(nil)
		return
	}
	publishAPISnapshot(newAPIConfigSnapshot(path, files, nil, nil, nil, nil))
}

func cachedAPIFilesFor(path string) (map[string]interface{}, bool) {
	snapshot := currentAPISnapshot()
	if snapshot == nil || snapshot.Definitions == nil || snapshot.RootPath == "" {
		return nil, false
	}
	normalizedPath, err := normalizeAPIFilePath(path)
	if err != nil || normalizedPath != snapshot.RootPath {
		return nil, false
	}
	return snapshot.Definitions, true
}

func sameAPIConfigSnapshot(current, candidate *APIConfigSnapshot) bool {
	if current == nil || candidate == nil {
		return current == candidate
	}
	return current.RootPath == candidate.RootPath &&
		reflect.DeepEqual(current.Definitions, candidate.Definitions) &&
		(current.Sources == nil || reflect.DeepEqual(current.Sources, candidate.Sources)) &&
		(current.Schedules == nil || reflect.DeepEqual(current.Schedules, candidate.Schedules)) &&
		(current.WSClients == nil || reflect.DeepEqual(current.WSClients, candidate.WSClients))
}

func publishLoadedAPIConfig(loaded *apiConfigLoadResult) bool {
	current := currentAPISnapshot()
	configChanged := !sameAPIConfigSnapshot(current, loaded.Snapshot)
	fileStatesChanged := current == nil || !reflect.DeepEqual(current.FileStates, loaded.Snapshot.FileStates)
	if !configChanged && !fileStatesChanged {
		return false
	}
	publishAPISnapshot(loaded.Snapshot)
	if configChanged && backgroundRuntimes != nil {
		backgroundRuntimes.reconcile(loaded.Snapshot.Schedules, loaded.Snapshot.WSClients)
	}
	return configChanged
}

func mergeAPIFileStates(stateSets ...map[string]APIFileState) map[string]APIFileState {
	merged := make(map[string]APIFileState)
	for _, states := range stateSets {
		for identity, state := range states {
			merged[identity] = state
		}
	}
	return merged
}

func apiFileStatesFingerprint(states map[string]APIFileState) [sha256.Size]byte {
	identities := make([]string, 0, len(states))
	for identity := range states {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	hasher := sha256.New()
	for _, identity := range identities {
		state := states[identity]
		_, _ = io.WriteString(hasher, identity)
		_, _ = hasher.Write([]byte{0})
		_, _ = io.WriteString(hasher, state.Path)
		_, _ = hasher.Write([]byte{0})
		if state.Exists {
			_, _ = hasher.Write([]byte{1})
		} else {
			_, _ = hasher.Write([]byte{0})
		}
		_, _ = hasher.Write(state.Hash[:])
		_, _ = io.WriteString(hasher, state.Error)
		_, _ = hasher.Write([]byte{0})
	}
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hasher.Sum(nil))
	return fingerprint
}

func reloadAPIFileIfChanged(apiFilePath, apiBaseDir string, lastObservedHash [sha256.Size]byte) ([sha256.Size]byte, bool, error) {
	data, err := os.ReadFile(apiFilePath)
	if err != nil {
		return lastObservedHash, false, fmt.Errorf("read api file: %w", err)
	}
	observedHash := sha256.Sum256(data)
	if observedHash == lastObservedHash {
		return lastObservedHash, false, nil
	}

	loaded, err := loadAPIConfigData(apiFilePath, apiBaseDir, data)
	if err != nil {
		return observedHash, false, err
	}
	if err := verifyAPIFileStates(loaded.Snapshot.FileStates); err != nil {
		return observedHash, false, err
	}
	return observedHash, publishLoadedAPIConfig(loaded), nil
}

func initialAPIFileStates(apiFilePath string, initialHash [sha256.Size]byte) map[string]APIFileState {
	normalizedPath, err := normalizeAPIFilePath(apiFilePath)
	if err == nil {
		if snapshot := currentAPISnapshot(); snapshot != nil && snapshot.RootPath == normalizedPath && len(snapshot.FileStates) > 0 {
			return cloneAPIFileStates(snapshot.FileStates)
		}
		return map[string]APIFileState{normalizedPath: {Path: normalizedPath, Exists: true, Hash: initialHash}}
	}
	return map[string]APIFileState{apiFilePath: {Path: apiFilePath, Exists: true, Hash: initialHash}}
}

func observeAPIFileStates(watched map[string]APIFileState) map[string]APIFileState {
	observed := make(map[string]APIFileState, len(watched))
	for identity, previous := range watched {
		path := previous.Path
		if path == "" {
			path = identity
		}
		_, state, _, _ := inspectAPIFile(path)
		observed[identity] = state
	}
	return observed
}

func verifyAPIFileStates(expected map[string]APIFileState) error {
	if observed := observeAPIFileStates(expected); !reflect.DeepEqual(observed, expected) {
		return fmt.Errorf("API files changed while the configuration was being loaded")
	}
	return nil
}

func reloadAPIConfigGraphIfChanged(apiFilePath, apiBaseDir string, lastObservedStates map[string]APIFileState) (map[string]APIFileState, bool, error) {
	observedStates := observeAPIFileStates(lastObservedStates)
	if reflect.DeepEqual(observedStates, lastObservedStates) {
		return lastObservedStates, false, nil
	}
	loaded, discoveredStates, err := readAPIConfigFileAttempt(apiFilePath, apiBaseDir)
	if err != nil {
		var activeStates map[string]APIFileState
		if snapshot := currentAPISnapshot(); snapshot != nil {
			activeStates = snapshot.FileStates
		}
		watched := mergeAPIFileStates(activeStates, discoveredStates)
		return observeAPIFileStates(watched), false, err
	}
	if err := verifyAPIFileStates(loaded.Snapshot.FileStates); err != nil {
		var activeStates map[string]APIFileState
		if snapshot := currentAPISnapshot(); snapshot != nil {
			activeStates = snapshot.FileStates
		}
		watched := mergeAPIFileStates(activeStates, discoveredStates)
		return observeAPIFileStates(watched), false, err
	}
	configChanged := publishLoadedAPIConfig(loaded)
	return cloneAPIFileStates(loaded.Snapshot.FileStates), configChanged, nil
}

func watchAPIFile(apiFilePath, apiBaseDir string, interval time.Duration, initialHash [sha256.Size]byte) {
	watchAPIFileUntil(apiFilePath, apiBaseDir, interval, initialHash, nil)
}

func watchAPIFileUntil(apiFilePath, apiBaseDir string, interval time.Duration, initialHash [sha256.Size]byte, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastObservedStates := initialAPIFileStates(apiFilePath, initialHash)
	lastReloadError := ""
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		observedStates, reloaded, err := reloadAPIConfigGraphIfChanged(apiFilePath, apiBaseDir, lastObservedStates)
		lastObservedStates = observedStates
		if err != nil {
			errorKey := fmt.Sprintf("%x:%s", apiFileStatesFingerprint(observedStates), err.Error())
			if errorKey != lastReloadError {
				logger.Printf("API hot reload failed: %v; current API configuration remains active", err)
			}
			lastReloadError = errorKey
			continue
		}
		lastReloadError = ""
		if reloaded {
			logger.Printf("API hot reload succeeded: api_count=%d", len(currentAPIFiles()))
		}
	}
}

type backgroundRuntimeManager struct {
	mu        sync.Mutex
	schedules map[string]*scheduleRuntime
	wsClients map[string]*wsClientRuntime
}

type scheduleRuntime struct {
	mu      sync.Mutex
	desired *scheduleJobConfig
	wake    chan struct{}
	done    chan struct{}
	stopped bool
}

type wsClientRuntime struct {
	mu         sync.Mutex
	desired    *wsClientConfig
	wake       chan struct{}
	done       chan struct{}
	stopped    bool
	conn       *websocket.Conn
	dialCancel context.CancelFunc
}

func newBackgroundRuntimeManager() *backgroundRuntimeManager {
	return &backgroundRuntimeManager{
		schedules: make(map[string]*scheduleRuntime),
		wsClients: make(map[string]*wsClientRuntime),
	}
}

func (manager *backgroundRuntimeManager) reconcile(schedules map[string]scheduleJobConfig, wsClients map[string]wsClientConfig) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	for name, runtime := range manager.schedules {
		if _, exists := schedules[name]; !exists {
			if _, changed := runtime.update(nil); changed {
				logger.Printf("Stopping schedule job %s", name)
			}
		}
	}
	for name, cfg := range schedules {
		if runtime, exists := manager.schedules[name]; exists {
			accepted, changed := runtime.update(&cfg)
			if accepted {
				if changed {
					logger.Printf("Updated schedule job %s with cron %q", name, cfg.trigger.Value)
				}
				continue
			}
		}
		runtime := newScheduleRuntime(cfg)
		manager.schedules[name] = runtime
		logger.Printf("Starting schedule job %s with cron %q", name, cfg.trigger.Value)
		go manager.runSchedule(name, runtime)
	}

	for name, runtime := range manager.wsClients {
		if _, exists := wsClients[name]; !exists {
			if _, changed, _ := runtime.update(nil); changed {
				logger.Printf("Stopping WebSocket client %s", name)
			}
		}
	}
	for name, cfg := range wsClients {
		if runtime, exists := manager.wsClients[name]; exists {
			accepted, changed, reconnect := runtime.update(&cfg)
			if accepted {
				if changed {
					logger.Printf("Updated WebSocket client %s reconnect=%t", name, reconnect)
				}
				continue
			}
		}
		runtime := newWSClientRuntime(cfg)
		manager.wsClients[name] = runtime
		logger.Printf("Starting WebSocket client %s -> %s", name, cfg.connectURL)
		go manager.runWSClient(name, runtime)
	}
}

func (manager *backgroundRuntimeManager) runSchedule(name string, runtime *scheduleRuntime) {
	runtime.run()
	manager.mu.Lock()
	if manager.schedules[name] == runtime {
		delete(manager.schedules, name)
	}
	manager.mu.Unlock()
}

func (manager *backgroundRuntimeManager) runWSClient(name string, runtime *wsClientRuntime) {
	runtime.run()
	manager.mu.Lock()
	if manager.wsClients[name] == runtime {
		delete(manager.wsClients, name)
	}
	manager.mu.Unlock()
}

func buildScheduleJobConfigs(files map[string]interface{}, execDir string) (map[string]scheduleJobConfig, error) {
	configs := make(map[string]scheduleJobConfig)
	for name, raw := range files {
		entry, ok := raw.(map[string]interface{})
		if !ok || getAPIType(entry) != apiTypeSchedule {
			continue
		}
		scriptPath := getAPIString(entry, "script")
		trigger := getTriggerConfig(entry)
		if scriptPath == "" {
			return nil, fmt.Errorf("schedule %s: script is missing", name)
		}
		if trigger.Type != "cron" {
			return nil, fmt.Errorf("schedule %s: unsupported trigger type %q", name, trigger.Type)
		}
		schedule, err := parseCronSchedule(trigger.Value)
		if err != nil {
			return nil, fmt.Errorf("schedule %s: invalid cron trigger %q: %w", name, trigger.Value, err)
		}
		scriptAbs, err := resolvePath(execDir, scriptPath)
		if err != nil {
			return nil, fmt.Errorf("schedule %s: invalid script path %s: %w", name, scriptPath, err)
		}
		configs[name] = scheduleJobConfig{name: name, scriptPath: scriptAbs, trigger: trigger, description: getAPIString(entry, "description"), schedule: schedule}
	}
	return configs, nil
}

func buildWSClientConfigs(files map[string]interface{}, execDir string) (map[string]wsClientConfig, error) {
	configs := make(map[string]wsClientConfig)
	for name, raw := range files {
		entry, ok := raw.(map[string]interface{})
		if !ok || getAPIType(entry) != apiTypeWSClient {
			continue
		}
		scriptPath := getAPIString(entry, "script")
		connectURLRaw := getAPIString(entry, "connectURL")
		if scriptPath == "" {
			return nil, fmt.Errorf("ws_client %s: script is missing", name)
		}
		if connectURLRaw == "" {
			return nil, fmt.Errorf("ws_client %s: connectURL is missing", name)
		}
		connectURL, err := resolveConnectURL(connectURLRaw)
		if err != nil {
			return nil, fmt.Errorf("ws_client %s: %w", name, err)
		}
		scriptAbs, err := resolvePath(execDir, scriptPath)
		if err != nil {
			return nil, fmt.Errorf("ws_client %s: invalid script path %s: %w", name, scriptPath, err)
		}
		configs[name] = wsClientConfig{name: name, scriptPath: scriptAbs, connectURL: connectURL, description: getAPIString(entry, "description")}
	}
	return configs, nil
}

func newScheduleRuntime(cfg scheduleJobConfig) *scheduleRuntime {
	copyOfConfig := cfg
	return &scheduleRuntime{desired: &copyOfConfig, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func sameScheduleTiming(a, b scheduleJobConfig) bool {
	return a.name == b.name && a.scriptPath == b.scriptPath && a.trigger == b.trigger
}

func sameScheduleJobConfig(a, b scheduleJobConfig) bool {
	return sameScheduleTiming(a, b) && a.description == b.description
}

func signalRuntime(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (runtime *scheduleRuntime) update(cfg *scheduleJobConfig) (bool, bool) {
	runtime.mu.Lock()
	if runtime.stopped {
		runtime.mu.Unlock()
		return false, false
	}
	if cfg == nil {
		if runtime.desired == nil {
			runtime.mu.Unlock()
			return true, false
		}
		runtime.desired = nil
		runtime.mu.Unlock()
		signalRuntime(runtime.wake)
		return true, true
	}
	if runtime.desired != nil && sameScheduleJobConfig(*runtime.desired, *cfg) {
		runtime.mu.Unlock()
		return true, false
	}
	wake := runtime.desired == nil || !sameScheduleTiming(*runtime.desired, *cfg)
	copyOfConfig := *cfg
	runtime.desired = &copyOfConfig
	runtime.mu.Unlock()
	if wake {
		signalRuntime(runtime.wake)
	}
	return true, true
}

func (runtime *scheduleRuntime) currentConfigOrStop() (scheduleJobConfig, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.desired == nil {
		runtime.stopped = true
		return scheduleJobConfig{}, false
	}
	return *runtime.desired, true
}

func (runtime *scheduleRuntime) currentConfig() (scheduleJobConfig, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.desired == nil {
		return scheduleJobConfig{}, false
	}
	return *runtime.desired, true
}

func (runtime *scheduleRuntime) run() {
	defer close(runtime.done)
	for {
		cfg, active := runtime.currentConfigOrStop()
		if !active {
			return
		}
		next := cfg.schedule.next(time.Now())
		if next.IsZero() {
			logger.Printf("Schedule job %s has no next run time", cfg.name)
			<-runtime.wake
			continue
		}
		logger.Printf("Schedule job %s next run at %s", cfg.name, next.Format(time.RFC3339))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-runtime.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-timer.C:
		}
		latest, active := runtime.currentConfig()
		if !active || !sameScheduleTiming(cfg, latest) {
			continue
		}
		allParams := map[string]interface{}{
			"api": latest.name, "nyan_job_name": latest.name,
			"nyan_schedule_trigger_type": latest.trigger.Type,
			"nyan_schedule_trigger":      latest.trigger.Value,
			"nyan_schedule_time":         next.Format(time.RFC3339),
			"nyan_schedule_description":  latest.description,
		}
		result, err := runJavaScript(latest.scriptPath, allParams, nil)
		if err != nil {
			logger.Printf("Schedule job %s failed: %v", latest.name, err)
			continue
		}
		logger.Printf("Schedule job %s completed: %s", latest.name, result)
	}
}

func newWSClientRuntime(cfg wsClientConfig) *wsClientRuntime {
	copyOfConfig := cfg
	return &wsClientRuntime{desired: &copyOfConfig, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (runtime *wsClientRuntime) update(cfg *wsClientConfig) (bool, bool, bool) {
	runtime.mu.Lock()
	if runtime.stopped {
		runtime.mu.Unlock()
		return false, false, false
	}
	if cfg == nil {
		if runtime.desired == nil {
			runtime.mu.Unlock()
			return true, false, false
		}
		runtime.desired = nil
		conn, cancel := runtime.conn, runtime.dialCancel
		runtime.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if conn != nil {
			_ = conn.Close()
		}
		signalRuntime(runtime.wake)
		return true, true, false
	}
	if runtime.desired != nil && *runtime.desired == *cfg {
		runtime.mu.Unlock()
		return true, false, false
	}
	reconnect := runtime.desired == nil || runtime.desired.connectURL != cfg.connectURL
	copyOfConfig := *cfg
	runtime.desired = &copyOfConfig
	conn, cancel := runtime.conn, runtime.dialCancel
	runtime.mu.Unlock()
	if reconnect {
		if cancel != nil {
			cancel()
		}
		if conn != nil {
			_ = conn.Close()
		}
		signalRuntime(runtime.wake)
	}
	return true, true, reconnect
}

func (runtime *wsClientRuntime) currentConfigOrStop() (wsClientConfig, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.desired == nil {
		runtime.stopped = true
		return wsClientConfig{}, false
	}
	return *runtime.desired, true
}

func (runtime *wsClientRuntime) currentConfig() (wsClientConfig, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.desired == nil {
		return wsClientConfig{}, false
	}
	return *runtime.desired, true
}

func (runtime *wsClientRuntime) beginDial(cfg wsClientConfig) (context.Context, context.CancelFunc, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.stopped || runtime.desired == nil || runtime.desired.connectURL != cfg.connectURL {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime.dialCancel = cancel
	return ctx, cancel, true
}

func (runtime *wsClientRuntime) finishDial(cancel context.CancelFunc) {
	runtime.mu.Lock()
	runtime.dialCancel = nil
	runtime.mu.Unlock()
	cancel()
}

func (runtime *wsClientRuntime) acceptConnection(conn *websocket.Conn, connectURL string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.stopped || runtime.desired == nil || runtime.desired.connectURL != connectURL {
		return false
	}
	runtime.conn = conn
	return true
}

func (runtime *wsClientRuntime) clearConnection(conn *websocket.Conn) {
	runtime.mu.Lock()
	if runtime.conn == conn {
		runtime.conn = nil
	}
	runtime.mu.Unlock()
}

func (runtime *wsClientRuntime) run() {
	defer close(runtime.done)
	backoff := time.Second
	for {
		cfg, active := runtime.currentConfigOrStop()
		if !active {
			return
		}
		err := runtime.connectAndListen(cfg)
		latest, active := runtime.currentConfigOrStop()
		if !active {
			return
		}
		if latest.connectURL != cfg.connectURL {
			select {
			case <-runtime.wake:
			default:
			}
			backoff = time.Second
			continue
		}
		if err != nil {
			logger.Printf("WebSocket client %s disconnected: %v", cfg.name, err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-runtime.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			backoff = time.Second
		case <-timer.C:
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		}
	}
}

func (runtime *wsClientRuntime) connectAndListen(cfg wsClientConfig) error {
	ctx, cancel, ok := runtime.beginDial(cfg)
	if !ok {
		return nil
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, cfg.connectURL, nil)
	runtime.finishDial(cancel)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	if !runtime.acceptConnection(conn, cfg.connectURL) {
		_ = conn.Close()
		return nil
	}
	defer runtime.clearConnection(conn)
	defer conn.Close()
	logger.Printf("WebSocket client %s connected", cfg.name)
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read error: %w", err)
		}
		if msgType == websocket.CloseMessage {
			return fmt.Errorf("close message received: %s", string(data))
		}
		latest, active := runtime.currentConfig()
		if !active || latest.connectURL != cfg.connectURL {
			return nil
		}
		allParams := map[string]interface{}{
			"api": latest.name, "ws_client": latest.name,
			"ws_message_type": websocketMessageTypeLabel(msgType),
			"ws_message_text": string(data), "ws_connect_url": latest.connectURL,
			"ws_description": latest.description,
		}
		if msgType == websocket.BinaryMessage {
			allParams["ws_message_base64"] = base64.StdEncoding.EncodeToString(data)
		}
		if msgType == websocket.TextMessage {
			var decoded interface{}
			if json.Unmarshal(data, &decoded) == nil {
				allParams["ws_message_json"] = decoded
			}
		}
		result, err := runJavaScript(latest.scriptPath, allParams, nil)
		if err != nil {
			logger.Printf("ws_client %s script error: %v", latest.name, err)
			continue
		}
		if trimmed := strings.TrimSpace(result); trimmed != "" {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(trimmed)); err != nil {
				return fmt.Errorf("send error: %w", err)
			}
		}
	}
}
