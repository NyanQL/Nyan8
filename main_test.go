package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
}

func writeHotReloadTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
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

func waitForHotReloadSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}
