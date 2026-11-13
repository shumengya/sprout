package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/mcp"
	"github.com/shumengya/sprout/internal/agent/skills"
	"github.com/shumengya/sprout/internal/agent/tool"
)

func TestExpandEnv(t *testing.T) {
	getenv := func(k string) string {
		if k == "TOKEN" {
			return "s3cret"
		}
		return ""
	}
	cases := map[string]string{
		"Bearer ${TOKEN}":     "Bearer s3cret",
		"${TOKEN}${TOKEN}":    "s3crets3cret",
		"${MISSING}":          "",
		"no refs at all":      "no refs at all",
		"a ${1bad} b":         "a ${1bad} b", // 不是合法的变量名，原样保留
		"${TOKEN}${MISSING}x": "s3cretx",
	}
	for in, want := range cases {
		if got := expandEnv(in, getenv); got != want {
			t.Errorf("expandEnv(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestMCPSpecAppliesEnvHeadersAndTimeout(t *testing.T) {
	e := testEnv(map[string]string{"TOKEN": "abc"}, shellSet(true, true))
	spec := mcpSpec("srv", config.MCPServer{
		URL:            "https://x/mcp?k=${TOKEN}",
		Headers:        map[string]string{"Authorization": "Bearer ${TOKEN}"},
		Env:            map[string]string{"KEY": "${TOKEN}"},
		Args:           []string{"--token=${TOKEN}"},
		TimeoutSeconds: 7,
	}, e.getenv)

	if spec.URL != "https://x/mcp?k=abc" {
		t.Errorf("URL = %q", spec.URL)
	}
	if spec.Headers["Authorization"] != "Bearer abc" {
		t.Errorf("Headers = %v", spec.Headers)
	}
	if spec.Env["KEY"] != "abc" {
		t.Errorf("Env = %v", spec.Env)
	}
	if len(spec.Args) != 1 || spec.Args[0] != "--token=abc" {
		t.Errorf("Args = %v", spec.Args)
	}
	if spec.Timeout.Seconds() != 7 {
		t.Errorf("Timeout = %v", spec.Timeout)
	}
}

// fakeMCPServer 提供一个带 tools/list 与 tools/call 的旧版服务端。
func fakeMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply := func(result any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result})
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			reply(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "fake"}})
		case "tools/list":
			reply(map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "回显"},
				{"name": "secret", "description": "不该注册"},
			}})
		case "tools/call":
			reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID),
				"error": map[string]any{"code": -32601, "message": "不支持"}})
		}
	})
	return httptest.NewServer(mux)
}

func TestMCPToolsAreRegisteredWithPrefixAndExclusions(t *testing.T) {
	srv := fakeMCPServer(t)
	defer srv.Close()

	f := &config.MCPFile{Servers: map[string]config.MCPServer{
		"fake": {Enable: true, URL: srv.URL + "/mcp", ExcludeTools: []string{"secret"}},
	}}
	e := testEnv(nil, shellSet(true, true))
	set := connectMCP(context.Background(), f, e, slog.New(slog.DiscardHandler), mcp.LoadVersions(filepath.Join(t.TempDir(), "v.json")))
	if set == nil || len(set.servers) != 1 {
		t.Fatalf("应连接一个服务端: %+v", set)
	}
	if set.servers[0].Err != "" {
		t.Fatalf("连接失败: %s", set.servers[0].Err)
	}
	if set.servers[0].Tools != 1 {
		t.Errorf("被屏蔽的工具不该计入: Tools = %d", set.servers[0].Tools)
	}

	reg, notes := registerMCPInto(t, set, f)
	names := reg.Names()
	if strings.Join(names, ",") != "mcp__fake__echo" {
		t.Fatalf("注册的工具 = %v（%v）", names, notes)
	}
	tl, _ := reg.Get("mcp__fake__echo")
	if !strings.Contains(tl.Description(), "fake") {
		t.Errorf("描述应注明来源服务端: %q", tl.Description())
	}

	res, err := tl.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "ok") {
		t.Errorf("结果 = %q", res.Stdout)
	}
}

func TestMCPConnectionFailureIsRecordedNotFatal(t *testing.T) {
	f := &config.MCPFile{Servers: map[string]config.MCPServer{
		"dead": {Enable: true, Command: "definitely-not-a-real-binary-xyz"},
	}}
	set := connectMCP(context.Background(), f, testEnv(nil, shellSet(true, true)), slog.New(slog.DiscardHandler), mcp.LoadVersions(filepath.Join(t.TempDir(), "v.json")))
	if set == nil || len(set.servers) != 1 || set.servers[0].Err == "" {
		t.Fatalf("失败应记录在 servers 里: %+v", set)
	}
	if len(set.clients) != 0 {
		t.Errorf("失败的服务端不该留下客户端")
	}
}

func TestSkillRootsOrderAndHomeOmitted(t *testing.T) {
	root := t.TempDir()
	dataDir, cwd, home := filepath.Join(root, "data", ".sprout"), filepath.Join(root, "work", "proj"), filepath.Join(root, "home")

	roots := skillRoots(dataDir, cwd, home)
	var dirs []string
	for _, r := range roots {
		dirs = append(dirs, r.Dir)
	}
	want := []string{
		filepath.Join(cwd, ".sprout", "skills"),
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(dataDir, "skills"),
		filepath.Join(home, ".agents", "skills"),
	}
	if strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Errorf("技能目录顺序 = %v，期望 %v", dirs, want)
	}
	// 项目级排在用户级前面，冲突时项目级赢。
	if roots[0].Source != skills.SourceProject || roots[2].Source != skills.SourceUser {
		t.Errorf("来源 = %+v", roots)
	}
	if got := skillRoots(dataDir, cwd, ""); len(got) != 3 {
		t.Errorf("没有 home 时不该扫 ~/.agents/skills: %+v", got)
	}
	// cwd 不是绝对路径时不往上找项目技能（测试里的 cwd 常是相对路径）。
	if got := skillRoots(dataDir, "work/dir", home); len(got) != 2 {
		t.Errorf("相对 cwd 不扫项目级: %+v", got)
	}
}

func TestLoadSkillsCreatesDataDirAndFindsInstalled(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "skills", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: demo\ndescription: 演示技能。\n---\n正文\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	list, problems := loadSkills(dataDir, "", "")
	if len(problems) != 0 {
		t.Fatalf("不应有问题: %v", problems)
	}
	if len(list) != 1 || list[0].Name != "demo" {
		t.Fatalf("技能 = %+v", list)
	}
	if got := skillInfos(list); len(got) != 1 || got[0].Name != "demo" || got[0].Path != list[0].Path {
		t.Errorf("skillInfos = %v", got)
	}
}

func TestLoadMCPConfigCreatesTemplateOnce(t *testing.T) {
	dataDir := t.TempDir()
	f, err := loadMCPConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Servers) != 0 {
		t.Errorf("模板不该启用服务端: %+v", f.Servers)
	}
	if b, err := os.ReadFile(config.MCPPath(dataDir)); err != nil || !strings.Contains(string(b), "mcp__") {
		t.Errorf("应生成带说明的模板: %v", err)
	}
}

// registerMCPInto 把 MCP 工具注册进一个新的空注册表，返回注册表与提示。
func registerMCPInto(t *testing.T, set *mcpSet, f *config.MCPFile) (*tool.Registry, []string) {
	t.Helper()
	reg := tool.NewRegistry()
	notes := registerMCPTools(reg, set, f, func(tl tool.Tool, detail string) error {
		_ = detail
		return reg.Register(tl)
	}, slog.New(slog.DiscardHandler))
	return reg, notes
}

// 后台连接：buildApp 不等 MCP 就返回，连好后 applyMCP 把工具接进 agent，且对话历史不丢。
func TestBackgroundMCPIsAppliedAndKeepsHistory(t *testing.T) {
	model := newModelServer(t, "bash")
	mcpSrv := fakeMCPServer(t)
	defer mcpSrv.Close()

	dir := t.TempDir()
	writeServerConfig(t, dir, model.URL)
	cfg := `{"servers": {"fake": {"enable": true, "url": "` + mcpSrv.URL + `/mcp", "excludeTools": ["secret"]}}}`
	if err := os.WriteFile(config.MCPPath(dir), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := buildApp(dir, testEnv(nil, shellSet(true, true)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v / %s", err, a.disabled)
	}
	defer a.close()
	if len(a.info.MCP) != 1 || !a.info.MCP[0].Pending {
		t.Fatalf("启动时应先显示连接中: %+v", a.info.MCP)
	}
	// 连接期间 agent 已经可用，只是还没有 MCP 工具。
	if _, ok := a.reg.Get("mcp__fake__echo"); ok {
		t.Fatal("连好之前不该已经注册 MCP 工具")
	}
	before := a.ag
	before.Restore([]agent.Message{agent.NewTextMessage(agent.RoleUser, "早先的问题")}, "早先的摘要")

	a.waitMCP()
	info, notices := a.applyMCP()
	if len(notices) != 0 {
		t.Fatalf("不应有提示: %v", notices)
	}
	if len(info.MCP) != 1 || info.MCP[0].Pending || info.MCP[0].Err != "" || info.MCP[0].Tools != 1 {
		t.Fatalf("连好后的状态 = %+v", info.MCP)
	}
	if _, ok := a.reg.Get("mcp__fake__echo"); !ok {
		t.Fatalf("应注册 mcp__fake__echo: %v", a.reg.Names())
	}
	if a.ag == before {
		t.Fatal("增加工具后应重建 agent")
	}
	if h := a.ag.History(); len(h) != 1 || a.ag.Summary() != "早先的摘要" {
		t.Errorf("重建后历史 = %d 条，摘要 = %q", len(h), a.ag.Summary())
	}
	// 版本缓存写下来了，下次启动可以跳过 server/discover。
	if _, err := os.Stat(config.MCPVersionsPath(dir)); err != nil {
		t.Errorf("应写出版本缓存: %v", err)
	}
}
