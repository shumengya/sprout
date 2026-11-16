package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resourceServer 是声明了 resources 能力的旧版服务端：
// resources/list 分两页，resources/templates/list 返回 -32601，resources/read 按 URI 返回内容。
func resourceServer(t *testing.T, contents map[string][]ResourceContent) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
				URI    string `json:"uri"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "server/discover":
			writeError(t, w, req.ID, -32601, "不支持")
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-r")
			writeResult(t, w, req.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"resources": map[string]any{}},
				"serverInfo":      map[string]any{"name": "res", "version": "1.0"},
			})
		case "tools/list":
			writeResult(t, w, req.ID, map[string]any{"tools": []any{}})
		case "resources/list":
			if req.Params.Cursor == "" {
				writeResult(t, w, req.ID, map[string]any{
					"resources":  []map[string]any{{"uri": "file:///a.txt", "name": "a", "mimeType": "text/plain"}},
					"nextCursor": "p2",
				})
				return
			}
			writeResult(t, w, req.ID, map[string]any{
				"resources": []map[string]any{{"uri": "file:///b.png", "title": "截图", "mimeType": "image/png", "size": 12}},
			})
		case "resources/templates/list":
			writeError(t, w, req.ID, -32601, "不支持")
		case "resources/read":
			c, ok := contents[req.Params.URI]
			if !ok {
				writeError(t, w, req.ID, -32002, "没有这个资源")
				return
			}
			writeResult(t, w, req.ID, map[string]any{"contents": c})
		default:
			writeError(t, w, req.ID, -32601, "不支持 "+req.Method)
		}
	})
	return httptest.NewServer(mux)
}

func TestListAndReadResources(t *testing.T) {
	png := pngBase64(t)
	srv := resourceServer(t, map[string][]ResourceContent{
		"file:///a.txt": {{URI: "file:///a.txt", MimeType: "text/plain", Text: "第一行\n第二行"}},
		"file:///b.png": {{URI: "file:///b.png", MimeType: "image/png", Blob: png}},
		"file:///bad":   {{URI: "file:///bad", MimeType: "application/octet-stream", Blob: base64.StdEncoding.EncodeToString([]byte("not an image"))}},
	})
	defer srv.Close()

	c, err := Dial(context.Background(), Spec{Name: "res", URL: srv.URL + "/mcp"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if !c.HasResources() {
		t.Fatal("服务端声明了 resources 能力，HasResources 应为 true")
	}
	tools := ResourceTools(c)
	if len(tools) != 2 {
		t.Fatalf("资源工具 = %d 个，期望 2", len(tools))
	}

	res, err := tools[0].Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"资源（2 个）", "file:///a.txt", "file:///b.png（截图）", "[image/png]"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("列表缺少 %q:\n%s", want, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, "资源模板") {
		t.Errorf("templates/list 返回 -32601，不应出现模板段:\n%s", res.Stdout)
	}

	got, err := tools[1].Run(context.Background(), json.RawMessage(`{"uri":"file:///a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Stdout != "第一行\n第二行\n" || len(got.Images) != 0 {
		t.Errorf("文本资源 = %q，图片 %d 张", got.Stdout, len(got.Images))
	}

	got, err = tools[1].Run(context.Background(), json.RawMessage(`{"uri":"file:///b.png"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images) != 1 || got.Images[0].Data != png {
		t.Errorf("图片资源 = %+v", got.Images)
	}
	if !strings.Contains(got.Stdout, "[图片 1：image/png，已附在结果后]") {
		t.Errorf("图片占位文本缺失: %q", got.Stdout)
	}

	got, err = tools[1].Run(context.Background(), json.RawMessage(`{"uri":"file:///bad"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images) != 0 || !strings.Contains(got.Stdout, "不是支持的格式") {
		t.Errorf("非图片的二进制应只在文本里说明: %q，图片 %d 张", got.Stdout, len(got.Images))
	}

	if _, err := tools[1].Run(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("缺少 uri 时应返回错误")
	}
	if _, err := c.ReadResource(context.Background(), "file:///missing"); err == nil {
		t.Error("服务端报错时应返回错误")
	}
}

func TestNoResourcesCapability(t *testing.T) {
	var calls []string
	srv := legacyServer(t, &calls)
	defer srv.Close()

	c, err := Dial(context.Background(), Spec{Name: "fake", URL: srv.URL + "/mcp"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.HasResources() {
		t.Error("只声明 tools 能力的服务端，HasResources 应为 false")
	}
	if got := ResourceTools(c); got != nil {
		t.Errorf("没声明能力时不应注册资源工具，得到 %d 个", len(got))
	}
}

func TestStatelessResourcesCapability(t *testing.T) {
	srv := statelessServer(t)
	defer srv.Close()

	c, err := Dial(context.Background(), Spec{Name: "new", URL: srv.URL + "/mcp"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.HasResources() {
		t.Error("无状态服务端只声明 tools，HasResources 应为 false")
	}
}

func TestRenderResourceMultipleContents(t *testing.T) {
	text, imgs := renderResource([]ResourceContent{
		{URI: "file:///a", Text: "甲"},
		{URI: "file:///b", Text: "乙"},
	})
	if !strings.Contains(text, "--- file:///a ---\n甲\n") || !strings.Contains(text, "--- file:///b ---\n乙\n") {
		t.Errorf("多段内容应分段标注: %q", text)
	}
	if len(imgs) != 0 {
		t.Errorf("纯文本不应产生图片")
	}
	if text, _ := renderResource(nil); text != "（资源没有内容）\n" {
		t.Errorf("空内容 = %q", text)
	}
}
