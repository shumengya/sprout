package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// stdioServerEnv 让测试二进制在子进程里充当 MCP 服务端：
// 这样测的是真实的管道读写，而不是内存里的假实现。
const stdioServerEnv = "SPROUT_MCP_TEST_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(stdioServerEnv) == "1" {
		runFakeStdioServer()
		return
	}
	os.Exit(m.Run())
}

// runFakeStdioServer 是一个行式 JSON-RPC 服务端：支持 initialize / tools/list / tools/call，
// 并把收到的每条方法名写到 stderr，供测试确认握手过程。
func runFakeStdioServer() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	out := bufio.NewWriter(os.Stdout)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		var result any
		var rerr *rpcError
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake-stdio", "version": "1.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "回显"},
				{"name": "fail", "description": "总是失败"},
			}}
		case "tools/call":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name == "fail" {
				result = map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "出错了"}}}
			} else {
				result = map[string]any{"content": []map[string]any{{"type": "text", "text": "pong"}}}
			}
		case "notifications/initialized", "notifications/cancelled":
			continue // 通知不需要响应
		default:
			rerr = &rpcError{Code: -32601, Message: "不支持 " + req.Method}
		}
		msg := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID)}
		if rerr != nil {
			msg["error"] = map[string]any{"code": rerr.Code, "message": rerr.Message}
		} else {
			msg["result"] = result
		}
		data, _ := json.Marshal(msg)
		out.Write(data)
		out.WriteByte('\n')
		out.Flush()
	}
}

// stdioSpec 让当前测试二进制以假服务端的方式启动。
func stdioSpec() Spec {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return Spec{
		Name:    "fake",
		Command: exe,
		Env:     map[string]string{stdioServerEnv: "1"},
		Timeout: 10 * time.Second,
	}
}

func TestDialStdioAndCall(t *testing.T) {
	c, err := Dial(context.Background(), stdioSpec(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c.Version() != "2025-06-18" {
		t.Errorf("版本 = %q", c.Version())
	}
	if c.ServerInfo().Name != "fake-stdio" {
		t.Errorf("服务端信息 = %+v", c.ServerInfo())
	}
	if n := len(c.Tools()); n != 2 {
		t.Fatalf("工具数 = %d", n)
	}

	res, err := c.Call(context.Background(), "echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "pong" {
		t.Errorf("结果 = %+v", res.Content)
	}

	// 工具层面的失败走 IsError，不是连接错误。
	res, err = c.Call(context.Background(), "fail", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("工具失败不该是调用错误: %v", err)
	}
	if !res.IsError {
		t.Error("IsError 应为 true")
	}
}

func TestDialStdioMissingCommand(t *testing.T) {
	if _, err := Dial(context.Background(), Spec{Name: "x"}, nil, nil); err == nil {
		t.Fatal("没有 command 也没有 url 应报错")
	}
}

func TestDialStdioCommandNotFound(t *testing.T) {
	_, err := Dial(context.Background(), Spec{
		Name: "nope", Command: "definitely-not-a-real-binary-xyz", Timeout: 5 * time.Second,
	}, nil, nil)
	if err == nil {
		t.Fatal("命令不存在应报错")
	}
}

func TestStdioTimeoutIsReported(t *testing.T) {
	// 用一个只会读、从不回应的进程模拟卡住的服务端：
	// 让测试二进制在“服务端”模式里对未知方法直接忽略是不行的，这里改成启动一个真正的空命令。
	c, err := Dial(context.Background(), Spec{
		Name: "sleeper", Command: os.Args[0], Args: []string{"-test.run=TestHelperSleeper"},
		Env: map[string]string{"SPROUT_MCP_TEST_SLEEPER": "1"}, Timeout: time.Second,
	}, nil, nil)
	if err == nil {
		c.Close()
		t.Fatal("服务端不回应时应超时失败")
	}
	if !strings.Contains(err.Error(), "超时") && !strings.Contains(err.Error(), "取消") {
		t.Errorf("错误应说明超时: %v", err)
	}
}

// TestHelperSleeper 在被 -test.run 选中时睡住不回任何消息；平时立即返回。
func TestHelperSleeper(t *testing.T) {
	if os.Getenv("SPROUT_MCP_TEST_SLEEPER") != "1" {
		t.Skip("只在子进程里运行")
	}
	fmt.Print("") // 保持 stdout 打开
	time.Sleep(30 * time.Second)
}
