package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// fakeOAuth 把 MCP 端点、受保护资源元数据、授权服务器和令牌端点放在同一个 httptest 服务上。
// 开关字段在发出第一个请求之前设置，之后只读。
type fakeOAuth struct {
	t   *testing.T
	srv *httptest.Server
	url string // 服务根，同时是 issuer

	noS256         bool // 元数据里不声明 S256
	noRegistration bool
	badIssuer      bool // 元数据里的 issuer 与取文档的地址不一致
	badResource    bool // 资源元数据里的 resource 不是 MCP 端点
	issParam       bool // 声明会在回调里带 iss
	noRotate       bool // 刷新时不返回新的 refresh_token
	open           bool // MCP 端点不要求认证

	mu           sync.Mutex
	registered   []map[string]any
	challenge    string // 登录时记下的 code_challenge，令牌端点用它校验 code_verifier
	redirect     string
	tokenForms   []url.Values
	refreshToken string
	accessToken  string // MCP 端点当前接受的令牌
	seq          int
	expiresIn    int
	insufficient bool // MCP 端点对有效令牌回 403 insufficient_scope
}

const fakeCode = "auth-code-1"

func newFakeOAuth(t *testing.T) *fakeOAuth {
	f := &fakeOAuth{t: t, issParam: true, expiresIn: 3600}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.url = f.srv.URL
	return f
}

func (f *fakeOAuth) mcpURL() string { return f.url + "/mcp" }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeOAuth) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/mcp":
		f.serveMCP(w, r)
	case "/.well-known/oauth-protected-resource/mcp":
		resource := f.mcpURL()
		if f.badResource {
			resource = "https://evil.example/mcp"
		}
		writeJSON(w, 200, map[string]any{"resource": resource, "authorization_servers": []string{f.url}, "scopes_supported": []string{"read", "write"}})
	case "/.well-known/oauth-authorization-server":
		meta := map[string]any{
			"issuer": f.url, "authorization_endpoint": f.url + "/authorize", "token_endpoint": f.url + "/token",
			"code_challenge_methods_supported":               []string{"S256"},
			"authorization_response_iss_parameter_supported": f.issParam,
		}
		if f.noS256 {
			meta["code_challenge_methods_supported"] = []string{"plain"}
		}
		if f.badIssuer {
			meta["issuer"] = "https://other.example"
		}
		if !f.noRegistration {
			meta["registration_endpoint"] = f.url + "/register"
		}
		writeJSON(w, 200, meta)
	case "/register":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.registered = append(f.registered, body)
		f.mu.Unlock()
		writeJSON(w, 201, map[string]any{"client_id": "cid"})
	case "/token":
		f.serveToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOAuth) serveMCP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	token, insufficient := f.accessToken, f.insufficient
	f.mu.Unlock()
	if f.open {
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		return
	}
	got := r.Header.Get("Authorization")
	if token == "" || got != "Bearer "+token {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp", scope="read"`, f.url))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if insufficient {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="write"`)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"ok": true}})
}

func (f *fakeOAuth) serveToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenForms = append(f.tokenForms, r.PostForm)
	fail := func(code string) { writeJSON(w, 400, map[string]any{"error": code, "error_description": "测试用"}) }
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		switch {
		case r.PostForm.Get("code") != fakeCode:
			fail("invalid_grant")
			return
		case base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge:
			fail("invalid_grant")
			return
		case r.PostForm.Get("redirect_uri") != f.redirect || r.PostForm.Get("resource") != f.mcpURL():
			fail("invalid_request")
			return
		}
	case "refresh_token":
		if r.PostForm.Get("refresh_token") != f.refreshToken || f.refreshToken == "" {
			fail("invalid_grant")
			return
		}
	default:
		fail("unsupported_grant_type")
		return
	}
	f.seq++
	f.accessToken = fmt.Sprintf("at-%d", f.seq)
	resp := map[string]any{"access_token": f.accessToken, "token_type": "Bearer", "expires_in": f.expiresIn, "scope": "read"}
	if !(f.noRotate && r.PostForm.Get("grant_type") == "refresh_token") {
		f.refreshToken = fmt.Sprintf("rt-%d", f.seq)
		resp["refresh_token"] = f.refreshToken
	}
	writeJSON(w, 200, resp)
}

// browser 扮演用户的浏览器：解析授权地址、记下 PKCE 参数，再带着 cb 里的参数访问回调。
// mutate 可以改回调参数，用来模拟各种攻击或出错的授权服务器。
func (f *fakeOAuth) browser(mutate func(q url.Values)) func(authURL string) error {
	return func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		q := u.Query()
		f.mu.Lock()
		f.challenge, f.redirect = q.Get("code_challenge"), q.Get("redirect_uri")
		f.mu.Unlock()
		cb := url.Values{"code": {fakeCode}, "state": {q.Get("state")}}
		if f.issParam {
			cb.Set("iss", f.url)
		}
		if mutate != nil {
			mutate(cb)
		}
		resp, err := http.Get(q.Get("redirect_uri") + "?" + cb.Encode())
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
}
