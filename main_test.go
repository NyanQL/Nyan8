package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestAdjustConfigPathsResolvesFromConfigDir(t *testing.T) {
	initTestLogger()

	configBaseDir := t.TempDir()
	config := Config{
		CertFile:          "ssl/localhost.crt",
		KeyFile:           "ssl/localhost.key",
		JavaScriptInclude: []string{"javascript/base.js"},
		Log:               LogConfig{Filename: "logs/nyan8.log"},
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

func TestBuildToolsListSkipsPublicAPI(t *testing.T) {
	initTestLogger()

	tempDir := t.TempDir()
	t.Chdir(tempDir)
	apiJSON := []byte(`{
  "assets": {
    "type": "public",
    "path": "./public"
  },
  "hello": {
    "script": "./hello.js",
    "description": "hello API"
  }
}`)
	if err := os.WriteFile(filepath.Join(tempDir, "api.json"), apiJSON, 0644); err != nil {
		t.Fatal(err)
	}

	result := buildToolsList()
	tools, ok := result["tools"].([]map[string]any)
	if !ok {
		t.Fatalf("tools has unexpected type: %T", result["tools"])
	}
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1: %#v", len(tools), tools)
	}
	if got, want := tools[0]["name"], "hello"; got != want {
		t.Fatalf("tool name = %v, want %q", got, want)
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
	assertMCPToolDescription(t, "sub/target", "included target")

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

func TestJSONRPCAndMCPUseUpdatedDefinition(t *testing.T) {
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
	assertMCPToolDescription(t, "hot", "first")
	if output := callJS("hot", map[string]interface{}{}, nil); !containsJSONValue([]byte(output), "value", "v1") {
		t.Fatalf("MCP call output=%q, want v1", output)
	}
	setAPIFiles(apiPath, map[string]interface{}{"hot": map[string]interface{}{"script": "./v2.js", "description": "second"}})
	assertJSONRPCValue(t, router, "v2")
	assertMCPToolDescription(t, "hot", "second")
	if output := callJS("hot", map[string]interface{}{}, nil); !containsJSONValue([]byte(output), "value", "v2") {
		t.Fatalf("MCP call output=%q, want v2", output)
	}
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

func assertMCPToolDescription(t *testing.T, name, wantDescription string) {
	t.Helper()
	result := buildToolsList()
	tools, ok := result["tools"].([]map[string]interface{})
	if !ok {
		t.Fatalf("tools type=%T", result["tools"])
	}
	for _, tool := range tools {
		if tool["name"] == name {
			if tool["description"] != wantDescription {
				t.Fatalf("tool description=%v, want %q", tool["description"], wantDescription)
			}
			return
		}
	}
	t.Fatalf("tool %q not found in %#v", name, tools)
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
