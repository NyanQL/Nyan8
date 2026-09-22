package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
	writeHotReloadTestFile(t, childPath, fmt.Sprintf(`{"read":{"script":%q}}`, commonPath))
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

func TestRuntimeFilePathsUseAPIConfigDirectory(t *testing.T) {
	f := newFilePathFixture(t)
	for _, tc := range []struct {
		name, api, path, want string
	}{
		{name: "root", api: "root", path: "./data.txt", want: "root-data"},
		{name: "included", api: "child/read", path: "data.txt", want: "child-data"},
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

func TestRuntimeFilePathsKeepSnapshotAndInitialAPIIdentity(t *testing.T) {
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

func TestRuntimeFilePathsNyanCallMeAndChecksUseCalleeConfig(t *testing.T) {
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
	assertFilePathResult(t, string(got.Normal), "child-data")
	if got.Before != "root-data" || got.After != "root-data" || got.Param.Result != "child-data" || got.Out.Status != 409 || got.Out.Result.File != base64.StdEncoding.EncodeToString([]byte("child-data")) || got.Out.Result.Body != "child-data" {
		t.Fatalf("nested/check helper paths = %s", result)
	}
}

func TestRuntimeFilePathsSendMailAttachmentPath(t *testing.T) {
	f := newFilePathFixture(t)
	host, port, smtpResult := newPhase5SMTPServer(t)
	globalConfig.SMTP = SMTPConfig{Host: host, Port: port, Username: "local-test", Password: "local-test", FromEmail: "sender@example.test"}
	script := filepath.Join(f.scriptDir, "mail.js")
	writeHotReloadTestFile(t, script, fmt.Sprintf(`nyanSendMail({to:"recipient@example.test",subject:"paths",body:"paths",attachments:[{path:"data.txt"},{path:%q}]})`, filepath.Join(f.rootDir, "data.txt")))
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
	if strings.Join(attachments, ",") != "child-data,root-data" {
		t.Fatalf("relative/absolute attachments = %#v", attachments)
	}
}

func TestRuntimeFilePathsPushUsesTargetConfigAndPreservesSourceAPI(t *testing.T) {
	f := newFilePathFixture(t)
	script := filepath.Join(f.scriptDir, "push.js")
	writeHotReloadTestFile(t, script, `JSON.stringify({api:nyanAllParams.api,text:nyanGetFile("data.txt")})`)
	definitions := map[string]interface{}{"root": map[string]interface{}{"push": "child/read"}, "child/read": map[string]interface{}{"script": script}}
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
	previousConnection, hadConnection := pushConnections.Load("child/read")
	pushConnections.Store("child/read", connection)
	t.Cleanup(func() {
		_ = connection.Close()
		if hadConnection {
			pushConnections.Store("child/read", previousConnection)
		} else {
			pushConnections.Delete("child/read")
		}
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
	if got.API != "root" || got.Text != "child-data" || params["api"] != "root" {
		t.Fatalf("push response=%s source params=%#v", response, params)
	}
}
