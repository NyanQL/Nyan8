package main

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"os"
	"path/filepath"
)

// ========================================
// Nyan8 MCP対応エンドポイント群
// ========================================
// - /nyan                  : サーバー基本情報とAPI一覧
// - /nyan/:apiName         : 各APIの詳細仕様
// - /nyan-rpc              : JSON-RPC 2.0実行エンドポイント
// - /nyan-toolbox          : MCPクライアント用ツール一覧
// - /nyan-toolbox/:toolName (GET) : ツール仕様
// - /nyan-toolbox/:toolName (POST): ツール実行（内部で/nyan-rpcを呼び出し）
// ========================================

func main() {
	r := gin.Default()

	// 基本情報
	r.GET("/nyan", handleNyanInfo)

	// API詳細
	r.GET("/nyan/:apiName", handleNyanDetail)

	// JSON-RPC 2.0エンドポイント
	r.POST("/nyan-rpc", handleJSONRPC)

	// MCP対応ツールボックス一覧
	r.GET("/nyan-toolbox", handleNyanToolboxList)

	// MCP対応ツール詳細(GET) / 実行(POST)
	r.GET("/nyan-toolbox/:toolName", handleNyanToolboxDetail)
	r.POST("/nyan-toolbox/:toolName", handleNyanToolboxExecute)

	r.Run(fmt.Sprintf(":%d", config.Port))
}

/*
-------------------------------------------------
executeJSONRPC()
-------------------------------------------------
内部的に /nyan-rpc と同等の処理を行う共通関数。
外部HTTP呼び出しを介さず、Go内部でスクリプトを実行する。

【特徴】
- SSLやポート設定に依存しない
- /nyan-rpc, /nyan-toolbox の両方から利用される
- api.json の script パスを読み込み、Goja上で実行
-------------------------------------------------
*/
func executeJSONRPC(method string, params map[string]interface{}) (map[string]interface{}, error) {
	execDir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		return nil, fmt.Errorf("cannot get exec path: %v", err)
	}
	if isTemporaryDirectory(execDir) {
		if execDir, err = os.Getwd(); err != nil {
			return nil, fmt.Errorf("cannot get working directory: %v", err)
		}
	}

	apiJsonPath := filepath.Join(execDir, "api.json")
	scriptListData, err := loadJSONFile(apiJsonPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read api.json: %v", err)
	}

	scriptInfoRaw, ok := scriptListData[method]
	if !ok {
		return nil, fmt.Errorf("method not found: %s", method)
	}
	scriptInfo, ok := scriptInfoRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid format in api.json for %s", method)
	}

	scriptPath, ok := scriptInfo["script"].(string)
	if !ok {
		return nil, fmt.Errorf("script path not found for %s", method)
	}
	fullPath := filepath.Join(execDir, scriptPath)

	allParams := make(map[string]interface{})
	for k, v := range params {
		allParams[k] = v
	}
	allParams["api"] = method

	resultStr, err := runJavaScript(fullPath, allParams, nil)
	if err != nil {
		return nil, fmt.Errorf("script execution failed: %v", err)
	}

	var jsResult map[string]interface{}
	if err := json.Unmarshal([]byte(resultStr), &jsResult); err != nil {
		return nil, fmt.Errorf("failed to parse script result: %v", err)
	}

	return jsResult, nil
}

/*
-------------------------------------------------
handleJSONRPC()
-------------------------------------------------
JSON-RPC 2.0形式でのAPI呼び出しを受け付けるエンドポイント。
外部クライアント（例: ChatGPT MCPなど）向け。

【リクエスト例】
POST /nyan-rpc
{
  "jsonrpc": "2.0",
  "method": "add",
  "params": { "addNumber": 10 },
  "id": 1
}

【レスポンス例】
{
  "jsonrpc": "2.0",
  "result": { "result": 12, "status": 201, "success": true },
  "id": 1
}
-------------------------------------------------
*/
func handleJSONRPC(c *gin.Context) {
	var req struct {
		JSONRPC string                 `json:"jsonrpc"`
		Method  string                 `json:"method"`
		Params  map[string]interface{} `json:"params"`
		ID      interface{}            `json:"id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"jsonrpc": "2.0",
			"error":   "Invalid JSON-RPC request",
			"id":      nil,
		})
		return
	}

	result, err := executeJSONRPC(req.Method, req.Params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"jsonrpc": "2.0",
			"error":   err.Error(),
			"id":      req.ID,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"jsonrpc": "2.0",
		"result":  result,
		"id":      req.ID,
	})
}

/*
-------------------------------------------------
handleNyanToolboxExecute()
-------------------------------------------------
/nyan-toolbox/:toolName (POST)
内部的に executeJSONRPC() を呼び出し、APIを実行する。

外部HTTP呼び出しを行わないため、SSL・ポートに依存せず動作。
MCPクライアントがこのエンドポイントを呼ぶと
ツールの実行が可能になる。

【リクエスト例】
POST /nyan-toolbox/add
{ "addNumber": 10 }

【レスポンス例】
{
  "success": true,
  "status": 200,
  "result": {
    "tool": "add",
    "input": { "addNumber": 10 },
    "output": { "result": 12, "status": 201, "success": true },
    "description": "2に対して足し算した結果を返します。"
  }
}
-------------------------------------------------
*/
func handleNyanToolboxExecute(c *gin.Context) {
	toolName := c.Param("toolName")

	var params map[string]interface{}
	if err := c.ShouldBindJSON(&params); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"status":  400,
			"error":   "Invalid JSON body",
		})
		return
	}

	result, err := executeJSONRPC(toolName, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"status":  500,
			"error":   err.Error(),
		})
		return
	}

	execDir, _ := os.Getwd()
	apiJsonPath := filepath.Join(execDir, "api.json")
	apiConf, _ := loadJSONFile(apiJsonPath)
	desc := ""
	if api, ok := apiConf[toolName].(map[string]interface{}); ok {
		if d, ok := api["description"].(string); ok {
			desc = d
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"status":  200,
		"result": gin.H{
			"tool":        toolName,
			"input":       params,
			"output":      result,
			"description": desc,
		},
	})
}

/*
-------------------------------------------------
 handleNyanToolboxDetail()
-------------------------------------------------
/nyan-toolbox/:toolName (GET)
ツールの詳細仕様（説明・パラメータ・戻り値）を返す。

MCPクライアントがこの情報をもとに、
「どのように使えるツールか」を理解できるようになる。
-------------------------------------------------
*/
func handleNyanToolboxDetail(c *gin.Context) {
	toolName := c.Param("toolName")

	execDir, _ := os.Getwd()
	apiJsonPath := filepath.Join(execDir, "api.json")
	apiConf, _ := loadJSONFile(apiJsonPath)

	desc := ""
	if api, ok := apiConf[toolName].(map[string]interface{}); ok {
		if d, ok := api["description"].(string); ok {
			desc = d
		}
	}

	// 通常は /nyan/:apiName の情報を再利用してもOK
	c.JSON(http.StatusOK, gin.H{
		"tool":        toolName,
		"description": desc,
		"parameters":  gin.H{"sample": 1},
		"outputs":     []string{"result"},
		"kind":        "js",
	})
}

func handleNyanDetail(c *gin.Context) {
}

// /nyan-toolbox ハンドラ（一覧）
func handleNyanToolboxList(c *gin.Context) {
	apiData, _ := loadJSONFile("api.json")
	tools := []gin.H{}
	for name, v := range apiData {
		item := gin.H{"tool": name}
		if m, ok := v.(map[string]interface{}); ok {
			if desc, ok := m["description"].(string); ok {
				item["description"] = desc
			}
		}
		item["kind"] = "js"
		tools = append(tools, item)
	}
	c.JSON(http.StatusOK, gin.H{
		"tools": tools,
	})
}
