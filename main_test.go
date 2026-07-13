package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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
