package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// A real upgraded connection exercises frame types and the message loop. Wait for
// hijacked handlers explicitly: httptest.Server.Close does not wait for them.
type websocketCheckFixture struct {
	t      *testing.T
	dir    string
	server *httptest.Server
	conns  []*websocket.Conn
}

func newWebSocketCheckFixture(t *testing.T, definitions map[string]interface{}) *websocketCheckFixture {
	t.Helper()
	previousSnapshot, previousConfig, previousPaths, previousLogger := currentAPISnapshot(), globalConfig, servicePaths, logger
	previousPush := map[interface{}]interface{}{}
	pushConnections.Range(func(key, value interface{}) bool {
		previousPush[key] = value
		pushConnections.Delete(key)
		return true
	})
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
		pushConnections.Range(func(key, _ interface{}) bool { pushConnections.Delete(key); return true })
		for key, value := range previousPush {
			pushConnections.Store(key, value)
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
	const allow = `({success:true,status:200,result:{checked:true}});`
	const denyParam = `({success:false,status:403,result:{message:"input blocked"}});`
	const denyOut = `({success:false,status:409,result:{message:"output blocked"}});`
	const mainBody = `{"success":false,"status":403,"value":"private result"}`
	tests := []struct {
		name, param, out, paramKey, outKey, wantOrder, wantBody string
		checkOnly, binary, missingParam, missingOut             bool
	}{
		{name: "allow failed main response", param: allow, out: allow, wantOrder: "param,main,out,push,", wantBody: mainBody},
		{name: "param denial", param: denyParam, out: allow, binary: true, wantOrder: "param,", wantBody: `{"success":false,"status":403,"result":{"message":"input blocked"}}`},
		{name: "param non-200", param: `({success:true,status:202,result:null});`, out: allow, wantOrder: "param,", wantBody: `{"success":true,"status":202,"result":null}`},
		{name: "output denial", param: allow, out: denyOut, binary: true, wantOrder: "param,main,out,", wantBody: `{"success":false,"status":409,"result":{"message":"output blocked"}}`},
		{name: "output non-200", param: allow, out: `({success:true,status:202,result:null});`, wantOrder: "param,main,out,", wantBody: `{"success":true,"status":202,"result":null}`},
		{name: "checkOnly", param: allow, out: allow, checkOnly: true, binary: true, wantOrder: "param,", wantBody: `{"success":true,"status":200,"result":{"checked":true}}`},
		{name: "checkOnly without param", out: allow, checkOnly: true, wantBody: `{"success":true,"status":200,"result":null}`},
		{name: "aliases", param: allow, out: allow, paramKey: "paramcheck", outKey: "outcheck", wantOrder: "param,main,out,push,", wantBody: mainBody},
		{name: "legacy check alias", param: denyParam, out: allow, paramKey: "check", wantOrder: "param,", wantBody: `{"success":false,"status":403,"result":{"message":"input blocked"}}`},
		{name: "param exception", param: `throw new Error("private exception");`, out: allow, binary: true, wantOrder: "param,", wantBody: `{"success":false,"status":500,"result":{"message":"Failed to run paramCheck"}}`},
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
				writeHotReloadTestFile(t, path, fmt.Sprintf(`nyanSetItem(%q,nyanGetItem(%q)+%q);`, key, key, name+",")+body)
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
			conn := f.dial("/target", nil)
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
			channel := writeScript("channel", `throw new Error("connection URL scripts must not execute");`)
			f := newWebSocketCheckFixture(t, map[string]interface{}{
				"channel":       map[string]interface{}{"script": channel, "paramCheck": channel, "outCheck": channel},
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
	conn := f.dial("/target", nil)
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
