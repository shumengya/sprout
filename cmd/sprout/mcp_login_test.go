package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/mcp"
)

func TestParseMCPArgs(t *testing.T) {
	for _, argv := range [][]string{
		{"mcp", "login", "gh"},
		{"--data-dir", "D:/x", "mcp", "login", "gh"},
		{"mcp", "login", "--data-dir", "D:/x", "gh"},
		{"mcp", "login", "gh", "--data-dir", "D:/x"},
	} {
		a, err := parseArgs(argv)
		if err != nil || a.mcpAuth != "login" || a.mcpServer != "gh" {
			t.Errorf("%v: %+v %v", argv, a, err)
		}
		if strings.Contains(strings.Join(argv, " "), "D:/x") && a.dataDir != "D:/x" {
			t.Errorf("%v: dataDir = %q", argv, a.dataDir)
		}
	}
	if a, err := parseArgs([]string{"mcp", "logout", "gh"}); err != nil || a.mcpAuth != "logout" {
		t.Errorf("logout: %+v %v", a, err)
	}
	for _, argv := range [][]string{{"mcp"}, {"mcp", "list"}, {"mcp", "login"}, {"mcp", "login", "a", "b"}, {"mcp", "login", "  "}} {
		if _, err := parseArgs(argv); !errors.Is(err, errUsage) {
			t.Errorf("%v 应是用法错误，得到 %v", argv, err)
		}
	}
	if !strings.Contains(usage, "sprout mcp login") {
		t.Error("用法说明缺少 sprout mcp login")
	}
}

// miniOAuth 是一个最小的 OAuth 授权服务器 + 受保护的 MCP 端点，只覆盖正常流程；
// 各种异常分支在 internal/agent/mcp 的测试里。
func miniOAuth(t *testing.T) *httptest.Server {
	var challenge string
	var srv *httptest.Server
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"resource": srv.URL + "/mcp", "authorization_servers": []string{srv.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize", "token_endpoint": srv.URL + "/token",
			"code_challenge_methods_supported": []string{"S256"}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		challenge = r.URL.Query().Get("code_challenge")
		http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?code=c1&state="+url.QueryEscape(r.URL.Query().Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if r.PostForm.Get("code") != "c1" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.PostForm.Get("client_id") != "preset" {
			w.WriteHeader(http.StatusBadRequest)
			reply(w, map[string]any{"error": "invalid_grant"})
			return
		}
		reply(w, map[string]any{"access_token": "tok", "token_type": "Bearer", "refresh_token": "r"})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeMCPConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(config.MCPPath(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 登录、存令牌、退出：走真实的 mcp.jsonc 与令牌文件，浏览器换成直接 GET 授权地址（跟随重定向到本机回调）。
func TestMCPLoginLogout(t *testing.T) {
	srv := miniOAuth(t)
	dir := t.TempDir()
	t.Setenv("GH_CLIENT", "preset")
	writeMCPConfig(t, dir, `{"servers": {
		"gh": {"url": "`+srv.URL+`/mcp", "oauth": {"clientId": "${GH_CLIENT}"}},
		"local": {"command": "x"},
		"plain": {"url": "`+srv.URL+`/mcp"}
	}}`)
	browser := func(u string) error {
		resp, err := http.Get(u)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
	run := func(action, name string) (int, string, string) {
		var out, errb bytes.Buffer
		code := runMCPAuth(dir, action, name, os.Getenv, nil, browser, &out, &errb)
		return code, out.String(), errb.String()
	}

	code, out, errOut := run("login", "gh")
	if code != 0 || !strings.Contains(out, "已登录 gh") || !strings.Contains(out, srv.URL+"/authorize?") {
		t.Fatalf("登录: code=%d out=%q err=%q", code, out, errOut)
	}
	store := mcp.NewAuthStore(config.MCPAuthPath(dir))
	if !store.LoggedIn("gh", srv.URL+"/mcp") {
		t.Fatal("登录后应保存令牌")
	}
	data, _ := os.ReadFile(config.MCPAuthPath(dir))
	if !strings.Contains(string(data), `"tok"`) {
		t.Errorf("令牌文件内容: %s", data)
	}
	if b, _ := os.ReadFile(config.MCPVersionsPath(dir)); strings.Contains(string(b), "tok") {
		t.Error("令牌不能写进版本缓存")
	}

	for _, c := range []struct{ name, want string }{
		{"nope", "没有名为"},
		{"local", "stdio"},
		{"plain", `"oauth": {}`},
	} {
		if code, _, errOut := run("login", c.name); code == 0 || !strings.Contains(errOut, c.want) {
			t.Errorf("login %s: code=%d err=%q", c.name, code, errOut)
		}
	}

	if code, out, _ := run("logout", "gh"); code != 0 || !strings.Contains(out, "已退出") {
		t.Errorf("logout: %d %q", code, out)
	}
	if store.LoggedIn("gh", srv.URL+"/mcp") {
		t.Error("logout 后不该还有令牌")
	}
	if code, out, _ := run("logout", "gh"); code != 0 || !strings.Contains(out, "没有保存的登录") {
		t.Errorf("再次 logout: %d %q", code, out)
	}
}

// 配置了 oauth 的远程服务端连接时带上令牌来源；没配的、stdio 的不带。
func TestSpecTweakAttachesOAuth(t *testing.T) {
	a := &app{mcpAuth: mcp.NewAuthStore(filepath.Join(t.TempDir(), "mcp-auth.json"))}
	getenv := func(string) string { return "" }
	cases := []struct {
		name string
		srv  config.MCPServer
		want bool
	}{
		{"oauth", config.MCPServer{URL: "https://a.example/mcp", OAuth: &config.MCPOAuth{}}, true},
		{"plain", config.MCPServer{URL: "https://a.example/mcp"}, false},
	}
	for _, c := range cases {
		spec := mcpSpec(c.name, c.srv, getenv)
		a.specTweak()(&spec)
		if (spec.Auth != nil) != c.want {
			t.Errorf("%s: Auth = %v", c.name, spec.Auth)
		}
	}
}

func TestMCPConfigOAuthValidation(t *testing.T) {
	dir := t.TempDir()
	writeMCPConfig(t, dir, `{"servers": {
		"a": {"command": "x", "oauth": {}},
		"b": {"url": "https://a.example/mcp", "oauth": {"callbackPort": 70000}}
	}}`)
	_, err := config.LoadMCP(config.MCPPath(dir))
	if err == nil || !strings.Contains(err.Error(), "servers.a.oauth") || !strings.Contains(err.Error(), "callbackPort") {
		t.Errorf("得到 %v", err)
	}
}
