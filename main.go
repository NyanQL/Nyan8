package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
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
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/natefinch/lumberjack"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/crypto/argon2"
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
	Name              string              `json:"name"`
	Profile           string              `json:"profile"`
	Version           string              `json:"version"`
	Port              int                 `json:"Port"`
	BindAddress       string              `json:"bindAddress"`
	CertFile          string              `json:"certPath"`
	KeyFile           string              `json:"keyPath"`
	JavaScriptInclude []string            `json:"javascript_include"`
	Log               LogConfig           `json:"log"`
	SMTP              SMTPConfig          `json:"smtp"`
	OAuthAdmin        OAuthAdminConfig    `json:"oauth_admin"`
	OAuthStateRoot    string              `json:"oauth_state_directory"`
	APIHotReload      APIHotReloadConfig  `json:"APIHotReload"`
	WebSocket         WebSocketConfig     `json:"websocket"`
	ProxyProtocol     ProxyProtocolConfig `json:"proxyProtocol"`
}

// WebSocketConfig keeps the legacy API WebSocket feature available while
// allowing a production MCP deployment to disable the catch-all root upgrade
// and to cap long-lived connections shared with the MCP HTTP server.
type WebSocketConfig struct {
	AllowRoot      *bool `json:"allowRoot"`
	MaxConnections int   `json:"maxConnections"`
}

// ProxyProtocolConfig allows a TCP-mode reverse proxy to preserve the real
// client address without trusting spoofable HTTP forwarding headers. Only
// peers in TrustedCIDRs may connect when this mode is enabled, and every such
// connection must start with a valid PROXY protocol v2 header.
type ProxyProtocolConfig struct {
	Enabled      bool     `json:"enabled"`
	TrustedCIDRs []string `json:"trustedCIDRs"`
}

// OAuthAdminConfig contains the operator credential used only by the OAuth
// user bootstrap endpoint. Production values belong in the runtime config
// rendered by Ansible, never in api.json or a release artifact.
type OAuthAdminConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
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
	Name    string                 `json:"name"`
	Profile string                 `json:"profile"`
	Version string                 `json:"version"`
	Apis    map[string]NyanAPIData `json:"apis"`
}

type NyanAPIData struct {
	Description string `json:"description"`
	Push        string `json:"push,omitempty"`
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

const (
	schemaSourceParamCheck   = "paramCheck"
	schemaSourceOutCheck     = "outCheck"
	schemaSourceScriptLegacy = "scriptLegacy"
	schemaSourceUnknown      = "unknown"
)

type APISchema struct {
	Input        map[string]interface{}
	Output       map[string]interface{}
	InputSource  string
	OutputSource string
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

var (
	// BinaryVersion can be set at build time with:
	// go build -ldflags "-X main.BinaryVersion=vX.Y.Z"
	BinaryVersion = "v0.0.18"
)

const (
	apiTypeAPI      = "api"
	apiTypeInclude  = "include"
	apiTypeMCP      = "mcp"
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

type startupOptions struct {
	Paths     serviceFilePaths
	MCPServer string
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

var websocketConnectionCount = struct {
	sync.Mutex
	Active int
}{}

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

	options, err := resolveStartupOptions(execDir, os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	paths := options.Paths
	servicePaths = paths
	mcpStdio := options.MCPServer != ""
	if !mcpStdio {
		fmt.Println("Executable directory:", execDir)
		fmt.Printf("Config file path: %s (source: %s)\n", paths.Config.Path, paths.Config.Source)
		fmt.Printf("API file path: %s (source: %s)\n", paths.API.Path, paths.API.Source)
	}

	config, err := loadConfig(paths.Config.Path)
	if err != nil {
		// logger はまだ初期化前なので標準ログで終了
		log.Fatalf("Error loading config from %s: %v", paths.Config.Path, err)
	}
	configBaseDir := filepath.Dir(paths.Config.Path)
	apiBaseDir := filepath.Dir(paths.API.Path)
	adjustConfigPaths(configBaseDir, &config)
	globalConfig = config

	// ロガーをセットアップ
	loggerFallback := io.Writer(os.Stdout)
	if mcpStdio {
		loggerFallback = os.Stderr
	}
	initLoggerWithFallback(configBaseDir, loggerFallback)
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
	if mcpStdio {
		mcp, selectErr := selectMCPStdioServer(initialAPIConfig.Snapshot, options.MCPServer)
		if selectErr != nil {
			logger.Fatalf("Failed to start stdio MCP: %v", selectErr)
		}
		logger.Printf("Starting stdio MCP server: %s", mcp.Name)
		if serveErr := serveMCPStdio(os.Stdin, os.Stdout, initialAPIConfig.Snapshot, mcp); serveErr != nil {
			logger.Fatalf("stdio MCP failed: %v", serveErr)
		}
		return
	}

	listenAddress, err := resolveListenAddress(config.BindAddress, config.Port)
	if err != nil {
		logger.Fatalf("Invalid listen address: %v", err)
	}
	apiHotReloadInterval, err := parseAPIHotReloadInterval(config.APIHotReload.Interval)
	if err != nil {
		logger.Fatalf("Invalid APIHotReload.Interval %q: %v", config.APIHotReload.Interval, err)
	}
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
		if dispatchMCPOrOAuth(c) || dispatchDynamicEndpoint(c, apiBaseDir) {
			return
		}
		respondWithError(c, http.StatusNotFound, "Endpoint not found", nil)
	})

	r.POST("/nyan-rpc", handleJSONRPC)
	r.Any("/nyan", handleNyan)
	r.Any("/nyan/*apiName", handleNyanDetail)
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

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           h2c.NewHandler(r, &http2.Server{}),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	if config.ProxyProtocol.Enabled {
		logger.Printf("Trusted PROXY protocol v2 enabled and required")
	}
	if config.CertFile != "" && config.KeyFile != "" {
		logger.Printf("Starting HTTPS server at %s", listenAddress)
		err = serveConfiguredHTTPServer(server, certFilePath, keyFilePath, config.ProxyProtocol)
	} else {
		logger.Printf("Starting HTTP server at %s", listenAddress)
		err = serveConfiguredHTTPServer(server, "", "", config.ProxyProtocol)
	}
	if err != nil && err != http.ErrServerClosed {
		logger.Fatalf("Failed to start server: %v", err)
	}
}

const (
	proxyProtocolHeaderTimeout = 5 * time.Second
	maxProxyProtocolV2Payload  = 4 << 10
)

var proxyProtocolV2Signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

// serveConfiguredHTTPServer keeps PROXY protocol parsing in front of TLS: the
// HAProxy v2 header is cleartext and precedes the TLS ClientHello in TCP mode.
func serveConfiguredHTTPServer(server *http.Server, certFile, keyFile string, proxyConfig ProxyProtocolConfig) error {
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	if proxyConfig.Enabled {
		listener, err = newProxyProtocolListener(listener, proxyConfig)
		if err != nil {
			_ = listener.Close()
			return err
		}
	}
	if certFile != "" && keyFile != "" {
		return server.ServeTLS(listener, certFile, keyFile)
	}
	return server.Serve(listener)
}

type proxyProtocolListener struct {
	net.Listener
	trustedSources []*net.IPNet
	headerTimeout  time.Duration
}

func newProxyProtocolListener(listener net.Listener, config ProxyProtocolConfig) (net.Listener, error) {
	trustedSources, err := validateProxyProtocolConfig(config)
	if err != nil {
		return listener, err
	}
	return &proxyProtocolListener{
		Listener:       listener,
		trustedSources: trustedSources,
		headerTimeout:  proxyProtocolHeaderTimeout,
	}, nil
}

func validateProxyProtocolConfig(config ProxyProtocolConfig) ([]*net.IPNet, error) {
	if !config.Enabled {
		return nil, nil
	}
	if len(config.TrustedCIDRs) == 0 || len(config.TrustedCIDRs) > 32 {
		return nil, fmt.Errorf("proxyProtocol.trustedCIDRs must contain between 1 and 32 CIDRs")
	}
	trustedSources := make([]*net.IPNet, 0, len(config.TrustedCIDRs))
	seen := make(map[string]struct{}, len(config.TrustedCIDRs))
	for _, rawCIDR := range config.TrustedCIDRs {
		cidr := strings.TrimSpace(rawCIDR)
		_, network, err := net.ParseCIDR(cidr)
		if err != nil || cidr == "" {
			return nil, fmt.Errorf("proxyProtocol.trustedCIDRs contains an invalid CIDR")
		}
		ones, bits := network.Mask.Size()
		if ones != bits {
			return nil, fmt.Errorf("proxyProtocol.trustedCIDRs must contain host CIDRs only")
		}
		canonical := network.String()
		if _, duplicate := seen[canonical]; duplicate {
			return nil, fmt.Errorf("proxyProtocol.trustedCIDRs contains a duplicate CIDR")
		}
		seen[canonical] = struct{}{}
		trustedSources = append(trustedSources, network)
	}
	return trustedSources, nil
}

func (listener *proxyProtocolListener) Accept() (net.Conn, error) {
	for {
		conn, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		peerIP, peerErr := networkAddressIP(conn.RemoteAddr())
		trusted := peerErr == nil
		if trusted {
			trusted = false
			for _, network := range listener.trustedSources {
				if network.Contains(peerIP) {
					trusted = true
					break
				}
			}
		}
		if trusted {
			return &proxyProtocolConn{Conn: conn, headerTimeout: listener.headerTimeout}, nil
		}
		peer := conn.RemoteAddr()
		_ = conn.Close()
		if logger != nil {
			logger.Printf("Rejected connection from untrusted PROXY protocol peer %s", peer)
		}
	}
}

type proxyProtocolConn struct {
	net.Conn
	parseOnce     sync.Once
	readMu        sync.Mutex
	reader        *bufio.Reader
	remoteAddress net.Addr
	parseErr      error
	headerTimeout time.Duration
}

func (conn *proxyProtocolConn) Read(buffer []byte) (int, error) {
	if err := conn.ensureHeader(); err != nil {
		return 0, err
	}
	conn.readMu.Lock()
	defer conn.readMu.Unlock()
	return conn.reader.Read(buffer)
}

func (conn *proxyProtocolConn) RemoteAddr() net.Addr {
	_ = conn.ensureHeader()
	if conn.remoteAddress != nil {
		return conn.remoteAddress
	}
	return conn.Conn.RemoteAddr()
}

func (conn *proxyProtocolConn) ensureHeader() error {
	conn.parseOnce.Do(func() {
		if err := conn.Conn.SetReadDeadline(time.Now().Add(conn.headerTimeout)); err != nil {
			conn.parseErr = fmt.Errorf("cannot set PROXY protocol deadline")
			return
		}
		defer func() { _ = conn.Conn.SetReadDeadline(time.Time{}) }()
		conn.reader = bufio.NewReaderSize(conn.Conn, 16+maxProxyProtocolV2Payload)
		conn.remoteAddress, conn.parseErr = readProxyProtocolV2Header(conn.reader)
		if conn.parseErr != nil && logger != nil {
			logger.Printf("Rejected invalid PROXY protocol v2 header from %s: %v", conn.Conn.RemoteAddr(), conn.parseErr)
		}
	})
	return conn.parseErr
}

func readProxyProtocolV2Header(reader *bufio.Reader) (net.Addr, error) {
	fixed := make([]byte, 16)
	if _, err := io.ReadFull(reader, fixed); err != nil {
		return nil, fmt.Errorf("incomplete PROXY protocol v2 header")
	}
	if !bytes.Equal(fixed[:12], proxyProtocolV2Signature) {
		return nil, fmt.Errorf("invalid PROXY protocol v2 signature")
	}
	if fixed[12]>>4 != 2 {
		return nil, fmt.Errorf("unsupported PROXY protocol version")
	}
	payloadLength := int(binary.BigEndian.Uint16(fixed[14:16]))
	if payloadLength > maxProxyProtocolV2Payload {
		return nil, fmt.Errorf("invalid PROXY protocol v2 payload length")
	}
	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("incomplete PROXY protocol v2 payload")
	}
	command := fixed[12] & 0x0f
	if command == 0 { // LOCAL: retain the transport peer for HAProxy health checks.
		return nil, nil
	}
	if command != 1 {
		return nil, fmt.Errorf("unsupported PROXY protocol command")
	}

	var sourceIP net.IP
	var sourcePort int
	switch fixed[13] {
	case 0x11: // TCP over IPv4
		if payloadLength < 12 {
			return nil, fmt.Errorf("short IPv4 PROXY protocol v2 payload")
		}
		sourceIP = net.IP(append([]byte(nil), payload[0:4]...))
		sourcePort = int(binary.BigEndian.Uint16(payload[8:10]))
	case 0x21: // TCP over IPv6
		if payloadLength < 36 {
			return nil, fmt.Errorf("short IPv6 PROXY protocol v2 payload")
		}
		sourceIP = net.IP(append([]byte(nil), payload[0:16]...))
		sourcePort = int(binary.BigEndian.Uint16(payload[32:34]))
	default:
		return nil, fmt.Errorf("unsupported PROXY protocol v2 address family or transport")
	}
	if sourceIP == nil || sourceIP.IsUnspecified() || sourcePort < 1 {
		return nil, fmt.Errorf("invalid PROXY protocol v2 source address")
	}
	return &net.TCPAddr{IP: sourceIP, Port: sourcePort}, nil
}

func networkAddressIP(address net.Addr) (net.IP, error) {
	if address == nil {
		return nil, fmt.Errorf("network address is missing")
	}
	if tcpAddress, ok := address.(*net.TCPAddr); ok && tcpAddress.IP != nil {
		return tcpAddress.IP, nil
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return nil, fmt.Errorf("invalid network address")
	}
	return ip, nil
}

// isTemporaryDirectory はディレクトリが一時ディレクトリかどうかを判定します
// ★★★ 修正：filepath.HasPrefix は存在しないため、安全な判定に置き換え ★★★
func isTemporaryDirectory(path string) bool {
	sep := string(os.PathSeparator)
	p := filepath.Clean(path) + sep
	t := filepath.Clean(os.TempDir()) + sep
	return strings.HasPrefix(p, t)
}

func resolveStartupOptions(execDir string, args []string) (startupOptions, error) {
	flags := flag.NewFlagSet("Nyan8", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	apiFlag := flags.String("api", "", "path to api.json")
	configFlag := flags.String("config", "", "path to config.json")
	mcpServerFlag := flags.String("mcp-server", "", "MCP API name selected for stdio mode")
	if err := flags.Parse(args); err != nil {
		return startupOptions{}, err
	}
	if flags.NArg() != 0 {
		return startupOptions{}, fmt.Errorf("unexpected command arguments: %s", strings.Join(flags.Args(), " "))
	}
	apiPath, apiSource := chooseServiceFilePath(*apiFlag, "NYAN_API_PATH", filepath.Join(execDir, "api.json"), "--api")
	configPath, configSource := chooseServiceFilePath(*configFlag, "NYAN_CONFIG_PATH", filepath.Join(execDir, "config.json"), "--config")

	resolvedAPIPath, err := resolveExistingServiceFilePath(apiPath, "api", apiSource)
	if err != nil {
		return startupOptions{}, err
	}
	resolvedConfigPath, err := resolveExistingServiceFilePath(configPath, "config", configSource)
	if err != nil {
		return startupOptions{}, err
	}

	return startupOptions{
		Paths: serviceFilePaths{
			API:    serviceFilePath{Path: resolvedAPIPath, Source: apiSource},
			Config: serviceFilePath{Path: resolvedConfigPath, Source: configSource},
		},
		MCPServer: strings.TrimSpace(*mcpServerFlag),
	}, nil
}

func resolveServiceFilePaths(execDir string, args []string) (serviceFilePaths, error) {
	options, err := resolveStartupOptions(execDir, args)
	return options.Paths, err
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
	if config.WebSocket.MaxConnections < 1 || config.WebSocket.MaxConnections > 4096 {
		return config, fmt.Errorf("websocket.maxConnections must be between 1 and 4096")
	}
	if _, err := validateProxyProtocolConfig(config.ProxyProtocol); err != nil {
		return config, err
	}
	return config, nil
}

func adjustConfigPaths(configBaseDir string, config *Config) {
	config.CertFile = resolvePathFromBase(configBaseDir, config.CertFile)
	config.KeyFile = resolvePathFromBase(configBaseDir, config.KeyFile)
	config.Log.Filename = resolvePathFromBase(configBaseDir, config.Log.Filename)
	config.OAuthStateRoot = resolvePathFromBase(configBaseDir, config.OAuthStateRoot)
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

func resolveListenAddress(bindAddress string, port int) (string, error) {
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535")
	}
	bindAddress = strings.TrimSpace(bindAddress)
	if bindAddress != "" && net.ParseIP(bindAddress) == nil {
		return "", fmt.Errorf("bindAddress must be an IP address")
	}
	return net.JoinHostPort(bindAddress, strconv.Itoa(port)), nil
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
	if dispatchMCPOrOAuth(c) {
		return
	}
	if websocket.IsWebSocketUpgrade(c.Request) {
		if !rootWebSocketAllowed() {
			c.Status(http.StatusForbidden)
			return
		}
		handleWebSocket(c)
	} else {
		handleAPIRequest(c)
	}
}

func rootWebSocketAllowed() bool {
	return globalConfig.WebSocket.AllowRoot == nil || *globalConfig.WebSocket.AllowRoot
}

func webSocketMaxConnections() int {
	if globalConfig.WebSocket.MaxConnections > 0 {
		return globalConfig.WebSocket.MaxConnections
	}
	return 128
}

func apiWebSocketAllowed(apiConfig map[string]interface{}) bool {
	value, exists := apiConfig["websocket"]
	if !exists {
		return true
	}
	enabled, ok := value.(bool)
	return ok && enabled
}

func acquireWebSocketConnection(limit int) (func(), bool) {
	if limit < 1 {
		return func() {}, false
	}
	websocketConnectionCount.Lock()
	defer websocketConnectionCount.Unlock()
	if websocketConnectionCount.Active >= limit {
		return func() {}, false
	}
	websocketConnectionCount.Active++
	return func() {
		websocketConnectionCount.Lock()
		if websocketConnectionCount.Active > 0 {
			websocketConnectionCount.Active--
		}
		websocketConnectionCount.Unlock()
	}, true
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
	release, acquired := acquireWebSocketConnection(webSocketMaxConnections())
	if !acquired {
		c.Header("Retry-After", "1")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "WebSocket connection limit reached"})
		return
	}
	defer release()
	// WebSocket 接続をアップグレード
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Printf("Failed to upgrade WebSocket: %v", err)
		return
	}
	// http.Server.ReadTimeout protects ordinary request bodies from slow
	// clients.  A successful WebSocket upgrade is intentionally long-lived,
	// so clear the inherited socket deadline before entering its read loop.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		logger.Printf("Failed to clear WebSocket read deadline: %v", err)
		conn.Close()
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
		if !apiWebSocketAllowed(scriptInfo) {
			logger.Printf("WebSocket is disabled for key: %s", scriptValue)
			sendErrorMessage(conn, "WebSocket is not enabled for this API")
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
		if isMCPOrOAuthRequest(c.Request) {
			c.Next()
			return
		}
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
	for key, value := range headers {
		req.Header.Set(key, value)
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
	initLoggerWithFallback(execDir, os.Stdout)
}

func initLoggerWithFallback(execDir string, fallback io.Writer) {
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
		logger = log.New(fallback, "", log.LstdFlags)
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
		// A route registered from the startup snapshot can later be reassigned
		// to MCP/OAuth by a valid hot reload. Give the current immutable MCP
		// snapshot priority instead of letting the stale Gin route shadow it.
		if dispatchMCPOrOAuth(c) {
			return
		}
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
func registerDynamicEndpoints(r *gin.Engine, execDir string) error {
	apiConf, err := loadJSONFile(apiJSONPath(execDir))
	if err != nil {
		return fmt.Errorf("failed to load api.json: %v", err)
	}

	for apiName, apiRaw := range apiConf {
		apiMap, ok := apiRaw.(map[string]interface{})
		if !ok {
			continue
		}

		// nyan系の名前は組み込みendpoint用に予約する。
		if isReservedNyanAPIName(apiName) {
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
		r.Any("/"+currentAPIName, func(c *gin.Context) {
			if dispatchMCPOrOAuth(c) {
				return
			}
			executeAPIEndpoint(c, currentAPIName, execDir)
		})
		if !strings.HasPrefix(currentAPIName, "api/") {
			r.Any("/api/"+currentAPIName, func(c *gin.Context) {
				if dispatchMCPOrOAuth(c) {
					return
				}
				executeAPIEndpoint(c, currentAPIName, execDir)
			})
		}
	}
	return nil
}

func executeAPIEndpoint(c *gin.Context, apiName, execDir string) {
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
	if websocket.IsWebSocketUpgrade(c.Request) {
		if !apiWebSocketAllowed(scriptInfo) {
			c.Status(http.StatusForbidden)
			return
		}
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

func dispatchMCPOrOAuth(c *gin.Context) bool {
	match, ok := matchMCPOrOAuthRequest(currentAPISnapshot(), c.Request)
	if !ok {
		return false
	}
	if match.Role == "mcp" {
		handleMCPHTTP(c, match.Snapshot, match.MCP)
		return true
	}
	handleOAuthHTTP(c, match.Snapshot, match.MCP, match.APIName, match.Role)
	return true
}

type mcpRequestMatch struct {
	Snapshot *APIConfigSnapshot
	MCP      *MCPServerConfig
	APIName  string
	Role     string
}

func requestedAPIName(request *http.Request) string {
	if request == nil || request.URL == nil {
		return ""
	}
	if request.URL.Path == "/" {
		return strings.TrimSpace(request.URL.Query().Get("api"))
	}
	return strings.Trim(strings.TrimSpace(request.URL.Path), "/")
}

func matchMCPOrOAuthRequest(snapshot *APIConfigSnapshot, request *http.Request) (mcpRequestMatch, bool) {
	if snapshot == nil {
		return mcpRequestMatch{}, false
	}
	apiName := requestedAPIName(request)
	if apiName == "" {
		return mcpRequestMatch{}, false
	}
	for _, name := range sortedMCPServerNames(snapshot.MCPServers) {
		mcp := snapshot.MCPServers[name]
		if !mcpSupportsTransport(mcp, "streamable_http") {
			continue
		}
		if apiName == name {
			return mcpRequestMatch{Snapshot: snapshot, MCP: mcp, APIName: apiName, Role: "mcp"}, true
		}
		if role, ok := mcpOAuthRoleForAPI(mcp, apiName); ok {
			return mcpRequestMatch{Snapshot: snapshot, MCP: mcp, APIName: apiName, Role: role}, true
		}
	}
	return mcpRequestMatch{}, false
}

func isMCPOrOAuthRequest(request *http.Request) bool {
	_, ok := matchMCPOrOAuthRequest(currentAPISnapshot(), request)
	return ok
}

func mcpOAuthRoleForAPI(mcp *MCPServerConfig, apiName string) (string, bool) {
	if mcp == nil || !mcpOAuthConfigured(mcp.OAuth) {
		return "", false
	}
	roles := []struct {
		API  string
		Role string
	}{
		{mcp.OAuth.AuthorizationServerMetadata, "authorizationServerMetadata"},
		{mcp.OAuth.ProtectedResourceMetadata, "protectedResourceMetadata"},
		{mcp.OAuth.Authorize, "oauthAuthorize"},
		{mcp.OAuth.Token, "oauthToken"},
		{mcp.OAuth.Register, "oauthRegister"},
		{mcp.OAuth.AdminUser, "oauthAdminUser"},
		{mcp.OAuth.VerifyAccess, "oauthValidateAccessToken"},
	}
	for _, candidate := range roles {
		if candidate.API != "" && apiName == candidate.API {
			return candidate.Role, true
		}
	}
	return "", false
}

type mcpRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpRuntimeURLs struct {
	Origin                      string
	Resource                    string
	Issuer                      string
	AuthorizationServerMetadata string
	ProtectedResourceMetadata   string
	AuthorizationEndpoint       string
	TokenEndpoint               string
	RegistrationEndpoint        string
	AdminUserEndpoint           string
}

func deriveMCPRuntimeURLs(request *http.Request, mcp *MCPServerConfig) (mcpRuntimeURLs, error) {
	if request == nil || request.URL == nil || mcp == nil {
		return mcpRuntimeURLs{}, fmt.Errorf("request URL is unavailable")
	}
	scheme := strings.ToLower(strings.TrimSpace(request.URL.Scheme))
	if scheme == "" {
		if request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	if scheme != "http" && scheme != "https" {
		return mcpRuntimeURLs{}, fmt.Errorf("request scheme is invalid")
	}
	authority := strings.TrimSpace(request.Host)
	if authority == "" || strings.ContainsAny(authority, "\\/?#@\r\n\t ") {
		return mcpRuntimeURLs{}, fmt.Errorf("request authority is invalid")
	}
	parsed, err := url.Parse("//" + authority)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return mcpRuntimeURLs{}, fmt.Errorf("request authority is invalid")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if !validMCPRequestHostname(hostname) {
		return mcpRuntimeURLs{}, fmt.Errorf("request authority is invalid")
	}
	port := parsed.Port()
	if port != "" {
		portNumber, portErr := strconv.Atoi(port)
		if portErr != nil || portNumber < 1 || portNumber > 65535 {
			return mcpRuntimeURLs{}, fmt.Errorf("request authority is invalid")
		}
	}
	canonicalAuthority := hostname
	if strings.Contains(hostname, ":") {
		canonicalAuthority = "[" + hostname + "]"
	}
	if port != "" {
		canonicalAuthority = net.JoinHostPort(hostname, port)
	}
	origin := (&url.URL{Scheme: scheme, Host: canonicalAuthority}).String()
	apiURL := func(apiName string) string {
		if apiName == "" {
			return ""
		}
		path, pathErr := canonicalAPIEndpointPath(apiName)
		if pathErr != nil {
			return ""
		}
		return origin + path
	}
	return mcpRuntimeURLs{
		Origin:                      origin,
		Resource:                    origin + mcp.Path,
		Issuer:                      origin,
		AuthorizationServerMetadata: apiURL(mcp.OAuth.AuthorizationServerMetadata),
		ProtectedResourceMetadata:   apiURL(mcp.OAuth.ProtectedResourceMetadata),
		AuthorizationEndpoint:       apiURL(mcp.OAuth.Authorize),
		TokenEndpoint:               apiURL(mcp.OAuth.Token),
		RegistrationEndpoint:        apiURL(mcp.OAuth.Register),
		AdminUserEndpoint:           apiURL(mcp.OAuth.AdminUser),
	}, nil
}

func validMCPRequestHostname(hostname string) bool {
	if hostname == "" || len(hostname) > 253 {
		return false
	}
	if net.ParseIP(hostname) != nil {
		return true
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || !mcpDNSLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func handleMCPHTTP(c *gin.Context, snapshot *APIConfigSnapshot, mcp *MCPServerConfig) {
	clearWriteDeadline := setProtectedResponseWriteDeadline(c)
	defer clearWriteDeadline()
	c.Header("Cache-Control", "no-store")
	runtimeURLs, err := deriveMCPRuntimeURLs(c.Request, mcp)
	if err != nil {
		c.JSON(http.StatusMisdirectedRequest, gin.H{"error": "Host is not allowed"})
		return
	}
	if !mcpOriginAllowed(c.GetHeader("Origin"), mcp, runtimeURLs.Origin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Origin is not allowed"})
		return
	}
	writeMCPCORSHeaders(c, mcp, runtimeURLs.Origin)
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusNoContent)
		return
	}
	if c.Request.Method != http.MethodPost {
		c.Header("Allow", "POST, OPTIONS")
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	if allowed, retryAfter := mcpRateLimitAllows(mcp.Name, mcp.RateLimit, c.Request.RemoteAddr, time.Now()); !allowed {
		c.Header("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retryAfter.Seconds())))))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
		return
	}
	contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || contentType != "application/json" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "Content-Type must be application/json"})
		return
	}
	if !mcpAcceptsJSONAndEventStream(c.GetHeader("Accept")) {
		c.JSON(http.StatusNotAcceptable, gin.H{"error": "Accept must include application/json and text/event-stream"})
		return
	}
	body, err := readLimitedRequestBody(c.Request, maxMCPRequestBytes)
	if err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is too large"})
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		mcpWriteError(c, nil, -32600, "Invalid Request")
		return
	}
	var request mcpRPCRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		mcpWriteError(c, nil, -32700, "Parse error")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		mcpWriteError(c, nil, -32700, "Parse error")
		return
	}
	if err := validateNoDuplicateJSONKeys(body); err != nil {
		mcpWriteError(c, nil, -32600, "Invalid Request")
		return
	}
	release, acquired := acquireMCPExecutionSlot(mcp.Name, mcpMaxConcurrent(mcp))
	if !acquired {
		c.Header("Retry-After", "1")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP server is busy"})
		return
	}
	defer release()
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" || !validMCPRequestID(request.ID) {
		mcpWriteError(c, nil, -32600, "Invalid Request")
		return
	}
	if len(request.ID) == 0 {
		if request.Method != "initialize" && !mcpProtocolVersionAllowed(c.GetHeader("MCP-Protocol-Version"), mcp.ProtocolVersions) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "MCP-Protocol-Version is missing or unsupported"})
			return
		}
		c.Status(http.StatusAccepted)
		return
	}

	if request.Method == "initialize" {
		var params struct {
			ProtocolVersion string                 `json:"protocolVersion"`
			Capabilities    map[string]interface{} `json:"capabilities"`
			ClientInfo      map[string]interface{} `json:"clientInfo"`
			Meta            map[string]interface{} `json:"_meta"`
		}
		if !decodeMCPParams(request.Params, &params) || !mcpProtocolVersionAllowed(params.ProtocolVersion, mcp.ProtocolVersions) {
			mcpWriteError(c, request.ID, -32602, "unsupported or missing protocolVersion")
			return
		}
		c.Header("MCP-Protocol-Version", params.ProtocolVersion)
		mcpWriteResult(c, request.ID, mcpInitializeResult(mcp, params.ProtocolVersion))
		return
	}

	protocolVersion := c.GetHeader("MCP-Protocol-Version")
	if !mcpProtocolVersionAllowed(protocolVersion, mcp.ProtocolVersions) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MCP-Protocol-Version is missing or unsupported"})
		return
	}
	c.Header("MCP-Protocol-Version", protocolVersion)
	switch request.Method {
	case "ping":
		mcpWriteResult(c, request.ID, map[string]interface{}{})
	case "tools/list":
		if !mcpParamsAreObjectOrEmpty(request.Params) {
			mcpWriteError(c, request.ID, -32602, "Invalid params")
			return
		}
		mcpWriteResult(c, request.ID, map[string]interface{}{"tools": mcpToolList(mcp)})
	case "tools/call":
		handleMCPToolCall(c, snapshot, mcp, runtimeURLs, request)
	default:
		mcpWriteError(c, request.ID, -32601, "Method not found")
	}
}

func setProtectedResponseWriteDeadline(c *gin.Context) func() {
	if c == nil || c.Writer == nil {
		return func() {}
	}
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return func() {}
	}
	return func() {
		_ = controller.SetWriteDeadline(time.Time{})
	}
}

func mcpAcceptsJSONAndEventStream(value string) bool {
	hasJSON := false
	hasEventStream := false
	for _, part := range strings.Split(value, ",") {
		mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		hasJSON = hasJSON || mediaType == "application/json"
		hasEventStream = hasEventStream || mediaType == "text/event-stream"
	}
	return hasJSON && hasEventStream
}

func readLimitedRequestBody(request *http.Request, limit int64) ([]byte, error) {
	if request == nil || request.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("body exceeds limit")
	}
	return data, nil
}

func validMCPRequestID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	trimmed := bytes.TrimSpace(id)
	if bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == '"' {
		var value string
		return json.Unmarshal(trimmed, &value) == nil
	}
	var number json.Number
	return json.Unmarshal(trimmed, &number) == nil
}

func decodeMCPParams(raw json.RawMessage, target interface{}) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || trimmed[0] != '{' {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil
}

func mcpParamsAreObjectOrEmpty(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	if trimmed[0] != '{' {
		return false
	}
	var params map[string]interface{}
	return json.Unmarshal(trimmed, &params) == nil
}

func mcpProtocolVersionAllowed(version string, allowed []string) bool {
	for _, candidate := range allowed {
		if version == candidate {
			return true
		}
	}
	return false
}

func mcpToolList(mcp *MCPServerConfig) []map[string]interface{} {
	return mcpToolListForTransport(mcp, true)
}

func mcpToolListForTransport(mcp *MCPServerConfig, includeHTTPSecurity bool) []map[string]interface{} {
	tools := make([]map[string]interface{}, 0, len(mcp.Tools))
	for _, tool := range mcp.Tools {
		entry := map[string]interface{}{
			"name":        tool.Name,
			"title":       tool.Title,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		}
		if tool.OutputSchema != nil {
			entry["outputSchema"] = tool.OutputSchema
		}
		if includeHTTPSecurity && tool.SecuritySchemes != nil {
			entry["securitySchemes"] = tool.SecuritySchemes
			entry["_meta"] = map[string]interface{}{"securitySchemes": tool.SecuritySchemes}
		}
		if tool.Annotations != nil {
			entry["annotations"] = tool.Annotations
		}
		tools = append(tools, entry)
	}
	return tools
}

func mcpInitializeResult(mcp *MCPServerConfig, protocolVersion string) map[string]interface{} {
	return map[string]interface{}{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]interface{}{"tools": map[string]interface{}{"listChanged": false}},
		"serverInfo":      map[string]interface{}{"name": globalConfig.Name, "version": getVersion()},
		"instructions":    mcp.Instructions,
	}
}

func handleMCPToolCall(c *gin.Context, snapshot *APIConfigSnapshot, mcp *MCPServerConfig, runtimeURLs mcpRuntimeURLs, request mcpRPCRequest) {
	params, ok := decodeMCPToolCallParams(request.Params)
	if !ok {
		mcpWriteError(c, request.ID, -32602, "Invalid params")
		return
	}
	tool := findMCPTool(mcp, params.Name)
	if tool == nil {
		mcpWriteError(c, request.ID, -32602, "Unknown tool")
		return
	}
	requiredScopes := mcpToolScopes(*tool)
	if len(requiredScopes) == 0 {
		requiredScopes = append([]string(nil), mcp.OAuth.Scopes...)
	}
	principal, authenticated, forbidden := validateMCPAccessToken(snapshot, mcp, runtimeURLs, c.GetHeader("Authorization"), tool.Name, requiredScopes)
	if !authenticated {
		status := http.StatusUnauthorized
		message := "Authentication required."
		oauthError := "invalid_token"
		if forbidden {
			status = http.StatusForbidden
			message = "The access token does not grant the required scope."
			oauthError = "insufficient_scope"
		}
		challenge := mcpOAuthChallenge(runtimeURLs, requiredScopes, oauthError, message)
		c.Header("WWW-Authenticate", challenge)
		mcpWriteHTTPResult(c, request.ID, status, map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": message}},
			"isError": true,
			"_meta":   map[string]interface{}{"mcp/www_authenticate": []string{challenge}},
		})
		return
	}
	payload, toolError := executeMCPTool(snapshot, tool, params.Arguments, principal)
	if toolError != "" {
		mcpWriteToolError(c, request.ID, toolError)
		return
	}
	mcpWriteResult(c, request.ID, payload)
}

type mcpToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
	Meta      map[string]interface{} `json:"_meta"`
}

func decodeMCPToolCallParams(raw json.RawMessage) (mcpToolCallParams, bool) {
	var params mcpToolCallParams
	if !decodeMCPParams(raw, &params) || strings.TrimSpace(params.Name) == "" {
		return mcpToolCallParams{}, false
	}
	return params, true
}

func findMCPTool(mcp *MCPServerConfig, name string) *MCPToolConfig {
	if mcp == nil {
		return nil
	}
	for index := range mcp.Tools {
		if mcp.Tools[index].Name == name {
			return &mcp.Tools[index]
		}
	}
	return nil
}

func executeMCPTool(snapshot *APIConfigSnapshot, tool *MCPToolConfig, rawArguments map[string]interface{}, principal interface{}) (map[string]interface{}, string) {
	argumentsValue := rawArguments
	if argumentsValue == nil {
		argumentsValue = map[string]interface{}{}
	}
	if err := validateMCPJSONSchemaValue(tool.InputSchema, argumentsValue); err != nil {
		return nil, "Tool arguments do not match inputSchema."
	}
	for key := range argumentsValue {
		if key == "api" || key == "mcp_principal" || key == "mcp_tool" || strings.HasPrefix(key, "_headers") || strings.HasPrefix(key, "_remote") {
			return nil, "Tool arguments contain a reserved parameter."
		}
	}
	arguments := cloneParams(argumentsValue)
	arguments["mcp_principal"] = principal
	arguments["mcp_tool"] = tool.Name
	arguments["api"] = tool.API
	backing, ok := snapshot.Definitions[tool.API].(map[string]interface{})
	if !ok || getAPIType(backing) != apiTypeAPI {
		return nil, "Tool backing API is unavailable."
	}
	scriptPath := getAPIString(backing, "script")
	if scriptPath == "" {
		return nil, "Tool backing API is unavailable."
	}
	value, err := runJavaScriptValueWithSnapshot(snapshot, scriptPath, arguments, nil)
	if err != nil {
		return nil, "Tool execution failed."
	}
	structured, body, err := mcpStructuredResult(value)
	if err != nil {
		return nil, "Tool returned invalid JSON."
	}
	if len(body) > maxMCPToolResultBytes {
		return nil, "Tool result is too large."
	}
	if tool.OutputSchema != nil {
		if err := validateMCPJSONSchemaValue(tool.OutputSchema, structured); err != nil {
			return nil, "Tool result does not match outputSchema."
		}
	}
	return map[string]interface{}{
		"content":           []map[string]interface{}{{"type": "text", "text": string(body)}},
		"structuredContent": structured,
		"isError":           false,
	}, ""
}

func mcpStructuredResult(value goja.Value) (interface{}, []byte, error) {
	if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return nil, nil, fmt.Errorf("empty result")
	}
	exported := value.Export()
	if text, ok := exported.(string); ok {
		body := []byte(text)
		var structured interface{}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err := decoder.Decode(&structured); err != nil {
			return nil, nil, err
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return nil, nil, fmt.Errorf("trailing JSON data")
		}
		return structured, body, nil
	}
	body, err := json.Marshal(exported)
	if err != nil {
		return nil, nil, err
	}
	var structured interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&structured); err != nil {
		return nil, nil, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, nil, fmt.Errorf("trailing JSON data")
	}
	return structured, body, nil
}

type mcpStdioLifecycle int

const (
	mcpStdioCreated mcpStdioLifecycle = iota
	mcpStdioWaitingForInitialized
	mcpStdioReady
)

type mcpStdioProtocolResponse struct {
	Payload interface{}
	Respond bool
}

func serveMCPStdio(input io.Reader, output io.Writer, snapshot *APIConfigSnapshot, mcp *MCPServerConfig) error {
	if input == nil || output == nil || snapshot == nil || mcp == nil {
		return fmt.Errorf("stdio MCP input, output, and configuration are required")
	}
	if !mcpSupportsTransport(mcp, "stdio") {
		return fmt.Errorf("MCP API %q does not enable stdio", mcp.Name)
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxMCPRequestBytes+1)
	state := mcpStdioCreated
	for scanner.Scan() {
		message := append([]byte(nil), scanner.Bytes()...)
		if len(message) > maxMCPRequestBytes {
			return fmt.Errorf("stdio MCP message exceeds %d bytes", maxMCPRequestBytes)
		}
		response := handleMCPStdioMessage(snapshot, mcp, &state, message)
		if !response.Respond {
			continue
		}
		if err := writeMCPStdioMessage(output, response.Payload); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stdio MCP input failed or message exceeded %d bytes: %w", maxMCPRequestBytes, err)
	}
	return nil
}

func handleMCPStdioMessage(snapshot *APIConfigSnapshot, mcp *MCPServerConfig, state *mcpStdioLifecycle, message []byte) mcpStdioProtocolResponse {
	request, code, decodeMessage := decodeMCPStdioRequest(message)
	if code != 0 {
		return mcpStdioError(nil, code, decodeMessage)
	}
	isNotification := len(request.ID) == 0
	if request.Method == "notifications/initialized" {
		if !isNotification {
			return mcpStdioError(request.ID, -32600, "notifications/initialized must be a notification")
		}
		if *state == mcpStdioWaitingForInitialized && mcpParamsAreObjectOrEmpty(request.Params) {
			*state = mcpStdioReady
		}
		return mcpStdioProtocolResponse{}
	}
	if isNotification {
		return mcpStdioProtocolResponse{}
	}
	if request.Method == "initialize" {
		if *state != mcpStdioCreated {
			return mcpStdioError(request.ID, -32600, "MCP server is already initialized")
		}
		var params struct {
			ProtocolVersion string                 `json:"protocolVersion"`
			Capabilities    map[string]interface{} `json:"capabilities"`
			ClientInfo      map[string]interface{} `json:"clientInfo"`
			Meta            map[string]interface{} `json:"_meta"`
		}
		if !decodeMCPParams(request.Params, &params) || !mcpProtocolVersionAllowed(params.ProtocolVersion, mcp.ProtocolVersions) {
			return mcpStdioError(request.ID, -32602, "unsupported or missing protocolVersion")
		}
		*state = mcpStdioWaitingForInitialized
		return mcpStdioResult(request.ID, mcpInitializeResult(mcp, params.ProtocolVersion))
	}
	if *state != mcpStdioReady {
		return mcpStdioError(request.ID, -32002, "MCP server is not initialized")
	}
	switch request.Method {
	case "ping":
		if !mcpParamsAreObjectOrEmpty(request.Params) {
			return mcpStdioError(request.ID, -32602, "Invalid params")
		}
		return mcpStdioResult(request.ID, map[string]interface{}{})
	case "tools/list":
		if !mcpParamsAreObjectOrEmpty(request.Params) {
			return mcpStdioError(request.ID, -32602, "Invalid params")
		}
		return mcpStdioResult(request.ID, map[string]interface{}{"tools": mcpToolListForTransport(mcp, false)})
	case "tools/call":
		return handleMCPStdioToolCall(snapshot, mcp, request)
	default:
		return mcpStdioError(request.ID, -32601, "Method not found")
	}
}

func decodeMCPStdioRequest(message []byte) (mcpRPCRequest, int, string) {
	trimmed := bytes.TrimSpace(message)
	if len(trimmed) == 0 {
		return mcpRPCRequest{}, -32700, "Parse error"
	}
	if trimmed[0] != '{' {
		if !json.Valid(trimmed) {
			return mcpRPCRequest{}, -32700, "Parse error"
		}
		return mcpRPCRequest{}, -32600, "Invalid Request"
	}
	if err := validateNoDuplicateJSONKeys(trimmed); err != nil {
		return mcpRPCRequest{}, -32600, "Invalid Request"
	}
	var request mcpRPCRequest
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return mcpRPCRequest{}, -32700, "Parse error"
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return mcpRPCRequest{}, -32700, "Parse error"
	}
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" || !validMCPRequestID(request.ID) {
		return mcpRPCRequest{}, -32600, "Invalid Request"
	}
	return request, 0, ""
}

func handleMCPStdioToolCall(snapshot *APIConfigSnapshot, mcp *MCPServerConfig, request mcpRPCRequest) mcpStdioProtocolResponse {
	params, ok := decodeMCPToolCallParams(request.Params)
	if !ok {
		return mcpStdioError(request.ID, -32602, "Invalid params")
	}
	tool := findMCPTool(mcp, params.Name)
	if tool == nil {
		return mcpStdioError(request.ID, -32602, "Unknown tool")
	}
	requiredScopes := mcpToolScopes(*tool)
	principal := map[string]interface{}{
		"user_id":   "local-process",
		"username":  "local-process",
		"client_id": "stdio",
		"transport": "stdio",
		"scope":     strings.Join(requiredScopes, " "),
		"scopes":    requiredScopes,
	}
	release, acquired := acquireMCPExecutionSlot(mcp.Name+":stdio", mcpMaxConcurrent(mcp))
	if !acquired {
		return mcpStdioError(request.ID, -32603, "MCP server is busy")
	}
	defer release()
	payload, toolError := executeMCPTool(snapshot, tool, params.Arguments, principal)
	if toolError != "" {
		return mcpStdioResult(request.ID, map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": toolError}},
			"isError": true,
		})
	}
	return mcpStdioResult(request.ID, payload)
}

func mcpStdioResult(id json.RawMessage, result interface{}) mcpStdioProtocolResponse {
	return mcpStdioProtocolResponse{Respond: true, Payload: map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      rawMCPID(id),
		"result":  result,
	}}
}

func mcpStdioError(id json.RawMessage, code int, message string) mcpStdioProtocolResponse {
	return mcpStdioProtocolResponse{Respond: true, Payload: map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      rawMCPID(id),
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}}
}

func writeMCPStdioMessage(output io.Writer, payload interface{}) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("stdio MCP response could not be serialized")
	}
	if len(encoded) > maxMCPResponseBytes {
		return fmt.Errorf("stdio MCP response exceeds %d bytes", maxMCPResponseBytes)
	}
	encoded = append(encoded, '\n')
	written, err := output.Write(encoded)
	if err != nil {
		return fmt.Errorf("stdio MCP output failed: %w", err)
	}
	if written != len(encoded) {
		return fmt.Errorf("stdio MCP output failed: %w", io.ErrShortWrite)
	}
	return nil
}

func validateMCPAccessToken(snapshot *APIConfigSnapshot, mcp *MCPServerConfig, runtimeURLs mcpRuntimeURLs, authorization, tool string, requiredScopes []string) (interface{}, bool, bool) {
	if !mcpOAuthConfigured(mcp.OAuth) {
		return map[string]interface{}{"anonymous": true}, true, false
	}
	value, err := invokeOAuthHook(snapshot, mcp, runtimeURLs, "oauthValidateAccessToken", map[string]interface{}{
		"authorization":   authorization,
		"tool":            tool,
		"required_scopes": requiredScopes,
	})
	if err != nil {
		return nil, false, false
	}
	result, ok := value.(map[string]interface{})
	if !ok {
		return nil, false, false
	}
	authenticated, _ := result["authenticated"].(bool)
	forbidden, _ := result["forbidden"].(bool)
	if !authenticated {
		return nil, false, forbidden
	}
	principal, exists := result["principal"]
	if !exists || principal == nil {
		return nil, false, false
	}
	return principal, true, false
}

func mcpOAuthChallenge(runtimeURLs mcpRuntimeURLs, scopes []string, errorCode, description string) string {
	return fmt.Sprintf(
		`Bearer resource_metadata="%s", scope="%s", error="%s", error_description="%s"`,
		httpAuthQuotedString(runtimeURLs.ProtectedResourceMetadata),
		httpAuthQuotedString(strings.Join(scopes, " ")),
		httpAuthQuotedString(errorCode),
		httpAuthQuotedString(description),
	)
}

func httpAuthQuotedString(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch character {
		case '\\', '"':
			builder.WriteByte('\\')
			builder.WriteRune(character)
		default:
			if character >= 0x20 && character != 0x7f {
				builder.WriteRune(character)
			}
		}
	}
	return builder.String()
}

func mcpWriteToolError(c *gin.Context, id json.RawMessage, message string) {
	mcpWriteResult(c, id, map[string]interface{}{
		"content": []map[string]interface{}{{"type": "text", "text": message}},
		"isError": true,
	})
}

func mcpRateLimitAllows(endpointName string, limit *MCPRateLimit, remoteAddress string, now time.Time) (bool, time.Duration) {
	if limit == nil {
		return true, 0
	}
	window, err := time.ParseDuration(limit.Window)
	if err != nil || window <= 0 || limit.Requests <= 0 {
		return false, time.Second
	}
	host := remoteAddress
	if parsed, _, err := net.SplitHostPort(remoteAddress); err == nil {
		host = parsed
	}
	key := endpointName + "\x00" + host + "\x00" + strconv.Itoa(limit.Requests) + "\x00" + window.String()
	mcpRateBuckets.Lock()
	defer mcpRateBuckets.Unlock()
	if mcpRateBuckets.LastCleanup.IsZero() || now.Sub(mcpRateBuckets.LastCleanup) >= time.Minute {
		for bucketKey, bucket := range mcpRateBuckets.Buckets {
			if now.Sub(bucket.StartedAt) >= bucket.Window*2 {
				delete(mcpRateBuckets.Buckets, bucketKey)
			}
		}
		mcpRateBuckets.LastCleanup = now
	}
	bucket, exists := mcpRateBuckets.Buckets[key]
	if !exists || now.Before(bucket.StartedAt) || now.Sub(bucket.StartedAt) >= window {
		mcpRateBuckets.Buckets[key] = mcpRateBucket{StartedAt: now, Window: window, Count: 1}
		return true, 0
	}
	if bucket.Count >= limit.Requests {
		return false, window - now.Sub(bucket.StartedAt)
	}
	bucket.Count++
	mcpRateBuckets.Buckets[key] = bucket
	return true, 0
}

func mcpMaxConcurrent(mcp *MCPServerConfig) int {
	if mcp.MaxConcurrent > 0 {
		return mcp.MaxConcurrent
	}
	return 16
}

func acquireMCPExecutionSlot(endpointName string, limit int) (func(), bool) {
	key := endpointName + "\x00" + strconv.Itoa(limit)
	mcpConcurrencyLimiters.Lock()
	limiter := mcpConcurrencyLimiters.Limiters[key]
	if limiter == nil {
		limiter = make(chan struct{}, limit)
		mcpConcurrencyLimiters.Limiters[key] = limiter
	}
	mcpConcurrencyLimiters.Unlock()
	select {
	case limiter <- struct{}{}:
		return func() { <-limiter }, true
	default:
		return func() {}, false
	}
}

func mcpWriteResult(c *gin.Context, id json.RawMessage, result interface{}) {
	mcpWriteHTTPResult(c, id, http.StatusOK, map[string]interface{}{"jsonrpc": "2.0", "id": rawMCPID(id), "result": result})
}

func mcpWriteError(c *gin.Context, id json.RawMessage, code int, message string) {
	mcpWriteHTTPResult(c, id, http.StatusOK, map[string]interface{}{"jsonrpc": "2.0", "id": rawMCPID(id), "error": map[string]interface{}{"code": code, "message": message}})
}

func mcpWriteHTTPResult(c *gin.Context, id json.RawMessage, status int, payload interface{}) {
	object, isObject := payload.(map[string]interface{})
	if !isObject || object["jsonrpc"] == nil {
		payload = map[string]interface{}{"jsonrpc": "2.0", "id": rawMCPID(id), "result": payload}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		status = http.StatusInternalServerError
		encoded = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"MCP response could not be serialized"}}`)
	} else if len(encoded) > maxMCPResponseBytes {
		status = http.StatusInternalServerError
		encoded = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"MCP response is too large"}}`)
	}
	c.Data(status, "application/json; charset=utf-8", encoded)
}

func rawMCPID(id json.RawMessage) interface{} {
	if len(id) == 0 {
		return nil
	}
	return id
}

func mcpOriginAllowed(origin string, mcp *MCPServerConfig, requestOrigin string) bool {
	origin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
	if origin == "" {
		return true
	}
	if strings.EqualFold(origin, strings.TrimSuffix(requestOrigin, "/")) {
		return true
	}
	for _, allowed := range mcp.AllowedOrigins {
		if strings.EqualFold(origin, strings.TrimSuffix(allowed, "/")) {
			return true
		}
	}
	return false
}

func writeMCPCORSHeaders(c *gin.Context, mcp *MCPServerConfig, requestOrigin string) {
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin == "" || !mcpOriginAllowed(origin, mcp, requestOrigin) {
		return
	}
	c.Header("Access-Control-Allow-Origin", origin)
	c.Header("Vary", "Origin")
	c.Header("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization, MCP-Protocol-Version")
	c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
	c.Header("Access-Control-Expose-Headers", "WWW-Authenticate, MCP-Protocol-Version, Retry-After")
}

func handleOAuthHTTP(c *gin.Context, snapshot *APIConfigSnapshot, mcp *MCPServerConfig, apiName, role string) {
	clearWriteDeadline := setProtectedResponseWriteDeadline(c)
	defer clearWriteDeadline()
	c.Header("Cache-Control", "no-store")
	runtimeURLs, err := deriveMCPRuntimeURLs(c.Request, mcp)
	if err != nil {
		c.JSON(http.StatusMisdirectedRequest, gin.H{"error": "Host is not allowed"})
		return
	}
	if !mcpOriginAllowed(c.GetHeader("Origin"), mcp, runtimeURLs.Origin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Origin is not allowed"})
		return
	}
	writeOAuthCORSHeaders(c, mcp, runtimeURLs.Origin)
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusNoContent)
		return
	}
	if role == "authorizationServerMetadata" {
		if c.Request.Method != http.MethodGet {
			c.Header("Allow", "GET, OPTIONS")
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"issuer":                                runtimeURLs.Issuer,
			"authorization_endpoint":                runtimeURLs.AuthorizationEndpoint,
			"token_endpoint":                        runtimeURLs.TokenEndpoint,
			"registration_endpoint":                 runtimeURLs.RegistrationEndpoint,
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"none"},
			"code_challenge_methods_supported":      []string{"S256"},
			"scopes_supported":                      mcp.OAuth.Scopes,
		})
		return
	}
	if role == "protectedResourceMetadata" {
		if c.Request.Method != http.MethodGet {
			c.Header("Allow", "GET, OPTIONS")
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"resource":                 runtimeURLs.Resource,
			"authorization_servers":    []string{runtimeURLs.Issuer},
			"scopes_supported":         mcp.OAuth.Scopes,
			"bearer_methods_supported": []string{"header"},
		})
		return
	}
	if role == "oauthValidateAccessToken" || apiName == mcp.OAuth.VerifyAccess {
		c.Status(http.StatusNotFound)
		return
	}
	hookName := role
	if !oauthMethodAllowed(hookName, c.Request.Method) {
		c.Header("Allow", oauthAllowedMethods(hookName))
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	if err := validateOAuthContentType(hookName, c.Request); err != nil {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported Content-Type"})
		return
	}
	limit := oauthRateLimitForHook(mcp, hookName)
	if allowed, retryAfter := mcpRateLimitAllows(mcp.Name+":oauth:"+hookName, limit, c.Request.RemoteAddr, time.Now()); !allowed {
		c.Header("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retryAfter.Seconds())))))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
		return
	}
	params, err := oauthRequestParams(c.Request)
	if err != nil {
		if strings.Contains(err.Error(), "too large") {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is too large"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		}
		return
	}
	release, acquired := acquireMCPExecutionSlot(mcp.Name+":oauth:"+hookName, oauthMaxConcurrentForHook(mcp, hookName))
	if !acquired {
		c.Header("Retry-After", "1")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth endpoint is busy"})
		return
	}
	defer release()
	value, err := invokeOAuthHook(snapshot, mcp, runtimeURLs, hookName, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "OAuth hook failed"})
		return
	}
	if err := writeOAuthHookResponse(c, value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "OAuth hook returned an invalid response"})
	}
}

func writeOAuthCORSHeaders(c *gin.Context, mcp *MCPServerConfig, requestOrigin string) {
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin == "" || !mcpOriginAllowed(origin, mcp, requestOrigin) {
		return
	}
	c.Header("Access-Control-Allow-Origin", origin)
	c.Header("Vary", "Origin")
	c.Header("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")
	c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

func oauthMethodAllowed(hook, method string) bool {
	switch hook {
	case "oauthAuthorize":
		return method == http.MethodGet || method == http.MethodPost
	case "oauthAdminUser", "oauthRegister", "oauthToken":
		return method == http.MethodPost
	default:
		return false
	}
}

func oauthAllowedMethods(hook string) string {
	if hook == "oauthAuthorize" {
		return "GET, POST, OPTIONS"
	}
	return "POST, OPTIONS"
}

func validateOAuthContentType(hook string, request *http.Request) error {
	if request.Method != http.MethodPost {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	switch hook {
	case "oauthRegister", "oauthAdminUser":
		if mediaType != "application/json" {
			return fmt.Errorf("JSON is required")
		}
	case "oauthAuthorize", "oauthToken":
		if mediaType != "application/x-www-form-urlencoded" {
			return fmt.Errorf("form data is required")
		}
	}
	return nil
}

func oauthRateLimitForHook(mcp *MCPServerConfig, hook string) *MCPRateLimit {
	requests := 60
	switch hook {
	case "oauthAdminUser", "oauthRegister":
		requests = 10
	case "oauthAuthorize":
		requests = 30
	}
	if mcp.RateLimit != nil && mcp.RateLimit.Requests < requests {
		return mcp.RateLimit
	}
	return &MCPRateLimit{Requests: requests, Window: "1m"}
}

func oauthMaxConcurrentForHook(mcp *MCPServerConfig, hook string) int {
	limit := mcpMaxConcurrent(mcp)
	switch hook {
	case "oauthAdminUser":
		return min(limit, 1)
	case "oauthAuthorize":
		return min(limit, 2)
	default:
		return limit
	}
}

func oauthRequestParams(request *http.Request) (map[string]interface{}, error) {
	params := map[string]interface{}{
		"method":       request.Method,
		"request_path": request.URL.Path,
		"query":        oauthValuesForJavaScript(request.URL.Query()),
		"headers": map[string]interface{}{
			"Authorization": request.Header.Get("Authorization"),
			"Content-Type":  request.Header.Get("Content-Type"),
			"Accept":        request.Header.Get("Accept"),
			"Origin":        request.Header.Get("Origin"),
		},
		"authorization": request.Header.Get("Authorization"),
	}
	cookies := map[string]string{}
	for _, cookie := range request.Cookies() {
		cookies[cookie.Name] = cookie.Value
	}
	params["cookies"] = cookies
	if request.Method != http.MethodPost {
		return params, nil
	}
	body, err := readLimitedRequestBody(request, maxMCPRequestBytes)
	if err != nil {
		return nil, fmt.Errorf("request body is too large")
	}
	mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if mediaType == "application/json" {
		if err := validateNoDuplicateJSONKeys(body); err != nil {
			return nil, fmt.Errorf("invalid JSON")
		}
		var value interface{}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if len(bytes.TrimSpace(body)) == 0 || decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, fmt.Errorf("invalid JSON")
		}
		params["body"] = value
		return params, nil
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	params["form"] = oauthValuesForJavaScript(values)
	return params, nil
}

// url.Values is a named Go map whose values are []string.  Passing it
// directly to goja does not produce the plain JavaScript object/Array shape
// expected by OAuth hooks on every supported Go/goja combination.  Convert
// both levels explicitly, while preserving duplicate values so the hook can
// reject ambiguous OAuth parameters.
func oauthValuesForJavaScript(values url.Values) map[string]interface{} {
	result := make(map[string]interface{}, len(values))
	for key, sourceValues := range values {
		items := make([]interface{}, len(sourceValues))
		for index, value := range sourceValues {
			items[index] = value
		}
		result[key] = items
	}
	return result
}

func invokeOAuthHook(snapshot *APIConfigSnapshot, mcp *MCPServerConfig, runtimeURLs mcpRuntimeURLs, hookName string, extra map[string]interface{}) (interface{}, error) {
	apiName := mcpOAuthAPIForRole(mcp, hookName)
	apiPath, err := canonicalAPIEndpointPath(apiName)
	if err != nil {
		return nil, fmt.Errorf("OAuth API is not configured")
	}
	apiDefinition, ok := snapshot.Definitions[apiName].(map[string]interface{})
	if !ok || getAPIType(apiDefinition) != apiTypeAPI {
		return nil, fmt.Errorf("OAuth API is not configured")
	}
	hookPath := getAPIString(apiDefinition, "script")
	if hookPath == "" {
		return nil, fmt.Errorf("OAuth API script is not configured")
	}
	params := map[string]interface{}{
		"oauth_hook":                    hookName,
		"endpoint":                      mcp.Name,
		"oauth_api":                     apiName,
		"resource":                      runtimeURLs.Resource,
		"issuer":                        runtimeURLs.Issuer,
		"path":                          apiPath,
		"scopes":                        mcp.OAuth.Scopes,
		"redirect_uri_allowed_prefixes": mcp.RedirectURIAllowedPrefixes,
		"state_directory":               mcp.OAuth.StateDirectory,
	}
	for key, value := range extra {
		params[key] = value
	}
	// /API名と/?api=API名は同じAPIであるため、policyへ渡すpathは
	// request表記ではなく参照先API名から導出したcanonical pathに固定する。
	params["path"] = apiPath
	return runOAuthHookJavaScript(snapshot, mcp, hookPath, params)
}

func mcpOAuthAPIForRole(mcp *MCPServerConfig, role string) string {
	if mcp == nil {
		return ""
	}
	switch role {
	case "oauthAuthorize":
		return mcp.OAuth.Authorize
	case "oauthToken":
		return mcp.OAuth.Token
	case "oauthRegister":
		return mcp.OAuth.Register
	case "oauthAdminUser":
		return mcp.OAuth.AdminUser
	case "oauthValidateAccessToken":
		return mcp.OAuth.VerifyAccess
	default:
		return ""
	}
}

func runOAuthHookJavaScript(snapshot *APIConfigSnapshot, mcp *MCPServerConfig, scriptPath string, params map[string]interface{}) (interface{}, error) {
	code, err := os.ReadFile(scriptPath)
	if err != nil || len(code) > maxMCPRequestBytes {
		return nil, fmt.Errorf("OAuth hook cannot be read")
	}
	vm := goja.New()
	setupOAuthGojaVM(vm, snapshot, mcp)
	if err := vm.Set("nyanAllParams", params); err != nil {
		return nil, fmt.Errorf("OAuth hook input could not be prepared")
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-time.After(15 * time.Second):
			vm.Interrupt("OAuth hook timed out")
		case <-finished:
		}
	}()
	value, runErr := vm.RunString(string(code))
	close(finished)
	if runErr != nil || value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return nil, fmt.Errorf("OAuth hook execution failed")
	}
	return value.Export(), nil
}

func writeOAuthHookResponse(c *gin.Context, value interface{}) error {
	response, ok := value.(map[string]interface{})
	if !ok {
		return fmt.Errorf("response is not an object")
	}
	status, ok := oauthStatusCode(response["status"])
	if !ok {
		return fmt.Errorf("invalid response status")
	}
	contentType, _ := response["contentType"].(string)
	if contentType == "" {
		contentType = "application/json; charset=utf-8"
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/json" && mediaType != "text/html" && mediaType != "text/plain") {
		return fmt.Errorf("invalid response content type")
	}
	body, err := oauthResponseBody(response["body"])
	if err != nil || len(body) > maxMCPRequestBytes {
		return fmt.Errorf("invalid response body")
	}
	validatedHeaders := map[string]string{}
	if headers, ok := response["headers"].(map[string]interface{}); ok {
		for key, rawValue := range headers {
			if !oauthResponseHeaderAllowed(key) {
				return fmt.Errorf("response header is not allowed")
			}
			value := fmt.Sprint(rawValue)
			if len(value) > 8192 || strings.ContainsAny(value, "\r\n") || !oauthResponseHeaderValueAllowed(key, value) {
				return fmt.Errorf("invalid response header")
			}
			validatedHeaders[http.CanonicalHeaderKey(key)] = value
		}
	}
	for key, value := range validatedHeaders {
		c.Header(key, value)
	}
	c.Header("Cache-Control", "no-store")
	c.Data(status, contentType, body)
	return nil
}

func oauthStatusCode(raw interface{}) (int, bool) {
	switch value := raw.(type) {
	case float64:
		if math.Trunc(value) != value {
			return 0, false
		}
	case float32:
		if math.Trunc(float64(value)) != float64(value) {
			return 0, false
		}
	}
	status, ok := parseStatusCode(raw)
	return status, ok && status >= 100 && status <= 599
}

func oauthResponseHeaderAllowed(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Cache-Control", "Content-Security-Policy", "Location", "Pragma", "Referrer-Policy", "Set-Cookie":
		return true
	default:
		return false
	}
}

func oauthResponseHeaderValueAllowed(name, value string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Location":
		parsed, err := url.Parse(value)
		return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
	case "Set-Cookie":
		secure := false
		httpOnly := false
		sameSite := false
		for _, attribute := range strings.Split(value, ";") {
			attribute = strings.ToLower(strings.TrimSpace(attribute))
			secure = secure || attribute == "secure"
			httpOnly = httpOnly || attribute == "httponly"
			sameSite = sameSite || attribute == "samesite=lax" || attribute == "samesite=strict"
		}
		return secure && httpOnly && sameSite
	default:
		return true
	}
}

func oauthResponseBody(value interface{}) ([]byte, error) {
	switch body := value.(type) {
	case nil:
		return nil, nil
	case string:
		return []byte(body), nil
	case []byte:
		return body, nil
	default:
		return json.Marshal(body)
	}
}

func setupOAuthGojaVM(vm *goja.Runtime, _ *APIConfigSnapshot, mcp *MCPServerConfig) {
	stateRoot := mcp.OAuth.StateDirectory
	vm.Set("nyanOAuthRead", func(key string) string {
		value, err := oauthReadState(stateRoot, key)
		if err != nil {
			if os.IsNotExist(err) {
				return ""
			}
			panic(vm.ToValue("OAuth state read failed"))
		}
		return value
	})
	vm.Set("nyanOAuthWrite", func(key, value string) bool {
		if err := oauthWriteState(stateRoot, key, value); err != nil {
			panic(vm.ToValue("OAuth state write failed"))
		}
		return true
	})
	vm.Set("nyanOAuthDelete", func(key string) bool {
		if err := oauthDeleteState(stateRoot, key); err != nil && !os.IsNotExist(err) {
			panic(vm.ToValue("OAuth state delete failed"))
		}
		return true
	})
	vm.Set("nyanOAuthConsume", func(key string) string {
		value, err := oauthConsumeState(stateRoot, key)
		if err != nil {
			if os.IsNotExist(err) {
				return ""
			}
			panic(vm.ToValue("OAuth state consume failed"))
		}
		return value
	})
	vm.Set("nyanOAuthList", func(namespace string) []string {
		keys, err := oauthListState(stateRoot, namespace)
		if err != nil {
			panic(vm.ToValue("OAuth state list failed"))
		}
		return keys
	})
	vm.Set("nyanRandomBase64URL", func(size int) string {
		value, err := secureRandomBase64URL(size)
		if err != nil {
			panic(vm.ToValue("secure random generation failed"))
		}
		return value
	})
	vm.Set("nyanSHA256Base64URL", sha256Base64URL)
	vm.Set("nyanArgon2idHash", func(password string) string {
		encoded, err := argon2idHash(password)
		if err != nil {
			panic(vm.ToValue("password hashing failed"))
		}
		return encoded
	})
	vm.Set("nyanArgon2idVerify", argon2idVerify)
	vm.Set("nyanOAuthAdminAuthorized", oauthAdminAuthorized)
	vm.Set("nyanBase64Decode", func(value string) string {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return ""
		}
		return string(decoded)
	})
}

func secureRandomBase64URL(size int) (string, error) {
	if size < 16 || size > 128 {
		return "", fmt.Errorf("random size must be between 16 and 128 bytes")
	}
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func sha256Base64URL(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

const (
	argon2Memory      = 64 * 1024
	argon2Iterations  = 3
	argon2Parallelism = 2
	argon2SaltLength  = 16
	argon2KeyLength   = 32
)

func argon2idHash(password string) (string, error) {
	if len(password) < 1 || len(password) > 4096 {
		return "", fmt.Errorf("password length is invalid")
	}
	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	oauthArgon2Slots <- struct{}{}
	defer func() { <-oauthArgon2Slots }()
	hash := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2Memory, argon2Parallelism, argon2KeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argon2Memory, argon2Iterations, argon2Parallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func argon2idVerify(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	if memory != argon2Memory || iterations != argon2Iterations || parallelism != argon2Parallelism {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != argon2SaltLength {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != argon2KeyLength || len(password) > 4096 {
		return false
	}
	oauthArgon2Slots <- struct{}{}
	defer func() { <-oauthArgon2Slots }()
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func oauthAdminAuthorized(authorization string) bool {
	if !strings.HasPrefix(authorization, "Basic ") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(authorization, "Basic ")))
	if err != nil {
		return false
	}
	username, password, found := strings.Cut(string(decoded), ":")
	if !found || globalConfig.OAuthAdmin.Username == "" || globalConfig.OAuthAdmin.Password == "" {
		return false
	}
	return constantTimeStringEqual(username, globalConfig.OAuthAdmin.Username) && constantTimeStringEqual(password, globalConfig.OAuthAdmin.Password)
}

func constantTimeStringEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func oauthReadState(root, key string) (string, error) {
	oauthStateMu.Lock()
	defer oauthStateMu.Unlock()
	return oauthReadStateLocked(root, key)
}

func oauthReadStateLocked(root, key string) (string, error) {
	path, err := resolveOAuthStatePath(root, key, false)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || oauthPermissionsTooBroad(info.Mode(), 0077) {
		return "", fmt.Errorf("OAuth state file is unsafe")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMCPRequestBytes+1))
	if err != nil || len(data) > maxMCPRequestBytes || !json.Valid(data) || validateNoDuplicateJSONKeys(data) != nil {
		return "", fmt.Errorf("OAuth state file is invalid")
	}
	return string(data), nil
}

func oauthWriteState(root, key, value string) error {
	if len(value) > maxMCPRequestBytes || !json.Valid([]byte(value)) || validateNoDuplicateJSONKeys([]byte(value)) != nil {
		return fmt.Errorf("OAuth state must be valid JSON")
	}
	oauthStateMu.Lock()
	defer oauthStateMu.Unlock()
	path, err := resolveOAuthStatePath(root, key, true)
	if err != nil {
		return err
	}
	if err := enforceOAuthStateQuota(root, key, path); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".nyan8-oauth-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := io.WriteString(temp, value); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return removeErr
		}
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			return retryErr
		}
	}
	return syncOAuthStateDirectory(filepath.Dir(path))
}

// File-backed OAuth is intentionally single-process, but its public DCR and
// authorization endpoints must still have a hard storage ceiling.  The hook
// owns record semantics; this primitive only limits regular files per safe
// top-level namespace so an unauthenticated client cannot exhaust the VPS's
// disk or inodes indefinitely.
func enforceOAuthStateQuota(root, key, destination string) error {
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode().IsRegular() {
			return nil // Updating an existing record does not consume a new slot.
		}
		return fmt.Errorf("OAuth state destination is unsafe")
	} else if !os.IsNotExist(err) {
		return err
	}

	cleanKey := filepath.Clean(filepath.FromSlash(key))
	parts := strings.Split(cleanKey, string(os.PathSeparator))
	namespace := parts[0]
	limit := 256
	switch namespace {
	case "users":
		limit = 1000
	case "clients":
		limit = 2048
	case "requests", "codes", "tokens":
		limit = 4096
	}
	scanRoot := root
	if len(parts) > 1 {
		scanRoot = filepath.Join(root, namespace)
	}
	count := 0
	err := filepath.Walk(scanRoot, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("OAuth state namespace contains a symlink")
		}
		if info.Mode().IsRegular() {
			count++
			if count >= limit {
				return fmt.Errorf("OAuth state namespace quota exceeded")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func oauthDeleteState(root, key string) error {
	oauthStateMu.Lock()
	defer oauthStateMu.Unlock()
	path, err := resolveOAuthStatePath(root, key, false)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncOAuthStateDirectory(filepath.Dir(path))
}

func oauthConsumeState(root, key string) (string, error) {
	oauthStateMu.Lock()
	defer oauthStateMu.Unlock()
	value, err := oauthReadStateLocked(root, key)
	if err != nil {
		return "", err
	}
	path, err := resolveOAuthStatePath(root, key, false)
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	if err := syncOAuthStateDirectory(filepath.Dir(path)); err != nil {
		return "", err
	}
	return value, nil
}

func oauthListState(root, namespace string) ([]string, error) {
	oauthStateMu.Lock()
	defer oauthStateMu.Unlock()
	if !oauthStateNamespacePattern.MatchString(namespace) {
		return nil, fmt.Errorf("OAuth state namespace is invalid")
	}
	if err := ensureOAuthStateDirectory(root, false); err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	directoryPath := filepath.Join(root, namespace)
	if relative, err := filepath.Rel(root, directoryPath); err != nil || !filepath.IsLocal(relative) {
		return nil, fmt.Errorf("OAuth state namespace escapes its root")
	}
	info, err := os.Lstat(directoryPath)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || oauthPermissionsTooBroad(info.Mode(), 0077) {
		return nil, fmt.Errorf("OAuth state namespace is unsafe")
	}
	entries, err := os.ReadDir(directoryPath)
	if err != nil {
		return nil, err
	}
	if len(entries) > 4096 {
		return nil, fmt.Errorf("OAuth state namespace is too large")
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		entryInfo, entryErr := entry.Info()
		if entryErr != nil {
			return nil, entryErr
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.Mode().IsRegular() || oauthPermissionsTooBroad(entryInfo.Mode(), 0077) || filepath.Ext(entry.Name()) != ".json" {
			return nil, fmt.Errorf("OAuth state namespace contains an unsafe entry")
		}
		keys = append(keys, namespace+"/"+entry.Name())
	}
	sort.Strings(keys)
	return keys, nil
}

func resolveOAuthStatePath(root, key string, createParent bool) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("OAuth state root must be absolute")
	}
	key = filepath.Clean(filepath.FromSlash(strings.TrimSpace(key)))
	if !filepath.IsLocal(key) || filepath.Ext(key) != ".json" {
		return "", fmt.Errorf("OAuth state key is invalid")
	}
	if err := ensureOAuthStateDirectory(root, createParent); err != nil {
		return "", err
	}
	parts := strings.Split(key, string(os.PathSeparator))
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && createParent {
			if err := os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || oauthPermissionsTooBroad(info.Mode(), 0077) {
			return "", fmt.Errorf("OAuth state directory is unsafe")
		}
	}
	path := filepath.Join(root, key)
	if relative, err := filepath.Rel(root, path); err != nil || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("OAuth state key escapes its root")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("OAuth state file must not be a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

func ensureOAuthStateDirectory(root string, create bool) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) && create {
		if err := os.MkdirAll(root, 0750); err != nil {
			return err
		}
		info, err = os.Lstat(root)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || oauthPermissionsTooBroad(info.Mode(), 0027) {
		return fmt.Errorf("OAuth state root is unsafe")
	}
	return nil
}

func syncOAuthStateDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func oauthPermissionsTooBroad(mode os.FileMode, mask os.FileMode) bool {
	return runtime.GOOS != "windows" && mode.Perm()&mask != 0
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
	handleNyanWithSnapshot(c, currentAPISnapshot())
}

func handleNyanWithSnapshot(c *gin.Context, snapshot *APIConfigSnapshot) {
	if snapshot == nil {
		respondWithError(c, http.StatusInternalServerError, "API configuration is not loaded", nil)
		return
	}

	// 公開済みのAPI定義mapは変更せず、通常APIの公開情報だけを返す。
	responseAPIs := make(map[string]NyanAPIData)
	for key, api := range snapshot.Definitions {
		apiMap, ok := api.(map[string]interface{})
		if !ok || getAPIType(apiMap) != apiTypeAPI {
			continue
		}
		responseAPIs[key] = NyanAPIData{
			Description: getAPIString(apiMap, "description"),
			Push:        getAPIString(apiMap, "push"),
		}
	}

	response := NyanResponse{
		Name:    globalConfig.Name,
		Profile: globalConfig.Profile,
		Version: globalConfig.Version,
		Apis:    responseAPIs,
	}
	c.JSON(http.StatusOK, response)
}

// /nyan/*apiName で特定APIの詳細を返す。
func handleNyanDetail(c *gin.Context) {
	snapshot := currentAPISnapshot()
	apiName := strings.TrimPrefix(c.Param("apiName"), "/")
	if apiName == "" {
		handleNyanWithSnapshot(c, snapshot)
		return
	}
	handleNyanDetailWithSnapshot(c, snapshot, apiName)
}

func handleNyanDetailWithSnapshot(c *gin.Context, snapshot *APIConfigSnapshot, apiName string) {
	if snapshot == nil {
		respondWithError(c, http.StatusInternalServerError, "API configuration is not loaded", nil)
		return
	}

	apiDataRaw, exists := snapshot.Definitions[apiName]
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

	description, _ := apiData["description"].(string)
	apiType := getAPIType(apiData)
	if apiType != apiTypeAPI {
		respondWithError(c, http.StatusNotFound, fmt.Sprintf("API not found: %s", apiName), nil)
		return
	}

	apiSchema, err := resolveAPISchema(apiData)
	if err != nil {
		respondWithError(c, http.StatusInternalServerError, fmt.Sprintf("Failed to resolve API schemas: %s", apiName), err)
		return
	}

	var nyanAcceptedParams map[string]interface{}
	if apiSchema.InputSource == schemaSourceScriptLegacy {
		scriptPath := getAPIString(apiData, "script")
		if params, found, readErr := readStaticLegacyAcceptedParams(scriptPath); readErr == nil && found {
			nyanAcceptedParams = params
		}
	}

	result := map[string]interface{}{
		"api":          apiName,
		"type":         apiType,
		"description":  description,
		"inputSchema":  apiSchema.Input,
		"outputSchema": apiSchema.Output,
		"schemaSource": map[string]interface{}{
			"input":  normalizeSchemaSource(apiSchema.InputSource),
			"output": normalizeSchemaSource(apiSchema.OutputSource),
		},
	}
	if nyanAcceptedParams != nil {
		result["nyanAcceptedParams"] = nyanAcceptedParams
	}

	c.JSON(http.StatusOK, result)
}

func normalizeSchemaSource(source string) string {
	switch source {
	case schemaSourceParamCheck, schemaSourceOutCheck, schemaSourceScriptLegacy:
		return source
	default:
		return schemaSourceUnknown
	}
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

func parseStaticJavaScriptValue(filename, source string) (interface{}, error) {
	program, err := parser.ParseFile(nil, filename, "("+source+");", 0)
	if err != nil {
		return nil, fmt.Errorf("parse static JavaScript value: %w", err)
	}
	if len(program.Body) != 1 {
		return nil, fmt.Errorf("parse static JavaScript value: expected one expression")
	}
	statement, ok := program.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return nil, fmt.Errorf("parse static JavaScript value: expected an expression, got %T", program.Body[0])
	}
	return convertStaticJavaScriptValue(statement.Expression, "$")
}

func convertStaticJavaScriptValue(expression ast.Expression, path string) (interface{}, error) {
	switch value := expression.(type) {
	case *ast.ObjectLiteral:
		result := make(map[string]interface{}, len(value.Value))
		for _, rawProperty := range value.Value {
			property, ok := rawProperty.(*ast.PropertyKeyed)
			if !ok {
				return nil, fmt.Errorf("static JavaScript value at %s: %s are not supported", path, staticJavaScriptPropertyDescription(rawProperty))
			}
			if property.Computed {
				return nil, fmt.Errorf("static JavaScript value at %s: computed property names are not supported", path)
			}
			if property.Kind != ast.PropertyKindValue {
				return nil, fmt.Errorf("static JavaScript value at %s: property kind %q is not supported", path, property.Kind)
			}
			keyLiteral, ok := property.Key.(*ast.StringLiteral)
			if !ok {
				return nil, fmt.Errorf("static JavaScript value at %s: property names must be strings, got %T", path, property.Key)
			}
			key := keyLiteral.Value.String()
			if _, exists := result[key]; exists {
				return nil, fmt.Errorf("static JavaScript value at %s: duplicate property %q", path, key)
			}
			converted, err := convertStaticJavaScriptValue(property.Value, staticJavaScriptChildPath(path, key))
			if err != nil {
				return nil, err
			}
			result[key] = converted
		}
		return result, nil
	case *ast.ArrayLiteral:
		result := make([]interface{}, len(value.Value))
		for index, item := range value.Value {
			itemPath := fmt.Sprintf("%s[%d]", path, index)
			if item == nil {
				return nil, fmt.Errorf("static JavaScript value at %s: array holes are not supported", itemPath)
			}
			converted, err := convertStaticJavaScriptValue(item, itemPath)
			if err != nil {
				return nil, err
			}
			result[index] = converted
		}
		return result, nil
	case *ast.StringLiteral:
		return value.Value.String(), nil
	case *ast.NumberLiteral:
		return staticJavaScriptNumber(value.Value, path)
	case *ast.BooleanLiteral:
		return value.Value, nil
	case *ast.NullLiteral:
		return nil, nil
	case *ast.UnaryExpression:
		if value.Postfix || (value.Operator != token.MINUS && value.Operator != token.PLUS) {
			return nil, fmt.Errorf("static JavaScript value at %s: unary operator %q is not supported", path, value.Operator)
		}
		numberLiteral, ok := value.Operand.(*ast.NumberLiteral)
		if !ok {
			return nil, fmt.Errorf("static JavaScript value at %s: unary %q requires a numeric literal", path, value.Operator)
		}
		number, err := staticJavaScriptNumber(numberLiteral.Value, path)
		if err != nil {
			return nil, err
		}
		if value.Operator == token.PLUS {
			return number, nil
		}
		switch number := number.(type) {
		case int64:
			return -number, nil
		case float64:
			return -number, nil
		default:
			return nil, fmt.Errorf("static JavaScript value at %s: unsupported numeric value %T", path, number)
		}
	default:
		return nil, fmt.Errorf("static JavaScript value at %s: %s are not supported", path, staticJavaScriptExpressionDescription(expression))
	}
}

func staticJavaScriptNumber(value interface{}, path string) (interface{}, error) {
	switch number := value.(type) {
	case int64:
		return number, nil
	case float64:
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, fmt.Errorf("static JavaScript value at %s: non-finite numbers are not JSON-compatible", path)
		}
		return number, nil
	default:
		return nil, fmt.Errorf("static JavaScript value at %s: numeric value %T is not JSON-compatible", path, value)
	}
}

func staticJavaScriptChildPath(parent, key string) string {
	if key != "" && !strings.ContainsAny(key, ".[]") {
		return parent + "." + key
	}
	return fmt.Sprintf("%s[%q]", parent, key)
}

func staticJavaScriptPropertyDescription(property ast.Property) string {
	switch property.(type) {
	case *ast.SpreadElement:
		return "spread properties"
	case *ast.PropertyShort:
		return "shorthand properties"
	default:
		return fmt.Sprintf("properties of type %T", property)
	}
}

func staticJavaScriptExpressionDescription(expression ast.Expression) string {
	switch expression.(type) {
	case *ast.CallExpression:
		return "function calls"
	case *ast.Identifier:
		return "identifier references"
	case *ast.SpreadElement:
		return "spread elements"
	case *ast.ConditionalExpression:
		return "conditional expressions"
	case *ast.TemplateLiteral:
		return "template literals"
	case *ast.FunctionLiteral, *ast.ArrowFunctionLiteral:
		return "function values"
	case *ast.BinaryExpression:
		return "computed expressions"
	default:
		return fmt.Sprintf("expressions of type %T", expression)
	}
}

func readStaticJavaScriptObjectConstant(filePath, constantName string) (map[string]interface{}, bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false, fmt.Errorf("read JavaScript file %s: %w", filePath, err)
	}
	return extractStaticJavaScriptObjectConstant(filePath, data, constantName)
}

func extractStaticJavaScriptObjectConstant(filename string, source []byte, constantName string) (map[string]interface{}, bool, error) {
	converted, found, err := extractStaticJavaScriptConstant(filename, source, constantName)
	if err != nil || !found {
		return nil, found, err
	}
	object, ok := converted.(map[string]interface{})
	if !ok {
		return nil, false, fmt.Errorf("JavaScript file %s: %s must be a static object literal, got %T", filename, constantName, converted)
	}
	return object, true, nil
}

func extractStaticJavaScriptConstant(filename string, source []byte, constantName string) (interface{}, bool, error) {
	if strings.TrimSpace(constantName) == "" {
		return nil, false, fmt.Errorf("JavaScript constant name is empty")
	}
	program, err := parser.ParseFile(nil, filename, source, 0)
	if err != nil {
		return nil, false, fmt.Errorf("parse JavaScript file %s: %w", filename, err)
	}

	var initializer ast.Expression
	for _, statement := range program.Body {
		switch declaration := statement.(type) {
		case *ast.LexicalDeclaration:
			for _, binding := range declaration.List {
				if !staticJavaScriptBindingHasName(binding, constantName) {
					continue
				}
				if declaration.Token != token.CONST {
					return nil, false, fmt.Errorf("JavaScript file %s: %s must be declared with const", filename, constantName)
				}
				if initializer != nil {
					return nil, false, fmt.Errorf("JavaScript file %s: duplicate declaration of %s", filename, constantName)
				}
				if binding.Initializer == nil {
					return nil, false, fmt.Errorf("JavaScript file %s: %s has no initializer", filename, constantName)
				}
				initializer = binding.Initializer
			}
		case *ast.VariableStatement:
			for _, binding := range declaration.List {
				if staticJavaScriptBindingHasName(binding, constantName) {
					return nil, false, fmt.Errorf("JavaScript file %s: %s must be declared with const", filename, constantName)
				}
			}
		}
	}

	if initializer == nil {
		return nil, false, nil
	}
	converted, err := convertStaticJavaScriptValue(initializer, constantName)
	if err != nil {
		return nil, false, fmt.Errorf("JavaScript file %s: %w", filename, err)
	}
	return converted, true, nil
}

func staticJavaScriptBindingHasName(binding *ast.Binding, constantName string) bool {
	if binding == nil {
		return false
	}
	identifier, ok := binding.Target.(*ast.Identifier)
	return ok && identifier.Name.String() == constantName
}

func resolveAPISchema(apiConfig map[string]interface{}) (APISchema, error) {
	resolved := unknownAPISchema()

	paramCheckPath := getAPIString(apiConfig, "paramCheck", "paramcheck", "check")
	if paramCheckPath != "" {
		input, found, err := readOptionalStaticJavaScriptObjectConstant(paramCheckPath, "nyanInputSchema")
		if err != nil {
			return APISchema{}, fmt.Errorf("input schema from paramCheck: %w", err)
		}
		if found {
			resolved.Input = input
			resolved.InputSource = schemaSourceParamCheck
		}
	}

	outCheckPath := getAPIString(apiConfig, "outCheck", "outcheck")
	if outCheckPath != "" {
		output, found, err := readOptionalStaticJavaScriptObjectConstant(outCheckPath, "nyanOutputSchema")
		if err != nil {
			return APISchema{}, fmt.Errorf("output schema from outCheck: %w", err)
		}
		if found {
			resolved.Output = output
			resolved.OutputSource = schemaSourceOutCheck
		}
	}

	if scriptPath := getAPIString(apiConfig, "script"); scriptPath != "" && resolved.InputSource == schemaSourceUnknown {
		acceptedParams, found, err := readStaticLegacyAcceptedParams(scriptPath)
		if err == nil && found {
			resolved.Input = legacyInputSchema(acceptedParams)
			resolved.InputSource = schemaSourceScriptLegacy
		}
	}

	return resolved, nil
}

func readStaticLegacyAcceptedParams(filePath string) (map[string]interface{}, bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false, err
	}
	acceptedValue, found, err := extractStaticJavaScriptConstant(filePath, data, "nyanAcceptedParams")
	if err != nil {
		return nil, false, err
	}
	if !found {
		return map[string]interface{}{}, false, nil
	}
	acceptedParams, ok := acceptedValue.(map[string]interface{})
	if !ok {
		return nil, false, fmt.Errorf("nyanAcceptedParams must be a static object literal, got %T", acceptedValue)
	}
	return acceptedParams, true, nil
}

func readOptionalStaticJavaScriptObjectConstant(filePath, constantName string) (map[string]interface{}, bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false, nil
	}
	return extractStaticJavaScriptObjectConstant(filePath, data, constantName)
}

func unknownAPISchema() APISchema {
	return APISchema{
		Input:        map[string]interface{}{},
		Output:       map[string]interface{}{},
		InputSource:  schemaSourceUnknown,
		OutputSource: schemaSourceUnknown,
	}
}

func legacyInputSchema(params map[string]interface{}) map[string]interface{} {
	properties := make(map[string]interface{}, len(params))
	for name, value := range params {
		properties[name] = legacyValueSchema(value)
	}
	return map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": true,
	}
}

func legacyValueSchema(value interface{}) map[string]interface{} {
	schema := make(map[string]interface{})
	switch value := value.(type) {
	case string:
		schema["type"] = "string"
	case bool:
		schema["type"] = "boolean"
	case float64:
		if math.IsInf(value, 0) || math.IsNaN(value) {
			return schema
		}
		if math.Trunc(value) == value {
			schema["type"] = "integer"
		} else {
			schema["type"] = "number"
		}
	case float32:
		number := float64(value)
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return schema
		}
		if math.Trunc(number) == number {
			schema["type"] = "integer"
		} else {
			schema["type"] = "number"
		}
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		schema["type"] = "integer"
	case map[string]interface{}:
		properties := make(map[string]interface{}, len(value))
		for name, item := range value {
			properties[name] = legacyValueSchema(item)
		}
		schema["type"] = "object"
		schema["properties"] = properties
		schema["additionalProperties"] = true
	case []interface{}:
		schema["type"] = "array"
		schema["items"] = legacyArrayItemsSchema(value)
	case nil:
		return schema
	default:
		return schema
	}
	schema["examples"] = []interface{}{cloneJSONCompatibleValue(value)}
	return schema
}

func legacyArrayItemsSchema(values []interface{}) map[string]interface{} {
	if len(values) == 0 {
		return map[string]interface{}{}
	}
	first := legacyValueSchema(values[0])
	delete(first, "examples")
	for _, value := range values[1:] {
		candidate := legacyValueSchema(value)
		delete(candidate, "examples")
		if !reflect.DeepEqual(first, candidate) {
			return map[string]interface{}{}
		}
	}
	return first
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
	// Do not trust client-controlled forwarding headers. In production the
	// trusted PROXY protocol listener has already replaced RemoteAddr with the
	// HAProxy-authenticated source address.
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

const defaultAPIHotReloadCheckInterval = time.Second

const (
	maxMCPRequestBytes    = 1 << 20
	maxMCPToolResultBytes = 2 << 20
	maxMCPResponseBytes   = 4 << 20
	mcpProtocol20250618   = "2025-06-18"
	mcpProtocol20251125   = "2025-11-25"
)

var (
	mcpScopePattern            = regexp.MustCompile(`^[A-Za-z0-9._~:+/-]{1,128}$`)
	mcpToolNamePattern         = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	mcpDNSLabelPattern         = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	oauthStateNamespacePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

type MCPRateLimit struct {
	Requests int    `json:"requests"`
	Window   string `json:"window"`
}

type MCPOAuthConfig struct {
	AuthorizationServerMetadata string `json:"authorizationServerMetadata"`
	ProtectedResourceMetadata   string `json:"protectedResourceMetadata"`
	Authorize                   string `json:"authorize"`
	Token                       string `json:"token"`
	Register                    string `json:"register"`
	AdminUser                   string `json:"adminUser,omitempty"`
	VerifyAccess                string `json:"verifyAccess"`

	// The state directory and supported scopes are derived while the complete
	// API graph is validated.  They are runtime values, not duplicated MCP
	// configuration.
	StateDirectory string   `json:"-"`
	Scopes         []string `json:"-"`
}

type MCPToolConfig struct {
	Name            string                   `json:"name"`
	API             string                   `json:"api"`
	Title           string                   `json:"title"`
	Description     string                   `json:"description"`
	InputSchema     map[string]interface{}   `json:"inputSchema"`
	OutputSchema    map[string]interface{}   `json:"outputSchema"`
	SecuritySchemes []map[string]interface{} `json:"securitySchemes"`
	Annotations     map[string]interface{}   `json:"annotations"`
}

// MCPServerConfig is the validated MCP view stored with one immutable API
// snapshot. Public paths come from API names and public URLs are derived from
// each validated request; neither is duplicated in api.json.
type MCPServerConfig struct {
	Name                       string         `json:"-"`
	SourcePath                 string         `json:"-"`
	Type                       string         `json:"type"`
	Transport                  string         `json:"transport"`
	ProtocolVersions           []string       `json:"protocolVersions,omitempty"`
	AllowedOrigins             []string       `json:"allowedOrigins"`
	RedirectURIAllowedPrefixes []string       `json:"redirectURIAllowedPrefixes,omitempty"`
	RateLimit                  *MCPRateLimit  `json:"rateLimit,omitempty"`
	MaxConcurrent              int            `json:"maxConcurrent,omitempty"`
	OAuth                      MCPOAuthConfig `json:"oauth,omitempty"`
	ToolAPIs                   []string       `json:"tools"`
	Instructions               string         `json:"instructions,omitempty"`

	Path  string          `json:"-"`
	Tools []MCPToolConfig `json:"-"`
}

type rejectingMCPJSONSchemaLoader struct{}

func (rejectingMCPJSONSchemaLoader) Load(location string) (interface{}, error) {
	return nil, fmt.Errorf("external JSON Schema resource is not allowed: %s", location)
}

func compileMCPJSONSchema(schema map[string]interface{}) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(rejectingMCPJSONSchemaLoader{})
	const location = "urn:nyan8:mcp-schema"
	if err := compiler.AddResource(location, schema); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

func validateMCPJSONSchemaValue(schema map[string]interface{}, value interface{}) error {
	compiled, err := compileMCPJSONSchema(schema)
	if err != nil {
		return err
	}
	return compiled.Validate(value)
}

func buildMCPServerConfigs(rootPath string, definitions map[string]interface{}, sources map[string]string) (map[string]*MCPServerConfig, error) {
	result := make(map[string]*MCPServerConfig)
	for _, name := range sortedDefinitionNames(definitions) {
		raw, ok := definitions[name].(map[string]interface{})
		if !ok || getAPIType(raw) != apiTypeMCP {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("MCP endpoint %s cannot be decoded", name)
		}
		var candidate MCPServerConfig
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&candidate); err != nil {
			return nil, fmt.Errorf("MCP endpoint %s cannot be decoded: %w", name, err)
		}
		candidate.Name = name
		candidate.SourcePath = sources[name]
		if candidate.SourcePath == "" {
			candidate.SourcePath = rootPath
		}
		if err := resolveAndValidateMCPConfig(&candidate, definitions); err != nil {
			return nil, fmt.Errorf("MCP endpoint %s: %w", name, err)
		}
		result[name] = &candidate
	}
	publicOwners := make(map[string]string)
	for _, name := range sortedMCPServerNames(result) {
		mcp := result[name]
		if !mcpSupportsTransport(mcp, "streamable_http") {
			continue
		}
		for _, apiName := range mcpPublicOAuthAPINames(mcp) {
			if owner, exists := publicOwners[apiName]; exists && owner != name {
				return nil, fmt.Errorf("OAuth API %q is referenced as a public endpoint by MCP definitions %q and %q", apiName, owner, name)
			}
			publicOwners[apiName] = name
		}
	}
	return result, nil
}

func sortedMCPServerNames(servers map[string]*MCPServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedDefinitionNames(definitions map[string]interface{}) []string {
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func resolveAndValidateMCPConfig(mcp *MCPServerConfig, definitions map[string]interface{}) error {
	path, err := canonicalAPIEndpointPath(mcp.Name)
	if err != nil {
		return err
	}
	mcp.Path = path
	if err := resolveMCPTransport(mcp); err != nil {
		return err
	}
	if len(mcp.ProtocolVersions) == 0 {
		mcp.ProtocolVersions = []string{mcpProtocol20251125, mcpProtocol20250618}
	}
	seenVersions := make(map[string]struct{}, len(mcp.ProtocolVersions))
	for _, version := range mcp.ProtocolVersions {
		if version != mcpProtocol20250618 && version != mcpProtocol20251125 {
			return fmt.Errorf("unsupported protocolVersion %q", version)
		}
		if _, exists := seenVersions[version]; exists {
			return fmt.Errorf("duplicate protocolVersion %q", version)
		}
		seenVersions[version] = struct{}{}
	}

	httpEnabled := mcpSupportsTransport(mcp, "streamable_http")
	if httpEnabled && len(mcp.AllowedOrigins) == 0 {
		return fmt.Errorf("allowedOrigins is required")
	}
	seenOrigins := map[string]struct{}{}
	for _, allowed := range mcp.AllowedOrigins {
		parsed, parseErr := parseMCPOrigin(allowed)
		if parseErr != nil {
			return fmt.Errorf("invalid allowedOrigin %q", allowed)
		}
		canonical := parsed.Scheme + "://" + parsed.Host
		if _, exists := seenOrigins[canonical]; exists {
			return fmt.Errorf("duplicate allowedOrigin %q", canonical)
		}
		seenOrigins[canonical] = struct{}{}
	}
	if mcp.RateLimit != nil {
		window, parseErr := time.ParseDuration(mcp.RateLimit.Window)
		if parseErr != nil || mcp.RateLimit.Requests < 1 || mcp.RateLimit.Requests > 10000 || window < time.Second || window > 24*time.Hour {
			return fmt.Errorf("invalid rateLimit")
		}
	}
	if mcp.MaxConcurrent < 0 || mcp.MaxConcurrent > 256 {
		return fmt.Errorf("maxConcurrent must be between 0 and 256")
	}

	oauthEnabled := mcpOAuthConfigured(mcp.OAuth)
	if oauthEnabled && !httpEnabled {
		return fmt.Errorf("oauth requires the streamable_http transport")
	}
	if oauthEnabled && len(mcp.RedirectURIAllowedPrefixes) == 0 {
		return fmt.Errorf("redirectURIAllowedPrefixes is required when oauth is configured")
	}
	for _, prefix := range mcp.RedirectURIAllowedPrefixes {
		parsed, parseErr := url.Parse(prefix)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || !strings.HasSuffix(parsed.Path, "/") || parsed.EscapedPath() != parsed.Path {
			return fmt.Errorf("invalid redirect URI prefix %q", prefix)
		}
	}
	if oauthEnabled {
		if err := resolveAndValidateMCPOAuthAPIs(mcp, definitions); err != nil {
			return err
		}
	}

	if len(mcp.ToolAPIs) == 0 {
		return fmt.Errorf("at least one Tool is required")
	}
	toolNames := map[string]struct{}{}
	serverScopes := make(map[string]struct{}, len(mcp.OAuth.Scopes))
	for _, scope := range mcp.OAuth.Scopes {
		serverScopes[scope] = struct{}{}
	}
	mcp.Tools = make([]MCPToolConfig, 0, len(mcp.ToolAPIs))
	for _, rawAPIName := range mcp.ToolAPIs {
		apiName := strings.TrimSpace(rawAPIName)
		if !mcpToolNamePattern.MatchString(apiName) {
			return fmt.Errorf("Tool API name %q cannot be used as an MCP Tool name", rawAPIName)
		}
		if _, exists := toolNames[apiName]; exists {
			return fmt.Errorf("duplicate Tool API %q", apiName)
		}
		toolNames[apiName] = struct{}{}
		backing, ok := definitions[apiName].(map[string]interface{})
		if !ok || getAPIType(backing) != apiTypeAPI || getAPIString(backing, "script") == "" {
			return fmt.Errorf("Tool references an invalid backing API %q", apiName)
		}
		backingInfo, statErr := os.Stat(getAPIString(backing, "script"))
		if statErr != nil || !backingInfo.Mode().IsRegular() {
			return fmt.Errorf("Tool %s backing API script is not a regular file", apiName)
		}
		schema, schemaErr := resolveAPISchema(backing)
		if schemaErr != nil {
			return fmt.Errorf("Tool %s schema cannot be resolved: %w", apiName, schemaErr)
		}
		if _, schemaErr := compileMCPJSONSchema(schema.Input); schemaErr != nil {
			return fmt.Errorf("Tool %s has invalid inputSchema: %w", apiName, schemaErr)
		}
		if schema.OutputSource != schemaSourceUnknown {
			if _, schemaErr := compileMCPJSONSchema(schema.Output); schemaErr != nil {
				return fmt.Errorf("Tool %s has invalid outputSchema: %w", apiName, schemaErr)
			}
		}
		securitySchemes, securityErr := mcpSecuritySchemesFromAPI(backing)
		if securityErr != nil {
			return fmt.Errorf("Tool %s: %w", apiName, securityErr)
		}
		for _, scheme := range securitySchemes {
			if schemeType, _ := scheme["type"].(string); schemeType != "oauth2" {
				return fmt.Errorf("Tool %s contains an unsupported securityScheme", apiName)
			}
		}
		tool := MCPToolConfig{
			Name:            apiName,
			API:             apiName,
			Title:           getAPIString(backing, "title"),
			Description:     getAPIString(backing, "description"),
			InputSchema:     schema.Input,
			SecuritySchemes: securitySchemes,
			Annotations:     mcpAnnotationsFromAPI(backing),
		}
		if tool.Title == "" {
			tool.Title = apiName
		}
		if schema.OutputSource != schemaSourceUnknown {
			tool.OutputSchema = schema.Output
		}
		requiredScopes := mcpToolScopes(tool)
		for _, requiredScope := range requiredScopes {
			if !mcpScopePattern.MatchString(requiredScope) {
				return fmt.Errorf("Tool %s contains invalid OAuth scope %q", apiName, requiredScope)
			}
			if oauthEnabled {
				if _, exists := serverScopes[requiredScope]; !exists {
					return fmt.Errorf("Tool %s requires unknown OAuth scope %q", apiName, requiredScope)
				}
			}
		}
		if oauthEnabled && len(requiredScopes) == 0 {
			return fmt.Errorf("Tool %s must define OAuth scopes on its backing API", apiName)
		}
		mcp.Tools = append(mcp.Tools, tool)
	}
	if oauthEnabled && len(serverScopes) == 0 {
		return fmt.Errorf("OAuth MCP server has no scopes")
	}
	return nil
}

func resolveMCPTransport(mcp *MCPServerConfig) error {
	if mcp == nil {
		return fmt.Errorf("MCP config is missing")
	}
	transport := strings.TrimSpace(mcp.Transport)
	if transport == "" {
		return fmt.Errorf("transport is required")
	}
	if transport != "streamable_http" && transport != "stdio" {
		return fmt.Errorf("unsupported MCP transport %q", mcp.Transport)
	}
	mcp.Transport = transport
	return nil
}

func mcpSupportsTransport(mcp *MCPServerConfig, transport string) bool {
	if mcp == nil {
		return false
	}
	return mcp.Transport == transport
}

func selectMCPStdioServer(snapshot *APIConfigSnapshot, requestedName string) (*MCPServerConfig, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("API configuration is not loaded")
	}
	requestedName = strings.TrimSpace(requestedName)
	if requestedName == "" {
		return nil, fmt.Errorf("--mcp-server is required for stdio mode")
	}
	mcp := snapshot.MCPServers[requestedName]
	if mcp == nil {
		return nil, fmt.Errorf("MCP API %q is not configured", requestedName)
	}
	if !mcpSupportsTransport(mcp, "stdio") {
		return nil, fmt.Errorf("MCP API %q does not enable stdio", requestedName)
	}
	return mcp, nil
}

func canonicalAPIEndpointPath(apiName string) (string, error) {
	if apiName == "" || apiName != strings.TrimSpace(apiName) || strings.HasPrefix(apiName, "/") || strings.HasSuffix(apiName, "/") || strings.Contains(apiName, "//") || strings.ContainsAny(apiName, "\\?#\r\n\t") {
		return "", fmt.Errorf("invalid API name %q for endpoint", apiName)
	}
	for _, part := range strings.Split(apiName, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid API name %q for endpoint", apiName)
		}
	}
	if isReservedNyanAPIName(apiName) {
		return "", fmt.Errorf("API name %q uses the reserved nyan namespace", apiName)
	}
	path := "/" + apiName
	if (&url.URL{Path: path}).EscapedPath() != path {
		return "", fmt.Errorf("API name %q is not a canonical URL path", apiName)
	}
	switch path {
	case "/", "/favicon.ico":
		return "", fmt.Errorf("endpoint path %s is reserved", path)
	}
	return path, nil
}

func isReservedNyanAPIName(apiName string) bool {
	root := strings.SplitN(strings.TrimSpace(apiName), "/", 2)[0]
	return root == "nyan" || root == "nyan-rpc" || strings.HasPrefix(root, "nyan-")
}

func mcpOAuthConfigured(config MCPOAuthConfig) bool {
	return config.AuthorizationServerMetadata != "" || config.ProtectedResourceMetadata != "" || config.Authorize != "" || config.Token != "" || config.Register != "" || config.AdminUser != "" || config.VerifyAccess != ""
}

func resolveAndValidateMCPOAuthAPIs(mcp *MCPServerConfig, definitions map[string]interface{}) error {
	required := map[string]string{
		"authorizationServerMetadata": mcp.OAuth.AuthorizationServerMetadata,
		"protectedResourceMetadata":   mcp.OAuth.ProtectedResourceMetadata,
		"authorize":                   mcp.OAuth.Authorize,
		"token":                       mcp.OAuth.Token,
		"register":                    mcp.OAuth.Register,
		"verifyAccess":                mcp.OAuth.VerifyAccess,
	}
	seen := make(map[string]string)
	for label, apiName := range required {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			return fmt.Errorf("oauth.%s API name is required", label)
		}
		if _, err := canonicalAPIEndpointPath(apiName); err != nil {
			return fmt.Errorf("oauth.%s: %w", label, err)
		}
		if previous, exists := seen[apiName]; exists {
			return fmt.Errorf("oauth.%s and oauth.%s reference the same API %q", label, previous, apiName)
		}
		seen[apiName] = label
		entry, ok := definitions[apiName].(map[string]interface{})
		if !ok || getAPIType(entry) != apiTypeAPI {
			return fmt.Errorf("oauth.%s references invalid API %q", label, apiName)
		}
		if label != "authorizationServerMetadata" && label != "protectedResourceMetadata" {
			if info, err := os.Stat(getAPIString(entry, "script")); err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("oauth.%s API %q must have a regular JavaScript file", label, apiName)
			}
		}
	}
	if mcp.OAuth.AdminUser != "" {
		apiName := strings.TrimSpace(mcp.OAuth.AdminUser)
		if _, err := canonicalAPIEndpointPath(apiName); err != nil {
			return fmt.Errorf("oauth.adminUser: %w", err)
		}
		entry, ok := definitions[apiName].(map[string]interface{})
		if !ok || getAPIType(entry) != apiTypeAPI {
			return fmt.Errorf("oauth.adminUser references invalid API %q", apiName)
		}
		if info, err := os.Stat(getAPIString(entry, "script")); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("oauth.adminUser API %q must have a regular JavaScript file", apiName)
		}
		if previous, exists := seen[apiName]; exists {
			return fmt.Errorf("oauth.adminUser and oauth.%s reference the same API %q", previous, apiName)
		}
	}
	verifyDefinition := definitions[mcp.OAuth.VerifyAccess].(map[string]interface{})
	scopes, ok := stringSliceFromJSON(verifyDefinition["scopes"])
	if !ok || len(scopes) == 0 {
		return fmt.Errorf("oauth.verifyAccess API %q must define scopes", mcp.OAuth.VerifyAccess)
	}
	seenScopes := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if !mcpScopePattern.MatchString(scope) {
			return fmt.Errorf("invalid OAuth scope %q", scope)
		}
		if _, exists := seenScopes[scope]; exists {
			return fmt.Errorf("duplicate OAuth scope %q", scope)
		}
		seenScopes[scope] = struct{}{}
	}
	mcp.OAuth.Scopes = append([]string(nil), scopes...)
	sort.Strings(mcp.OAuth.Scopes)
	stateBase := strings.TrimSpace(globalConfig.OAuthStateRoot)
	if stateBase == "" {
		stateBase = filepath.Join(filepath.Dir(mcp.SourcePath), "oauth-state")
	}
	stateRoot := filepath.Join(stateBase, filepath.FromSlash(mcp.Name))
	absoluteStateRoot, err := filepath.Abs(stateRoot)
	if err != nil {
		return fmt.Errorf("OAuth state directory cannot be derived")
	}
	mcp.OAuth.StateDirectory = filepath.Clean(absoluteStateRoot)
	return nil
}

func mcpSecuritySchemesFromAPI(api map[string]interface{}) ([]map[string]interface{}, error) {
	raw, exists := api["securitySchemes"]
	if !exists {
		if scopes, ok := stringSliceFromJSON(api["scopes"]); ok && len(scopes) > 0 {
			return []map[string]interface{}{{"type": "oauth2", "scopes": scopes}}, nil
		}
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("securitySchemes is invalid")
	}
	var schemes []map[string]interface{}
	if err := json.Unmarshal(data, &schemes); err != nil || len(schemes) == 0 {
		return nil, fmt.Errorf("securitySchemes must be a non-empty array")
	}
	for _, scheme := range schemes {
		scopes := mcpToolScopes(MCPToolConfig{SecuritySchemes: []map[string]interface{}{scheme}})
		if len(scopes) == 0 {
			return nil, fmt.Errorf("OAuth securityScheme must define scopes")
		}
	}
	return schemes, nil
}

func stringSliceFromJSON(raw interface{}) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []interface{}:
		result := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			result = append(result, text)
		}
		return result, true
	default:
		return nil, false
	}
}

func mcpAnnotationsFromAPI(api map[string]interface{}) map[string]interface{} {
	annotations, _ := cloneJSONCompatibleValue(api["annotations"]).(map[string]interface{})
	return annotations
}

func mcpPublicOAuthAPINames(mcp *MCPServerConfig) []string {
	if mcp == nil || !mcpOAuthConfigured(mcp.OAuth) {
		return nil
	}
	result := []string{
		mcp.OAuth.AuthorizationServerMetadata,
		mcp.OAuth.ProtectedResourceMetadata,
		mcp.OAuth.Authorize,
		mcp.OAuth.Token,
		mcp.OAuth.Register,
	}
	if mcp.OAuth.AdminUser != "" {
		result = append(result, mcp.OAuth.AdminUser)
	}
	return result
}

func parseMCPOrigin(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(value), "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid HTTPS origin")
	}
	return parsed, nil
}

func mcpToolScopes(tool MCPToolConfig) []string {
	var scopes []string
	seen := map[string]struct{}{}
	for _, scheme := range tool.SecuritySchemes {
		if schemeType, _ := scheme["type"].(string); schemeType != "oauth2" {
			continue
		}
		switch raw := scheme["scopes"].(type) {
		case []interface{}:
			for _, item := range raw {
				if scope, ok := item.(string); ok {
					if _, exists := seen[scope]; !exists {
						seen[scope] = struct{}{}
						scopes = append(scopes, scope)
					}
				}
			}
		case []string:
			for _, scope := range raw {
				if _, exists := seen[scope]; !exists {
					seen[scope] = struct{}{}
					scopes = append(scopes, scope)
				}
			}
		}
	}
	return scopes
}

type APIHotReloadConfig struct {
	Enabled  bool   `json:"Enabled"`
	Interval string `json:"Interval"`
}

var (
	apiSnapshot        atomic.Pointer[APIConfigSnapshot]
	backgroundRuntimes *backgroundRuntimeManager
)

type mcpRateBucket struct {
	StartedAt time.Time
	Window    time.Duration
	Count     int
}

var mcpRateBuckets = struct {
	sync.Mutex
	Buckets     map[string]mcpRateBucket
	LastCleanup time.Time
}{Buckets: make(map[string]mcpRateBucket)}

var mcpConcurrencyLimiters = struct {
	sync.Mutex
	Limiters map[string]chan struct{}
}{Limiters: make(map[string]chan struct{})}

var oauthStateMu sync.Mutex
var oauthArgon2Slots = make(chan struct{}, 2)

type APIConfigSnapshot struct {
	RootPath    string
	Definitions map[string]interface{}
	Sources     map[string]string
	FileStates  map[string]APIFileState
	Schedules   map[string]scheduleJobConfig
	WSClients   map[string]wsClientConfig
	MCPServers  map[string]*MCPServerConfig
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
	target.WebSocket.MaxConnections = 128
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
	for _, name := range sortedDefinitionNames(definitions) {
		if isReservedNyanAPIName(name) {
			return nil, fileStates, fmt.Errorf("API name %q uses the reserved nyan namespace", name)
		}
	}
	mcpServers, err := buildMCPServerConfigs(apiFilePath, definitions, sources)
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
	snapshot := newAPIConfigSnapshot(apiFilePath, definitions, sources, fileStates, schedules, wsClients)
	snapshot.MCPServers = mcpServers
	return &apiConfigLoadResult{
		Snapshot: snapshot,
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
