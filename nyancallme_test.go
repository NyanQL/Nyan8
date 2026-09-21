package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/gin-gonic/gin"
)

func TestNyanCallMeRunsChecksInOrder(t *testing.T) {
	initTestLogger()
	gin.SetMode(gin.TestMode)
	const allow = `({success:true,status:200,result:{checked:true}});`
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
		{name: "param non-200", param: `({success:true,status:202,result:"pending"});`, out: allow, wantOrder: "param,", wantResult: `{"success":true,"status":202,"result":"pending"}`},
		{name: "output rejection", param: allow, out: denyOut, wantOrder: "param,main,out,", wantResult: `{"success":false,"status":409,"result":{"message":"output blocked"}}`},
		{name: "failed main response is checked", param: allow, out: denyOut, main: `JSON.stringify({success:false,status:403,value:"private result"});`, wantOrder: "param,main,out,", wantResult: `{"success":false,"status":409,"result":{"message":"output blocked"}}`},
		{name: "checkOnly", param: allow, out: allow, checkOnly: true, wantOrder: "param,", wantResult: `{"success":true,"status":200,"result":{"checked":true}}`},
		{name: "checkOnly without param checker", out: allow, checkOnly: true, wantOrder: "", wantResult: `{"success":true,"status":200,"result":null}`},
		{name: "lowercase aliases", param: allow, out: allow, paramKey: "paramcheck", outKey: "outcheck", wantOrder: "param,main,out,", wantResult: `{"status":201,"value":"private result"}`},
		{name: "legacy check alias", param: denyParam, out: allow, paramKey: "check", wantOrder: "param,", wantResult: `{"success":false,"status":403,"result":{"message":"input blocked"}}`},
		{name: "param exception", param: `throw new Error("param exploded");`, out: allow, wantOrder: "param,", wantError: "param exploded"},
		{name: "invalid param response", param: `({success:true});`, out: allow, wantOrder: "param,", wantError: "paramCheck response status must be a number"},
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
				writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,nyanGetItem(%q)+%q);`, key, key, name+",")+body)
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
