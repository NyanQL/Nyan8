package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/net/http2"
	"io"
	"log"
	"log/slog"
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/natefinch/lumberjack"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/crypto/argon2"
)

func initTestLogger() {
	logger = log.New(os.Stdout, "", 0)
	globalConfig = Config{}
	servicePaths = serviceFilePaths{}
}

type headerTestTransport func(*http.Request) (*http.Response, error)

func (transport headerTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestJSONAPIAdditionalHeaders(t *testing.T) {
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	for _, functionName := range []string{"nyanJsonAPI", "nyanCallAPI"} {
		for _, tc := range []struct {
			name, argument string
			wantHeaders    map[string]string
			wantError      bool
		}{
			{name: "omitted"},
			{name: "empty_object", argument: `{}`},
			{name: "empty_json_object", argument: `'{}'`},
			{name: "null", argument: `null`},
			{name: "json_null", argument: `'null'`},
			{name: "object", argument: `{"Authorization":"Bearer test-token","X-Audit":"sent","Content-Type":"application/custom+json"}`, wantHeaders: map[string]string{"Authorization": "Bearer test-token", "X-Audit": "sent", "Content-Type": "application/custom+json"}},
			{name: "json_string", argument: `'{"Authorization":"Bearer test-token","X-Audit":"sent","Content-Type":"application/custom+json"}'`, wantHeaders: map[string]string{"Authorization": "Bearer test-token", "X-Audit": "sent", "Content-Type": "application/custom+json"}},
			{name: "object_values", argument: `{"X-Number":123,"X-Boolean":true}`, wantHeaders: map[string]string{"X-Number": "123", "X-Boolean": "true"}},
			{name: "json_null_value", argument: `'{"X-Empty":null}'`, wantHeaders: map[string]string{"X-Empty": ""}},
			{name: "invalid_json", argument: `'not-json'`, wantError: true},
			{name: "trailing_json", argument: `'{}{}'`, wantError: true},
			{name: "json_array", argument: `'[]'`, wantError: true},
			{name: "json_numeric_value", argument: `'{"X-Number":123}'`, wantError: true},
			{name: "undefined", argument: `undefined`, wantError: true},
			{name: "number", argument: `123`, wantError: true},
		} {
			t.Run(functionName+"/"+tc.name, func(t *testing.T) {
				requests := 0
				http.DefaultTransport = headerTestTransport(func(request *http.Request) (*http.Response, error) {
					requests++
					if request.Method != http.MethodPost || request.URL.String() != "http://headers.test/echo" {
						t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
					}
					body, err := io.ReadAll(request.Body)
					if err != nil || string(body) != `{"message":"hello"}` {
						t.Fatalf("body=%q error=%v", body, err)
					}
					wantHeaders := map[string]string{"Content-Type": "application/json", "Authorization": "Basic dXNlcjpwYXNz"}
					for name, value := range tc.wantHeaders {
						wantHeaders[name] = value
					}
					for name, want := range wantHeaders {
						values, exists := request.Header[http.CanonicalHeaderKey(name)]
						if !exists || len(values) != 1 || values[0] != want {
							t.Errorf("header %s=%q, want %q", name, values, want)
						}
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: request}, nil
				})
				vm := goja.New()
				setupGojaVMWithSnapshot(vm, nil, nil)
				script := functionName + `("http://headers.test/echo",'{"message":"hello"}',"user","pass"`
				if tc.argument != "" {
					script += "," + tc.argument
				}
				value, err := vm.RunString(script + ")")
				if tc.wantError {
					if err == nil || !strings.Contains(err.Error(), "Invalid header JSON:") || requests != 0 {
						t.Fatalf("invalid headers: error=%v requests=%d, want exception before sending", err, requests)
					}
					return
				}
				if err != nil || requests != 1 || value.String() != `{"ok":true}` {
					t.Fatalf("value=%v error=%v requests=%d, want response and one request", value, err, requests)
				}
			})
		}
	}
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

func TestRootHTTPChecksMatchNamedEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldSnapshot, oldConfig, oldPaths, oldContext := currentAPISnapshot(), globalConfig, servicePaths, ginContext
	t.Cleanup(func() {
		publishAPISnapshot(oldSnapshot)
		globalConfig, servicePaths, ginContext = oldConfig, oldPaths, oldContext
	})
	globalConfig = Config{}
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:{checked:true}});`
	const denyParam = `({success:false,status:403,result:{reason:"input denied"}});`
	const denyOut = `({success:false,status:409,result:{reason:"output denied"}});`
	for _, tc := range []struct {
		name, param, out, main, paramKey, outKey, order, result string
		status                                                  int
		checkOnly                                               bool
	}{
		{name: "allow", param: allow, out: allow, status: 201, order: "param,main,out,push,", result: `{"status":201,"value":"日本語"}`},
		{name: "no checks", status: 201, order: "main,push,", result: `{"status":201,"value":"日本語"}`},
		{name: "param denied", param: denyParam, out: allow, status: 403, order: "param,", result: `{"success":false,"status":403,"result":{"reason":"input denied"}}`},
		{name: "param false with 200", param: `({success:false,status:200,result:"denied"});`, out: allow, status: 200, order: "param,", result: `{"success":false,"status":200,"result":"denied"}`},
		{name: "param non-200", param: `({success:true,status:202,result:"pending"});`, out: allow, status: 201, order: "param,main,out,push,", result: `{"status":201,"value":"日本語"}`},
		{name: "out denied", param: allow, out: denyOut, status: 409, order: "param,main,out,", result: `{"success":false,"status":409,"result":{"reason":"output denied"}}`},
		{name: "out non-200", param: allow, out: `({success:true,status:202,result:"pending"});`, status: 201, order: "param,main,out,push,", result: `{"status":201,"value":"日本語"}`},
		{name: "checkOnly", param: allow, out: allow, checkOnly: true, status: 200, order: "param,", result: `{"success":true,"status":200,"result":{"checked":true}}`},
		{name: "checkOnly denied", param: denyParam, out: allow, checkOnly: true, status: 403, order: "param,", result: `{"success":false,"status":403,"result":{"reason":"input denied"}}`},
		{name: "checkOnly without param", out: allow, checkOnly: true, status: 404, order: "", result: `{"success":false,"status":404,"result":{"message":"No check script for this API"}}`},
		{name: "lowercase aliases", param: allow, out: denyOut, paramKey: "paramcheck", outKey: "outcheck", status: 409, order: "param,main,out,", result: `{"success":false,"status":409,"result":{"reason":"output denied"}}`},
		{name: "legacy check", param: denyParam, paramKey: "check", status: 403, order: "param,", result: `{"success":false,"status":403,"result":{"reason":"input denied"}}`},
		{name: "param exception", param: `throw new Error("param failed");`, out: allow, status: 500, order: "param,"},
		{name: "param fractional status", param: `({success:true,status:200.5});`, out: allow, status: 500, order: "param,"},
		{name: "out fractional status", param: allow, out: `({success:true,status:200.5});`, status: 500, order: "param,main,out,"},
		{name: "param informational status", param: `({success:true,status:100});`, out: allow, status: 500, order: "param,"},
		{name: "checkOnly fractional status", param: `({success:true,status:200.5});`, out: allow, checkOnly: true, status: 500, order: "param,"},
		{name: "param invalid", param: `({success:true});`, out: allow, status: 500, order: "param,"},
		{name: "param missing", param: "missing", out: allow, status: 500, order: ""},
		{name: "out exception", param: allow, out: `throw new Error("out failed");`, status: 500, order: "param,main,out,"},
		{name: "out invalid", param: allow, out: `"not JSON";`, status: 500, order: "param,main,out,"},
		{name: "out missing", param: allow, out: "missing", status: 500, order: "param,main,"},
		{name: "main exception", param: allow, out: allow, main: `throw new Error("main failed");`, status: 500, order: "param,main,"},
		{name: "main invalid JSON", param: allow, out: allow, main: `"not JSON";`, status: 500, order: "param,main,"},
		{name: "main missing status", param: allow, out: allow, main: `JSON.stringify({value:"missing status"});`, status: 500, order: "param,main,"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, key := t.TempDir(), "root HTTP checks: "+t.Name()
			t.Cleanup(func() { storage.Delete(key) })
			writeScript := func(stage, body string) string {
				path := filepath.Join(dir, stage+".js")
				if body != "missing" {
					prefix := fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, key, key, stage+",")
					if stage == "param" || stage == "out" {
						prefix += `if(nyanAllParams.api!=="nested/target" || nyanAllParams.value!=="payload" || nyanGetCookie("session")!=="test-cookie" || nyanGetRequestHeaders()["X-Test"]!=="test-header") throw new Error("wrong check context");`
					}
					if stage == "out" {
						prefix += `if(nyanAllParams.nyan_output.status!==201 || nyanAllParams.nyan_output.contentType!=="application/json" || JSON.parse(nyanAllParams.nyan_output_body).value!=="日本語" || nyanAllParams.nyan_output_body_base64!=="eyJzdGF0dXMiOjIwMSwidmFsdWUiOiLml6XmnKzoqp4ifQ==") throw new Error("wrong output metadata");`
					}
					writeHotReloadTestFile(t, path, prefix+body)
				}
				return path
			}
			main := tc.main
			if main == "" {
				main = `JSON.stringify({status:201,value:"日本語"});`
			}
			entry := map[string]interface{}{"script": writeScript("main", main), "push": "root-check-sink"}
			paramKey, outKey := tc.paramKey, tc.outKey
			if paramKey == "" {
				paramKey = "paramCheck"
			}
			if outKey == "" {
				outKey = "outCheck"
			}
			if tc.param != "" {
				entry[paramKey] = writeScript("param", tc.param)
			}
			if tc.out != "" {
				entry[outKey] = writeScript("out", tc.out)
			}
			definitions := map[string]interface{}{
				"nested/target":   entry,
				"root-check-sink": map[string]interface{}{"paramCheck": writeScript("push", allow), "script": filepath.Join(dir, "sink.js")},
			}
			// Only the Push check is marked, so checkOnly cannot hide a Push invocation.
			writeHotReloadTestFile(t, filepath.Join(dir, "sink.js"), `"message";`)
			rootPath := filepath.Join(dir, "api.json")
			data, err := json.Marshal(definitions)
			if err != nil {
				t.Fatal(err)
			}
			writeHotReloadTestFile(t, rootPath, string(data))
			loaded, err := readAPIConfigFile(rootPath, dir)
			if err != nil {
				t.Fatal(err)
			}
			publishAPISnapshot(loaded.Snapshot)
			servicePaths.API.Path = rootPath
			router := gin.New()
			router.Any("/", handleRequest)
			if err := registerDynamicEndpoints(router, dir); err != nil {
				t.Fatal(err)
			}
			for _, input := range []string{"query", "JSON", "form"} {
				t.Run(input, func(t *testing.T) {
					var previous map[string]interface{}
					for _, path := range []string{"/nested/target", "/"} {
						storage.Delete(key)
						params := url.Values{"api": {"nested/target"}, "value": {"payload"}}
						if tc.checkOnly {
							params.Set("nyan_mode", "checkOnly")
						}
						var request *http.Request
						switch input {
						case "query":
							request = httptest.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
						case "JSON":
							values := map[string]string{}
							for key := range params {
								values[key] = params.Get(key)
							}
							body, _ := json.Marshal(values)
							request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
							request.Header.Set("Content-Type", "application/json")
						case "form":
							request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(params.Encode()))
							request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
						}
						request.AddCookie(&http.Cookie{Name: "session", Value: "test-cookie"})
						request.Header.Set("X-Test", "test-header")
						recorder := httptest.NewRecorder()
						router.ServeHTTP(recorder, request)
						if recorder.Code != tc.status {
							t.Fatalf("%s status=%d body=%s, want %d", path, recorder.Code, recorder.Body.String(), tc.status)
						}
						order, _ := storage.Load(key)
						if order == nil {
							order = ""
						}
						if order != tc.order {
							t.Fatalf("%s order=%q, want %q", path, order, tc.order)
						}
						var got map[string]interface{}
						if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						if tc.result != "" {
							var want map[string]interface{}
							if err := json.Unmarshal([]byte(tc.result), &want); err != nil {
								t.Fatal(err)
							}
							if !reflect.DeepEqual(got, want) {
								t.Fatalf("%s body=%s, want %s", path, recorder.Body.String(), tc.result)
							}
						}
						if previous != nil && !reflect.DeepEqual(got, previous) {
							t.Fatalf("root body=%v, named endpoint body=%v", got, previous)
						}
						previous = got
					}
				})
			}
		})
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

func TestAPIEndpointAliasCollision(t *testing.T) {
	for _, test := range []struct {
		name string
		root string
	}{
		{"direct", `{"x":{},"api/x":{}}`},
		{"included", `{"x":{},"api":{"type":"include","path":"./child.json"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			rootPath := filepath.Join(dir, "api.json")
			writeHotReloadTestFile(t, rootPath, test.root)
			writeHotReloadTestFile(t, filepath.Join(dir, "child.json"), `{"x":{}}`)
			_, err := readAPIConfigFile(rootPath, dir)
			if err == nil {
				t.Fatal("conflicting endpoint paths were accepted")
			}
			for _, detail := range []string{`endpoint path "/api/x"`, `"x"`, `"api/x"`} {
				if !strings.Contains(err.Error(), detail) {
					t.Fatalf("error=%v, want %s", err, detail)
				}
			}
		})
	}
}

func TestAPIEndpointAliasesRemainAvailable(t *testing.T) {
	dir := t.TempDir()
	writeHotReloadTestFile(t, filepath.Join(dir, "script.js"), `JSON.stringify({status:200,api:nyanAllParams.api});`)
	loaded, err := loadMCPPhase12Config(dir, map[string]interface{}{
		"x":     map[string]interface{}{"script": "./script.js"},
		"api/y": map[string]interface{}{"script": "./script.js"},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	if err := registerDynamicEndpoints(router, dir); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, api string }{
		{"/x", "x"}, {"/api/x", "x"}, {"/api/y", "api/y"},
	} {
		response := serveMCPPhase12Request(router, httptest.NewRequest(http.MethodGet, test.path, nil))
		var body map[string]interface{}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK || body["api"] != test.api {
			t.Fatalf("%s: status=%d body=%s error=%v", test.path, response.Code, response.Body.String(), err)
		}
	}
}

func TestReloadRejectsAPIEndpointAliasCollision(t *testing.T) {
	initTestLogger()
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "api.json")
	childPath := filepath.Join(dir, "child.json")
	writeHotReloadTestFile(t, rootPath, `{"x":{},"api":{"type":"include","path":"./child.json"}}`)
	writeHotReloadTestFile(t, childPath, `{"y":{}}`)
	loaded, err := readAPIConfigFile(rootPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	previousSnapshot, previousManager := currentAPISnapshot(), backgroundRuntimes
	publishAPISnapshot(loaded.Snapshot)
	backgroundRuntimes = nil
	t.Cleanup(func() {
		publishAPISnapshot(previousSnapshot)
		backgroundRuntimes = previousManager
	})
	writeHotReloadTestFile(t, childPath, `{"x":{}}`)
	_, reloaded, err := reloadAPIConfigGraphIfChanged(rootPath, dir, loaded.Snapshot.FileStates)
	if err == nil || reloaded || !strings.Contains(err.Error(), `endpoint path "/api/x"`) {
		t.Fatalf("reload=%t error=%v, want rejected collision", reloaded, err)
	}
	if currentAPISnapshot() != loaded.Snapshot {
		t.Fatal("last good snapshot was replaced after rejected collision")
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
		return strings.Contains(logs.String(), `"msg":"api_hot_reload_completed","api_count":1`)
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
		return strings.Count(logs.String(), `"msg":"api_hot_reload_failed"`) == 1
	})
	time.Sleep(25 * time.Millisecond)
	if count := strings.Count(logs.String(), `"msg":"api_hot_reload_failed"`); count != 1 {
		t.Fatalf("reload error count=%d, want 1; logs=%q", count, logs.String())
	}

	writeHotReloadTestFile(t, apiPath, `{"fixed":{"description":"recovered"}}`)
	waitForHotReloadCondition(t, "watcher recovery", func() bool {
		_, exists := currentAPIFiles()["fixed"]
		return exists
	})
	waitForHotReloadCondition(t, "success log", func() bool {
		return strings.Contains(logs.String(), `"msg":"api_hot_reload_completed","api_count":1`)
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
				return newMCPPhase12Request(http.MethodPost, "/custom-mcp", strings.Repeat("x", int(defaultReceiveBytes)+1))
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

func TestMCPOAuthReferenceWhitespace(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	oauth := mcpPhase12Entry(t, definitions)["oauth"].(map[string]interface{})
	for key, value := range oauth {
		oauth[key] = " \t" + value.(string) + "\n "
	}
	writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	server := loaded.Snapshot.MCPServers["custom-mcp"]
	encoded, err := json.Marshal(server.OAuth)
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]interface{}
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	for key, value := range oauth {
		if normalized[key] != strings.TrimSpace(value.(string)) {
			t.Errorf("oauth.%s=%q, want normalized reference", key, normalized[key])
		}
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	response := mcpPhase2GapToolCall(router, `{}`, "Bearer test")
	body := oauthPhase4JSONBody(t, response)
	result, _ := body["result"].(map[string]interface{})
	if response.Code != http.StatusOK || result["isError"] != false {
		t.Fatalf("normalized OAuth hook failed: status=%d body=%s", response.Code, response.Body.String())
	}
	writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), `({authenticated:false,forbidden:false});`)
	response = mcpPhase2GapToolCall(router, `{}`, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("OAuth rejection was bypassed: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMCPOAuthInvalidReferences(t *testing.T) {
	for _, test := range []struct {
		name, reference, want string
		allBlank              bool
	}{
		{name: "missing", reference: "missing", want: "references invalid API"},
		{name: "wrong type", reference: "assets", want: "references invalid API"},
		{name: "blank", reference: " \t", want: "oauth.verifyAccess API name is required"},
		{name: "all blank", allBlank: true, want: "API name is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, definitions := newMCPPhase12Definitions(t)
			oauth := mcpPhase12Entry(t, definitions)["oauth"].(map[string]interface{})
			oauth["verifyAccess"] = test.reference
			if test.allBlank {
				for key := range oauth {
					oauth[key] = " \t"
				}
			}
			if _, err := loadMCPPhase12Config(dir, definitions); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestMCPWithoutOAuthRemainsAnonymous(t *testing.T) {
	for _, transport := range []string{"streamable_http", "stdio"} {
		for _, form := range []string{"omitted", "empty object", "empty references"} {
			t.Run(transport+"/"+form, func(t *testing.T) {
				dir, definitions := newMCPPhase12Definitions(t)
				mcp := mcpPhase12Entry(t, definitions)
				mcp["transport"] = transport
				delete(mcp, "redirectURIAllowedPrefixes")
				switch form {
				case "omitted":
					delete(mcp, "oauth")
				case "empty object":
					mcp["oauth"] = map[string]interface{}{}
				case "empty references":
					for key := range mcp["oauth"].(map[string]interface{}) {
						mcp["oauth"].(map[string]interface{})[key] = ""
					}
				}
				loaded, err := loadMCPPhase12Config(dir, definitions)
				if err != nil {
					t.Fatal(err)
				}
				server := loaded.Snapshot.MCPServers["custom-mcp"]
				principal, authenticated, forbidden := validateMCPAccessToken(loaded.Snapshot, server, mcpRuntimeURLs{}, "", "sample", nil)
				if mcpOAuthConfigured(server.OAuth) || !authenticated || forbidden || !reflect.DeepEqual(principal, map[string]interface{}{"anonymous": true}) {
					t.Fatalf("anonymous access changed: principal=%#v authenticated=%t forbidden=%t", principal, authenticated, forbidden)
				}
				if transport == "streamable_http" {
					router := publishMCPPhase12Snapshot(t, loaded)
					response := mcpPhase2GapToolCall(router, `{}`, "")
					body := oauthPhase4JSONBody(t, response)
					result, _ := body["result"].(map[string]interface{})
					if response.Code != http.StatusOK || result["isError"] != false {
						t.Fatalf("anonymous Tool call failed: status=%d body=%s", response.Code, response.Body.String())
					}
				}
			})
		}
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

func TestMCPToolChecksAcrossHTTPAndStdio(t *testing.T) {
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:{checked:true}});`
	const denyParam = `({success:false,status:403,result:{reason:"input denied"}});`
	const denyOut = `({success:false,status:409,result:{reason:"output denied"}});`
	const normalResult = `{"ok":true,"service":"Nyan8","items":[1,2,3]}`
	for _, tc := range []struct {
		name, param, out, main, paramKey, outKey, arguments, order, result, failure string
		checkOnly, isError                                                          bool
	}{
		{name: "allow", param: allow, out: allow, order: "param,main,out,", result: normalResult},
		{name: "no checks", order: "main,", result: normalResult},
		{name: "native object", param: allow, out: allow, main: `({ok:true,service:"Nyan8",items:[1,2,3]});`, order: "param,main,out,", result: normalResult},
		{name: "param denied", param: denyParam, out: allow, order: "param,", isError: true, result: `{"success":false,"status":403,"result":{"reason":"input denied"}}`},
		{name: "param non-200", param: `({success:true,status:202,result:"pending"});`, out: allow, order: "param,main,out,", result: normalResult},
		{name: "param false with 200", param: `({success:false,status:200,result:"denied"});`, out: allow, order: "param,", isError: true, result: `{"success":false,"status":200,"result":"denied"}`},
		{name: "out denied", param: allow, out: denyOut, order: "param,main,out,", isError: true, result: `{"success":false,"status":409,"result":{"reason":"output denied"}}`},
		{name: "out non-200", param: allow, out: `({success:true,status:202,result:"pending"});`, order: "param,main,out,", result: normalResult},
		{name: "checkOnly", param: allow, out: allow, checkOnly: true, order: "param,", result: `{"success":true,"status":200,"result":{"checked":true}}`},
		{name: "checkOnly denied", param: denyParam, out: allow, checkOnly: true, order: "param,", isError: true, result: `{"success":false,"status":403,"result":{"reason":"input denied"}}`},
		{name: "checkOnly without param", out: allow, checkOnly: true, isError: true, order: "", result: `{"success":false,"status":404,"result":{"message":"No check script for this API"}}`},
		{name: "lowercase aliases", param: allow, out: denyOut, paramKey: "paramcheck", outKey: "outcheck", order: "param,main,out,", isError: true, result: `{"success":false,"status":409,"result":{"reason":"output denied"}}`},
		{name: "legacy check", param: denyParam, paramKey: "check", order: "param,", isError: true, result: `{"success":false,"status":403,"result":{"reason":"input denied"}}`},
		{name: "param exception", param: `throw new Error("private param failure");`, out: allow, order: "param,", failure: "Tool paramCheck failed."},
		{name: "param fractional status", param: `({success:true,status:200.5});`, out: allow, order: "param,", failure: "Tool paramCheck failed."},
		{name: "out fractional status", param: allow, out: `({success:true,status:200.5});`, order: "param,main,out,", failure: "Tool outCheck failed."},
		{name: "param invalid", param: `({success:true});`, out: allow, order: "param,", failure: "Tool paramCheck failed."},
		{name: "param missing", param: "missing", out: allow, order: "", failure: "Tool paramCheck failed."},
		{name: "out exception", param: allow, out: `throw new Error("private out failure");`, order: "param,main,out,", failure: "Tool outCheck failed."},
		{name: "out invalid", param: allow, out: `"not JSON";`, order: "param,main,out,", failure: "Tool outCheck failed."},
		{name: "out missing", param: allow, out: "missing", order: "param,main,", failure: "Tool outCheck failed."},
		{name: "main exception", param: allow, out: allow, main: `throw new Error("private main failure");`, order: "param,main,", failure: "Tool execution failed."},
		{name: "main invalid JSON", param: allow, out: allow, main: `"not JSON";`, order: "param,main,", failure: "Tool returned invalid JSON."},
		{name: "output schema", param: allow, out: allow, main: `({ok:true});`, order: "param,main,out,", failure: "Tool result does not match outputSchema."},
		{name: "input schema before checkOnly", param: allow, out: allow, arguments: `{"nyan_mode":"checkOnly","value":123}`, order: "", failure: "Tool arguments do not match inputSchema."},
		{name: "param result oversized", param: fmt.Sprintf(`({success:false,status:403,result:"x".repeat(%d)});`, maxMCPToolResultBytes), out: allow, order: "param,", failure: "Tool check result is invalid or too large."},
		{name: "out result oversized", param: allow, out: fmt.Sprintf(`({success:false,status:409,result:"x".repeat(%d)});`, maxMCPToolResultBytes), order: "param,main,out,", failure: "Tool check result is invalid or too large."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, definitions := newMCPPhase12Definitions(t)
			key := "MCP checks: " + t.Name()
			t.Cleanup(func() { storage.Delete(key) })
			writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
			writeHotReloadTestFile(t, filepath.Join(dir, "data.txt"), "root-data")
			writeScript := func(stage, code string) string {
				path := filepath.Join(dir, stage+".js")
				if code != "missing" {
					prefix := fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, key, key, stage+",")
					prefix += `if(nyanAllParams.api!=="sample" || nyanAllParams.mcp_tool!=="sample" || !["phase2","local-process"].includes(nyanAllParams.mcp_principal.user_id) || nyanGetFile("data.txt")!=="root-data") throw new Error("wrong Tool context");`
					if stage == "out" {
						prefix += `if(nyanAllParams.nyan_output_status!==200 || nyanAllParams.nyan_output_content_type!=="application/json" || nyanAllParams.nyan_output.body!==nyanAllParams.nyan_output_body || !JSON.parse(nyanAllParams.nyan_output_body).ok) throw new Error("wrong output metadata");`
					}
					writeHotReloadTestFile(t, path, prefix+code)
				}
				return path
			}
			entry := definitions["sample"].(map[string]interface{})
			delete(entry, "paramCheck")
			delete(entry, "outCheck")
			main := tc.main
			if main == "" {
				main = `JSON.stringify({ok:true,service:"Nyan8",items:[1,2,3]});`
			}
			entry["script"] = writeScript("main", main)
			paramKey, outKey := tc.paramKey, tc.outKey
			if paramKey == "" {
				paramKey = "paramCheck"
			}
			if outKey == "" {
				outKey = "outCheck"
			}
			if tc.param != "" {
				entry[paramKey] = writeScript("param", tc.param)
			}
			if tc.out != "" {
				entry[outKey] = writeScript("out", tc.out)
			}
			entry["push"] = "checked-push"
			definitions["checked-push"] = map[string]interface{}{"script": writeScript("push", `"notification";`)}
			definitions["local-mcp"] = map[string]interface{}{"type": "mcp", "transport": "stdio", "tools": []interface{}{"sample"}}
			loaded, err := loadMCPPhase12Config(dir, definitions)
			if err != nil {
				t.Fatal(err)
			}
			// Give normal Tool outputs a schema that check responses do not match.
			for _, server := range loaded.Snapshot.MCPServers {
				tool := findMCPTool(server, "sample")
				tool.InputSchema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{"value": map[string]interface{}{"type": "string"}, "nyan_mode": map[string]interface{}{"type": "string"}}, "additionalProperties": false}
				tool.OutputSchema = map[string]interface{}{"type": "object", "required": []interface{}{"ok", "service", "items"}}
			}
			router := publishMCPPhase12Snapshot(t, loaded)
			arguments := tc.arguments
			if arguments == "" {
				arguments = `{"value":"input"}`
			}
			if tc.checkOnly {
				arguments = `{"value":"input","nyan_mode":"checkOnly"}`
			}
			for _, transport := range []string{"http", "stdio"} {
				t.Run(transport, func(t *testing.T) {
					storage.Delete(key)
					var envelope map[string]interface{}
					if transport == "http" {
						response := mcpPhase2GapToolCall(router, arguments, "Bearer phase2")
						if response.Code != http.StatusOK {
							t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
						}
						envelope = oauthPhase4JSONBody(t, response)
					} else {
						call := fmt.Sprintf(`{"jsonrpc":"2.0","id":"checked","method":"tools/call","params":{"name":"sample","arguments":%s}}`, arguments)
						input := mcpPhase12InitializeBody(mcpProtocol20251125) + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n" + call + "\n"
						var output bytes.Buffer
						if err := serveMCPStdio(strings.NewReader(input), &output, loaded.Snapshot, loaded.Snapshot.MCPServers["local-mcp"]); err != nil {
							t.Fatal(err)
						}
						lines := strings.Split(strings.TrimSpace(output.String()), "\n")
						if len(lines) != 2 {
							t.Fatalf("stdio output=%s", output.String())
						}
						if err := json.Unmarshal([]byte(lines[1]), &envelope); err != nil {
							t.Fatal(err)
						}
					}
					order, _ := storage.Load(key)
					if order == nil {
						order = ""
					}
					wantOrder := tc.order
					if !tc.checkOnly && !tc.isError && tc.failure == "" {
						wantOrder += "push,"
					}
					if order != wantOrder {
						t.Fatalf("order=%q, want %q", order, wantOrder)
					}
					result, ok := envelope["result"].(map[string]interface{})
					if !ok {
						t.Fatalf("not a Tool result: %v", envelope)
					}
					if result["isError"] != (tc.isError || tc.failure != "") {
						t.Fatalf("isError=%v", result["isError"])
					}
					content, ok := result["content"].([]interface{})
					if !ok || len(content) != 1 {
						t.Fatalf("content=%v", result["content"])
					}
					text, _ := content[0].(map[string]interface{})["text"].(string)
					if tc.failure != "" {
						if text != tc.failure {
							t.Fatalf("error=%q, want %q", text, tc.failure)
						}
						if _, exists := result["structuredContent"]; exists {
							t.Fatal("failed execution returned structured content")
						}
						return
					}
					var want, gotText interface{}
					if err := json.Unmarshal([]byte(tc.result), &want); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(text), &gotText); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(want, result["structuredContent"]) || !reflect.DeepEqual(want, gotText) {
						t.Fatalf("unexpected result=%v", result)
					}
				})
			}
		})
	}
}

func TestMCPOutCheckUsesReturnedJSONMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		status       int
	}{
		{name: "native object", script: `({status:201,value:"日本語"});`, status: 201},
		{name: "JSON text", script: `' { "status": 409, "value": "日本語" } ';`, status: 409},
		{name: "array", script: `[1,"日本語"];`, status: 200},
		{name: "number", script: `42;`, status: 200},
		{name: "JSON null", script: `"null";`, status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, key := t.TempDir(), "MCP output: "+t.Name()
			t.Cleanup(func() { storage.Delete(key) })
			mainPath, outPath := filepath.Join(dir, "main.js"), filepath.Join(dir, "out.js")
			writeHotReloadTestFile(t, mainPath, tc.script)
			writeHotReloadTestFile(t, outPath, fmt.Sprintf(`nyanSetItem(%q,JSON.stringify(nyanAllParams.nyan_output)); ({success:true,status:200,result:null});`, key))
			snapshot := newAPIConfigSnapshot(filepath.Join(dir, "api.json"), map[string]interface{}{"target": map[string]interface{}{"script": mainPath, "outCheck": outPath}}, nil, nil, nil, nil)
			result, failure := executeMCPTool(snapshot, &MCPToolConfig{Name: "tool", API: "target", InputSchema: map[string]interface{}{"type": "object"}}, nil, nil)
			if failure != "" {
				t.Fatal(failure)
			}
			body := result["content"].([]map[string]interface{})[0]["text"].(string)
			var observed struct {
				Status          int               `json:"status"`
				ContentType     string            `json:"contentType"`
				Body            string            `json:"body"`
				BodyBase64      string            `json:"bodyBase64"`
				BodyLength      int               `json:"bodyLength"`
				BodyLengthBytes int               `json:"bodyLengthBytes"`
				Headers         map[string]string `json:"headers"`
			}
			raw, ok := storage.Load(key)
			if !ok {
				t.Fatal("outCheck did not run")
			}
			if err := json.Unmarshal([]byte(raw.(string)), &observed); err != nil {
				t.Fatal(err)
			}
			if observed.Status != tc.status || observed.ContentType != "application/json" || observed.Body != body ||
				observed.BodyBase64 != base64.StdEncoding.EncodeToString([]byte(body)) || observed.BodyLength != len(body) ||
				observed.BodyLengthBytes != len(body) || observed.Headers == nil || len(observed.Headers) != 0 {
				t.Fatalf("output metadata=%s, result body=%q", raw, body)
			}
		})
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
  "log":{"EnableLogging":false,"Level":"debug"},
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
	decodeLogRecords(t, stderr.String())
	if !strings.Contains(stderr.String(), `"msg":"mcp_stdio_starting","api":"local_mcp"`) || !strings.Contains(stderr.String(), "stdio-process-log") {
		t.Fatalf("stderr did not receive startup/JavaScript logs: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), `"msg":"schedule_starting"`) || strings.Contains(stderr.String(), `"msg":"ws_client_starting"`) || strings.Contains(stderr.String(), `"msg":"api_hot_reload_enabled"`) {
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

func TestNyanSHA256Base64URLArguments(t *testing.T) {
	vm := goja.New()
	setupOAuthGojaVM(vm, nil, &MCPServerConfig{})
	value, err := vm.RunString(`
(function () {
  try { nyanSHA256Base64URL(); }
  catch (error) {
    return error instanceof TypeError && error.message === "nyanSHA256Base64URL requires a string";
  }
  return false;
})()`)
	if err != nil || !value.ToBoolean() {
		t.Fatalf("missing argument must throw a catchable TypeError: value=%v error=%v", value, err)
	}
	for _, tc := range []struct{ argument, want string }{
		{`""`, "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"},
		{`"abc"`, "ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0"},
		{`"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"`, "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		{`undefined`, "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"},
		{`null`, "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"},
		{`"abc", "ignored"`, "ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0"},
	} {
		t.Run(tc.argument, func(t *testing.T) {
			value, err := vm.RunString("nyanSHA256Base64URL(" + tc.argument + ")")
			if err != nil || value.String() != tc.want {
				t.Fatalf("hash=%v error=%v, want %q", value, err, tc.want)
			}
		})
	}
}

func TestOAuthRandomBase64URLDefaultAndSizes(t *testing.T) {
	for _, tc := range []struct {
		argument string
		bytes    int
	}{
		{argument: "", bytes: 32},
		{argument: "1", bytes: 1},
		{argument: "8", bytes: 8},
		{argument: "16", bytes: 16},
		{argument: "32", bytes: 32},
		{argument: "128", bytes: 128},
		{argument: "256", bytes: 256},
		{argument: "1024", bytes: 1024},
	} {
		t.Run("argument="+tc.argument, func(t *testing.T) {
			vm := goja.New()
			setupOAuthGojaVM(vm, nil, &MCPServerConfig{})
			value, err := vm.RunString("nyanRandomBase64URL(" + tc.argument + ");")
			if err != nil {
				t.Fatal(err)
			}
			encoded, ok := value.Export().(string)
			if !ok {
				t.Fatalf("result is not a string: %#v", value.Export())
			}
			decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
			if err != nil || len(decoded) != tc.bytes {
				t.Fatalf("decoded length=%d error=%v, want %d bytes", len(decoded), err, tc.bytes)
			}
			if len(encoded) != base64.RawURLEncoding.EncodedLen(tc.bytes) || strings.ContainsAny(encoded, "=+/\r\n") {
				t.Fatalf("unexpected Base64URL format: %q", encoded)
			}
		})
	}
	for _, argument := range []string{"0", "-1", "1025", "undefined", "null"} {
		t.Run("invalid="+argument, func(t *testing.T) {
			vm := goja.New()
			setupOAuthGojaVM(vm, nil, &MCPServerConfig{})
			_, err := vm.RunString("nyanRandomBase64URL(" + argument + ");")
			if err == nil || !strings.Contains(err.Error(), "outside the allowed range") {
				t.Fatalf("error=%v, want range exception", err)
			}
		})
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
				request := newMCPPhase12Request(http.MethodPost, "/oauth_token", strings.Repeat("x", int(defaultReceiveBytes)+1))
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

// OAuth checks use the restricted runtime and its state helpers, including in metadata routes.
func newOAuthChecksFixture(t *testing.T, api, param, out, body string) (*apiConfigLoadResult, http.Handler) {
	t.Helper()
	dir, definitions := newMCPPhase12Definitions(t)
	entry := definitions[api].(map[string]interface{})
	for _, stage := range []struct{ key, code, mark string }{
		{"paramCheck", param, "param"}, {"script", body, "main"}, {"outCheck", out, "out"},
	} {
		if stage.code == "" {
			delete(entry, stage.key)
			continue
		}
		path := filepath.Join(dir, stage.mark+".js")
		entry[stage.key] = path
		if stage.code == "missing" {
			continue
		}
		prefix := fmt.Sprintf(`
if(typeof nyanOAuthRead!=="function" || typeof nyanHostExec!=="undefined" || typeof nyanGetFile!=="undefined") throw new Error("wrong runtime");
if(nyanAllParams.oauth_api!==%q || nyanAllParams.path!==%q) throw new Error("wrong API context");
nyanOAuthWrite("checks/order.json",JSON.stringify(JSON.parse(nyanOAuthRead("checks/order.json")||'""')+%q));
`, api, "/"+api, stage.mark+",")
		if stage.mark == "out" {
			prefix += `nyanOAuthWrite("checks/output.json",JSON.stringify(nyanAllParams.nyan_output));
if(nyanAllParams.nyan_output_body!==nyanAllParams.nyan_output.body || nyanAllParams.nyan_output_status!==nyanAllParams.nyan_output.status || nyanAllParams.nyan_output_body_base64!==nyanAllParams.nyan_output.bodyBase64) throw new Error("bad aliases");`
		}
		writeHotReloadTestFile(t, path, prefix+stage.code)
	}
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, publishMCPPhase12Snapshot(t, loaded)
}

func assertOAuthCheckOrder(t *testing.T, loaded *apiConfigLoadResult, want string) {
	t.Helper()
	raw, err := oauthReadState(loaded.Snapshot.MCPServers["custom-mcp"].OAuth.StateDirectory, "checks/order.json")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var got string
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
	}
	if got != want {
		t.Fatalf("execution order=%q, want %q", got, want)
	}
}

func TestOAuthChecksHTTPRoutes(t *testing.T) {
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:{checked:true}});`
	const main = `({status:201,contentType:"application/json",headers:{"Set-Cookie":"test=ok; Secure; HttpOnly; SameSite=Lax","Location":"https://client.example.test/callback"},body:{message:"日本語"}});`
	for _, endpoint := range []struct {
		api, method, contentType, body string
		metadata                       bool
	}{
		{"oauth_authorize", "GET", "", "", false},
		{"oauth_token", "POST", "application/x-www-form-urlencoded", "code=test", false},
		{"oauth_register", "POST", "application/json", `{}`, false},
		{"oauth_admin_user", "POST", "application/json", `{}`, false},
		{"oauth_authorization_server_metadata", "GET", "", "", true},
		{"oauth_protected_resource_metadata", "GET", "", "", true},
	} {
		for _, tc := range []struct {
			name, param, out, mode, order, result string
			status                                int
		}{
			{"allow", allow, allow, "", "param,main,out,", "", 201},
			{"param denied", `({success:false,status:403,result:"input denied"});`, allow, "", "param,", `{"success":false,"status":403,"result":"input denied"}`, 403},
			{"out denied", allow, `({success:false,status:409,result:"output denied"});`, "", "param,main,out,", `{"success":false,"status":409,"result":"output denied"}`, 409},
			{"param non-200", `({success:true,status:202,result:"pending"});`, allow, "", "param,main,out,", "", 201},
			{"out false with 200", allow, `({success:false,status:200,result:"denied"});`, "", "param,main,out,", `{"success":false,"status":200,"result":"denied"}`, 200},
			{"checkOnly", allow, allow, "checkOnly", "param,", `{"success":true,"status":200,"result":{"checked":true}}`, 200},
			{"checkOnly denied", `({success:false,status:403,result:"denied"});`, allow, "checkOnly", "param,", `{"success":false,"status":403,"result":"denied"}`, 403},
			{"checkOnly without param", "", allow, "checkOnly", "", `{"success":false,"status":500,"result":{"message":"No check script for this API"}}`, 500},
			{"param exception", `throw new Error("private detail");`, allow, "", "param,", "", 500},
			{"param invalid", `({success:true});`, allow, "", "param,", "", 500},
			{"param missing", "missing", allow, "", "", "", 500},
			{"out exception", allow, `throw new Error("private detail");`, "", "param,main,out,", "", 500},
			{"out invalid", allow, `"invalid JSON";`, "", "param,main,out,", "", 500},
			{"out missing", allow, "missing", "", "param,main,", "", 500},
			{"string check result", `JSON.stringify({success:true,status:200,result:null});`, allow, "", "param,main,out,", "", 201},
			{"output mutation", allow, `nyanAllParams.nyan_output.headers.Location="http://unsafe.test/"; nyanAllParams.nyan_output.headers["Set-Cookie"]="unsafe=yes"; nyanAllParams.nyan_output.body="changed";` + allow, "", "param,main,out,", "", 201},
		} {
			for _, rootRoute := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/root=%v", endpoint.api, tc.name, rootRoute), func(t *testing.T) {
					loaded, router := newOAuthChecksFixture(t, endpoint.api, tc.param, tc.out, main)
					path := "/" + endpoint.api + "?probe=one&probe=two"
					if rootRoute {
						path = "/?api=" + endpoint.api + "&probe=one&probe=two"
					}
					if tc.mode != "" {
						path += "&nyan_mode=" + tc.mode
					}
					request := newMCPPhase12Request(endpoint.method, path, endpoint.body)
					request.RemoteAddr = t.Name()
					if endpoint.contentType != "" {
						request.Header.Set("Content-Type", endpoint.contentType)
					}
					response := serveMCPPhase12Request(router, request)
					wantStatus, order := tc.status, tc.order
					if endpoint.metadata {
						order = strings.ReplaceAll(order, "main,", "")
						if wantStatus == 201 {
							wantStatus = 200
						}
					}
					assertOAuthCheckOrder(t, loaded, order)
					if response.Code != wantStatus || response.Header().Get("Cache-Control") != "no-store" {
						t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
					}
					if strings.Contains(response.Body.String(), "private detail") {
						t.Fatal("check exception leaked")
					}
					if tc.result != "" {
						var got, want interface{}
						if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal([]byte(tc.result), &want); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("check result=%v want=%v", got, want)
						}
					}
					if tc.result != "" || tc.status == 500 || endpoint.metadata {
						if response.Header().Get("Set-Cookie") != "" || response.Header().Get("Location") != "" {
							t.Fatal("discarded response headers leaked")
						}
					} else if response.Header().Get("Location") != "https://client.example.test/callback" || !strings.Contains(response.Header().Get("Set-Cookie"), "Secure") {
						t.Fatalf("response headers changed: %v", response.Header())
					}
					if tc.result == "" && tc.status == 201 {
						raw, err := oauthReadState(loaded.Snapshot.MCPServers["custom-mcp"].OAuth.StateDirectory, "checks/output.json")
						if err != nil {
							t.Fatal(err)
						}
						var output struct {
							Status                        int
							ContentType, Body, BodyBase64 string
							BodyLength, BodyLengthBytes   int
							Headers                       map[string]string
						}
						if err := json.Unmarshal([]byte(raw), &output); err != nil {
							t.Fatal(err)
						}
						if output.Status != response.Code || output.ContentType != response.Header().Get("Content-Type") || output.Body != response.Body.String() || output.BodyBase64 != base64.StdEncoding.EncodeToString(response.Body.Bytes()) || output.BodyLength != response.Body.Len() || output.BodyLengthBytes != response.Body.Len() || output.Headers["Cache-Control"] != "no-store" {
							t.Fatalf("outCheck metadata does not match response: %s", raw)
						}
					}
				})
			}
		}
	}
}

func TestOAuthCheckOnlyBodyAndRequestValidation(t *testing.T) {
	const allow = `if(!Array.isArray(nyanAllParams.query.probe) || nyanAllParams.query.probe.length!==2 || nyanAllParams.cookies.session!=="cookie" || nyanAllParams.authorization!=="Bearer test") throw new Error("wrong request context"); ({success:true,status:200,result:"checked"});`
	for _, tc := range []struct {
		name, api, method, query, body, contentType, order string
		status                                             int
	}{
		{"form checkOnly", "oauth_token", "POST", "", "nyan_mode=checkOnly", "application/x-www-form-urlencoded", "param,", 200},
		{"JSON checkOnly", "oauth_register", "POST", "", `{"nyan_mode":"checkOnly"}`, "application/json", "param,", 200},
		{"form overrides query", "oauth_token", "POST", "&nyan_mode=checkOnly", "nyan_mode=", "application/x-www-form-urlencoded", "param,main,out,", 201},
		{"JSON overrides query", "oauth_register", "POST", "&nyan_mode=checkOnly", `{"nyan_mode":""}`, "application/json", "param,main,out,", 201},
		{"authorize POST", "oauth_authorize", "POST", "", "nyan_mode=checkOnly", "application/x-www-form-urlencoded", "param,", 200},
		{"invalid method", "oauth_token", "GET", "&nyan_mode=checkOnly", "", "", "", 405},
		{"invalid JSON", "oauth_register", "POST", "&nyan_mode=checkOnly", "{", "application/json", "", 400},
		{"invalid content type", "oauth_token", "POST", "&nyan_mode=checkOnly", "{}", "application/json", "", 415},
		{"options", "oauth_token", "OPTIONS", "&nyan_mode=checkOnly", "", "", "", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded, router := newOAuthChecksFixture(t, tc.api, allow, `({success:true,status:nyanAllParams.nyan_output.status});`, `({status:201,body:{ok:true}});`)
			request := newMCPPhase12Request(tc.method, "/"+tc.api+"?probe=one&probe=two"+tc.query, tc.body)
			request.RemoteAddr = t.Name()
			request.Header.Set("Content-Type", tc.contentType)
			request.Header.Set("Authorization", "Bearer test")
			request.AddCookie(&http.Cookie{Name: "session", Value: "cookie"})
			response := serveMCPPhase12Request(router, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertOAuthCheckOrder(t, loaded, tc.order)
		})
	}
}

func TestOAuthChecksResponseBoundaries(t *testing.T) {
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:null});`
	const normal = `({status:200,body:{ok:true}});`
	for _, tc := range []struct {
		name, param, out, main, order string
		status                        int
	}{
		{"param fractional status", `({success:true,status:200.5});`, allow, normal, "param,", 500},
		{"out fractional status", allow, `({success:true,status:200.5});`, normal, "param,main,out,", 500},
		{"param informational status", `({success:true,status:100});`, allow, normal, "param,", 500},
		{"main exception", allow, allow, `throw new Error("private body failure");`, "param,main,", 500},
		{"main invalid", allow, allow, `({body:"missing status"});`, "param,main,", 500},
		{"unsafe header", allow, allow, `({status:200,headers:{"Set-Cookie":"bad=yes"},body:{ok:true}});`, "param,main,", 500},
		{"oversized param", fmt.Sprintf(`({success:false,status:403,result:"x".repeat(%d)});`, maxMCPRequestBytes), allow, normal, "param,", 500},
		{"oversized out", allow, fmt.Sprintf(`({success:false,status:403,result:"x".repeat(%d)});`, maxMCPRequestBytes), normal, "param,main,out,", 500},
		{"oversized body", allow, allow, fmt.Sprintf(`({status:200,body:"x".repeat(%d)});`, maxMCPRequestBytes+1), "param,main,", 500},
		{"error body inspected", allow, allow, `({status:400,body:{error:"invalid_request"}});`, "param,main,out,", 400},
		{"HTML redirect", allow, allow, `({status:302,contentType:"text/html; charset=utf-8",headers:{Location:"https://client.example.test/callback"},body:"<p>日本語</p>"});`, "param,main,out,", 302},
		{"aliases", allow, allow, normal, "param,main,out,", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded, router := newOAuthChecksFixture(t, "oauth_token", tc.param, tc.out, tc.main)
			if tc.name == "aliases" {
				entry := loaded.Snapshot.Definitions["oauth_token"].(map[string]interface{})
				entry["check"], entry["outcheck"] = entry["paramCheck"], entry["outCheck"]
				delete(entry, "paramCheck")
				delete(entry, "outCheck")
			}
			request := newMCPPhase12Request("POST", "/oauth_token", "code=test")
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.RemoteAddr = t.Name()
			response := serveMCPPhase12Request(router, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertOAuthCheckOrder(t, loaded, tc.order)
			if tc.status == 500 && (response.Header().Get("Set-Cookie") != "" || strings.Contains(response.Body.String(), "private body failure")) {
				t.Fatal("failed body leaked")
			}
			if tc.name == "HTML redirect" && (response.Body.String() != "<p>日本語</p>" || response.Header().Get("Location") != "https://client.example.test/callback") {
				t.Fatalf("redirect response=%v %s", response.Header(), response.Body.String())
			}
		})
	}
}

func TestOAuthVerifyAccessChecksBeforeMCPTool(t *testing.T) {
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:null});`
	const authenticated = `({authenticated:true,principal:{user_id:"checked"}});`
	for _, tc := range []struct {
		name, param, out, main, order string
		status                        int
		checkOnly                     bool
	}{
		{"allow", allow, allow, authenticated, "param,main,out,", 200, false},
		{"Tool checkOnly", allow, allow, authenticated, "param,main,out,", 200, true},
		{"param denied", `({success:false,status:403,result:{authenticated:true,principal:{user_id:"injected"}}});`, allow, authenticated, "param,", 401, false},
		{"param non-200", `({success:true,status:202,result:null});`, allow, authenticated, "param,main,out,", 200, false},
		{"out denied", allow, `({success:false,status:403,result:null});`, authenticated, "param,main,out,", 401, false},
		{"param exception", `throw new Error("private");`, allow, authenticated, "param,", 401, false},
		{"out invalid", allow, `({success:true});`, authenticated, "param,main,out,", 401, false},
		{"main denied", allow, allow, `({authenticated:false});`, "param,main,out,", 401, false},
		{"main forbidden", allow, allow, `({authenticated:false,forbidden:true});`, "param,main,out,", 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			param := `if(nyanAllParams.nyan_mode!==undefined || nyanAllParams.body!==undefined || nyanAllParams.tool!=="sample") throw new Error("Tool input leaked into verifier");` + tc.param
			loaded, router := newOAuthChecksFixture(t, "oauth_verify_access", param, tc.out, tc.main)
			key := t.Name()
			t.Cleanup(func() { storage.Delete(key) })
			sample := loaded.Snapshot.Definitions["sample"].(map[string]interface{})
			writeHotReloadTestFile(t, sample["script"].(string), fmt.Sprintf(`nyanSetItem(%q,"executed"); ({ok:true,service:"Nyan8",items:[1,2,3]});`, key))
			tool := findMCPTool(loaded.Snapshot.MCPServers["custom-mcp"], "sample")
			tool.InputSchema = map[string]interface{}{"type": "object"}
			arguments := "{}"
			if tc.checkOnly {
				arguments = `{"nyan_mode":"checkOnly"}`
			}
			response := mcpPhase2GapToolCall(router, arguments, "Bearer checked-token")
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertOAuthCheckOrder(t, loaded, tc.order)
			_, ran := storage.Load(key)
			if ran != (tc.status == 200 && !tc.checkOnly) {
				t.Fatalf("Tool ran=%v", ran)
			}
			if tc.status != 200 && response.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("authentication challenge missing")
			}
			if strings.Contains(tc.order, "out,") {
				raw, err := oauthReadState(loaded.Snapshot.MCPServers["custom-mcp"].OAuth.StateDirectory, "checks/output.json")
				if err != nil {
					t.Fatal(err)
				}
				var output struct {
					Status  int
					Body    string
					Headers map[string]string
				}
				if err := json.Unmarshal([]byte(raw), &output); err != nil {
					t.Fatal(err)
				}
				var decision map[string]interface{}
				if err := json.Unmarshal([]byte(output.Body), &decision); err != nil {
					t.Fatal(err)
				}
				if output.Status != 200 || len(output.Headers) != 0 || decision["authenticated"] != (tc.name != "main denied" && tc.name != "main forbidden") {
					t.Fatalf("decision output=%s", raw)
				}
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
	key := "MCP captured checks: " + t.Name()
	t.Cleanup(func() { storage.Delete(key) })
	configureChecks := func(dir string, definitions map[string]interface{}, generation string) {
		writeHotReloadTestFile(t, filepath.Join(dir, "generation.txt"), generation)
		for _, stage := range []string{"param", "out"} {
			path := filepath.Join(dir, stage+"-generation.js")
			writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q+nyanGetFile("generation.txt")+",");
			if(nyanCallMe({api:"identity"}).generation!==nyanGetFile("generation.txt")) throw new Error("mixed snapshot");
			({success:true,status:200,result:null});`, key, key, stage+":"))
			definitions["sample"].(map[string]interface{})[stage+"Check"] = path
		}
		identityPath := filepath.Join(dir, "identity.js")
		writeHotReloadTestFile(t, identityPath, fmt.Sprintf(`JSON.stringify({generation:%q});`, generation))
		definitions["identity"] = map[string]interface{}{"script": identityPath}
	}
	oldDir, oldDefinitions := newMCPPhase12Definitions(t)
	writeHotReloadTestFile(t, filepath.Join(oldDir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
	writeHotReloadTestFile(t, filepath.Join(oldDir, "sample.js"), `({generation:"old"});`)
	configureChecks(oldDir, oldDefinitions, "old")
	oldLoaded, err := loadMCPPhase12Config(oldDir, oldDefinitions)
	if err != nil {
		t.Fatal(err)
	}

	newDir, newDefinitions := newMCPPhase12Definitions(t)
	writeHotReloadTestFile(t, filepath.Join(newDir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
	writeHotReloadTestFile(t, filepath.Join(newDir, "sample.js"), `({generation:"new"});`)
	configureChecks(newDir, newDefinitions, "new")
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
	if order, _ := storage.Load(key); order != "param:old,out:old," {
		t.Fatalf("check snapshot order=%v", order)
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
	previousLevel := serviceLogLevel.Level()
	serviceLogLevel.Set(slog.LevelDebug)
	t.Cleanup(func() { serviceLogLevel.Set(previousLevel) })
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
	if !strings.Contains(logs, `"attachments":1`) {
		t.Fatalf("non-secret mail diagnostic missing: %q", logs)
	}
}

func TestPhase5IncomingWebSocketResponseAndPush(t *testing.T) {
	previousSnapshot := currentAPISnapshot()
	previousConfig := globalConfig
	previousPaths := servicePaths
	previousLogger := logger
	isolatePushConnections(t)
	var server *httptest.Server
	var sourceConnection *websocket.Conn
	var sinkConnection *websocket.Conn
	t.Cleanup(func() {
		closePhase5WebSocket(sourceConnection)
		closePhase5WebSocket(sinkConnection)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if len(pushConnections.snapshot("source")) == 0 && len(pushConnections.snapshot("sink")) == 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if server != nil {
			server.Close()
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
		return len(pushConnections.snapshot("sink")) == 1
	})
	sourceConnection, _, err = dialer.Dial(websocketURL+"/source", nil)
	if err != nil {
		t.Fatalf("dial source WebSocket: %v", err)
	}
	waitForHotReloadCondition(t, "source WebSocket registration", func() bool {
		return len(pushConnections.snapshot("source")) == 1
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
	if sinkBody["kind"] != "sink-push" || sinkBody["api"] != "sink" || sinkBody["value"] != "phase5-websocket" {
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

func captureServiceLogs(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	previous, previousLevel := logger, serviceLogLevel.Level()
	output := new(bytes.Buffer)
	logger = log.New(output, "", 0)
	serviceLogLevel.Set(level)
	t.Cleanup(func() { logger = previous; serviceLogLevel.Set(previousLevel) })
	return output
}

func decodeLogRecords(t *testing.T, data string) []map[string]interface{} {
	t.Helper()
	var records []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		if line == "" {
			continue
		}
		var record map[string]interface{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON log: %q: %v", line, err)
		}
		if record["time"] == nil || record["level"] == nil || record["msg"] == nil {
			t.Fatalf("missing log fields: %v", record)
		}
		records = append(records, record)
	}
	return records
}

func TestServiceLoggingLevelsAndErrorPrivacy(t *testing.T) {
	output := captureServiceLogs(t, slog.LevelInfo)
	err := fmt.Errorf("wrapped: %w", errors.New("password=private-error\nforged log line"))
	serviceLog(slog.LevelDebug, "hidden_debug")
	logServiceError(slog.LevelError, "execution_failed", err, "api", "example\napi")
	records := decodeLogRecords(t, output.String())
	if len(records) != 1 || records[0]["level"] != "ERROR" || records[0]["error_type"] != "*errors.errorString" || records[0]["api"] != "example\napi" || records[0]["error_detail"] != nil {
		t.Fatalf("unexpected records: %v", records)
	}
	if strings.Contains(output.String(), "private-error") {
		t.Fatal("info log exposed error detail")
	}
	output.Reset()
	serviceLogLevel.Set(slog.LevelDebug)
	logServiceError(slog.LevelError, "execution_failed", err)
	if got := decodeLogRecords(t, output.String())[0]["error_detail"]; got != err.Error() {
		t.Fatalf("debug detail=%v", got)
	}
	output.Reset()
	serviceLogLevel.Set(slog.LevelError)
	serviceLog(slog.LevelInfo, "hidden_info")
	serviceLog(slog.LevelWarn, "hidden_warn")
	if output.Len() != 0 {
		t.Fatalf("lower levels emitted: %s", output)
	}
}

func TestInitLoggerDestinationsLevelsAndRotation(t *testing.T) {
	output := captureServiceLogs(t, slog.LevelInfo)
	previous := globalConfig
	t.Cleanup(func() { globalConfig = previous })
	for _, tc := range []struct {
		value string
		want  slog.Level
	}{
		{"", slog.LevelInfo}, {"info", slog.LevelInfo}, {" DEBUG ", slog.LevelDebug}, {"warn", slog.LevelWarn}, {"error", slog.LevelError},
	} {
		globalConfig.Log = LogConfig{Level: tc.value}
		if err := initLogger(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if logger.Writer() != os.Stderr || serviceLogLevel.Level() != tc.want {
			t.Fatalf("wrong destination/level for %q", tc.value)
		}
	}
	logger.SetOutput(output)
	globalConfig.Log = LogConfig{EnableLogging: true, Filename: "should-not-exist.log", Level: "verbose"}
	if err := initLogger(t.TempDir()); err == nil {
		t.Fatal("invalid level accepted")
	}
	if logger.Writer() != output || serviceLogLevel.Level() != slog.LevelError {
		t.Fatal("invalid config changed logging")
	}
	dir := t.TempDir()
	globalConfig.Log = LogConfig{EnableLogging: true, Filename: "logs/service.log", Level: "info", MaxSize: 5, MaxBackups: 3, MaxAge: 7, Compress: true}
	if err := initLogger(dir); err != nil {
		t.Fatal(err)
	}
	writer, ok := logger.Writer().(*lumberjack.Logger)
	if !ok {
		t.Fatalf("unexpected file writer %T", logger.Writer())
	}
	defer writer.Close()
	if writer.Filename != filepath.Join(dir, "logs/service.log") || writer.MaxSize != 5 || writer.MaxBackups != 3 || writer.MaxAge != 7 || !writer.Compress {
		t.Fatalf("rotation config changed: %+v", writer)
	}
	serviceLog(slog.LevelInfo, "file_event")
	data, err := os.ReadFile(writer.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeLogRecords(t, string(data)); len(got) != 1 || got[0]["msg"] != "file_event" {
		t.Fatalf("file records: %v", got)
	}
}

func TestScriptConsoleLoggingRequiresDebug(t *testing.T) {
	output := captureServiceLogs(t, slog.LevelInfo)
	vm := goja.New()
	setupGojaVM(vm, nil)
	script := `console.log("private-console\nsecond line", {count:2});`
	if _, err := vm.RunString(script); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("info console leaked: %s", output)
	}
	serviceLogLevel.Set(slog.LevelDebug)
	if _, err := vm.RunString(script); err != nil {
		t.Fatal(err)
	}
	records := decodeLogRecords(t, output.String())
	if len(records) != 1 || records[0]["message"] != "private-console\nsecond line {\"count\":2}" {
		t.Fatalf("console record: %v", records)
	}
	output.Reset()
	if _, err := vm.RunString(`console.log("x".repeat(10000));`); err != nil {
		t.Fatal(err)
	}
	message := decodeLogRecords(t, output.String())[0]["message"].(string)
	if message != strings.Repeat("x", 4096)+"...[truncated]" {
		t.Fatalf("unexpected truncation length: %d", len(message))
	}
}

func TestWebSocketLoggingPrivacyAndNormalClose(t *testing.T) {
	output := captureServiceLogs(t, slog.LevelInfo)
	logWebSocketDisconnect("ws_client_disconnected", "sample", &websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "private-close"})
	if output.Len() != 0 {
		t.Fatal("normal close emitted at info")
	}
	logWebSocketDisconnect("ws_client_disconnected", "sample", &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "private-close"})
	record := decodeLogRecords(t, output.String())[0]
	if record["level"] != "WARN" || record["close_code"] != float64(websocket.CloseAbnormalClosure) || strings.Contains(output.String(), "private-close") {
		t.Fatalf("close record: %v", record)
	}
	if got := logURLOrigin("wss://user:password@example.test:443/private-path?token=secret#secret"); got != "wss://example.test:443" {
		t.Fatalf("origin=%s", got)
	}
}

func TestHTTPRecoveryAndServerDiagnosticsArePrivateJSON(t *testing.T) {
	output := captureServiceLogs(t, slog.LevelInfo)
	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(previousMode) })
	router := gin.New()
	router.Use(RecoveryMiddleware())
	router.GET("/panic", func(c *gin.Context) { panic("private-panic") })
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic status=%d", rec.Code)
	}
	httpServerErrorLogger().Print("private-server-diagnostic\nforged line")
	records := decodeLogRecords(t, output.String())
	if len(records) != 2 || strings.Contains(output.String(), "private-") || strings.Contains(output.String(), "forged") {
		t.Fatalf("diagnostics leaked: %s", output)
	}
}

// Run the actual main entry point in an isolated process without rebuilding it.
func TestLoggingCommandHelper(t *testing.T) {
	if os.Getenv("NYAN8_LOGGING_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func loggingTestCommand(t *testing.T, configPath, apiPath string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLoggingCommandHelper$", "--", "--config", configPath, "--api", apiPath)
	command.Env = append(os.Environ(), "NYAN8_LOGGING_TEST_HELPER=1")
	return command
}

func TestStartupLoggingErrorsAreJSONOnStderr(t *testing.T) {
	for _, tc := range []struct{ name, config, event string }{
		{"missing", "", "startup_options_invalid"},
		{"malformed", `{`, "config_load_failed"},
		{"invalid_level", `{"log":{"Level":"verbose"}}`, "invalid_log_level"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			apiPath := filepath.Join(dir, "api.json")
			writeHotReloadTestFile(t, apiPath, `{}`)
			if tc.config != "" {
				writeHotReloadTestFile(t, configPath, tc.config)
			}
			command := loggingTestCommand(t, configPath, apiPath)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err := command.Run(); err == nil {
				t.Fatal("startup unexpectedly succeeded")
			}
			records := decodeLogRecords(t, stderr.String())
			if stdout.Len() != 0 || len(records) != 1 || records[0]["msg"] != tc.event || records[0]["level"] != "ERROR" {
				t.Fatalf("stdout=%s stderr=%s", &stdout, &stderr)
			}
		})
	}
}

func TestHTTPProcessLoggingDoesNotDumpPayloadsOrAccessLogs(t *testing.T) {
	for _, level := range []string{"info", "debug"} {
		t.Run(level, func(t *testing.T) { checkHTTPProcessLogging(t, level) })
	}
}

func checkHTTPProcessLogging(t *testing.T, level string) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listener unavailable: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	apiPath := filepath.Join(dir, "api.json")
	writeHotReloadTestFile(t, configPath, fmt.Sprintf(`{"Port":%d,"bindAddress":"127.0.0.1","APIHotReload":{"Enabled":false},"log":{"EnableLogging":false,"Level":%q}}`, port, level))
	writeHotReloadTestFile(t, apiPath, `{"echo":{"script":"./echo.js","private_config":"private-config"},"push":{"script":"./push.js"},"with_push":{"script":"./echo.js","push":"push"}}`)
	writeHotReloadTestFile(t, filepath.Join(dir, "echo.js"), `console.log("private-console"); JSON.stringify({status:200,value:nyanAllParams.value});`)
	writeHotReloadTestFile(t, filepath.Join(dir, "push.js"), `JSON.stringify({value:"private-push"});`)
	command := loggingTestCommand(t, configPath, apiPath)
	var stdout, stderr synchronizedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
	}()
	client := &http.Client{Timeout: time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := client.Get(base + "/favicon.ico")
		if err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, path := range []string{"/", "/echo", "/with_push", "/nyan-rpc"} {
		body := `{"api":"echo","value":"private-request"}`
		if path == "/nyan-rpc" {
			body = `{"jsonrpc":"2.0","id":1,"method":"with_push","params":{"value":"private-request"}}`
		}
		response, err := client.Post(base+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || !bytes.Contains(data, []byte("private-request")) {
			t.Fatalf("%s response=%s err=%v", path, data, readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d logs=%s", path, response.StatusCode, stderr.String())
		}
	}
	command.Process.Kill()
	command.Wait()
	if stdout.String() != "" {
		t.Fatalf("stdout polluted: %s", stdout.String())
	}
	records := decodeLogRecords(t, stderr.String())
	if len(records) == 0 {
		t.Fatal("missing startup logs")
	}
	for _, record := range records {
		switch record["msg"] {
		case "starting", "config_loaded", "api_config_loaded", "api_hot_reload_disabled", "http_server_starting":
		case "push_script_completed", "push_no_subscribers":
			if level != "debug" {
				t.Fatalf("debug event emitted at info: %v", record)
			}
		case "script_console":
			if level != "debug" {
				t.Fatalf("console emitted at info: %v", record)
			}
		default:
			t.Fatalf("unexpected request/access log: %v", record)
		}
	}
	for _, secret := range []string{"private-request", "private-config", "private-push"} {
		if strings.Contains(stderr.String(), secret) {
			t.Fatalf("log exposed payload: %s", stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "private-console") != (level == "debug") {
		t.Fatalf("console level mismatch: %s", stderr.String())
	}
}

type filePathFixture struct {
	rootDir, childDir, scriptDir, cwd string
	snapshot                          *APIConfigSnapshot
}

func newFilePathFixture(t *testing.T) filePathFixture {
	t.Helper()
	previousConfig, previousPaths, previousSnapshot := globalConfig, servicePaths, currentAPISnapshot()
	globalConfig = Config{}
	servicePaths = serviceFilePaths{}
	t.Cleanup(func() {
		globalConfig, servicePaths = previousConfig, previousPaths
		publishAPISnapshot(previousSnapshot)
	})
	base := t.TempDir()
	f := filePathFixture{
		rootDir: filepath.Join(base, "config"), childDir: filepath.Join(base, "included"),
		scriptDir: filepath.Join(base, "scripts"), cwd: filepath.Join(base, "cwd"),
	}
	for dir, data := range map[string]string{f.rootDir: "root-data", f.childDir: "child-data", f.scriptDir: "script-data", f.cwd: "cwd-data"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeHotReloadTestFile(t, filepath.Join(dir, "data.txt"), data)
	}
	t.Chdir(f.cwd)
	commonPath := filepath.Join(f.scriptDir, "read.js")
	writeHotReloadTestFile(t, commonPath, `JSON.stringify({
		text: nyanGetFile(nyanAllParams.path || "data.txt"),
		base64: nyanReadFileB64(nyanAllParams.path || "data.txt"),
		attachment: nyanSendMailAttachment(nyanAllParams.path || "data.txt")
	})`)
	rootPath := filepath.Join(f.rootDir, "api.json")
	childPath := filepath.Join(f.childDir, "api.json")
	writeHotReloadTestFile(t, rootPath, fmt.Sprintf(`{"root":{"script":%q},"child":{"type":"include","path":%q}}`, commonPath, childPath))
	grandchildDir := filepath.Join(f.childDir, "nested")
	if err := os.MkdirAll(grandchildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeHotReloadTestFile(t, filepath.Join(grandchildDir, "data.txt"), "grandchild-data")
	writeHotReloadTestFile(t, childPath, fmt.Sprintf(`{"read":{"script":%q},"nested":{"type":"include","path":"nested/api.json"}}`, commonPath))
	writeHotReloadTestFile(t, filepath.Join(grandchildDir, "api.json"), fmt.Sprintf(`{"read":{"script":%q}}`, commonPath))
	loaded, err := readAPIConfigFile(rootPath, f.rootDir)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot = loaded.Snapshot
	return f
}

func assertFilePathResult(t *testing.T, result, want string) {
	t.Helper()
	var got struct {
		Text       string `json:"text"`
		Base64     string `json:"base64"`
		Attachment struct {
			Filename    string `json:"filename"`
			DataBase64  string `json:"dataBase64"`
			ContentType string `json:"contentType"`
		} `json:"attachment"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("decode file helper result %q: %v", result, err)
	}
	wantBase64 := base64.StdEncoding.EncodeToString([]byte(want))
	if got.Text != want || got.Base64 != wantBase64 || got.Attachment.DataBase64 != wantBase64 || got.Attachment.Filename != "data.txt" || !strings.HasPrefix(got.Attachment.ContentType, "text/plain") {
		t.Fatalf("file helper result = %s, want all helpers to read %q", result, want)
	}
}

func TestRuntimeFilePathsUseRootAPIConfigDirectory(t *testing.T) {
	f := newFilePathFixture(t)
	for _, tc := range []struct {
		name, api, path, want string
	}{
		{name: "root", api: "root", path: "./data.txt", want: "root-data"},
		{name: "included", api: "child/read", path: "data.txt", want: "root-data"},
		{name: "nested include", api: "child/nested/read", path: "data.txt", want: "root-data"},
		{name: "absolute", api: "root", path: filepath.Join(f.childDir, "data.txt"), want: "child-data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := runJavaScriptWithSnapshot(f.snapshot, filepath.Join(f.scriptDir, "read.js"), map[string]interface{}{"api": tc.api, "path": tc.path}, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertFilePathResult(t, result, tc.want)
			cwd, err := os.Getwd()
			if err != nil || cwd != f.cwd {
				t.Fatalf("runtime changed process directory: got %q, want %q; err=%v", cwd, f.cwd, err)
			}
		})
	}
}

func TestRuntimeFilePathsFallbacks(t *testing.T) {
	f := newFilePathFixture(t)
	previousArgs := os.Args
	os.Args = append([]string{filepath.Join(t.TempDir(), "Nyan8")}, os.Args[1:]...)
	t.Cleanup(func() { os.Args = previousArgs })
	withoutSources := newAPIConfigSnapshot(f.snapshot.RootPath, f.snapshot.Definitions, nil, nil, nil, nil)
	for _, tc := range []struct {
		name     string
		snapshot *APIConfigSnapshot
		apiPath  string
		want     string
	}{
		{name: "snapshot root without source metadata", snapshot: withoutSources, apiPath: filepath.Join(f.childDir, "api.json"), want: "root-data"},
		{name: "configured API path without snapshot", apiPath: f.snapshot.RootPath, want: "root-data"},
		{name: "default config directory for temporary executable", want: "cwd-data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			servicePaths.API.Path = tc.apiPath
			result, err := runJavaScriptWithSnapshot(tc.snapshot, filepath.Join(f.scriptDir, "read.js"), map[string]interface{}{"api": "root"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertFilePathResult(t, result, tc.want)
		})
	}
}

func TestRuntimeFilePathsKeepCapturedRootDespiteReloadAndAPIChange(t *testing.T) {
	f := newFilePathFixture(t)
	newSnapshot := newAPIConfigSnapshot(filepath.Join(f.childDir, "api.json"), f.snapshot.Definitions, map[string]string{"root": filepath.Join(f.childDir, "api.json")}, nil, nil, nil)
	publishAPISnapshot(newSnapshot)
	servicePaths.API.Path = newSnapshot.RootPath
	script := filepath.Join(f.scriptDir, "mutate.js")
	writeHotReloadTestFile(t, script, `nyanAllParams.api = "child/read";
		JSON.stringify({text:nyanGetFile("data.txt"),base64:nyanReadFileB64("data.txt"),attachment:nyanSendMailAttachment("data.txt")})`)
	result, err := runJavaScriptWithSnapshot(f.snapshot, script, map[string]interface{}{"api": "root"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertFilePathResult(t, result, "root-data")
}

func TestRuntimeFilePathsPreserveMissingFileAndDirectoryBehavior(t *testing.T) {
	f := newFilePathFixture(t)
	script := filepath.Join(f.scriptDir, "missing.js")
	writeHotReloadTestFile(t, script, `function throws(fn) { try { fn(); return false; } catch (_) { return true; } }
		JSON.stringify([
			nyanGetFile("missing.txt") === null, nyanGetFile(".") === null,
			throws(function(){nyanReadFileB64("missing.txt")}), throws(function(){nyanReadFileB64(".")}),
			throws(function(){nyanSendMailAttachment("missing.txt")}), throws(function(){nyanSendMailAttachment(".")})
		])`)
	result, err := runJavaScriptWithSnapshot(f.snapshot, script, map[string]interface{}{"api": "child/read"}, nil)
	if err != nil || result != "[true,true,true,true,true,true]" {
		t.Fatalf("missing/directory result = %q, err = %v", result, err)
	}
}

func TestRuntimeFilePathsNyanCallMeAndChecksUseRootConfig(t *testing.T) {
	f := newFilePathFixture(t)
	parentScript := filepath.Join(f.scriptDir, "parent.js")
	paramScript := filepath.Join(f.scriptDir, "param.js")
	outScript := filepath.Join(f.scriptDir, "out.js")
	writeHotReloadTestFile(t, paramScript, `({success:true,status:200,result:nyanGetFile("data.txt")})`)
	writeHotReloadTestFile(t, outScript, `({success:!nyanAllParams.rejectOutput,status:nyanAllParams.rejectOutput?409:200,
		result:{file:nyanSendMailAttachment("data.txt").dataBase64,body:JSON.parse(nyanAllParams.nyan_output_body).text}})`)
	writeHotReloadTestFile(t, parentScript, `JSON.stringify({
		before:nyanGetFile("data.txt"),
		normal:nyanCallMe({api:"child/read"}),
		param:nyanCallMe({api:"child/read",nyan_mode:"checkOnly"}),
		out:nyanCallMe({api:"child/read",rejectOutput:true}),
		after:nyanGetFile("data.txt")
	})`)
	definitions := map[string]interface{}{
		"root":       map[string]interface{}{"script": parentScript},
		"child/read": map[string]interface{}{"script": filepath.Join(f.scriptDir, "read.js"), "paramCheck": paramScript, "outCheck": outScript},
	}
	snapshot := newAPIConfigSnapshot(f.snapshot.RootPath, definitions, f.snapshot.Sources, nil, nil, nil)
	result, err := runJavaScriptWithSnapshot(snapshot, parentScript, map[string]interface{}{"api": "root"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Before string          `json:"before"`
		After  string          `json:"after"`
		Normal json.RawMessage `json:"normal"`
		Param  struct {
			Result string `json:"result"`
		} `json:"param"`
		Out struct {
			Status int `json:"status"`
			Result struct {
				File string `json:"file"`
				Body string `json:"body"`
			} `json:"result"`
		} `json:"out"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	assertFilePathResult(t, string(got.Normal), "root-data")
	if got.Before != "root-data" || got.After != "root-data" || got.Param.Result != "root-data" || got.Out.Status != 409 || got.Out.Result.File != base64.StdEncoding.EncodeToString([]byte("root-data")) || got.Out.Result.Body != "root-data" {
		t.Fatalf("nested/check helper paths = %s", result)
	}
}

func TestRuntimeFilePathsSendMailAttachmentPath(t *testing.T) {
	f := newFilePathFixture(t)
	host, port, smtpResult := newPhase5SMTPServer(t)
	globalConfig.SMTP = SMTPConfig{Host: host, Port: port, Username: "local-test", Password: "local-test", FromEmail: "sender@example.test"}
	script := filepath.Join(f.scriptDir, "mail.js")
	writeHotReloadTestFile(t, script, fmt.Sprintf(`nyanSendMail({to:"recipient@example.test",subject:"paths",body:"paths",attachments:[{path:"data.txt"},{path:%q}]})`, filepath.Join(f.childDir, "data.txt")))
	result, err := runJavaScriptWithSnapshot(f.snapshot, script, map[string]interface{}{"api": "child/read"}, nil)
	if err != nil || result != "true" {
		t.Fatalf("local mock mail = %q, err=%v", result, err)
	}
	var captured phase5SMTPResult
	select {
	case captured = <-smtpResult:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for local SMTP mock")
	}
	if captured.err != nil {
		t.Fatal(captured.err)
	}
	message, err := mail.ReadMessage(bytes.NewReader(captured.message))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	var attachments []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		disposition, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if disposition != "attachment" {
			continue
		}
		var body io.Reader = part
		if strings.EqualFold(part.Header.Get("Content-Transfer-Encoding"), "base64") {
			body = base64.NewDecoder(base64.StdEncoding, part)
		}
		data, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(err)
		}
		attachments = append(attachments, string(data))
	}
	if strings.Join(attachments, ",") != "root-data,child-data" {
		t.Fatalf("relative/absolute attachments = %#v", attachments)
	}
}

func TestRuntimeFilePathsPushUsesRootConfigAndTargetAPI(t *testing.T) {
	f := newFilePathFixture(t)
	script := filepath.Join(f.scriptDir, "push.js")
	writeHotReloadTestFile(t, script, `JSON.stringify({api:nyanAllParams.api,text:nyanGetFile("data.txt")})`)
	check := filepath.Join(f.scriptDir, "push-check.js")
	writeHotReloadTestFile(t, check, `
if(nyanAllParams.api!=="child/read" || nyanGetFile("data.txt")!=="root-data") throw new Error("wrong Push check context");
({success:true,status:200,result:null});`)
	definitions := map[string]interface{}{"root": map[string]interface{}{"push": "child/read"}, "child/read": map[string]interface{}{"script": script, "paramCheck": check, "outCheck": check}}
	snapshot := newAPIConfigSnapshot(f.snapshot.RootPath, definitions, f.snapshot.Sources, nil, nil, nil)
	publishAPISnapshot(snapshot)
	connections := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connections <- connection
	}))
	t.Cleanup(server.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var connection *websocket.Conn
	select {
	case connection = <-connections:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for local WebSocket")
	}
	registered := &serverWebSocket{Conn: connection}
	pushConnections.add("child/read", registered)
	t.Cleanup(func() {
		_ = connection.Close()
		pushConnections.remove("child/read", registered)
	})
	params := map[string]interface{}{"api": "root"}
	performPush(definitions["root"].(map[string]interface{}), snapshot.Definitions, params, f.rootDir)
	if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, response, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		API  string `json:"api"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(response, &got); err != nil {
		t.Fatal(err)
	}
	if got.API != "child/read" || got.Text != "root-data" || params["api"] != "root" {
		t.Fatalf("push response=%s source params=%#v", response, params)
	}
}

const requestInfoTestExpression = `({cookie:nyanGetCookie("session"),missing:nyanGetCookie("absent"),ip:nyanGetRemoteIP(),userAgent:nyanGetUserAgent(),headers:nyanGetRequestHeaders()})`

func assertScriptRequestInfo(t *testing.T, raw, cookie, ip, agent, header string) {
	t.Helper()
	var info struct {
		Cookie, Missing, IP, UserAgent string
		Headers                        map[string]string
		Nested                         json.RawMessage
	}
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatal(err)
	}
	if info.Cookie != cookie || info.Missing != "" || info.IP != ip || info.UserAgent != agent || info.Headers["X-Request"] != header {
		t.Fatalf("request info=%s, want cookie=%q ip=%q agent=%q header=%q", raw, cookie, ip, agent, header)
	}
	if info.Nested != nil {
		assertScriptRequestInfo(t, string(info.Nested), cookie, ip, agent, header)
	}
}

func TestScriptReadOnlyRequestSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "https://example.test/", nil)
	ctx.Request.RemoteAddr = "203.0.113.7:1234"
	ctx.Request.Header.Set("User-Agent", "original-agent")
	ctx.Request.Header.Set("X-Request", "original-header")
	ctx.Request.Header.Set("X-Forwarded-For", "198.51.100.99")
	ctx.Request.AddCookie(&http.Cookie{Name: "session", Value: "original-cookie"})
	captured := readOnlyScriptRequestContext(ctx)
	ctx.Request.RemoteAddr = "198.51.100.8:1234"
	ctx.Request.Header.Set("Cookie", "session=changed")
	ctx.Request.Header.Set("User-Agent", "changed")
	ctx.Request.Header.Set("X-Request", "changed")
	dir := t.TempDir()
	path := filepath.Join(dir, "nested.js")
	writeHotReloadTestFile(t, path, `nyanSetCookie("nested","no-write"); JSON.stringify(`+requestInfoTestExpression+`);`)
	snapshot := newAPIConfigSnapshot(filepath.Join(dir, "api.json"), map[string]interface{}{"identity": map[string]interface{}{"script": path}}, nil, nil, nil, nil)
	vm := goja.New()
	setupGojaVMWithSnapshot(vm, snapshot, captured)
	value, err := vm.RunString(`nyanSetCookie("direct","no-write");
const copy=nyanGetRequestHeaders();copy["X-Request"]="forged";
const info=` + requestInfoTestExpression + `;info.nested=nyanCallMe({api:"identity",_headers:{Cookie:"session=forged"},_remote_ip:"forged",_user_agent:"forged"});JSON.stringify(info);`)
	if err != nil {
		t.Fatal(err)
	}
	assertScriptRequestInfo(t, value.String(), "original-cookie", "203.0.113.7", "original-agent", "original-header")
	if len(recorder.Header()) != 0 || recorder.Body.Len() != 0 {
		t.Fatalf("read-only script wrote HTTP response: %v", recorder.Header())
	}
	for _, empty := range []*gin.Context{nil, {}} {
		vm := goja.New()
		setupGojaVMWithSnapshot(vm, nil, readOnlyScriptRequestContext(empty))
		value, err := vm.RunString(`JSON.stringify(` + requestInfoTestExpression + `);`)
		if err != nil {
			t.Fatal(err)
		}
		assertScriptRequestInfo(t, value.String(), "", "", "", "")
	}
}

func TestWebSocketAndPushRequestInformation(t *testing.T) {
	for _, transport := range []string{"http", "root", "jsonrpc", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			dir, key := t.TempDir(), t.Name()
			stages := []string{"source-param", "source-main", "source-out", "push-param", "push-main", "push-out"}
			t.Cleanup(func() {
				for _, stage := range stages {
					storage.Delete(key + stage)
				}
			})
			write := func(name, code string) string {
				path := filepath.Join(dir, name+".js")
				writeHotReloadTestFile(t, path, code)
				return path
			}
			record := func(stage, body string) string {
				code := `var info=` + requestInfoTestExpression + `; info.nested=nyanCallMe({api:"identity",_headers:{Cookie:"session=forged"},_remote_ip:"forged",_user_agent:"forged"});`
				code += fmt.Sprintf(`nyanSetItem(%q,JSON.stringify(info));`, key+stage)
				if strings.HasPrefix(stage, "push-") || transport == "websocket" {
					code += `nyanSetCookie("unexpected","no-write");`
				}
				return write(stage, code+body)
			}
			allow := `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:null});`
			f := newWebSocketCheckFixture(t, map[string]interface{}{
				"identity": map[string]interface{}{"script": write("identity", `JSON.stringify(`+requestInfoTestExpression+`);`)},
				"source":   map[string]interface{}{"paramCheck": record("source-param", allow), "script": record("source-main", `JSON.stringify({status:200,value:"ok"});`), "outCheck": record("source-out", allow), "push": "sink"},
				"sink":     map[string]interface{}{"paramCheck": record("push-param", allow), "script": record("push-main", `JSON.stringify(info);`), "outCheck": record("push-out", allow)},
				"recovery": map[string]interface{}{"script": write("recovery", `"barrier";`)},
			})
			barrier := func(conn *websocket.Conn) {
				if got := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
					t.Fatal(got)
				}
			}
			var receivers []*websocket.Conn
			for _, who := range []string{"B", "C"} {
				conn := f.dial("/sink", http.Header{"Cookie": {"session=" + who}, "User-Agent": {"receiver-" + who}, "X-Request": {"receiver-" + who}})
				barrier(conn)
				receivers = append(receivers, conn)
			}
			headers := http.Header{"Cookie": {"session=A"}, "User-Agent": {"sender-A"}, "X-Request": {"request-A"}, "X-Forwarded-For": {"198.51.100.99"}}
			var origin *websocket.Conn
			if transport == "websocket" {
				origin = f.dial("/", headers)
				barrier(origin)
			}
			for _, stage := range stages {
				storage.Delete(key + stage)
			}
			if transport == "websocket" {
				got := exchangeWebSocketCheckFrame(t, origin, websocket.BinaryMessage, `{"api":"source","_headers":{"Cookie":"session=forged","X-Request":"forged"},"_remote_ip":"forged","_user_agent":"forged","session":"forged"}`)
				if !containsJSONValue([]byte(got), "value", "ok") {
					t.Fatal(got)
				}
				barrier(origin)
			} else {
				method, path, body := "GET", "/source?_headers=forged&_remote_ip=forged&_user_agent=forged", ""
				if transport == "root" {
					path = "/?api=source&_headers=forged&_remote_ip=forged&_user_agent=forged"
				}
				if transport == "jsonrpc" {
					method, path, body = "POST", "/nyan-rpc", `{"jsonrpc":"2.0","id":1,"method":"source","params":{"_headers":{"Cookie":"session=forged"},"_remote_ip":"forged","_user_agent":"forged"}}`
				}
				request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				request.Header = headers.Clone()
				if body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				response, err := f.server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 || !strings.Contains(string(data), "ok") {
					t.Fatalf("source response=%s err=%v", data, err)
				}
				if response.Header.Get("Set-Cookie") != "" {
					t.Fatal("Push modified caller's cookies")
				}
			}
			for _, stage := range stages {
				raw, ok := storage.Load(key + stage)
				if !ok {
					t.Fatalf("%s did not execute", stage)
				}
				assertScriptRequestInfo(t, raw.(string), "A", "127.0.0.1", "sender-A", "request-A")
			}
			for _, conn := range receivers {
				if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				_, data, err := conn.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				assertScriptRequestInfo(t, string(data), "A", "127.0.0.1", "sender-A", "request-A")
				barrier(conn)
			}
		})
	}
}

func TestMCPRequestInformationHTTPAndStdio(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	key := t.Name()
	stages := []string{"param", "main", "out"}
	t.Cleanup(func() {
		for _, stage := range stages {
			storage.Delete(key + stage)
		}
	})
	writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
	identity := filepath.Join(dir, "identity.js")
	writeHotReloadTestFile(t, identity, `nyanSetCookie("nested","no-write");JSON.stringify(`+requestInfoTestExpression+`);`)
	definitions["identity"] = map[string]interface{}{"script": identity}
	entry := definitions["sample"].(map[string]interface{})
	for _, stage := range stages {
		path := filepath.Join(dir, stage+"-request.js")
		code := `nyanSetCookie("direct","no-write");var info=` + requestInfoTestExpression + `;info.nested=nyanCallMe({api:"identity",_headers:{Cookie:"session=forged"},_remote_ip:"forged",_user_agent:"forged"});`
		code += fmt.Sprintf(`nyanSetItem(%q,JSON.stringify(info));`, key+stage)
		if stage == "main" {
			code += `JSON.stringify(info);`
			entry["script"] = path
		} else {
			code += `({success:true,status:200,result:{checked:true}});`
			entry[stage+"Check"] = path
		}
		writeHotReloadTestFile(t, path, code)
	}
	definitions["local-mcp"] = map[string]interface{}{"type": "mcp", "transport": "stdio", "tools": []interface{}{"sample"}}
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	for _, transport := range []string{"http", "stdio"} {
		for _, checkOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/checkOnly=%v", transport, checkOnly), func(t *testing.T) {
				for _, stage := range stages {
					storage.Delete(key + stage)
				}
				arguments := `{"_user_agent":"forged","session":"forged"}`
				if checkOnly {
					arguments = `{"nyan_mode":"checkOnly","_user_agent":"forged","session":"forged"}`
				}
				body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"request-info","method":"tools/call","params":{"name":"sample","arguments":%s}}`, arguments)
				var envelope map[string]interface{}
				if transport == "http" {
					request := newMCPPhase12Request("POST", "/custom-mcp", body)
					request.RemoteAddr = "203.0.113.8:1234"
					request.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
					request.Header.Set("Authorization", "Bearer phase2")
					request.Header.Set("Cookie", "session=MCP")
					request.Header.Set("User-Agent", "mcp-client")
					request.Header.Set("X-Request", "mcp-request")
					response := serveMCPPhase12Request(router, request)
					if response.Code != 200 || response.Header().Get("Set-Cookie") != "" {
						t.Fatalf("response=%d %v %s", response.Code, response.Header(), response.Body.String())
					}
					envelope = oauthPhase4JSONBody(t, response)
				} else {
					input := mcpPhase12InitializeBody(mcpProtocol20251125) + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n" + body + "\n"
					var output bytes.Buffer
					if err := serveMCPStdio(strings.NewReader(input), &output, loaded.Snapshot, loaded.Snapshot.MCPServers["local-mcp"]); err != nil {
						t.Fatal(err)
					}
					lines := strings.Split(strings.TrimSpace(output.String()), "\n")
					if len(lines) != 2 {
						t.Fatalf("stdio output=%s", output.String())
					}
					if err := json.Unmarshal([]byte(lines[1]), &envelope); err != nil {
						t.Fatal(err)
					}
				}
				result, ok := envelope["result"].(map[string]interface{})
				if !ok || result["isError"] != false {
					t.Fatalf("Tool failed: %v", envelope)
				}
				for _, stage := range stages {
					raw, ran := storage.Load(key + stage)
					if checkOnly && stage != "param" {
						if ran {
							t.Fatalf("checkOnly executed %s", stage)
						}
						continue
					}
					if !ran {
						t.Fatalf("missing %s", stage)
					}
					if transport == "http" {
						assertScriptRequestInfo(t, raw.(string), "MCP", "203.0.113.8", "mcp-client", "mcp-request")
					} else {
						assertScriptRequestInfo(t, raw.(string), "", "", "", "")
					}
				}
			})
		}
	}
}

func TestNyanSetCookieSecureFollowsReceivedTLS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, target, forwardedProto string
		wantSecure                   bool
	}{
		{name: "HTTP", target: "http://example.test/", wantSecure: false},
		{name: "HTTPS", target: "https://example.test/", wantSecure: true},
		{name: "HTTP with forwarded HTTPS", target: "http://example.test/", forwardedProto: "https", wantSecure: false},
		{name: "HTTPS with forwarded HTTP", target: "https://example.test/", forwardedProto: "http", wantSecure: true},
		{name: "no request", wantSecure: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			if tc.target != "" {
				ctx.Request = httptest.NewRequest(http.MethodGet, tc.target, nil)
				if tc.forwardedProto != "" {
					ctx.Request.Header.Set("X-Forwarded-Proto", tc.forwardedProto)
					ctx.Request.Header.Set("Forwarded", "proto="+tc.forwardedProto)
				}
			}
			vm := goja.New()
			setupGojaVMWithSnapshot(vm, nil, ctx)
			if _, err := vm.RunString(`nyanSetCookie("session", "abc123");`); err != nil {
				t.Fatal(err)
			}
			response := recorder.Result()
			defer response.Body.Close()
			cookies := response.Cookies()
			if len(cookies) != 1 {
				t.Fatalf("cookies=%v, want one cookie", cookies)
			}
			cookie := cookies[0]
			if cookie.Secure != tc.wantSecure || !cookie.HttpOnly || cookie.MaxAge != 3600 ||
				cookie.Path != "/" || cookie.Domain != "" || cookie.Name != "session" || cookie.Value != "abc123" {
				t.Fatalf("unexpected cookie: %s", cookie.String())
			}
		})
	}
	t.Run("no HTTP context", func(t *testing.T) {
		vm := goja.New()
		setupGojaVMWithSnapshot(vm, nil, nil)
		if _, err := vm.RunString(`nyanSetCookie("session", "abc123");`); err != nil {
			t.Fatalf("cookie setter without HTTP context: %v", err)
		}
	})
}

func TestNyanHostExecReturnsCommandResults(t *testing.T) {
	for _, exitCode := range []int{0, 1, 7} {
		t.Run(fmt.Sprintf("exit %d", exitCode), func(t *testing.T) {
			command := fmt.Sprintf("echo processing; echo problem >&2; exit %d", exitCode)
			if runtime.GOOS == "windows" {
				command = fmt.Sprintf("echo processing& echo problem 1>&2& exit /b %d", exitCode)
			}
			vm := goja.New()
			setupGojaVMWithSnapshot(vm, nil, nil)
			if err := vm.Set("command", command); err != nil {
				t.Fatal(err)
			}
			value, err := vm.RunString(`nyanHostExec(command);`)
			if err != nil {
				t.Fatalf("command exit %d threw an exception: %v", exitCode, err)
			}
			if _, ok := value.Export().(map[string]interface{}); !ok {
				t.Fatalf("result is not an object: %#v", value.Export())
			}
			encoded, err := json.Marshal(value.Export())
			if err != nil {
				t.Fatal(err)
			}
			var result ExecResult
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			if result.Success != (exitCode == 0) || result.ExitCode != exitCode ||
				strings.TrimSpace(result.Stdout) != "processing" || strings.TrimSpace(result.Stderr) != "problem" {
				t.Fatalf("unexpected result: %s", encoded)
			}
		})
	}
	t.Run("command not found", func(t *testing.T) {
		vm := goja.New()
		setupGojaVMWithSnapshot(vm, nil, nil)
		value, err := vm.RunString(`const result = nyanHostExec("nyan8_missing_command_643acf5e");
			result.success === false && result.exit_code !== 0 && result.stderr.length > 0;`)
		if err != nil || !value.ToBoolean() {
			t.Fatalf("missing command result=%v error=%v", value, err)
		}
	})
}

func TestNyanHostExecInvocationErrorsRemainExceptions(t *testing.T) {
	t.Run("missing argument", func(t *testing.T) {
		vm := goja.New()
		setupGojaVMWithSnapshot(vm, nil, nil)
		if _, err := vm.RunString(`nyanHostExec();`); err == nil || !strings.Contains(err.Error(), "command required") {
			t.Fatalf("missing argument error=%v", err)
		}
	})
	t.Run("shell unavailable", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows can locate cmd through system directories independently of PATH")
		}
		t.Setenv("PATH", t.TempDir())
		vm := goja.New()
		setupGojaVMWithSnapshot(vm, nil, nil)
		if _, err := vm.RunString(`nyanHostExec("echo test");`); err == nil || !strings.Contains(err.Error(), "failed to exec") {
			t.Fatalf("missing shell error=%v", err)
		}
	})
}

func TestNyanCallMeRequiresExplicitAPI(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	f := newFilePathFixture(t)
	key := "nyanCallMe required API: " + t.Name()
	t.Cleanup(func() { storage.Delete(key) })
	caller := filepath.Join(f.rootDir, "caller.js")
	target := filepath.Join(f.rootDir, "target.js")
	check := filepath.Join(f.rootDir, "check.js")
	writeHotReloadTestFile(t, check, fmt.Sprintf(`nyanSetItem(%q,"executed"); ({success:true,status:200,result:null});`, key))
	writeHotReloadTestFile(t, target, `JSON.stringify({status:200,called:nyanAllParams.api});`)
	rootPath := filepath.Join(f.rootDir, "api.json")
	writeHotReloadTestFile(t, rootPath, `{
		"caller":{"script":"caller.js"},
		"hello2":{"script":"target.js","paramCheck":"check.js"}
	}`)
	loaded, err := readAPIConfigFile(rootPath, f.rootDir)
	if err != nil {
		t.Fatal(err)
	}
	publishAPISnapshot(loaded.Snapshot)
	servicePaths.API.Path = rootPath
	router := gin.New()
	router.GET("/caller", func(c *gin.Context) { executeAPIEndpoint(c, "caller", f.rootDir) })
	for _, tc := range []struct{ name, args string }{
		{"no arguments", ""},
		{"undefined", "undefined"},
		{"null", "null"},
		{"empty object", "{}"},
		{"parameters without API", `{id:123}`},
		{"checkOnly without API", `{nyan_mode:"checkOnly"}`},
		{"empty API", `{api:""}`},
		{"blank API", `{api:" \t\n"}`},
		{"undefined API", `{api:undefined}`},
		{"null API", `{api:null}`},
		{"numeric API", `{api:123}`},
		{"boolean API", `{api:true}`},
		{"object API", `{api:{}}`},
		{"array API", `{api:["hello2"]}`},
		{"string argument", `"hello2"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "nyanCallMe(" + tc.args + ");"
			vm := goja.New()
			setupGojaVMWithSnapshot(vm, loaded.Snapshot, nil)
			if _, err := vm.RunString(script); err == nil || !strings.Contains(err.Error(), "nyanCallMe: api is required") {
				t.Fatalf("error = %v, want missing API exception", err)
			}
			writeHotReloadTestFile(t, caller, script)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/caller", nil))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s, want 500", recorder.Code, recorder.Body.String())
			}
			if _, executed := storage.Load(key); executed {
				t.Fatal("missing API executed the hello2 check")
			}
		})
	}
	// The exception can be caught without writing an HTTP response from the helper.
	writeHotReloadTestFile(t, caller, `try { nyanCallMe(); } catch (err) { JSON.stringify({status:200,error:String(err)}); }`)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/caller", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "nyanCallMe: api is required") {
		t.Fatalf("caught exception: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// hello2 remains callable when explicitly named.
	writeHotReloadTestFile(t, caller, `JSON.stringify(nyanCallMe({api:"hello2"}));`)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/caller", nil))
	if recorder.Code != http.StatusOK || !containsJSONValue(recorder.Body.Bytes(), "called", "hello2") {
		t.Fatalf("explicit API: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, executed := storage.Load(key); !executed {
		t.Fatal("explicit API did not execute its check")
	}
}

func TestNyanCallMeRunsChecksInOrder(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:{checked:true}});`
	const denyParam = `({success:false,status:403,result:{message:"input blocked"}});`
	const denyOut = `({success:false,status:409,result:{message:"output blocked"}});`
	tests := []struct {
		name       string
		param      string
		out        string
		main       string
		paramKey   string
		outKey     string
		checkOnly  bool
		wantOrder  string
		wantResult string
		wantError  string
	}{
		{name: "allow", param: allow, out: allow, wantOrder: "param,main,out,", wantResult: `{"status":201,"value":"private result"}`},
		{name: "param rejection", param: denyParam, out: allow, wantOrder: "param,", wantResult: `{"success":false,"status":403,"result":{"message":"input blocked"}}`},
		{name: "param non-200", param: `({success:true,status:202,result:"pending"});`, out: allow, wantOrder: "param,main,out,", wantResult: `{"status":201,"value":"private result"}`},
		{name: "output rejection", param: allow, out: denyOut, wantOrder: "param,main,out,", wantResult: `{"success":false,"status":409,"result":{"message":"output blocked"}}`},
		{name: "failed main response is checked", param: allow, out: denyOut, main: `JSON.stringify({success:false,status:403,value:"private result"});`, wantOrder: "param,main,out,", wantResult: `{"success":false,"status":409,"result":{"message":"output blocked"}}`},
		{name: "checkOnly", param: allow, out: allow, checkOnly: true, wantOrder: "param,", wantResult: `{"success":true,"status":200,"result":{"checked":true}}`},
		{name: "checkOnly without param checker", out: allow, checkOnly: true, wantOrder: "", wantError: "No check script"},
		{name: "lowercase aliases", param: allow, out: allow, paramKey: "paramcheck", outKey: "outcheck", wantOrder: "param,main,out,", wantResult: `{"status":201,"value":"private result"}`},
		{name: "legacy check alias", param: denyParam, out: allow, paramKey: "check", wantOrder: "param,", wantResult: `{"success":false,"status":403,"result":{"message":"input blocked"}}`},
		{name: "param exception", param: `throw new Error("param exploded");`, out: allow, wantOrder: "param,", wantError: "param exploded"},
		{name: "param fractional status", param: `({success:true,status:200.5});`, out: allow, wantOrder: "param,", wantError: "paramCheck response status must be an integer"},
		{name: "out fractional status", param: allow, out: `({success:true,status:200.5});`, wantOrder: "param,main,out,", wantError: "outCheck response status must be an integer"},
		{name: "invalid param response", param: `({success:true});`, out: allow, wantOrder: "param,", wantError: "paramCheck response status must be an integer between 200 and 599"},
		{name: "output exception", param: allow, out: `throw new Error("out exploded");`, wantOrder: "param,main,out,", wantError: "out exploded"},
		{name: "invalid output response", param: allow, out: `"not JSON";`, wantOrder: "param,main,out,", wantError: "outCheck string response must be JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootDir := t.TempDir()
			key := "nyanCallMe checks: " + t.Name()
			t.Cleanup(func() { storage.Delete(key) })
			writeScript := func(name, body string) string {
				path := filepath.Join(rootDir, name+".js")
				writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, key, key, name+",")+body)
				return path
			}
			mainScript := tt.main
			if mainScript == "" {
				mainScript = `JSON.stringify({status:201,value:"private result"});`
			}
			entry := map[string]interface{}{"script": writeScript("main", mainScript)}
			if tt.param != "" {
				paramKey := tt.paramKey
				if paramKey == "" {
					paramKey = "paramCheck"
				}
				entry[paramKey] = writeScript("param", tt.param)
			}
			if tt.out != "" {
				outKey := tt.outKey
				if outKey == "" {
					outKey = "outCheck"
				}
				entry[outKey] = writeScript("out", tt.out)
			}
			snapshot := newAPIConfigSnapshot(filepath.Join(rootDir, "api.json"), map[string]interface{}{"target": entry}, nil, nil, nil, nil)
			recorder := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(recorder)
			ginCtx.Request = httptest.NewRequest(http.MethodGet, "/parent", nil)
			vm := goja.New()
			setupGojaVMWithSnapshot(vm, snapshot, ginCtx)
			params := `{api:"target"}`
			if tt.checkOnly {
				params = `{api:"target",nyan_mode:"checkOnly"}`
			}
			value, err := vm.RunString("nyanCallMe(" + params + ");")
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				got, err := json.Marshal(value.Export())
				if err != nil {
					t.Fatal(err)
				}
				var gotResult, wantResult interface{}
				if err := json.Unmarshal(got, &gotResult); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(tt.wantResult), &wantResult); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotResult, wantResult) {
					t.Fatalf("result = %s, want %s", got, tt.wantResult)
				}
			}
			order, _ := storage.Load(key)
			if order == nil {
				order = ""
			}
			if order != tt.wantOrder {
				t.Errorf("execution order = %q, want %q", order, tt.wantOrder)
			}
			if ginCtx.Writer.Written() || recorder.Body.Len() != 0 || len(recorder.Header()) != 0 {
				t.Errorf("internal call wrote parent response: headers=%v body=%q", recorder.Header(), recorder.Body.String())
			}
		})
	}
}

func TestNyanCallMeOutCheckPreservesResultFormats(t *testing.T) {
	initTestLogger()
	for _, tt := range []struct {
		name        string
		body        string
		status      int
		contentType string
	}{
		{"JSON object with status", `{"status":201,"value":"created"}`, 201, "application/json"},
		{"JSON without status", `{"value":"unchanged"}`, 200, "application/json"},
		{"failed JSON result", `{"success":false,"status":403,"value":"denied"}`, 403, "application/json"},
		{"JSON array", `[1,"two",null]`, 200, "application/json"},
		{"JSON null", `null`, 200, "application/json"},
		{"JSON string", `"text"`, 200, "application/json"},
		{"plain text", "raw 日本語", 200, "text/plain"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rootDir := t.TempDir()
			mainPath := filepath.Join(rootDir, "main.js")
			outPath := filepath.Join(rootDir, "out.js")
			writeHotReloadTestFile(t, mainPath, strconv.Quote(tt.body)+";")
			key := "nyanCallMe output: " + t.Name()
			t.Cleanup(func() { storage.Delete(key) })
			writeHotReloadTestFile(t, outPath, fmt.Sprintf(`
nyanSetItem(%q, JSON.stringify({api:nyanAllParams.api, output:nyanAllParams.nyan_output,
 status:nyanAllParams.nyan_output_status, contentType:nyanAllParams.nyan_output_content_type,
 body:nyanAllParams.nyan_output_body, base64:nyanAllParams.nyan_output_body_base64}));
({success:true,status:200,result:null});`, key))
			snapshot := newAPIConfigSnapshot(filepath.Join(rootDir, "api.json"), map[string]interface{}{
				"nested/target": map[string]interface{}{"script": mainPath, "outCheck": outPath},
			}, nil, nil, nil, nil)
			vm := goja.New()
			setupGojaVMWithSnapshot(vm, snapshot, nil)
			value, err := vm.RunString(`nyanCallMe({api:"nested/target"});`)
			if err != nil {
				t.Fatal(err)
			}
			var want interface{}
			if err := json.Unmarshal([]byte(tt.body), &want); err != nil {
				want = tt.body
			}
			if !reflect.DeepEqual(value.Export(), want) {
				t.Errorf("result = %#v, want %#v", value.Export(), want)
			}
			raw, ok := storage.Load(key)
			if !ok {
				t.Fatal("outCheck did not execute")
			}
			var observed struct {
				API         string `json:"api"`
				Status      int    `json:"status"`
				ContentType string `json:"contentType"`
				Body        string `json:"body"`
				Base64      string `json:"base64"`
				Output      struct {
					Status          int    `json:"status"`
					ContentType     string `json:"contentType"`
					Body            string `json:"body"`
					Base64          string `json:"bodyBase64"`
					BodyLength      int    `json:"bodyLength"`
					BodyLengthBytes int    `json:"bodyLengthBytes"`
				} `json:"output"`
			}
			if err := json.Unmarshal([]byte(raw.(string)), &observed); err != nil {
				t.Fatal(err)
			}
			encoded := base64.StdEncoding.EncodeToString([]byte(tt.body))
			if observed.API != "nested/target" || observed.Body != tt.body || observed.Output.Body != tt.body ||
				observed.Status != tt.status || observed.Output.Status != tt.status ||
				observed.ContentType != tt.contentType || observed.Output.ContentType != tt.contentType ||
				observed.Base64 != encoded || observed.Output.Base64 != encoded ||
				observed.Output.BodyLength != len(tt.body) || observed.Output.BodyLengthBytes != len(tt.body) {
				t.Fatalf("outCheck observed unexpected output: %s", raw)
			}
		})
	}
}

func TestNyanCallMeChecksKeepSnapshotAndRequestContext(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	rootDir := t.TempDir()
	writeScript := func(name, body string) string {
		path := filepath.Join(rootDir, name+".js")
		writeHotReloadTestFile(t, path, body)
		return path
	}
	oldIdentity := writeScript("old", `JSON.stringify({generation:"old"});`)
	newIdentity := writeScript("new", `JSON.stringify({generation:"new"});`)
	checker := writeScript("check", `
if (nyanAllParams.api !== "target" || nyanCallMe({api:"identity"}).generation !== "old" || nyanGetCookie("session") !== "original-request") {
 throw new Error("checker lost captured snapshot or request context");
}
({success:true,status:200,result:null});`)
	mainScript := writeScript("main", `JSON.stringify({generation:nyanCallMe({api:"identity"}).generation,cookie:nyanGetCookie("session")});`)
	captured := newAPIConfigSnapshot(filepath.Join(rootDir, "api.json"), map[string]interface{}{
		"target":   map[string]interface{}{"script": mainScript, "paramCheck": checker, "outCheck": checker},
		"identity": map[string]interface{}{"script": oldIdentity},
	}, nil, nil, nil, nil)
	original := currentAPISnapshot()
	t.Cleanup(func() { publishAPISnapshot(original) })
	publishAPISnapshot(newAPIConfigSnapshot(filepath.Join(rootDir, "api.json"), map[string]interface{}{
		"identity": map[string]interface{}{"script": newIdentity},
	}, nil, nil, nil, nil))
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/parent", nil)
	ginCtx.Request.AddCookie(&http.Cookie{Name: "session", Value: "original-request"})
	vm := goja.New()
	setupGojaVMWithSnapshot(vm, captured, ginCtx)
	value, err := vm.RunString(`nyanCallMe({api:"target"});`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"generation": "old", "cookie": "original-request"}
	if !reflect.DeepEqual(value.Export(), want) {
		t.Fatalf("nested result = %#v, want %#v", value.Export(), want)
	}
}

func TestNyanCallMePush(t *testing.T) {
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:null});`
	const deny = `({success:false,status:403,result:"blocked"});`
	const normal = `{"status":200,"value":"original result"}`
	for _, tc := range []struct {
		name, body, param, out, pushParam, pushMain, pushOut, order, wantResult string
		noChecks, noOut, checkOnly, wantPush, wantError, noRequest              bool
	}{
		{name: "allow", wantPush: true},
		{name: "without output checker", noOut: true, wantPush: true, order: "param,main,push-param,push-main,push-out,"},
		{name: "without checks or request", noChecks: true, noRequest: true, wantPush: true, order: "main,push-param,push-main,push-out,"},
		{name: "plain text", body: "plain result", wantPush: true},
		{name: "no status", body: `{"value":"ok"}`, wantPush: true},
		{name: "created", body: `{"status":201}`, wantPush: true},
		{name: "redirect", body: `{"status":302}`, wantPush: true},
		{name: "last allowed status", body: `{"status":399}`, wantPush: true},
		{name: "bad request", body: `{"status":400}`},
		{name: "conflict", body: `{"status":409}`},
		{name: "server error", body: `{"status":500,"success":true}`},
		{name: "false with 200", body: `{"status":200,"success":false}`},
		{name: "false without status", body: `{"success":false}`},
		{name: "checkOnly", checkOnly: true, order: "param,", wantResult: `{"success":true,"status":200,"result":null}`},
		{name: "checkOnly without checker", checkOnly: true, noChecks: true, order: "none", wantError: true},
		{name: "input denied", param: deny, order: "param,", wantResult: `{"success":false,"status":403,"result":"blocked"}`},
		{name: "input non-200", param: `({success:true,status:202,result:null});`, wantPush: true},
		{name: "output denied", out: deny, wantResult: `{"success":false,"status":403,"result":"blocked"}`},
		{name: "input exception", param: `throw new Error("boom");`, wantError: true, order: "param,"},
		{name: "main exception", body: "throw", wantError: true, order: "param,main,"},
		{name: "output exception", out: `throw new Error("boom");`, wantError: true},
		{name: "push input denied", pushParam: deny, order: "param,main,out,push-param,"},
		{name: "push output denied", pushOut: deny, order: "param,main,out,push-param,push-main,push-out,"},
		{name: "push main exception", pushMain: `throw new Error("boom");`, order: "param,main,out,push-param,push-main,"},
		{name: "push input exception", pushParam: `throw new Error("boom");`, order: "param,main,out,push-param,"},
		{name: "push output exception", pushOut: `throw new Error("boom");`, order: "param,main,out,push-param,push-main,push-out,"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, key := t.TempDir(), t.Name()
			t.Cleanup(func() { storage.Delete(key) })
			cookie := "source-cookie"
			if tc.noRequest {
				cookie = ""
			}
			write := func(stage, body string) string {
				path := filepath.Join(dir, stage+".js")
				guard := fmt.Sprintf(`
if(nyanAllParams.api!=="child" || nyanAllParams.value!=="input" || nyanGetCookie("session")!==%q || nyanGetFile("data.txt")!=="root-data") throw new Error("wrong execution context");
nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, cookie, key, key, stage+",")
				if strings.HasPrefix(stage, "push-") {
					// Push sees its own target API; retain the source request context.
					guard = strings.ReplaceAll(guard, `api!=="child"`, `api!=="sink"`)
					// Handshake checks have no internal-call parameters.
					guard = `if(nyanAllParams.value==="input"){` + guard + `nyanSetCookie("push-cookie","ignored");}`
					body = `if(nyanAllParams.value!=="input"){` + allow + `}else{` + body + `}`
				}
				writeHotReloadTestFile(t, path, guard+body)
				return path
			}
			body := tc.body
			if body == "" {
				body = normal
			}
			mainScript := strconv.Quote(body) + ";"
			if body == "throw" {
				mainScript = `throw new Error("boom");`
			}
			orAllow := func(script string) string {
				if script == "" {
					return allow
				}
				return script
			}
			child := map[string]interface{}{"script": write("main", mainScript), "push": "sink"}
			if !tc.noChecks {
				child["paramCheck"] = write("param", orAllow(tc.param))
				if !tc.noOut {
					child["outCheck"] = write("out", orAllow(tc.out))
				}
			}
			pushMain := tc.pushMain
			if pushMain == "" {
				pushMain = `"Push: notification";`
			}
			recovery := filepath.Join(dir, "recovery.js")
			writeHotReloadTestFile(t, recovery, `"barrier";`)
			f := newWebSocketCheckFixture(t, map[string]interface{}{
				"child": child,
				"sink": map[string]interface{}{
					"paramCheck": write("push-param", orAllow(tc.pushParam)),
					"script":     write("push-main", pushMain), "outCheck": write("push-out", orAllow(tc.pushOut)),
				},
				"recovery": map[string]interface{}{"script": recovery},
			})
			writeHotReloadTestFile(t, filepath.Join(f.dir, "data.txt"), "root-data")
			barrier := func(conn *websocket.Conn) {
				if got := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
					t.Fatalf("unexpected delivery: %s", got)
				}
			}
			var subscribers []*websocket.Conn
			for i := 0; i < 3; i++ {
				conn := f.dial("/sink", http.Header{"Cookie": {"session=receiver-cookie"}})
				barrier(conn)
				subscribers = append(subscribers, conn)
			}
			// A newer published configuration must not replace the caller's snapshot.
			captured := currentAPISnapshot()
			publishAPISnapshot(newAPIConfigSnapshot(captured.RootPath, map[string]interface{}{
				"recovery": captured.Definitions["recovery"],
			}, nil, nil, nil, nil))
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/parent", nil)
			ctx.Request.AddCookie(&http.Cookie{Name: "session", Value: cookie})
			if tc.noRequest {
				ctx = nil
			}
			vm := goja.New()
			setupGojaVMWithSnapshot(vm, captured, ctx)
			mode := ""
			if tc.checkOnly {
				mode = "checkOnly"
			}
			value, err := vm.RunString(fmt.Sprintf(`nyanCallMe({api:"child",value:"input",nyan_mode:%q});`, mode))
			if tc.wantError {
				wantError := "boom"
				if tc.checkOnly && tc.noChecks {
					wantError = "No check script"
				}
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("error=%v, want %s", err, wantError)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				wantText := body
				if tc.wantResult != "" {
					wantText = tc.wantResult
				}
				var want interface{}
				if json.Unmarshal([]byte(wantText), &want) != nil {
					want = wantText
				}
				if !reflect.DeepEqual(value.Export(), want) {
					t.Fatalf("return value=%#v, want %#v", value.Export(), want)
				}
			}
			order := tc.order
			if order == "" {
				order = "param,main,out,"
				if tc.wantPush {
					order += "push-param,push-main,push-out,"
				}
			} else if order == "none" {
				order = ""
			}
			gotOrder, _ := storage.Load(key)
			if gotOrder == nil {
				gotOrder = ""
			}
			if gotOrder != order {
				t.Fatalf("order=%v want=%s", gotOrder, order)
			}
			if len(recorder.Header()) != 0 || recorder.Body.Len() != 0 {
				t.Fatalf("Push modified the caller response: %v %s", recorder.Header(), recorder.Body.String())
			}
			for _, conn := range subscribers {
				if tc.wantPush {
					kind, data, err := conn.ReadMessage()
					if err != nil || kind != websocket.TextMessage || string(data) != "Push: notification" {
						t.Fatalf("Push=(%d,%q,%v)", kind, data, err)
					}
				}
				barrier(conn) // Proves absence of rejected or duplicate deliveries.
			}
		})
	}
}

func TestNyanCallMePushBeforeParentCompletes(t *testing.T) {
	for _, rejectParent := range []bool{false, true} {
		t.Run(fmt.Sprintf("reject parent=%t", rejectParent), func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string) string {
				path := filepath.Join(dir, name+".js")
				writeHotReloadTestFile(t, path, body)
				return path
			}
			f := newWebSocketCheckFixture(t, map[string]interface{}{
				"parent": map[string]interface{}{
					"script":   write("parent", `const child=nyanCallMe({api:"child"}); JSON.stringify({status:200,child:child});`),
					"outCheck": write("parent-out", fmt.Sprintf(`({success:%t,status:200,result:"parent denied"});`, !rejectParent)), "push": "sink",
				},
				"child":    map[string]interface{}{"script": write("child", `JSON.stringify({status:200,value:"child result"});`), "push": "sink"},
				"sink":     map[string]interface{}{"script": write("sink", `JSON.stringify({api:nyanAllParams.api,cookie:nyanGetCookie("session")});`)},
				"recovery": map[string]interface{}{"script": write("recovery", `"barrier";`)},
			})
			sink := f.dial("/sink", nil)
			if got := exchangeWebSocketCheckFrame(t, sink, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
				t.Fatal(got)
			}
			request, err := http.NewRequest(http.MethodGet, f.server.URL+"/parent", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.AddCookie(&http.Cookie{Name: "session", Value: "parent-cookie"})
			response, err := f.server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != 200 {
				t.Fatalf("response=%s err=%v", body, err)
			}
			if rejectParent {
				if !containsJSONValue(body, "result", "parent denied") {
					t.Fatalf("parent rejection lost: %s", body)
				}
			} else if !strings.Contains(string(body), "child result") {
				t.Fatalf("child result lost: %s", body)
			}
			apis := []string{"sink"}
			if !rejectParent {
				apis = append(apis, "sink")
			}
			for _, api := range apis {
				kind, data, err := sink.ReadMessage()
				if err != nil || kind != websocket.TextMessage || !containsJSONValue(data, "api", api) || !containsJSONValue(data, "cookie", "parent-cookie") {
					t.Fatalf("Push=%s err=%v, want api=%s", data, err, api)
				}
			}
			if got := exchangeWebSocketCheckFrame(t, sink, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
				t.Fatalf("duplicate Push: %s", got)
			}
		})
	}
}

func TestPushTargetChecksAcrossTransports(t *testing.T) {
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:null});`
	cases := []struct {
		name, param, out, wantOrder                                                   string
		missingParam, missingOut, mainError, noChecks, aliases, legacyAlias, wantPush bool
	}{
		{name: "allow", param: allow, out: allow, wantOrder: "param,main,out,", wantPush: true},
		{name: "no checks", noChecks: true, wantOrder: "main,", wantPush: true},
		{name: "aliases", param: allow, out: allow, aliases: true, wantOrder: "param,main,out,", wantPush: true},
		{name: "legacy alias", param: allow, out: allow, legacyAlias: true, wantOrder: "param,main,out,", wantPush: true},
		{name: "param denial", param: `({success:false,status:403,result:"denied"});`, out: allow, wantOrder: "param,"},
		{name: "param non-200", param: `({success:true,status:202,result:null});`, out: allow, wantOrder: "param,main,out,", wantPush: true},
		{name: "param exception", param: `throw new Error("check failed");`, out: allow, wantOrder: "param,"},
		{name: "param fractional status", param: `({success:true,status:200.5});`, out: allow, wantOrder: "param,"},
		{name: "out fractional status", param: allow, out: `({success:true,status:200.5});`, wantOrder: "param,main,out,"},
		{name: "invalid param result", param: `({success:true});`, out: allow, wantOrder: "param,"},
		{name: "missing param", missingParam: true, out: allow},
		{name: "out denial", param: allow, out: `({success:false,status:409,result:"denied"});`, wantOrder: "param,main,out,"},
		{name: "out non-200", param: allow, out: `({success:true,status:202,result:null});`, wantOrder: "param,main,out,", wantPush: true},
		{name: "out exception", param: allow, out: `throw new Error("check failed");`, wantOrder: "param,main,out,"},
		{name: "invalid out result", param: allow, out: `"invalid";`, wantOrder: "param,main,out,"},
		{name: "missing out", param: allow, missingOut: true, wantOrder: "param,main,"},
		{name: "main exception", param: allow, out: allow, mainError: true, wantOrder: "param,main,"},
	}
	for _, transport := range []string{"http", "root", "jsonrpc", "websocket"} {
		for _, tc := range cases {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				dir, key := t.TempDir(), t.Name()
				t.Cleanup(func() { storage.Delete(key) })
				writeScript := func(name, code string) string {
					path := filepath.Join(dir, name+".js")
					code = fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, key, key, name+",") + code
					if name == "param" {
						// Permit the receiver's handshake; exercise Push checks on
						// the Push target parameters afterward.
						code = `if(nyanAllParams.value!=="input") { ({success:true,status:200,result:null}); } else {` + code + `}`
					}
					writeHotReloadTestFile(t, path, code)
					return path
				}
				const payload = `{"status":201,"value":"通知"}`
				wireBody := "Push: " + payload
				contentType, status := "text/plain", 200
				if transport == "websocket" {
					wireBody, contentType, status = payload, "application/json", 201
				}
				contextCheck := `if(nyanAllParams.api!=="sink" || nyanAllParams.value!=="input") throw new Error("wrong parameters");`
				main := contextCheck + strconv.Quote("Push: "+payload) + ";"
				if tc.mainError {
					main = `throw new Error("main failed");`
				}
				target := map[string]interface{}{"script": writeScript("main", main)}
				paramKey, outKey := "paramCheck", "outCheck"
				if tc.aliases {
					paramKey, outKey = "paramcheck", "outcheck"
				}
				if tc.legacyAlias {
					paramKey = "check"
				}
				if !tc.noChecks {
					target[paramKey] = writeScript("param", contextCheck+tc.param)
					outputCheck := fmt.Sprintf(`
var o=nyanAllParams.nyan_output;
if(o.body!==%q || o.bodyBase64!==%q || o.bodyLength!==%d || o.bodyLengthBytes!==%d || o.status!==%d || o.contentType!==%q || Object.keys(o.headers).length!==0) throw new Error("wrong output metadata");
if(nyanAllParams.nyan_output_body!==o.body || nyanAllParams.nyan_output_body_base64!==o.bodyBase64) throw new Error("wrong output aliases");
`, wireBody, base64.StdEncoding.EncodeToString([]byte(wireBody)), len(wireBody), len(wireBody), status, contentType)
					target[outKey] = writeScript("out", contextCheck+outputCheck+tc.out)
				}
				if tc.missingParam {
					target[paramKey] = writeScript("param", allow)
				}
				if tc.missingOut {
					target[outKey] = filepath.Join(dir, "missing-out.js")
				}
				source, recovery := filepath.Join(dir, "source.js"), filepath.Join(dir, "recovery.js")
				writeHotReloadTestFile(t, source, `JSON.stringify({status:200,body:"origin response"});`)
				writeHotReloadTestFile(t, recovery, `"barrier";`)
				f := newWebSocketCheckFixture(t, map[string]interface{}{
					"source":   map[string]interface{}{"script": source, "push": "sink"},
					"sink":     target,
					"recovery": map[string]interface{}{"script": recovery},
				})
				sink := f.dial("/sink", nil)
				// Confirm the receiver is registered before triggering Push.
				if got := exchangeWebSocketCheckFrame(t, sink, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
					t.Fatal(got)
				}
				if tc.missingParam {
					// Remove the checker after the receiver has connected.
					if err := os.Remove(target[paramKey].(string)); err != nil {
						t.Fatal(err)
					}
				}
				frameType := websocket.TextMessage
				if transport == "websocket" {
					frameType = websocket.BinaryMessage
					origin := f.dial("/source", nil)
					got := exchangeWebSocketCheckFrame(t, origin, frameType, `{"api":"source","value":"input"}`)
					if !containsJSONValue([]byte(got), "body", "origin response") {
						t.Fatalf("origin response changed: %s", got)
					}
					// Wait until the source handler has finished its Push dispatch.
					if got := exchangeWebSocketCheckFrame(t, origin, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
						t.Fatal(got)
					}
				} else {
					method, path, body := http.MethodGet, "/source?value=input", ""
					if transport == "root" {
						path = "/?api=source&value=input"
					} else if transport == "jsonrpc" {
						method, path = http.MethodPost, "/nyan-rpc"
						body = `{"jsonrpc":"2.0","method":"source","params":{"value":"input"},"id":1}`
					}
					req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					if body != "" {
						req.Header.Set("Content-Type", "application/json")
					}
					resp, err := f.server.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					var value map[string]interface{}
					if err := json.Unmarshal(data, &value); err != nil {
						t.Fatal(err)
					}
					if transport == "jsonrpc" {
						value, _ = value["result"].(map[string]interface{})
					}
					if resp.StatusCode != http.StatusOK || value["body"] != "origin response" {
						t.Fatalf("origin response changed: status=%d body=%s", resp.StatusCode, data)
					}
				}
				order, _ := storage.Load(key)
				if order == nil {
					order = ""
				}
				if order != tc.wantOrder {
					t.Fatalf("execution order=%q, want %q", order, tc.wantOrder)
				}
				if tc.wantPush {
					if err := sink.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
						t.Fatal(err)
					}
					gotType, got, err := sink.ReadMessage()
					if err != nil {
						t.Fatal(err)
					}
					if gotType != frameType || string(got) != wireBody {
						t.Fatalf("Push=(%d,%q), want (%d,%q)", gotType, got, frameType, wireBody)
					}
				}
				// A barrier frame proves rejected data/check results were not sent,
				// without relying on a timeout to assert the absence of a Push.
				if got := exchangeWebSocketCheckFrame(t, sink, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
					t.Fatalf("unexpected Push or check response: %s", got)
				}
			})
		}
	}
}

func TestPushSourceResultAcrossTransports(t *testing.T) {
	for _, transport := range []string{"http", "root", "jsonrpc", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			dir, key := t.TempDir(), t.Name()
			bodyKey := key + " body"
			t.Cleanup(func() { storage.Delete(key); storage.Delete(bodyKey) })
			write := func(stage, body string) string {
				path := filepath.Join(dir, stage+".js")
				prefix := fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, key, key, stage+",")
				writeHotReloadTestFile(t, path, prefix+body)
				return path
			}
			allow := `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:null});`
			recovery := filepath.Join(dir, "recovery.js")
			writeHotReloadTestFile(t, recovery, `"barrier";`)
			f := newWebSocketCheckFixture(t, map[string]interface{}{
				"source": map[string]interface{}{
					"paramCheck": write("source-param", allow),
					"script":     write("source-main", fmt.Sprintf(`if(nyanGetItem(%q)==="throw") throw new Error("failed"); nyanGetItem(%q);`, bodyKey, bodyKey)),
					"outCheck":   write("source-out", allow), "push": "sink",
				},
				"sink":     map[string]interface{}{"paramCheck": write("push-param", allow), "script": write("push-main", `"notification";`), "outCheck": write("push-out", allow)},
				"recovery": map[string]interface{}{"script": recovery},
			})
			barrier := func(conn *websocket.Conn) {
				if got := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
					t.Fatalf("unexpected Push: %s", got)
				}
			}
			var subscribers []*websocket.Conn
			for i := 0; i < 3; i++ {
				conn := f.dial("/sink", nil)
				barrier(conn)
				subscribers = append(subscribers, conn)
			}
			var source *websocket.Conn
			if transport == "websocket" {
				source = f.dial("/", nil)
				barrier(source)
			}
			for _, tc := range []struct {
				name, body            string
				status                int
				push, failed, nonHTTP bool
			}{
				{"success", `{"success":true,"status":200,"value":"ok"}`, 200, true, false, false},
				{"created without success", `{"status":201,"value":"created"}`, 201, true, false, false},
				{"redirect", `{"status":302,"value":"redirect"}`, 302, true, false, false},
				{"last non-error status", `{"status":399,"value":"ok"}`, 399, true, false, false},
				{"bad request", `{"status":400,"value":"bad"}`, 400, false, false, false},
				{"conflict", `{"success":false,"status":409,"value":"conflict"}`, 409, false, true, false},
				{"server error", `{"success":false,"status":500,"value":"failed"}`, 500, false, true, false},
				{"server error with success true", `{"success":true,"status":500,"value":"failed"}`, 500, false, false, false},
				{"unavailable without success", `{"status":503,"value":"failed"}`, 503, false, false, false},
				{"false with 200", `{"success":false,"status":200,"value":"failed"}`, 200, false, true, false},
				{"false without status", `{"success":false,"value":"failed"}`, 200, false, true, true},
				{"no status", `{"value":"ok"}`, 200, true, false, true},
				{"plain text", "plain text", 200, true, false, true},
				{"exception", "throw", 500, false, false, false},
			} {
				if tc.nonHTTP && (transport == "http" || transport == "root") {
					continue
				}
				if tc.name == "plain text" && transport != "websocket" {
					continue
				}
				t.Run(tc.name, func(t *testing.T) {
					storage.Store(bodyKey, tc.body)
					storage.Delete(key)
					if transport == "websocket" {
						frameType := websocket.BinaryMessage
						if tc.name == "exception" {
							frameType = websocket.TextMessage
						}
						got := exchangeWebSocketCheckFrame(t, source, frameType, `{"api":"source"}`)
						if tc.name != "exception" && got != tc.body {
							t.Fatalf("response changed: %s", got)
						}
						barrier(source)
					} else {
						method, path, body := "GET", "/source", ""
						if transport == "root" {
							path = "/?api=source"
						}
						if transport == "jsonrpc" {
							method, path, body = "POST", "/nyan-rpc", `{"jsonrpc":"2.0","id":1,"method":"source","params":{}}`
						}
						request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
						if err != nil {
							t.Fatal(err)
						}
						if body != "" {
							request.Header.Set("Content-Type", "application/json")
						}
						response, err := f.server.Client().Do(request)
						if err != nil {
							t.Fatal(err)
						}
						data, err := io.ReadAll(response.Body)
						response.Body.Close()
						if err != nil {
							t.Fatal(err)
						}
						wantStatus := tc.status
						if transport == "jsonrpc" {
							wantStatus = 200
						}
						if response.StatusCode != wantStatus {
							t.Fatalf("HTTP=%d body=%s", response.StatusCode, data)
						}
						if tc.name != "exception" {
							var got, want map[string]interface{}
							if err := json.Unmarshal(data, &got); err != nil {
								t.Fatal(err)
							}
							if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
								t.Fatal(err)
							}
							if transport == "jsonrpc" {
								if tc.failed {
									rpcError, ok := got["error"].(map[string]interface{})
									if !ok {
										t.Fatalf("expected RPC error: %s", data)
									}
									got, _ = rpcError["data"].(map[string]interface{})
								} else {
									got, _ = got["result"].(map[string]interface{})
									delete(want, "status")
								}
							}
							if !reflect.DeepEqual(got, want) {
								t.Fatalf("response changed: got=%v want=%v", got, want)
							}
						}
					}
					order := "source-param,source-main,source-out,"
					if tc.name == "exception" || (transport == "jsonrpc" && tc.failed) {
						order = "source-param,source-main,"
					}
					if tc.push {
						order += "push-param,push-main,push-out,"
					}
					if got, _ := storage.Load(key); got != order {
						t.Fatalf("execution order=%v want=%s", got, order)
					}
					for _, conn := range subscribers {
						if tc.push {
							if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
								t.Fatal(err)
							}
							kind, data, err := conn.ReadMessage()
							wantKind := websocket.TextMessage
							if transport == "websocket" {
								wantKind = websocket.BinaryMessage
							}
							if err != nil || kind != wantKind || string(data) != "notification" {
								t.Fatalf("Push=%d %q %v", kind, data, err)
							}
						}
						barrier(conn)
					}
				})
			}
		})
	}
}

func TestPushMultipleSubscribersAcrossTransports(t *testing.T) {
	for _, transport := range []string{"http", "root", "jsonrpc", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			dir, key := t.TempDir(), t.Name()
			modeKey := key + " mode"
			t.Cleanup(func() { storage.Delete(key); storage.Delete(modeKey) })
			write := func(name, body string) string {
				path := filepath.Join(dir, name+".js")
				writeHotReloadTestFile(t, path, body)
				return path
			}
			mark := func(stage string) string {
				return fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, key, key, stage+",")
			}
			target := map[string]interface{}{
				"paramCheck": write("param", mark("param")+fmt.Sprintf(`({success:nyanGetItem(%q)!=="param denied",status:200,result:null});`, modeKey)),
				"script":     write("sink", mark("main")+`"Push: notification";`),
				"outCheck":   write("out", mark("out")+fmt.Sprintf(`({success:nyanGetItem(%q)!=="out denied",status:200,result:null});`, modeKey)),
			}
			f := newWebSocketCheckFixture(t, map[string]interface{}{
				"source":   map[string]interface{}{"script": write("source", `JSON.stringify({status:200,body:"origin response"});`), "push": "sink"},
				"sink":     target,
				"other":    map[string]interface{}{"script": write("other", `"other";`)},
				"recovery": map[string]interface{}{"script": write("recovery", `"barrier";`)},
			})
			ready := func(conn *websocket.Conn) {
				if got := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
					t.Fatalf("unexpected frame: %s", got)
				}
			}
			var subscribers []*websocket.Conn
			for _, path := range []string{"/sink", "/api/sink", "/?api=sink"} {
				conn := f.dial(path, nil)
				ready(conn)
				subscribers = append(subscribers, conn)
			}
			other := f.dial("/other", nil)
			ready(other)
			var source *websocket.Conn
			if transport == "websocket" {
				source = f.dial("/source", nil)
				ready(source)
			}
			trigger := func() {
				if transport == "websocket" {
					got := exchangeWebSocketCheckFrame(t, source, websocket.BinaryMessage, `{"api":"source"}`)
					if !containsJSONValue([]byte(got), "body", "origin response") {
						t.Fatal(got)
					}
					ready(source) // The source handler finishes dispatch before reading its next frame.
					return
				}
				method, path, body := "GET", "/source", ""
				if transport == "root" {
					path = "/?api=source"
				}
				if transport == "jsonrpc" {
					method, path, body = "POST", "/nyan-rpc", `{"jsonrpc":"2.0","id":1,"method":"source","params":{}}`
				}
				request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				if body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				response, err := f.server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 || !strings.Contains(string(data), "origin response") {
					t.Fatalf("source response=%s err=%v", data, err)
				}
			}
			expectPush := func(conn *websocket.Conn) {
				if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				kind, data, err := conn.ReadMessage()
				wantKind, wantBody := websocket.TextMessage, "Push: notification"
				if transport == "websocket" {
					wantKind, wantBody = websocket.BinaryMessage, "notification"
				}
				if err != nil || kind != wantKind || string(data) != wantBody {
					t.Fatalf("push=(%d,%q,%v), want=(%d,%q)", kind, data, err, wantKind, wantBody)
				}
			}
			for _, mode := range []string{"allow", "param denied", "out denied"} {
				storage.Store(modeKey, mode)
				storage.Delete(key) // Discard the individual handshake checks.
				trigger()
				wantOrder := "param,main,out,"
				if mode == "param denied" {
					wantOrder = "param,"
				}
				if got, _ := storage.Load(key); got != wantOrder {
					t.Fatalf("checks/main ran per recipient: order=%v", got)
				}
				for _, conn := range subscribers {
					if mode == "allow" {
						expectPush(conn)
					}
					ready(conn) // Also proves no duplicate Push, and no rejected result was delivered.
				}
				ready(other) // Different APIs are not subscribed to this target.
			}
			storage.Store(modeKey, "allow")
			closePhase5WebSocket(subscribers[0]) // A leaves after B and C have registered.
			waitForHotReloadCondition(t, "only A removed", func() bool { return len(pushConnections.snapshot("sink")) == 2 })
			storage.Delete(key)
			trigger()
			for _, conn := range subscribers[1:] {
				expectPush(conn)
				ready(conn)
			}
			if got, _ := storage.Load(key); got != "param,main,out," {
				t.Fatalf("order=%v", got)
			}
			replacement := f.dial("/sink", nil)
			ready(replacement)
			closePhase5WebSocket(subscribers[2]) // Removing a middle registration preserves newer ones too.
			waitForHotReloadCondition(t, "B and replacement remain", func() bool { return len(pushConnections.snapshot("sink")) == 2 })
			trigger()
			for _, conn := range []*websocket.Conn{subscribers[1], replacement} {
				expectPush(conn)
				ready(conn)
			}
			closePhase5WebSocket(subscribers[1])
			closePhase5WebSocket(replacement)
			waitForHotReloadCondition(t, "last subscriber removed", func() bool { return len(pushConnections.snapshot("sink")) == 0 })
			pushConnections.RLock()
			_, exists := pushConnections.connections["sink"]
			pushConnections.RUnlock()
			if exists {
				t.Fatal("empty subscription entry retained")
			}
		})
	}
}

func TestPushContinuesAfterFailedSubscriber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sink.js")
	writeHotReloadTestFile(t, path, `"notification";`)
	f := newWebSocketCheckFixture(t, map[string]interface{}{"sink": map[string]interface{}{"script": path}})
	first := f.dial("/sink", nil)
	if got := exchangeWebSocketCheckFrame(t, first, websocket.TextMessage, `{"api":"sink"}`); got != "notification" {
		t.Fatal(got)
	}
	closed := pushConnections.snapshot("sink")[0]
	closePhase5WebSocket(first)
	waitForHotReloadCondition(t, "closed subscriber removed", func() bool { return len(pushConnections.snapshot("sink")) == 0 })
	// Keep a stale, definitively unwritable recipient first to exercise a send failure.
	_ = closed.Close()
	pushConnections.add("sink", closed)
	var healthy []*websocket.Conn
	for i := 0; i < 2; i++ {
		conn := f.dial("/sink", nil)
		if got := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"sink"}`); got != "notification" {
			t.Fatal(got)
		}
		healthy = append(healthy, conn)
	}
	snapshot := currentAPISnapshot()
	performPushWithSnapshot(snapshot, map[string]interface{}{"push": "sink"}, snapshot.Definitions, map[string]interface{}{"api": "source"}, f.dir)
	for _, conn := range healthy {
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, data, err := conn.ReadMessage()
		if err != nil || string(data) != "notification" {
			t.Fatalf("healthy subscriber missed Push: %q %v", data, err)
		}
	}
	if connections := pushConnections.snapshot("sink"); len(connections) != 2 || connections[0] == closed || connections[1] == closed {
		t.Fatalf("failed recipient retained: %v", connections)
	}
}

func TestPushConcurrentSubscriptionChanges(t *testing.T) {
	registry := pushConnectionRegistry{}
	stable := &serverWebSocket{}
	registry.add("sink", stable)
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				conn := &serverWebSocket{}
				registry.add("sink", conn)
				for _, recipient := range registry.snapshot("sink") {
					if recipient == nil {
						t.Error("snapshot contains a removed entry")
					}
				}
				registry.remove("sink", conn)
				registry.remove("sink", conn) // Handler cleanup can follow a send-failure removal.
			}
		}()
	}
	workers.Wait()
	if connections := registry.snapshot("sink"); len(connections) != 1 || connections[0] != stable {
		t.Fatalf("stable subscription was lost: %v", connections)
	}
}

func TestPushCheckOnlyStopsBeforeMain(t *testing.T) {
	for _, configured := range []bool{true, false} {
		t.Run(strconv.FormatBool(configured), func(t *testing.T) {
			key, dir := t.Name(), t.TempDir()
			t.Cleanup(func() { storage.Delete(key) })
			path := filepath.Join(dir, "check.js")
			writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,"checked");({success:true,status:200,result:null});`, key))
			mainPath, outPath := filepath.Join(dir, "main.js"), filepath.Join(dir, "out.js")
			writeHotReloadTestFile(t, mainPath, fmt.Sprintf(`nyanSetItem(%q,"main");"unexpected Push";`, key))
			writeHotReloadTestFile(t, outPath, fmt.Sprintf(`nyanSetItem(%q,"out");({success:true,status:200,result:null});`, key))
			target := map[string]interface{}{"script": mainPath, "outCheck": outPath}
			if configured {
				target["paramCheck"] = path
			}
			snapshot := newAPIConfigSnapshot(filepath.Join(dir, "api.json"), map[string]interface{}{"sink": target}, nil, nil, nil, nil)
			performPushWithSnapshot(snapshot, map[string]interface{}{"push": "sink"}, snapshot.Definitions, map[string]interface{}{"api": "source", "nyan_mode": "checkOnly"}, dir)
			value, ran := storage.Load(key)
			if ran != configured || (configured && value != "checked") {
				t.Fatalf("execution marker=%v, want only paramCheck configured=%v", value, configured)
			}
		})
	}
}

func TestPushChecksKeepCapturedSnapshot(t *testing.T) {
	dir, key := t.TempDir(), t.Name()
	t.Cleanup(func() { storage.Delete(key) })
	writeScript := func(name, body string) string {
		path := filepath.Join(dir, name+".js")
		writeHotReloadTestFile(t, path, body)
		return path
	}
	marker := fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+nyanCallMe({api:"identity"}).generation+",");`, key, key)
	target := map[string]interface{}{
		"paramCheck": writeScript("param", marker+`({success:true,status:200,result:null});`),
		"script":     writeScript("main", marker+`"notification";`),
		"outCheck":   writeScript("out", marker+`({success:true,status:200,result:null});`),
	}
	f := newWebSocketCheckFixture(t, map[string]interface{}{
		"sink":     target,
		"identity": map[string]interface{}{"script": writeScript("old", `JSON.stringify({generation:"old"});`)},
	})
	captured := currentAPISnapshot()
	publishAPISnapshot(newAPIConfigSnapshot(captured.RootPath, map[string]interface{}{
		"sink":     target,
		"identity": map[string]interface{}{"script": writeScript("new", `JSON.stringify({generation:"new"});`)},
	}, nil, nil, nil, nil))
	performPushWithSnapshot(captured, map[string]interface{}{"push": "sink"}, captured.Definitions, map[string]interface{}{"api": "source"}, f.dir)
	if got, _ := storage.Load(key); got != "old,old,old," {
		t.Fatalf("Push check/main snapshots=%v, want old,old,old,", got)
	}
}

// A real upgraded connection exercises frame types and the message loop. Wait for
// hijacked handlers explicitly: httptest.Server.Close does not wait for them.
type websocketCheckFixture struct {
	t      *testing.T
	dir    string
	server *httptest.Server
	conns  []*websocket.Conn
}

func isolatePushConnections(t *testing.T) {
	t.Helper()
	pushConnections.Lock()
	previous := pushConnections.connections
	pushConnections.connections = make(map[string][]*serverWebSocket)
	pushConnections.Unlock()
	t.Cleanup(func() {
		pushConnections.Lock()
		pushConnections.connections = previous
		pushConnections.Unlock()
	})
}

func newWebSocketCheckFixture(t *testing.T, definitions map[string]interface{}) *websocketCheckFixture {
	t.Helper()
	previousSnapshot, previousConfig, previousPaths, previousLogger := currentAPISnapshot(), globalConfig, servicePaths, logger
	isolatePushConnections(t)
	f := &websocketCheckFixture{t: t, dir: t.TempDir()}
	var handlers sync.WaitGroup
	t.Cleanup(func() {
		for _, conn := range f.conns {
			closePhase5WebSocket(conn)
		}
		if f.server != nil {
			f.server.Close()
		}
		done := make(chan struct{})
		go func() { handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("WebSocket handlers did not stop")
		}
		publishAPISnapshot(previousSnapshot)
		globalConfig, servicePaths, logger = previousConfig, previousPaths, previousLogger
	})
	apiPath := filepath.Join(f.dir, "api.json")
	data, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	writeHotReloadTestFile(t, apiPath, string(data))
	loaded, err := readAPIConfigFile(apiPath, f.dir)
	if err != nil {
		t.Fatal(err)
	}
	globalConfig = Config{}
	servicePaths.API.Path = apiPath
	logger = log.New(io.Discard, "", 0)
	publishAPISnapshot(loaded.Snapshot)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/", handleRequest)
	router.POST("/nyan-rpc", handleJSONRPC)
	if err := registerDynamicEndpoints(router, f.dir); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("local TCP listener is unavailable: %v", err)
	}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		router.ServeHTTP(w, r)
	}))
	f.server.Listener = listener
	f.server.Start()
	return f
}

func (f *websocketCheckFixture) dial(path string, headers http.Header) *websocket.Conn {
	f.t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+path, headers)
	if err != nil {
		f.t.Fatalf("dial WebSocket %s: %v", path, err)
	}
	f.conns = append(f.conns, conn)
	return conn
}

func exchangeWebSocketCheckFrame(t *testing.T, conn *websocket.Conn, frameType int, body string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(frameType, []byte(body)); err != nil {
		t.Fatal(err)
	}
	gotType, response, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if gotType != frameType {
		t.Fatalf("response frame type = %d, want %d", gotType, frameType)
	}
	return string(response)
}

func TestWebSocketChecksMessageFlow(t *testing.T) {
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:{checked:true}});`
	const denyParam = `({success:false,status:403,result:{message:"input blocked"}});`
	const denyOut = `({success:false,status:409,result:{message:"output blocked"}});`
	const mainBody = `{"success":false,"status":403,"value":"private result"}`
	tests := []struct {
		name, param, out, paramKey, outKey, wantOrder, wantBody string
		checkOnly, binary, missingParam, missingOut             bool
	}{
		{name: "failed main response suppresses Push", param: allow, out: allow, wantOrder: "param,main,out,", wantBody: mainBody},
		{name: "param denial", param: denyParam, out: allow, binary: true, wantOrder: "param,", wantBody: `{"success":false,"status":403,"result":{"message":"input blocked"}}`},
		{name: "param non-200", param: `({success:true,status:202,result:null});`, out: allow, wantOrder: "param,main,out,", wantBody: mainBody},
		{name: "output denial", param: allow, out: denyOut, binary: true, wantOrder: "param,main,out,", wantBody: `{"success":false,"status":409,"result":{"message":"output blocked"}}`},
		{name: "output non-200", param: allow, out: `({success:true,status:202,result:null});`, wantOrder: "param,main,out,", wantBody: mainBody},
		{name: "checkOnly", param: allow, out: allow, checkOnly: true, binary: true, wantOrder: "param,", wantBody: `{"success":true,"status":200,"result":{"checked":true}}`},
		{name: "checkOnly without param", out: allow, checkOnly: true, wantBody: `{"success":false,"status":404,"result":{"message":"No check script for this API"}}`},
		{name: "aliases", param: allow, out: allow, paramKey: "paramcheck", outKey: "outcheck", wantOrder: "param,main,out,", wantBody: mainBody},
		{name: "legacy check alias", param: denyParam, out: allow, paramKey: "check", wantOrder: "param,", wantBody: `{"success":false,"status":403,"result":{"message":"input blocked"}}`},
		{name: "param exception", param: `throw new Error("private exception");`, out: allow, binary: true, wantOrder: "param,", wantBody: `{"success":false,"status":500,"result":{"message":"Failed to run paramCheck"}}`},
		{name: "param fractional status", param: `({success:true,status:200.5});`, out: allow, wantOrder: "param,", wantBody: `{"success":false,"status":500,"result":{"message":"Failed to run paramCheck"}}`},
		{name: "out fractional status", param: allow, out: `({success:true,status:200.5});`, wantOrder: "param,main,out,", wantBody: `{"success":false,"status":500,"result":{"message":"Failed to run outCheck"}}`},
		{name: "invalid param result", param: `({success:true});`, out: allow, wantOrder: "param,", wantBody: `{"success":false,"status":500,"result":{"message":"Failed to run paramCheck"}}`},
		{name: "missing param file", missingParam: true, out: allow, wantBody: `{"success":false,"status":500,"result":{"message":"Failed to run paramCheck"}}`},
		{name: "output exception", param: allow, out: `throw new Error("private exception");`, binary: true, wantOrder: "param,main,out,", wantBody: `{"success":false,"status":500,"result":{"message":"Failed to run outCheck"}}`},
		{name: "invalid output result", param: allow, out: `"not JSON";`, wantOrder: "param,main,out,", wantBody: `{"success":false,"status":500,"result":{"message":"Failed to run outCheck"}}`},
		{name: "unserializable denial", param: allow, out: `({success:false,status:403,result:NaN});`, binary: true, wantOrder: "param,main,out,", wantBody: `{"success":false,"status":500,"result":{"message":"Failed to encode check response"}}`},
		{name: "missing output file", param: allow, missingOut: true, wantOrder: "param,main,", wantBody: `{"success":false,"status":500,"result":{"message":"Failed to run outCheck"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			key := "WebSocket flow: " + t.Name()
			t.Cleanup(func() { storage.Delete(key) })
			writeScript := func(name, body string) string {
				path := filepath.Join(dir, name+".js")
				writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, key, key, name+",")+body)
				return path
			}
			target := map[string]interface{}{"script": writeScript("main", strconv.Quote(mainBody)+";"), "push": "sink"}
			if tt.paramKey == "" {
				tt.paramKey = "paramCheck"
			}
			if tt.outKey == "" {
				tt.outKey = "outCheck"
			}
			if tt.param != "" {
				target[tt.paramKey] = writeScript("param", tt.param)
			}
			if tt.out != "" {
				target[tt.outKey] = writeScript("out", tt.out)
			}
			if tt.missingParam {
				target[tt.paramKey] = filepath.Join(dir, "missing-param.js")
			}
			if tt.missingOut {
				target[tt.outKey] = filepath.Join(dir, "missing-out.js")
			}
			recovery := filepath.Join(dir, "recovery.js")
			writeHotReloadTestFile(t, recovery, `"connection remains usable";`)
			f := newWebSocketCheckFixture(t, map[string]interface{}{
				"target":   target,
				"sink":     map[string]interface{}{"script": writeScript("push", `"push";`)},
				"recovery": map[string]interface{}{"script": recovery},
			})
			conn := f.dial("/", nil)
			if order, exists := storage.Load(key); exists {
				t.Fatalf("handshake ran scripts: %v", order)
			}
			request := `{"api":"target"}`
			if tt.checkOnly {
				request = `{"api":"target","nyan_mode":"checkOnly"}`
			}
			frameType := websocket.TextMessage
			if tt.binary {
				frameType = websocket.BinaryMessage
			}
			got := exchangeWebSocketCheckFrame(t, conn, frameType, request)
			var gotJSON, wantJSON interface{}
			if err := json.Unmarshal([]byte(got), &gotJSON); err != nil {
				t.Fatalf("response %q: %v", got, err)
			}
			if err := json.Unmarshal([]byte(tt.wantBody), &wantJSON); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotJSON, wantJSON) {
				t.Errorf("response = %s, want %s", got, tt.wantBody)
			}
			// The next response also proves the previous iteration (including any
			// Push dispatch) completed before we inspect execution markers.
			if got := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"recovery"}`); got != "connection remains usable" {
				t.Fatalf("recovery response = %q", got)
			}
			order, _ := storage.Load(key)
			if order == nil {
				order = ""
			}
			if order != tt.wantOrder {
				t.Fatalf("execution order = %q, want %q", order, tt.wantOrder)
			}
		})
	}
}

func TestWebSocketChecksTargetAndOutputMetadata(t *testing.T) {
	formats := []struct {
		name, body, contentType string
		status                  int
	}{
		{"JSON status", ` {"status":201,"value":"created"} `, "application/json", 201},
		{"JSON failure", `{"success":false,"status":403,"value":"private"}`, "application/json", 403},
		{"JSON no status", `{"value":"unchanged"}`, "application/json", 200},
		{"JSON array", `[1,"two",null]`, "application/json", 200},
		{"JSON null", `null`, "application/json", 200},
		{"JSON string", `"日本語"`, "application/json", 200},
		{"plain text", "raw 日本語", "text/plain", 200},
	}
	for index, tt := range formats {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			key := "WebSocket metadata: " + t.Name()
			t.Cleanup(func() { storage.Delete(key) })
			writeScript := func(name, body string) string {
				path := filepath.Join(dir, name+".js")
				writeHotReloadTestFile(t, path, body)
				return path
			}
			trustedParams := `
if (nyanAllParams.api !== "nested/target" || nyanAllParams._headers.Authorization !== "Bearer trusted-header" ||
 nyanAllParams._user_agent !== "check-test-agent" || nyanAllParams._remote_ip !== "127.0.0.1") {
 throw new Error("checker received untrusted request metadata or wrong target");
}
`
			param := writeScript("param", trustedParams+`({success:true,status:200,result:null});`)
			out := writeScript("out", trustedParams+fmt.Sprintf(`nyanSetItem(%q,JSON.stringify(nyanAllParams));
({success:true,status:200,result:null});`, key))
			channel := writeScript("channel", `throw new Error("connection URL main/out scripts must not execute");`)
			channelCheck := writeScript("channel-check", `if(nyanAllParams.api!=="channel") throw new Error("wrong connection target"); ({success:true,status:200,result:null});`)
			f := newWebSocketCheckFixture(t, map[string]interface{}{
				"channel":       map[string]interface{}{"script": channel, "paramCheck": channelCheck, "outCheck": channel},
				"nested/target": map[string]interface{}{"script": writeScript("main", strconv.Quote(tt.body)+";"), "paramCheck": param, "outCheck": out},
			})
			path := []string{"/channel", "/api/channel", "/"}[index%3]
			conn := f.dial(path, http.Header{"Authorization": {"Bearer trusted-header"}, "User-Agent": {"check-test-agent"}, "X-Forwarded-For": {"203.0.113.10"}})
			got := exchangeWebSocketCheckFrame(t, conn, websocket.BinaryMessage,
				`{"api":"nested/target","_headers":{"Authorization":"forged"},"_user_agent":"forged","_remote_ip":"203.0.113.10","nyan_output_status":599,"nyan_output_body":"forged"}`)
			if got != tt.body {
				t.Fatalf("raw response = %q, want %q", got, tt.body)
			}
			raw, exists := storage.Load(key)
			if !exists {
				t.Fatal("outCheck did not execute")
			}
			var params struct {
				Status      int    `json:"nyan_output_status"`
				ContentType string `json:"nyan_output_content_type"`
				Body        string `json:"nyan_output_body"`
				Base64      string `json:"nyan_output_body_base64"`
				Output      struct {
					Status          int    `json:"status"`
					ContentType     string `json:"contentType"`
					Body            string `json:"body"`
					Base64          string `json:"bodyBase64"`
					BodyLength      int    `json:"bodyLength"`
					BodyLengthBytes int    `json:"bodyLengthBytes"`
				} `json:"nyan_output"`
			}
			if err := json.Unmarshal([]byte(raw.(string)), &params); err != nil {
				t.Fatal(err)
			}
			encoded := base64.StdEncoding.EncodeToString([]byte(tt.body))
			if params.Status != tt.status || params.Output.Status != tt.status ||
				params.ContentType != tt.contentType || params.Output.ContentType != tt.contentType ||
				params.Body != tt.body || params.Output.Body != tt.body || params.Base64 != encoded || params.Output.Base64 != encoded ||
				params.Output.BodyLength != len(tt.body) || params.Output.BodyLengthBytes != len(tt.body) {
				t.Fatalf("outCheck metadata = %s", raw)
			}
		})
	}
}

func TestWebSocketChecksKeepSnapshotUntilNextMessage(t *testing.T) {
	dir := t.TempDir()
	startedKey, releaseKey, outputKey := t.Name()+":started", t.Name()+":release", t.Name()+":output"
	t.Cleanup(func() {
		storage.Delete(startedKey)
		storage.Delete(releaseKey)
		storage.Delete(outputKey)
	})
	writeScript := func(name, body string) string {
		path := filepath.Join(dir, name+".js")
		writeHotReloadTestFile(t, path, body)
		return path
	}
	param := writeScript("param", fmt.Sprintf(`
nyanSetItem(%q,nyanCallMe({api:"identity"}).generation);
var deadline = Date.now() + 2000;
while (nyanGetItem(%q) !== "released" && Date.now() < deadline) {}
if (nyanGetItem(%q) !== "released") { throw new Error("reload barrier timed out"); }
({success:true,status:200,result:null});`, startedKey, releaseKey, releaseKey))
	out := writeScript("out", fmt.Sprintf(`nyanSetItem(%q,nyanCallMe({api:"identity"}).generation);
({success:true,status:200,result:null});`, outputKey))
	main := writeScript("main", `JSON.stringify(nyanCallMe({api:"identity"}));`)
	oldIdentity := writeScript("old", `JSON.stringify({generation:"old"});`)
	newIdentity := writeScript("new", `JSON.stringify({generation:"new"});`)
	target := map[string]interface{}{"script": main, "paramCheck": param, "outCheck": out}
	f := newWebSocketCheckFixture(t, map[string]interface{}{
		"target":   target,
		"identity": map[string]interface{}{"script": oldIdentity},
	})
	t.Cleanup(func() { storage.Store(releaseKey, "released") })
	conn := f.dial("/", nil)
	if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"api":"target"}`)); err != nil {
		t.Fatal(err)
	}
	waitForHotReloadCondition(t, "paramCheck reload barrier", func() bool {
		value, exists := storage.Load(startedKey)
		return exists && value == "old"
	})
	publishAPISnapshot(newAPIConfigSnapshot(filepath.Join(f.dir, "api.json"), map[string]interface{}{
		"target":   target,
		"identity": map[string]interface{}{"script": newIdentity},
	}, nil, nil, nil, nil))
	storage.Store(releaseKey, "released")
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, got, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"generation":"old"}` {
		t.Fatalf("in-flight main response = %s, want old snapshot", got)
	}
	if generation, _ := storage.Load(outputKey); generation != "old" {
		t.Fatalf("in-flight outCheck used snapshot %v, want old", generation)
	}
	if got := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"target"}`); got != `{"generation":"new"}` {
		t.Fatalf("next message main response = %s, want new snapshot", got)
	}
	if generation, _ := storage.Load(startedKey); generation != "new" {
		t.Errorf("next message paramCheck used snapshot %v, want new", generation)
	}
	if generation, _ := storage.Load(outputKey); generation != "new" {
		t.Errorf("next message outCheck used snapshot %v, want new", generation)
	}
}

func TestWebSocketHandshakeChecks(t *testing.T) {
	const allow = `({success:true,status:(nyanAllParams.nyan_output ? nyanAllParams.nyan_output.status : 200),result:{checked:true}});`
	for _, route := range []string{"/nested/channel", "/api/nested/channel", "/?api=nested/channel"} {
		for _, tc := range []struct {
			name, code                         string
			status                             int
			checkOnly, noCheck, missing, alias bool
		}{
			{name: "allow", code: allow, status: 101},
			{name: "alias", code: allow, alias: true, status: 101},
			{name: "no checker", noCheck: true, status: 101},
			{name: "deny", code: `({success:false,status:403,result:"denied"});`, status: 403},
			{name: "non-200", code: `({success:true,status:202,result:null});`, status: 101},
			{name: "exception", code: `throw new Error("check failed");`, status: 500},
			{name: "invalid result", code: `({success:true});`, status: 500},
			{name: "missing checker", missing: true, status: 500},
			{name: "checkOnly", code: allow, checkOnly: true, status: 200},
			{name: "checkOnly denied", code: `({success:false,status:403,result:"denied"});`, checkOnly: true, status: 403},
			{name: "checkOnly without checker", noCheck: true, checkOnly: true, status: 404},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				dir, key := t.TempDir(), t.Name()
				t.Cleanup(func() { storage.Delete(key) })
				param, main, out, recovery := filepath.Join(dir, "param.js"), filepath.Join(dir, "main.js"), filepath.Join(dir, "out.js"), filepath.Join(dir, "recovery.js")
				writeHotReloadTestFile(t, param, fmt.Sprintf(`
nyanSetItem(%q,(nyanGetItem(%q) ?? "")+"param,");
if(nyanAllParams.api!=="nested/channel" || nyanAllParams.token!=="allowed" || nyanAllParams._remote_ip!=="127.0.0.1" || nyanAllParams._headers.Origin!=="https://trusted.example" || nyanAllParams._user_agent!=="handshake-test") throw new Error("wrong connection parameters");
if(nyanGetCookie("session")!=="trusted" || nyanGetRequestHeaders().Origin!=="https://trusted.example" || nyanGetUserAgent()!=="handshake-test") throw new Error("missing HTTP context");
`, key, key)+tc.code)
				writeHotReloadTestFile(t, main, fmt.Sprintf(`nyanSetItem(%q,"main");"main";`, key))
				writeHotReloadTestFile(t, out, fmt.Sprintf(`nyanSetItem(%q,"out");({success:true,status:200,result:null});`, key))
				writeHotReloadTestFile(t, recovery, `"ready";`)
				entry := map[string]interface{}{"script": main, "outCheck": out}
				if !tc.noCheck {
					field := "paramCheck"
					if tc.alias {
						field = "check"
					}
					entry[field] = param
					if tc.missing {
						entry[field] = filepath.Join(dir, "missing.js")
					}
				}
				f := newWebSocketCheckFixture(t, map[string]interface{}{
					"nested/channel": entry, "recovery": map[string]interface{}{"script": recovery},
				})
				separator := "?"
				if strings.Contains(route, "?") {
					separator = "&"
				}
				path := route + separator + "token=allowed&_remote_ip=forged&_headers=forged&_user_agent=forged"
				if tc.checkOnly {
					path += "&nyan_mode=checkOnly"
				}
				headers := http.Header{"Origin": {"https://trusted.example"}, "Cookie": {"session=trusted"}, "User-Agent": {"handshake-test"}}
				if tc.status == http.StatusSwitchingProtocols {
					conn := f.dial(path, headers)
					if got := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"recovery"}`); got != "ready" {
						t.Fatal(got)
					}
					if len(pushConnections.snapshot("nested/channel")) != 1 {
						t.Fatal("approved connection was not registered under canonical API name")
					}
				} else {
					dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
					conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+path, headers)
					if conn != nil {
						conn.Close()
						t.Fatal("rejected/checkOnly request was upgraded")
					}
					if err == nil || resp == nil {
						t.Fatalf("expected HTTP check response, got response=%v err=%v", resp, err)
					}
					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					var check ParamCheckResponse
					if err := json.Unmarshal(body, &check); err != nil {
						t.Fatalf("response=%s: %v", body, err)
					}
					if resp.StatusCode != tc.status || check.Status != tc.status {
						t.Fatalf("HTTP=%d check=%s, want %d", resp.StatusCode, body, tc.status)
					}
					if tc.checkOnly && tc.status == 200 {
						if !check.Success {
							t.Fatalf("checkOnly failed: %s", body)
						}
						if tc.noCheck {
							if check.Result != nil {
								t.Fatalf("expected null result: %s", body)
							}
						} else if result, ok := check.Result.(map[string]interface{}); !ok || result["checked"] != true {
							t.Fatalf("check result lost: %s", body)
						}
					}
					if len(pushConnections.snapshot("nested/channel")) != 0 {
						t.Fatal("rejected/checkOnly connection registered for Push")
					}
				}
				want := "param,"
				if tc.noCheck || tc.missing {
					want = ""
				}
				got, _ := storage.Load(key)
				if got == nil {
					got = ""
				}
				if got != want {
					t.Fatalf("handshake order=%q, want %q", got, want)
				}
			})
		}
	}
}

func TestWebSocketRootHandshakeRejectsUnavailableAPI(t *testing.T) {
	for _, tc := range []struct {
		name       string
		definition map[string]interface{}
		status     int
	}{
		{name: "missing", status: 404},
		{name: "disabled", definition: map[string]interface{}{"script": "unused.js", "websocket": false}, status: 403},
		{name: "public", definition: map[string]interface{}{"type": "public", "path": "."}, status: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definitions := map[string]interface{}{}
			if tc.definition != nil {
				definitions["target"] = tc.definition
			}
			f := newWebSocketCheckFixture(t, definitions)
			conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+"/?api=target", nil)
			if conn != nil {
				conn.Close()
				t.Fatal("unavailable API accepted a subscription")
			}
			if resp != nil {
				defer resp.Body.Close()
			}
			if err == nil || resp == nil || resp.StatusCode != tc.status {
				t.Fatalf("response=%v err=%v, want %d", resp, err, tc.status)
			}
		})
	}
}

func TestWebSocketChecksHandshakeThenEachMessage(t *testing.T) {
	dir, key := t.TempDir(), t.Name()
	t.Cleanup(func() { storage.Delete(key) })
	write := func(name, body string) string {
		path := filepath.Join(dir, name+".js")
		writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, key, key, name+",")+body)
		return path
	}
	f := newWebSocketCheckFixture(t, map[string]interface{}{
		"target": map[string]interface{}{
			"paramCheck": write("param", `({success:true,status:200,result:{checked:true}});`),
			"script":     write("main", `"body";`),
			"outCheck":   write("out", `({success:nyanAllParams.nyan_output.body==="body",status:200,result:null});`),
		},
	})
	conn := f.dial("/target", nil)
	if got, _ := storage.Load(key); got != "param," {
		t.Fatalf("connection order=%v", got)
	}
	if got := exchangeWebSocketCheckFrame(t, conn, websocket.BinaryMessage, `{"api":"target"}`); got != "body" {
		t.Fatal(got)
	}
	if got, _ := storage.Load(key); got != "param,param,main,out," {
		t.Fatalf("first message order=%v", got)
	}
	body := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"target","nyan_mode":"checkOnly"}`)
	assertParamCheckResponse(t, []byte(body), true, http.StatusOK)
	if got, _ := storage.Load(key); got != "param,param,main,out,param," {
		t.Fatalf("checkOnly message order=%v", got)
	}
	if got := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"target"}`); got != "body" {
		t.Fatal(got)
	}
	if got, _ := storage.Load(key); got != "param,param,main,out,param,param,main,out," {
		t.Fatalf("next message order=%v", got)
	}
}

func TestWebSocketRootCheckOnlyDoesNotUpgrade(t *testing.T) {
	f := newWebSocketCheckFixture(t, map[string]interface{}{})
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+"/?nyan_mode=checkOnly", nil)
	if conn != nil {
		conn.Close()
		t.Fatal("checkOnly upgraded")
	}
	if resp == nil || err == nil {
		t.Fatalf("response=%v err=%v", resp, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("HTTP=%d body=%s", resp.StatusCode, body)
	}
	assertParamCheckResponse(t, body, false, http.StatusNotFound)
	if len(pushConnections.snapshot("")) != 0 {
		t.Fatal("checkOnly registered a root subscription")
	}
}

// getAPIArgumentTestTransport captures requests without sending them to an external service.
type getAPIArgumentTestTransport func(*http.Request) (*http.Response, error)

func (transport getAPIArgumentTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestNyanGetAPIArguments(t *testing.T) {
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	for _, tc := range []struct {
		name, arguments, wantUser, wantPass   string
		wantAuth, noArguments, transportError bool
		status                                int
	}{
		{name: "no_arguments", noArguments: true},
		{name: "url_only", arguments: `url`},
		{name: "empty_username", arguments: `url, ""`},
		{name: "empty_credentials", arguments: `url, "", ""`},
		{name: "username_only", arguments: `url, "alice"`, wantAuth: true, wantUser: "alice"},
		{name: "credentials", arguments: `url, "alice", "secret"`, wantAuth: true, wantUser: "alice", wantPass: "secret"},
		{name: "password_without_username", arguments: `url, "", "secret"`},
		{name: "explicit_undefined_username", arguments: `url, undefined, "secret"`, wantAuth: true, wantUser: "undefined", wantPass: "secret"},
		{name: "explicit_null_username", arguments: `url, null, "secret"`, wantAuth: true, wantUser: "null", wantPass: "secret"},
		{name: "explicit_undefined_password", arguments: `url, "alice", undefined`, wantAuth: true, wantUser: "alice", wantPass: "undefined"},
		{name: "explicit_null_password", arguments: `url, "alice", null`, wantAuth: true, wantUser: "alice", wantPass: "null"},
		{name: "extra_argument", arguments: `url, "alice", "secret", "ignored"`, wantAuth: true, wantUser: "alice", wantPass: "secret"},
		{name: "http_404", arguments: `url`, status: http.StatusNotFound},
		{name: "http_500", arguments: `url`, status: http.StatusInternalServerError},
		{name: "transport_error", arguments: `url`, transportError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			http.DefaultTransport = getAPIArgumentTestTransport(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "getapi.test" || request.URL.Path != "/data" {
					t.Errorf("unexpected request: %s %s", request.Method, request.URL)
				}
				query := request.URL.Query()
				if query.Get("name") != "猫 &+/?=" || query.Get("limit") != "10" || !reflect.DeepEqual(query["tag"], []string{"a", "b"}) {
					t.Errorf("query was not preserved: %#v", query)
				}
				if request.Body != nil && request.Body != http.NoBody {
					t.Error("GET unexpectedly contains a request body")
				}
				user, pass, ok := request.BasicAuth()
				if ok != tc.wantAuth || user != tc.wantUser || pass != tc.wantPass {
					t.Errorf("BasicAuth=(%q, %q, %t), want (%q, %q, %t)", user, pass, ok, tc.wantUser, tc.wantPass, tc.wantAuth)
				}
				if !tc.wantAuth && request.Header.Get("Authorization") != "" {
					t.Error("unexpected Authorization header")
				}
				if tc.transportError {
					return nil, errors.New("getapi-test-offline")
				}
				status := tc.status
				if status == 0 {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("get-response")), Request: request}, nil
			})
			vm := goja.New()
			setupGojaVMWithSnapshot(vm, nil, nil)
			value, err := vm.RunString(`(function () {
				var url = "https://getapi.test/data?name=" + encodeURIComponent("猫 &+/?=") + "&limit=10&tag=a&tag=b";
				try {
					return JSON.stringify({result: nyanGetAPI(` + tc.arguments + `)});
				} catch (e) {
					return JSON.stringify({typeError: e instanceof TypeError, message: String(e)});
				}
			})()`)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Result    *string `json:"result"`
				TypeError bool    `json:"typeError"`
				Message   string  `json:"message"`
			}
			if err := json.Unmarshal([]byte(value.String()), &result); err != nil {
				t.Fatal(err)
			}
			if tc.noArguments {
				if requests != 0 || result.Result != nil || !result.TypeError || result.Message != "TypeError: nyanGetAPI requires a URL" {
					t.Fatalf("result=%s requests=%d, want TypeError before sending", value, requests)
				}
				return
			}
			if requests != 1 {
				t.Fatalf("requests=%d, want 1", requests)
			}
			if tc.transportError && true {
				if result.Result != nil || result.TypeError || !strings.Contains(result.Message, "getapi-test-offline") {
					t.Fatalf("transport failure was not preserved: %s", value)
				}
				return
			}
			want := "get-response"
			if tc.transportError {
				want = ""
			}
			if result.Result == nil || *result.Result != want || result.Message != "" {
				t.Fatalf("result=%s, want response %q", value, want)
			}
		})
	}
}

func TestCheckStatusContract(t *testing.T) {

	for _, tc := range []struct {
		name, field string
		wantStatus  int
	}{
		{name: "missing"},
		{name: "null", field: `,"status":null`},
		{name: "string", field: `,"status":"200"`},
		{name: "boolean", field: `,"status":true`},
		{name: "array", field: `,"status":[]`},
		{name: "object", field: `,"status":{}`},
		{name: "zero", field: `,"status":0`},
		{name: "negative", field: `,"status":-1`},
		{name: "below_http", field: `,"status":99`},
		{name: "informational", field: `,"status":100`},
		{name: "below_minimum", field: `,"status":199`},
		{name: "fractional_below_minimum", field: `,"status":199.9`},
		{name: "fractional_success", field: `,"status":200.5`},
		{name: "fractional_upper_boundary", field: `,"status":599.9`},
		{name: "above_maximum", field: `,"status":600`},
		{name: "overflow", field: `,"status":1e100`},
		{name: "minimum", field: `,"status":200`, wantStatus: 200},
		{name: "created", field: `,"status":201`, wantStatus: 201},
		{name: "forbidden", field: `,"status":403`, wantStatus: 403},
		{name: "server_error", field: `,"status":500`, wantStatus: 500},
		{name: "maximum", field: `,"status":599`, wantStatus: 599},
		{name: "integral_decimal", field: `,"status":200.0`, wantStatus: 200},
		{name: "integral_exponent", field: `,"status":2e2`, wantStatus: 200},
		{name: "nan", field: `,"status":NaN`},
		{name: "infinity", field: `,"status":Infinity`},
		{name: "negative_infinity", field: `,"status":-Infinity`},
		{name: "undefined", field: `,"status":undefined`},
	} {
		for _, format := range []string{"object", "json_string"} {
			for _, success := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/success_%t", tc.name, format, success), func(t *testing.T) {
					body := fmt.Sprintf(`{"success":%t%s,"result":{"kept":[1,"value",null]}}`, success, tc.field)
					script := "(" + body + ");"
					wantStatus := tc.wantStatus
					if format == "json_string" {
						script = fmt.Sprintf("%q;", body)
						// Match NyanQL's integer JSON decoding, including literal notation.
						if tc.name == "integral_decimal" || tc.name == "integral_exponent" {
							wantStatus = 0
						}
					}
					vm := goja.New()
					value, err := vm.RunString(script)
					if err != nil {
						t.Fatal(err)
					}
					response, err := parseCheckResponse(value, "paramCheck")
					gotSuccess, gotStatus, gotResult := response.Success, response.Status, response.Result
					if wantStatus == 0 {
						if err == nil {
							t.Fatalf("invalid check status accepted: %s", body)
						}
						return
					}
					if err != nil || gotStatus != wantStatus || gotSuccess != success {
						t.Fatalf("success=%t status=%d err=%v, want success=%t status=%d", gotSuccess, gotStatus, err, success, wantStatus)
					}
					encoded, err := json.Marshal(gotResult)
					if err != nil || string(encoded) != `{"kept":[1,"value",null]}` {
						t.Fatalf("result changed: %s error=%v", encoded, err)
					}
				})
			}
		}
	}
}

func TestMCPBusinessResultAcrossTransports(t *testing.T) {
	for _, tc := range []struct {
		name, payload  string
		wantError      bool
		responseStatus int
	}{
		{name: "business_failure", payload: `{"success":false,"result":{"message":"在庫不足","remaining":0}}`, wantError: true},
		{name: "success", payload: `{"success":true,"result":"購入完了"}`},
		{name: "missing_success", payload: `{"result":"取得結果"}`},
		{name: "string_false", payload: `{"success":"false","result":"kept"}`},
		{name: "null_success", payload: `{"success":null,"result":"kept"}`},
		{name: "zero_success", payload: `{"success":0,"result":"kept"}`},
		{name: "empty_string_success", payload: `{"success":"","result":"kept"}`},
		{name: "nested_false", payload: `{"result":{"success":false}}`},
		{name: "array_success", payload: `{"success":[false],"result":"kept"}`},
		{name: "object_success", payload: `{"success":{"value":false},"result":"kept"}`},
		{name: "array_result", payload: `[{"success":false}]`},
		{name: "boolean_result", payload: `false`},
	} {
		for _, format := range []string{"object", "json_text"} {
			for _, transport := range []string{"http", "stdio"} {
				t.Run(tc.name+"/"+format+"/"+transport, func(t *testing.T) {
					script := "(" + tc.payload + ");"
					if format == "json_text" {
						script = fmt.Sprintf("%q;", tc.payload)
					}
					var response []byte

					key := t.Name()
					t.Cleanup(func() { storage.Delete(key) })
					dir, definitions := newMCPPhase12Definitions(t)
					definitions["sample"].(map[string]interface{})["push"] = "events"
					definitions["events"] = map[string]interface{}{"script": "./push.js"}
					writeHotReloadTestFile(t, filepath.Join(dir, "push.js"), fmt.Sprintf(`nyanSetItem(%q,"ran"); "notification";`, key))
					writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
					writeHotReloadTestFile(t, filepath.Join(dir, "sample.js"), script)
					writeHotReloadTestFile(t, filepath.Join(dir, "sample-output.js"), `const nyanOutputSchema={}; ({success:true,status:200});`)
					definitions["local-mcp"] = map[string]interface{}{"type": "mcp", "transport": "stdio", "tools": []interface{}{"sample"}}
					loaded, err := loadMCPPhase12Config(dir, definitions)
					if err != nil {
						t.Fatal(err)
					}
					router := publishMCPPhase12Snapshot(t, loaded)
					if transport == "http" {
						rec := mcpPhase2GapToolCall(router, `{}`, "Bearer phase2")
						if rec.Code != http.StatusOK {
							t.Fatalf("HTTP status=%d body=%s", rec.Code, rec.Body.String())
						}
						response = rec.Body.Bytes()
					} else {
						call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sample","arguments":{}}}`
						input := mcpPhase12InitializeBody(mcpProtocol20251125) + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n" + call + "\n"
						var output bytes.Buffer
						if err := serveMCPStdio(strings.NewReader(input), &output, loaded.Snapshot, loaded.Snapshot.MCPServers["local-mcp"]); err != nil {
							t.Fatal(err)
						}
						lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
						if len(lines) != 2 {
							t.Fatalf("stdio output=%s", output.String())
						}
						response = lines[1]
					}

					var envelope struct {
						Result map[string]interface{} `json:"result"`
						Error  json.RawMessage        `json:"error"`
					}
					if err := json.Unmarshal(response, &envelope); err != nil {
						t.Fatal(err)
					}
					if len(envelope.Error) > 0 || envelope.Result == nil {
						t.Fatalf("expected Tool result, got %s", response)
					}
					if (envelope.Result["isError"] == true) != tc.wantError {
						t.Fatalf("isError=%v want %t: %s", envelope.Result["isError"], tc.wantError, response)
					}
					gotPush, _ := storage.Load(key)
					if (gotPush != nil) == tc.wantError {
						t.Fatalf("Push=%v want execution=%t", gotPush, !tc.wantError)
					}
					var want interface{}
					if err := json.Unmarshal([]byte(tc.payload), &want); err != nil {
						t.Fatal(err)
					}
					content, ok := envelope.Result["content"].([]interface{})
					if !ok || len(content) != 1 {
						t.Fatalf("content=%v", envelope.Result["content"])
					}
					block, ok := content[0].(map[string]interface{})
					if !ok || block["type"] != "text" {
						t.Fatalf("content=%v", content)
					}
					text, ok := block["text"].(string)
					if !ok {
						t.Fatalf("text=%v", block["text"])
					}
					var got interface{}
					if err := json.Unmarshal([]byte(text), &got); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("text result changed: %s want %s", text, tc.payload)
					}
					structured, exists := envelope.Result["structuredContent"]
					_, wantObject := want.(map[string]interface{})
					if (exists && !reflect.DeepEqual(structured, want)) || (!exists && wantObject) {
						t.Fatalf("structured result changed: %v want %s", structured, tc.payload)
					}
				})
			}
		}
	}
}

// A passed output check preserves the API response; only success controls passage.
func TestCheckSuccessContract(t *testing.T) {
	for _, route := range []string{"http", "root", "jsonrpc"} {
		for _, tc := range []struct {
			name                   string
			param, main, out, want int
			checkOnly, push        bool
		}{
			{"param_created", 201, 201, -1, 201, false, true},
			{"param_forbidden", 403, 201, -1, 201, false, true},
			{"param_unavailable", 503, 201, -1, 201, false, true},
			{"out_keeps_created", 200, 201, 200, 201, false, true},
			{"out_created", 200, 200, 201, 200, false, true},
			{"out_forbidden", 200, 200, 403, 200, false, true},
			{"out_unavailable", 200, 200, 503, 200, false, true},
			{"out_preserves_status", 200, 201, -1, 201, false, true},
			{"out_no_content", 200, 201, 204, 201, false, true},
			{"out_reset_content", 200, 201, 205, 201, false, true},
			{"out_not_modified", 200, 201, 304, 201, false, true},
			{"out_keeps_redirect", 200, 302, 503, 302, false, true},
			{"out_keeps_failure", 200, 503, 200, 503, false, false},
			{"original_error_still_suppresses_push", 200, 403, 200, 403, false, false},
			{"no_out_check", 503, 200, 0, 200, false, true},
			{"check_only_created", 201, 200, 503, 201, true, false},
			{"check_only_unavailable", 503, 200, 201, 503, true, false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				marker := filepath.Join(dir, "push")
				mainMarker := filepath.Join(dir, "main")
				outMarker := filepath.Join(dir, "out")
				param := fmt.Sprintf(`({success:true,status:%d,result:"checked"});`, tc.param)
				out := ""
				if tc.out != 0 {
					expression := fmt.Sprint(tc.out)
					if tc.out == -1 {
						expression = "nyanAllParams.nyan_output.status"
					}
					out = fmt.Sprintf(`MARK_OUT if(nyanAllParams.nyan_output.status!==%d) throw new Error("wrong output metadata"); ({success:true,status:%s,result:"must not replace body"});`, tc.main, expression)
				}

				write := func(name, code string) string {
					p := filepath.Join(dir, name+".js")
					writeHotReloadTestFile(t, p, code)
					return p
				}
				out = strings.ReplaceAll(out, "MARK_OUT", fmt.Sprintf(`nyanSetItem(%q,"ran");`, outMarker))
				entry := map[string]interface{}{"paramCheck": write("param", param), "script": write("main", fmt.Sprintf(`nyanSetItem(%q,"ran"); JSON.stringify({status:%d,value:"日本語"});`, mainMarker, tc.main)), "push": "events"}
				if out != "" {
					entry["outCheck"] = write("out", out)
				}
				f := newWebSocketCheckFixture(t, map[string]interface{}{"target": entry, "events": map[string]interface{}{"script": write("push", fmt.Sprintf(`nyanSetItem(%q,"ran"); "pushed";`, marker))}})

				path := "/target"
				if route == "root" {
					path = "/?api=target"
				}
				if tc.checkOnly {
					if route == "root" {
						path += "&nyan_mode=checkOnly"
					} else {
						path += "?nyan_mode=checkOnly"
					}
				}
				req := httptest.NewRequest(http.MethodGet, path, nil)
				if route == "jsonrpc" {
					params := `{}`
					if tc.checkOnly {
						params = `{"nyan_mode":"checkOnly"}`
					}
					req = httptest.NewRequest(http.MethodPost, "/nyan-rpc", strings.NewReader(`{"jsonrpc":"2.0","id":42,"method":"target","params":`+params+`}`))
					req.Header.Set("Content-Type", "application/json")
				}
				rec := httptest.NewRecorder()
				f.server.Config.Handler.ServeHTTP(rec, req)
				wantStatus := tc.want
				if route == "jsonrpc" && !tc.checkOnly {
					wantStatus = http.StatusOK
				}
				if rec.Code != wantStatus {
					t.Fatalf("HTTP=%d want=%d body=%s", rec.Code, wantStatus, rec.Body.String())
				}
				var body map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if route == "jsonrpc" {
					if body["id"] != float64(42) || body["error"] != nil {
						t.Fatalf("wrong RPC envelope: %s", rec.Body.String())
					}
					body, _ = body["result"].(map[string]interface{})
				}
				if tc.checkOnly {
					if body["success"] != true || body["status"] != float64(tc.param) || body["result"] != "checked" {
						t.Fatalf("wrong checkOnly result: %v", body)
					}
				} else {
					if body["value"] != "日本語" || (route != "jsonrpc" && body["status"] != float64(tc.main)) {
						t.Fatalf("original body changed: %v", body)
					}
				}
				for _, m := range []struct {
					path string
					want bool
				}{{mainMarker, !tc.checkOnly}, {outMarker, !tc.checkOnly && tc.out != 0}, {marker, tc.push}} {
					data, exists := storage.Load(m.path)
					storage.Delete(m.path)
					if exists != m.want || (exists && data != "ran") {
						t.Fatalf("stage %s: data=%v exists=%t want=%t", m.path, data, exists, m.want)
					}
				}
			})
		}
	}
}

func TestPublicSuccessfulCheckStatus(t *testing.T) {
	for _, status := range []int{200, 201, 204, 205, 304, 403, 503} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(fmt.Sprintf("%s/%d", method, status), func(t *testing.T) {
				dir := t.TempDir()
				data := []byte{0, 1, 2, 255, 128, 10}
				if err := os.WriteFile(filepath.Join(dir, "data.bin"), data, 0600); err != nil {
					t.Fatal(err)
				}
				param := `({success:true,status:403});`
				out := fmt.Sprintf(`if(nyanAllParams.nyan_output.bodyBase64!==%q) throw new Error("binary changed"); ({success:true,status:%d,result:"must not replace body"});`, base64.StdEncoding.EncodeToString(data), status)
				write := func(name, code string) string {
					p := filepath.Join(dir, name+".js")
					writeHotReloadTestFile(t, p, code)
					return p
				}
				f := newWebSocketCheckFixture(t, map[string]interface{}{"assets": map[string]interface{}{"type": "public", "path": dir, "paramCheck": write("param", param), "outCheck": write("out", out)}})
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/assets/data.bin", nil)
				f.server.Config.Handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%q", rec.Code, rec.Body.Bytes())
				}
				if method == http.MethodGet && !bytes.Equal(rec.Body.Bytes(), data) {
					t.Fatalf("binary body changed: %q", rec.Body.Bytes())
				}
				if method == http.MethodHead && rec.Body.Len() != 0 {
					t.Fatalf("HEAD has body: %q", rec.Body.Bytes())
				}
				if rec.Header().Get("Content-Type") != "application/octet-stream" {
					t.Fatalf("content type changed: %v", rec.Header())
				}
			})
		}
	}
}

func TestOAuthSuccessfulCheckStatus(t *testing.T) {
	for _, paramStatus := range []int{201, 403, 503} {
		for _, outStatus := range []int{200, 201, 204, 205, 304, 503} {
			t.Run(fmt.Sprintf("param_%d/out_%d", paramStatus, outStatus), func(t *testing.T) {
				param := fmt.Sprintf(`({success:true,status:%d});`, paramStatus)
				out := fmt.Sprintf(`if(nyanAllParams.nyan_output.status!==201) throw new Error("wrong original status"); ({success:true,status:%d,result:"must not replace body"});`, outStatus)
				main := `({status:201,contentType:"text/plain",headers:{Location:"https://client.example/callback/done"},body:"original"});`
				_, router := newOAuthChecksFixture(t, "oauth_authorize", param, out, main)
				req := newMCPPhase12Request(http.MethodGet, "/oauth_authorize", "")
				req.RemoteAddr = t.Name()
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusCreated || rec.Body.String() != "original" || rec.Header().Get("Location") != "https://client.example/callback/done" {
					t.Fatalf("status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
				}
			})
		}
	}
}

// comparison06: invalid controls and absent checkers must never execute the API body.
func TestExecutionModeContract(t *testing.T) {
	for _, route := range []string{"http", "root", "jsonrpc", "public", "internal", "mcp"} {
		for _, hasCheck := range []bool{true, false} {
			for _, tc := range []struct {
				name, raw          string
				invalid, checkOnly bool
			}{
				{"omitted", "", false, false}, {"empty", `""`, false, false}, {"check_only", `"checkOnly"`, false, true},
				{"typo", `"checkOnyl"`, true, false}, {"case", `"CHECKONLY"`, true, false}, {"space", `" checkOnly "`, true, false},
				{"unknown", `"normal"`, true, false}, {"null", `null`, true, false}, {"number", `1`, true, false},
				{"boolean", `true`, true, false}, {"array", `["checkOnly"]`, true, false}, {"object", `{}`, true, false},
			} {
				t.Run(fmt.Sprintf("%s/check_%t/%s", route, hasCheck, tc.name), func(t *testing.T) {
					dir := t.TempDir()
					marker := filepath.Join(dir, "stages")

					t.Cleanup(func() { storage.Delete(marker) })
					write := func(name, body string) string {
						path := filepath.Join(dir, name+".js")
						writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, marker, marker, name+",")+body)
						return path
					}
					entry := map[string]interface{}{"script": write("main", `JSON.stringify({status:200,value:"original"});`), "outCheck": write("out", `({success:true,status:200});`), "push": "events"}
					if hasCheck {
						entry["paramCheck"] = write("param", `({success:true,status:201,result:"checked"});`)
					}
					if route == "public" {
						entry["type"] = "public"
						entry["path"] = dir
						writeHotReloadTestFile(t, filepath.Join(dir, "data.txt"), "original")
					}
					f := newWebSocketCheckFixture(t, map[string]interface{}{"target": entry, "events": map[string]interface{}{"script": write("push", `"notification";`)}})

					params := map[string]interface{}{}
					if tc.raw != "" {
						var v interface{}
						if err := json.Unmarshal([]byte(tc.raw), &v); err != nil {
							t.Fatal(err)
						}
						params["nyan_mode"] = v
					}
					encoded, _ := json.Marshal(params)
					blocked := tc.invalid || (tc.checkOnly && !hasCheck)
					wantStatus := http.StatusOK
					if tc.checkOnly {
						wantStatus = 201
					}
					if tc.invalid {
						wantStatus = 400
					} else if tc.checkOnly && !hasCheck {
						wantStatus = 404
					}
					switch route {
					case "internal":
						_, err := callNyanAPIFromVMWithSnapshot(currentAPISnapshot(), "target", params, nil)
						if (err != nil) != blocked {
							t.Fatalf("internal error=%v blocked=%t", err, blocked)
						}
					case "mcp":
						result, failure := executeMCPTool(currentAPISnapshot(), &MCPToolConfig{Name: "target", API: "target", InputSchema: map[string]interface{}{"type": "object", "additionalProperties": false}}, params, nil)
						failed := failure != "" || result["isError"] == true
						if failed != blocked {
							t.Fatalf("MCP result=%v failure=%q blocked=%t", result, failure, blocked)
						}
					default:
						path := "/target"
						if route == "root" {
							path = "/?api=target"
						}
						if route == "public" {
							path = "/target/data.txt"
						}
						req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
						req.Header.Set("Content-Type", "application/json")
						if route == "public" {
							values := url.Values{}
							if tc.raw != "" {
								v, ok := params["nyan_mode"].(string)
								if !ok {
									v = tc.raw
								}
								values.Set("nyan_mode", v)
							}
							req = httptest.NewRequest(http.MethodGet, path+"?"+values.Encode(), nil)
						}
						if route == "jsonrpc" {
							req = httptest.NewRequest(http.MethodPost, "/nyan-rpc", strings.NewReader(`{"jsonrpc":"2.0","id":42,"method":"target","params":`+string(encoded)+`}`))
							req.Header.Set("Content-Type", "application/json")
							if blocked {
								wantStatus = 400
							}
						}
						rec := httptest.NewRecorder()
						f.server.Config.Handler.ServeHTTP(rec, req)
						if rec.Code != wantStatus {
							t.Fatalf("HTTP %d want %d: %s", rec.Code, wantStatus, rec.Body.String())
						}
						if blocked && strings.Contains(rec.Body.String(), "original") {
							t.Fatalf("body leaked: %s", rec.Body.String())
						}
						if tc.checkOnly && !blocked && !strings.Contains(rec.Body.String(), "checked") {
							t.Fatalf("checker result lost: %s", rec.Body.String())
						}
					}
					want := ""
					if !blocked {
						if hasCheck {
							want = "param,"
						}
						if !tc.checkOnly {
							if route != "public" {
								want += "main,"
							}
							want += "out,"
							if route != "public" {
								want += "push,"
							}
						}
					}
					value, _ := storage.Load(marker)
					got, _ := value.(string)
					if got != want {
						t.Fatalf("execution order=%q want=%q", got, want)
					}
				})
			}
		}
	}
}

func TestExecutionModeSchema(t *testing.T) {
	original := map[string]interface{}{"type": "object", "properties": map[string]interface{}{"value": map[string]interface{}{"type": "string"}}, "required": []interface{}{"value"}, "additionalProperties": false}
	normalized := normalizedMCPInputSchema(original)
	if _, exists := original["properties"].(map[string]interface{})["nyan_mode"]; exists {
		t.Fatal("normalization mutated original schema")
	}
	for _, tc := range []struct {
		name    string
		args    map[string]interface{}
		invalid bool
	}{
		{"check_only", map[string]interface{}{"value": "ok", "nyan_mode": "checkOnly"}, false},
		{"normal", map[string]interface{}{"value": "ok", "nyan_mode": ""}, false},
		{"missing_required", map[string]interface{}{"nyan_mode": "checkOnly"}, true},
		{"unknown_property", map[string]interface{}{"value": "ok", "extra": true}, true},
		{"bad_mode", map[string]interface{}{"value": "ok", "nyan_mode": "CHECKONLY"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMCPJSONSchemaValue(normalized, tc.args)
			if (err != nil) != tc.invalid {
				t.Fatalf("schema error=%v want invalid=%t", err, tc.invalid)
			}
		})
	}
}

func TestExecutionModeRequestPrecedence(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		for _, tc := range []struct {
			name, query, contentType, body, want string
			invalid                              bool
		}{
			{"query_only", "nyan_mode=checkOnly", "", "", "checkOnly", false},
			{"duplicate_query", "nyan_mode=checkOnly&nyan_mode=checkOnly", "", "", "", true},
			{"duplicate_form", "", "application/x-www-form-urlencoded", "nyan_mode=checkOnly&nyan_mode=checkOnly", "", true},
			{"form_over_query", "nyan_mode=invalid", "application/x-www-form-urlencoded", "nyan_mode=checkOnly", "checkOnly", false},
			{"json_over_query", "nyan_mode=invalid", "application/json", `{"nyan_mode":"checkOnly"}`, "checkOnly", false},
			{"empty_form_over_query", "nyan_mode=checkOnly", "application/x-www-form-urlencoded", "nyan_mode=", "", false},
			{"empty_json_over_query", "nyan_mode=checkOnly", "application/json", `{"nyan_mode":""}`, "", false},
			{"null_json_over_query", "nyan_mode=checkOnly", "application/json", `{"nyan_mode":null}`, "", true},
			{"query_with_json", "nyan_mode=checkOnly", "application/json", `{}`, "checkOnly", false},
		} {
			t.Run(fmt.Sprintf("oauth_%t/%s", oauth, tc.name), func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "https://example.test/target?"+tc.query, strings.NewReader(tc.body))
				c.Request.Header.Set("Content-Type", tc.contentType)
				var params map[string]interface{}
				var err error
				if oauth {
					params, err = oauthRequestParams(c.Request)
				} else {
					params, err = collectRequestParams(c)
				}
				mode := ""
				if err == nil {
					mode, err = parseExecutionMode(params)
				}
				if (err != nil) != tc.invalid || (!tc.invalid && mode != tc.want) {
					t.Fatalf("mode=%q err=%v want=%q invalid=%t params=%v", mode, err, tc.want, tc.invalid, params)
				}
			})
		}
	}
}

func TestWebSocketExecutionModeContract(t *testing.T) {
	for _, handshake := range []bool{false, true} {
		for _, hasCheck := range []bool{true, false} {
			for _, tc := range []struct {
				mode    string
				invalid bool
			}{{"", false}, {"checkOnly", false}, {"CHECKONLY", true}, {" checkOnly ", true}, {"checkOnyl", true}} {
				t.Run(fmt.Sprintf("handshake_%t/check_%t/%q", handshake, hasCheck, tc.mode), func(t *testing.T) {
					dir := t.TempDir()
					marker := filepath.Join(dir, "stages")

					t.Cleanup(func() { storage.Delete(marker) })
					write := func(name, body string) string {
						path := filepath.Join(dir, name+".js")
						writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, marker, marker, name+",")+body)
						return path
					}
					entry := map[string]interface{}{"script": write("main", `JSON.stringify({status:200,value:"original"});`), "outCheck": write("out", `({success:true,status:200});`)}
					if hasCheck {
						entry["paramCheck"] = write("param", `({success:true,status:201,result:"checked"});`)
					}
					f := newWebSocketCheckFixture(t, map[string]interface{}{"target": entry, "channel": map[string]interface{}{"script": filepath.Join(dir, "unused.js")}})
					wsURL := "ws" + strings.TrimPrefix(f.server.URL, "http")

					blocked := tc.invalid || (tc.mode == "checkOnly" && !hasCheck)
					wantStatus := 200
					if tc.mode == "checkOnly" {
						wantStatus = 201
					}
					if tc.invalid {
						wantStatus = 400
					} else if blocked {
						wantStatus = 404
					}
					var reply []byte
					if handshake {
						conn, resp, err := websocket.DefaultDialer.Dial(wsURL+"/target?nyan_mode="+url.QueryEscape(tc.mode), nil)
						if !blocked && tc.mode == "" {
							if err != nil {
								t.Fatal(err)
							}
							conn.Close()
						} else {
							if conn != nil {
								conn.Close()
								t.Fatal("checkOnly or invalid request upgraded")
							}
							if err == nil || resp == nil {
								t.Fatalf("missing HTTP error: %v", err)
							}
							defer resp.Body.Close()
							reply, _ = io.ReadAll(resp.Body)
							if resp.StatusCode != wantStatus {
								t.Fatalf("HTTP=%d want=%d body=%s", resp.StatusCode, wantStatus, reply)
							}
						}
					} else {
						conn, _, err := websocket.DefaultDialer.Dial(wsURL+"/channel", nil)
						if err != nil {
							t.Fatal(err)
						}
						defer conn.Close()
						if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
							t.Fatal(err)
						}
						data, _ := json.Marshal(map[string]interface{}{"api": "target", "nyan_mode": tc.mode})
						if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
							t.Fatal(err)
						}
						_, reply, err = conn.ReadMessage()
						if err != nil {
							t.Fatal(err)
						}
						if blocked || tc.mode == "checkOnly" {
							var check ParamCheckResponse
							if err := json.Unmarshal(reply, &check); err != nil {
								t.Fatal(err)
							}
							if check.Status != wantStatus || check.Success == blocked {
								t.Fatalf("unexpected response: %s", reply)
							}
						} else if !strings.Contains(string(reply), "original") {
							t.Fatalf("body=%s", reply)
						}
					}
					if tc.mode == "checkOnly" && !blocked && !strings.Contains(string(reply), "checked") {
						t.Fatalf("check result lost: %s", reply)
					}
					want := ""
					if !blocked {
						if hasCheck {
							want = "param,"
						}
						if !handshake && tc.mode == "" {
							want += "main,out,"
						}
					}
					value, _ := storage.Load(marker)
					got, _ := value.(string)
					if got != want {
						t.Fatalf("stages=%q want=%q", got, want)
					}
				})
			}
		}
	}
}

func TestOAuthInvalidExecutionMode(t *testing.T) {
	for _, raw := range []string{`"CHECKONLY"`, `" checkOnly "`, `"checkOnyl"`, `null`, `1`, `true`, `["checkOnly"]`, `{}`} {
		t.Run(raw, func(t *testing.T) {
			loaded, router := newOAuthChecksFixture(t, "oauth_register", `({success:true,status:200});`, `({success:true,status:200});`, `({status:200,body:"original"});`)
			req := httptest.NewRequest(http.MethodPost, "https://nyan8.test/oauth_register", strings.NewReader(`{"nyan_mode":`+raw+`}`))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = t.Name()
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			assertOAuthCheckOrder(t, loaded, "")
		})
	}
}

func TestMCPPushCompletion(t *testing.T) {
	for _, transport := range []string{"http", "stdio"} {
		for _, tc := range []struct {
			name, body, out, requestID string
			noPush, failure            bool
		}{
			{name: "normal", body: `{"status":200,"success":true,"value":"original"}`},
			{name: "no_flags", body: `{"value":"original"}`},
			{name: "false", body: `{"status":200,"success":false}`, noPush: true},
			{name: "false_no_status", body: `{"success":false}`, noPush: true},
			{name: "string_false", body: `{"success":"false"}`},
			{name: "nested_false", body: `{"value":{"success":false}}`},
			{name: "status_201", body: `{"status":201}`},
			{name: "status_399", body: `{"status":399}`},
			{name: "status_199", body: `{"status":199}`, noPush: true},
			{name: "status_400", body: `{"status":400}`, noPush: true},
			{name: "status_503", body: `{"status":503,"success":true}`, noPush: true},
			{name: "status_fractional", body: `{"status":200.5}`, noPush: true},
			{name: "status_string", body: `{"status":"503"}`},
			{name: "array", body: `[{"success":false}]`},
			{name: "out_201", body: `{"value":"original"}`, out: `({success:true,status:201});`},
			{name: "out_503", body: `{"value":"original"}`, out: `({success:true,status:503});`},
			{name: "source_failure_out_200", body: `{"status":503}`, out: `({success:true,status:200});`, noPush: true},
			{name: "invalid_json", body: `not JSON`, noPush: true, failure: true},
			{name: "body_too_large", body: `"` + strings.Repeat("x", maxMCPToolResultBytes) + `"`, noPush: true, failure: true},
			{name: "escaped_response_too_large", body: `{"value":"` + strings.Repeat("<", 400000) + `"}`, noPush: true, failure: true},
			// A near-limit response fits with the short ID, but not with the long one.
			{name: "near_limit", body: `{"value":"` + strings.Repeat("x", 2097000) + `"}`},
			{name: "request_id_size", body: `{"value":"` + strings.Repeat("x", 2097000) + `"}`, requestID: `"` + strings.Repeat("i", 1024) + `"`, noPush: true, failure: true},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				id := tc.requestID
				if id == "" {
					id = "2"
				}
				call := `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"sample","arguments":{}}}`
				var response []byte

				dir, definitions := newMCPPhase12Definitions(t)
				key := t.Name()
				t.Cleanup(func() { storage.Delete(key) })
				writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
				writeHotReloadTestFile(t, filepath.Join(dir, "sample.js"), fmt.Sprintf("%q;", tc.body))
				entry := definitions["sample"].(map[string]interface{})
				delete(entry, "outCheck")
				if tc.out != "" {
					path := filepath.Join(dir, "output.js")
					writeHotReloadTestFile(t, path, tc.out)
					entry["outCheck"] = path
				}
				entry["push"] = "events"
				push := filepath.Join(dir, "push.js")
				writeHotReloadTestFile(t, push, fmt.Sprintf(`if(nyanAllParams.api!=="events")throw new Error("wrong Push API");nyanSetItem(%q,(nyanGetItem(%q) ?? "")+"push,"); "notification";`, key, key))
				definitions["events"] = map[string]interface{}{"script": push}
				definitions["local-mcp"] = map[string]interface{}{"type": "mcp", "transport": "stdio", "tools": []interface{}{"sample"}}
				loaded, err := loadMCPPhase12Config(dir, definitions)
				if err != nil {
					t.Fatal(err)
				}
				for _, server := range loaded.Snapshot.MCPServers {
					findMCPTool(server, "sample").OutputSchema = nil
				}
				router := publishMCPPhase12Snapshot(t, loaded)
				if transport == "http" {
					req := httptest.NewRequest(http.MethodPost, "http://example.test/custom-mcp", strings.NewReader(call))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Accept", "application/json, text/event-stream")
					req.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
					req.Header.Set("Authorization", "Bearer phase2")
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					if rec.Code != 200 {
						t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
					}
					response = rec.Body.Bytes()
				} else {
					input := mcpPhase12InitializeBody(mcpProtocol20251125) + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + call + "\n"
					var output bytes.Buffer
					if err := serveMCPStdio(strings.NewReader(input), &output, loaded.Snapshot, loaded.Snapshot.MCPServers["local-mcp"]); err != nil {
						t.Fatal(err)
					}
					lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
					if len(lines) != 2 {
						t.Fatalf("stdio replies=%d", len(lines))
					}
					response = lines[1]
				}

				var envelope struct {
					ID     json.RawMessage `json:"id"`
					Error  json.RawMessage `json:"error"`
					Result struct {
						IsError bool `json:"isError"`
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
						Structured interface{} `json:"structuredContent"`
					} `json:"result"`
				}
				if err := json.Unmarshal(response, &envelope); err != nil {
					t.Fatal(err)
				}
				if string(envelope.ID) != id || len(envelope.Error) > 0 || len(envelope.Result.Content) != 1 {
					t.Fatalf("invalid envelope (bytes=%d)", len(response))
				}
				if tc.failure {
					if !envelope.Result.IsError {
						t.Fatal("invalid result accepted")
					}
				} else {
					var want interface{}
					if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(envelope.Result.Structured, want) {
						t.Fatal("structured result changed")
					}
					var textResult interface{}
					if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &textResult); err != nil || !reflect.DeepEqual(textResult, want) {
						t.Fatal("text result changed")
					}
				}
				got, _ := storage.Load(key)
				if tc.noPush {
					if got != nil {
						t.Fatalf("unexpected Push: %v", got)
					}
				} else if got != "push," {
					t.Fatalf("Push count/order=%v", got)
				}
			})
		}
	}
}

func TestMCPPushReachesSubscribers(t *testing.T) {
	for _, pushScript := range []string{`JSON.stringify({success:false,status:503});`, `({toString(){throw new Error("push conversion failed");}});`} {
		t.Run(pushScript, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.js")
			push := filepath.Join(dir, "push.js")
			recovery := filepath.Join(dir, "recovery.js")
			writeHotReloadTestFile(t, source, `JSON.stringify({value:"original"});`)
			writeHotReloadTestFile(t, push, pushScript)
			writeHotReloadTestFile(t, recovery, `"barrier";`)
			f := newWebSocketCheckFixture(t, map[string]interface{}{"source": map[string]interface{}{"script": source, "push": "events"}, "events": map[string]interface{}{"script": push}, "recovery": map[string]interface{}{"script": recovery}})
			clients := []*websocket.Conn{f.dial("/events", nil), f.dial("/events", nil)}
			for _, c := range clients {
				if got := exchangeWebSocketCheckFrame(t, c, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
					t.Fatal(got)
				}
			}
			result, failure := executeMCPTool(currentAPISnapshot(), &MCPToolConfig{Name: "source", API: "source"}, nil, nil)
			if failure != "" || result["isError"] != false {
				t.Fatalf("Push replaced source result: %v %s", result, failure)
			}
			for _, c := range clients {
				if !strings.Contains(pushScript, "throw") {
					_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
					kind, body, err := c.ReadMessage()
					if err != nil || kind != websocket.TextMessage || string(body) != `{"success":false,"status":503}` {
						t.Fatalf("notification=%s type=%d error=%v", body, kind, err)
					}
				}
				if got := exchangeWebSocketCheckFrame(t, c, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
					t.Fatalf("unexpected extra Push: %s", got)
				}
			}
		})
	}
}

func TestHTTPInputMergeAndJSONDocument(t *testing.T) {
	for _, route := range []string{"http", "root"} {
		for _, tc := range []struct {
			name, contentType, body, want, query string
			invalid, readError                   bool
		}{
			{name: "query_only", want: `{"x":"query","z":"kept"}`},
			{name: "repeated_query", query: "x=2&x=1&x=2&z=kept", want: `{"x":["2","1","2"],"z":"kept"}`},
			{name: "comma_query", query: "x=a%2Cb&z=kept", want: `{"x":"a,b","z":"kept"}`},
			{name: "json_query", query: "x=%7B%22a%22%3A1%7D&z=kept", want: `{"x":"{\"a\":1}","z":"kept"}`},
			{name: "empty_json", contentType: "application/json", want: `{"x":"query","z":"kept"}`},
			{name: "whitespace_json", contentType: "application/json", body: " \r\n\t", want: `{"x":"query","z":"kept"}`},
			{name: "empty_object", contentType: "application/json", body: `{}`, want: `{"x":"query","z":"kept"}`},
			{name: "null_document", contentType: "application/json", body: `null`, want: `{"x":"query","z":"kept"}`},
			{name: "json_over_query", contentType: "application/json", body: `{"x":2}`, want: `{"x":2,"z":"kept"}`},
			{name: "json_null_over_query", contentType: "application/json", body: `{"x":null}`, want: `{"x":null,"z":"kept"}`},
			{name: "json_empty_over_query", contentType: "application/json", body: `{"x":""}`, want: `{"x":"","z":"kept"}`},
			{name: "json_array_over_query", contentType: "application/json; charset=utf-8", body: `{"x":[1,null,true]}`, want: `{"x":[1,null,true],"z":"kept"}`},
			{name: "json_trailing_whitespace", contentType: "application/json", body: "{\"x\":2} \n\t", want: `{"x":2,"z":"kept"}`},
			{name: "form_over_query", contentType: "application/x-www-form-urlencoded", body: "x=body", want: `{"x":"body","z":"kept"}`},
			{name: "empty_form_value", contentType: "application/x-www-form-urlencoded", body: "x=", want: `{"x":"","z":"kept"}`},
			{name: "form_array_over_query", contentType: "application/x-www-form-urlencoded", body: "x=1&x=2", want: `{"x":["1","2"],"z":"kept"}`},
			{name: "two_objects", contentType: "application/json", body: `{"x":1}{"x":2}`, invalid: true},
			{name: "trailing_null", contentType: "application/json", body: `{"x":1} null`, invalid: true},
			{name: "trailing_text", contentType: "application/json", body: `{"x":1} extra`, invalid: true},
			{name: "null_then_object", contentType: "application/json", body: `null {}`, invalid: true},
			{name: "incomplete_object", contentType: "application/json", body: `{"x":`, invalid: true},
			{name: "array_document", contentType: "application/json", body: `[1,2]`, invalid: true},
			{name: "string_document", contentType: "application/json", body: `"text"`, invalid: true},
			{name: "number_document", contentType: "application/json", body: `123`, invalid: true},
			{name: "read_error_after_valid_json", contentType: "application/json", body: `{"x":1}`, readError: true, invalid: true},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {

				marker := t.Name()
				t.Cleanup(func() { storage.Delete(marker) })
				dir := t.TempDir()
				write := func(stage, code string) string {
					path := filepath.Join(dir, stage+".js")
					writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,(nyanGetItem(%q) ?? "")+%q);`, marker, marker, stage+",")+code)
					return path
				}
				fixture := newWebSocketCheckFixture(t, map[string]interface{}{
					"target": map[string]interface{}{"paramCheck": write("param", `({success:true,status:200});`), "script": write("main", `JSON.stringify({status:200,seen:{x:nyanAllParams.x,z:nyanAllParams.z}});`), "outCheck": write("out", `({success:true,status:200});`), "push": "events"},
					"events": map[string]interface{}{"script": write("push", `"notification";`)},
				})

				query := tc.query
				if query == "" {
					query = "x=query&z=kept"
				}
				path := "/target?" + query
				if route == "root" {
					path = "/?api=target&" + query
				}
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(tc.body))
				if tc.contentType != "" {
					req.Header.Set("Content-Type", tc.contentType)
				}
				if tc.readError {
					reader, writer := io.Pipe()
					go func() { _, _ = io.WriteString(writer, tc.body); _ = writer.CloseWithError(errors.New("read failed")) }()
					req.Body = reader
					defer reader.Close()
				}
				rec := httptest.NewRecorder()
				fixture.server.Config.Handler.ServeHTTP(rec, req)
				status := 200
				wantStages := "param,main,out,push,"
				if tc.invalid {
					status = 400
					wantStages = ""
				}
				if rec.Code != status {
					t.Fatalf("HTTP %d want %d: %s", rec.Code, status, rec.Body.String())
				}
				if !tc.invalid {
					var result struct {
						Seen interface{} `json:"seen"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					var want interface{}
					if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(result.Seen, want) {
						t.Fatalf("seen=%#v want=%#v", result.Seen, want)
					}
				}
				gotStages := ""
				if value, ok := storage.Load(marker); ok {
					gotStages = value.(string)
				}
				if gotStages != wantStages {
					t.Fatalf("execution order=%q want=%q", gotStages, wantStages)
				}
			})
		}
	}
}

func newCommonFileWriterTestVM(rootPath string) *goja.Runtime {
	vm := goja.New()
	setupGojaVMWithSnapshot(vm, &APIConfigSnapshot{RootPath: rootPath}, nil)
	return vm
}

func TestCommonFileWriters(t *testing.T) {
	for _, function := range []string{"nyanWriteTextFile", "nyanWriteBase64File"} {
		t.Run(function, func(t *testing.T) {
			for _, tc := range []struct{ name, text, want string }{
				{"unicode", "猫\n🌸\x00", "猫\n🌸\x00"},
				{"empty", "", ""},
				{"literal base64", "aGVsbG8=", "aGVsbG8="},
				{"json text", `{"ok":true}`, `{"ok":true}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					dir := t.TempDir()
					vm := newCommonFileWriterTestVM(filepath.Join(dir, "api.json"))
					data := tc.text
					if function == "nyanWriteBase64File" {
						data = base64.StdEncoding.EncodeToString([]byte(data))
					}
					value, err := vm.RunString(fmt.Sprintf(`%s("nested/result.txt", %q)`, function, data))
					if err != nil || value.Export() != true {
						t.Fatalf("result=%v err=%v", value, err)
					}
					path := filepath.Join(dir, "nested", "result.txt")
					got, err := os.ReadFile(path)
					if err != nil || string(got) != tc.want {
						t.Fatalf("content=%q err=%v", got, err)
					}
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if info.Mode().Perm() != 0600 {
						t.Fatalf("mode=%o", info.Mode().Perm())
					}
					value, err = vm.RunString(fmt.Sprintf(`%s(%q, "")`, function, path))
					if err != nil || value.Export() != true {
						t.Fatalf("overwrite=%v err=%v", value, err)
					}
					got, err = os.ReadFile(path)
					if err != nil || len(got) != 0 {
						t.Fatalf("overwrite content=%q err=%v", got, err)
					}
					files, err := os.ReadDir(filepath.Dir(path))
					if err != nil || len(files) != 1 {
						t.Fatalf("temporary files remain: %v %v", files, err)
					}
				})
			}
			for _, tc := range []struct{ name, args string }{
				{"no args", ``}, {"missing data", `"file"`}, {"undefined path", `undefined,""`}, {"null path", `null,""`},
				{"number path", `1,""`}, {"object path", `{},""`}, {"array path", `[],""`}, {"empty path", `"",""`}, {"blank path", `" \t\n",""`},
				{"undefined data", `"file",undefined`}, {"null data", `"file",null`}, {"number data", `"file",1`}, {"boolean data", `"file",true`},
				{"object data", `"file",{}`}, {"array data", `"file",[]`}, {"boxed data", `"file",new String("")`},
				{"path coercion", `{toString(){throw new Error("coerced")}},""`}, {"data coercion", `"file",{toString(){throw new Error("coerced")}}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					dir := t.TempDir()
					vm := newCommonFileWriterTestVM(filepath.Join(dir, "api.json"))
					got, err := vm.RunString(fmt.Sprintf(`(()=>{try{%s(%s);return "accepted"}catch(e){return e.name}})()`, function, tc.args))
					if err != nil || got.String() != "TypeError" {
						t.Fatalf("result=%v err=%v", got, err)
					}
					files, err := os.ReadDir(dir)
					if err != nil || len(files) != 0 {
						t.Fatalf("invalid call wrote files: %v %v", files, err)
					}
				})
			}
			t.Run("io failures preserve files", func(t *testing.T) {
				dir := t.TempDir()
				vm := newCommonFileWriterTestVM(filepath.Join(dir, "api.json"))
				existing := filepath.Join(dir, "existing")
				if err := os.WriteFile(existing, []byte("original"), 0644); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{"existing/child", "."} {
					value, err := vm.RunString(fmt.Sprintf(`(()=>{try{%s(%q, "");return false}catch(e){return e instanceof Error}})()`, function, path))
					if err != nil || !value.ToBoolean() {
						t.Fatalf("path=%q result=%v err=%v", path, value, err)
					}
				}
				got, err := os.ReadFile(existing)
				if err != nil || string(got) != "original" {
					t.Fatalf("original=%q err=%v", got, err)
				}
				files, err := os.ReadDir(dir)
				if err != nil || len(files) != 1 {
					t.Fatalf("temporary files remain: %v %v", files, err)
				}
			})
			t.Run("missing root rejects relative accepts absolute", func(t *testing.T) {
				for _, root := range []string{"", "relative/api.json"} {
					vm := newCommonFileWriterTestVM(root)
					value, err := vm.RunString(fmt.Sprintf(`(()=>{try{%s("file", "");return false}catch(e){return e instanceof Error}})()`, function))
					if err != nil || !value.ToBoolean() {
						t.Fatalf("result=%v err=%v", value, err)
					}
					path := filepath.Join(t.TempDir(), "file")
					value, err = vm.RunString(fmt.Sprintf(`%s(%q, "")`, function, path))
					if err != nil || value.Export() != true {
						t.Fatalf("absolute=%v err=%v", value, err)
					}
					if _, err := os.Stat(path); err != nil {
						t.Fatal(err)
					}
				}
			})
			t.Run("captured root ignores cwd and reload", func(t *testing.T) {
				dir, other := t.TempDir(), t.TempDir()
				t.Chdir(other)
				snapshot := &APIConfigSnapshot{RootPath: filepath.Join(dir, "api.json")}
				vm := newCommonFileWriterTestVM(snapshot.RootPath)
				previous := currentAPISnapshot()
				t.Cleanup(func() { publishAPISnapshot(previous) })
				publishAPISnapshot(&APIConfigSnapshot{RootPath: filepath.Join(other, "api.json")})
				got, err := vm.RunString(fmt.Sprintf(`%s("result", "")`, function))
				if err != nil || got.Export() != true {
					t.Fatalf("result=%v err=%v", got, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "result")); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(other, "result")); !os.IsNotExist(err) {
					t.Fatalf("wrong base: %v", err)
				}
			})
		})
	}
	t.Run("binary and line breaks", func(t *testing.T) {
		dir := t.TempDir()
		vm := newCommonFileWriterTestVM(filepath.Join(dir, "api.json"))
		got, err := vm.RunString(`nyanWriteBase64File("binary", "AA\r\nH/")`)
		if err != nil || got.Export() != true {
			t.Fatalf("result=%v err=%v", got, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "binary"))
		if err != nil || !bytes.Equal(data, []byte{0, 1, 255}) {
			t.Fatalf("binary=%v err=%v", data, err)
		}
	})
	for _, tc := range []struct{ name, data string }{
		{"invalid character", "%%%"}, {"partial valid input", "aGVsbG8=%%%"}, {"missing padding", "YQ"},
		{"URL safe", "AAH_"}, {"data URL", "data:text/plain;base64,YQ=="}, {"spaces", "Y Q=="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "existing")
			if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
				t.Fatal(err)
			}
			vm := newCommonFileWriterTestVM(filepath.Join(dir, "api.json"))
			for _, dest := range []string{"existing", "new/invalid"} {
				got, err := vm.RunString(fmt.Sprintf(`(()=>{try{nyanWriteBase64File(%q,%q);return "accepted"}catch(e){return e.name}})()`, dest, tc.data))
				if err != nil || got.String() != "TypeError" {
					t.Fatalf("result=%v err=%v", got, err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "original" {
				t.Fatalf("existing=%q err=%v", data, err)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 1 {
				t.Fatalf("invalid Base64 created files: %v %v", files, err)
			}
		})
	}
}

func TestCommonFileWritersPreserveLegacy(t *testing.T) {
	vm := newCommonFileWriterTestVM("")
	got, err := vm.RunString(`typeof nyanSaveFile`)
	if err != nil || got.String() != "undefined" {
		t.Fatalf("legacy=%v err=%v", got, err)
	}
}

func TestCommonFileWritersThroughIncludedAPIs(t *testing.T) {
	f := newFilePathFixture(t)
	script := filepath.Join(f.scriptDir, "write.js")
	writeHotReloadTestFile(t, script, `if(nyanWriteTextFile("output/"+nyanAllParams.api+".txt","猫")!==true)throw new Error("text write failed");
 if(nyanWriteBase64File("output/"+nyanAllParams.api+".bin","AAH/")!==true)throw new Error("binary write failed");
 JSON.stringify({ok:true})`)
	for _, api := range []string{"root", "child/read", "child/nested/read"} {
		t.Run(api, func(t *testing.T) {
			result, err := runJavaScriptWithSnapshot(f.snapshot, script, map[string]interface{}{"api": api}, nil)
			if err != nil || result != `{"ok":true}` {
				t.Fatalf("result=%q err=%v", result, err)
			}
			for suffix, want := range map[string]string{".txt": "猫", ".bin": "\x00\x01\xff"} {
				data, err := os.ReadFile(filepath.Join(filepath.Dir(f.snapshot.RootPath), "output", api+suffix))
				if err != nil || string(data) != want {
					t.Fatalf("%s=%q err=%v", suffix, data, err)
				}
			}
		})
	}
	for _, wrongDir := range []string{f.childDir, f.scriptDir, f.cwd} {
		if _, err := os.Stat(filepath.Join(wrongDir, "output")); !os.IsNotExist(err) {
			t.Fatalf("wrong write base %s: %v", wrongDir, err)
		}
	}
}

func TestHostExecCommonContract(t *testing.T) {
	t.Run("missing argument TypeError", func(t *testing.T) {
		vm := newCommonFileWriterTestVM("")
		value, err := vm.RunString(`(()=>{try {nyanHostExec(); return false;} catch(e){return e instanceof TypeError && e.message === "nyanHostExec: command required";}})()`)
		if err != nil || !value.ToBoolean() {
			t.Fatalf("missing argument=%v err=%v", value, err)
		}
	})
	for _, exitCode := range []int{0, 7} {
		t.Run(fmt.Sprintf("object exit %d", exitCode), func(t *testing.T) {
			command := fmt.Sprintf("echo hello; echo problem >&2; exit %d", exitCode)
			if runtime.GOOS == "windows" {
				command = fmt.Sprintf("echo hello& echo problem 1>&2& exit /b %d", exitCode)
			}
			vm := newCommonFileWriterTestVM("")
			value, err := vm.RunString(fmt.Sprintf(`(()=>{const r=nyanHostExec(%q);return typeof r === "object" && r.success === %t && r.exit_code === %d && r.stdout.trim() === "hello" && r.stderr.trim() === "problem" && Object.keys(r).sort().join(",") === "exit_code,stderr,stdout,success";})()`, command, exitCode == 0, exitCode))
			if err != nil || !value.ToBoolean() {
				t.Fatalf("object access=%v err=%v", value, err)
			}
		})
	}
}

func TestScriptResultPreservesStructuredValues(t *testing.T) {
	previous := globalConfig
	globalConfig = Config{}
	t.Cleanup(func() { globalConfig = previous })
	for _, tc := range []struct {
		name, script, want string
		invalid            bool
	}{
		{"object", `({status:200,result:{name:"猫",values:[1,true,null]}})`, `{"result":{"name":"猫","values":[1,true,null]},"status":200}`, false},
		{"array", `[1,{name:"猫"},null]`, `[1,{"name":"猫"},null]`, false},
		{"null", `null`, `null`, false}, {"undefined", `undefined`, `null`, false},
		{"boolean", `false`, `false`, false}, {"number", `1.5`, `1.5`, false},
		{"plain text", `"hello"`, `hello`, false}, {"empty string", `""`, ``, false},
		{"JSON string preserves whitespace", `' {"status":200, "value":"猫"} '`, ` {"status":200, "value":"猫"} `, false},
		{"NaN", `NaN`, ``, true}, {"infinity", `Infinity`, ``, true},
		{"function property", `({callback:function(){}})`, ``, true},
		{"cycle", `const o={};o.self=o;o;`, ``, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "main.js")
			writeHotReloadTestFile(t, path, tc.script)
			got, err := runJavaScriptWithSnapshot(nil, path, nil, nil)
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "encode script result") {
					t.Fatalf("got=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got=%q want=%q err=%v", got, tc.want, err)
			}
		})
	}
}

func TestStructuredScriptResultsAcrossTransports(t *testing.T) {
	for _, transport := range []string{"http", "root", "jsonrpc", "websocket", "internal", "mcp"} {
		for _, asString := range []bool{false, true} {
			for _, status := range []int{201, 409} {
				t.Run(fmt.Sprintf("%s/string=%t/status=%d", transport, asString, status), func(t *testing.T) {
					dir := t.TempDir()
					key := t.Name()
					t.Cleanup(func() { storage.Delete(key) })
					write := func(name, script string) string {
						path := filepath.Join(dir, name+".js")
						writeHotReloadTestFile(t, path, script)
						return path
					}
					body := fmt.Sprintf(`{success:%t,status:%d,result:{name:"猫",values:[1,true,null]},api:nyanAllParams.api}`, status < 400, status)
					source := `(` + body + `);`
					if asString {
						source = `JSON.stringify(` + body + `);`
					}
					out := fmt.Sprintf(`const r=JSON.parse(nyanAllParams.nyan_output_body);if(nyanAllParams.api!=="source" || r.api!=="source" || r.result.name!=="猫" || r.result.values[1]!==true || nyanAllParams.nyan_output.status!==%d)throw new Error("lost output");({success:true,status:%d});`, status, status)
					pushOut := `const r=JSON.parse(nyanAllParams.nyan_output_body);if(nyanAllParams.api!=="events" || r.api!=="events" || r.notice!=="猫")throw new Error("lost Push");({success:true,status:200});`
					f := newWebSocketCheckFixture(t, map[string]interface{}{
						"source":   map[string]interface{}{"script": write("source", source), "outCheck": write("out", out), "push": "events"},
						"events":   map[string]interface{}{"script": write("push", fmt.Sprintf(`nyanSetItem(%q,"sent");({status:200,notice:"猫",api:nyanAllParams.api});`, key)), "outCheck": write("push-out", pushOut)},
						"recovery": map[string]interface{}{"script": write("recovery", `"barrier";`)},
					})
					subscriber := f.dial("/events", nil)
					if got := exchangeWebSocketCheckFrame(t, subscriber, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
						t.Fatal(got)
					}
					var got map[string]interface{}
					switch transport {
					case "internal":
						vm := goja.New()
						setupGojaVMWithSnapshot(vm, currentAPISnapshot(), nil)
						value, err := vm.RunString(`nyanCallMe({api:"source"})`)
						if err != nil {
							t.Fatal(err)
						}
						got, _ = value.Export().(map[string]interface{})
					case "mcp":
						result, failure := executeMCPTool(currentAPISnapshot(), &MCPToolConfig{Name: "source", API: "source"}, nil, nil)
						if failure != "" || result["isError"] != (status >= 400) {
							t.Fatalf("MCP=%v failure=%s", result, failure)
						}
						encoded, err := json.Marshal(result["structuredContent"])
						if err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(encoded, &got); err != nil {
							t.Fatal(err)
						}
					case "websocket":
						conn := f.dial("/", nil)
						reply := exchangeWebSocketCheckFrame(t, conn, websocket.TextMessage, `{"api":"source"}`)
						if err := json.Unmarshal([]byte(reply), &got); err != nil {
							t.Fatal(err)
						}
					default:
						path, method, requestBody := "/source", http.MethodGet, ""
						if transport == "root" {
							path = "/?api=source"
						}
						if transport == "jsonrpc" {
							path = "/nyan-rpc"
							method = http.MethodPost
							requestBody = `{"jsonrpc":"2.0","id":1,"method":"source","params":{}}`
						}
						request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(requestBody))
						if err != nil {
							t.Fatal(err)
						}
						if requestBody != "" {
							request.Header.Set("Content-Type", "application/json")
						}
						response, err := f.server.Client().Do(request)
						if err != nil {
							t.Fatal(err)
						}
						data, err := io.ReadAll(response.Body)
						response.Body.Close()
						if err != nil {
							t.Fatal(err)
						}
						wantStatus := status
						if transport == "jsonrpc" {
							wantStatus = 200
						}
						if response.StatusCode != wantStatus {
							t.Fatalf("HTTP=%d want=%d body=%s", response.StatusCode, wantStatus, data)
						}
						if err := json.Unmarshal(data, &got); err != nil {
							t.Fatal(err)
						}
						if transport == "jsonrpc" {
							if status >= 400 {
								rpcError, _ := got["error"].(map[string]interface{})
								got, _ = rpcError["data"].(map[string]interface{})
							} else {
								got, _ = got["result"].(map[string]interface{})
							}
						}
					}
					result, ok := got["result"].(map[string]interface{})
					if !ok || got["api"] != "source" || result["name"] != "猫" || got["success"] != (status < 400) {
						t.Fatalf("lost result: %#v", got)
					}
					values, ok := result["values"].([]interface{})
					if !ok || len(values) != 3 || values[1] != true || values[2] != nil {
						t.Fatalf("lost nested values: %#v", result)
					}
					if status < 400 {
						_ = subscriber.SetReadDeadline(time.Now().Add(3 * time.Second))
						_, data, err := subscriber.ReadMessage()
						if err != nil {
							t.Fatal(err)
						}
						var notification map[string]interface{}
						if err := json.Unmarshal(data, &notification); err != nil || notification["notice"] != "猫" || notification["api"] != "events" {
							t.Fatalf("Push=%s err=%v", data, err)
						}
					}
					// The barrier also detects an unexpected notification for failed source results.
					if got := exchangeWebSocketCheckFrame(t, subscriber, websocket.TextMessage, `{"api":"recovery"}`); got != "barrier" {
						t.Fatalf("unexpected Push: %s", got)
					}
					sent, _ := storage.Load(key)
					if (sent == "sent") != (status < 400) {
						t.Fatalf("Push marker=%v", sent)
					}
				})
			}
		}
	}
}

func newArgon2ContractRuntime(t *testing.T) *goja.Runtime {
	vm := goja.New()
	setupOAuthGojaVM(vm, nil, &MCPServerConfig{})
	return vm
}

func TestArgon2idCommonVerificationContract(t *testing.T) {
	const saved = "$argon2id$v=19$m=65536,t=3,p=2$MDEyMzQ1Njc4OWFiY2RlZg$8AcZ9tO47h2U7BO3dpzQuEogqm6bqJy8+taXF1/F90g"
	verify := func(t *testing.T, password, encoded string, want bool) {
		t.Helper()
		vm := newArgon2ContractRuntime(t)
		value, err := vm.RunString(fmt.Sprintf(`nyanArgon2idVerify(%q,%q)`, password, encoded))
		if err != nil || value.Export() != want {
			t.Fatalf("verify=%v want=%t err=%v", value, want, err)
		}
	}
	t.Run("saved standard", func(t *testing.T) { verify(t, "fixture-password", saved, true) })
	t.Run("wrong password", func(t *testing.T) { verify(t, "wrong-password", saved, false) })
	for _, tc := range []struct {
		name, password        string
		memory, iterations    uint32
		parallelism           uint8
		saltLength, keyLength int
	}{
		{"higher time", "fixture-password", 65536, 4, 2, 16, 32},
		{"parallelism one", "fixture-password", 65536, 3, 1, 16, 32},
		{"higher memory", "fixture-password", 131072, 3, 2, 16, 32},
		{"minimum digest", "fixture-password", 65536, 3, 2, 16, 16},
		{"long salt and digest", "fixture-password", 65536, 3, 2, 64, 64},
		{"interior sizes", "fixture-password", 65537, 3, 3, 17, 33},
		{"upper limits", "fixture-password", 262144, 10, 16, 64, 64},
		{"4096 bytes", strings.Repeat("x", 4096), 65536, 3, 2, 16, 32},
		{"multibyte password", strings.Repeat("猫", 1365), 65536, 3, 2, 16, 32},
		{"empty verification remains supported", "", 65536, 3, 2, 16, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Generate an independent deterministic fixture with the Argon2 library;
			// generation through the product's API deliberately stays at standard settings.
			salt := bytes.Repeat([]byte("s"), tc.saltLength)
			digest := argon2.IDKey([]byte(tc.password), salt, tc.iterations, tc.memory, tc.parallelism, uint32(tc.keyLength))
			encoded := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", tc.memory, tc.iterations, tc.parallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(digest))
			verify(t, tc.password, encoded, true)
		})
	}
	parts := strings.Split(saved, "$")
	changed := func(index int, value string) string {
		p := append([]string(nil), parts...)
		p[index] = value
		return strings.Join(p, "$")
	}
	for _, tc := range []struct{ name, encoded string }{
		{"empty", ""}, {"oversize hash", strings.Repeat("x", 10000)},
		{"prefix", changed(0, "junk")}, {"missing prefix", strings.TrimPrefix(saved, "$")}, {"extra field", saved + "$extra"},
		{"algorithm", changed(1, "argon2i")}, {"version", changed(2, "v=16")}, {"version suffix", changed(2, "v=19x")}, {"version space", changed(2, "v=19 ")}, {"version zero", changed(2, "v=019")},
		{"low memory", changed(3, "m=65535,t=3,p=2")}, {"high memory", changed(3, "m=262145,t=3,p=2")},
		{"low time", changed(3, "m=65536,t=2,p=2")}, {"high time", changed(3, "m=65536,t=11,p=2")},
		{"low parallelism", changed(3, "m=65536,t=3,p=0")}, {"high parallelism", changed(3, "m=65536,t=3,p=17")},
		{"all zero", changed(3, "m=0,t=0,p=0")}, {"overflow", changed(3, "m=4294967296,t=3,p=2")},
		{"narrowing overflow", changed(3, "m=65536,t=3,p=258")}, {"negative", changed(3, "m=-65536,t=3,p=2")},
		{"sign", changed(3, "m=+65536,t=3,p=2")}, {"leading zero", changed(3, "m=065536,t=3,p=2")},
		{"suffix", changed(3, parts[3]+",extra=1")}, {"trailing text", changed(3, parts[3]+"x")}, {"trailing space", changed(3, parts[3]+" ")},
		{"reordered", changed(3, "t=3,m=65536,p=2")}, {"duplicate", changed(3, "m=65536,t=3,t=2")}, {"missing", changed(3, "m=65536,t=3")},
		{"short salt", changed(4, base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 15)))},
		{"long salt", changed(4, base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 65)))},
		{"short digest", changed(5, base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 15)))},
		{"long digest", changed(5, base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 65)))},
		{"invalid salt", changed(4, strings.Repeat("!", 22))}, {"invalid digest", changed(5, strings.Repeat("!", 43))},
		{"padded salt", changed(4, parts[4]+"==")}, {"padded digest", changed(5, parts[5]+"=")},
		{"salt newline", changed(4, parts[4][:5]+"\n"+parts[4][5:])}, {"digest CRLF", changed(5, parts[5][:5]+"\r\n"+parts[5][5:])},
		{"noncanonical salt bits", changed(4, parts[4][:len(parts[4])-1]+"h")},
		{"noncanonical digest bits", changed(5, parts[5][:len(parts[5])-1]+"h")},
		{"URL safe", changed(5, strings.ReplaceAll(parts[5], "+", "-"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseArgon2idHash(tc.encoded); err == nil {
				t.Fatal("invalid hash parsed")
			}
			verify(t, "fixture-password", tc.encoded, false)
		})
	}
	t.Run("overlong password", func(t *testing.T) { verify(t, strings.Repeat("x", 4097), saved, false) })
	t.Run("overlong multibyte password", func(t *testing.T) { verify(t, strings.Repeat("猫", 1366), saved, false) })
	t.Run("generation unchanged", func(t *testing.T) {
		vm := newArgon2ContractRuntime(t)
		value, err := vm.RunString(`const a=nyanArgon2idHash("fixture-password"), b=nyanArgon2idHash("fixture-password");[a,b];`)
		if err != nil {
			t.Fatal(err)
		}
		values := value.Export().([]interface{})
		if values[0] == values[1] {
			t.Fatal("salt reused")
		}
		for _, value := range values {
			encoded := value.(string)
			parsed, err := parseArgon2idHash(encoded)
			if err != nil || parsed.memory != 65536 || parsed.iterations != 3 || parsed.parallelism != 2 || len(parsed.salt) != 16 || len(parsed.digest) != 32 {
				t.Fatalf("generation changed: %s %v", encoded, err)
			}
			verify(t, "fixture-password", encoded, true)
		}
		for _, password := range []string{"", strings.Repeat("x", 4097)} {
			if _, err := vm.RunString(fmt.Sprintf(`nyanArgon2idHash(%q)`, password)); err == nil {
				t.Fatal("invalid password generated")
			}
		}
	})
}

func TestArgon2idCalculationSlots(t *testing.T) {
	const saved = "$argon2id$v=19$m=65536,t=3,p=2$MDEyMzQ1Njc4OWFiY2RlZg$8AcZ9tO47h2U7BO3dpzQuEogqm6bqJy8+taXF1/F90g"
	if cap(oauthArgon2Slots) != 2 {
		t.Fatalf("slots=%d", cap(oauthArgon2Slots))
	}
	for _, operation := range []string{"generate", "verify", "invalid", "overlong"} {
		t.Run(operation, func(t *testing.T) {
			oauthArgon2Slots <- struct{}{}
			oauthArgon2Slots <- struct{}{}
			held := 2
			release := func() {
				for held > 0 {
					<-oauthArgon2Slots
					held--
				}
			}
			defer release()
			started, done := make(chan struct{}), make(chan error, 1)
			go func() {
				vm := newArgon2ContractRuntime(t)
				close(started)
				script := fmt.Sprintf(`nyanArgon2idVerify("fixture-password",%q)`, saved)
				switch operation {
				case "generate":
					script = `nyanArgon2idHash("fixture-password")`
				case "invalid":
					script = `nyanArgon2idVerify("fixture-password","bad")`
				case "overlong":
					script = fmt.Sprintf(`nyanArgon2idVerify(%q,%q)`, strings.Repeat("x", 4097), saved)
				}
				value, err := vm.RunString(script)
				if err == nil && operation != "generate" && value.Export() != (operation == "verify") {
					err = fmt.Errorf("unexpected result: %v", value)
				}
				done <- err
			}()
			<-started
			if operation == "invalid" || operation == "overlong" {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					release()
					<-done
					t.Fatal("invalid input waited for calculation slot")
				}
				return
			}
			select {
			case err := <-done:
				t.Fatalf("calculation bypassed full slots: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			<-oauthArgon2Slots
			held--
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				release()
				<-done
				t.Fatal("calculation did not finish after releasing a slot")
			}
		})
	}
	if len(oauthArgon2Slots) != 0 {
		t.Fatal("calculation slot leaked")
	}
}

func runPushTargetContract(t *testing.T, scripts map[string]string, params map[string]interface{}) {
	t.Helper()
	f := newWebSocketCheckFixture(t, map[string]interface{}{"group/events": map[string]interface{}{"script": scripts["main"], "paramCheck": scripts["param"], "outCheck": scripts["out"]}})
	snapshot := currentAPISnapshot()
	performPushWithSnapshot(snapshot, map[string]interface{}{"push": "group/events"}, snapshot.Definitions, params, f.dir)
}

func TestPushTargetAPIContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]interface{}
		stop   string
	}{
		{"source", map[string]interface{}{"api": "source", "nested": map[string]interface{}{"value": "kept"}}, ""},
		{"slash source", map[string]interface{}{"api": "/source"}, ""},
		{"forged name", map[string]interface{}{"api": "not-the-target"}, ""},
		{"number api", map[string]interface{}{"api": 7}, ""},
		{"array api", map[string]interface{}{"api": []interface{}{"x", "y"}}, ""},
		{"null api", map[string]interface{}{"api": nil}, ""},
		{"missing api", map[string]interface{}{"value": "kept"}, ""},
		{"nil parameters", nil, ""},
		{"input rejection", map[string]interface{}{"api": "source"}, "param"},
		{"body exception", map[string]interface{}{"api": "source"}, "main"},
		{"output rejection", map[string]interface{}{"api": "source"}, "out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			before, err := json.Marshal(tc.params)
			if err != nil {
				t.Fatal(err)
			}
			scripts := map[string]string{}
			for _, stage := range []string{"param", "main", "out"} {
				path := filepath.Join(dir, stage+".js")
				script := fmt.Sprintf(`if(nyanAllParams.api!=="group/events")throw new Error("wrong Push target");nyanWriteTextFile(%q,nyanAllParams.api);`, filepath.Join(dir, stage+".txt"))
				if stage == tc.stop {
					script += `nyanAllParams.api="changed";nyanAllParams.extra="changed";`
					if stage == "main" {
						script += `throw new Error("expected Push failure");`
					} else {
						script += `({success:false,status:403});`
					}
				} else if stage == "main" {
					script += `"notification";`
				} else {
					script += `({success:true,status:200});`
				}
				if err := os.WriteFile(path, []byte(script), 0600); err != nil {
					t.Fatal(err)
				}
				scripts[stage] = path
			}
			runPushTargetContract(t, scripts, tc.params)
			after, err := json.Marshal(tc.params)
			if err != nil || string(before) != string(after) {
				t.Fatalf("source mutated: before=%s after=%s err=%v", before, after, err)
			}
			for _, stage := range []string{"param", "main", "out"} {
				want := stage == "param" || (stage == "main" && tc.stop != "param") || (stage == "out" && tc.stop != "param" && tc.stop != "main")
				data, err := os.ReadFile(filepath.Join(dir, stage+".txt"))
				if want {
					if err != nil || string(data) != "group/events" {
						t.Fatalf("stage=%s data=%s err=%v", stage, data, err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("unexpected stage %s: %s %v", stage, data, err)
				}
			}
		})
	}
}

func TestGetItemMissingAndStoredValues(t *testing.T) {
	t.Run("missing returns null without inserting", func(t *testing.T) {
		key := t.Name()
		defer storage.Delete(key)
		vm := newCommonFileWriterTestVM("")
		value, err := vm.RunString(fmt.Sprintf(`nyanGetItem(%q) === null && JSON.stringify({value:nyanGetItem(%q)}) === '{"value":null}'`, key, key))
		if err != nil || !value.ToBoolean() {
			t.Fatalf("missing value=%v err=%v", value, err)
		}
		if _, ok := storage.Load(key); ok {
			t.Fatal("read inserted a missing key")
		}
	})
	for _, tc := range []struct{ name, value string }{
		{"empty", ""}, {"text", "hello"}, {"unicode", "猫\n🌸"}, {"literal null", "null"}, {"zero string", "0"}, {"false string", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, other := t.Name(), t.Name()+" other"
			defer storage.Delete(key)
			defer storage.Delete(other)
			writer, reader := newCommonFileWriterTestVM(""), newCommonFileWriterTestVM("")
			if _, err := writer.RunString(fmt.Sprintf(`nyanSetItem(%q,%q);nyanSetItem(%q,"unchanged");`, key, tc.value, other)); err != nil {
				t.Fatal(err)
			}
			value, err := reader.RunString(fmt.Sprintf(`typeof nyanGetItem(%q)==="string" && nyanGetItem(%q)===%q && nyanGetItem(%q)==="unchanged"`, key, key, tc.value, other))
			if err != nil || !value.ToBoolean() {
				t.Fatalf("stored value=%v err=%v", value, err)
			}
		})
	}
	t.Run("overwrite preserves empty and other keys", func(t *testing.T) {
		key := t.Name()
		defer storage.Delete(key)
		vm := newCommonFileWriterTestVM("")
		value, err := vm.RunString(fmt.Sprintf(`(()=>{nyanSetItem(%q,"first");nyanSetItem(%q,"");return nyanGetItem(%q)==="" && nyanGetItem(%q)!==null;})()`, key, key, key, key))
		if err != nil || !value.ToBoolean() {
			t.Fatalf("overwrite=%v err=%v", value, err)
		}
	})
	t.Run("explicit string default", func(t *testing.T) {
		key := t.Name()
		defer storage.Delete(key)
		vm := newCommonFileWriterTestVM("")
		value, err := vm.RunString(fmt.Sprintf(`(()=>{const text=nyanGetItem(%q) ?? "";nyanSetItem(%q,text+"next");return text.trim()==="" && nyanGetItem(%q)==="next";})()`, key, key, key))
		if err != nil || !value.ToBoolean() {
			t.Fatalf("default=%v err=%v", value, err)
		}
	})
	t.Run("configuration reload retains process storage", func(t *testing.T) {
		key := t.Name()
		defer storage.Delete(key)
		previous := currentAPISnapshot()
		t.Cleanup(func() { publishAPISnapshot(previous) })
		vm := newCommonFileWriterTestVM("")
		if _, err := vm.RunString(fmt.Sprintf(`nyanSetItem(%q,"retained")`, key)); err != nil {
			t.Fatal(err)
		}
		publishAPISnapshot(&APIConfigSnapshot{RootPath: filepath.Join(t.TempDir(), "api.json")})
		after := goja.New()
		setupGojaVMWithSnapshot(after, currentAPISnapshot(), nil)
		value, err := after.RunString(fmt.Sprintf(`nyanGetItem(%q)==="retained"`, key))
		if err != nil || !value.ToBoolean() {
			t.Fatalf("after reload=%v err=%v", value, err)
		}
	})
}

func TestJSONRPCPreservesEnvelopeForBodylessCheckStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{200, 201, 204, 205, 304, 403} {
		for _, stage := range []string{"out_allow", "out_deny", "check_only_allow", "check_only_deny"} {
			t.Run(fmt.Sprintf("%s/%d", stage, status), func(t *testing.T) {
				initTestLogger()
				dir := t.TempDir()
				t.Chdir(dir)
				marker := "rpc-envelope:" + t.Name()
				t.Cleanup(func() { storage.Delete(marker) })
				checkOnly := strings.HasPrefix(stage, "check_only")
				success := strings.HasSuffix(stage, "allow")
				param := `({success:true,status:200,result:null});`
				out := fmt.Sprintf(`nyanSetItem(%q,nyanGetItem(%q)+"out,"); ({success:%t,status:%d,result:"checked"});`, marker, marker, success, status)
				if checkOnly {
					param = fmt.Sprintf(`({success:%t,status:%d,result:"checked"});`, success, status)
				}
				for name, content := range map[string]string{
					"api.json": `{"checked":{"script":"./main.js","paramCheck":"./param.js","outCheck":"./out.js","push":"event"},"event":{"script":"./push.js"}}`,
					"param.js": param,
					"out.js":   out,
					"main.js":  fmt.Sprintf(`nyanSetItem(%q,"main,"); ({status:201,value:"日本語"});`, marker),
					"push.js":  fmt.Sprintf(`nyanSetItem(%q,nyanGetItem(%q)+"push,"); "event";`, marker, marker),
				} {
					writeHotReloadTestFile(t, filepath.Join(dir, name), content)
				}
				router := gin.New()
				router.POST("/nyan-rpc", handleJSONRPC)
				// Use a real HTTP response so transport-level content suppression is covered.
				server := httptest.NewServer(router)
				defer server.Close()
				params := `{}`
				if checkOnly {
					params = `{"nyan_mode":"checkOnly"}`
				}
				response, err := server.Client().Post(server.URL+"/nyan-rpc", "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"checked","id":"review-1","params":`+params+`}`))
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				wantStatus := status
				if stage == "out_allow" || status == 204 || status == 205 || status == 304 {
					wantStatus = 200
				}
				if response.StatusCode != wantStatus {
					t.Fatalf("HTTP=%d want=%d", response.StatusCode, wantStatus)
				}
				var envelope map[string]interface{}
				if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
					t.Fatalf("missing JSON-RPC envelope: %v", err)
				}
				if envelope["jsonrpc"] != "2.0" || envelope["id"] != "review-1" || envelope["error"] != nil {
					t.Fatalf("envelope=%v", envelope)
				}
				result, ok := envelope["result"].(map[string]interface{})
				if !ok {
					t.Fatalf("result=%v", envelope["result"])
				}
				wantOrder := "main,out,"
				if checkOnly || !success {
					if result["status"] != float64(status) || result["success"] != success || result["result"] != "checked" {
						t.Fatalf("check result=%v", result)
					}
					if checkOnly {
						wantOrder = ""
					}
				} else {
					if result["value"] != "日本語" || len(result) != 1 {
						t.Fatalf("body replaced: %v", result)
					}
					wantOrder += "push,"
				}
				order, _ := storage.Load(marker)
				if order == nil {
					order = ""
				}
				if order != wantOrder {
					t.Fatalf("order=%q want=%q", order, wantOrder)
				}
			})
		}
	}
}

func TestExecutionModeReferencedSchema(t *testing.T) {
	const input = `{"$anchor":"InputAnchor","type":"object","properties":{"value":{"type":"string"},"child":{"$anchor":"ChildAnchor","$ref":"#/$defs/Input"},"embedded":{"$id":"urn:nyan8:embedded-input","type":"object","properties":{"flag":{"type":"boolean"}},"additionalProperties":false}},"required":["value"],"additionalProperties":false}`
	for _, rootRef := range []string{"#/$defs/Input", "#/$defs/Alias", "#/$defs/Input~1escaped~0name", "#InputAnchor"} {
		t.Run(rootRef, func(t *testing.T) {
			var original map[string]interface{}
			raw := fmt.Sprintf(`{"$id":"urn:nyan8:root-input","type":"object","$ref":%q,"$defs":{"Input":%s,"Alias":{"$ref":"#/$defs/Input"},"Input/escaped~name":%s,"nyan_input":{"const":"reserved"}}}`, rootRef, input, strings.NewReplacer(`"$anchor":"InputAnchor",`, "", `"$anchor":"ChildAnchor",`, "", `"$id":"urn:nyan8:embedded-input",`, "").Replace(input))
			if err := json.Unmarshal([]byte(raw), &original); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(original)
			normalized := normalizedMCPInputSchema(original)
			after, _ := json.Marshal(original)
			if !bytes.Equal(before, after) {
				t.Fatal("normalization mutated original schema")
			}
			if twice := normalizedMCPInputSchema(normalized); !reflect.DeepEqual(twice, normalized) {
				t.Fatal("repeated normalization changed schema")
			}
			// tools/list consumers compile the schema under their own URI.
			clientCompiler := jsonschema.NewCompiler()
			clientCompiler.UseLoader(rejectingMCPJSONSchemaLoader{})
			const clientLocation = "https://client.example/tool-input"
			if err := clientCompiler.AddResource(clientLocation, normalized); err != nil {
				t.Fatal(err)
			}
			if _, err := clientCompiler.Compile(clientLocation); err != nil {
				t.Fatalf("published schema is not self-contained: %v", err)
			}
			for _, tc := range []struct {
				name, args string
				invalid    bool
			}{
				{"omitted", `{"value":"ok"}`, false},
				{"check_only", `{"value":"ok","nyan_mode":"checkOnly"}`, false},
				{"normal", `{"value":"ok","nyan_mode":""}`, false},
				{"required", `{"nyan_mode":"checkOnly"}`, true},
				{"type", `{"value":1,"nyan_mode":"checkOnly"}`, true},
				{"extra", `{"value":"ok","extra":1,"nyan_mode":"checkOnly"}`, true},
				{"bad_mode", `{"value":"ok","nyan_mode":"bad"}`, true},
				{"null_mode", `{"value":"ok","nyan_mode":null}`, true},
				{"nested", `{"value":"ok","nyan_mode":"checkOnly","child":{"value":"child"}}`, false},
				{"nested_mode_stays_forbidden", `{"value":"ok","child":{"value":"child","nyan_mode":"checkOnly"}}`, true},
				{"nested_required", `{"value":"ok","nyan_mode":"checkOnly","child":{}}`, true},
				{"embedded_resource", `{"value":"ok","nyan_mode":"checkOnly","embedded":{"flag":true}}`, false},
				{"embedded_extra", `{"value":"ok","nyan_mode":"checkOnly","embedded":{"flag":true,"extra":1}}`, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var args interface{}
					if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
						t.Fatal(err)
					}
					if err := validateMCPJSONSchemaValue(normalized, args); (err != nil) != tc.invalid {
						t.Fatalf("validation=%v want invalid=%t", err, tc.invalid)
					}
				})
			}
		})
	}
}

func TestMCPReferencedInputSchemaAcrossTransports(t *testing.T) {
	dir, definitions := newMCPPhase12Definitions(t)
	marker := "mcp-ref:" + t.Name()
	t.Cleanup(func() { storage.Delete(marker) })
	writeHotReloadTestFile(t, filepath.Join(dir, "oauth-hook.js"), mcpPhase2GapAuthenticatedHook())
	writeHotReloadTestFile(t, filepath.Join(dir, "sample-input.js"), fmt.Sprintf(`
 const nyanInputSchema={type:"object",$ref:"#/$defs/Input",$defs:{Input:{$dynamicAnchor:"Input",type:"object",properties:{value:{type:"string"}},required:["value"],additionalProperties:false}}};
 nyanSetItem(%q,"param,");
 ({success:true,status:200,result:{mode:nyanAllParams.nyan_mode,value:nyanAllParams.value}});`, marker))
	writeHotReloadTestFile(t, filepath.Join(dir, "sample.js"), fmt.Sprintf(`nyanSetItem(%q,nyanGetItem(%q)+"main,"); ({ok:true,service:"Nyan8",items:[1,2,3]});`, marker, marker))
	definitions["local-mcp"] = map[string]interface{}{"type": "mcp", "transport": "stdio", "tools": []interface{}{"sample"}}
	loaded, err := loadMCPPhase12Config(dir, definitions)
	if err != nil {
		t.Fatal(err)
	}
	router := publishMCPPhase12Snapshot(t, loaded)
	for _, transport := range []string{"http", "stdio"} {
		t.Run(transport, func(t *testing.T) {
			request := func(body string) map[string]interface{} {
				t.Helper()
				var envelope map[string]interface{}
				if transport == "http" {
					req := newMCPPhase12Request(http.MethodPost, "/custom-mcp", body)
					req.Header.Set("MCP-Protocol-Version", mcpProtocol20251125)
					req.Header.Set("Authorization", "Bearer phase2")
					rec := serveMCPPhase12Request(router, req)
					if rec.Code != http.StatusOK {
						t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
						t.Fatal(err)
					}
				} else {
					input := mcpPhase12InitializeBody(mcpProtocol20251125) + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n" + body + "\n"
					var output bytes.Buffer
					if err := serveMCPStdio(strings.NewReader(input), &output, loaded.Snapshot, loaded.Snapshot.MCPServers["local-mcp"]); err != nil {
						t.Fatal(err)
					}
					lines := strings.Split(strings.TrimSpace(output.String()), "\n")
					if len(lines) != 2 {
						t.Fatalf("stdio=%s", output.String())
					}
					if err := json.Unmarshal([]byte(lines[1]), &envelope); err != nil {
						t.Fatal(err)
					}
				}
				if envelope["error"] != nil {
					t.Fatalf("RPC error=%v", envelope)
				}
				return envelope["result"].(map[string]interface{})
			}
			list := request(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
			tool := list["tools"].([]interface{})[0].(map[string]interface{})
			schema := tool["inputSchema"].(map[string]interface{})
			for _, tc := range []struct {
				name, args, order  string
				invalid, checkOnly bool
			}{
				{"check_only", `{"value":"ok","nyan_mode":"checkOnly"}`, "param,", false, true},
				{"normal", `{"value":"ok","nyan_mode":""}`, "param,main,", false, false},
				{"missing_required", `{"nyan_mode":"checkOnly"}`, "", true, false},
				{"unknown", `{"value":"ok","nyan_mode":"checkOnly","extra":true}`, "", true, false},
				{"wrong_type", `{"value":42,"nyan_mode":"checkOnly"}`, "", true, false},
				{"wrong_mode", `{"value":"ok","nyan_mode":"CHECKONLY"}`, "", true, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					storage.Delete(marker)
					var args interface{}
					if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
						t.Fatal(err)
					}
					if err := validateMCPJSONSchemaValue(schema, args); (err != nil) != tc.invalid {
						t.Fatalf("published schema validation=%v", err)
					}
					result := request(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sample","arguments":` + tc.args + `}}`)
					if result["isError"] != tc.invalid {
						t.Fatalf("Tool result=%v", result)
					}
					order, _ := storage.Load(marker)
					if order == nil {
						order = ""
					}
					if order != tc.order {
						t.Fatalf("order=%q want=%q", order, tc.order)
					}
					if tc.invalid {
						return
					}
					content := result["structuredContent"].(map[string]interface{})
					if tc.checkOnly {
						if content["success"] != true || content["status"] != float64(200) {
							t.Fatalf("check=%v", content)
						}
						checked := content["result"].(map[string]interface{})
						if checked["mode"] != "checkOnly" || checked["value"] != "ok" {
							t.Fatalf("check arguments=%v", checked)
						}
					} else if content["ok"] != true {
						t.Fatalf("body=%v", content)
					}
				})
			}
		})
	}
}

func TestExecutionModeDynamicAnchor(t *testing.T) {
	for _, ref := range []string{"#/$defs/Input", "#Input"} {
		t.Run(ref, func(t *testing.T) {
			var original map[string]interface{}
			raw := fmt.Sprintf(`{"type":"object","$ref":%q,"$defs":{"Input":{"$dynamicAnchor":"Input","type":"object","properties":{"value":{"type":"string"},"child":{"$dynamicRef":"#Input"}},"required":["value"],"additionalProperties":false}}}`, ref)
			if err := json.Unmarshal([]byte(raw), &original); err != nil {
				t.Fatal(err)
			}
			if _, err := compileMCPJSONSchema(original); err != nil {
				t.Fatalf("original schema: %v", err)
			}
			before, _ := json.Marshal(original)
			normalized := normalizedMCPInputSchema(original)
			compiled, err := compileMCPJSONSchema(normalized)
			if err != nil {
				t.Fatalf("normalized schema: %v", err)
			}
			after, _ := json.Marshal(original)
			if !bytes.Equal(before, after) {
				t.Fatal("normalization mutated original schema")
			}
			definitions := normalized["$defs"].(map[string]interface{})
			if definitions["Input"].(map[string]interface{})["$dynamicAnchor"] != "Input" {
				t.Fatal("original dynamic anchor was removed")
			}
			if twice := normalizedMCPInputSchema(normalized); !reflect.DeepEqual(twice, normalized) {
				t.Fatal("repeated normalization changed schema")
			}
			for _, tc := range []struct {
				name, args string
				invalid    bool
			}{
				{"omitted", `{"value":"ok"}`, false},
				{"normal", `{"value":"ok","nyan_mode":""}`, false},
				{"check_only", `{"value":"ok","nyan_mode":"checkOnly"}`, false},
				{"missing_required", `{"nyan_mode":"checkOnly"}`, true},
				{"bad_mode", `{"value":"ok","nyan_mode":"CHECKONLY"}`, true},
				{"extra", `{"value":"ok","nyan_mode":"checkOnly","extra":true}`, true},
				{"recursive", `{"value":"ok","nyan_mode":"checkOnly","child":{"value":"child","child":{"value":"grandchild"}}}`, false},
				{"recursive_required", `{"value":"ok","child":{"value":"child","child":{}}}`, true},
				{"recursive_type", `{"value":"ok","child":{"value":42}}`, true},
				{"recursive_extra", `{"value":"ok","child":{"value":"child","extra":true}}`, true},
				{"recursive_mode_stays_forbidden", `{"value":"ok","child":{"value":"child","nyan_mode":"checkOnly"}}`, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var args interface{}
					if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
						t.Fatal(err)
					}
					if err := compiled.Validate(args); (err != nil) != tc.invalid {
						t.Fatalf("validation=%v want invalid=%t", err, tc.invalid)
					}
				})
			}
		})
	}
}

func TestPublicPassedOutCheckPreservesTransfer(t *testing.T) {
	for _, checkStatus := range []int{200, 204, 205, 304, 503} {
		for _, kind := range []string{"range", "head", "not_modified"} {
			t.Run(fmt.Sprintf("%s/%d", kind, checkStatus), func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("abcdef"), 0600); err != nil {
					t.Fatal(err)
				}
				out := fmt.Sprintf(`({success:true,status:%d,result:"must not replace file"});`, checkStatus)
				outPath := filepath.Join(dir, "out.js")
				writeHotReloadTestFile(t, outPath, out)
				fixture := newWebSocketCheckFixture(t, map[string]interface{}{"assets": map[string]interface{}{"type": "public", "path": dir, "outCheck": outPath}})
				handler := fixture.server.Config.Handler
				method := http.MethodGet
				if kind == "head" {
					method = http.MethodHead
				}
				req := httptest.NewRequest(method, "/assets/data.txt", nil)
				wantStatus, wantBody := http.StatusOK, ""
				switch kind {
				case "range":
					req.Header.Set("Range", "bytes=1-3")
					wantStatus, wantBody = http.StatusPartialContent, "bcd"
				case "not_modified":
					req.Header.Set("If-Modified-Since", time.Now().UTC().Add(time.Hour).Format(http.TimeFormat))
					wantStatus = http.StatusNotModified
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != wantStatus || rec.Body.String() != wantBody {
					t.Fatalf("transfer changed: HTTP=%d body=%q want=%d/%q", rec.Code, rec.Body.String(), wantStatus, wantBody)
				}
				if kind == "range" && rec.Header().Get("Content-Range") != "bytes 1-3/6" {
					t.Fatalf("range header lost: %v", rec.Header())
				}
			})
		}
	}
}

func TestReceiveLimitsConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input    string
		http, ws int64
		bad      bool
	}{
		{`{}`, 20 << 20, 20 << 20, false},
		{`{"receiveLimits":{}}`, 20 << 20, 20 << 20, false},
		{`{"receiveLimits":{"httpBodyBytes":0,"webSocketMessageBytes":0}}`, 20 << 20, 20 << 20, false},
		{`{"receiveLimits":{"httpBodyBytes":32,"webSocketMessageBytes":64}}`, 32, 64, false},
		{`{"receiveLimits":{"httpBodyBytes":-1}}`, 0, 0, true},
		{`{"receiveLimits":{"webSocketMessageBytes":-1}}`, 0, 0, true},
		{`{"receiveLimits":{"httpBodyBytes":1.5}}`, 0, 0, true},

		{`{"receiveLimits":{"httpBodyBytes":"20MB","webSocketMessageBytes":"1GB"}}`, 20 << 20, 1 << 30, false},
		{`{"receiveLimits":{"httpBodyBytes":"2MB","webSocketMessageBytes":64}}`, 2 << 20, 64, false},
		{`{"receiveLimits":{"httpBodyBytes":" 2 mb ","webSocketMessageBytes":"1GiB"}}`, 2 << 20, 1 << 30, false},
		{`{"receiveLimits":{"httpBodyBytes":"2KB","webSocketMessageBytes":"3KiB"}}`, 2 << 10, 3 << 10, false},
		{`{"receiveLimits":{"httpBodyBytes":"4B","webSocketMessageBytes":"5"}}`, 4, 5, false},
		{`{"receiveLimits":{"httpBodyBytes":"0MB","webSocketMessageBytes":"0"}}`, 20 << 20, 20 << 20, false},
		{`{"receiveLimits":{"httpBodyBytes":"9223372036854775807B"}}`, 9223372036854775807, 20 << 20, false},
		{`{"receiveLimits":{"httpBodyBytes":"8589934591GB"}}`, 8589934591 << 30, 20 << 20, false},
		{`{"receiveLimits":{"httpBodyBytes":"8589934592GB"}}`, 0, 0, true},
		{`{"receiveLimits":{"httpBodyBytes":"9223372036854775808B"}}`, 0, 0, true},
		{`{"receiveLimits":{"httpBodyBytes":"-1MB"}}`, 0, 0, true},
		{`{"receiveLimits":{"webSocketMessageBytes":"1.5MB"}}`, 0, 0, true},
		{`{"receiveLimits":{"httpBodyBytes":""}}`, 0, 0, true},
		{`{"receiveLimits":{"httpBodyBytes":"MB"}}`, 0, 0, true},
		{`{"receiveLimits":{"httpBodyBytes":"2XB"}}`, 0, 0, true},
		{`{"receiveLimits":{"httpBodyBytes":"2MBjunk"}}`, 0, 0, true},
		{`{"receiveLimits":{"httpBodyBytes":true}}`, 0, 0, true},
		{`{"receiveLimits":{"httpBodyBytes":9223372036854775808}}`, 0, 0, true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var cfg Config
			err := json.Unmarshal([]byte(tc.input), &cfg)
			if tc.bad {
				if err == nil {
					t.Fatal("invalid limit accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ReceiveLimits.httpBodyBytes() != tc.http || cfg.ReceiveLimits.webSocketMessageBytes() != tc.ws {
				t.Fatalf("wrong effective limits: %+v", cfg.ReceiveLimits)
			}
		})
	}
}

func TestReceiveLimitsHTTP(t *testing.T) {
	old := globalConfig.ReceiveLimits
	t.Cleanup(func() { globalConfig.ReceiveLimits = old })
	for _, tc := range []struct {
		name    string
		limit   int64
		size    int
		unknown bool
		status  int
	}{
		{"below", 64, 63, false, 200}, {"exact", 64, 64, false, 200}, {"over", 64, 65, false, 413},
		{"stream exact", 64, 64, true, 200}, {"stream over", 64, 65, true, 413},
		{"raised", 128, 100, true, 200},
		{"default exact", 0, 20 << 20, true, 200}, {"default over", 0, (20 << 20) + 1, true, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			globalConfig.ReceiveLimits.HTTPBodyBytes = tc.limit
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				data, err := io.ReadAll(r.Body)
				if err != nil || len(data) != tc.size {
					t.Errorf("body changed: len=%d err=%v", len(data), err)
				}
				w.WriteHeader(200)
			})
			handler := receiveTestHTTPHandler(next)
			req := httptest.NewRequest("POST", "/nyan-rpc", strings.NewReader(strings.Repeat("x", tc.size)))
			if tc.unknown {
				req.ContentLength = -1
				req.TransferEncoding = []string{"chunked"}
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status || called != (tc.status == 200) {
				t.Fatalf("status=%d called=%v", rec.Code, called)
			}
		})
	}
	// Form parsing must respect a configured limit greater than net/http's 10 MiB default.
	globalConfig.ReceiveLimits.HTTPBodyBytes = 12 << 20
	req := httptest.NewRequest("POST", "/", strings.NewReader("value="+strings.Repeat("a", 11<<20)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	receiveTestHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if len(r.PostForm.Get("value")) != 11<<20 {
			t.Error("form truncated")
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}

func TestReceiveLimitsWebSocket(t *testing.T) {
	f := newWebSocketCheckFixture(t, map[string]interface{}{"channel": map[string]interface{}{"script": "unused.js"}})
	server := f.server
	globalConfig.ReceiveLimits.WebSocketMessageBytes = 64
	for _, size := range []int{63, 64, 65} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			dialer := websocket.Dialer{WriteBufferSize: 16, HandshakeTimeout: 3 * time.Second}
			conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/channel", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			message := `{"api":"x","pad":"` + strings.Repeat("x", size-20) + `"}`
			if len(message) != size {
				t.Fatal("bad test message length")
			}
			writer, err := conn.NextWriter(websocket.TextMessage)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = writer.Write([]byte(message)); err != nil {
				t.Fatal(err)
			}
			if err = writer.Close(); err != nil {
				t.Fatal(err)
			}
			_, _, err = conn.ReadMessage()
			if size > 64 {
				if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
					t.Fatalf("expected close 1009, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func receiveTestHTTPHandler(next http.Handler) http.Handler {
	r := gin.New()
	r.Use(receiveLimitMiddleware())
	r.NoRoute(func(c *gin.Context) { next.ServeHTTP(c.Writer, c.Request) })
	return r
}

func TestReceiveLimitsLargeFormCollection(t *testing.T) {
	old := globalConfig.ReceiveLimits
	t.Cleanup(func() { globalConfig.ReceiveLimits = old })
	globalConfig.ReceiveLimits.HTTPBodyBytes = 12 << 20
	req := httptest.NewRequest("POST", "/", strings.NewReader("value="+strings.Repeat("a", 11<<20)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	if !enforceReceiveBodyLimit(rec, req) {
		t.Fatalf("status %d", rec.Code)
	}
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	params, err := collectRequestParams(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(params["value"].(string)) != 11<<20 {
		t.Fatal("form truncated")
	}
}

func TestReceiveLimitsWSClient(t *testing.T) {
	old := globalConfig.ReceiveLimits
	t.Cleanup(func() { globalConfig.ReceiveLimits = old })
	globalConfig.ReceiveLimits.WebSocketMessageBytes = 32
	closed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err = conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 33))); err != nil {
			closed <- err
			return
		}
		_, _, err = conn.ReadMessage()
		closed <- err
	}))
	defer server.Close()
	cfg := wsClientConfig{name: "limit-test", connectURL: "ws" + strings.TrimPrefix(server.URL, "http")}
	runtime := newWSClientRuntime(cfg)
	err := runtime.connectAndListen(cfg)
	if !errors.Is(err, websocket.ErrReadLimit) {
		t.Fatalf("expected receive limit error, got %v", err)
	}
	select {
	case err := <-closed:
		if !websocket.IsCloseError(err, 1009) {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("missing close")
	}
}

func TestReceiveLimitsH2CUpgrade(t *testing.T) {
	old := globalConfig.ReceiveLimits
	t.Cleanup(func() { globalConfig.ReceiveLimits = old })
	globalConfig.ReceiveLimits.HTTPBodyBytes = 32
	for _, unknown := range []bool{false, true} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 33)))
		req.Header.Set("Upgrade", "h2c")
		req.Header.Set("Connection", "Upgrade, HTTP2-Settings")
		req.Header.Set("HTTP2-Settings", "")
		if unknown {
			req.ContentLength = -1
		}
		rec := httptest.NewRecorder()
		receiveLimitH2CHandler(receiveTestHTTPHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("oversized upgrade dispatched") })), HTTPTimeoutsConfig{}).ServeHTTP(rec, req)
		if rec.Code != 413 {
			t.Fatalf("status %d", rec.Code)
		}
	}
}

func TestHTTPTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  [4]time.Duration
		bad   bool
	}{
		{`{}`, [4]time.Duration{30 * time.Second, 20 * time.Minute, 30 * time.Minute, 5 * time.Minute}, false},
		{`{"httpTimeouts":{}}`, [4]time.Duration{30 * time.Second, 20 * time.Minute, 30 * time.Minute, 5 * time.Minute}, false},
		{`{"httpTimeouts":{"readTimeout":"0s","writeTimeout":null}}`, [4]time.Duration{30 * time.Second, 20 * time.Minute, 30 * time.Minute, 5 * time.Minute}, false},
		{`{"httpTimeouts":{"readHeaderTimeout":"1s","readTimeout":"2m","writeTimeout":"1h","idleTimeout":"500ms"}}`, [4]time.Duration{time.Second, 2 * time.Minute, time.Hour, 500 * time.Millisecond}, false},
		{`{"httpTimeouts":{"readTimeout":"-1s"}}`, [4]time.Duration{}, true},
		{`{"httpTimeouts":{"readTimeout":""}}`, [4]time.Duration{}, true},
		{`{"httpTimeouts":{"readTimeout":20}}`, [4]time.Duration{}, true},
		{`{"httpTimeouts":{"writeTimeout":"30"}}`, [4]time.Duration{}, true},
		{`{"httpTimeouts":{"idleTimeout":"999999999999999999h"}}`, [4]time.Duration{}, true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var cfg Config
			err := json.Unmarshal([]byte(tc.input), &cfg)
			if tc.bad {
				if err == nil {
					t.Fatal("invalid timeout accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			server := newConfiguredHTTPServer("", nil, cfg.HTTPTimeouts)
			got := [4]time.Duration{server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout}
			if got != tc.want {
				t.Fatalf("timeouts=%v want=%v", got, tc.want)
			}
			encoded, err := json.Marshal(cfg.HTTPTimeouts)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip HTTPTimeoutsConfig
			if err = json.Unmarshal(encoded, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if roundtrip != cfg.HTTPTimeouts {
				t.Fatal("roundtrip changed timeouts")
			}
		})
	}
}

func TestHTTPTimeoutSlowRequests(t *testing.T) {
	for _, stage := range []string{"header", "body", "idle"} {
		t.Run(stage, func(t *testing.T) {
			var deadlines HTTPTimeoutsConfig
			if err := json.Unmarshal([]byte(`{"readHeaderTimeout":"150ms","readTimeout":"150ms","writeTimeout":"2s","idleTimeout":"150ms"}`), &deadlines); err != nil {
				t.Fatal(err)
			}
			handled := make(chan struct{}, 1)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !enforceReceiveBodyLimit(w, r) {
					return
				}
				handled <- struct{}{}
				w.Header().Set("Content-Length", "2")
				_, _ = w.Write([]byte("ok"))
			})
			server := httptest.NewUnstartedServer(handler)
			server.Config = newConfiguredHTTPServer("", handler, deadlines)
			server.Start()
			defer server.Close()
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			switch stage {
			case "header":
				_, err = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: test\r\nX-Pending: ")
			case "body":
				_, err = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 10\r\n\r\nx")
			case "idle":
				_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
			}
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			if stage == "idle" {
				response, err := http.ReadResponse(reader, nil)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || string(body) != "ok" {
					t.Fatalf("healthy request failed: %s %v", body, err)
				}
			}
			// The server must finish/close the slow connection before the client deadline.
			_, err = io.ReadAll(reader)
			if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("server failed to enforce deadline")
			}
			if stage != "idle" {
				select {
				case <-handled:
					t.Fatal("incomplete request reached handler body")
				default:
				}
			}
		})
	}
}

func TestHTTPTimeoutWrite(t *testing.T) {
	var deadlines HTTPTimeoutsConfig
	if err := json.Unmarshal([]byte(`{"writeTimeout":"100ms"}`), &deadlines); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(250 * time.Millisecond)
		_, _ = w.Write([]byte("too late"))
	})
	server := httptest.NewUnstartedServer(handler)
	server.Config = newConfiguredHTTPServer("", handler, deadlines)
	server.Start()
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(server.URL)
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil {
		t.Fatal("expired write deadline allowed a response")
	}
}

func TestHTTPTimeoutWebSocketStaysOpen(t *testing.T) {
	f := newWebSocketCheckFixture(t, map[string]interface{}{"channel": map[string]interface{}{"script": "unused.js"}})
	handler := f.server.Config.Handler
	f.server.Close()
	var deadlines HTTPTimeoutsConfig
	if err := json.Unmarshal([]byte(`{"readTimeout":"100ms","writeTimeout":"100ms","idleTimeout":"100ms"}`), &deadlines); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config = newConfiguredHTTPServer("", handler, deadlines)
	server.Start()
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/channel", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(250 * time.Millisecond)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err = conn.WriteMessage(websocket.TextMessage, []byte(`{"api":"missing"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = conn.ReadMessage(); err != nil {
		t.Fatalf("HTTP deadline leaked into WebSocket: %v", err)
	}
}

func TestHTTPTimeoutH2C(t *testing.T) {
	for _, stage := range []string{"body", "write"} {
		t.Run(stage, func(t *testing.T) {
			var deadlines HTTPTimeoutsConfig
			if err := json.Unmarshal([]byte(`{"readTimeout":"100ms","writeTimeout":"100ms"}`), &deadlines); err != nil {
				t.Fatal(err)
			}
			handlerDone := make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-handlerDone:
				case <-time.After(3 * time.Second):
					t.Error("HTTP/2 handler did not stop")
				}
			})
			handler := receiveLimitH2CHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				if r.ProtoMajor != 2 {
					t.Error("expected HTTP/2")
				}
				if !enforceReceiveBodyLimit(w, r) {
					return
				}
				if stage == "write" {
					time.Sleep(250 * time.Millisecond)
				}
				_, _ = w.Write([]byte("late"))
			}), deadlines)
			server := httptest.NewUnstartedServer(handler)
			server.Config = newConfiguredHTTPServer("", handler, deadlines)
			server.Start()
			defer server.Close()
			transport := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			req, err := http.NewRequest("POST", server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "body" {
				reader, writer := io.Pipe()
				defer reader.Close()
				defer writer.Close()
				req.Body = reader
				req.ContentLength = 10
				go func() { _, _ = writer.Write([]byte("x")) }()
			}
			response, err := client.Do(req)
			if response != nil {
				defer response.Body.Close()
				if response.StatusCode < 400 {
					t.Fatalf("deadline allowed status %d", response.StatusCode)
				}
			}
			if err != nil {
				if e, ok := err.(net.Error); ok && e.Timeout() {
					t.Fatalf("client deadline fired instead of server: %v", err)
				}
			}
		})
	}
}

type admissionBodyProbe struct {
	reader        io.Reader
	reads, closed int
}

func (body *admissionBodyProbe) Read(p []byte) (int, error) {
	n, err := body.reader.Read(p)
	body.reads += n
	return n, err
}
func (body *admissionBodyProbe) Close() error { body.closed++; return nil }

func TestReceiveAdmissionBusyAndRateDoNotRead(t *testing.T) {
	for _, mode := range []string{"busy", "rate", "origin"} {
		t.Run(mode, func(t *testing.T) {
			handler, name := newReceiveAdmissionHandler(t, mode == "rate")
			if mode == "busy" {
				release, ok := acquireMCPExecutionSlot(name, 1)
				if !ok {
					t.Fatal("slot unavailable")
				}
				defer release()
			}
			makeRequest := func() *http.Request {
				req := httptest.NewRequest("POST", "https://service.example/"+name, nil)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("Origin", "https://client.example")
				req.ContentLength = 1 << 20
				req.Body = &admissionBodyProbe{reader: strings.NewReader(strings.Repeat("x", 1<<20))}
				return req
			}
			if mode == "rate" {
				handler.ServeHTTP(httptest.NewRecorder(), makeRequest())
			}
			req := makeRequest()
			want := 503
			if mode == "rate" {
				want = 429
			}
			if mode == "origin" {
				want = 403
				req.Header.Set("Origin", "https://untrusted.example")
			}
			probe := req.Body.(*admissionBodyProbe)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != want || probe.reads != 0 || probe.closed != 0 {
				t.Fatalf("status=%d read=%d close=%d", rec.Code, probe.reads, probe.closed)
			}
		})
	}
}

func TestReceiveAdmissionCORS(t *testing.T) {
	handler, name := newReceiveAdmissionHandler(t, false)
	for _, path := range []string{"/ordinary", "/nyan-rpc", "/" + name} {
		for _, unknown := range []bool{false, true} {
			req := httptest.NewRequest("POST", "https://service.example"+path, strings.NewReader(strings.Repeat("x", 65)))
			req.SetBasicAuth("audit", "audit")
			req.Header.Set("Origin", "https://client.example")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if unknown {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			origin := rec.Header().Get("Access-Control-Allow-Origin")
			if rec.Code != 413 || (origin != "*" && origin != "https://client.example") {
				t.Fatalf("%s unknown=%v status=%d CORS=%q", path, unknown, rec.Code, origin)
			}
		}
	}
}

func TestReceiveAdmissionOverflowDoesNotCloseBody(t *testing.T) {
	handler, name := newReceiveAdmissionHandler(t, false)
	for _, path := range []string{"/ordinary", "/" + name} {
		probe := &admissionBodyProbe{reader: strings.NewReader(strings.Repeat("x", 65))}
		req := httptest.NewRequest("POST", "https://service.example"+path, nil)
		req.Body = probe
		req.ContentLength = -1
		req.SetBasicAuth("audit", "audit")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 413 || probe.closed != 0 {
			t.Fatalf("%s status=%d close=%d", path, rec.Code, probe.closed)
		}
	}
}

func TestReceiveAdmissionChunkedStopsAtLimit(t *testing.T) {
	handler, name := newReceiveAdmissionHandler(t, false)
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, path := range []string{"/ordinary", "/" + name} {
		func() {
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			// No terminal chunk: the sender stops immediately after exceeding 64 bytes.
			_, err = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: service.example\r\nAuthorization: Basic YXVkaXQ6YXVkaXQ=\r\nOrigin: https://client.example\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n41\r\n%s\r\n", path, strings.Repeat("x", 65))
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatalf("%s waited for unread body: %v", path, err)
			}
			defer response.Body.Close()
			if _, err := io.ReadAll(response.Body); err != nil {
				t.Fatalf("413 response body stalled: %v", err)
			}
			if _, err := reader.ReadByte(); err != io.EOF {
				t.Fatalf("connection remains after 413; want EOF, got %v", err)
			}
			if response.StatusCode != 413 || response.Header.Get("Access-Control-Allow-Origin") == "" {
				t.Fatalf("%s response=%v", path, response)
			}
		}()
	}
}

func newReceiveAdmissionHandler(t *testing.T, rate bool) (http.Handler, string) {
	t.Helper()
	dir, defs := newMCPPhase12Definitions(t)
	name := strings.ReplaceAll(t.Name(), "/", "-")
	mcp := defs["custom-mcp"].(map[string]interface{})
	delete(defs, "custom-mcp")
	defs[name] = mcp
	mcp["maxConcurrent"] = 1
	mcp["allowedOrigins"] = []interface{}{"https://client.example"}
	if rate {
		mcp["rateLimit"] = map[string]interface{}{"requests": 1, "window": "1m"}
	}
	loaded, err := loadMCPPhase12Config(dir, defs)
	if err != nil {
		t.Fatal(err)
	}
	publishMCPPhase12Snapshot(t, loaded)
	globalConfig.ReceiveLimits.HTTPBodyBytes = 64
	router := gin.New()
	router.Use(CORSMiddleware(), receiveLimitMiddleware())
	router.NoRoute(func(c *gin.Context) { c.Status(404) })
	return receiveLimitH2CHandler(router, HTTPTimeoutsConfig{}), name
}

func TestReceiveAdmissionOAuthBusyDoesNotRead(t *testing.T) {
	handler, name := newReceiveAdmissionHandler(t, false)
	release, ok := acquireMCPExecutionSlot(name+":oauth:oauthToken", 1)
	if !ok {
		t.Fatal("slot unavailable")
	}
	defer release()
	req := httptest.NewRequest("POST", "https://service.example/oauth_token", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	probe := &admissionBodyProbe{reader: strings.NewReader(strings.Repeat("x", 1024))}
	req.Body = probe
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 503 || probe.reads != 0 || probe.closed != 0 {
		t.Fatalf("status=%d reads=%d closed=%d", rec.Code, probe.reads, probe.closed)
	}
}

func TestReceiveAdmissionH2CUpgradeBusyDoesNotRead(t *testing.T) {
	handler, name := newReceiveAdmissionHandler(t, false)
	release, ok := acquireMCPExecutionSlot(name, 1)
	if !ok {
		t.Fatal("slot unavailable")
	}
	defer release()
	req := httptest.NewRequest("POST", "https://service.example/"+name, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Connection", "Upgrade, HTTP2-Settings")
	req.Header.Set("Upgrade", "h2c")
	req.Header.Set("HTTP2-Settings", "")
	probe := &admissionBodyProbe{reader: strings.NewReader(strings.Repeat("x", 1024))}
	req.Body = probe
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 503 || probe.reads != 0 || probe.closed != 0 {
		t.Fatalf("status=%d reads=%d closed=%d", rec.Code, probe.reads, probe.closed)
	}
}

// Inspect the request at the upgrade boundary: keeping the buffered Body here
// retains the allocation for the lifetime of the WebSocket handler.
func TestReceiveLimitsWebSocketReleasesHandshakeBody(t *testing.T) {
	newWebSocketCheckFixture(t, map[string]interface{}{"channel": map[string]interface{}{"script": "unused.js"}})
	router := gin.New()
	router.Use(receiveLimitMiddleware())
	router.GET("/", handleWebSocket)
	var handler http.Handler = router
	oldCheck := upgrader.CheckOrigin
	t.Cleanup(func() { upgrader.CheckOrigin = oldCheck })
	reachedUpgrade := false
	upgrader.CheckOrigin = func(r *http.Request) bool {
		reachedUpgrade = true
		if r.Body != http.NoBody {
			t.Error("buffered HTTP body remains referenced at WebSocket upgrade")
		}
		return false // Stop before hijacking; no socket or GC timing is needed.
	}
	req := httptest.NewRequest("GET", "http://service.example/", strings.NewReader(strings.Repeat("x", 16<<20)))
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !reachedUpgrade {
		t.Fatalf("upgrade not reached: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReceiveAdmissionBusyRespondsWithoutBody(t *testing.T) {
	handler, name := newReceiveAdmissionHandler(t, false)
	release, ok := acquireMCPExecutionSlot(name, 1)
	if !ok {
		t.Fatal("slot unavailable")
	}
	defer release()
	assertEarlyBusyResponse(t, handler, "/"+name, "application/json")
}

func assertEarlyBusyResponse(t *testing.T, handler http.Handler, path, contentType string) {
	t.Helper()
	assertEarlyRejectionEOF(t, handler, path, contentType, http.StatusServiceUnavailable)
}

func assertEarlyRejectionEOF(t *testing.T, handler http.Handler, path, contentType string, status int) {
	t.Helper()
	for _, framing := range []string{"Content-Length: 32", "Transfer-Encoding: chunked"} {
		t.Run(framing, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			_, err = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: service.example\r\nContent-Type: %s\r\nAccept: application/json, text/event-stream\r\n%s\r\n\r\n", path, contentType, framing)
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatalf("early rejection waited for body: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != status || !response.Close {
				t.Fatalf("status=%d close=%v", response.StatusCode, response.Close)
			}
			if _, err := io.ReadAll(response.Body); err != nil {
				t.Fatalf("response body stalled: %v", err)
			}
			if _, err := reader.ReadByte(); err != io.EOF {
				t.Fatalf("connection remains after rejection; want EOF, got %v", err)
			}
		})
	}
}

func TestReceiveAdmissionOAuthBusyRespondsWithoutBody(t *testing.T) {
	handler, name := newReceiveAdmissionHandler(t, false)
	release, ok := acquireMCPExecutionSlot(name+":oauth:oauthToken", 1)
	if !ok {
		t.Fatal("slot unavailable")
	}
	defer release()
	assertEarlyBusyResponse(t, handler, "/oauth_token", "application/x-www-form-urlencoded")
}
func TestReceiveAdmissionWebSocketBusyDoesNotRead(t *testing.T) {
	handler, _ := newReceiveAdmissionHandler(t, false)
	for i := 0; i < webSocketMaxConnections(); i++ {
		release, ok := acquireWebSocketConnection(webSocketMaxConnections())
		if !ok {
			t.Fatal("slot unavailable")
		}
		defer release()
	}
	req := httptest.NewRequest("GET", "http://service.example/", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	probe := &admissionBodyProbe{reader: strings.NewReader(strings.Repeat("x", 1024))}
	req.Body = probe
	req.ContentLength = 1024
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 503 || probe.reads != 0 || rec.Header().Get("Connection") != "close" {
		t.Fatalf("status=%d reads=%d headers=%v", rec.Code, probe.reads, rec.Header())
	}
}

func TestReceiveAdmissionWebSocketSlotReleasedOnBodyRejection(t *testing.T) {
	handler, _ := newReceiveAdmissionHandler(t, false)
	globalConfig.WebSocket.MaxConnections = 1
	req := httptest.NewRequest("GET", "http://service.example/", strings.NewReader(strings.Repeat("x", 65)))
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("status=%d", rec.Code)
	}
	release, ok := acquireWebSocketConnection(1)
	if !ok {
		t.Fatal("connection slot leaked after body rejection")
	}
	release()
}

func TestExecutionModeDraftIdentifierReferences(t *testing.T) {
	for _, tc := range []struct {
		name, draft, identifier string
		preserve                bool
	}{
		{"draft4_anchor", "http://json-schema.org/draft-04/schema#", `"id":"#Input",`, true},
		{"draft4_resource", "http://json-schema.org/draft-04/schema#", `"id":"urn:test:input",`, true},
		{"draft7_anchor", "http://json-schema.org/draft-07/schema#", `"$id":"#Input",`, true},
		{"modern_id_annotation", "https://json-schema.org/draft/2020-12/schema", `"id":"#Input",`, false},
		{"draft4_data_id", "http://json-schema.org/draft-04/schema#", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var original map[string]interface{}
			raw := fmt.Sprintf(`{"$schema":%q,"$ref":"#/definitions/Input","definitions":{"Input":{%s"type":"object","properties":{"id":{"type":"integer"}},"required":["id"],"additionalProperties":false}}}`, tc.draft, tc.identifier)
			if err := json.Unmarshal([]byte(raw), &original); err != nil {
				t.Fatal(err)
			}
			if _, err := compileMCPJSONSchema(original); err != nil {
				t.Fatalf("original: %v", err)
			}
			before, _ := json.Marshal(original)
			normalized := normalizedMCPInputSchema(original)
			compiled, err := compileMCPJSONSchema(normalized)
			if err != nil {
				t.Fatalf("normalized: %v", err)
			}
			after, _ := json.Marshal(original)
			if !bytes.Equal(before, after) {
				t.Fatal("original mutated")
			}
			if !reflect.DeepEqual(normalized, normalizedMCPInputSchema(normalized)) {
				t.Fatal("normalization is not idempotent")
			}
			if tc.preserve && normalized["$ref"] != original["$ref"] {
				t.Fatal("identified resource was copied")
			}
			if err := compiled.Validate(map[string]interface{}{"id": 123}); err != nil {
				t.Fatalf("valid input rejected: %v", err)
			}
			if err := compiled.Validate(map[string]interface{}{"id": "wrong"}); err == nil {
				t.Fatal("invalid id accepted")
			}
			if err := compiled.Validate(map[string]interface{}{}); err == nil {
				t.Fatal("required id ignored")
			}
			err = compiled.Validate(map[string]interface{}{"id": 123, "nyan_mode": "checkOnly"})
			if (err != nil) != tc.preserve {
				t.Fatalf("mode extension changed: %v", err)
			}
		})
	}
}

func TestReceiveAdmissionRateRejectionClosesConnection(t *testing.T) {
	handler, name := newReceiveAdmissionHandler(t, true)
	seed := httptest.NewRequest("POST", "http://service.example/"+name, nil)
	seed.RemoteAddr = "127.0.0.1:1"
	seed.Header.Set("Content-Type", "application/json")
	seed.Header.Set("Accept", "application/json, text/event-stream")
	handler.ServeHTTP(httptest.NewRecorder(), seed)
	assertEarlyRejectionEOF(t, handler, "/"+name, "application/json", http.StatusTooManyRequests)
}
