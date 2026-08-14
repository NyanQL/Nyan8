package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func initTestLogger() {
	logger = log.New(os.Stdout, "", 0)
	globalConfig = Config{}
	servicePaths = serviceFilePaths{}
}

func TestResolveServiceFilePathsDefaultsToExecDir(t *testing.T) {
	initTestLogger()

	execDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(execDir, "api.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(execDir, "config.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	paths, err := resolveServiceFilePaths(execDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if paths.API.Path != filepath.Join(execDir, "api.json") {
		t.Fatalf("API path = %q, want %q", paths.API.Path, filepath.Join(execDir, "api.json"))
	}
	if paths.API.Source != "default" {
		t.Fatalf("API source = %q, want default", paths.API.Source)
	}
	if paths.Config.Path != filepath.Join(execDir, "config.json") {
		t.Fatalf("Config path = %q, want %q", paths.Config.Path, filepath.Join(execDir, "config.json"))
	}
	if paths.Config.Source != "default" {
		t.Fatalf("Config source = %q, want default", paths.Config.Source)
	}
}

func TestResolveServiceFilePathsPrefersCLIOverEnv(t *testing.T) {
	initTestLogger()

	execDir := t.TempDir()
	envDir := t.TempDir()
	cliDir := t.TempDir()
	for _, dir := range []string{execDir, envDir, cliDir} {
		if err := os.WriteFile(filepath.Join(dir, "api.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NYAN_API_PATH", filepath.Join(envDir, "api.json"))
	t.Setenv("NYAN_CONFIG_PATH", filepath.Join(envDir, "config.json"))

	paths, err := resolveServiceFilePaths(execDir, []string{
		"--api", filepath.Join(cliDir, "api.json"),
		"--config", filepath.Join(cliDir, "config.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if paths.API.Path != filepath.Join(cliDir, "api.json") {
		t.Fatalf("API path = %q, want CLI path", paths.API.Path)
	}
	if paths.API.Source != "--api" {
		t.Fatalf("API source = %q, want --api", paths.API.Source)
	}
	if paths.Config.Path != filepath.Join(cliDir, "config.json") {
		t.Fatalf("Config path = %q, want CLI path", paths.Config.Path)
	}
	if paths.Config.Source != "--config" {
		t.Fatalf("Config source = %q, want --config", paths.Config.Source)
	}
}

func TestResolveStartupOptionsForMCPStdio(t *testing.T) {
	initTestLogger()
	execDir := t.TempDir()
	for _, name := range []string{"api.json", "config.json"} {
		if err := os.WriteFile(filepath.Join(execDir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options, err := resolveStartupOptions(execDir, []string{"--mcp-server", "local_mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if options.MCPServer != "local_mcp" {
		t.Fatalf("stdio options = %#v", options)
	}
	if _, err := resolveStartupOptions(execDir, []string{"--mcp-stdio"}); err == nil {
		t.Fatal("removed --mcp-stdio flag was accepted")
	}
}

func TestAdjustConfigPathsResolvesFromConfigDir(t *testing.T) {
	initTestLogger()

	configBaseDir := t.TempDir()
	config := Config{
		CertFile:          "ssl/localhost.crt",
		KeyFile:           "ssl/localhost.key",
		JavaScriptInclude: []string{"javascript/base.js"},
		Log:               LogConfig{Filename: "logs/nyan8.log"},
		OAuthStateRoot:    "state/oauth",
	}

	adjustConfigPaths(configBaseDir, &config)

	if config.CertFile != filepath.Join(configBaseDir, "ssl/localhost.crt") {
		t.Fatalf("CertFile = %q, want config-relative path", config.CertFile)
	}
	if config.KeyFile != filepath.Join(configBaseDir, "ssl/localhost.key") {
		t.Fatalf("KeyFile = %q, want config-relative path", config.KeyFile)
	}
	if config.JavaScriptInclude[0] != filepath.Join(configBaseDir, "javascript/base.js") {
		t.Fatalf("JavaScriptInclude[0] = %q, want config-relative path", config.JavaScriptInclude[0])
	}
	if config.Log.Filename != filepath.Join(configBaseDir, "logs/nyan8.log") {
		t.Fatalf("Log.Filename = %q, want config-relative path", config.Log.Filename)
	}
	if config.OAuthStateRoot != filepath.Join(configBaseDir, "state/oauth") {
		t.Fatalf("OAuthStateRoot = %q, want config-relative path", config.OAuthStateRoot)
	}
}

func TestRegisterPublicEndpointServesFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	publicDir := filepath.Join(tempDir, "public")
	if err := os.Mkdir(publicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "app.js"), []byte("console.log('nyan');"), 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	registerPublicEndpoint(router, "assets", map[string]interface{}{
		"type": apiTypePublic,
		"path": "./public",
	}, tempDir)

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, want := rec.Body.String(), "console.log('nyan');"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestRegisterPublicEndpointRunsParamCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	publicDir := filepath.Join(tempDir, "public")
	if err := os.Mkdir(publicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "app.js"), []byte("console.log('private');"), 0644); err != nil {
		t.Fatal(err)
	}
	checkScript := `
if (nyanAllParams.allow !== "1") {
  ({ success: false, status: 401, result: { path: nyanAllParams.nyan_public_path } });
} else {
  ({ success: true, status: 200, result: { path: nyanAllParams.nyan_public_path } });
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "check.js"), []byte(checkScript), 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	registerPublicEndpoint(router, "assets", map[string]interface{}{
		"type":       apiTypePublic,
		"path":       "./public",
		"paramcheck": "./check.js",
	}, tempDir)

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	assertParamCheckResponse(t, rec.Body.Bytes(), false, http.StatusUnauthorized)

	req = httptest.NewRequest(http.MethodGet, "/assets/app.js?allow=1", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, want := rec.Body.String(), "console.log('private');"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestRegisterPublicEndpointRunsOutCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	publicDir := filepath.Join(tempDir, "public")
	if err := os.Mkdir(publicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "test.txt"), []byte("blocked"), 0644); err != nil {
		t.Fatal(err)
	}
	outCheckScript := `
({ success: false, status: 409, result: { body: nyanAllParams.nyan_output_body } });
`
	if err := os.WriteFile(filepath.Join(tempDir, "out_check.js"), []byte(outCheckScript), 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	registerPublicEndpoint(router, "public", map[string]interface{}{
		"type":     apiTypePublic,
		"path":     "./public",
		"outCheck": "./out_check.js",
	}, tempDir)

	req := httptest.NewRequest(http.MethodGet, "/public/test.txt", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusConflict, rec.Body.String())
	}
	assertParamCheckResponse(t, rec.Body.Bytes(), false, http.StatusConflict)
	if rec.Body.String() == "blocked" {
		t.Fatal("outCheck failure returned public file content")
	}
}

func TestRegisterDynamicEndpointsRegistersPublicAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	publicDir := filepath.Join(tempDir, "public")
	if err := os.Mkdir(publicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "app.js"), []byte("dynamic"), 0644); err != nil {
		t.Fatal(err)
	}
	apiJSON := []byte(`{
  "assets": {
    "type": "public",
    "path": "./public"
  }
}`)
	if err := os.WriteFile(filepath.Join(tempDir, "api.json"), apiJSON, 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	if err := registerDynamicEndpoints(router, tempDir); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, want := rec.Body.String(), "dynamic"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestMCPToolAllowlistSkipsPublicAndUnlistedAPIs(t *testing.T) {
	initTestLogger()

	tempDir := t.TempDir()
	writeHotReloadTestFile(t, filepath.Join(tempDir, "hello.js"), `JSON.stringify({ok: true});`)
	writeHotReloadTestFile(t, filepath.Join(tempDir, "other.js"), `JSON.stringify({ok: true});`)
	apiJSON := []byte(`{
  "assets": {
    "type": "public",
    "path": "./public"
  },
  "hello": {
    "script": "./hello.js",
    "description": "hello API"
  },
  "other": {
    "script": "./other.js",
    "description": "not exposed"
  },
	"connector": {
    "type": "mcp",
	"transport": "streamable_http",
    "allowedOrigins": ["https://chatgpt.com"],
    "tools": ["hello"]
  }
}`)
	if err := os.WriteFile(filepath.Join(tempDir, "api.json"), apiJSON, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := loadAPIConfigData(filepath.Join(tempDir, "api.json"), tempDir, apiJSON)
	if err != nil {
		t.Fatal(err)
	}
	mcp := result.Snapshot.MCPServers["connector"]
	if mcp == nil || len(mcp.Tools) != 1 {
		t.Fatalf("MCP config = %#v", mcp)
	}
	if got, want := mcp.Path, "/connector"; got != want {
		t.Fatalf("MCP path = %q, want %q", got, want)
	}
	if got, want := mcp.Tools[0].Name, "hello"; got != want {
		t.Fatalf("Tool name = %q, want %q", got, want)
	}
}

func TestDynamicEndpointRunsParamCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	apiJSON := []byte(`{
  "secure": {
    "script": "./main.js",
    "paramCheck": "./check.js"
  }
}`)
	if err := os.WriteFile(filepath.Join(tempDir, "api.json"), apiJSON, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "main.js"), []byte(`JSON.stringify({ status: 200, body: "main ok" });`), 0644); err != nil {
		t.Fatal(err)
	}
	checkScript := `
if (nyanAllParams.allow === "1") {
  ({ success: true, status: 200, result: { api: nyanAllParams.api } });
} else {
  ({ success: false, status: 401, result: { message: "blocked" } });
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "check.js"), []byte(checkScript), 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	if err := registerDynamicEndpoints(router, tempDir); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/secure", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	assertParamCheckResponse(t, rec.Body.Bytes(), false, http.StatusUnauthorized)
	if rec.Body.String() == `{"status":200,"body":"main ok"}` {
		t.Fatal("paramCheck failure ran main script")
	}

	req = httptest.NewRequest(http.MethodGet, "/secure?allow=1", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, want := body["body"], "main ok"; got != want {
		t.Fatalf("body = %v, want %q; response=%q", got, want, rec.Body.String())
	}
}

func TestDynamicEndpointParamCheckOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	apiJSON := []byte(`{
  "secure": {
    "script": "./main.js",
    "paramCheck": "./check.js"
  }
}`)
	if err := os.WriteFile(filepath.Join(tempDir, "api.json"), apiJSON, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "main.js"), []byte(`JSON.stringify({ status: 200, body: "main ok" });`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "check.js"), []byte(`({ success: true, status: 200, result: { checked: true } });`), 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	if err := registerDynamicEndpoints(router, tempDir); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/secure?nyan_mode=checkOnly", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	assertParamCheckResponse(t, rec.Body.Bytes(), true, http.StatusOK)
	if strings.Contains(rec.Body.String(), "main ok") {
		t.Fatal("checkOnly ran main script")
	}
}

func TestDynamicEndpointRunsOutCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	apiJSON := []byte(`{
  "checked": {
    "script": "./main.js",
    "outCheck": "./out_check.js"
  }
}`)
	if err := os.WriteFile(filepath.Join(tempDir, "api.json"), apiJSON, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "main.js"), []byte(`JSON.stringify({ status: 201, body: "created" });`), 0644); err != nil {
		t.Fatal(err)
	}
	outCheckScript := `
if (nyanAllParams.nyan_output.status === 201 && nyanAllParams.nyan_output_body.indexOf("created") >= 0) {
  ({ success: false, status: 409, result: { message: "blocked", body: nyanAllParams.nyan_output.body } });
} else {
  ({ success: true, status: 200, result: null });
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "out_check.js"), []byte(outCheckScript), 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	if err := registerDynamicEndpoints(router, tempDir); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/checked", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusConflict, rec.Body.String())
	}
	assertParamCheckResponse(t, rec.Body.Bytes(), false, http.StatusConflict)
	var checkResp ParamCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &checkResp); err != nil {
		t.Fatal(err)
	}
	result, ok := checkResp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result has unexpected type: %T", checkResp.Result)
	}
	if !strings.Contains(fmt.Sprint(result["body"]), "created") {
		t.Fatalf("outCheck did not receive response body: %#v", result)
	}
}

func TestJSONRPCRunsParamCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	t.Chdir(tempDir)
	apiJSON := []byte(`{
  "secure": {
    "script": "./main.js",
    "paramCheck": "./check.js"
  }
}`)
	if err := os.WriteFile(filepath.Join(tempDir, "api.json"), apiJSON, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "main.js"), []byte(`JSON.stringify({ status: 200, body: "main ok" });`), 0644); err != nil {
		t.Fatal(err)
	}
	checkScript := `
if (nyanAllParams.allow === "1") {
  ({ success: true, status: 200, result: { checked: true } });
} else {
  ({ success: false, status: 401, result: { message: "blocked" } });
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "check.js"), []byte(checkScript), 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.POST("/nyan-rpc", handleJSONRPC)

	reqBody := []byte(`{"jsonrpc":"2.0","method":"secure","params":{},"id":1}`)
	req := httptest.NewRequest(http.MethodPost, "/nyan-rpc", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rpcResp); err != nil {
		t.Fatal(err)
	}
	if rpcResp.Error == nil || rpcResp.Error.Code != -32602 {
		t.Fatalf("error = %#v, want invalid params; body=%q", rpcResp.Error, rec.Body.String())
	}

	reqBody = []byte(`{"jsonrpc":"2.0","method":"secure","params":{"allow":"1","nyan_mode":"checkOnly"},"id":2}`)
	req = httptest.NewRequest(http.MethodPost, "/nyan-rpc", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	rpcResp = JSONRPCResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpcResp); err != nil {
		t.Fatal(err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected error: %#v; body=%q", rpcResp.Error, rec.Body.String())
	}
	result, ok := rpcResp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result has unexpected type: %T", rpcResp.Result)
	}
	if result["success"] != true {
		t.Fatalf("result = %#v, want success", result)
	}
}

func TestJSONRPCRunsOutCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	t.Chdir(tempDir)
	apiJSON := []byte(`{
  "checked": {
    "script": "./main.js",
    "outCheck": "./out_check.js"
  }
}`)
	if err := os.WriteFile(filepath.Join(tempDir, "api.json"), apiJSON, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "main.js"), []byte(`JSON.stringify({ status: 201, body: "created" });`), 0644); err != nil {
		t.Fatal(err)
	}
	outCheckScript := `
if (nyanAllParams.nyan_output.status === 201 && nyanAllParams.nyan_output_body.indexOf("created") >= 0) {
  ({ success: false, status: 409, result: { message: "blocked" } });
} else {
  ({ success: true, status: 200, result: null });
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "out_check.js"), []byte(outCheckScript), 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.POST("/nyan-rpc", handleJSONRPC)

	reqBody := []byte(`{"jsonrpc":"2.0","method":"checked","params":{},"id":1}`)
	req := httptest.NewRequest(http.MethodPost, "/nyan-rpc", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rpcResp); err != nil {
		t.Fatal(err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("unexpected error: %#v; body=%q", rpcResp.Error, rec.Body.String())
	}
	result, ok := rpcResp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result has unexpected type: %T", rpcResp.Result)
	}
	if result["success"] != false {
		t.Fatalf("result = %#v, want outCheck failure", result)
	}
}

func TestParseCronScheduleNext(t *testing.T) {
	schedule, err := parseCronSchedule("*/15 9-10 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}

	after := time.Date(2026, 5, 14, 9, 7, 30, 0, time.Local)
	next := schedule.next(after)
	want := time.Date(2026, 5, 14, 9, 15, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestRegisterDynamicEndpointsSkipsScheduleAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initTestLogger()

	tempDir := t.TempDir()
	apiJSON := []byte(`{
  "scheduled": {
    "type": "schedule",
    "script": "./schedule.js",
    "trigger": {
      "type": "cron",
      "value": "* * * * *"
    }
  }
}`)
	if err := os.WriteFile(filepath.Join(tempDir, "api.json"), apiJSON, 0644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	if err := registerDynamicEndpoints(router, tempDir); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/scheduled", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func assertParamCheckResponse(t *testing.T, body []byte, success bool, status int) {
	t.Helper()

	var response ParamCheckResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("failed to unmarshal response %q: %v", string(body), err)
	}
	if response.Success != success {
		t.Fatalf("success = %v, want %v; body=%q", response.Success, success, string(body))
	}
	if response.Status != status {
		t.Fatalf("status = %d, want %d; body=%q", response.Status, status, string(body))
	}
}

func TestParseAPIHotReloadInterval(t *testing.T) {
	tests := []struct {
		value   string
		want    time.Duration
		wantErr bool
	}{
		{value: "", want: time.Second},
		{value: "250ms", want: 250 * time.Millisecond},
		{value: "later", wantErr: true},
		{value: "0s", wantErr: true},
		{value: "-1s", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseAPIHotReloadInterval(tt.value)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("parseAPIHotReloadInterval(%q) error = nil", tt.value)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("parseAPIHotReloadInterval(%q) = %s, %v; want %s", tt.value, got, err, tt.want)
		}
	}
}

func TestConfigAPIHotReloadDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		data string
		want APIHotReloadConfig
	}{
		{data: `{}`, want: APIHotReloadConfig{Enabled: true, Interval: "1s"}},
		{data: `{"APIHotReload":{"Enabled":false}}`, want: APIHotReloadConfig{Enabled: false, Interval: "1s"}},
		{data: `{"APIHotReload":{"Enabled":true,"Interval":"2s"}}`, want: APIHotReloadConfig{Enabled: true, Interval: "2s"}},
	}
	for _, tt := range tests {
		var got Config
		applyConfigDefaults(&got)
		if err := json.Unmarshal([]byte(tt.data), &got); err != nil {
			t.Fatal(err)
		}
		if got.APIHotReload != tt.want {
			t.Fatalf("APIHotReload = %#v, want %#v", got.APIHotReload, tt.want)
		}
	}
}

func TestReloadAPIFileAppliesChangesAndKeepsLastGoodDefinition(t *testing.T) {
	initTestLogger()
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, apiPath, `{"old":{"description":"active"}}`)
	initial, initialHash, err := readAPIFile(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	setAPIFiles(apiPath, initial)
	t.Cleanup(func() { setAPIFiles("", nil) })

	writeHotReloadTestFile(t, apiPath, `{"new":{"description":"updated"}}`)
	observedHash, reloaded, err := reloadAPIFileIfChanged(apiPath, apiDir, initialHash)
	if err != nil || !reloaded {
		t.Fatalf("reload = %t, err = %v", reloaded, err)
	}
	if observedHash == initialHash {
		t.Fatal("observed hash was not updated")
	}
	if _, exists := currentAPIFiles()["old"]; exists {
		t.Fatal("old API remains after reload")
	}

	writeHotReloadTestFile(t, apiPath, `{"broken":`)
	invalidHash, reloaded, err := reloadAPIFileIfChanged(apiPath, apiDir, observedHash)
	if err == nil || reloaded {
		t.Fatalf("invalid reload = %t, err = %v; want rejected", reloaded, err)
	}
	if _, exists := currentAPIFiles()["new"]; !exists {
		t.Fatal("last good API definition was not retained")
	}
	secondHash, reloaded, err := reloadAPIFileIfChanged(apiPath, apiDir, invalidHash)
	if err != nil || reloaded || secondHash != invalidHash {
		t.Fatalf("unchanged invalid content was processed again: reload=%t err=%v", reloaded, err)
	}
}

func TestReloadAPIFileRejectsInvalidBackgroundConfiguration(t *testing.T) {
	initTestLogger()
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, apiPath, `{"current":{"description":"active"}}`)
	initial, initialHash, err := readAPIFile(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	setAPIFiles(apiPath, initial)
	t.Cleanup(func() { setAPIFiles("", nil) })
	writeHotReloadTestFile(t, apiPath, `{"job":{"type":"schedule","trigger":{"type":"cron","value":"* * * * *"}}}`)
	_, reloaded, err := reloadAPIFileIfChanged(apiPath, apiDir, initialHash)
	if err == nil || reloaded {
		t.Fatalf("invalid background config reload=%t err=%v", reloaded, err)
	}
	if _, exists := currentAPIFiles()["current"]; !exists {
		t.Fatal("current definition changed after rejected candidate")
	}
}

func TestDecodeAPIFileRejectsInvalidTopLevelAndEntries(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "null top level", data: `null`},
		{name: "array top level", data: `[]`},
		{name: "scalar API definition", data: `{"api":"script.js"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeAPIFile([]byte(tt.data)); err == nil {
				t.Fatalf("decodeAPIFile(%s) error = nil, want error", tt.data)
			}
		})
	}
}

func TestReadAPIFileExpandsNestedIncludesAndResolvesDefinitionPaths(t *testing.T) {
	rootDir := t.TempDir()
	childDir := filepath.Join(rootDir, "sub")
	adminDir := filepath.Join(childDir, "admin")
	if err := os.MkdirAll(adminDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(childDir, "api.json")
	adminPath := filepath.Join(adminDir, "api.json")
	writeHotReloadTestFile(t, rootPath, `{
		"health":{"script":"./scripts/health.js"},
		"sub":{"type":"include","path":"./sub/api.json"}
	}`)
	writeHotReloadTestFile(t, childPath, `{
		"getItem":{"script":"./scripts/item.js","paramCheck":"./checks/param.js","outcheck":"./checks/out.js"},
		"assets":{"type":"public","path":"./public"},
		"admin":{"type":"include","path":"./admin/api.json"}
	}`)
	writeHotReloadTestFile(t, adminPath, `{"getUser":{"script":"./scripts/user.js"}}`)

	files, _, err := readAPIFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"health", "sub/getItem", "sub/assets", "sub/admin/getUser"} {
		if _, exists := files[name]; !exists {
			t.Fatalf("expanded API %q is missing: %#v", name, files)
		}
	}
	if _, exists := files["sub"]; exists {
		t.Fatal("include mount was published as an API")
	}
	assertDefinitionPath(t, files, "health", "script", filepath.Join(rootDir, "scripts/health.js"))
	assertDefinitionPath(t, files, "sub/getItem", "script", filepath.Join(childDir, "scripts/item.js"))
	assertDefinitionPath(t, files, "sub/getItem", "paramCheck", filepath.Join(childDir, "checks/param.js"))
	assertDefinitionPath(t, files, "sub/getItem", "outcheck", filepath.Join(childDir, "checks/out.js"))
	assertDefinitionPath(t, files, "sub/assets", "path", filepath.Join(childDir, "public"))
	assertDefinitionPath(t, files, "sub/admin/getUser", "script", filepath.Join(adminDir, "scripts/user.js"))
}

func TestIncludedAPICallFormsUseCompleteName(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, filepath.Join(rootDir, "target.js"), `JSON.stringify({status:200,value:nyanAllParams.api,called:nyanAllParams.api});`)
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"target":{"script":"./target.js","description":"included target"}}`)
	loaded, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(loaded.Snapshot)
	servicePaths.API.Path = rootPath
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		servicePaths = serviceFilePaths{}
	})
	router := gin.New()
	router.POST("/nyan-rpc", handleJSONRPC)
	router.GET("/nyan", handleNyan)
	router.NoRoute(func(c *gin.Context) {
		if !dispatchDynamicEndpoint(c, rootDir) {
			c.Status(http.StatusNotFound)
		}
	})
	if err := registerDynamicEndpoints(router, rootDir); err != nil {
		t.Fatal(err)
	}
	assertDynamicAPIValue(t, router, "/sub/target", http.StatusOK, "sub/target")
	assertDynamicAPIValue(t, router, "/api/sub/target", http.StatusOK, "sub/target")

	rpcBody := strings.NewReader(`{"jsonrpc":"2.0","method":"sub/target","params":{},"id":1}`)
	rpcResponse := httptest.NewRecorder()
	router.ServeHTTP(rpcResponse, httptest.NewRequest(http.MethodPost, "/nyan-rpc", rpcBody))
	if rpcResponse.Code != http.StatusOK || !strings.Contains(rpcResponse.Body.String(), `"called":"sub/target"`) {
		t.Fatalf("included JSON-RPC status=%d body=%q", rpcResponse.Code, rpcResponse.Body.String())
	}
	nyanResponse := httptest.NewRecorder()
	router.ServeHTTP(nyanResponse, httptest.NewRequest(http.MethodGet, "/nyan", nil))
	var listed NyanResponse
	if err := json.Unmarshal(nyanResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if _, exists := listed.Apis["sub/target"]; !exists {
		t.Fatalf("included API missing from /nyan: %#v", listed.Apis)
	}
	if _, exists := listed.Apis["sub"]; exists {
		t.Fatal("include mount was listed by /nyan")
	}
}

func TestReadAPIFileAllowsSamePhysicalFileUnderDistinctMounts(t *testing.T) {
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	sharedPath := filepath.Join(rootDir, "shared.json")
	writeHotReloadTestFile(t, rootPath, `{
		"left":{"type":"include","path":"./shared.json"},
		"right":{"type":"include","path":"./shared.json"}
	}`)
	writeHotReloadTestFile(t, sharedPath, `{"value":{"script":"./value.js"}}`)

	files, _, err := readAPIFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"left/value", "right/value"} {
		if _, exists := files[name]; !exists {
			t.Fatalf("expanded API %q is missing: %#v", name, files)
		}
	}
}

func TestIncludedBackgroundDefinitionsUseExpandedNamesAndSourcePaths(t *testing.T) {
	rootDir := t.TempDir()
	childDir := filepath.Join(rootDir, "workers")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(rootDir, "api.json")
	writeHotReloadTestFile(t, rootPath, `{"workers":{"type":"include","path":"./workers/api.json"}}`)
	writeHotReloadTestFile(t, filepath.Join(childDir, "api.json"), `{
		"job":{"type":"schedule","script":"./job.js","trigger":{"type":"cron","value":"* * * * *"}},
		"client":{"type":"ws_client","script":"./client.js","connectURL":"ws://localhost:9999"}
	}`)

	files, _, err := readAPIFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := buildScheduleJobConfigs(files, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := schedules["workers/job"].scriptPath; got != filepath.Join(childDir, "job.js") {
		t.Fatalf("schedule script path = %q", got)
	}
	clients, err := buildWSClientConfigs(files, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := clients["workers/client"].scriptPath; got != filepath.Join(childDir, "client.js") {
		t.Fatalf("WebSocket client script path = %q", got)
	}
}

func TestAPIConfigSnapshotContainsOneCompleteGeneration(t *testing.T) {
	rootDir := t.TempDir()
	childDir := filepath.Join(rootDir, "workers")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(childDir, "api.json")
	writeHotReloadTestFile(t, rootPath, `{
		"root":{"script":"./root.js"},
		"workers":{"type":"include","path":"./workers/api.json"}
	}`)
	writeHotReloadTestFile(t, childPath, `{
		"job":{"type":"schedule","script":"./job.js","description":"generation-one","trigger":{"type":"cron","value":"* * * * *"}},
		"client":{"type":"ws_client","script":"./client.js","description":"generation-one","connectURL":"ws://localhost:9999"}
	}`)

	loaded, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := loaded.Snapshot
	if snapshot.RootPath != rootPath {
		t.Fatalf("root path = %q, want %q", snapshot.RootPath, rootPath)
	}
	if snapshot.Sources["root"] != rootPath || snapshot.Sources["workers/job"] != childPath || snapshot.Sources["workers/client"] != childPath {
		t.Fatalf("snapshot sources = %#v", snapshot.Sources)
	}
	if len(snapshot.FileStates) != 2 {
		t.Fatalf("snapshot file states = %#v, want root and child", snapshot.FileStates)
	}
	if snapshot.Schedules["workers/job"].description != "generation-one" {
		t.Fatalf("snapshot schedules = %#v", snapshot.Schedules)
	}
	if snapshot.WSClients["workers/client"].description != "generation-one" {
		t.Fatalf("snapshot WebSocket clients = %#v", snapshot.WSClients)
	}
	if _, exists := snapshot.Definitions["workers"]; exists {
		t.Fatal("include mount exists in published definitions")
	}
}

func TestAPIConfigSnapshotOwnsClonedConfiguration(t *testing.T) {
	definitions := map[string]interface{}{
		"api": map[string]interface{}{
			"description": "original",
			"nested":      []interface{}{map[string]interface{}{"value": "original"}},
		},
	}
	sources := map[string]string{"api": "/tmp/original.json"}
	fileStates := map[string]APIFileState{"/tmp/api.json": {Path: "/tmp/api.json", Exists: true}}
	schedules := map[string]scheduleJobConfig{"job": {name: "job", description: "original", schedule: cronSchedule{minutes: cronField{1: true}}}}
	clients := map[string]wsClientConfig{"client": {name: "client", description: "original"}}
	snapshot := newAPIConfigSnapshot("/tmp/api.json", definitions, sources, fileStates, schedules, clients)

	definitions["api"].(map[string]interface{})["description"] = "mutated"
	definitions["api"].(map[string]interface{})["nested"].([]interface{})[0].(map[string]interface{})["value"] = "mutated"
	sources["api"] = "/tmp/mutated.json"
	fileStates["/tmp/api.json"] = APIFileState{Path: "/tmp/mutated.json"}
	schedule := schedules["job"]
	schedule.description = "mutated"
	schedule.schedule.minutes[1] = false
	schedules["job"] = schedule
	client := clients["client"]
	client.description = "mutated"
	clients["client"] = client

	definition := snapshot.Definitions["api"].(map[string]interface{})
	if definition["description"] != "original" || definition["nested"].([]interface{})[0].(map[string]interface{})["value"] != "original" {
		t.Fatalf("snapshot definitions were mutated: %#v", definition)
	}
	if snapshot.Sources["api"] != "/tmp/original.json" || snapshot.FileStates["/tmp/api.json"].Path != "/tmp/api.json" || snapshot.Schedules["job"].description != "original" || !snapshot.Schedules["job"].schedule.minutes[1] || snapshot.WSClients["client"].description != "original" {
		t.Fatal("snapshot metadata or runtime configuration was mutated through its input")
	}
}

func TestRootReloadPublishesSourceOnlySnapshotChange(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"child/same":{"description":"unchanged"}}`)
	initial, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(initial.Snapshot)
	oldManager := backgroundRuntimes
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		backgroundRuntimes = oldManager
	})
	writeHotReloadTestFile(t, childPath, `{"same":{"description":"unchanged"}}`)
	writeHotReloadTestFile(t, rootPath, `{"child":{"type":"include","path":"./child.json"}}`)

	_, reloaded, err := reloadAPIFileIfChanged(rootPath, rootDir, initial.Hash)
	if err != nil || !reloaded {
		t.Fatalf("source-only reload=%t err=%v", reloaded, err)
	}
	snapshot := currentAPISnapshot()
	if snapshot.Sources["child/same"] != childPath {
		t.Fatalf("published sources = %#v", snapshot.Sources)
	}
}

func TestAPISnapshotConcurrentReadersNeverObserveMixedGeneration(t *testing.T) {
	makeSnapshot := func(generation string) *APIConfigSnapshot {
		return newAPIConfigSnapshot("/tmp/api.json",
			map[string]interface{}{"marker": map[string]interface{}{"description": generation}},
			map[string]string{"marker": generation},
			map[string]APIFileState{"marker": {Path: generation, Exists: true}},
			map[string]scheduleJobConfig{"marker": {name: "marker", description: generation}},
			map[string]wsClientConfig{"marker": {name: "marker", description: generation}},
		)
	}
	first := makeSnapshot("first")
	second := makeSnapshot("second")
	publishAPISnapshot(first)
	t.Cleanup(func() { publishAPISnapshot(nil) })

	var wait sync.WaitGroup
	errors := make(chan string, 8)
	for reader := 0; reader < 8; reader++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 2000; index++ {
				snapshot := currentAPISnapshot()
				definitionGeneration := snapshot.Definitions["marker"].(map[string]interface{})["description"].(string)
				if snapshot.Sources["marker"] != definitionGeneration || snapshot.FileStates["marker"].Path != definitionGeneration || snapshot.Schedules["marker"].description != definitionGeneration || snapshot.WSClients["marker"].description != definitionGeneration {
					errors <- fmt.Sprintf("mixed snapshot: %#v", snapshot)
					return
				}
			}
		}()
	}
	for index := 0; index < 2000; index++ {
		if index%2 == 0 {
			publishAPISnapshot(second)
		} else {
			publishAPISnapshot(first)
		}
	}
	wait.Wait()
	close(errors)
	for message := range errors {
		t.Fatal(message)
	}
}

func TestJavaScriptNyanCallMeKeepsCapturedSnapshotGeneration(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	oldTarget := filepath.Join(rootDir, "old.js")
	newTarget := filepath.Join(rootDir, "new.js")
	caller := filepath.Join(rootDir, "caller.js")
	writeHotReloadTestFile(t, oldTarget, `JSON.stringify({status:200,generation:"old"});`)
	writeHotReloadTestFile(t, newTarget, `JSON.stringify({status:200,generation:"new"});`)
	writeHotReloadTestFile(t, caller, `JSON.stringify(nyanCallMe({api:"target"}));`)
	captured := newAPIConfigSnapshot(filepath.Join(rootDir, "api.json"), map[string]interface{}{
		"target": map[string]interface{}{"script": oldTarget},
	}, nil, nil, nil, nil)
	publishAPISnapshot(newAPIConfigSnapshot(filepath.Join(rootDir, "api.json"), map[string]interface{}{
		"target": map[string]interface{}{"script": newTarget},
	}, nil, nil, nil, nil))
	t.Cleanup(func() { publishAPISnapshot(nil) })

	result, err := runJavaScriptWithSnapshot(captured, caller, map[string]interface{}{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONValue([]byte(result), "generation", "old") {
		t.Fatalf("nested API result=%q, want captured generation", result)
	}
}

func TestIncludedCompleteAPINameIsPreservedForNyanCallMeAndPush(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	targetScript := filepath.Join(rootDir, "target.js")
	callerScript := filepath.Join(rootDir, "caller.js")
	writeHotReloadTestFile(t, targetScript, `JSON.stringify({status:200,called:nyanAllParams.api});`)
	writeHotReloadTestFile(t, callerScript, `JSON.stringify(nyanCallMe({api:"sub/target"}));`)
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{
		"target":{"script":"./target.js"},
		"caller":{"script":"./caller.js"},
		"emitter":{"script":"./target.js","push":"sub/target"}
	}`)
	loaded, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	emitter := loaded.Snapshot.Definitions["sub/emitter"].(map[string]interface{})
	if emitter["push"] != "sub/target" {
		t.Fatalf("included push target=%v", emitter["push"])
	}
	result, err := callNyanAPIFromVMWithSnapshot(loaded.Snapshot, "sub/caller", map[string]interface{}{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONValue([]byte(result), "called", "sub/target") {
		t.Fatalf("included nyanCallMe result=%q", result)
	}
}

func TestParseStaticJavaScriptValueConvertsJSONCompatibleLiterals(t *testing.T) {
	source := `{
  $schema: "https://json-schema.org/draft/2020-12/schema",
  type: "object",
  properties: {
    id: {type: "integer", minimum: -10},
    ratio: {type: "number", examples: [1.5, +2]},
    enabled: {type: "boolean", default: false},
    note: {default: null},
    names: {type: "array", items: {type: "string"}}
  },
  required: ["id"],
  additionalProperties: false
}`

	got, err := parseStaticJavaScriptValue("schema.js", source)
	if err != nil {
		t.Fatalf("parseStaticJavaScriptValue() error = %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var normalizedGot interface{}
	if err := json.Unmarshal(encoded, &normalizedGot); err != nil {
		t.Fatal(err)
	}
	var want interface{}
	if err := json.Unmarshal([]byte(`{
      "$schema":"https://json-schema.org/draft/2020-12/schema",
      "type":"object",
      "properties":{
        "id":{"type":"integer","minimum":-10},
        "ratio":{"type":"number","examples":[1.5,2]},
        "enabled":{"type":"boolean","default":false},
        "note":{"default":null},
        "names":{"type":"array","items":{"type":"string"}}
      },
      "required":["id"],
      "additionalProperties":false
    }`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalizedGot, want) {
		t.Fatalf("static value = %#v, want %#v", normalizedGot, want)
	}
}

func TestParseStaticJavaScriptValueRejectsDynamicAndNonJSONValues(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "function call", source: `{value: createSchema()}`, want: `$.value: function calls are not supported`},
		{name: "identifier reference", source: `{type: schemaType}`, want: `$.type: identifier references are not supported`},
		{name: "object spread", source: `{...commonSchema}`, want: `spread properties are not supported`},
		{name: "array spread", source: `[...values]`, want: `$[0]: spread elements are not supported`},
		{name: "conditional", source: `condition ? {} : []`, want: `conditional expressions are not supported`},
		{name: "computed property", source: `{[key]: 1}`, want: `computed property names are not supported`},
		{name: "shorthand property", source: `{id}`, want: `shorthand properties are not supported`},
		{name: "getter", source: `{get id() { return 1; }}`, want: `property kind "get" is not supported`},
		{name: "template literal", source: "`object`", want: `template literals are not supported`},
		{name: "array hole", source: `[1,,2]`, want: `$[1]: array holes are not supported`},
		{name: "bigint", source: `1n`, want: `parse static JavaScript value`},
		{name: "infinity", source: `1e400`, want: `non-finite numbers are not JSON-compatible`},
		{name: "duplicate property", source: `{id: 1, id: 2}`, want: `duplicate property "id"`},
		{name: "numeric property", source: `{1: "value"}`, want: `property names must be strings`},
		{name: "unsupported unary", source: `!true`, want: `unary operator "!" is not supported`},
		{name: "unary identifier", source: `-value`, want: `unary "-" requires a numeric literal`},
		{name: "computed expression", source: `1 + 2`, want: `computed expressions are not supported`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseStaticJavaScriptValue("schema.js", tt.source)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseStaticJavaScriptValue() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseStaticJavaScriptValueReportsParserErrors(t *testing.T) {
	_, err := parseStaticJavaScriptValue("broken-schema.js", `{type: }`)
	if err == nil {
		t.Fatal("parseStaticJavaScriptValue() error = nil, want parser error")
	}
	if !strings.Contains(err.Error(), "broken-schema.js") {
		t.Fatalf("parseStaticJavaScriptValue() error = %v, want filename", err)
	}
}

func TestExtractStaticJavaScriptObjectConstantReadsTopLevelConst(t *testing.T) {
	source := []byte(`
const helper = "unchanged";
const nyanInputSchema = {
  type: "object",
  properties: {
    id: {type: "integer", minimum: -1}
  },
  required: ["id"],
  additionalProperties: false
};

function checkInput() {
  return nyanAllParams.id !== undefined;
}
`)

	got, found, err := extractStaticJavaScriptObjectConstant("param-check.js", source, "nyanInputSchema")
	if err != nil {
		t.Fatalf("extractStaticJavaScriptObjectConstant() error = %v", err)
	}
	if !found {
		t.Fatal("extractStaticJavaScriptObjectConstant() found = false, want true")
	}
	if got["type"] != "object" || got["additionalProperties"] != false {
		t.Fatalf("schema = %#v", got)
	}
	if _, exists := got["$schema"]; exists {
		t.Fatal("$schema was added to a schema that omitted it")
	}
	properties, ok := got["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties = %#v", got["properties"])
	}
	id, ok := properties["id"].(map[string]interface{})
	if !ok || id["type"] != "integer" || id["minimum"] != int64(-1) {
		t.Fatalf("id schema = %#v", properties["id"])
	}
}

func TestReadStaticJavaScriptObjectConstantPreservesSchemaKeyword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out-check.js")
	writeHotReloadTestFile(t, path, `
const ignored = null, nyanOutputSchema = {
  $schema: "https://json-schema.org/draft/2020-12/schema",
  type: "object",
  properties: {
    success: {const: true},
    status: {type: "integer"}
  },
  required: ["success", "status"]
};
`)

	got, found, err := readStaticJavaScriptObjectConstant(path, "nyanOutputSchema")
	if err != nil {
		t.Fatalf("readStaticJavaScriptObjectConstant() error = %v", err)
	}
	if !found {
		t.Fatal("readStaticJavaScriptObjectConstant() found = false, want true")
	}
	if got["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %#v", got["$schema"])
	}
	properties := got["properties"].(map[string]interface{})
	if _, exists := properties["result"]; exists {
		t.Fatal("Nyan8 added a result property to the explicit output schema")
	}
}

func TestExtractStaticJavaScriptObjectConstantReturnsNotFound(t *testing.T) {
	source := []byte(`
const nyanInputSchemaExample = {type: "object"};
function makeCheck() {
  const nyanInputSchema = {type: "array"};
  return nyanInputSchema;
}
`)

	got, found, err := extractStaticJavaScriptObjectConstant("without-schema.js", source, "nyanInputSchema")
	if err != nil {
		t.Fatalf("extractStaticJavaScriptObjectConstant() error = %v", err)
	}
	if found || got != nil {
		t.Fatalf("schema = %#v, found=%t; want nil, false", got, found)
	}
}

func TestExtractStaticJavaScriptObjectConstantRejectsInvalidDeclarations(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "let declaration", source: `let nyanInputSchema = {};`, want: `must be declared with const`},
		{name: "var declaration", source: `var nyanInputSchema = {};`, want: `must be declared with const`},
		{name: "array value", source: `const nyanInputSchema = [];`, want: `must be a static object literal`},
		{name: "null value", source: `const nyanInputSchema = null;`, want: `must be a static object literal`},
		{name: "function call", source: `const nyanInputSchema = createSchema();`, want: `function calls are not supported`},
		{name: "identifier reference", source: `const schema = {}; const nyanInputSchema = schema;`, want: `identifier references are not supported`},
		{name: "spread", source: `const nyanInputSchema = {...commonSchema};`, want: `spread properties are not supported`},
		{name: "duplicate", source: `const nyanInputSchema = {}; const nyanInputSchema = {};`, want: `nyanInputSchema`},
		{name: "syntax error", source: `const nyanInputSchema = {type: };`, want: `invalid-schema.js`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := extractStaticJavaScriptObjectConstant("invalid-schema.js", []byte(tt.source), "nyanInputSchema")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("extractStaticJavaScriptObjectConstant() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestExtractStaticJavaScriptObjectConstantValidatesArgumentsAndReadErrors(t *testing.T) {
	if _, _, err := extractStaticJavaScriptObjectConstant("schema.js", []byte(`const value = {};`), " "); err == nil {
		t.Fatal("empty constant name error = nil")
	}
	missingPath := filepath.Join(t.TempDir(), "missing.js")
	if _, _, err := readStaticJavaScriptObjectConstant(missingPath, "nyanInputSchema"); err == nil || !strings.Contains(err.Error(), missingPath) {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestResolveAPISchemaAppliesPriorityAndSources(t *testing.T) {
	dir := t.TempDir()
	paramCheck := filepath.Join(dir, "param-check.js")
	outCheck := filepath.Join(dir, "out-check.js")
	legacyScript := filepath.Join(dir, "legacy.js")
	writeHotReloadTestFile(t, paramCheck, `
const nyanInputSchema = {
  $schema: "https://json-schema.org/draft/2020-12/schema",
  type: "object",
  properties: {explicit_id: {type: "integer"}},
  required: ["explicit_id"]
};
`)
	writeHotReloadTestFile(t, outCheck, `
const nyanOutputSchema = {
  type: "object",
  properties: {
    status: {type: "integer"},
    payload: {type: "string"}
  },
  required: ["status", "payload"]
};
`)
	writeHotReloadTestFile(t, legacyScript, `
const nyanAcceptedParams = {legacy_id:1,price:1.5,enabled:true,tags:["a","b"],nested:{name:"cat"}};
`)

	explicit, err := resolveAPISchema(map[string]interface{}{
		"paramCheck": paramCheck,
		"outCheck":   outCheck,
		"script":     legacyScript,
	})
	if err != nil {
		t.Fatalf("resolveAPISchema(explicit) error = %v", err)
	}
	if explicit.InputSource != schemaSourceParamCheck || explicit.OutputSource != schemaSourceOutCheck {
		t.Fatalf("explicit sources = input:%q output:%q", explicit.InputSource, explicit.OutputSource)
	}
	if explicit.Input["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("explicit input schema = %#v", explicit.Input)
	}
	inputProperties := explicit.Input["properties"].(map[string]interface{})
	if _, exists := inputProperties["legacy_id"]; exists {
		t.Fatalf("legacy input unexpectedly replaced explicit schema: %#v", explicit.Input)
	}
	outputProperties := explicit.Output["properties"].(map[string]interface{})
	if _, exists := outputProperties["success"]; exists {
		t.Fatalf("success was added to explicit output schema: %#v", explicit.Output)
	}
	if _, exists := outputProperties["result"]; exists {
		t.Fatalf("result was added to explicit output schema: %#v", explicit.Output)
	}

	legacy, err := resolveAPISchema(map[string]interface{}{"script": legacyScript})
	if err != nil {
		t.Fatalf("resolveAPISchema(legacy) error = %v", err)
	}
	if legacy.InputSource != schemaSourceScriptLegacy || legacy.OutputSource != schemaSourceUnknown {
		t.Fatalf("legacy sources = input:%q output:%q", legacy.InputSource, legacy.OutputSource)
	}
	legacyProperties := legacy.Input["properties"].(map[string]interface{})
	if legacyProperties["legacy_id"].(map[string]interface{})["type"] != "integer" || legacyProperties["price"].(map[string]interface{})["type"] != "number" {
		t.Fatalf("legacy input properties = %#v", legacyProperties)
	}
	if legacyProperties["nested"].(map[string]interface{})["type"] != "object" {
		t.Fatalf("nested legacy input = %#v", legacyProperties["nested"])
	}
	if _, exists := legacy.Input["required"]; exists {
		t.Fatalf("legacy input must not infer required: %#v", legacy.Input)
	}

	unknown, err := resolveAPISchema(map[string]interface{}{})
	if err != nil {
		t.Fatalf("resolveAPISchema(unknown) error = %v", err)
	}
	if unknown.InputSource != schemaSourceUnknown || unknown.OutputSource != schemaSourceUnknown || len(unknown.Input) != 0 || len(unknown.Output) != 0 {
		t.Fatalf("unknown schema = %#v", unknown)
	}
}

func TestResolveAPISchemaSupportsCheckPathAliases(t *testing.T) {
	dir := t.TempDir()
	paramCheck := filepath.Join(dir, "param-check.js")
	outCheck := filepath.Join(dir, "out-check.js")
	writeHotReloadTestFile(t, paramCheck, `const nyanInputSchema = {type:"object"};`)
	writeHotReloadTestFile(t, outCheck, `const nyanOutputSchema = {type:"array"};`)

	for _, inputKey := range []string{"paramcheck", "check"} {
		t.Run(inputKey, func(t *testing.T) {
			resolved, err := resolveAPISchema(map[string]interface{}{
				inputKey:   paramCheck,
				"outcheck": outCheck,
			})
			if err != nil {
				t.Fatalf("resolveAPISchema() error = %v", err)
			}
			if resolved.InputSource != schemaSourceParamCheck || resolved.OutputSource != schemaSourceOutCheck {
				t.Fatalf("sources = input:%q output:%q", resolved.InputSource, resolved.OutputSource)
			}
		})
	}
}

func TestResolveAPISchemaRejectsInvalidExplicitSchemaWithoutLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	paramCheck := filepath.Join(dir, "param-check.js")
	legacyScript := filepath.Join(dir, "legacy.js")
	writeHotReloadTestFile(t, paramCheck, `const nyanInputSchema = createSchema();`)
	writeHotReloadTestFile(t, legacyScript, `const nyanAcceptedParams = {id: 1};`)

	_, err := resolveAPISchema(map[string]interface{}{
		"paramCheck": paramCheck,
		"script":     legacyScript,
	})
	if err == nil || !strings.Contains(err.Error(), "input schema from paramCheck") || !strings.Contains(err.Error(), "function calls") {
		t.Fatalf("resolveAPISchema() error = %v", err)
	}
}

func TestResolveAPISchemaIgnoresMissingOptionalSchemaFiles(t *testing.T) {
	dir := t.TempDir()
	legacyScript := filepath.Join(dir, "legacy.js")
	writeHotReloadTestFile(t, legacyScript, `const nyanAcceptedParams = {id: 1};`)

	resolved, err := resolveAPISchema(map[string]interface{}{
		"paramCheck": filepath.Join(dir, "missing-param-check.js"),
		"outCheck":   filepath.Join(dir, "missing-out-check.js"),
		"script":     legacyScript,
	})
	if err != nil {
		t.Fatalf("resolveAPISchema() error = %v", err)
	}
	if resolved.InputSource != schemaSourceScriptLegacy || resolved.OutputSource != schemaSourceUnknown {
		t.Fatalf("sources = input:%q output:%q", resolved.InputSource, resolved.OutputSource)
	}
}

func TestResolveAPISchemaIgnoresInvalidLegacyAcceptedParams(t *testing.T) {
	dir := t.TempDir()
	legacyScript := filepath.Join(dir, "legacy.js")
	writeHotReloadTestFile(t, legacyScript, `const nyanAcceptedParams = buildAcceptedParams();`)

	resolved, err := resolveAPISchema(map[string]interface{}{"script": legacyScript})
	if err != nil {
		t.Fatalf("resolveAPISchema() error = %v", err)
	}
	if resolved.InputSource != schemaSourceUnknown || len(resolved.Input) != 0 {
		t.Fatalf("input schema = %#v, source = %q", resolved.Input, resolved.InputSource)
	}
}

func TestReadStaticLegacyAcceptedParams(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.js")
	writeHotReloadTestFile(t, validPath, `const nyanAcceptedParams = {id: 1, names: ["mike", "tama"]};`)
	params, found, err := readStaticLegacyAcceptedParams(validPath)
	if err != nil || !found {
		t.Fatalf("readStaticLegacyAcceptedParams() params=%#v found=%t error=%v", params, found, err)
	}
	if params["id"] != int64(1) || !reflect.DeepEqual(params["names"], []interface{}{"mike", "tama"}) {
		t.Fatalf("params = %#v", params)
	}

	missingPath := filepath.Join(dir, "missing-declaration.js")
	writeHotReloadTestFile(t, missingPath, `const somethingElse = {};`)
	params, found, err = readStaticLegacyAcceptedParams(missingPath)
	if err != nil || found || len(params) != 0 {
		t.Fatalf("missing declaration params=%#v found=%t error=%v", params, found, err)
	}

	invalidPath := filepath.Join(dir, "invalid.js")
	writeHotReloadTestFile(t, invalidPath, `const nyanAcceptedParams = [1, 2];`)
	if _, _, err := readStaticLegacyAcceptedParams(invalidPath); err == nil || !strings.Contains(err.Error(), "static object literal") {
		t.Fatalf("invalid declaration error = %v", err)
	}
}

func TestLegacyValueSchemaFallsBackSafelyForMixedArrays(t *testing.T) {
	schema := legacyInputSchema(map[string]interface{}{
		"nested":  map[string]interface{}{"name": "cat"},
		"mixed":   []interface{}{float64(1), "two"},
		"empty":   []interface{}{},
		"unknown": nil,
	})
	properties := schema["properties"].(map[string]interface{})
	nested := properties["nested"].(map[string]interface{})
	if nested["type"] != "object" {
		t.Fatalf("nested schema = %#v", nested)
	}
	mixed := properties["mixed"].(map[string]interface{})
	if mixed["type"] != "array" || !reflect.DeepEqual(mixed["items"], map[string]interface{}{}) {
		t.Fatalf("mixed schema = %#v", mixed)
	}
	empty := properties["empty"].(map[string]interface{})
	if empty["type"] != "array" || !reflect.DeepEqual(empty["items"], map[string]interface{}{}) {
		t.Fatalf("empty schema = %#v", empty)
	}
	if got := properties["unknown"]; !reflect.DeepEqual(got, map[string]interface{}{}) {
		t.Fatalf("unknown value schema = %#v", got)
	}
}

func TestHandleNyanDetailPublishesExplicitSchemasForNestedAPI(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	paramCheck := filepath.Join(dir, "param-check.js")
	outCheck := filepath.Join(dir, "out-check.js")
	writeHotReloadTestFile(t, paramCheck, `
const nyanInputSchema = {
  $schema: "https://json-schema.org/draft/2020-12/schema",
  type: "object",
  properties: {id: {type: "integer"}},
  required: ["id"]
};
`)
	writeHotReloadTestFile(t, outCheck, `
const nyanOutputSchema = {
  type: "object",
  properties: {status: {const: 200}, payload: {type: "string"}},
  required: ["status", "payload"]
};
`)
	setAPIFiles(filepath.Join(dir, "api.json"), map[string]interface{}{
		"sub/items/get": map[string]interface{}{
			"paramCheck":  paramCheck,
			"outCheck":    outCheck,
			"description": "nested API",
		},
	})
	t.Cleanup(func() { setAPIFiles("", nil) })

	router := gin.New()
	router.GET("/nyan", handleNyan)
	router.GET("/nyan/*apiName", handleNyanDetail)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/nyan/sub/items/get", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["api"] != "sub/items/get" || response["type"] != apiTypeAPI || response["description"] != "nested API" {
		t.Fatalf("detail response = %#v", response)
	}
	source := response["schemaSource"].(map[string]interface{})
	if source["input"] != schemaSourceParamCheck || source["output"] != schemaSourceOutCheck {
		t.Fatalf("schemaSource = %#v", source)
	}
	input := response["inputSchema"].(map[string]interface{})
	if input["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("inputSchema = %#v", input)
	}
	output := response["outputSchema"].(map[string]interface{})
	properties := output["properties"].(map[string]interface{})
	if _, exists := properties["success"]; exists {
		t.Fatalf("success was added to outputSchema: %#v", output)
	}
	if _, exists := properties["result"]; exists {
		t.Fatalf("result was added to outputSchema: %#v", output)
	}
	if _, exists := response["nyanAcceptedParams"]; exists {
		t.Fatalf("nyanAcceptedParams must be omitted for an explicit input schema: %#v", response)
	}
	if _, exists := response["nyanOutputColumns"]; exists {
		t.Fatalf("nyanOutputColumns must be removed: %#v", response)
	}
}

func TestHandleNyanDetailResolvesSchemaFromMultiStageInclude(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "api.json")
	childDir := filepath.Join(dir, "sub")
	adminDir := filepath.Join(childDir, "admin")
	if err := os.MkdirAll(adminDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./sub/api.json"}}`)
	writeHotReloadTestFile(t, filepath.Join(childDir, "api.json"), `{"admin":{"type":"include","path":"./admin/api.json"}}`)
	writeHotReloadTestFile(t, filepath.Join(adminDir, "api.json"), `{
  "getItem": {
    "paramCheck": "./check.js",
    "description": "included schema"
  }
}`)
	writeHotReloadTestFile(t, filepath.Join(adminDir, "check.js"), `
const nyanInputSchema = {type:"object", properties:{id:{type:"integer"}}, required:["id"]};
`)
	loaded, err := readAPIConfigFile(rootPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(loaded.Snapshot)
	t.Cleanup(func() { publishAPISnapshot(nil) })

	router := gin.New()
	router.GET("/nyan/*apiName", handleNyanDetail)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/nyan/sub/admin/getItem", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["api"] != "sub/admin/getItem" {
		t.Fatalf("api = %#v", response["api"])
	}
	source := response["schemaSource"].(map[string]interface{})
	if source["input"] != schemaSourceParamCheck {
		t.Fatalf("schemaSource = %#v", source)
	}
	properties := response["inputSchema"].(map[string]interface{})["properties"].(map[string]interface{})
	if properties["id"].(map[string]interface{})["type"] != "integer" {
		t.Fatalf("inputSchema properties = %#v", properties)
	}
}

func TestHandleNyanDetailPublishesLegacyAndUnknownSchemas(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	legacyScript := filepath.Join(dir, "legacy.js")
	writeHotReloadTestFile(t, legacyScript, `
const nyanAcceptedParams = {id: 1, name: "cat"};
const nyanOutputColumns = ["obsolete"];
JSON.stringify({status: 200});
`)
	setAPIFiles(filepath.Join(dir, "api.json"), map[string]interface{}{
		"legacy":  map[string]interface{}{"script": legacyScript, "description": "legacy"},
		"unknown": map[string]interface{}{"description": "unknown"},
	})
	t.Cleanup(func() { setAPIFiles("", nil) })

	router := gin.New()
	router.GET("/nyan/*apiName", handleNyanDetail)

	legacyRecorder := httptest.NewRecorder()
	router.ServeHTTP(legacyRecorder, httptest.NewRequest(http.MethodGet, "/nyan/legacy", nil))
	if legacyRecorder.Code != http.StatusOK {
		t.Fatalf("legacy status = %d; body=%s", legacyRecorder.Code, legacyRecorder.Body.String())
	}
	var legacy map[string]interface{}
	if err := json.Unmarshal(legacyRecorder.Body.Bytes(), &legacy); err != nil {
		t.Fatal(err)
	}
	legacySource := legacy["schemaSource"].(map[string]interface{})
	if legacySource["input"] != schemaSourceScriptLegacy || legacySource["output"] != schemaSourceUnknown {
		t.Fatalf("legacy schemaSource = %#v", legacySource)
	}
	if legacy["nyanAcceptedParams"].(map[string]interface{})["name"] != "cat" {
		t.Fatalf("nyanAcceptedParams = %#v", legacy["nyanAcceptedParams"])
	}
	if _, exists := legacy["nyanOutputColumns"]; exists {
		t.Fatalf("nyanOutputColumns must be removed: %#v", legacy)
	}

	unknownRecorder := httptest.NewRecorder()
	router.ServeHTTP(unknownRecorder, httptest.NewRequest(http.MethodGet, "/nyan/unknown", nil))
	if unknownRecorder.Code != http.StatusOK {
		t.Fatalf("unknown status = %d; body=%s", unknownRecorder.Code, unknownRecorder.Body.String())
	}
	var unknown map[string]interface{}
	if err := json.Unmarshal(unknownRecorder.Body.Bytes(), &unknown); err != nil {
		t.Fatal(err)
	}
	unknownSource := unknown["schemaSource"].(map[string]interface{})
	if unknownSource["input"] != schemaSourceUnknown || unknownSource["output"] != schemaSourceUnknown {
		t.Fatalf("unknown schemaSource = %#v", unknownSource)
	}
	if len(unknown["inputSchema"].(map[string]interface{})) != 0 || len(unknown["outputSchema"].(map[string]interface{})) != 0 {
		t.Fatalf("unknown schemas = input:%#v output:%#v", unknown["inputSchema"], unknown["outputSchema"])
	}
	if _, exists := unknown["nyanAcceptedParams"]; exists {
		t.Fatalf("unknown nyanAcceptedParams must be omitted: %#v", unknown)
	}
}

func TestHandleNyanDetailReloadsSchemaOnEveryRequest(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	checkPath := filepath.Join(dir, "check.js")
	writeHotReloadTestFile(t, checkPath, `const nyanInputSchema = {type:"object", properties:{id:{type:"integer"}}};`)
	setAPIFiles(filepath.Join(dir, "api.json"), map[string]interface{}{
		"item": map[string]interface{}{"paramCheck": checkPath},
	})
	t.Cleanup(func() { setAPIFiles("", nil) })

	router := gin.New()
	router.GET("/nyan/*apiName", handleNyanDetail)
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/nyan/item", nil))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"id"`) {
		t.Fatalf("first detail = status %d body %s", first.Code, first.Body.String())
	}

	writeHotReloadTestFile(t, checkPath, `const nyanInputSchema = {type:"object", properties:{name:{type:"string"}}};`)
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/nyan/item", nil))
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"name"`) || strings.Contains(second.Body.String(), `"id"`) {
		t.Fatalf("second detail = status %d body %s", second.Code, second.Body.String())
	}
}

func TestHandleNyanDetailReturnsSchemaErrorsAtRequestTime(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	checkPath := filepath.Join(dir, "check.js")
	writeHotReloadTestFile(t, checkPath, `const nyanInputSchema = createSchema();`)
	setAPIFiles(filepath.Join(dir, "api.json"), map[string]interface{}{
		"item": map[string]interface{}{"paramCheck": checkPath},
	})
	t.Cleanup(func() { setAPIFiles("", nil) })

	router := gin.New()
	router.GET("/nyan/*apiName", handleNyanDetail)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/nyan/item", nil))
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "function calls are not supported") {
		t.Fatalf("detail = status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleNyanListsOnlyNormalAPIsAndTrailingSlashUsesList(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	originalConfig := globalConfig
	globalConfig.Name = "Nyan8 test"
	globalConfig.Profile = "flat response"
	globalConfig.Version = "test-version"
	setAPIFiles("/tmp/nyan8-schema-list-api.json", map[string]interface{}{
		"normal": map[string]interface{}{"description": "visible", "script": "/tmp/secret.js", "push": "normal_push", "securitySchemes": []interface{}{map[string]interface{}{"type": "oauth2"}}},
		"job":    map[string]interface{}{"type": apiTypeSchedule, "description": "hidden"},
		"client": map[string]interface{}{"type": apiTypeWSClient, "description": "hidden"},
		"assets": map[string]interface{}{"type": apiTypePublic, "description": "hidden"},
		"mcp":    map[string]interface{}{"type": apiTypeMCP, "description": "hidden"},
	})
	t.Cleanup(func() { setAPIFiles("", nil); globalConfig = originalConfig })

	router := gin.New()
	router.GET("/nyan", handleNyan)
	router.GET("/nyan/*apiName", handleNyanDetail)
	for _, requestPath := range []string{"/nyan", "/nyan/"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d; body=%s", requestPath, recorder.Code, recorder.Body.String())
		}
		var response NyanResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Apis) != 1 {
			t.Fatalf("%s APIs = %#v", requestPath, response.Apis)
		}
		if response.Name != "Nyan8 test" || response.Profile != "flat response" || response.Version != "test-version" {
			t.Fatalf("%s server metadata = %#v", requestPath, response)
		}
		normal := response.Apis["normal"]
		if normal.Description != "visible" || normal.Push != "normal_push" {
			t.Fatalf("normal API = %#v", normal)
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if _, exists := raw["nyan"]; exists {
			t.Fatalf("legacy nyan wrapper was exposed: %s", recorder.Body.String())
		}
		rawAPIs := raw["apis"].(map[string]interface{})
		rawNormal := rawAPIs["normal"].(map[string]interface{})
		for _, internalField := range []string{"script", "type", "securitySchemes"} {
			if _, exists := rawNormal[internalField]; exists {
				t.Fatalf("%s was exposed by API list: %#v", internalField, rawNormal)
			}
		}
	}

	detailRecorder := httptest.NewRecorder()
	router.ServeHTTP(detailRecorder, httptest.NewRequest(http.MethodGet, "/nyan/job", nil))
	if detailRecorder.Code != http.StatusNotFound {
		t.Fatalf("schedule detail status = %d; body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
}

func TestPublishedOutputSchemaDoesNotSupplyMissingRuntimeStatus(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	apiPath := filepath.Join(dir, "api.json")
	writeHotReloadTestFile(t, filepath.Join(dir, "script.js"), `JSON.stringify({success:true, result:{name:"cat"}});`)
	writeHotReloadTestFile(t, filepath.Join(dir, "out-check.js"), `
const nyanOutputSchema = {
  type: "object",
  properties: {
    success: {const: true},
    status: {const: 200},
    result: {type: "object"}
  },
  required: ["success", "status", "result"]
};
({success:true, status:200, result:null});
`)
	writeHotReloadTestFile(t, apiPath, `{
  "item": {
    "script": "./script.js",
    "outCheck": "./out-check.js",
    "description": "runtime status remains required"
  }
}`)
	loaded, err := readAPIConfigFile(apiPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(loaded.Snapshot)
	servicePaths.API.Path = apiPath
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		servicePaths = serviceFilePaths{}
	})

	router := gin.New()
	router.GET("/nyan", handleNyan)
	router.GET("/nyan/*apiName", handleNyanDetail)
	if err := registerDynamicEndpoints(router, dir); err != nil {
		t.Fatal(err)
	}

	detail := httptest.NewRecorder()
	router.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/nyan/item", nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"output":"outCheck"`) {
		t.Fatalf("detail = status %d body %s", detail.Code, detail.Body.String())
	}

	runtimeResponse := httptest.NewRecorder()
	router.ServeHTTP(runtimeResponse, httptest.NewRequest(http.MethodGet, "/item", nil))
	if runtimeResponse.Code != http.StatusInternalServerError || !strings.Contains(runtimeResponse.Body.String(), "Status field not found") {
		t.Fatalf("runtime response = status %d body %s", runtimeResponse.Code, runtimeResponse.Body.String())
	}
}

func TestReadAPIFileRejectsIncludeCycle(t *testing.T) {
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"child":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"root":{"type":"include","path":"./api.json"}}`)

	if _, _, err := readAPIFile(rootPath); err == nil || !strings.Contains(err.Error(), "include cycle detected") {
		t.Fatalf("readAPIFile() error = %v, want include cycle", err)
	}
}

func TestReadAPIFileRejectsIncludeCycleThroughSymlink(t *testing.T) {
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	aliasPath := filepath.Join(rootDir, "alias.json")
	writeHotReloadTestFile(t, rootPath, `{"alias":{"type":"include","path":"./alias.json"}}`)
	if err := os.Symlink(rootPath, aliasPath); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	if _, _, err := readAPIFile(rootPath); err == nil || !strings.Contains(err.Error(), "include cycle detected") || !strings.Contains(err.Error(), aliasPath) {
		t.Fatalf("readAPIFile() error=%v, want symlink cycle", err)
	}
}

func TestReadAPIFileRejectsInvalidIncludeDefinitions(t *testing.T) {
	tests := []struct {
		name       string
		mountName  string
		definition string
		want       string
	}{
		{name: "empty mount", mountName: "", definition: `{"type":"include","path":"./child.json"}`, want: "invalid include mount name"},
		{name: "dot mount", mountName: ".", definition: `{"type":"include","path":"./child.json"}`, want: "invalid include mount name"},
		{name: "dot dot mount", mountName: "..", definition: `{"type":"include","path":"./child.json"}`, want: "invalid include mount name"},
		{name: "surrounding whitespace", mountName: " child ", definition: `{"type":"include","path":"./child.json"}`, want: "invalid include mount name"},
		{name: "slash mount", mountName: "a/b", definition: `{"type":"include","path":"./child.json"}`, want: "invalid include mount name"},
		{name: "empty path", mountName: "child", definition: `{"type":"include","path":" "}`, want: "path is empty"},
		{name: "extra field", mountName: "child", definition: `{"type":"include","path":"./child.json","description":"invalid"}`, want: "unsupported field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootDir := t.TempDir()
			rootPath := filepath.Join(rootDir, "api.json")
			writeHotReloadTestFile(t, filepath.Join(rootDir, "child.json"), `{}`)
			data, err := json.Marshal(map[string]json.RawMessage{tt.mountName: json.RawMessage(tt.definition)})
			if err != nil {
				t.Fatal(err)
			}
			writeHotReloadTestFile(t, rootPath, string(data))
			if _, _, err := readAPIFile(rootPath); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("readAPIFile() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestReadAPIFileRejectsMountNamespaceConflict(t *testing.T) {
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	writeHotReloadTestFile(t, filepath.Join(rootDir, "child.json"), `{}`)
	writeHotReloadTestFile(t, rootPath, `{
		"sub":{"type":"include","path":"./child.json"},
		"sub/direct":{"script":"./direct.js"}
	}`)

	if _, _, err := readAPIFile(rootPath); err == nil || !strings.Contains(err.Error(), "conflicts with mount namespace") {
		t.Fatalf("readAPIFile() error = %v, want namespace conflict", err)
	}
}

func TestDecodeAPIFileRejectsDuplicateJSONKeysAtAnyDepth(t *testing.T) {
	tests := []string{
		`{"api":{},"api":{}}`,
		`{"api":{"script":"one.js","script":"two.js"}}`,
		`{"api":{"trigger":{"type":"cron","type":"timer"}}}`,
		`{"api":{"items":[{"name":"one","name":"two"}]}}`,
	}
	for _, data := range tests {
		if _, err := decodeAPIFile([]byte(data)); err == nil || !strings.Contains(err.Error(), "duplicate key") {
			t.Fatalf("decodeAPIFile(%s) error = %v, want duplicate key", data, err)
		}
	}
}

func TestRootHotReloadRebuildsIncludedDefinitions(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"first":{"description":"first"}}`)
	initial, initialHash, err := readAPIFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	setAPIFiles(rootPath, initial)
	t.Cleanup(func() { setAPIFiles("", nil) })

	writeHotReloadTestFile(t, childPath, `{"second":{"description":"second"}}`)
	writeHotReloadTestFile(t, rootPath, "{\n\"sub\":{\"type\":\"include\",\"path\":\"./child.json\"}}")
	_, reloaded, err := reloadAPIFileIfChanged(rootPath, rootDir, initialHash)
	if err != nil || !reloaded {
		t.Fatalf("reload=%t err=%v", reloaded, err)
	}
	if _, exists := currentAPIFiles()["sub/first"]; exists {
		t.Fatal("old included definition remains after root reload")
	}
	if _, exists := currentAPIFiles()["sub/second"]; !exists {
		t.Fatal("updated included definition is missing after root reload")
	}
}

func TestReloadAPIConfigGraphDetectsGrandchildChangeWithoutRootChange(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	childDir := filepath.Join(rootDir, "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(childDir, "api.json")
	grandchildPath := filepath.Join(childDir, "grandchild.json")
	writeHotReloadTestFile(t, rootPath, `{"child":{"type":"include","path":"./child/api.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"grandchild":{"type":"include","path":"./grandchild.json"}}`)
	writeHotReloadTestFile(t, grandchildPath, `{"old":{"description":"old"}}`)
	loaded, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(loaded.Snapshot)
	oldManager := backgroundRuntimes
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		backgroundRuntimes = oldManager
	})
	writeHotReloadTestFile(t, grandchildPath, `{"new":{"description":"new"}}`)

	states, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, loaded.Snapshot.FileStates)
	if err != nil || !reloaded {
		t.Fatalf("graph reload=%t err=%v", reloaded, err)
	}
	if len(states) != 3 {
		t.Fatalf("observed file count=%d, want 3", len(states))
	}
	if _, exists := currentAPIFiles()["child/grandchild/old"]; exists {
		t.Fatal("old grandchild API remains active")
	}
	if _, exists := currentAPIFiles()["child/grandchild/new"]; !exists {
		t.Fatal("updated grandchild API is missing")
	}
}

func TestReloadAPIConfigGraphUpdatesWatchedFilesWhenIncludesChange(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, childPath, `{"api":{"description":"child"}}`)
	writeHotReloadTestFile(t, rootPath, `{"child":{"type":"include","path":"./child.json"}}`)
	loaded, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(loaded.Snapshot)
	oldManager := backgroundRuntimes
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		backgroundRuntimes = oldManager
	})
	if len(loaded.Snapshot.FileStates) != 2 {
		t.Fatalf("initial watched file count=%d, want 2", len(loaded.Snapshot.FileStates))
	}

	writeHotReloadTestFile(t, rootPath, `{"root":{"description":"root"}}`)
	states, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, loaded.Snapshot.FileStates)
	if err != nil || !reloaded {
		t.Fatalf("include removal reload=%t err=%v", reloaded, err)
	}
	if len(states) != 1 || len(currentAPISnapshot().FileStates) != 1 {
		t.Fatalf("watched files after removal: returned=%d snapshot=%d", len(states), len(currentAPISnapshot().FileStates))
	}

	writeHotReloadTestFile(t, rootPath, `{"child":{"type":"include","path":"./child.json"}}`)
	states, reloaded, err = reloadAPIConfigGraphIfChanged(rootPath, rootDir, states)
	if err != nil || !reloaded {
		t.Fatalf("include addition reload=%t err=%v", reloaded, err)
	}
	if len(states) != 2 || len(currentAPISnapshot().FileStates) != 2 {
		t.Fatalf("watched files after addition: returned=%d snapshot=%d", len(states), len(currentAPISnapshot().FileStates))
	}
	if _, exists := currentAPIFiles()["child/api"]; !exists {
		t.Fatal("re-added include API is missing")
	}
}

func TestReloadAPIConfigGraphKeepsLastGoodSnapshotAndRecoversIncludedFile(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"child":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"current":{"description":"active"}}`)
	loaded, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(loaded.Snapshot)
	oldManager := backgroundRuntimes
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		backgroundRuntimes = oldManager
	})

	writeHotReloadTestFile(t, childPath, `{"broken":`)
	invalidStates, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, loaded.Snapshot.FileStates)
	if err == nil || reloaded {
		t.Fatalf("invalid child reload=%t err=%v", reloaded, err)
	}
	if _, exists := currentAPIFiles()["child/current"]; !exists {
		t.Fatal("last known good included API was replaced")
	}
	if _, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, invalidStates); err != nil || reloaded {
		t.Fatalf("unchanged invalid child reload=%t err=%v", reloaded, err)
	}

	writeHotReloadTestFile(t, childPath, `{"fixed":{"description":"recovered"}}`)
	_, reloaded, err = reloadAPIConfigGraphIfChanged(rootPath, rootDir, invalidStates)
	if err != nil || !reloaded {
		t.Fatalf("fixed child reload=%t err=%v", reloaded, err)
	}
	if _, exists := currentAPIFiles()["child/fixed"]; !exists {
		t.Fatal("fixed included API was not published")
	}
}

func TestWatchAPIFileReloadsGrandchildWithoutRootChange(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	grandchildPath := filepath.Join(rootDir, "grandchild.json")
	writeHotReloadTestFile(t, rootPath, `{"child":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"grandchild":{"type":"include","path":"./grandchild.json"}}`)
	writeHotReloadTestFile(t, grandchildPath, `{"before":{"description":"before"}}`)
	loaded, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(loaded.Snapshot)
	oldLogger := logger
	oldManager := backgroundRuntimes
	logs := &synchronizedBuffer{}
	logger = log.New(logs, "", 0)
	backgroundRuntimes = nil
	done := make(chan struct{})
	watcherStopped := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		<-watcherStopped
		logger = oldLogger
		backgroundRuntimes = oldManager
		publishAPISnapshot(nil)
	})
	go func() {
		defer close(watcherStopped)
		watchAPIFileUntil(rootPath, rootDir, 5*time.Millisecond, loaded.Hash, done)
	}()

	writeHotReloadTestFile(t, grandchildPath, `{"after":{"description":"after"}}`)
	waitForHotReloadCondition(t, "grandchild hot reload", func() bool {
		_, exists := currentAPIFiles()["child/grandchild/after"]
		return exists
	})
	waitForHotReloadCondition(t, "grandchild reload log", func() bool {
		return strings.Contains(logs.String(), "API hot reload succeeded: api_count=1")
	})
}

func TestReloadAPIConfigGraphWatchesMissingCandidateIncludeUntilCreated(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"health":{"description":"active"}}`)
	initial, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(initial.Snapshot)
	oldManager := backgroundRuntimes
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		backgroundRuntimes = oldManager
	})
	writeHotReloadTestFile(t, rootPath, `{"health":{"description":"candidate"},"sub":{"type":"include","path":"./child.json"}}`)

	observed, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, initial.Snapshot.FileStates)
	if err == nil || !strings.Contains(err.Error(), "file not found") || reloaded {
		t.Fatalf("missing include reload=%t err=%v", reloaded, err)
	}
	if currentAPIFiles()["health"].(map[string]interface{})["description"] != "active" {
		t.Fatal("missing include candidate replaced the active snapshot")
	}
	missing, exists := observed[childPath]
	if !exists || missing.Exists || missing.Error != "not_found" {
		t.Fatalf("missing include state=%#v exists=%t", missing, exists)
	}
	unchanged, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, observed)
	if err != nil || reloaded || !reflect.DeepEqual(unchanged, observed) {
		t.Fatalf("unchanged candidate retried: reload=%t err=%v states=%#v", reloaded, err, unchanged)
	}

	writeHotReloadTestFile(t, childPath, `{"item":{"description":"created"}}`)
	observed, reloaded, err = reloadAPIConfigGraphIfChanged(rootPath, rootDir, observed)
	if err != nil || !reloaded {
		t.Fatalf("created include reload=%t err=%v", reloaded, err)
	}
	if _, exists := currentAPIFiles()["sub/item"]; !exists || len(observed) != 2 {
		t.Fatalf("created include was not published: files=%#v states=%#v", currentAPIFiles(), observed)
	}
}

func TestReloadAPIConfigGraphWatchesMissingNestedCandidateInclude(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	grandchildPath := filepath.Join(rootDir, "grandchild.json")
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"local":{"description":"active"}}`)
	initial, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(initial.Snapshot)
	oldManager := backgroundRuntimes
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		backgroundRuntimes = oldManager
	})
	writeHotReloadTestFile(t, childPath, `{"local":{"description":"candidate"},"admin":{"type":"include","path":"./grandchild.json"}}`)

	observed, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, initial.Snapshot.FileStates)
	if err == nil || reloaded {
		t.Fatalf("missing nested include reload=%t err=%v", reloaded, err)
	}
	if _, exists := observed[grandchildPath]; !exists {
		t.Fatal("missing nested include is not watched")
	}
	if currentAPIFiles()["sub/local"].(map[string]interface{})["description"] != "active" {
		t.Fatal("missing nested include candidate replaced the active snapshot")
	}

	writeHotReloadTestFile(t, grandchildPath, `{"user":{"description":"created"}}`)
	observed, reloaded, err = reloadAPIConfigGraphIfChanged(rootPath, rootDir, observed)
	if err != nil || !reloaded {
		t.Fatalf("created nested include reload=%t err=%v", reloaded, err)
	}
	if _, exists := currentAPIFiles()["sub/admin/user"]; !exists || len(observed) != 3 {
		t.Fatalf("created nested include was not published: files=%#v states=%#v", currentAPIFiles(), observed)
	}
}

func TestReloadAPIConfigGraphWatchesInvalidCandidateUntilCorrected(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"health":{"description":"active"}}`)
	initial, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(initial.Snapshot)
	oldManager := backgroundRuntimes
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		backgroundRuntimes = oldManager
	})
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"broken":`)

	observed, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, initial.Snapshot.FileStates)
	if err == nil || reloaded {
		t.Fatalf("invalid candidate reload=%t err=%v", reloaded, err)
	}
	canonicalChild, err := canonicalExistingAPIFilePath(childPath)
	if err != nil {
		t.Fatal(err)
	}
	invalid, exists := observed[canonicalChild]
	if !exists || !invalid.Exists || invalid.Hash == ([sha256.Size]byte{}) {
		t.Fatalf("invalid candidate state=%#v exists=%t", invalid, exists)
	}
	if _, exists := currentAPIFiles()["health"]; !exists {
		t.Fatal("invalid candidate replaced the active snapshot")
	}

	writeHotReloadTestFile(t, childPath, `{"item":{"description":"corrected"}}`)
	_, reloaded, err = reloadAPIConfigGraphIfChanged(rootPath, rootDir, observed)
	if err != nil || !reloaded {
		t.Fatalf("corrected candidate reload=%t err=%v", reloaded, err)
	}
	if _, exists := currentAPIFiles()["sub/item"]; !exists {
		t.Fatal("corrected candidate was not published")
	}
}

func TestReloadAPIConfigGraphKeepsDeletedActiveIncludeWatched(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"item":{"description":"active"}}`)
	initial, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(initial.Snapshot)
	oldManager := backgroundRuntimes
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		backgroundRuntimes = oldManager
	})
	if err := os.Remove(childPath); err != nil {
		t.Fatal(err)
	}

	observed, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, initial.Snapshot.FileStates)
	if err == nil || reloaded {
		t.Fatalf("deleted active include reload=%t err=%v", reloaded, err)
	}
	if _, exists := currentAPIFiles()["sub/item"]; !exists {
		t.Fatal("deleted active include removed the last known good API")
	}
	missing, exists := observed[childPath]
	if !exists || missing.Exists || missing.Error != "not_found" {
		t.Fatalf("deleted include state=%#v exists=%t", missing, exists)
	}

	writeHotReloadTestFile(t, childPath, `{"item":{"description":"restored"}}`)
	_, reloaded, err = reloadAPIConfigGraphIfChanged(rootPath, rootDir, observed)
	if err != nil || !reloaded {
		t.Fatalf("restored include reload=%t err=%v", reloaded, err)
	}
	if currentAPIFiles()["sub/item"].(map[string]interface{})["description"] != "restored" {
		t.Fatal("restored include was not published")
	}
}

func TestVerifyAPIFileStatesRejectsChangesAfterLoad(t *testing.T) {
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"item":{"description":"old"}}`)
	loaded, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	writeHotReloadTestFile(t, childPath, `{"item":{"description":"new"}}`)
	if err := verifyAPIFileStates(loaded.Snapshot.FileStates); err == nil {
		t.Fatal("verifyAPIFileStates() error=nil, want changed-during-load error")
	}
}

func TestAPIFileStatesFingerprintIsDeterministicAndStateSensitive(t *testing.T) {
	first := map[string]APIFileState{
		"/b.json": {Path: "/b.json", Error: "not_found"},
		"/a.json": {Path: "/a.json", Exists: true, Hash: [sha256.Size]byte{1}},
	}
	second := map[string]APIFileState{
		"/a.json": {Path: "/a.json", Exists: true, Hash: [sha256.Size]byte{1}},
		"/b.json": {Path: "/b.json", Error: "not_found"},
	}
	if apiFileStatesFingerprint(first) != apiFileStatesFingerprint(second) {
		t.Fatal("file state fingerprint depends on map iteration order")
	}
	changed := cloneAPIFileStates(second)
	changed["/b.json"] = APIFileState{Path: "/b.json", Exists: true, Hash: [sha256.Size]byte{2}}
	if apiFileStatesFingerprint(first) == apiFileStatesFingerprint(changed) {
		t.Fatal("file state fingerprint did not change with state")
	}
}

func TestIncludedScheduleHotReloadUpdatesAndStopsWithMount(t *testing.T) {
	initTestLogger()
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"job":{"type":"schedule","script":"./job-v1.js","trigger":{"type":"cron","value":"0 0 1 1 *"}}}`)
	initial, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	oldManager := backgroundRuntimes
	manager := newBackgroundRuntimeManager()
	publishAPISnapshot(initial.Snapshot)
	backgroundRuntimes = manager
	manager.reconcile(initial.Snapshot.Schedules, initial.Snapshot.WSClients)
	t.Cleanup(func() {
		manager.reconcile(nil, nil)
		backgroundRuntimes = oldManager
		publishAPISnapshot(nil)
	})
	manager.mu.Lock()
	runtime := manager.schedules["sub/job"]
	manager.mu.Unlock()
	if runtime == nil {
		t.Fatal("included schedule was not started with its expanded name")
	}

	writeHotReloadTestFile(t, childPath, `{"job":{"type":"schedule","script":"./job-v2.js","trigger":{"type":"cron","value":"0 0 2 1 *"}}}`)
	observed, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, initial.Snapshot.FileStates)
	if err != nil || !reloaded {
		t.Fatalf("included schedule update reload=%t err=%v", reloaded, err)
	}
	manager.mu.Lock()
	updatedRuntime := manager.schedules["sub/job"]
	manager.mu.Unlock()
	if updatedRuntime != runtime {
		t.Fatal("included schedule update created a second runtime")
	}
	updated, active := runtime.currentConfig()
	if !active || updated.scriptPath != filepath.Join(rootDir, "job-v2.js") || updated.trigger.Value != "0 0 2 1 *" {
		t.Fatalf("updated included schedule=%#v active=%t", updated, active)
	}

	writeHotReloadTestFile(t, rootPath, `{}`)
	observed, reloaded, err = reloadAPIConfigGraphIfChanged(rootPath, rootDir, observed)
	if err != nil || !reloaded {
		t.Fatalf("included schedule removal reload=%t err=%v", reloaded, err)
	}
	waitForHotReloadSignal(t, runtime.done, "included schedule stop")
	if _, exists := currentAPISnapshot().Schedules["sub/job"]; exists || len(observed) != 1 {
		t.Fatalf("removed schedule remains: schedules=%#v states=%#v", currentAPISnapshot().Schedules, observed)
	}
}

func TestIncludedWSClientHotReloadReconnectsAndStopsWithMount(t *testing.T) {
	initTestLogger()
	firstURL, firstConnected, firstDisconnected := newHotReloadWebSocketServer(t)
	secondURL, secondConnected, secondDisconnected := newHotReloadWebSocketServer(t)
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, fmt.Sprintf(`{"client":{"type":"ws_client","script":"./client-v1.js","connectURL":%q}}`, firstURL))
	initial, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	oldManager := backgroundRuntimes
	manager := newBackgroundRuntimeManager()
	publishAPISnapshot(initial.Snapshot)
	backgroundRuntimes = manager
	manager.reconcile(initial.Snapshot.Schedules, initial.Snapshot.WSClients)
	t.Cleanup(func() {
		manager.reconcile(nil, nil)
		backgroundRuntimes = oldManager
		publishAPISnapshot(nil)
	})
	waitForHotReloadSignal(t, firstConnected, "first included WebSocket connection")
	manager.mu.Lock()
	runtime := manager.wsClients["sub/client"]
	manager.mu.Unlock()
	if runtime == nil {
		t.Fatal("included ws_client was not started with its expanded name")
	}

	writeHotReloadTestFile(t, childPath, fmt.Sprintf(`{"client":{"type":"ws_client","script":"./client-v2.js","connectURL":%q}}`, secondURL))
	observed, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, initial.Snapshot.FileStates)
	if err != nil || !reloaded {
		t.Fatalf("included ws_client update reload=%t err=%v", reloaded, err)
	}
	waitForHotReloadSignal(t, firstDisconnected, "old included WebSocket disconnection")
	waitForHotReloadSignal(t, secondConnected, "updated included WebSocket connection")
	updated, active := runtime.currentConfig()
	if !active || updated.connectURL != secondURL || updated.scriptPath != filepath.Join(rootDir, "client-v2.js") {
		t.Fatalf("updated included ws_client=%#v active=%t", updated, active)
	}

	writeHotReloadTestFile(t, rootPath, `{}`)
	_, reloaded, err = reloadAPIConfigGraphIfChanged(rootPath, rootDir, observed)
	if err != nil || !reloaded {
		t.Fatalf("included ws_client removal reload=%t err=%v", reloaded, err)
	}
	waitForHotReloadSignal(t, secondDisconnected, "included WebSocket mount removal")
	waitForHotReloadSignal(t, runtime.done, "included ws_client stop")
	if _, exists := currentAPISnapshot().WSClients["sub/client"]; exists {
		t.Fatal("removed included ws_client remains in snapshot")
	}
}

func TestIncludedPublicAPIHotReloadUsesExpandedPath(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, "api.json")
	childPath := filepath.Join(rootDir, "child.json")
	for _, version := range []string{"public-v1", "public-v2"} {
		directory := filepath.Join(rootDir, version)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		writeHotReloadTestFile(t, filepath.Join(directory, "file.txt"), version)
	}
	writeHotReloadTestFile(t, rootPath, `{"sub":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"assets":{"type":"public","path":"./public-v1"}}`)
	initial, err := readAPIConfigFile(rootPath, rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(initial.Snapshot)
	servicePaths.API.Path = rootPath
	oldManager := backgroundRuntimes
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(nil)
		servicePaths = serviceFilePaths{}
		backgroundRuntimes = oldManager
	})
	router := gin.New()
	if err := registerDynamicEndpoints(router, rootDir); err != nil {
		t.Fatal(err)
	}
	router.NoRoute(func(c *gin.Context) {
		if !dispatchDynamicEndpoint(c, rootDir) {
			c.Status(http.StatusNotFound)
		}
	})
	assertHTTPBody(t, router, "/sub/assets/file.txt", http.StatusOK, "public-v1")

	writeHotReloadTestFile(t, childPath, `{"static":{"type":"public","path":"./public-v2"}}`)
	_, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, rootDir, initial.Snapshot.FileStates)
	if err != nil || !reloaded {
		t.Fatalf("included public update reload=%t err=%v", reloaded, err)
	}
	assertHTTPBody(t, router, "/sub/assets/file.txt", http.StatusNotFound, "")
	assertHTTPBody(t, router, "/sub/static/file.txt", http.StatusOK, "public-v2")
}

func TestReloadAPIFileRecoversAfterInvalidJSONIsFixed(t *testing.T) {
	initTestLogger()
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, apiPath, `{"current":{"description":"active"}}`)
	initial, initialHash, err := readAPIFile(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	setAPIFiles(apiPath, initial)
	t.Cleanup(func() { setAPIFiles("", nil) })

	writeHotReloadTestFile(t, apiPath, `{"broken":`)
	invalidHash, reloaded, err := reloadAPIFileIfChanged(apiPath, apiDir, initialHash)
	if err == nil || reloaded {
		t.Fatalf("invalid reload=%t err=%v", reloaded, err)
	}
	writeHotReloadTestFile(t, apiPath, `{"fixed":{"description":"recovered"}}`)
	_, reloaded, err = reloadAPIFileIfChanged(apiPath, apiDir, invalidHash)
	if err != nil || !reloaded {
		t.Fatalf("fixed reload=%t err=%v", reloaded, err)
	}
	fixed := currentAPIFiles()["fixed"].(map[string]interface{})
	if fixed["description"] != "recovered" {
		t.Fatalf("fixed definition = %#v", fixed)
	}
}

func TestReloadAPIFileReadErrorKeepsCurrentDefinition(t *testing.T) {
	initTestLogger()
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, apiPath, `{"current":{"description":"active"}}`)
	initial, initialHash, err := readAPIFile(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	setAPIFiles(apiPath, initial)
	t.Cleanup(func() { setAPIFiles("", nil) })
	if err := os.Remove(apiPath); err != nil {
		t.Fatal(err)
	}
	observedHash, reloaded, err := reloadAPIFileIfChanged(apiPath, apiDir, initialHash)
	if err == nil || reloaded || observedHash != initialHash {
		t.Fatalf("hash=%x reload=%t err=%v", observedHash, reloaded, err)
	}
	if _, exists := currentAPIFiles()["current"]; !exists {
		t.Fatal("current definition was lost after read error")
	}
}

func TestReloadAPIFileSkipsSemanticallyUnchangedDefinition(t *testing.T) {
	initTestLogger()
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, apiPath, `{"api":{"description":"same"}}`)
	initial, initialHash, err := readAPIFile(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	setAPIFiles(apiPath, initial)
	t.Cleanup(func() { setAPIFiles("", nil) })
	writeHotReloadTestFile(t, apiPath, "{\n  \"api\": {\"description\": \"same\"}\n}\n")
	observedHash, reloaded, err := reloadAPIFileIfChanged(apiPath, apiDir, initialHash)
	if err != nil || reloaded || observedHash == initialHash {
		t.Fatalf("hashChanged=%t reload=%t err=%v", observedHash != initialHash, reloaded, err)
	}
}

func TestReloadAPIFileRejectsAllInvalidBackgroundVariants(t *testing.T) {
	t.Setenv("NYAN8_TEST_MISSING_WS_URL", "")
	tests := []struct {
		name      string
		candidate string
	}{
		{name: "schedule missing script", candidate: `{"job":{"type":"schedule","trigger":{"type":"cron","value":"* * * * *"}}}`},
		{name: "schedule unsupported trigger", candidate: `{"job":{"type":"schedule","script":"job.js","trigger":{"type":"timer","value":"* * * * *"}}}`},
		{name: "schedule invalid cron", candidate: `{"job":{"type":"schedule","script":"job.js","trigger":{"type":"cron","value":"invalid"}}}`},
		{name: "ws client missing script", candidate: `{"client":{"type":"ws_client","connectURL":"ws://localhost"}}`},
		{name: "ws client missing URL", candidate: `{"client":{"type":"ws_client","script":"client.js"}}`},
		{name: "ws client unresolved env", candidate: `{"client":{"type":"ws_client","script":"client.js","connectURL":"env:NYAN8_TEST_MISSING_WS_URL"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestLogger()
			apiDir := t.TempDir()
			apiPath := filepath.Join(apiDir, "api.json")
			writeHotReloadTestFile(t, apiPath, `{"current":{"description":"active"}}`)
			initial, initialHash, err := readAPIFile(apiPath)
			if err != nil {
				t.Fatal(err)
			}
			setAPIFiles(apiPath, initial)
			writeHotReloadTestFile(t, apiPath, tt.candidate)
			if _, reloaded, err := reloadAPIFileIfChanged(apiPath, apiDir, initialHash); err == nil || reloaded {
				t.Fatalf("reload=%t err=%v, want rejected", reloaded, err)
			}
			if _, exists := currentAPIFiles()["current"]; !exists {
				t.Fatal("last known good definition was replaced")
			}
			setAPIFiles("", nil)
		})
	}
}

func TestWatchAPIFileSuppressesDuplicateErrorsAndRecovers(t *testing.T) {
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, apiPath, `{"current":{"description":"active"}}`)
	initial, initialHash, err := readAPIFile(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	setAPIFiles(apiPath, initial)
	oldLogger := logger
	oldManager := backgroundRuntimes
	logs := &synchronizedBuffer{}
	logger = log.New(logs, "", 0)
	backgroundRuntimes = nil
	done := make(chan struct{})
	watcherStopped := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		<-watcherStopped
		logger = oldLogger
		backgroundRuntimes = oldManager
		setAPIFiles("", nil)
	})
	go func() {
		defer close(watcherStopped)
		watchAPIFileUntil(apiPath, apiDir, 5*time.Millisecond, initialHash, done)
	}()

	writeHotReloadTestFile(t, apiPath, `{"broken":`)
	waitForHotReloadCondition(t, "first reload error", func() bool {
		return strings.Count(logs.String(), "API hot reload failed:") == 1
	})
	time.Sleep(25 * time.Millisecond)
	if count := strings.Count(logs.String(), "API hot reload failed:"); count != 1 {
		t.Fatalf("reload error count=%d, want 1; logs=%q", count, logs.String())
	}

	writeHotReloadTestFile(t, apiPath, `{"fixed":{"description":"recovered"}}`)
	waitForHotReloadCondition(t, "watcher recovery", func() bool {
		_, exists := currentAPIFiles()["fixed"]
		return exists
	})
	waitForHotReloadCondition(t, "success log", func() bool {
		return strings.Contains(logs.String(), "API hot reload succeeded: api_count=1")
	})
}

func TestDynamicDispatcherServesAPIAddedAfterStartup(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	scriptPath := filepath.Join(apiDir, "added.js")
	writeHotReloadTestFile(t, scriptPath, `JSON.stringify({status: 200, value: "hot"});`)
	files := map[string]interface{}{
		"added": map[string]interface{}{"script": "./added.js"},
	}
	setAPIFiles(apiPath, files)
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })

	router := gin.New()
	router.NoRoute(func(c *gin.Context) {
		if !dispatchDynamicEndpoint(c, apiDir) {
			c.Status(http.StatusNotFound)
		}
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/added", nil))
	if rec.Code != http.StatusOK || !containsJSONValue(rec.Body.Bytes(), "value", "hot") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestDynamicDispatcherServesPublicAPIAddedAfterStartup(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	publicDir := filepath.Join(apiDir, "public")
	if err := os.Mkdir(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeHotReloadTestFile(t, filepath.Join(publicDir, "hello.txt"), "hello hot reload")
	setAPIFiles(apiPath, map[string]interface{}{
		"assets": map[string]interface{}{"type": "public", "path": "./public"},
	})
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })

	router := gin.New()
	router.NoRoute(func(c *gin.Context) {
		if !dispatchDynamicEndpoint(c, apiDir) {
			c.Status(http.StatusNotFound)
		}
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/hello.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "hello hot reload" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestRegisteredAPIUsesUpdatedDefinitionAndRejectsDeletion(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, filepath.Join(apiDir, "v1.js"), `JSON.stringify({status: 200, value: "v1"});`)
	writeHotReloadTestFile(t, filepath.Join(apiDir, "v2.js"), `JSON.stringify({status: 200, value: "v2"});`)
	setAPIFiles(apiPath, map[string]interface{}{"hot": map[string]interface{}{"script": "./v1.js"}})
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })
	router := gin.New()
	if err := registerDynamicEndpoints(router, apiDir); err != nil {
		t.Fatal(err)
	}

	assertDynamicAPIValue(t, router, "/hot", http.StatusOK, "v1")
	setAPIFiles(apiPath, map[string]interface{}{"hot": map[string]interface{}{"script": "./v2.js"}})
	assertDynamicAPIValue(t, router, "/hot", http.StatusOK, "v2")
	setAPIFiles(apiPath, map[string]interface{}{})
	assertDynamicAPIValue(t, router, "/hot", http.StatusNotFound, "")
}

func TestRegisteredPublicAPIUsesUpdatedPathAndRejectsDeletion(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	for _, version := range []string{"v1", "v2"} {
		dir := filepath.Join(apiDir, version)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeHotReloadTestFile(t, filepath.Join(dir, "file.txt"), version)
	}
	setAPIFiles(apiPath, map[string]interface{}{"assets": map[string]interface{}{"type": "public", "path": "./v1"}})
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })
	router := gin.New()
	if err := registerDynamicEndpoints(router, apiDir); err != nil {
		t.Fatal(err)
	}

	assertHTTPBody(t, router, "/assets/file.txt", http.StatusOK, "v1")
	setAPIFiles(apiPath, map[string]interface{}{"assets": map[string]interface{}{"type": "public", "path": "./v2"}})
	assertHTTPBody(t, router, "/assets/file.txt", http.StatusOK, "v2")
	setAPIFiles(apiPath, map[string]interface{}{})
	assertHTTPBody(t, router, "/assets/file.txt", http.StatusNotFound, "")
}

func TestDynamicDispatcherSupportsAPIAliasAndSlashNames(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, filepath.Join(apiDir, "api.js"), `JSON.stringify({status: 200, value: nyanAllParams.api});`)
	setAPIFiles(apiPath, map[string]interface{}{
		"added":       map[string]interface{}{"script": "./api.js"},
		"nested/name": map[string]interface{}{"script": "./api.js"},
	})
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })
	router := gin.New()
	router.NoRoute(func(c *gin.Context) {
		if !dispatchDynamicEndpoint(c, apiDir) {
			c.Status(http.StatusNotFound)
		}
	})
	assertDynamicAPIValue(t, router, "/api/added", http.StatusOK, "added")
	assertDynamicAPIValue(t, router, "/nested/name", http.StatusOK, "nested/name")
}

func TestRegisteredPublicAPITypeChangeToNormalAPI(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	publicDir := filepath.Join(apiDir, "public")
	if err := os.Mkdir(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeHotReloadTestFile(t, filepath.Join(apiDir, "api.js"), `JSON.stringify({status: 200, value: "converted"});`)
	setAPIFiles(apiPath, map[string]interface{}{"switch": map[string]interface{}{"type": "public", "path": "./public"}})
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })
	router := gin.New()
	if err := registerDynamicEndpoints(router, apiDir); err != nil {
		t.Fatal(err)
	}
	setAPIFiles(apiPath, map[string]interface{}{"switch": map[string]interface{}{"script": "./api.js"}})
	assertDynamicAPIValue(t, router, "/switch", http.StatusOK, "converted")
}

func TestJSONRPCUsesUpdatedDefinition(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, filepath.Join(apiDir, "v1.js"), `JSON.stringify({status: 200, value: "v1"});`)
	writeHotReloadTestFile(t, filepath.Join(apiDir, "v2.js"), `JSON.stringify({status: 200, value: "v2"});`)
	setAPIFiles(apiPath, map[string]interface{}{"hot": map[string]interface{}{"script": "./v1.js", "description": "first"}})
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })
	router := gin.New()
	router.POST("/nyan-rpc", handleJSONRPC)

	assertJSONRPCValue(t, router, "v1")
	setAPIFiles(apiPath, map[string]interface{}{"hot": map[string]interface{}{"script": "./v2.js", "description": "second"}})
	assertJSONRPCValue(t, router, "v2")
}

func TestNyanCallMeUsesUpdatedDefinition(t *testing.T) {
	initTestLogger()
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	writeHotReloadTestFile(t, filepath.Join(apiDir, "v1.js"), `JSON.stringify({status: 200, value: "v1"});`)
	writeHotReloadTestFile(t, filepath.Join(apiDir, "v2.js"), `JSON.stringify({status: 200, value: "v2"});`)
	setAPIFiles(apiPath, map[string]interface{}{"hot": map[string]interface{}{"script": "./v1.js"}})
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })
	result, err := callNyanAPIFromVM("hot", map[string]interface{}{}, nil)
	if err != nil || !containsJSONValue([]byte(result), "value", "v1") {
		t.Fatalf("first nyanCallMe result=%q err=%v", result, err)
	}
	setAPIFiles(apiPath, map[string]interface{}{"hot": map[string]interface{}{"script": "./v2.js"}})
	result, err = callNyanAPIFromVM("hot", map[string]interface{}{}, nil)
	if err != nil || !containsJSONValue([]byte(result), "value", "v2") {
		t.Fatalf("updated nyanCallMe result=%q err=%v", result, err)
	}
}

func TestHandleNyanDoesNotMutatePublishedAPIFiles(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	setAPIFiles(apiPath, map[string]interface{}{
		"hello": map[string]interface{}{"script": "./hello.js", "description": "hello"},
	})
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })

	router := gin.New()
	router.GET("/nyan", handleNyan)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nyan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	hello := currentAPIFiles()["hello"].(map[string]interface{})
	if hello["script"] != "./hello.js" {
		t.Fatal("handleNyan mutated the published API definition")
	}
}

func TestHandleNyanUsesUpdatedDefinition(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	apiDir := t.TempDir()
	apiPath := filepath.Join(apiDir, "api.json")
	setAPIFiles(apiPath, map[string]interface{}{"first": map[string]interface{}{"description": "first"}})
	servicePaths.API.Path = apiPath
	t.Cleanup(func() { setAPIFiles("", nil); servicePaths = serviceFilePaths{} })
	router := gin.New()
	router.GET("/nyan", handleNyan)
	setAPIFiles(apiPath, map[string]interface{}{"second": map[string]interface{}{"description": "second"}})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/nyan", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	var response NyanResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, exists := response.Apis["first"]; exists {
		t.Fatal("removed API remains in /nyan response")
	}
	if _, exists := response.Apis["second"]; !exists {
		t.Fatalf("updated API missing from /nyan response: %#v", response.Apis)
	}
}

func TestAPIFilesConcurrentReadAndReplace(t *testing.T) {
	setAPIFiles("/tmp/api.json", map[string]interface{}{"api": map[string]interface{}{"description": "initial"}})
	t.Cleanup(func() { setAPIFiles("", nil) })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_ = currentAPIFiles()["api"]
			}
		}()
	}
	for i := 0; i < 1000; i++ {
		setAPIFiles("/tmp/api.json", map[string]interface{}{"api": map[string]interface{}{"description": fmt.Sprintf("updated-%d", i)}})
	}
	wg.Wait()
}

func TestBackgroundRuntimeManagerUpdatesAndStopsSchedule(t *testing.T) {
	initTestLogger()
	manager := newBackgroundRuntimeManager()
	firstSchedule, _ := parseCronSchedule("0 0 1 1 *")
	first := scheduleJobConfig{name: "job", scriptPath: "/tmp/job-v1.js", trigger: triggerConfig{Type: "cron", Value: "0 0 1 1 *"}, schedule: firstSchedule}
	manager.reconcile(map[string]scheduleJobConfig{"job": first}, nil)
	manager.mu.Lock()
	runtime := manager.schedules["job"]
	manager.mu.Unlock()
	if runtime == nil {
		t.Fatal("schedule runtime was not started")
	}
	secondSchedule, _ := parseCronSchedule("0 0 2 1 *")
	second := scheduleJobConfig{name: "job", scriptPath: "/tmp/job-v2.js", trigger: triggerConfig{Type: "cron", Value: "0 0 2 1 *"}, schedule: secondSchedule}
	manager.reconcile(map[string]scheduleJobConfig{"job": second}, nil)
	manager.mu.Lock()
	updatedRuntime := manager.schedules["job"]
	manager.mu.Unlock()
	if updatedRuntime != runtime {
		t.Fatal("schedule update created a second runtime")
	}
	if got, active := runtime.currentConfig(); !active || got.scriptPath != second.scriptPath {
		t.Fatalf("runtime config = %#v, active=%t", got, active)
	}
	manager.reconcile(nil, nil)
	waitForHotReloadSignal(t, runtime.done, "schedule stop")
	waitForHotReloadCondition(t, "schedule manager cleanup", func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		_, exists := manager.schedules["job"]
		return !exists
	})
}

func TestScheduleDescriptionUpdateDoesNotWakeTimer(t *testing.T) {
	schedule, err := parseCronSchedule("0 0 1 1 *")
	if err != nil {
		t.Fatal(err)
	}
	first := scheduleJobConfig{name: "job", scriptPath: "/tmp/job.js", trigger: triggerConfig{Type: "cron", Value: "0 0 1 1 *"}, description: "first", schedule: schedule}
	runtime := newScheduleRuntime(first)
	updated := first
	updated.description = "second"
	accepted, changed := runtime.update(&updated)
	if !accepted || !changed {
		t.Fatalf("description update accepted=%t changed=%t", accepted, changed)
	}
	if len(runtime.wake) != 0 {
		t.Fatal("description-only update woke the schedule timer")
	}
	got, active := runtime.currentConfig()
	if !active || got.description != "second" {
		t.Fatalf("current config=%#v active=%t", got, active)
	}
}

func TestScheduleRuntimeAcceptsImmediateReAddBeforeStop(t *testing.T) {
	schedule, _ := parseCronSchedule("0 0 1 1 *")
	first := scheduleJobConfig{name: "job", scriptPath: "/tmp/v1.js", trigger: triggerConfig{Type: "cron", Value: "0 0 1 1 *"}, schedule: schedule}
	runtime := newScheduleRuntime(first)
	if accepted, changed := runtime.update(nil); !accepted || !changed {
		t.Fatalf("delete accepted=%t changed=%t", accepted, changed)
	}
	second := first
	second.scriptPath = "/tmp/v2.js"
	if accepted, changed := runtime.update(&second); !accepted || !changed {
		t.Fatalf("re-add accepted=%t changed=%t", accepted, changed)
	}
	got, active := runtime.currentConfig()
	if !active || got.scriptPath != second.scriptPath {
		t.Fatalf("current config=%#v active=%t", got, active)
	}
}

func TestBackgroundRuntimeManagerReconnectsWebSocketOnlyForURLChange(t *testing.T) {
	initTestLogger()
	firstURL, firstConnected, firstDisconnected := newHotReloadWebSocketServer(t)
	secondURL, secondConnected, secondDisconnected := newHotReloadWebSocketServer(t)
	manager := newBackgroundRuntimeManager()
	first := wsClientConfig{name: "client", scriptPath: "/tmp/client-v1.js", connectURL: firstURL, description: "first"}
	manager.reconcile(nil, map[string]wsClientConfig{"client": first})
	waitForHotReloadSignal(t, firstConnected, "first connect")
	manager.mu.Lock()
	runtime := manager.wsClients["client"]
	manager.mu.Unlock()

	softUpdate := first
	softUpdate.scriptPath = "/tmp/client-v2.js"
	softUpdate.description = "second"
	manager.reconcile(nil, map[string]wsClientConfig{"client": softUpdate})
	select {
	case <-firstDisconnected:
		t.Fatal("script/description update closed the connection")
	case <-time.After(100 * time.Millisecond):
	}

	reconnectUpdate := softUpdate
	reconnectUpdate.connectURL = secondURL
	manager.reconcile(nil, map[string]wsClientConfig{"client": reconnectUpdate})
	waitForHotReloadSignal(t, firstDisconnected, "old disconnect")
	waitForHotReloadSignal(t, secondConnected, "second connect")
	manager.reconcile(nil, nil)
	waitForHotReloadSignal(t, secondDisconnected, "second disconnect")
	waitForHotReloadSignal(t, runtime.done, "ws client stop")
	waitForHotReloadCondition(t, "ws client manager cleanup", func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		_, exists := manager.wsClients["client"]
		return !exists
	})
}

func TestWebSocketClientSoftUpdateUsesLatestScriptAndDescription(t *testing.T) {
	initTestLogger()
	apiDir := t.TempDir()
	firstScript := filepath.Join(apiDir, "first.js")
	secondScript := filepath.Join(apiDir, "second.js")
	writeHotReloadTestFile(t, firstScript, `"first:" + nyanAllParams.ws_description;`)
	writeHotReloadTestFile(t, secondScript, `"second:" + nyanAllParams.ws_description;`)
	serverURL, connected, replies, sendSecond, disconnected := newHotReloadMessageServer(t)
	manager := newBackgroundRuntimeManager()
	first := wsClientConfig{name: "client", scriptPath: firstScript, connectURL: serverURL, description: "one"}
	manager.reconcile(nil, map[string]wsClientConfig{"client": first})
	waitForHotReloadSignal(t, connected, "ws message test connect")
	if reply := waitForHotReloadString(t, replies, "first reply"); reply != "first:one" {
		t.Fatalf("first reply=%q", reply)
	}

	updated := first
	updated.scriptPath = secondScript
	updated.description = "two"
	manager.reconcile(nil, map[string]wsClientConfig{"client": updated})
	close(sendSecond)
	if reply := waitForHotReloadString(t, replies, "second reply"); reply != "second:two" {
		t.Fatalf("second reply=%q", reply)
	}
	select {
	case <-disconnected:
		t.Fatal("soft update disconnected the WebSocket")
	default:
	}
	manager.mu.Lock()
	runtime := manager.wsClients["client"]
	manager.mu.Unlock()
	manager.reconcile(nil, nil)
	waitForHotReloadSignal(t, disconnected, "ws message test disconnect")
	waitForHotReloadSignal(t, runtime.done, "ws message runtime stop")
}

func TestWebSocketClientStopsDuringReconnectBackoff(t *testing.T) {
	initTestLogger()
	manager := newBackgroundRuntimeManager()
	cfg := wsClientConfig{name: "client", scriptPath: "/tmp/client.js", connectURL: "ws://127.0.0.1:1"}
	manager.reconcile(nil, map[string]wsClientConfig{"client": cfg})
	manager.mu.Lock()
	runtime := manager.wsClients["client"]
	manager.mu.Unlock()
	if runtime == nil {
		t.Fatal("ws client runtime was not started")
	}
	time.Sleep(25 * time.Millisecond)
	manager.reconcile(nil, nil)
	waitForHotReloadSignal(t, runtime.done, "ws client backoff stop")
}

func writeHotReloadTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.Write(data)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.String()
}

func assertDynamicAPIValue(t *testing.T, handler http.Handler, path string, wantStatus int, wantValue string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != wantStatus {
		t.Fatalf("GET %s status=%d, want %d; body=%q", path, recorder.Code, wantStatus, recorder.Body.String())
	}
	if wantValue != "" && !containsJSONValue(recorder.Body.Bytes(), "value", wantValue) {
		t.Fatalf("GET %s body=%q, want value=%q", path, recorder.Body.String(), wantValue)
	}
}

func assertHTTPBody(t *testing.T, handler http.Handler, path string, wantStatus int, wantBody string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != wantStatus {
		t.Fatalf("GET %s status=%d, want %d; body=%q", path, recorder.Code, wantStatus, recorder.Body.String())
	}
	if wantBody != "" && recorder.Body.String() != wantBody {
		t.Fatalf("GET %s body=%q, want %q", path, recorder.Body.String(), wantBody)
	}
}

func assertDefinitionPath(t *testing.T, files map[string]interface{}, apiName, field, want string) {
	t.Helper()
	definition, ok := files[apiName].(map[string]interface{})
	if !ok {
		t.Fatalf("API %q definition type = %T", apiName, files[apiName])
	}
	if got := definition[field]; got != want {
		t.Fatalf("API %q %s = %v, want %q", apiName, field, got, want)
	}
}

func assertJSONRPCValue(t *testing.T, handler http.Handler, want string) {
	t.Helper()
	requestBody := strings.NewReader(`{"jsonrpc":"2.0","method":"hot","params":{},"id":1}`)
	request := httptest.NewRequest(http.MethodPost, "/nyan-rpc", requestBody)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("JSON-RPC status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Result map[string]interface{} `json:"result"`
		Error  *JSONRPCError          `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || response.Result["value"] != want {
		t.Fatalf("JSON-RPC response=%s, want value=%q", recorder.Body.String(), want)
	}
}

func containsJSONValue(data []byte, key, want string) bool {
	var value map[string]interface{}
	return json.Unmarshal(data, &value) == nil && value[key] == want
}

func newHotReloadWebSocketServer(t *testing.T) (string, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	connected := make(chan struct{}, 1)
	disconnected := make(chan struct{}, 1)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local TCP listener is unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connected <- struct{}{}
		defer func() { disconnected <- struct{}{}; _ = conn.Close() }()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return "ws" + server.URL[len("http"):], connected, disconnected
}

func newHotReloadMessageServer(t *testing.T) (string, <-chan struct{}, <-chan string, chan struct{}, <-chan struct{}) {
	t.Helper()
	connected := make(chan struct{}, 1)
	replies := make(chan string, 2)
	sendSecond := make(chan struct{})
	disconnected := make(chan struct{}, 1)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local TCP listener is unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connected <- struct{}{}
		defer func() { disconnected <- struct{}{}; _ = conn.Close() }()
		if err := conn.WriteMessage(websocket.TextMessage, []byte("first")); err != nil {
			return
		}
		if _, reply, err := conn.ReadMessage(); err == nil {
			replies <- string(reply)
		} else {
			return
		}
		<-sendSecond
		if err := conn.WriteMessage(websocket.TextMessage, []byte("second")); err != nil {
			return
		}
		if _, reply, err := conn.ReadMessage(); err == nil {
			replies <- string(reply)
		} else {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return "ws" + server.URL[len("http"):], connected, replies, sendSecond, disconnected
}

func waitForHotReloadSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForHotReloadString(t *testing.T, values <-chan string, label string) string {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return ""
	}
}

func waitForHotReloadCondition(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

func TestMCPPhase12RequiresTypeMCPForDispatch(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	delete(definitions, "custom-mcp")
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Snapshot.MCPServers) != 0 {
		t.Fatalf("MCP configs = %#v, want none", loaded.Snapshot.MCPServers)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	recorder := serveMCPPhase12Request(router, newMCPPhase12Request(http.MethodPost, "/custom-mcp", mcpPhase12InitializeBody(mcpProtocol20251125)))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%q", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
}

func TestMCPPhase12UsesAPINameAsPath(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)

	request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", mcpPhase12InitializeBody(mcpProtocol20251125))
	request.Header.Set("Origin", "https://chatgpt.com")
	recorder := serveMCPPhase12Request(router, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("configured MCP status = %d, want %d; body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	assertMCPPhase12InitializeResponse(t, recorder, mcpProtocol20251125)

	queryRequest := newMCPPhase12Request(http.MethodPost, "/?api=custom-mcp", mcpPhase12InitializeBody(mcpProtocol20251125))
	queryRequest.Header.Set("Origin", "https://chatgpt.com")
	queryRecorder := serveMCPPhase12Request(router, queryRequest)
	if queryRecorder.Code != http.StatusOK {
		t.Fatalf("query MCP status = %d, want %d; body=%q", queryRecorder.Code, http.StatusOK, queryRecorder.Body.String())
	}
	assertMCPPhase12InitializeResponse(t, queryRecorder, mcpProtocol20251125)

	for _, legacyPath := range []string{"/mcp", "/nyan-toolbox"} {
		recorder = serveMCPPhase12Request(router, newMCPPhase12Request(http.MethodPost, legacyPath, mcpPhase12InitializeBody(mcpProtocol20251125)))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want %d; body=%q", legacyPath, recorder.Code, http.StatusNotFound, recorder.Body.String())
		}
	}
}

func TestAPIConfigRejectsReservedNyanNamespace(t *testing.T) {
	for _, reservedName := range []string{"nyan", "nyan-rpc", "nyan-toolbox", "nyan-custom"} {
		t.Run(reservedName, func(t *testing.T) {
			dir, definitions := newMCPPhase12Definitions(t)
			mcp := definitions["custom-mcp"]
			delete(definitions, "custom-mcp")
			definitions[reservedName] = mcp
			_, err := loadMCPPhase12Config(dir, definitions)
			if err == nil || !strings.Contains(err.Error(), "reserved nyan namespace") {
				t.Fatalf("error=%v, want reserved nyan namespace", err)
			}
		})
	}
}

func TestMCPPhase12AllowsMultipleServers(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	second := cloneMCPPhase12Map(t, mcpPhase12Entry(t, definitions))
	delete(second, "oauth")
	delete(second, "redirectURIAllowedPrefixes")
	definitions["second-server"] = second
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Snapshot.MCPServers) != 2 || loaded.Snapshot.MCPServers["custom-mcp"].Path != "/custom-mcp" || loaded.Snapshot.MCPServers["second-server"].Path != "/second-server" {
		t.Fatalf("MCP servers = %#v", loaded.Snapshot.MCPServers)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	request := newMCPPhase12Request(http.MethodPost, "/second-server", mcpPhase12InitializeBody(mcpProtocol20251125))
	response := serveMCPPhase12Request(router, request)
	if response.Code != http.StatusOK {
		t.Fatalf("second MCP status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestMCPAPINameAndToolsHotReloadAtomically(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	initial, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, initial)
	apiPath := filepath.Join(dir, "api.json")
	states := initial.Snapshot.FileStates

	definitions["sample"].(map[string]interface{})["description"] = "reloaded description"
	data, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	writeHotReloadTestFile(t, apiPath, string(data))
	states, reloaded, err := reloadAPIConfigGraphIfChanged(apiPath, dir, states)
	if err != nil || !reloaded {
		t.Fatalf("Tool metadata reload: reloaded=%v err=%v", reloaded, err)
	}
	listRequest := newMCPPhase12Request(http.MethodPost, "/custom-mcp", `{"jsonrpc":"2.0","id":"reload-list","method":"tools/list","params":{}}`)
	listRequest.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	listResponse := serveMCPPhase12Request(router, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), "reloaded description") {
		t.Fatalf("reloaded tools/list status=%d body=%q", listResponse.Code, listResponse.Body.String())
	}

	definitions["renamed-mcp"] = definitions["custom-mcp"]
	delete(definitions, "custom-mcp")
	data, err = json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	writeHotReloadTestFile(t, apiPath, string(data))
	states, reloaded, err = reloadAPIConfigGraphIfChanged(apiPath, dir, states)
	if err != nil || !reloaded {
		t.Fatalf("MCP rename reload: reloaded=%v err=%v", reloaded, err)
	}
	pingBody := `{"jsonrpc":"2.0","id":"reload-ping","method":"ping","params":{}}`
	oldRequest := newMCPPhase12Request(http.MethodPost, "/custom-mcp", pingBody)
	oldRequest.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	if response := serveMCPPhase12Request(router, oldRequest); response.Code != http.StatusNotFound {
		t.Fatalf("old MCP endpoint status=%d body=%q", response.Code, response.Body.String())
	}
	newRequest := newMCPPhase12Request(http.MethodPost, "/renamed-mcp", pingBody)
	newRequest.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	if response := serveMCPPhase12Request(router, newRequest); response.Code != http.StatusOK {
		t.Fatalf("renamed MCP endpoint status=%d body=%q", response.Code, response.Body.String())
	}

	definitions["renamed-mcp"].(map[string]interface{})["path"] = "/legacy-path"
	data, err = json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	writeHotReloadTestFile(t, apiPath, string(data))
	_, reloaded, err = reloadAPIConfigGraphIfChanged(apiPath, dir, states)
	if err == nil || reloaded {
		t.Fatalf("invalid candidate: reloaded=%v err=%v", reloaded, err)
	}
	newRequest = newMCPPhase12Request(http.MethodPost, "/renamed-mcp", pingBody)
	newRequest.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	if response := serveMCPPhase12Request(router, newRequest); response.Code != http.StatusOK {
		t.Fatalf("active snapshot was lost after invalid reload: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestMCPPhase12RejectsUnknownAndNonAPIBacking(t *testing.T) {
	tests := []struct {
		name       string
		backingAPI string
	}{
		{name: "unknown", backingAPI: "missing"},
		{name: "public", backingAPI: "assets"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, definitions := newMCPPhase12Definitions(t)
			mcpPhase12Entry(t, definitions)["tools"] = []interface{}{test.backingAPI}
			_, err := loadMCPPhase12Config(dir, definitions)
			if err == nil || !strings.Contains(err.Error(), "invalid backing API") {
				t.Fatalf("error = %v, want invalid backing API rejection", err)
			}
		})
	}
}

func TestMCPPhase12RejectsExternalSchemaReferences(t *testing.T) {
	for _, field := range []string{"paramCheck", "outCheck"} {
		t.Run(field, func(t *testing.T) {
			dir, definitions := newMCPPhase12Definitions(t)
			constant := "nyanInputSchema"
			if field == "outCheck" {
				constant = "nyanOutputSchema"
			}
			path := filepath.Join(dir, "external-schema-"+field+".js")
			writeHotReloadTestFile(t, path, fmt.Sprintf(`const %s={"$ref":"https://schemas.example.test/tool.json"}; ({success:true,status:200,result:{}});`, constant))
			definitions["sample"].(map[string]interface{})[field] = path
			_, err := loadMCPPhase12Config(dir, definitions)
			if err == nil || !strings.Contains(err.Error(), "external JSON Schema resource is not allowed") {
				t.Fatalf("error = %v, want external schema rejection", err)
			}
		})
	}
}

func TestMCPPhase12InitializeSupportedVersions(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)

	for _, version := range []string{mcpProtocol20251125, mcpProtocol20250618} {
		t.Run(version, func(t *testing.T) {
			request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", mcpPhase12InitializeBody(version))
			request.Header.Set("Origin", "https://chatgpt.com")
			recorder := serveMCPPhase12Request(router, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			assertMCPPhase12InitializeResponse(t, recorder, version)
		})
	}
}

func TestMCPPhase12InitializeRejectsUnsupportedVersion(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	recorder := serveMCPPhase12Request(router, newMCPPhase12Request(http.MethodPost, "/custom-mcp", mcpPhase12InitializeBody("2025-03-26")))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if code := mcpPhase12ErrorCode(t, recorder); code != -32602 {
		t.Fatalf("error code = %d, want -32602; body=%q", code, recorder.Body.String())
	}
}

func TestMCPPhase12HTTPBoundaryValidation(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	validBody := mcpPhase12InitializeBody(mcpProtocol20251125)

	tests := []struct {
		name       string
		request    func() *http.Request
		wantStatus int
	}{
		{
			name: "GET",
			request: func() *http.Request {
				return newMCPPhase12Request(http.MethodGet, "/custom-mcp", "")
			},
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name: "DELETE",
			request: func() *http.Request {
				return newMCPPhase12Request(http.MethodDelete, "/custom-mcp", "")
			},
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name: "Content-Type",
			request: func() *http.Request {
				request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", validBody)
				request.Header.Set("Content-Type", "text/plain")
				return request
			},
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "Accept",
			request: func() *http.Request {
				request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", validBody)
				request.Header.Set("Accept", "application/json")
				return request
			},
			wantStatus: http.StatusNotAcceptable,
		},
		{
			name: "Origin",
			request: func() *http.Request {
				request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", validBody)
				request.Header.Set("Origin", "https://attacker.example.test")
				return request
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "body limit",
			request: func() *http.Request {
				return newMCPPhase12Request(http.MethodPost, "/custom-mcp", strings.Repeat("x", int(maxMCPRequestBytes)+1))
			},
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveMCPPhase12Request(router, test.request())
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestMCPPhase12JSONRPCValidationAndNotification(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)

	tests := []struct {
		name     string
		body     string
		protocol string
		wantCode int
	}{
		{
			name:     "batch",
			body:     `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
			wantCode: -32600,
		},
		{
			name:     "parse error",
			body:     `{"jsonrpc":`,
			wantCode: -32700,
		},
		{
			name:     "unknown method",
			body:     `{"jsonrpc":"2.0","id":3,"method":"unknown/method","params":{}}`,
			protocol: mcpProtocol20251125,
			wantCode: -32601,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", test.body)
			if test.protocol != "" {
				request.Header.Set("MCP-Protocol-Version", test.protocol)
			}
			recorder := serveMCPPhase12Request(router, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if code := mcpPhase12ErrorCode(t, recorder); code != test.wantCode {
				t.Fatalf("error code = %d, want %d; body=%q", code, test.wantCode, recorder.Body.String())
			}
		})
	}

	notification := newMCPPhase12Request(http.MethodPost, "/custom-mcp", `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
	notification.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	recorder := serveMCPPhase12Request(router, notification)
	if recorder.Code != http.StatusAccepted || recorder.Body.Len() != 0 {
		t.Fatalf("notification status=%d body=%q, want 202 with empty body", recorder.Code, recorder.Body.String())
	}
}

func TestMCPPhase12ToolsListUsesAllowlistAndSecurityMetadata(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`)
	request.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	recorder := serveMCPPhase12Request(router, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response struct {
		Result struct {
			Tools []map[string]interface{} `json:"tools"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("unexpected MCP error: %#v; body=%q", response.Error, recorder.Body.String())
	}
	if len(response.Result.Tools) != 1 {
		t.Fatalf("tools = %#v, want exactly one allowlisted Tool", response.Result.Tools)
	}
	tool := response.Result.Tools[0]
	if tool["name"] != "sample" {
		t.Fatalf("tool name = %v, want sample", tool["name"])
	}
	if tool["title"] != "Sample Tool" || tool["description"] != "allowlisted sample" {
		t.Fatalf("Tool metadata = %#v", tool)
	}
	inputSchema, ok := tool["inputSchema"].(map[string]interface{})
	if !ok || inputSchema["type"] != "object" || inputSchema["additionalProperties"] != false {
		t.Fatalf("inputSchema = %#v", tool["inputSchema"])
	}
	securitySchemes, ok := tool["securitySchemes"].([]interface{})
	if !ok || len(securitySchemes) != 1 {
		t.Fatalf("securitySchemes = %#v, want one scheme", tool["securitySchemes"])
	}
	meta, ok := tool["_meta"].(map[string]interface{})
	if !ok || !reflect.DeepEqual(meta["securitySchemes"], tool["securitySchemes"]) {
		t.Fatalf("_meta.securitySchemes = %#v, top-level = %#v", meta["securitySchemes"], tool["securitySchemes"])
	}
}

func newMCPPhase12Definitions(t *testing.T) (string, map[string]interface{}) {
	t.Helper()
	dir := t.TempDir()
	writeHotReloadTestFile(t, filepath.Join(dir, "sample.js"), `JSON.stringify({ok:true,service:"Nyan8",items:[1,2,3]});`)
	writeHotReloadTestFile(t, filepath.Join(dir, "other.js"), `JSON.stringify({ok:true});`)
	writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), `({authenticated:false,forbidden:false});`)
	writeHotReloadTestFile(t, filepath.Join(dir, "sample-input.js"), `const nyanInputSchema={type:"object",properties:{},additionalProperties:false}; ({success:true,status:200,result:{}});`)
	writeHotReloadTestFile(t, filepath.Join(dir, "sample-output.js"), `const nyanOutputSchema={type:"object",properties:{ok:{type:"boolean"},service:{const:"Nyan8"},items:{type:"array",items:{type:"integer"}}},required:["ok","service","items"],additionalProperties:false}; ({success:true,status:200,result:{}});`)
	if err := os.Mkdir(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}

	securitySchemes := []interface{}{
		map[string]interface{}{"type": "oauth2", "scopes": []interface{}{"nyan8:read"}},
	}
	definitions := map[string]interface{}{
		"sample": map[string]interface{}{
			"script":          "./sample.js",
			"paramCheck":      "./sample-input.js",
			"outCheck":        "./sample-output.js",
			"title":           "Sample Tool",
			"description":     "allowlisted sample",
			"securitySchemes": securitySchemes,
			"annotations": map[string]interface{}{
				"readOnlyHint":    true,
				"destructiveHint": false,
				"openWorldHint":   false,
			},
		},
		"other": map[string]interface{}{
			"script":      "./other.js",
			"description": "not allowlisted",
		},
		"assets": map[string]interface{}{
			"type": "public",
			"path": "./public",
		},
		"oauth_authorization_server_metadata": map[string]interface{}{"description": "OAuth authorization server metadata"},
		"oauth_protected_resource_metadata":   map[string]interface{}{"description": "OAuth protected resource metadata"},
		"oauth_authorize":                     map[string]interface{}{"script": "./oauth-hook.js"},
		"oauth_token":                         map[string]interface{}{"script": "./oauth-hook.js"},
		"oauth_register":                      map[string]interface{}{"script": "./oauth-hook.js"},
		"oauth_admin_user":                    map[string]interface{}{"script": "./oauth-hook.js"},
		"oauth_verify_access":                 map[string]interface{}{"script": "./oauth-hook.js", "scopes": []interface{}{"nyan8:read"}},
		"custom-mcp": map[string]interface{}{
			"type":                       "mcp",
			"transport":                  "streamable_http",
			"protocolVersions":           []interface{}{mcpProtocol20251125, mcpProtocol20250618},
			"allowedOrigins":             []interface{}{"https://chatgpt.com"},
			"redirectURIAllowedPrefixes": []interface{}{"https://chatgpt.com/connector/oauth/"},
			"oauth": map[string]interface{}{
				"authorizationServerMetadata": "oauth_authorization_server_metadata",
				"protectedResourceMetadata":   "oauth_protected_resource_metadata",
				"authorize":                   "oauth_authorize",
				"token":                       "oauth_token",
				"register":                    "oauth_register",
				"adminUser":                   "oauth_admin_user",
				"verifyAccess":                "oauth_verify_access",
			},
			"tools":        []interface{}{"sample"},
			"instructions": "Phase 1-2 test server",
		},
	}
	return dir, definitions
}

func newMCPStdioTestConfig(t *testing.T) (*apiConfigLoadResult, *MCPServerConfig) {
	t.Helper()
	dir, definitions := newMCPPhase12Definitions(t)
	mcp := mcpPhase12Entry(t, definitions)
	mcp["transport"] = "stdio"
	delete(mcp, "allowedOrigins")
	delete(mcp, "redirectURIAllowedPrefixes")
	delete(mcp, "oauth")
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	server := loaded.Snapshot.MCPServers["custom-mcp"]
	if server == nil {
		t.Fatal("stdio MCP server was not loaded")
	}
	return loaded, server
}

func TestMCPStdioTransportConfiguration(t *testing.T) {
	t.Run("stdio only", func(t *testing.T) {
		loaded, server := newMCPStdioTestConfig(t)
		if !mcpSupportsTransport(server, "stdio") || mcpSupportsTransport(server, "streamable_http") {
			t.Fatalf("transport = %q", server.Transport)
		}
		selected, err := selectMCPStdioServer(loaded.Snapshot, "custom-mcp")
		if err != nil || selected != server {
			t.Fatalf("selected=%v error=%v", selected, err)
		}
		router := publishMCPPhase12Snapshot(t, loaded)
		response := serveMCPPhase12Request(router, newMCPPhase12Request(http.MethodPost, "/custom-mcp", mcpPhase12InitializeBody(mcpProtocol20251125)))
		if response.Code != http.StatusNotFound {
			t.Fatalf("stdio-only HTTP status=%d body=%q", response.Code, response.Body.String())
		}
	})

	t.Run("separate HTTP and stdio definitions share a Tool", func(t *testing.T) {
		dir, definitions := newMCPPhase12Definitions(t)
		definitions["local-mcp"] = map[string]interface{}{
			"type":      "mcp",
			"transport": "stdio",
			"tools":     []interface{}{"sample"},
		}
		loaded, err := loadMCPPhase12Config(dir, definitions)
		if err != nil {
			t.Fatal(err)
		}
		httpServer := loaded.Snapshot.MCPServers["custom-mcp"]
		stdioServer := loaded.Snapshot.MCPServers["local-mcp"]
		if !mcpSupportsTransport(httpServer, "streamable_http") || mcpSupportsTransport(httpServer, "stdio") {
			t.Fatalf("HTTP transport = %q", httpServer.Transport)
		}
		if !mcpSupportsTransport(stdioServer, "stdio") || mcpSupportsTransport(stdioServer, "streamable_http") {
			t.Fatalf("stdio transport = %q", stdioServer.Transport)
		}
		if len(httpServer.Tools) != 1 || len(stdioServer.Tools) != 1 || httpServer.Tools[0].API != "sample" || stdioServer.Tools[0].API != "sample" {
			t.Fatalf("shared Tools: HTTP=%#v stdio=%#v", httpServer.Tools, stdioServer.Tools)
		}
	})

	t.Run("multiple servers require selection", func(t *testing.T) {
		loaded, _ := newMCPStdioTestConfig(t)
		second := *loaded.Snapshot.MCPServers["custom-mcp"]
		second.Name = "second-mcp"
		second.Path = "/second-mcp"
		loaded.Snapshot.MCPServers[second.Name] = &second
		if _, err := selectMCPStdioServer(loaded.Snapshot, ""); err == nil || !strings.Contains(err.Error(), "--mcp-server") {
			t.Fatalf("multiple server selection error = %v", err)
		}
		selected, err := selectMCPStdioServer(loaded.Snapshot, "second-mcp")
		if err != nil || selected.Name != "second-mcp" {
			t.Fatalf("selected=%v error=%v", selected, err)
		}
	})

	tests := []struct {
		name      string
		transport interface{}
		legacy    bool
		want      string
	}{
		{name: "missing", want: "transport is required"},
		{name: "legacy field", legacy: true, want: "unknown field \"transports\""},
		{name: "unknown", transport: "socket", want: "unsupported MCP transport"},
		{name: "blank", transport: " ", want: "transport is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, definitions := newMCPPhase12Definitions(t)
			mcp := mcpPhase12Entry(t, definitions)
			delete(mcp, "transport")
			delete(mcp, "allowedOrigins")
			delete(mcp, "redirectURIAllowedPrefixes")
			delete(mcp, "oauth")
			if test.legacy {
				mcp["transports"] = []interface{}{"stdio"}
			}
			if test.transport != nil {
				mcp["transport"] = test.transport
			}
			_, err := loadMCPPhase12Config(dir, definitions)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestMCPOAuthStateDirectoryUsesRuntimeConfigRoot(t *testing.T) {
	initTestLogger()
	dir, definitions := newMCPPhase12Definitions(t)
	stateRoot := filepath.Join(t.TempDir(), "persistent-oauth")
	previousConfig := globalConfig
	globalConfig.OAuthStateRoot = stateRoot
	t.Cleanup(func() { globalConfig = previousConfig })
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stateRoot, "custom-mcp")
	if got := loaded.Snapshot.MCPServers["custom-mcp"].OAuth.StateDirectory; got != want {
		t.Fatalf("OAuth state directory=%q, want %q", got, want)
	}
}

func TestMCPStdioProtocolAndToolExecution(t *testing.T) {
	initTestLogger()
	loaded, server := newMCPStdioTestConfig(t)
	previousConfig := globalConfig
	globalConfig.Name = "Nyan8 stdio Test"
	t.Cleanup(func() { globalConfig = previousConfig })

	input := strings.Join([]string{
		mcpPhase12InitializeBody(mcpProtocol20251125),
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"name":"sample","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":"ping","method":"ping","params":{}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := serveMCPStdio(strings.NewReader(input), &output, loaded.Snapshot, server); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("response lines=%d, want 4; output=%q", len(lines), output.String())
	}
	responses := make(map[string]map[string]interface{})
	for _, line := range lines {
		var response map[string]interface{}
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("stdout contains non-JSON MCP data %q: %v", line, err)
		}
		responses[fmt.Sprint(response["id"])] = response
	}
	listResult := responses["list"]["result"].(map[string]interface{})
	tools := listResult["tools"].([]interface{})
	tool := tools[0].(map[string]interface{})
	if _, exists := tool["securitySchemes"]; exists {
		t.Fatalf("stdio tools/list exposed HTTP security metadata: %#v", tool)
	}
	if _, exists := tool["_meta"]; exists {
		t.Fatalf("stdio tools/list exposed HTTP _meta security metadata: %#v", tool)
	}
	callResult := responses["call"]["result"].(map[string]interface{})
	structured := callResult["structuredContent"].(map[string]interface{})
	if structured["ok"] != true || structured["service"] != "Nyan8" {
		t.Fatalf("stdio Tool result = %#v", callResult)
	}
}

func TestMCPStdioLifecycleAndInvalidMessages(t *testing.T) {
	initTestLogger()
	loaded, server := newMCPStdioTestConfig(t)
	input := strings.Join([]string{
		``,
		`{"jsonrpc":"2.0","id":"early","method":"tools/list","params":{}}`,
		`[{"jsonrpc":"2.0","id":"batch","method":"ping"}]`,
		`{"jsonrpc":"2.0","id":"duplicate","id":"duplicate2","method":"ping"}`,
		`{"jsonrpc":"2.0","id":"trailing","method":"ping"} {}`,
		mcpPhase12InitializeBody("2099-01-01"),
		mcpPhase12InitializeBody(mcpProtocol20251125),
		`{"jsonrpc":"2.0","id":"waiting","method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":"second","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{}}}`,
		`{"jsonrpc":"2.0","id":"missing-tool","method":"tools/call","params":{"name":"missing","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":"unknown","method":"unknown/method","params":{}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := serveMCPStdio(strings.NewReader(input), &output, loaded.Snapshot, server); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(output.String()), "\n") + 1; got != 11 {
		t.Fatalf("response count=%d, want 11; output=%q", got, output.String())
	}
	for _, wantCode := range []string{`"code":-32700`, `"code":-32002`, `"code":-32600`, `"code":-32601`, `"code":-32602`} {
		if !strings.Contains(output.String(), wantCode) {
			t.Errorf("output does not contain %s: %q", wantCode, output.String())
		}
	}
}

func TestMCPStdioToolValidationAndReservedArguments(t *testing.T) {
	initTestLogger()
	loaded, server := newMCPStdioTestConfig(t)
	tool := findMCPTool(server, "sample")
	if tool == nil {
		t.Fatal("sample Tool is missing")
	}
	tool.InputSchema = map[string]interface{}{"type": "object", "additionalProperties": true}
	principal := map[string]interface{}{"transport": "stdio"}
	if _, message := executeMCPTool(loaded.Snapshot, tool, map[string]interface{}{"mcp_principal": "spoofed"}, principal); message != "Tool arguments contain a reserved parameter." {
		t.Fatalf("reserved argument error = %q", message)
	}
	tool.InputSchema = map[string]interface{}{"type": "object", "required": []interface{}{"required_value"}}
	if _, message := executeMCPTool(loaded.Snapshot, tool, map[string]interface{}{}, principal); message != "Tool arguments do not match inputSchema." {
		t.Fatalf("schema argument error = %q", message)
	}
}

func TestMCPStdioRejectsOversizedMessage(t *testing.T) {
	initTestLogger()
	loaded, server := newMCPStdioTestConfig(t)
	input := strings.NewReader(strings.Repeat("x", maxMCPRequestBytes+2) + "\n")
	var output bytes.Buffer
	err := serveMCPStdio(input, &output, loaded.Snapshot, server)
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("oversize error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("oversize input produced output %q", output.String())
	}
}

func TestMCPStdioCommandProcessEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio child-process E2E in short mode")
	}
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "Nyan8-stdio-test")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stdio test binary: %v\n%s", err, output)
	}
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{
  "name":"Nyan8 stdio process test",
  "version":"test",
  "Port":-1,
  "bindAddress":"invalid bind address",
  "log":{"EnableLogging":false},
  "APIHotReload":{"Enabled":true,"Interval":"not-a-duration"},
  "websocket":{"maxConnections":128}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "echo.js"), []byte(`(function () {
  console.log("stdio-process-log");
  return {ok:true,transport:nyanAllParams.mcp_principal.transport};
})()`), 0o600); err != nil {
		t.Fatal(err)
	}
	apiPath := filepath.Join(dir, "api.json")
	apiJSON := `{
  "echo": {
    "script":"./echo.js",
    "title":"Echo",
    "description":"stdio process Tool"
  },
  "local_mcp": {
    "type":"mcp",
	"transport":"stdio",
    "tools":["echo"]
  },
  "background_job": {
    "type":"schedule",
    "script":"./echo.js",
    "trigger":{"type":"cron","value":"* * * * *"}
  },
  "background_socket": {
    "type":"ws_client",
    "script":"./echo.js",
    "connectURL":"ws://127.0.0.1:1"
  }
}`
	if err := os.WriteFile(apiPath, []byte(apiJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		mcpPhase12InitializeBody(mcpProtocol20251125),
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"name":"echo","arguments":{}}}`,
	}, "\n") + "\n"
	command := exec.Command(binaryPath,
		"--mcp-server", "local_mcp",
		"--api", apiPath,
		"--config", configPath,
	)
	command.Stdin = strings.NewReader(input)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("stdio command: %v\nstderr=%s\nstdout=%s", err, stderr.String(), stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout response lines=%d, want 3; stdout=%q stderr=%q", len(lines), stdout.String(), stderr.String())
	}
	for _, line := range lines {
		var response map[string]interface{}
		if err := json.Unmarshal([]byte(line), &response); err != nil || response["jsonrpc"] != "2.0" {
			t.Fatalf("stdout is not MCP-only: line=%q error=%v", line, err)
		}
	}
	if strings.Contains(stdout.String(), "Executable directory") || strings.Contains(stdout.String(), "stdio-process-log") {
		t.Fatalf("stdout was polluted: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Starting stdio MCP server: local_mcp") || !strings.Contains(stderr.String(), "stdio-process-log") {
		t.Fatalf("stderr did not receive startup/JavaScript logs: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "Starting schedule job") || strings.Contains(stderr.String(), "Starting WebSocket client") || strings.Contains(stderr.String(), "API hot reload enabled") {
		t.Fatalf("stdio mode started background services: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"transport":"stdio"`) {
		t.Fatalf("stdio principal was not passed to Tool: %q", stdout.String())
	}
}

func loadMCPPhase12Config(dir string, definitions map[string]interface{}) (*apiConfigLoadResult, error) {
	data, err := json.Marshal(definitions)
	if err != nil {
		return nil, err
	}
	apiPath := filepath.Join(dir, "api.json")
	if err := os.WriteFile(apiPath, data, 0o644); err != nil {
		return nil, err
	}
	return loadAPIConfigData(apiPath, dir, data)
}

func publishMCPPhase12Snapshot(t *testing.T, loaded *apiConfigLoadResult) *gin.Engine {
	t.Helper()
	previousSnapshot := currentAPISnapshot()
	previousConfig := globalConfig
	previousPaths := servicePaths
	previousLogger := logger
	initTestLogger()
	globalConfig.Name = "Nyan8 Phase 1-2 Test"
	servicePaths.API.Path = loaded.Snapshot.RootPath
	publishAPISnapshot(loaded.Snapshot)
	t.Cleanup(func() {
		publishAPISnapshot(previousSnapshot)
		globalConfig = previousConfig
		servicePaths = previousPaths
		logger = previousLogger
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.NoRoute(func(c *gin.Context) {
		if dispatchMCPOrOAuth(c) {
			return
		}
		c.Status(http.StatusNotFound)
	})
	return router
}

func newMCPPhase12Request(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, "https://nyan8.test"+path, strings.NewReader(body))
	request.Host = "nyan8.test"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	return request
}

func serveMCPPhase12Request(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func mcpPhase12InitializeBody(version string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":"initialize","method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"phase12-test","version":"1.0"},"_meta":{}}}`, version)
}

func assertMCPPhase12InitializeResponse(t *testing.T, recorder *httptest.ResponseRecorder, version string) {
	t.Helper()
	var response struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("unexpected MCP error: %#v; body=%q", response.Error, recorder.Body.String())
	}
	if response.Result.ProtocolVersion != version {
		t.Fatalf("protocolVersion = %q, want %q", response.Result.ProtocolVersion, version)
	}
	if got := recorder.Header().Get("MCP-Protocol-Version"); got != version {
		t.Fatalf("MCP-Protocol-Version header = %q, want %q", got, version)
	}
}

func mcpPhase12ErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) int {
	t.Helper()
	var response struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil {
		t.Fatalf("response has no JSON-RPC error: %q", recorder.Body.String())
	}
	return response.Error.Code
}

func mcpPhase12Entry(t *testing.T, definitions map[string]interface{}) map[string]interface{} {
	t.Helper()
	mcp, ok := definitions["custom-mcp"].(map[string]interface{})
	if !ok {
		t.Fatalf("custom-mcp definition type = %T", definitions["custom-mcp"])
	}
	return mcp
}

func cloneMCPPhase12Map(t *testing.T, source map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]interface{}
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestOAuthPhase3Argon2idHashAndVerify(t *testing.T) {
	hash, err := argon2idHash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$", argon2Memory, argon2Iterations, argon2Parallelism)
	if !strings.HasPrefix(hash, wantPrefix) {
		t.Fatalf("hash = %q, want PHC prefix %q", hash, wantPrefix)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" || parts[4] == "" || parts[5] == "" {
		t.Fatalf("hash is not a complete Argon2id PHC string: %q", hash)
	}
	if !argon2idVerify("correct horse battery staple", hash) {
		t.Fatal("correct password did not verify")
	}
	if argon2idVerify("wrong password", hash) {
		t.Fatal("wrong password verified")
	}
	if argon2idVerify("correct horse battery staple", hash+"x") {
		t.Fatal("tampered PHC string verified")
	}
	if argon2idVerify("correct horse battery staple", "$argon2id$v=19$m=1,t=1,p=1$bad$bad") {
		t.Fatal("unsupported Argon2 parameters verified")
	}
	if _, err := argon2idHash(""); err == nil {
		t.Fatal("empty password was hashed")
	}
}

func TestOAuthPhase3StatePrimitivesAndSafety(t *testing.T) {
	root := filepath.Join(t.TempDir(), "oauth-state")
	key := "tokens/access.json"
	want := `{"token":"one","active":true}`
	if err := oauthWriteState(root, key, want); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, filepath.FromSlash(key))
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %04o, want 0600", info.Mode().Perm())
	}
	got, err := oauthReadState(root, key)
	if err != nil || got != want || !json.Valid([]byte(got)) {
		t.Fatalf("read value=%q err=%v, want valid JSON %q", got, err, want)
	}
	if err := oauthDeleteState(root, key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("deleted state stat error = %v, want not-exist", err)
	}

	consumeKey := "codes/authorization.json"
	consumeValue := `{"code":"single-use"}`
	if err := oauthWriteState(root, consumeKey, consumeValue); err != nil {
		t.Fatal(err)
	}
	consumed, err := oauthConsumeState(root, consumeKey)
	if err != nil || consumed != consumeValue {
		t.Fatalf("consume value=%q err=%v, want %q", consumed, err, consumeValue)
	}
	if _, err := oauthReadState(root, consumeKey); !os.IsNotExist(err) {
		t.Fatalf("consumed state read error = %v, want not-exist", err)
	}

	if err := oauthWriteState(root, "invalid.json", "not JSON"); err == nil {
		t.Fatal("invalid JSON state was written")
	}
	if err := oauthWriteState(root, "duplicate.json", `{"kind":"one","kind":"two"}`); err == nil {
		t.Fatal("state with duplicate JSON object keys was written")
	}
	invalidPath := filepath.Join(root, "invalid-on-disk.json")
	if err := os.WriteFile(invalidPath, []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := oauthReadState(root, "invalid-on-disk.json"); err == nil {
		t.Fatal("invalid JSON state file was read")
	}
	duplicatePath := filepath.Join(root, "duplicate-on-disk.json")
	if err := os.WriteFile(duplicatePath, []byte(`{"kind":"one","kind":"two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := oauthReadState(root, "duplicate-on-disk.json"); err == nil {
		t.Fatal("state file with duplicate JSON object keys was read")
	}
	if runtime.GOOS != "windows" {
		broadPath := filepath.Join(root, "broad.json")
		if err := os.WriteFile(broadPath, []byte(`{"ok":true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := oauthReadState(root, "broad.json"); err == nil {
			t.Fatal("overly broad state file permissions were accepted")
		}
	}

	for _, invalidKey := range []string{"../escape.json", "nested/../../escape.json", "no-json-extension"} {
		if err := oauthWriteState(root, invalidKey, `{"ok":true}`); err == nil {
			t.Errorf("invalid state key %q was accepted", invalidKey)
		}
	}
	absoluteKey := filepath.Join(t.TempDir(), "absolute.json")
	if err := oauthWriteState(root, absoluteKey, `{"ok":true}`); err == nil {
		t.Errorf("absolute state key %q was accepted", absoluteKey)
	}

	testOAuthPhase3SymlinkRejection(t, root)
}

func TestOAuthPhase3ConcurrentConsumeSucceedsOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "oauth-state")
	const key = "codes/once.json"
	const value = `{"nonce":"consume-once"}`
	if err := oauthWriteState(root, key, value); err != nil {
		t.Fatal(err)
	}

	type consumeResult struct {
		value string
		err   error
	}
	const consumers = 24
	start := make(chan struct{})
	results := make(chan consumeResult, consumers)
	var wait sync.WaitGroup
	for index := 0; index < consumers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			consumed, err := oauthConsumeState(root, key)
			results <- consumeResult{value: consumed, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	for result := range results {
		if result.err == nil {
			successes++
			if result.value != value {
				t.Errorf("consumed value = %q, want %q", result.value, value)
			}
			continue
		}
		if !os.IsNotExist(result.err) {
			t.Errorf("losing consumer error = %v, want not-exist", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful consumers = %d, want 1", successes)
	}
}

func TestOAuthPhase3RuntimeExcludesGeneralNyanCapabilities(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "runtime-check.js")
	writeHotReloadTestFile(t, scriptPath, `({
  nyanHostExec: typeof nyanHostExec,
  nyanGetAPI: typeof nyanGetAPI,
  nyanGetFile: typeof nyanGetFile,
  nyanSendMail: typeof nyanSendMail,
  nyanOAuthRead: typeof nyanOAuthRead,
  nyanOAuthWrite: typeof nyanOAuthWrite,
  nyanOAuthConsume: typeof nyanOAuthConsume
});`)
	mcp := &MCPServerConfig{OAuth: MCPOAuthConfig{StateDirectory: filepath.Join(dir, "state")}}
	value, err := runOAuthHookJavaScript(nil, mcp, scriptPath, map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("runtime result type = %T", value)
	}
	for _, forbidden := range []string{"nyanHostExec", "nyanGetAPI", "nyanGetFile", "nyanSendMail"} {
		if result[forbidden] != "undefined" {
			t.Errorf("%s typeof = %v, want undefined", forbidden, result[forbidden])
		}
	}
	for _, allowed := range []string{"nyanOAuthRead", "nyanOAuthWrite", "nyanOAuthConsume"} {
		if result[allowed] != "function" {
			t.Errorf("%s typeof = %v, want function", allowed, result[allowed])
		}
	}
}

func TestOAuthPhase3UsesAPINameRoutesAndRequestOrigin(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)

	metadata := newMCPPhase12Request(http.MethodGet, "/oauth_authorization_server_metadata", "")
	metadata.Host = "Connector.EXAMPLE.test:8443"
	recorder := serveMCPPhase12Request(router, metadata)
	if recorder.Code != http.StatusOK {
		t.Fatalf("metadata status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	var metadataBody map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &metadataBody); err != nil {
		t.Fatal(err)
	}
	if metadataBody["issuer"] != "https://connector.example.test:8443" ||
		metadataBody["authorization_endpoint"] != "https://connector.example.test:8443/oauth_authorize" ||
		metadataBody["token_endpoint"] != "https://connector.example.test:8443/oauth_token" ||
		metadataBody["registration_endpoint"] != "https://connector.example.test:8443/oauth_register" {
		t.Fatalf("custom metadata = %#v", metadataBody)
	}
	resourceMetadata := newMCPPhase12Request(http.MethodGet, "/?api=oauth_protected_resource_metadata", "")
	resourceRecorder := serveMCPPhase12Request(router, resourceMetadata)
	if resourceRecorder.Code != http.StatusOK {
		t.Fatalf("resource metadata status=%d body=%q", resourceRecorder.Code, resourceRecorder.Body.String())
	}
	var resourceBody map[string]interface{}
	if err := json.Unmarshal(resourceRecorder.Body.Bytes(), &resourceBody); err != nil {
		t.Fatal(err)
	}
	if resourceBody["resource"] != "https://nyan8.test/custom-mcp" {
		t.Fatalf("query resource metadata = %#v", resourceBody)
	}

	for _, route := range []string{"/oauth_authorize", "/oauth_token", "/oauth_register", "/oauth_admin_user"} {
		recorder = serveMCPPhase12Request(router, newMCPPhase12Request(http.MethodPut, route, ""))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("configured route %s status=%d, want 405; body=%q", route, recorder.Code, recorder.Body.String())
		}
	}
	for _, staleRoute := range []string{"/.well-known/oauth-authorization-server", "/oauth/authorize", "/oauth/token", "/oauth/register", "/oauth/admin/users"} {
		recorder = serveMCPPhase12Request(router, newMCPPhase12Request(http.MethodGet, staleRoute, ""))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("stale route %s status=%d, want 404; body=%q", staleRoute, recorder.Code, recorder.Body.String())
		}
	}
}

func TestOAuthPhase3HTTPBoundaryValidation(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)

	tests := []struct {
		name       string
		request    func() *http.Request
		wantStatus int
	}{
		{
			name: "Host",
			request: func() *http.Request {
				request := newMCPPhase12Request(http.MethodGet, "/oauth_authorization_server_metadata", "")
				request.Host = "bad_host.example.test"
				return request
			},
			wantStatus: http.StatusMisdirectedRequest,
		},
		{
			name: "Origin",
			request: func() *http.Request {
				request := newMCPPhase12Request(http.MethodGet, "/oauth_authorization_server_metadata", "")
				request.Header.Set("Origin", "https://attacker.example.test")
				return request
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "method",
			request: func() *http.Request {
				return newMCPPhase12Request(http.MethodGet, "/oauth_token", "")
			},
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name: "Content-Type",
			request: func() *http.Request {
				return newMCPPhase12Request(http.MethodPost, "/oauth_token", `{"grant_type":"authorization_code"}`)
			},
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "body limit",
			request: func() *http.Request {
				request := newMCPPhase12Request(http.MethodPost, "/oauth_token", strings.Repeat("x", int(maxMCPRequestBytes)+1))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return request
			},
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveMCPPhase12Request(router, test.request())
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d, want %d; body=%q", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestOAuthPhase3HookRequestAndResponseContract(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), oauthPhase3EchoHookScript())
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)

	t.Run("query headers and cookie", func(t *testing.T) {
		request := newMCPPhase12Request(http.MethodGet, "/oauth_authorize?prompt=login", "")
		request.Header.Set("Authorization", "Bearer request-token")
		request.AddCookie(&http.Cookie{Name: "session", Value: "cookie-value"})
		body := assertOAuthPhase3EchoResponse(t, serveMCPPhase12Request(router, request), "oauthAuthorize")
		if got := oauthPhase3NestedFirstString(body, "query", "prompt"); got != "login" {
			t.Fatalf("query prompt=%q, want login", got)
		}
		if body["authorization"] != "Bearer request-token" || body["cookie"] != "cookie-value" {
			t.Fatalf("header/cookie echo = %#v", body)
		}
		if body["path"] != "/oauth_authorize" {
			t.Fatalf("authorize path=%v, want /oauth_authorize", body["path"])
		}
	})

	t.Run("form", func(t *testing.T) {
		request := newMCPPhase12Request(http.MethodPost, "/?api=oauth_token&source=query", "grant_type=authorization_code&code=abc123")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		body := assertOAuthPhase3EchoResponse(t, serveMCPPhase12Request(router, request), "oauthToken")
		if got := oauthPhase3NestedFirstString(body, "form", "grant_type"); got != "authorization_code" {
			t.Fatalf("form grant_type=%q, want authorization_code", got)
		}
		if got := oauthPhase3NestedFirstString(body, "form", "code"); got != "abc123" {
			t.Fatalf("form code=%q, want abc123", got)
		}
		if body["path"] != "/oauth_token" {
			t.Fatalf("query-form token path=%v, want /oauth_token", body["path"])
		}
	})

	t.Run("JSON", func(t *testing.T) {
		request := newMCPPhase12Request(http.MethodPost, "/oauth_register", `{"client_name":"phase3-client"}`)
		body := assertOAuthPhase3EchoResponse(t, serveMCPPhase12Request(router, request), "oauthRegister")
		jsonBody, ok := body["json"].(map[string]interface{})
		if !ok || jsonBody["client_name"] != "phase3-client" {
			t.Fatalf("JSON body echo = %#v", body["json"])
		}
		if body["path"] != "/oauth_register" {
			t.Fatalf("register path=%v, want /oauth_register", body["path"])
		}
	})
}

func TestOAuthPhase3RejectsUnsafeHookResponseHeaders(t *testing.T) {
	tests := []struct {
		name      string
		headersJS string
	}{
		{name: "unapproved header", headersJS: `{"X-Not-Allowed":"value"}`},
		{name: "CRLF value", headersJS: `{"Location":"https://client.example.test/callback\r\nInjected: true"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, definitions := newMCPPhase12Definitions(t)
			script := fmt.Sprintf(`({status:200,contentType:"application/json",headers:%s,body:{ok:true}});`, test.headersJS)
			writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), script)
			loaded, err := loadMCPPhase12Config(dir, definitions)
			if err != nil {
				t.Fatal(err)
			}
			router := publishMCPPhase12Snapshot(t, loaded)
			request := newMCPPhase12Request(http.MethodPost, "/oauth_register", `{}`)
			recorder := serveMCPPhase12Request(router, request)
			if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "invalid response") {
				t.Fatalf("status=%d body=%q, want rejected hook response", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestOAuthPhase3AdminBasicCredential(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), `(function () {
  var authorized = nyanOAuthAdminAuthorized(nyanAllParams.authorization);
  return {
    status: authorized ? 201 : 401,
    contentType: "application/json",
    headers: {"Cache-Control": "no-store"},
    body: {authorized: authorized}
  };
})()`)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	globalConfig.OAuthAdmin = OAuthAdminConfig{Username: "operator", Password: "correct-password"}

	tests := []struct {
		name       string
		username   string
		password   string
		withBasic  bool
		wantStatus int
		wantAuth   bool
	}{
		{name: "correct", username: "operator", password: "correct-password", withBasic: true, wantStatus: http.StatusCreated, wantAuth: true},
		{name: "wrong username", username: "attacker", password: "correct-password", withBasic: true, wantStatus: http.StatusUnauthorized},
		{name: "wrong password", username: "operator", password: "wrong-password", withBasic: true, wantStatus: http.StatusUnauthorized},
		{name: "missing", wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := newMCPPhase12Request(http.MethodPost, "/oauth_admin_user", `{}`)
			if test.withBasic {
				request.SetBasicAuth(test.username, test.password)
			}
			recorder := serveMCPPhase12Request(router, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d, want %d; body=%q", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			var body map[string]interface{}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["authorized"] != test.wantAuth {
				t.Fatalf("authorized=%v, want %t", body["authorized"], test.wantAuth)
			}
		})
	}
}

func testOAuthPhase3SymlinkRejection(t *testing.T, root string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Log("symlink rejection checks skipped on Windows")
		return
	}
	outside := t.TempDir()
	nestedLink := filepath.Join(root, "linked-directory")
	if err := os.Symlink(outside, nestedLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := oauthWriteState(root, "linked-directory/state.json", `{"ok":true}`); err == nil {
		t.Error("state write followed a directory symlink")
	}

	target := filepath.Join(outside, "target.json")
	if err := os.WriteFile(target, []byte(`{"outside":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(root, "linked-file.json")
	if err := os.Symlink(target, fileLink); err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "read", run: func() error { _, err := oauthReadState(root, "linked-file.json"); return err }},
		{name: "write", run: func() error { return oauthWriteState(root, "linked-file.json", `{"changed":true}`) }},
		{name: "delete", run: func() error { return oauthDeleteState(root, "linked-file.json") }},
		{name: "consume", run: func() error { _, err := oauthConsumeState(root, "linked-file.json"); return err }},
	}
	for _, operation := range operations {
		if err := operation.run(); err == nil {
			t.Errorf("%s accepted a symlink state file", operation.name)
		}
	}

	realRoot := filepath.Join(outside, "real-root")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(t.TempDir(), "state-root-link")
	if err := os.Symlink(realRoot, rootLink); err != nil {
		t.Fatal(err)
	}
	if err := oauthWriteState(rootLink, "state.json", `{"ok":true}`); err == nil {
		t.Error("state write accepted a symlink root")
	}
}

func oauthPhase3EchoHookScript() string {
	return `(function () {
  var requestHeaders = nyanAllParams.headers || {};
  var requestCookies = nyanAllParams.cookies || {};
  return {
    status: 207,
    contentType: "application/json; charset=utf-8",
    headers: {
      "Cache-Control": "no-store",
      "Pragma": "no-cache",
      "Location": "https://client.example.test/callback",
      "Set-Cookie": "phase3=ok; Secure; HttpOnly; SameSite=Lax"
    },
    body: {
      hook: nyanAllParams.oauth_hook,
      method: nyanAllParams.method,
      path: nyanAllParams.path,
      query: nyanAllParams.query || null,
      form: nyanAllParams.form || null,
      json: nyanAllParams.body || null,
      authorization: requestHeaders.Authorization || "",
      cookie: requestCookies.session || ""
    }
  };
})()`
}

func assertOAuthPhase3EchoResponse(t *testing.T, recorder *httptest.ResponseRecorder, hook string) map[string]interface{} {
	t.Helper()
	if recorder.Code != http.StatusMultiStatus {
		t.Fatalf("status=%d, want %d; body=%q", recorder.Code, http.StatusMultiStatus, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type=%q, want application/json", contentType)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Location") != "https://client.example.test/callback" {
		t.Fatalf("response headers = %#v", recorder.Header())
	}
	if !strings.Contains(recorder.Header().Get("Set-Cookie"), "phase3=ok") {
		t.Fatalf("Set-Cookie=%q", recorder.Header().Get("Set-Cookie"))
	}
	var body map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["hook"] != hook {
		t.Fatalf("hook=%v, want %s; body=%#v", body["hook"], hook, body)
	}
	return body
}

func oauthPhase3NestedFirstString(body map[string]interface{}, group, key string) string {
	nested, _ := body[group].(map[string]interface{})
	values, _ := nested[key].([]interface{})
	if len(values) == 0 {
		return ""
	}
	value, _ := values[0].(string)
	return value
}

func TestOAuthPhase4EndToEndWithRealHooksAndTool(t *testing.T) {
	router, stateDirectory := newOAuthPhase4Fixture(t)
	const (
		adminUsername = "phase4-operator"
		adminPassword = "Phase4OperatorPassword123"
		username      = "phase4.user"
		password      = "Phase4UserPassword123"
		redirectURI   = "https://chatgpt.com/connector/oauth/phase4"
		resource      = "https://nyan8.stamps.necomori.asia/server_mcp_http"
	)
	globalConfig.OAuthAdmin = OAuthAdminConfig{Username: adminUsername, Password: adminPassword}

	adminRequest := newOAuthPhase4Request(http.MethodPost, "/oauth_admin_user", fmt.Sprintf(`{"username":%q,"password":%q}`, username, password), "application/json")
	adminRequest.SetBasicAuth(adminUsername, adminPassword)
	adminResponse := serveMCPPhase12Request(router, adminRequest)
	if adminResponse.Code != http.StatusCreated {
		t.Fatalf("admin bootstrap status=%d, want %d; body=%q", adminResponse.Code, http.StatusCreated, adminResponse.Body.String())
	}
	adminBody := oauthPhase4JSONBody(t, adminResponse)
	if adminBody["username"] != username || adminBody["created"] != true {
		t.Fatalf("admin bootstrap body=%#v", adminBody)
	}
	assertOAuthPhase4StateSecretsAbsent(t, stateDirectory, password, adminPassword)

	registerBody := `{"redirect_uris":["https://chatgpt.com/connector/oauth/phase4"],"client_name":"Phase 4 integration client","token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"scope":"nyan8:read"}`
	registerResponse := serveMCPPhase12Request(router, newOAuthPhase4Request(http.MethodPost, "/oauth_register", registerBody, "application/json"))
	if registerResponse.Code != http.StatusCreated {
		t.Fatalf("DCR status=%d, want %d; body=%q", registerResponse.Code, http.StatusCreated, registerResponse.Body.String())
	}
	registration := oauthPhase4JSONBody(t, registerResponse)
	clientID, _ := registration["client_id"].(string)
	if !strings.HasPrefix(clientID, "cli_") || len(clientID) != len("cli_")+32 {
		t.Fatalf("DCR client_id=%q, want cli_ plus 32 base64url characters", clientID)
	}
	if registration["scope"] != "nyan8:read" || registration["token_endpoint_auth_method"] != "none" {
		t.Fatalf("DCR response=%#v", registration)
	}
	grantTypes, _ := registration["grant_types"].([]interface{})
	if len(grantTypes) != 2 || grantTypes[0] != "authorization_code" || grantTypes[1] != "refresh_token" {
		t.Fatalf("DCR grant_types=%#v", registration["grant_types"])
	}
	assertOAuthPhase4StateSecretsAbsent(t, stateDirectory, password, adminPassword)

	verifier := strings.Repeat("v", 64)
	code, csrf := oauthPhase4Authorize(t, router, stateDirectory, clientID, redirectURI, resource, "phase4-state-one", verifier, username, password)
	assertOAuthPhase4StateSecretsAbsent(t, stateDirectory, password, adminPassword, csrf, code)

	wrongVerifier := strings.Repeat("x", 64)
	invalidPKCE := serveMCPPhase12Request(router, newOAuthPhase4TokenRequest(code, clientID, redirectURI, resource, wrongVerifier))
	if invalidPKCE.Code != http.StatusBadRequest {
		t.Fatalf("invalid PKCE status=%d, want %d; body=%q", invalidPKCE.Code, http.StatusBadRequest, invalidPKCE.Body.String())
	}
	if body := oauthPhase4JSONBody(t, invalidPKCE); body["error"] != "invalid_grant" {
		t.Fatalf("invalid PKCE body=%#v", body)
	}
	assertOAuthPhase4StateSecretsAbsent(t, stateDirectory, password, adminPassword, csrf, code, wrongVerifier)

	tokenResponse := serveMCPPhase12Request(router, newOAuthPhase4TokenRequest(code, clientID, redirectURI, resource, verifier))
	accessToken := oauthPhase4AccessToken(t, tokenResponse)
	tokenBody := oauthPhase4JSONBody(t, tokenResponse)
	refreshToken, _ := tokenBody["refresh_token"].(string)
	if !strings.HasPrefix(refreshToken, "rt_") || len(refreshToken) != len("rt_")+43 {
		t.Fatalf("authorization-code token response has invalid refresh_token: %#v", tokenBody)
	}
	assertOAuthPhase4StateSecretsAbsent(t, stateDirectory, password, adminPassword, csrf, code, accessToken, refreshToken)

	reusedCode := serveMCPPhase12Request(router, newOAuthPhase4TokenRequest(code, clientID, redirectURI, resource, verifier))
	if reusedCode.Code != http.StatusBadRequest {
		t.Fatalf("reused code status=%d, want %d; body=%q", reusedCode.Code, http.StatusBadRequest, reusedCode.Body.String())
	}
	if body := oauthPhase4JSONBody(t, reusedCode); body["error"] != "invalid_grant" {
		t.Fatalf("reused code body=%#v", body)
	}

	refreshResponse := serveMCPPhase12Request(router, newOAuthPhase4RefreshRequest(refreshToken, clientID, resource, ""))
	accessToken = oauthPhase4AccessToken(t, refreshResponse)
	refreshBody := oauthPhase4JSONBody(t, refreshResponse)
	rotatedRefreshToken, _ := refreshBody["refresh_token"].(string)
	if !strings.HasPrefix(rotatedRefreshToken, "rt_") || rotatedRefreshToken == refreshToken {
		t.Fatalf("refresh-token rotation response=%#v", refreshBody)
	}
	assertOAuthPhase4StateSecretsAbsent(t, stateDirectory, password, adminPassword, refreshToken, rotatedRefreshToken, accessToken)

	toolBody := `{"jsonrpc":"2.0","id":"phase4-tool","method":"tools/call","params":{"name":"mcp_sample","arguments":{}}}`
	unauthenticatedRequest := newOAuthPhase4Request(http.MethodPost, "/server_mcp_http", toolBody, "application/json")
	unauthenticatedRequest.Header.Set("Accept", "application/json, text/event-stream")
	unauthenticatedRequest.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	unauthenticatedResponse := serveMCPPhase12Request(router, unauthenticatedRequest)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated Tool status=%d, want %d; body=%q", unauthenticatedResponse.Code, http.StatusUnauthorized, unauthenticatedResponse.Body.String())
	}
	wantChallenge := `Bearer resource_metadata="https://nyan8.stamps.necomori.asia/oauth_protected_resource_metadata", scope="nyan8:read", error="invalid_token", error_description="Authentication required."`
	if got := unauthenticatedResponse.Header().Get("WWW-Authenticate"); got != wantChallenge {
		t.Fatalf("HTTP WWW-Authenticate=%q, want %q", got, wantChallenge)
	}
	unauthenticatedBody := oauthPhase4JSONBody(t, unauthenticatedResponse)
	result, _ := unauthenticatedBody["result"].(map[string]interface{})
	meta, _ := result["_meta"].(map[string]interface{})
	challenges, _ := meta["mcp/www_authenticate"].([]interface{})
	if len(challenges) != 1 || challenges[0] != wantChallenge {
		t.Fatalf("MCP challenges=%#v, want %#v", challenges, []interface{}{wantChallenge})
	}

	authenticatedRequest := newOAuthPhase4Request(http.MethodPost, "/server_mcp_http", toolBody, "application/json")
	authenticatedRequest.Header.Set("Accept", "application/json, text/event-stream")
	authenticatedRequest.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	authenticatedRequest.Header.Set("Authorization", "Bearer "+accessToken)
	authenticatedRequest.Header.Set("X-Phase4-Raw", "phase4-raw-header-secret")
	authenticatedResponse := serveMCPPhase12Request(router, authenticatedRequest)
	if authenticatedResponse.Code != http.StatusOK {
		t.Fatalf("authenticated Tool status=%d, want %d; body=%q", authenticatedResponse.Code, http.StatusOK, authenticatedResponse.Body.String())
	}

	reusedRefresh := serveMCPPhase12Request(router, newOAuthPhase4RefreshRequest(refreshToken, clientID, resource, ""))
	if reusedRefresh.Code != http.StatusBadRequest || oauthPhase4JSONBody(t, reusedRefresh)["error"] != "invalid_grant" {
		t.Fatalf("reused refresh token status=%d body=%q", reusedRefresh.Code, reusedRefresh.Body.String())
	}
	revokedFamilyRequest := newOAuthPhase4Request(http.MethodPost, "/server_mcp_http", toolBody, "application/json")
	revokedFamilyRequest.Header.Set("Accept", "application/json, text/event-stream")
	revokedFamilyRequest.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	revokedFamilyRequest.Header.Set("Authorization", "Bearer "+accessToken)
	revokedFamilyResponse := serveMCPPhase12Request(router, revokedFamilyRequest)
	if revokedFamilyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("refresh-token family replay did not revoke access token: status=%d body=%q", revokedFamilyResponse.Code, revokedFamilyResponse.Body.String())
	}
	authenticatedBody := oauthPhase4JSONBody(t, authenticatedResponse)
	authenticatedResult, _ := authenticatedBody["result"].(map[string]interface{})
	structured, _ := authenticatedResult["structuredContent"].(map[string]interface{})
	if structured["ok"] != true || structured["service"] != "Nyan8" || !reflect.DeepEqual(structured["items"], []interface{}{float64(1), float64(2), float64(3)}) {
		t.Fatalf("structuredContent=%#v", structured)
	}
	if authenticatedResult["isError"] != false {
		t.Fatalf("authenticated Tool result=%#v", authenticatedResult)
	}

	parallelVerifier := strings.Repeat("p", 64)
	parallelCode, parallelCSRF := oauthPhase4Authorize(t, router, stateDirectory, clientID, redirectURI, resource, "phase4-state-parallel", parallelVerifier, username, password)
	parallelToken := oauthPhase4ConcurrentTokenExchange(t, router, parallelCode, clientID, redirectURI, resource, parallelVerifier)
	assertOAuthPhase4StateSecretsAbsent(t, stateDirectory, password, adminPassword, csrf, code, accessToken, parallelCSRF, parallelCode, parallelToken, "phase4-raw-header-secret")
}

func loadOAuthPhase4TestConfig(t *testing.T, stateDirectory string, serverScopes, toolScopes []string) *apiConfigLoadResult {
	t.Helper()
	if len(serverScopes) == 0 {
		serverScopes = []string{"nyan8:read"}
	}
	if len(toolScopes) == 0 {
		toolScopes = []string{"nyan8:read"}
	}
	dir := t.TempDir()
	hookSourcePath := strings.TrimSpace(os.Getenv("NYAN8_OAUTH_HOOK_TEST_PATH"))
	if hookSourcePath == "" {
		t.Skip("OAuth policy E2E requires NYAN8_OAUTH_HOOK_TEST_PATH")
	}
	hookSource, err := os.ReadFile(hookSourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oauth_policy_fixture.js"), hookSource, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp_sample.js"), []byte(`({ok: true, service: "Nyan8", items: [1, 2, 3]});`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp_sample_input.js"), []byte(`const nyanInputSchema={type:"object",properties:{},additionalProperties:false}; ({success:true,status:200,result:{}});`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp_sample_output.js"), []byte(`const nyanOutputSchema={type:"object",properties:{ok:{type:"boolean"},service:{const:"Nyan8"},items:{type:"array",items:{type:"integer"}}},required:["ok","service","items"],additionalProperties:false}; ({success:true,status:200,result:{}});`), 0o600); err != nil {
		t.Fatal(err)
	}
	definitions := map[string]interface{}{
		"mcp_sample": map[string]interface{}{
			"script":          "./mcp_sample.js",
			"paramCheck":      "./mcp_sample_input.js",
			"outCheck":        "./mcp_sample_output.js",
			"title":           "Nyan8 test data",
			"description":     "Returns fixed data for MCP tests.",
			"websocket":       false,
			"securitySchemes": []interface{}{map[string]interface{}{"type": "oauth2", "scopes": toolScopes}},
			"annotations":     map[string]interface{}{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
		},
		"oauth_authorization_server_metadata": map[string]interface{}{"description": "OAuth authorization server metadata"},
		"oauth_protected_resource_metadata":   map[string]interface{}{"description": "OAuth protected resource metadata"},
		"oauth_authorize":                     map[string]interface{}{"script": "./oauth_policy_fixture.js"},
		"oauth_token":                         map[string]interface{}{"script": "./oauth_policy_fixture.js"},
		"oauth_register":                      map[string]interface{}{"script": "./oauth_policy_fixture.js"},
		"oauth_admin_user":                    map[string]interface{}{"script": "./oauth_policy_fixture.js"},
		"oauth_verify_access":                 map[string]interface{}{"script": "./oauth_policy_fixture.js", "scopes": serverScopes},
		"server_mcp_http": map[string]interface{}{
			"type":                       "mcp",
			"transport":                  "streamable_http",
			"protocolVersions":           []string{mcpProtocol20251125, mcpProtocol20250618},
			"allowedOrigins":             []string{"https://chatgpt.com", "https://platform.openai.com"},
			"redirectURIAllowedPrefixes": []string{"https://chatgpt.com/connector/oauth/"},
			"rateLimit":                  map[string]interface{}{"requests": 120, "window": "1m"},
			"maxConcurrent":              8,
			"oauth": map[string]interface{}{
				"authorizationServerMetadata": "oauth_authorization_server_metadata",
				"protectedResourceMetadata":   "oauth_protected_resource_metadata",
				"authorize":                   "oauth_authorize",
				"token":                       "oauth_token",
				"register":                    "oauth_register",
				"adminUser":                   "oauth_admin_user",
				"verifyAccess":                "oauth_verify_access",
			},
			"tools":        []interface{}{"mcp_sample"},
			"instructions": "Nyan8 MCP test server.",
		},
	}
	data, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	apiPath := filepath.Join(dir, "api.json")
	if err := os.WriteFile(apiPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := readAPIConfigFile(apiPath, dir)
	if err != nil {
		t.Fatalf("load OAuth test fixture: %v", err)
	}
	mcp := loaded.Snapshot.MCPServers["server_mcp_http"]
	if stateDirectory != "" && mcp != nil {
		mcp.OAuth.StateDirectory = stateDirectory
	}
	return loaded
}

func newOAuthPhase4Fixture(t *testing.T) (http.Handler, string) {
	t.Helper()
	stateDirectory := filepath.Join(t.TempDir(), "oauth-state")
	loaded := loadOAuthPhase4TestConfig(t, stateDirectory, nil, nil)
	mcp := loaded.Snapshot.MCPServers["server_mcp_http"]
	if mcp == nil || mcp.OAuth.StateDirectory == "" {
		t.Fatalf("fixture MCP=%v", mcp)
	}
	stateDirectory = mcp.OAuth.StateDirectory
	router := publishMCPPhase12Snapshot(t, loaded)

	guardPath := filepath.Join(t.TempDir(), "phase4_tool_argument_guard.js")
	writeHotReloadTestFile(t, guardPath, `(function () {
  var serialized = JSON.stringify(nyanAllParams);
  var names = Object.keys(nyanAllParams);
  for (var index = 0; index < names.length; index += 1) {
    var normalized = names[index].toLowerCase();
    if (normalized === "authorization" || normalized === "headers" || normalized.indexOf("_headers") === 0) {
      throw new Error("HTTP authorization or headers reached the Tool backing API");
    }
  }
  if (serialized.indexOf("Bearer ") >= 0 || serialized.indexOf("phase4-raw-header-secret") >= 0) {
    throw new Error("raw HTTP header value reached the Tool backing API");
  }
})();`)
	globalConfig.JavaScriptInclude = []string{guardPath}
	return router, stateDirectory
}

func newOAuthPhase4Request(method, path, body, contentType string) *http.Request {
	request := httptest.NewRequest(method, "https://nyan8.stamps.necomori.asia"+path, strings.NewReader(body))
	request.Host = "nyan8.stamps.necomori.asia"
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Accept", "application/json")
	return request
}

func oauthPhase4JSONBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status=%d JSON body %q: %v", recorder.Code, recorder.Body.String(), err)
	}
	return body
}

func oauthPhase4Authorize(t *testing.T, router http.Handler, stateDirectory, clientID, redirectURI, resource, state, verifier, username, password string) (string, string) {
	t.Helper()
	authorizePath := "/oauth_authorize?response_type=code" +
		"&client_id=" + clientID +
		"&redirect_uri=https%3A%2F%2Fchatgpt.com%2Fconnector%2Foauth%2Fphase4" +
		"&resource=https%3A%2F%2Fnyan8.stamps.necomori.asia%2Fserver_mcp_http" +
		"&scope=nyan8%3Aread" +
		"&state=" + state +
		"&code_challenge=" + sha256Base64URL(verifier) +
		"&code_challenge_method=S256"
	getResponse := serveMCPPhase12Request(router, newOAuthPhase4Request(http.MethodGet, authorizePath, "", ""))
	if getResponse.Code != http.StatusOK {
		t.Fatalf("authorize GET status=%d, want %d; body=%q", getResponse.Code, http.StatusOK, getResponse.Body.String())
	}
	if contentType := getResponse.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("authorize GET Content-Type=%q, want text/html", contentType)
	}
	requestID := oauthPhase4HiddenValue(t, getResponse.Body.String(), "request_id")
	csrf := oauthPhase4HiddenValue(t, getResponse.Body.String(), "csrf")
	if !strings.HasPrefix(requestID, "req_") || len(requestID) != len("req_")+32 || len(csrf) != 43 {
		t.Fatalf("authorize hidden request_id/CSRF=%q/%q", requestID, csrf)
	}
	csrfCookieName := "nyan8_oauth_csrf_" + sha256Base64URL(requestID)
	var csrfCookie *http.Cookie
	for _, cookie := range getResponse.Result().Cookies() {
		if cookie.Name == csrfCookieName {
			csrfCookie = cookie
			break
		}
	}
	if csrfCookie == nil || csrfCookie.Value != csrf || csrfCookie.Path != "/oauth_authorize" || !csrfCookie.HttpOnly || !csrfCookie.Secure || csrfCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("authorize CSRF cookie=%#v, hidden CSRF=%q", csrfCookie, csrf)
	}
	assertOAuthPhase4StateSecretsAbsent(t, stateDirectory, password, csrf)

	form := "request_id=" + requestID + "&csrf=" + csrf + "&decision=allow&username=" + username + "&password=" + password
	postRequest := newOAuthPhase4Request(http.MethodPost, "/oauth_authorize", form, "application/x-www-form-urlencoded")
	postRequest.AddCookie(csrfCookie)
	postResponse := serveMCPPhase12Request(router, postRequest)
	if postResponse.Code != http.StatusSeeOther {
		t.Fatalf("authorize POST status=%d, want %d; body=%q", postResponse.Code, http.StatusSeeOther, postResponse.Body.String())
	}
	location, err := postResponse.Result().Location()
	if err != nil {
		t.Fatalf("authorize redirect Location=%q: %v", postResponse.Header().Get("Location"), err)
	}
	if location.Scheme+"://"+location.Host+location.Path != redirectURI || location.Query().Get("state") != state {
		t.Fatalf("authorize redirect=%q", location.String())
	}
	code := location.Query().Get("code")
	if !strings.HasPrefix(code, "code_") || len(code) != len("code_")+43 {
		t.Fatalf("authorization code=%q", code)
	}
	if cookie := postResponse.Header().Get("Set-Cookie"); !strings.Contains(cookie, csrfCookieName+"=") || !strings.Contains(cookie, "Max-Age=0") {
		t.Fatalf("authorize POST did not clear CSRF cookie: %q", cookie)
	}
	assertOAuthPhase4StateSecretsAbsent(t, stateDirectory, password, csrf, code)
	return code, csrf
}

func oauthPhase4HiddenValue(t *testing.T, body, name string) string {
	t.Helper()
	prefix := `name="` + name + `" value="`
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatalf("hidden input %q not found in %q", name, body)
	}
	valueStart := start + len(prefix)
	valueEnd := strings.Index(body[valueStart:], `"`)
	if valueEnd < 0 {
		t.Fatalf("hidden input %q has no closing quote", name)
	}
	return body[valueStart : valueStart+valueEnd]
}

func newOAuthPhase4TokenRequest(code, clientID, redirectURI, resource, verifier string) *http.Request {
	form := "grant_type=authorization_code" +
		"&code=" + code +
		"&client_id=" + clientID +
		"&redirect_uri=https%3A%2F%2Fchatgpt.com%2Fconnector%2Foauth%2Fphase4" +
		"&resource=https%3A%2F%2Fnyan8.stamps.necomori.asia%2Fserver_mcp_http" +
		"&code_verifier=" + verifier
	if redirectURI != "https://chatgpt.com/connector/oauth/phase4" || resource != "https://nyan8.stamps.necomori.asia/server_mcp_http" {
		panic("Phase 4 token request received an unexpected redirect URI or resource")
	}
	return newOAuthPhase4Request(http.MethodPost, "/oauth_token", form, "application/x-www-form-urlencoded")
}

func newOAuthPhase4RefreshRequest(refreshToken, clientID, resource, scope string) *http.Request {
	form := "grant_type=refresh_token" +
		"&refresh_token=" + url.QueryEscape(refreshToken) +
		"&client_id=" + url.QueryEscape(clientID) +
		"&resource=" + url.QueryEscape(resource)
	if scope != "" {
		form += "&scope=" + url.QueryEscape(scope)
	}
	return newOAuthPhase4Request(http.MethodPost, "/oauth_token", form, "application/x-www-form-urlencoded")
}

func oauthPhase4AccessToken(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("token status=%d, want %d; body=%q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	body := oauthPhase4JSONBody(t, recorder)
	token, _ := body["access_token"].(string)
	if !strings.HasPrefix(token, "tok_") || len(token) != len("tok_")+43 || body["token_type"] != "Bearer" || body["scope"] != "nyan8:read" {
		t.Fatalf("token response=%#v", body)
	}
	return token
}

func oauthPhase4ConcurrentTokenExchange(t *testing.T, router http.Handler, code, clientID, redirectURI, resource, verifier string) string {
	t.Helper()
	type exchangeResult struct {
		status     int
		body       []byte
		retryAfter string
	}
	const contenders = 12
	start := make(chan struct{})
	results := make(chan exchangeResult, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			response := serveMCPPhase12Request(router, newOAuthPhase4TokenRequest(code, clientID, redirectURI, resource, verifier))
			results <- exchangeResult{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...), retryAfter: response.Header().Get("Retry-After")}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	invalidGrants := 0
	busyResponses := 0
	accessToken := ""
	for result := range results {
		switch result.status {
		case http.StatusOK:
			successes++
			var body map[string]interface{}
			if err := json.Unmarshal(result.body, &body); err != nil {
				t.Fatalf("decode concurrent success %q: %v", result.body, err)
			}
			accessToken, _ = body["access_token"].(string)
		case http.StatusBadRequest:
			invalidGrants++
			var body map[string]interface{}
			if err := json.Unmarshal(result.body, &body); err != nil || body["error"] != "invalid_grant" {
				t.Errorf("concurrent loser body=%q err=%v", result.body, err)
			}
		case http.StatusServiceUnavailable:
			busyResponses++
			var body map[string]interface{}
			if err := json.Unmarshal(result.body, &body); err != nil || body["error"] != "OAuth endpoint is busy" || result.retryAfter != "1" {
				t.Errorf("concurrent busy body=%q Retry-After=%q err=%v", result.body, result.retryAfter, err)
			}
		default:
			t.Errorf("concurrent exchange status=%d body=%q", result.status, result.body)
		}
	}
	if successes != 1 || invalidGrants < 1 || successes+invalidGrants+busyResponses != contenders || !strings.HasPrefix(accessToken, "tok_") {
		t.Fatalf("concurrent exchanges successes=%d invalid_grants=%d busy=%d token=%q, want one success and at least one invalid_grant", successes, invalidGrants, busyResponses, accessToken)
	}
	return accessToken
}

func assertOAuthPhase4StateSecretsAbsent(t *testing.T, stateDirectory string, secrets ...string) {
	t.Helper()
	if err := filepath.Walk(stateDirectory, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("OAuth state file %s mode=%04o, want 0600", path, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			t.Errorf("OAuth state file %s is not valid JSON: %q", path, data)
		}
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			if strings.Contains(path, secret) || bytes.Contains(data, []byte(secret)) {
				t.Errorf("OAuth state file %s contains plaintext secret %q", path, secret)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("walk OAuth state directory: %v", err)
	}
}

func TestMCPPhase2GapOptionsAndCORS(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)

	tests := []struct {
		name        string
		path        string
		wantMethods string
		wantHeaders []string
	}{
		{
			name:        "MCP",
			path:        "/custom-mcp",
			wantMethods: "POST, OPTIONS",
			wantHeaders: []string{"Authorization", "MCP-Protocol-Version"},
		},
		{
			name:        "OAuth",
			path:        "/oauth_token",
			wantMethods: "GET, POST, OPTIONS",
			wantHeaders: []string{"Authorization", "Content-Type"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := newMCPPhase12Request(http.MethodOptions, test.path, "")
			request.Header.Set("Origin", "https://chatgpt.com")
			response := serveMCPPhase12Request(router, request)
			if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
				t.Fatalf("OPTIONS status=%d body=%q, want 204 with empty body", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://chatgpt.com" {
				t.Fatalf("Access-Control-Allow-Origin=%q", got)
			}
			if got := response.Header().Get("Access-Control-Allow-Methods"); got != test.wantMethods {
				t.Fatalf("Access-Control-Allow-Methods=%q, want %q", got, test.wantMethods)
			}
			for _, header := range test.wantHeaders {
				if !strings.Contains(response.Header().Get("Access-Control-Allow-Headers"), header) {
					t.Errorf("Access-Control-Allow-Headers=%q, want %s", response.Header().Get("Access-Control-Allow-Headers"), header)
				}
			}
			if response.Header().Get("Vary") != "Origin" || response.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("OPTIONS headers=%#v", response.Header())
			}
		})
	}
}

func TestMCPPhase2GapSubsequentRequestRequiresKnownVersionHeader(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	body := `{"jsonrpc":"2.0","id":"phase2-version","method":"ping","params":{}}`
	for _, test := range []struct {
		name    string
		version string
	}{
		{name: "missing"},
		{name: "unknown", version: "2099-01-01"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", body)
			if test.version != "" {
				request.Header.Set("MCP-Protocol-Version", test.version)
			}
			response := serveMCPPhase12Request(router, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "missing or unsupported") {
				t.Fatalf("status=%d body=%q, want unsupported version rejection", response.Code, response.Body.String())
			}
			if got := response.Header().Get("MCP-Protocol-Version"); got != "" {
				t.Fatalf("rejected response MCP-Protocol-Version=%q", got)
			}
		})
	}
}

func TestMCPPhase2GapRateLimitAndExecutionBusy(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		dir, definitions := newMCPPhase12Definitions(t)
		mcp := definitions["custom-mcp"].(map[string]interface{})
		mcp["rateLimit"] = map[string]interface{}{"requests": 1, "window": "1m"}
		loaded, err := loadMCPPhase12Config(dir, definitions)
		if err != nil {
			t.Fatal(err)
		}
		router := publishMCPPhase12Snapshot(t, loaded)
		for attempt := 1; attempt <= 2; attempt++ {
			request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`)
			request.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
			response := serveMCPPhase12Request(router, request)
			if attempt == 1 && response.Code != http.StatusOK {
				t.Fatalf("first request status=%d body=%q", response.Code, response.Body.String())
			}
			if attempt == 2 && (response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "") {
				t.Fatalf("second request status=%d Retry-After=%q body=%q", response.Code, response.Header().Get("Retry-After"), response.Body.String())
			}
		}
	})

	t.Run("MCP busy", func(t *testing.T) {
		dir, definitions := newMCPPhase12Definitions(t)
		mcp := definitions["custom-mcp"].(map[string]interface{})
		mcp["maxConcurrent"] = 1
		loaded, err := loadMCPPhase12Config(dir, definitions)
		if err != nil {
			t.Fatal(err)
		}
		router := publishMCPPhase12Snapshot(t, loaded)
		release, acquired := acquireMCPExecutionSlot("custom-mcp", 1)
		if !acquired {
			t.Fatal("failed to occupy MCP execution slot")
		}
		defer release()
		request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`)
		request.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
		response := serveMCPPhase12Request(router, request)
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" || !strings.Contains(response.Body.String(), "MCP server is busy") {
			t.Fatalf("status=%d Retry-After=%q body=%q", response.Code, response.Header().Get("Retry-After"), response.Body.String())
		}
	})

	t.Run("OAuth busy", func(t *testing.T) {
		dir, definitions := newMCPPhase12Definitions(t)
		mcp := definitions["custom-mcp"].(map[string]interface{})
		mcp["maxConcurrent"] = 1
		loaded, err := loadMCPPhase12Config(dir, definitions)
		if err != nil {
			t.Fatal(err)
		}
		router := publishMCPPhase12Snapshot(t, loaded)
		release, acquired := acquireMCPExecutionSlot("custom-mcp:oauth:oauthToken", 1)
		if !acquired {
			t.Fatal("failed to occupy OAuth execution slot")
		}
		defer release()
		request := newMCPPhase12Request(http.MethodPost, "/oauth_token", "grant_type=authorization_code")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := serveMCPPhase12Request(router, request)
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" || !strings.Contains(response.Body.String(), "OAuth endpoint is busy") {
			t.Fatalf("status=%d Retry-After=%q body=%q", response.Code, response.Header().Get("Retry-After"), response.Body.String())
		}
	})
}

func TestMCPPhase2GapAuthenticationPrecedesInputValidation(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	response := mcpPhase2GapToolCall(router, `{"unexpected":true}`, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want authentication failure before schema validation; body=%q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "inputSchema") || response.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("authentication-first response headers=%#v body=%q", response.Header(), response.Body.String())
	}
}

func TestMCPPhase2GapToolValidationAndReservedParameters(t *testing.T) {
	t.Run("input schema", func(t *testing.T) {
		router := newMCPPhase2GapAuthenticatedFixture(t, nil)
		response := mcpPhase2GapToolCall(router, `{"unexpected":true}`, "Bearer phase2")
		assertMCPPhase2GapToolError(t, response, "Tool arguments do not match inputSchema.")
	})

	t.Run("output schema", func(t *testing.T) {
		router := newMCPPhase2GapAuthenticatedFixture(t, func(dir string, _ map[string]interface{}) {
			writeHotReloadTestFile(t, filepath.Join(dir, "sample.js"), `({ok:"wrong",service:"Nyan8",items:[1,2,3]});`)
		})
		response := mcpPhase2GapToolCall(router, `{}`, "Bearer phase2")
		assertMCPPhase2GapToolError(t, response, "Tool result does not match outputSchema.")
	})

	t.Run("reserved parameters", func(t *testing.T) {
		router := newMCPPhase2GapAuthenticatedFixture(t, func(dir string, definitions map[string]interface{}) {
			path := filepath.Join(dir, "allow-any-input.js")
			writeHotReloadTestFile(t, path, `const nyanInputSchema={type:"object",additionalProperties:true}; ({success:true,status:200,result:{}});`)
			definitions["sample"].(map[string]interface{})["paramCheck"] = path
		})
		for _, key := range []string{"api", "mcp_principal", "mcp_tool", "_headers_raw", "_remote_address"} {
			response := mcpPhase2GapToolCall(router, fmt.Sprintf(`{%q:"attacker"}`, key), "Bearer phase2")
			assertMCPPhase2GapToolError(t, response, "Tool arguments contain a reserved parameter.")
		}
	})
}

func TestMCPPhase2GapToolAndResponseSizeLimits(t *testing.T) {
	if maxMCPToolResultBytes != 2<<20 || maxMCPResponseBytes != 4<<20 {
		t.Fatalf("size limits Tool/MCP=%d/%d, want 2MiB/4MiB", maxMCPToolResultBytes, maxMCPResponseBytes)
	}

	t.Run("Tool result over 2 MiB", func(t *testing.T) {
		router := newMCPPhase2GapAuthenticatedFixture(t, func(dir string, _ map[string]interface{}) {
			script := fmt.Sprintf(`JSON.stringify({ok:true,service:"Nyan8",items:[],padding:"x".repeat(%d)});`, maxMCPToolResultBytes+1)
			writeHotReloadTestFile(t, filepath.Join(dir, "sample.js"), script)
		})
		response := mcpPhase2GapToolCall(router, `{}`, "Bearer phase2")
		assertMCPPhase2GapToolError(t, response, "Tool result is too large.")
	})

	t.Run("MCP response over 4 MiB", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		mcpWriteHTTPResult(context, json.RawMessage(`"oversized"`), http.StatusOK, map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "oversized",
			"result":  strings.Repeat("x", maxMCPResponseBytes),
		})
		if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "MCP response is too large") || recorder.Body.Len() >= maxMCPResponseBytes {
			t.Fatalf("status=%d bytes=%d body-prefix=%q", recorder.Code, recorder.Body.Len(), recorder.Body.String())
		}
	})
}

func TestMCPPhase2GapRequestUsesCapturedSnapshot(t *testing.T) {
	oldDir, oldDefinitions := newMCPPhase12Definitions(t)
	writeHotReloadTestFile(t, filepath.Join(oldDir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
	writeHotReloadTestFile(t, filepath.Join(oldDir, "sample.js"), `({generation:"old"});`)
	delete(oldDefinitions["sample"].(map[string]interface{}), "outCheck")
	oldLoaded, err := loadMCPPhase12Config(oldDir, oldDefinitions)
	if err != nil {
		t.Fatal(err)
	}

	newDir, newDefinitions := newMCPPhase12Definitions(t)
	writeHotReloadTestFile(t, filepath.Join(newDir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
	writeHotReloadTestFile(t, filepath.Join(newDir, "sample.js"), `({generation:"new"});`)
	delete(newDefinitions["sample"].(map[string]interface{}), "outCheck")
	newLoaded, err := loadMCPPhase12Config(newDir, newDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	publishMCPPhase12Snapshot(t, newLoaded)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/custom-mcp", func(c *gin.Context) {
		handleMCPHTTP(c, oldLoaded.Snapshot, oldLoaded.Snapshot.MCPServers["custom-mcp"])
	})
	response := mcpPhase2GapToolCall(router, `{}`, "Bearer phase2")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	body := oauthPhase4JSONBody(t, response)
	result, _ := body["result"].(map[string]interface{})
	structured, _ := result["structuredContent"].(map[string]interface{})
	if structured["generation"] != "old" {
		t.Fatalf("structuredContent=%#v, want captured old snapshot while current snapshot is new", structured)
	}
}

func TestMCPPhase2GapStartupAPIRouteRedispatchesToReloadedMCP(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	dir, definitions := newMCPPhase12Definitions(t)
	apiPath := filepath.Join(dir, "api.json")
	initialDefinitions := map[string]interface{}{
		"custom-mcp": map[string]interface{}{"script": "./sample.js"},
	}
	initialData, err := json.Marshal(initialDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apiPath, initialData, 0o644); err != nil {
		t.Fatal(err)
	}
	servicePaths.API.Path = apiPath
	setAPIFiles(apiPath, initialDefinitions)
	t.Cleanup(func() {
		setAPIFiles("", nil)
		servicePaths = serviceFilePaths{}
	})
	router := gin.New()
	if err := registerDynamicEndpoints(router, dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	publishMCPPhase12Snapshot(t, loaded)

	request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", `{"jsonrpc":"2.0","id":"reloaded","method":"ping","params":{}}`)
	request.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	response := serveMCPPhase12Request(router, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"result":{}`) {
		t.Fatalf("reused startup API route status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestMCPPhase2GapRejectsHeaderUnsafeScope(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	definitions["oauth_verify_access"].(map[string]interface{})["scopes"] = []interface{}{`nyan8:read"`}
	_, err := loadMCPPhase12Config(dir, definitions)
	if err == nil || !strings.Contains(err.Error(), "invalid OAuth scope") {
		t.Fatalf("unsafe scope load error=%v", err)
	}
}

func newMCPPhase2GapAuthenticatedFixture(t *testing.T, mutate func(string, map[string]interface{})) http.Handler {
	t.Helper()
	dir, definitions := newMCPPhase12Definitions(t)
	writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
	if mutate != nil {
		mutate(dir, definitions)
	}
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	return publishMCPPhase12Snapshot(t, loaded)
}

func mcpPhase2GapAuthenticatedHook() string {
	return `({authenticated:true,forbidden:false,principal:{user_id:"phase2",client_id:"phase2-client",scope:"nyan8:read",scopes:["nyan8:read"]}});`
}

func mcpPhase2GapToolCall(router http.Handler, argumentsJSON, authorization string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"phase2-tool","method":"tools/call","params":{"name":"sample","arguments":%s}}`, argumentsJSON)
	request := newMCPPhase12Request(http.MethodPost, "/custom-mcp", body)
	request.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return serveMCPPhase12Request(router, request)
}

func assertMCPPhase2GapToolError(t *testing.T, response *httptest.ResponseRecorder, wantText string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("Tool error status=%d body=%q", response.Code, response.Body.String())
	}
	body := oauthPhase4JSONBody(t, response)
	result, _ := body["result"].(map[string]interface{})
	content, _ := result["content"].([]interface{})
	if result["isError"] != true || len(content) != 1 {
		t.Fatalf("Tool error result=%#v", result)
	}
	entry, _ := content[0].(map[string]interface{})
	if entry["text"] != wantText {
		t.Fatalf("Tool error text=%v, want %q; result=%#v", entry["text"], wantText, result)
	}
}

type oauthPhase4NegativeFixture struct {
	router         http.Handler
	stateDirectory string
	clientID       string
	username       string
	password       string
	redirectURI    string
	resource       string
}

type oauthPhase4PendingAuthorization struct {
	requestID string
	csrf      string
	cookie    *http.Cookie
	state     string
	verifier  string
}

func TestOAuthPhase4NegativeCSRFDoesNotConsumeAuthorizationRequest(t *testing.T) {
	fixture := newOAuthPhase4NegativeFixture(t, nil, nil)
	pending := oauthPhase4NegativeBeginAuthorization(t, fixture, "phase4-csrf-state", strings.Repeat("c", 64))

	missingCookie := newOAuthPhase4NegativeAuthorizationPost(fixture, pending, pending.csrf, nil)
	missingResponse := serveMCPPhase12Request(fixture.router, missingCookie)
	assertOAuthPhase4NegativeError(t, missingResponse, http.StatusBadRequest, "invalid_request")

	wrongCSRF := strings.Repeat("z", 43)
	wrongCookie := &http.Cookie{Name: pending.cookie.Name, Value: wrongCSRF}
	mismatchedRequest := newOAuthPhase4NegativeAuthorizationPost(fixture, pending, wrongCSRF, wrongCookie)
	mismatchedResponse := serveMCPPhase12Request(fixture.router, mismatchedRequest)
	assertOAuthPhase4NegativeError(t, mismatchedResponse, http.StatusBadRequest, "invalid_request")

	validResponse := serveMCPPhase12Request(fixture.router, newOAuthPhase4NegativeAuthorizationPost(fixture, pending, pending.csrf, pending.cookie))
	code := oauthPhase4NegativeAuthorizationCode(t, validResponse, fixture.redirectURI, pending.state)
	if code == "" {
		t.Fatal("valid CSRF retry did not produce a code")
	}
}

func TestOAuthPhase4NegativeBindingMismatchesDoNotConsumeCode(t *testing.T) {
	fixture := newOAuthPhase4NegativeFixture(t, nil, nil)
	verifier := strings.Repeat("b", 64)
	code, _ := oauthPhase4Authorize(t, fixture.router, fixture.stateDirectory, fixture.clientID, fixture.redirectURI, fixture.resource, "phase4-binding-state", verifier, fixture.username, fixture.password)

	tests := []struct {
		name        string
		clientID    string
		redirectURI string
		resource    string
	}{
		{name: "client", clientID: fixture.clientID + "x", redirectURI: fixture.redirectURI, resource: fixture.resource},
		{name: "redirect", clientID: fixture.clientID, redirectURI: "https://chatgpt.com/connector/oauth/other", resource: fixture.resource},
		{name: "resource", clientID: fixture.clientID, redirectURI: fixture.redirectURI, resource: "https://nyan8.stamps.necomori.asia/not-mcp"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveMCPPhase12Request(fixture.router, newOAuthPhase4NegativeTokenRequest(code, test.clientID, test.redirectURI, test.resource, verifier))
			assertOAuthPhase4NegativeError(t, response, http.StatusBadRequest, "invalid_grant")
		})
	}

	validResponse := serveMCPPhase12Request(fixture.router, newOAuthPhase4NegativeTokenRequest(code, fixture.clientID, fixture.redirectURI, fixture.resource, verifier))
	if token := oauthPhase4AccessToken(t, validResponse); token == "" {
		t.Fatal("valid exchange after binding mismatch did not succeed")
	}

	for _, redirectURI := range []string{
		"https://chatgpt.com/connector/oauth/nested/path",
		"https://chatgpt.com/connector/oauth/phase4?unexpected=query",
	} {
		body := fmt.Sprintf(`{"redirect_uris":[%q],"client_name":"unsafe redirect","token_endpoint_auth_method":"none","grant_types":["authorization_code"],"response_types":["code"],"scope":"nyan8:read"}`, redirectURI)
		response := serveMCPPhase12Request(fixture.router, newOAuthPhase4Request(http.MethodPost, "/oauth_register", body, "application/json"))
		assertOAuthPhase4NegativeError(t, response, http.StatusBadRequest, "invalid_redirect_uri")
	}
}

func TestOAuthPhase4NegativeExpiredRequestCodeAndToken(t *testing.T) {
	fixture := newOAuthPhase4NegativeFixture(t, nil, nil)

	t.Run("authorization request", func(t *testing.T) {
		pending := oauthPhase4NegativeBeginAuthorization(t, fixture, "phase4-expired-request", strings.Repeat("r", 64))
		key := oauthPhase4NegativeExpireState(t, fixture.stateDirectory, "requests", pending.requestID)
		response := serveMCPPhase12Request(fixture.router, newOAuthPhase4NegativeAuthorizationPost(fixture, pending, pending.csrf, pending.cookie))
		assertOAuthPhase4NegativeError(t, response, http.StatusBadRequest, "invalid_request")
		if _, err := oauthReadState(fixture.stateDirectory, key); !os.IsNotExist(err) {
			t.Fatalf("expired authorization request read error=%v, want lazy deletion", err)
		}
	})

	t.Run("authorization code", func(t *testing.T) {
		verifier := strings.Repeat("d", 64)
		code, _ := oauthPhase4Authorize(t, fixture.router, fixture.stateDirectory, fixture.clientID, fixture.redirectURI, fixture.resource, "phase4-expired-code", verifier, fixture.username, fixture.password)
		key := oauthPhase4NegativeExpireState(t, fixture.stateDirectory, "codes", code)
		response := serveMCPPhase12Request(fixture.router, newOAuthPhase4NegativeTokenRequest(code, fixture.clientID, fixture.redirectURI, fixture.resource, verifier))
		assertOAuthPhase4NegativeError(t, response, http.StatusBadRequest, "invalid_grant")
		if _, err := oauthReadState(fixture.stateDirectory, key); !os.IsNotExist(err) {
			t.Fatalf("expired authorization code read error=%v, want lazy deletion", err)
		}
	})

	t.Run("access token", func(t *testing.T) {
		verifier := strings.Repeat("t", 64)
		code, _ := oauthPhase4Authorize(t, fixture.router, fixture.stateDirectory, fixture.clientID, fixture.redirectURI, fixture.resource, "phase4-expired-token", verifier, fixture.username, fixture.password)
		tokenResponse := serveMCPPhase12Request(fixture.router, newOAuthPhase4NegativeTokenRequest(code, fixture.clientID, fixture.redirectURI, fixture.resource, verifier))
		token := oauthPhase4AccessToken(t, tokenResponse)
		key := oauthPhase4NegativeExpireState(t, fixture.stateDirectory, "tokens", token)
		response := oauthPhase4NegativeToolCall(fixture, token)
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("expired token Tool status=%d headers=%#v body=%q", response.Code, response.Header(), response.Body.String())
		}
		if _, err := oauthReadState(fixture.stateDirectory, key); !os.IsNotExist(err) {
			t.Fatalf("expired access token read error=%v, want lazy deletion", err)
		}
	})
}

func TestOAuthPhase4NegativeInsufficientScopeReturns403(t *testing.T) {
	fixture := newOAuthPhase4NegativeFixture(t, []string{"nyan8:read", "nyan8:write"}, []string{"nyan8:write"})
	verifier := strings.Repeat("s", 64)
	code, _ := oauthPhase4Authorize(t, fixture.router, fixture.stateDirectory, fixture.clientID, fixture.redirectURI, fixture.resource, "phase4-scope-state", verifier, fixture.username, fixture.password)
	tokenResponse := serveMCPPhase12Request(fixture.router, newOAuthPhase4NegativeTokenRequest(code, fixture.clientID, fixture.redirectURI, fixture.resource, verifier))
	token := oauthPhase4AccessToken(t, tokenResponse)
	response := oauthPhase4NegativeToolCall(fixture, token)
	if response.Code != http.StatusForbidden {
		t.Fatalf("insufficient-scope status=%d, want 403; body=%q", response.Code, response.Body.String())
	}
	wantChallenge := `Bearer resource_metadata="https://nyan8.stamps.necomori.asia/oauth_protected_resource_metadata", scope="nyan8:write", error="insufficient_scope", error_description="The access token does not grant the required scope."`
	if got := response.Header().Get("WWW-Authenticate"); got != wantChallenge {
		t.Fatalf("WWW-Authenticate=%q, want %q", got, wantChallenge)
	}
	body := oauthPhase4JSONBody(t, response)
	result, _ := body["result"].(map[string]interface{})
	meta, _ := result["_meta"].(map[string]interface{})
	challenges, _ := meta["mcp/www_authenticate"].([]interface{})
	if result["isError"] != true || len(challenges) != 1 || challenges[0] != wantChallenge {
		t.Fatalf("insufficient-scope MCP result=%#v", result)
	}
}

func TestOAuthPhase4NegativeParallelAuthorizationCookiesAreIsolated(t *testing.T) {
	fixture := newOAuthPhase4NegativeFixture(t, nil, nil)
	first := oauthPhase4NegativeBeginAuthorization(t, fixture, "phase4-parallel-first", strings.Repeat("1", 64))
	second := oauthPhase4NegativeBeginAuthorization(t, fixture, "phase4-parallel-second", strings.Repeat("2", 64))
	if first.cookie.Name == second.cookie.Name || first.cookie.Value == second.cookie.Value {
		t.Fatalf("parallel authorization cookies collided: %#v / %#v", first.cookie, second.cookie)
	}

	firstRequest := newOAuthPhase4NegativeAuthorizationPost(fixture, first, first.csrf, first.cookie)
	firstRequest.AddCookie(second.cookie)
	secondRequest := newOAuthPhase4NegativeAuthorizationPost(fixture, second, second.csrf, second.cookie)
	secondRequest.AddCookie(first.cookie)
	type authorizationResult struct {
		state    string
		response *httptest.ResponseRecorder
	}
	start := make(chan struct{})
	results := make(chan authorizationResult, 2)
	var wait sync.WaitGroup
	for _, item := range []struct {
		state   string
		request *http.Request
	}{
		{state: first.state, request: firstRequest},
		{state: second.state, request: secondRequest},
	} {
		item := item
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- authorizationResult{state: item.state, response: serveMCPPhase12Request(fixture.router, item.request)}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for result := range results {
		if code := oauthPhase4NegativeAuthorizationCode(t, result.response, fixture.redirectURI, result.state); code == "" {
			t.Errorf("parallel authorization for state %q returned no code", result.state)
		}
	}
}

func TestOAuthPhase4NegativeStateQuotaDuplicateJSONAndRatePolicy(t *testing.T) {
	t.Run("quota", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		namespace := filepath.Join(root, "misc")
		if err := os.MkdirAll(namespace, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 255; index++ {
			path := filepath.Join(namespace, fmt.Sprintf("%03d.json", index))
			if err := os.WriteFile(path, []byte(`{"ok":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		destination := filepath.Join(namespace, "new.json")
		if err := enforceOAuthStateQuota(root, "misc/new.json", destination); err != nil {
			t.Fatalf("255 existing records unexpectedly exceeded quota: %v", err)
		}
		if err := os.WriteFile(filepath.Join(namespace, "255.json"), []byte(`{"ok":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := enforceOAuthStateQuota(root, "misc/new.json", destination); err == nil || !strings.Contains(err.Error(), "quota exceeded") {
			t.Fatalf("256 existing records quota error=%v", err)
		}
		if err := enforceOAuthStateQuota(root, "misc/000.json", filepath.Join(namespace, "000.json")); err != nil {
			t.Fatalf("updating an existing record at quota failed: %v", err)
		}
	})

	t.Run("duplicate JSON state", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(root, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "duplicate.json"), []byte(`{"key":1,"key":2}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := oauthReadState(root, "duplicate.json"); err == nil {
			t.Fatal("OAuth state reader accepted duplicate JSON keys")
		}
	})

	t.Run("endpoint rate policy", func(t *testing.T) {
		mcp := &MCPServerConfig{}
		want := map[string]int{"oauthAdminUser": 10, "oauthRegister": 10, "oauthAuthorize": 30, "oauthToken": 60}
		for hook, requests := range want {
			limit := oauthRateLimitForHook(mcp, hook)
			if limit.Requests != requests || limit.Window != "1m" {
				t.Errorf("%s rate=%#v, want %d/1m", hook, limit, requests)
			}
		}
		mcp.RateLimit = &MCPRateLimit{Requests: 7, Window: "10s"}
		for hook := range want {
			if limit := oauthRateLimitForHook(mcp, hook); limit != mcp.RateLimit {
				t.Errorf("%s did not honor stricter server rate limit: %#v", hook, limit)
			}
		}
	})
}

func newOAuthPhase4NegativeFixture(t *testing.T, serverScopes, toolScopes []string) oauthPhase4NegativeFixture {
	t.Helper()
	stateDirectory := filepath.Join(t.TempDir(), "oauth-state")
	loaded := loadOAuthPhase4TestConfig(t, stateDirectory, serverScopes, toolScopes)
	router := publishMCPPhase12Snapshot(t, loaded)
	globalConfig.OAuthAdmin = OAuthAdminConfig{Username: "phase4-negative-operator", Password: "Phase4NegativeOperator123"}
	fixture := oauthPhase4NegativeFixture{
		router:         router,
		stateDirectory: stateDirectory,
		username:       "phase4.negative.user",
		password:       "Phase4NegativeUser123",
		redirectURI:    "https://chatgpt.com/connector/oauth/phase4",
		resource:       "https://nyan8.stamps.necomori.asia/server_mcp_http",
	}
	adminBody := fmt.Sprintf(`{"username":%q,"password":%q}`, fixture.username, fixture.password)
	adminRequest := newOAuthPhase4Request(http.MethodPost, "/oauth_admin_user", adminBody, "application/json")
	adminRequest.SetBasicAuth(globalConfig.OAuthAdmin.Username, globalConfig.OAuthAdmin.Password)
	adminResponse := serveMCPPhase12Request(router, adminRequest)
	if adminResponse.Code != http.StatusCreated {
		t.Fatalf("negative fixture admin status=%d body=%q", adminResponse.Code, adminResponse.Body.String())
	}
	registerBody := fmt.Sprintf(`{"redirect_uris":[%q],"client_name":"Phase 4 negative client","token_endpoint_auth_method":"none","grant_types":["authorization_code"],"response_types":["code"],"scope":"nyan8:read"}`, fixture.redirectURI)
	registerResponse := serveMCPPhase12Request(router, newOAuthPhase4Request(http.MethodPost, "/oauth_register", registerBody, "application/json"))
	if registerResponse.Code != http.StatusCreated {
		t.Fatalf("negative fixture DCR status=%d body=%q", registerResponse.Code, registerResponse.Body.String())
	}
	fixture.clientID, _ = oauthPhase4JSONBody(t, registerResponse)["client_id"].(string)
	if fixture.clientID == "" {
		t.Fatal("negative fixture DCR returned no client_id")
	}
	return fixture
}

func oauthPhase4NegativeBeginAuthorization(t *testing.T, fixture oauthPhase4NegativeFixture, state, verifier string) oauthPhase4PendingAuthorization {
	t.Helper()
	path := "/oauth_authorize?response_type=code" +
		"&client_id=" + oauthPhase4NegativeEscape(fixture.clientID) +
		"&redirect_uri=" + oauthPhase4NegativeEscape(fixture.redirectURI) +
		"&resource=" + oauthPhase4NegativeEscape(fixture.resource) +
		"&scope=" + oauthPhase4NegativeEscape("nyan8:read") +
		"&state=" + oauthPhase4NegativeEscape(state) +
		"&code_challenge=" + oauthPhase4NegativeEscape(sha256Base64URL(verifier)) +
		"&code_challenge_method=S256"
	response := serveMCPPhase12Request(fixture.router, newOAuthPhase4Request(http.MethodGet, path, "", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("begin authorization status=%d body=%q", response.Code, response.Body.String())
	}
	pending := oauthPhase4PendingAuthorization{
		requestID: oauthPhase4HiddenValue(t, response.Body.String(), "request_id"),
		csrf:      oauthPhase4HiddenValue(t, response.Body.String(), "csrf"),
		state:     state,
		verifier:  verifier,
	}
	wantCookieName := "nyan8_oauth_csrf_" + sha256Base64URL(pending.requestID)
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == wantCookieName {
			pending.cookie = cookie
			break
		}
	}
	if pending.cookie == nil || pending.cookie.Value != pending.csrf {
		t.Fatalf("authorization cookie=%#v, want name=%q value=%q", pending.cookie, wantCookieName, pending.csrf)
	}
	return pending
}

func newOAuthPhase4NegativeAuthorizationPost(fixture oauthPhase4NegativeFixture, pending oauthPhase4PendingAuthorization, csrf string, cookie *http.Cookie) *http.Request {
	form := "request_id=" + oauthPhase4NegativeEscape(pending.requestID) +
		"&csrf=" + oauthPhase4NegativeEscape(csrf) +
		"&decision=allow" +
		"&username=" + oauthPhase4NegativeEscape(fixture.username) +
		"&password=" + oauthPhase4NegativeEscape(fixture.password)
	request := newOAuthPhase4Request(http.MethodPost, "/oauth_authorize", form, "application/x-www-form-urlencoded")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	return request
}

func oauthPhase4NegativeAuthorizationCode(t *testing.T, response *httptest.ResponseRecorder, redirectURI, state string) string {
	t.Helper()
	if response.Code != http.StatusSeeOther {
		t.Fatalf("authorization POST status=%d body=%q", response.Code, response.Body.String())
	}
	location, err := response.Result().Location()
	if err != nil {
		t.Fatal(err)
	}
	if location.Scheme+"://"+location.Host+location.Path != redirectURI || location.Query().Get("state") != state {
		t.Fatalf("authorization redirect=%q", location.String())
	}
	return location.Query().Get("code")
}

func newOAuthPhase4NegativeTokenRequest(code, clientID, redirectURI, resource, verifier string) *http.Request {
	form := "grant_type=authorization_code" +
		"&code=" + oauthPhase4NegativeEscape(code) +
		"&client_id=" + oauthPhase4NegativeEscape(clientID) +
		"&redirect_uri=" + oauthPhase4NegativeEscape(redirectURI) +
		"&resource=" + oauthPhase4NegativeEscape(resource) +
		"&code_verifier=" + oauthPhase4NegativeEscape(verifier)
	return newOAuthPhase4Request(http.MethodPost, "/oauth_token", form, "application/x-www-form-urlencoded")
}

func oauthPhase4NegativeEscape(value string) string {
	const hexadecimal = "0123456789ABCDEF"
	var escaped strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("-._~", rune(character)) {
			escaped.WriteByte(character)
			continue
		}
		if character == ' ' {
			escaped.WriteByte('+')
			continue
		}
		escaped.WriteByte('%')
		escaped.WriteByte(hexadecimal[character>>4])
		escaped.WriteByte(hexadecimal[character&0x0f])
	}
	return escaped.String()
}

func oauthPhase4NegativeExpireState(t *testing.T, stateDirectory, namespace, secret string) string {
	t.Helper()
	key := namespace + "/" + sha256Base64URL(secret) + ".json"
	text, err := oauthReadState(stateDirectory, key)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]interface{}
	if err := json.Unmarshal([]byte(text), &state); err != nil {
		t.Fatal(err)
	}
	state["expiresAt"] = float64(time.Now().Add(-time.Minute).UnixMilli())
	updated, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := oauthWriteState(stateDirectory, key, string(updated)); err != nil {
		t.Fatal(err)
	}
	return key
}

func oauthPhase4NegativeToolCall(fixture oauthPhase4NegativeFixture, token string) *httptest.ResponseRecorder {
	body := `{"jsonrpc":"2.0","id":"phase4-negative-tool","method":"tools/call","params":{"name":"mcp_sample","arguments":{}}}`
	request := newOAuthPhase4Request(http.MethodPost, "/server_mcp_http", body, "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
	request.Header.Set("Authorization", "Bearer "+token)
	return serveMCPPhase12Request(fixture.router, request)
}

func assertOAuthPhase4NegativeError(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("OAuth error status=%d, want %d; body=%q", response.Code, wantStatus, response.Body.String())
	}
	body := oauthPhase4JSONBody(t, response)
	if body["error"] != wantCode {
		t.Fatalf("OAuth error=%v, want %q; body=%#v", body["error"], wantCode, body)
	}
}

type phase5SMTPResult struct {
	recipients []string
	auth       []byte
	message    []byte
	err        error
}

func TestPhase5SendMailEnvelopeMIMEAndSecretHygiene(t *testing.T) {
	host, port, smtpResult := newPhase5SMTPServer(t)
	previousConfig := globalConfig
	previousLogger := logger
	var logOutput bytes.Buffer
	logger = log.New(&logOutput, "", 0)
	globalConfig = Config{SMTP: SMTPConfig{
		Host:       host,
		Port:       port,
		Username:   "phase5-smtp-user",
		Password:   "phase5-smtp-password-secret",
		FromEmail:  "sender@example.test",
		FromName:   "Nyan8 Phase 5",
		DefaultBCC: []string{"default-hidden@example.test", "HIDDEN@example.test", "cc@example.test"},
	}}
	t.Cleanup(func() {
		globalConfig = previousConfig
		logger = previousLogger
	})

	const (
		bodySecret       = "phase5-body-secret"
		attachmentSecret = "phase5-attachment-secret"
	)
	err := sendMail(
		[]string{"To@One.test", "duplicate@example.test"},
		[]string{"cc@example.test", "to@one.test"},
		[]string{"hidden@example.test", "DUPLICATE@example.test"},
		"Phase 5 mail",
		bodySecret,
		false,
		[]MailAttachment{{FileName: "phase5.txt", ContentType: "application/octet-stream", Data: []byte(attachmentSecret)}},
	)
	if err != nil {
		t.Fatalf("sendMail: %v", err)
	}

	var captured phase5SMTPResult
	select {
	case captured = <-smtpResult:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SMTP capture")
	}
	if captured.err != nil {
		t.Fatalf("SMTP mock: %v", captured.err)
	}
	wantRecipients := []string{
		"To@One.test",
		"duplicate@example.test",
		"cc@example.test",
		"hidden@example.test",
		"default-hidden@example.test",
	}
	if !reflect.DeepEqual(captured.recipients, wantRecipients) {
		t.Fatalf("SMTP recipients=%#v, want %#v", captured.recipients, wantRecipients)
	}
	if string(captured.auth) != "\x00phase5-smtp-user\x00phase5-smtp-password-secret" {
		t.Fatalf("SMTP AUTH payload=%q", captured.auth)
	}

	message, err := mail.ReadMessage(bytes.NewReader(captured.message))
	if err != nil {
		t.Fatalf("parse captured mail: %v\n%s", err, captured.message)
	}
	if got := message.Header.Get("To"); got != "To@One.test,duplicate@example.test" {
		t.Fatalf("To header=%q", got)
	}
	if got := message.Header.Get("Cc"); got != "cc@example.test" {
		t.Fatalf("Cc header=%q", got)
	}
	if got := message.Header.Get("Bcc"); got != "" {
		t.Fatalf("Bcc header was exposed: %q", got)
	}
	rawMessage := string(captured.message)
	for _, hiddenAddress := range []string{"hidden@example.test", "default-hidden@example.test"} {
		if strings.Contains(strings.ToLower(rawMessage), strings.ToLower(hiddenAddress)) {
			t.Errorf("Bcc recipient %q was exposed in message data", hiddenAddress)
		}
	}

	mediaType, parameters, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" || parameters["boundary"] == "" {
		t.Fatalf("message Content-Type=%q parameters=%#v err=%v", mediaType, parameters, err)
	}
	multipartReader := multipart.NewReader(message.Body, parameters["boundary"])
	var decodedBody string
	var attachmentName string
	var decodedAttachment string
	parts := 0
	for {
		part, partErr := multipartReader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			t.Fatalf("read MIME part: %v", partErr)
		}
		parts++
		var reader io.Reader = part
		if strings.EqualFold(part.Header.Get("Content-Transfer-Encoding"), "base64") {
			reader = base64.NewDecoder(base64.StdEncoding, part)
		}
		data, readErr := io.ReadAll(reader)
		if readErr != nil {
			t.Fatalf("decode MIME part: %v", readErr)
		}
		disposition, dispositionParameters, dispositionErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if dispositionErr == nil && disposition == "attachment" {
			attachmentName = dispositionParameters["filename"]
			decodedAttachment = string(data)
			continue
		}
		partType, _, typeErr := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if typeErr != nil || partType != "text/plain" {
			t.Fatalf("body part Content-Type=%q err=%v", part.Header.Get("Content-Type"), typeErr)
		}
		decodedBody = string(data)
	}
	if parts != 2 || decodedBody != bodySecret || attachmentName != "phase5.txt" || decodedAttachment != attachmentSecret {
		t.Fatalf("MIME parts=%d body=%q attachment=%q data=%q", parts, decodedBody, attachmentName, decodedAttachment)
	}

	logs := logOutput.String()
	for _, secret := range []string{
		"phase5-smtp-user",
		"phase5-smtp-password-secret",
		bodySecret,
		attachmentSecret,
		"hidden@example.test",
		"default-hidden@example.test",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("log exposed secret %q: %q", secret, logs)
		}
	}
	if !strings.Contains(logs, "attachments=1") {
		t.Fatalf("non-secret mail diagnostic missing: %q", logs)
	}
}

func TestPhase5IncomingWebSocketResponseAndPush(t *testing.T) {
	previousSnapshot := currentAPISnapshot()
	previousConfig := globalConfig
	previousPaths := servicePaths
	previousLogger := logger
	previousPushConnections := map[interface{}]interface{}{}
	pushConnections.Range(func(key, value interface{}) bool {
		previousPushConnections[key] = value
		pushConnections.Delete(key)
		return true
	})
	var server *httptest.Server
	var sourceConnection *websocket.Conn
	var sinkConnection *websocket.Conn
	t.Cleanup(func() {
		closePhase5WebSocket(sourceConnection)
		closePhase5WebSocket(sinkConnection)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			_, sourceExists := pushConnections.Load("source")
			_, sinkExists := pushConnections.Load("sink")
			if !sourceExists && !sinkExists {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if server != nil {
			server.Close()
		}
		pushConnections.Range(func(key, _ interface{}) bool {
			pushConnections.Delete(key)
			return true
		})
		for key, value := range previousPushConnections {
			pushConnections.Store(key, value)
		}
		publishAPISnapshot(previousSnapshot)
		globalConfig = previousConfig
		servicePaths = previousPaths
		logger = previousLogger
	})

	dir := t.TempDir()
	apiPath := filepath.Join(dir, "api.json")
	writeHotReloadTestFile(t, filepath.Join(dir, "source.js"), `JSON.stringify({kind:"source-response",api:nyanAllParams.api,value:nyanAllParams.value});`)
	writeHotReloadTestFile(t, filepath.Join(dir, "sink.js"), `JSON.stringify({kind:"sink-push",api:nyanAllParams.api,value:nyanAllParams.value});`)
	definitions := map[string]interface{}{
		"source": map[string]interface{}{"script": "./source.js", "push": "sink"},
		"sink":   map[string]interface{}{"script": "./sink.js"},
	}
	data, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apiPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := readAPIConfigFile(apiPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	var websocketLogs bytes.Buffer
	globalConfig = Config{Name: "Phase 5 WebSocket Test"}
	servicePaths.API.Path = apiPath
	logger = log.New(&websocketLogs, "", 0)
	publishAPISnapshot(loaded.Snapshot)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	if err := registerDynamicEndpoints(router, dir); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local TCP listener is unavailable: %v", err)
	}
	server = httptest.NewUnstartedServer(router)
	server.Listener = listener
	server.Start()

	websocketURL := "ws" + server.URL[len("http"):]
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	sinkConnection, _, err = dialer.Dial(websocketURL+"/sink", nil)
	if err != nil {
		t.Fatalf("dial sink WebSocket: %v", err)
	}
	waitForHotReloadCondition(t, "sink WebSocket registration", func() bool {
		connection, exists := pushConnections.Load("sink")
		_, isServerWebSocket := connection.(*websocket.Conn)
		return exists && isServerWebSocket
	})
	sourceConnection, _, err = dialer.Dial(websocketURL+"/source", nil)
	if err != nil {
		t.Fatalf("dial source WebSocket: %v", err)
	}
	waitForHotReloadCondition(t, "source WebSocket registration", func() bool {
		connection, exists := pushConnections.Load("source")
		_, isServerWebSocket := connection.(*websocket.Conn)
		return exists && isServerWebSocket
	})

	operationDeadline := time.Now().Add(3 * time.Second)
	if err := sourceConnection.SetWriteDeadline(operationDeadline); err != nil {
		t.Fatal(err)
	}
	if err := sourceConnection.SetReadDeadline(operationDeadline); err != nil {
		t.Fatal(err)
	}
	if err := sinkConnection.SetReadDeadline(operationDeadline); err != nil {
		t.Fatal(err)
	}
	requestBody := `{"api":"source","value":"phase5-websocket"}`
	if err := sourceConnection.WriteMessage(websocket.TextMessage, []byte(requestBody)); err != nil {
		t.Fatalf("write source WebSocket: %v", err)
	}
	sourceType, sourceMessage, err := sourceConnection.ReadMessage()
	if err != nil {
		t.Fatalf("read source response: %v; logs=%q", err, websocketLogs.String())
	}
	sinkType, sinkMessage, err := sinkConnection.ReadMessage()
	if err != nil {
		t.Fatalf("read sink push: %v; logs=%q", err, websocketLogs.String())
	}
	if sourceType != websocket.TextMessage || sinkType != websocket.TextMessage {
		t.Fatalf("message types source/sink=%d/%d, want text/text", sourceType, sinkType)
	}
	var sourceBody map[string]interface{}
	if err := json.Unmarshal(sourceMessage, &sourceBody); err != nil {
		t.Fatalf("source response=%q: %v", sourceMessage, err)
	}
	if sourceBody["kind"] != "source-response" || sourceBody["api"] != "source" || sourceBody["value"] != "phase5-websocket" {
		t.Fatalf("source response=%#v", sourceBody)
	}
	var sinkBody map[string]interface{}
	if err := json.Unmarshal(sinkMessage, &sinkBody); err != nil {
		t.Fatalf("sink push=%q: %v", sinkMessage, err)
	}
	if sinkBody["kind"] != "sink-push" || sinkBody["api"] != "source" || sinkBody["value"] != "phase5-websocket" {
		t.Fatalf("sink push=%#v", sinkBody)
	}
}

func newPhase5SMTPServer(t *testing.T) (string, int, <-chan phase5SMTPResult) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local SMTP listener is unavailable: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if tcpListener, ok := listener.(*net.TCPListener); ok {
		_ = tcpListener.SetDeadline(time.Now().Add(10 * time.Second))
	}
	results := make(chan phase5SMTPResult, 1)
	go func() {
		captured := phase5SMTPResult{}
		defer func() { results <- captured }()
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			captured.err = acceptErr
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(connection)
		writer := bufio.NewWriter(connection)
		writeResponse := func(response string) error {
			if _, writeErr := writer.WriteString(response); writeErr != nil {
				return writeErr
			}
			return writer.Flush()
		}
		if captured.err = writeResponse("220 localhost ESMTP phase5\r\n"); captured.err != nil {
			return
		}
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				captured.err = readErr
				return
			}
			line = strings.TrimRight(line, "\r\n")
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "EHLO ") || strings.HasPrefix(upper, "HELO "):
				captured.err = writeResponse("250-localhost\r\n250-AUTH PLAIN\r\n250 8BITMIME\r\n")
			case strings.HasPrefix(upper, "AUTH PLAIN"):
				fields := strings.Fields(line)
				encoded := ""
				if len(fields) >= 3 {
					encoded = fields[2]
				} else {
					if captured.err = writeResponse("334 \r\n"); captured.err != nil {
						return
					}
					encoded, captured.err = reader.ReadString('\n')
					encoded = strings.TrimSpace(encoded)
				}
				if captured.err == nil {
					captured.auth, captured.err = base64.StdEncoding.DecodeString(encoded)
				}
				if captured.err == nil {
					captured.err = writeResponse("235 2.7.0 authenticated\r\n")
				}
			case strings.HasPrefix(upper, "MAIL FROM:"):
				captured.err = writeResponse("250 2.1.0 sender ok\r\n")
			case strings.HasPrefix(upper, "RCPT TO:"):
				recipient := strings.TrimSpace(line[len("RCPT TO:"):])
				recipient = strings.TrimPrefix(recipient, "<")
				recipient = strings.TrimSuffix(recipient, ">")
				captured.recipients = append(captured.recipients, recipient)
				captured.err = writeResponse("250 2.1.5 recipient ok\r\n")
			case upper == "DATA":
				if captured.err = writeResponse("354 end with <CRLF>.<CRLF>\r\n"); captured.err != nil {
					return
				}
				captured.message, captured.err = textproto.NewReader(reader).ReadDotBytes()
				if captured.err == nil {
					captured.err = writeResponse("250 2.0.0 queued\r\n")
				}
			case upper == "QUIT":
				captured.err = writeResponse("221 2.0.0 bye\r\n")
				return
			default:
				captured.err = writeResponse("250 2.0.0 ok\r\n")
			}
			if captured.err != nil {
				return
			}
		}
	}()
	return "localhost", listener.Addr().(*net.TCPAddr).Port, results
}

func closePhase5WebSocket(connection *websocket.Conn) {
	if connection == nil {
		return
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "phase5 test complete"), deadline)
	_ = connection.Close()
}

func TestOAuthExpiredPublicStateIsPrunedBeforeQuotaGrowth(t *testing.T) {
	fixture := newOAuthPhase4NegativeFixture(t, nil, nil)

	clientStateKey := "clients/" + sha256Base64URL(fixture.clientID) + ".json"
	clientText, err := oauthReadState(fixture.stateDirectory, clientStateKey)
	if err != nil {
		t.Fatal(err)
	}
	var clientState map[string]interface{}
	if err := json.Unmarshal([]byte(clientText), &clientState); err != nil {
		t.Fatal(err)
	}
	clientState["expiresAt"] = float64(time.Now().Add(-time.Minute).UnixMilli())
	expiredClient, err := json.Marshal(clientState)
	if err != nil {
		t.Fatal(err)
	}
	if err := oauthWriteState(fixture.stateDirectory, clientStateKey, string(expiredClient)); err != nil {
		t.Fatal(err)
	}

	registerBody := fmt.Sprintf(`{"redirect_uris":[%q],"client_name":"prune replacement","token_endpoint_auth_method":"none","grant_types":["authorization_code"],"response_types":["code"],"scope":"nyan8:read"}`, fixture.redirectURI)
	registerResponse := serveMCPPhase12Request(fixture.router, newOAuthPhase4Request(http.MethodPost, "/oauth_register", registerBody, "application/json"))
	if registerResponse.Code != http.StatusCreated {
		t.Fatalf("replacement DCR status=%d body=%q", registerResponse.Code, registerResponse.Body.String())
	}
	if _, err := oauthReadState(fixture.stateDirectory, clientStateKey); !os.IsNotExist(err) {
		t.Fatalf("expired client read error=%v, want deletion before DCR growth", err)
	}

	replacementClient, _ := oauthPhase4JSONBody(t, registerResponse)["client_id"].(string)
	fixture.clientID = replacementClient
	pending := oauthPhase4NegativeBeginAuthorization(t, fixture, "expired-request-prune", strings.Repeat("q", 64))
	requestStateKey := oauthPhase4NegativeExpireState(t, fixture.stateDirectory, "requests", pending.requestID)
	_ = oauthPhase4NegativeBeginAuthorization(t, fixture, "replacement-request", strings.Repeat("w", 64))
	if _, err := oauthReadState(fixture.stateDirectory, requestStateKey); !os.IsNotExist(err) {
		t.Fatalf("expired request read error=%v, want deletion before authorization growth", err)
	}
}

func TestOAuthStateListIsRootedPrivateAndDeterministic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "oauth")
	for _, item := range []struct {
		key   string
		value string
	}{
		{key: "clients/z.json", value: `{"ok":true}`},
		{key: "clients/a.json", value: `{"ok":true}`},
	} {
		if err := oauthWriteState(root, item.key, item.value); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := oauthListState(root, "clients")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"clients/a.json", "clients/z.json"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("OAuth state keys=%#v, want %#v", keys, want)
	}
	for _, namespace := range []string{"../clients", "clients/other", ".", ""} {
		if _, err := oauthListState(root, namespace); err == nil {
			t.Errorf("unsafe namespace %q was accepted", namespace)
		}
	}
}

func TestProductionWebSocketExposureIsBounded(t *testing.T) {
	t.Run("configuration contract", func(t *testing.T) {
		backing := map[string]interface{}{"websocket": false}
		if apiWebSocketAllowed(backing) {
			t.Fatal("MCP Tool backing API allows WebSocket upgrades when explicitly disabled")
		}
	})

	t.Run("root disabled", func(t *testing.T) {
		previous := globalConfig
		allowRoot := false
		globalConfig.WebSocket = WebSocketConfig{AllowRoot: &allowRoot, MaxConnections: 32}
		t.Cleanup(func() { globalConfig = previous })
		router := gin.New()
		router.Any("/", handleRequest)
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Version", "13")
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("disabled root WebSocket status=%d, want 403", response.Code)
		}
	})

	t.Run("global limiter", func(t *testing.T) {
		firstRelease, firstOK := acquireWebSocketConnection(1)
		if !firstOK {
			t.Fatal("first WebSocket slot was rejected")
		}
		if _, secondOK := acquireWebSocketConnection(1); secondOK {
			firstRelease()
			t.Fatal("connection beyond the global WebSocket limit was accepted")
		}
		firstRelease()
		thirdRelease, thirdOK := acquireWebSocketConnection(1)
		if !thirdOK {
			t.Fatal("released WebSocket slot was not reusable")
		}
		thirdRelease()
	})
}

func TestProxyProtocolV2PreservesClientAddressAndPayload(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	wrappedListener, err := newProxyProtocolListener(listener, ProxyProtocolConfig{
		Enabled:      true,
		TrustedCIDRs: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatal(err)
	}

	type acceptedResult struct {
		remote  string
		payload string
		err     error
	}
	resultChannel := make(chan acceptedResult, 1)
	go func() {
		conn, acceptErr := wrappedListener.Accept()
		if acceptErr != nil {
			resultChannel <- acceptedResult{err: acceptErr}
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		payload := make([]byte, len("tls-client-hello-placeholder"))
		_, readErr := io.ReadFull(conn, payload)
		resultChannel <- acceptedResult{remote: conn.RemoteAddr().String(), payload: string(payload), err: readErr}
	}()

	client, err := net.DialTimeout("tcp", listener.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	source := net.ParseIP("203.0.113.17")
	destination := net.ParseIP("127.0.0.1")
	header := testProxyProtocolV2Header(t, source, destination, 45678, 10443)
	if _, err := client.Write(append(header, []byte("tls-client-hello-placeholder")...)); err != nil {
		client.Close()
		t.Fatal(err)
	}
	_ = client.Close()

	select {
	case result := <-resultChannel:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.remote != "203.0.113.17:45678" {
			t.Fatalf("proxied RemoteAddr=%q, want 203.0.113.17:45678", result.remote)
		}
		if result.payload != "tls-client-hello-placeholder" {
			t.Fatalf("payload=%q", result.payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for PROXY protocol listener")
	}
}

func TestProxyProtocolV2IPv6LocalAndTLV(t *testing.T) {
	t.Run("IPv6 with ignored TLV", func(t *testing.T) {
		source := net.ParseIP("2001:db8::17").To16()
		destination := net.ParseIP("2001:db8::44").To16()
		payload := append(append([]byte(nil), source...), destination...)
		ports := make([]byte, 4)
		binary.BigEndian.PutUint16(ports[0:2], 45678)
		binary.BigEndian.PutUint16(ports[2:4], 10443)
		payload = append(payload, ports...)
		payload = append(payload, 0x01, 0x00, 0x03, 't', 'l', 'v')
		header := append([]byte(nil), proxyProtocolV2Signature...)
		header = append(header, 0x21, 0x21, byte(len(payload)>>8), byte(len(payload)))
		reader := bufio.NewReader(bytes.NewReader(append(append(header, payload...), []byte("TLS")...)))
		remote, err := readProxyProtocolV2Header(reader)
		if err != nil {
			t.Fatal(err)
		}
		if got := remote.String(); got != "[2001:db8::17]:45678" {
			t.Fatalf("IPv6 RemoteAddr=%q", got)
		}
		remainder, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if string(remainder) != "TLS" {
			t.Fatalf("post-header bytes=%q", remainder)
		}
	})

	t.Run("LOCAL keeps transport peer", func(t *testing.T) {
		header := append([]byte(nil), proxyProtocolV2Signature...)
		header = append(header, 0x20, 0x00, 0x00, 0x00)
		reader := bufio.NewReader(bytes.NewReader(append(header, []byte("TLS")...)))
		remote, err := readProxyProtocolV2Header(reader)
		if err != nil {
			t.Fatal(err)
		}
		if remote != nil {
			t.Fatalf("LOCAL RemoteAddr=%v, want transport peer fallback", remote)
		}
		remainder, err := io.ReadAll(reader)
		if err != nil || string(remainder) != "TLS" {
			t.Fatalf("LOCAL post-header bytes=%q err=%v", remainder, err)
		}
	})
}

func TestSlowProxyHeaderDoesNotBlockAcceptLoop(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	wrappedListener, err := newProxyProtocolListener(listener, ProxyProtocolConfig{
		Enabled:      true,
		TrustedCIDRs: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatal(err)
	}

	stalledClient, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stalledServer, err := wrappedListener.Accept()
	if err != nil {
		stalledClient.Close()
		t.Fatal(err)
	}
	stalledDone := make(chan struct{})
	go func() {
		_ = stalledServer.RemoteAddr()
		close(stalledDone)
	}()

	secondResult := make(chan error, 1)
	go func() {
		conn, acceptErr := wrappedListener.Accept()
		if acceptErr != nil {
			secondResult <- acceptErr
			return
		}
		defer conn.Close()
		buffer := make([]byte, 3)
		_, readErr := io.ReadFull(conn, buffer)
		if readErr == nil && string(buffer) != "TLS" {
			readErr = fmt.Errorf("second payload=%q", buffer)
		}
		secondResult <- readErr
	}()
	secondClient, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		stalledClient.Close()
		stalledServer.Close()
		t.Fatal(err)
	}
	header := testProxyProtocolV2Header(t, net.ParseIP("198.51.100.20"), net.ParseIP("127.0.0.1"), 44000, 10443)
	if _, err := secondClient.Write(append(header, []byte("TLS")...)); err != nil {
		secondClient.Close()
		stalledClient.Close()
		stalledServer.Close()
		t.Fatal(err)
	}
	_ = secondClient.Close()
	select {
	case err := <-secondResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled PROXY peer blocked acceptance of a second connection")
	}
	_ = stalledClient.Close()
	_ = stalledServer.Close()
	select {
	case <-stalledDone:
	case <-time.After(time.Second):
		t.Fatal("stalled connection did not unblock after close")
	}
}

func TestRequiredProxyProtocolRejectsDirectTraffic(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	payload := "direct-private-backend-probe"
	errorChannel := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			errorChannel <- acceptErr
			return
		}
		defer conn.Close()
		wrapped := &proxyProtocolConn{Conn: conn, headerTimeout: time.Second}
		_, readErr := wrapped.Read(make([]byte, len(payload)))
		errorChannel <- readErr
	}()
	client, err := net.DialTimeout("tcp", listener.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(client, payload); err != nil {
		client.Close()
		t.Fatal(err)
	}
	_ = client.Close()
	select {
	case err := <-errorChannel:
		if err == nil || !strings.Contains(err.Error(), "invalid PROXY protocol v2 signature") {
			t.Fatalf("direct connection error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for required PROXY protocol rejection")
	}
}

func TestProxyProtocolClientAddressesIsolateRateLimits(t *testing.T) {
	parseRemote := func(source string, port int) string {
		header := testProxyProtocolV2Header(t, net.ParseIP(source), net.ParseIP("127.0.0.1"), port, 10443)
		remote, err := readProxyProtocolV2Header(bufio.NewReader(bytes.NewReader(header)))
		if err != nil {
			t.Fatal(err)
		}
		return remote.String()
	}
	first := parseRemote("198.51.100.10", 41000)
	second := parseRemote("198.51.100.11", 42000)
	mcpRateBuckets.Lock()
	previousBuckets := mcpRateBuckets.Buckets
	previousCleanup := mcpRateBuckets.LastCleanup
	mcpRateBuckets.Buckets = make(map[string]mcpRateBucket)
	mcpRateBuckets.LastCleanup = time.Time{}
	mcpRateBuckets.Unlock()
	t.Cleanup(func() {
		mcpRateBuckets.Lock()
		mcpRateBuckets.Buckets = previousBuckets
		mcpRateBuckets.LastCleanup = previousCleanup
		mcpRateBuckets.Unlock()
	})
	limit := &MCPRateLimit{Requests: 1, Window: "1m"}
	now := time.Now()
	if allowed, _ := mcpRateLimitAllows("proxy-rate-test", limit, first, now); !allowed {
		t.Fatal("first client was unexpectedly rate limited")
	}
	if allowed, _ := mcpRateLimitAllows("proxy-rate-test", limit, second, now); !allowed {
		t.Fatal("second client shared the first client's rate bucket")
	}
	if allowed, _ := mcpRateLimitAllows("proxy-rate-test", limit, first, now); allowed {
		t.Fatal("first client exceeded its own rate bucket")
	}
}

func TestClientIPIgnoresSpoofableForwardingHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	request.RemoteAddr = "203.0.113.44:54321"
	request.Header.Set("X-Forwarded-For", "198.51.100.99")
	request.Header.Set("X-Real-IP", "198.51.100.98")
	if got := getClientIP(request); got != "203.0.113.44" {
		t.Fatalf("client IP=%q, want authenticated RemoteAddr", got)
	}
}

func TestProxyProtocolValidation(t *testing.T) {
	valid := ProxyProtocolConfig{Enabled: true, TrustedCIDRs: []string{"127.0.0.1/32"}}
	if _, err := validateProxyProtocolConfig(valid); err != nil {
		t.Fatalf("valid PROXY protocol config: %v", err)
	}
	invalid := []ProxyProtocolConfig{
		{Enabled: true},
		{Enabled: true, TrustedCIDRs: []string{"not-a-cidr"}},
		{Enabled: true, TrustedCIDRs: []string{"127.0.0.0/8"}},
		{Enabled: true, TrustedCIDRs: []string{"127.0.0.1/32", "127.0.0.1/32"}},
	}
	for index, config := range invalid {
		if _, err := validateProxyProtocolConfig(config); err == nil {
			t.Errorf("invalid PROXY protocol config %d was accepted", index)
		}
	}
}

func testProxyProtocolV2Header(t *testing.T, sourceIP, destinationIP net.IP, sourcePort, destinationPort int) []byte {
	t.Helper()
	source := sourceIP.To4()
	destination := destinationIP.To4()
	if source == nil || destination == nil || sourcePort < 1 || sourcePort > 65535 || destinationPort < 1 || destinationPort > 65535 {
		t.Fatal("invalid IPv4 PROXY protocol test address")
	}
	header := append([]byte(nil), proxyProtocolV2Signature...)
	header = append(header, 0x21, 0x11, 0x00, 0x0c)
	header = append(header, source...)
	header = append(header, destination...)
	ports := make([]byte, 4)
	binary.BigEndian.PutUint16(ports[0:2], uint16(sourcePort))
	binary.BigEndian.PutUint16(ports[2:4], uint16(destinationPort))
	return append(header, ports...)
}
