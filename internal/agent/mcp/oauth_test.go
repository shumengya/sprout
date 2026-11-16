package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *AuthStore {
	return NewAuthStore(filepath.Join(t.TempDir(), "mcp-auth.json"))
}

func openWith(fn func(string) error) func(context.Context, string) error {
	return func(_ context.Context, u string) error { return fn(u) }
}

func login(t *testing.T, f *fakeOAuth, store *AuthStore, spec OAuthSpec, mutate func(url.Values)) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return Login(ctx, store, "fs", f.mcpURL(), spec, nil, openWith(f.browser(mutate)))
}

// 完整流程：动态注册、PKCE、resource 参数、iss 校验，令牌存盘后用于 MCP 请求。
func TestLoginFullFlow(t *testing.T) {
	f := newFakeOAuth(t)
	store := testStore(t)
	if err := login(t, f, store, OAuthSpec{}, nil); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	reg, forms := f.registered, f.tokenForms
	f.mu.Unlock()
	if len(reg) != 1 || reg[0]["token_endpoint_auth_method"] != "none" {
		t.Errorf("应动态注册一个公开客户端: %v", reg)
	}
	if uris, _ := reg[0]["redirect_uris"].([]any); len(uris) != 1 || !strings.HasPrefix(uris[0].(string), "http://127.0.0.1:") {
		t.Errorf("redirect_uris = %v", reg[0]["redirect_uris"])
	}
	if len(forms) != 1 || forms[0].Get("client_id") != "cid" || forms[0].Get("resource") != f.mcpURL() {
		t.Errorf("令牌请求 = %v", forms)
	}

	e, ok, err := store.get("fs", f.mcpURL())
	if err != nil || !ok || e.AccessToken != "at-1" || e.RefreshToken != "rt-1" || e.ClientID != "cid" || e.Issuer != f.url {
		t.Fatalf("存下的登录状态 = %+v ok=%v err=%v", e, ok, err)
	}
	if time.Until(e.Expiry) < 50*time.Minute {
		t.Errorf("过期时间应约为一小时后: %v", e.Expiry)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(store.path); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("令牌文件权限应为 0600: %v %v", st.Mode(), err)
		}
	}

	tr := newHTTPTransport(Spec{Name: "fs", URL: f.mcpURL(), Auth: NewTokenSource(store, "fs", f.mcpURL(), nil)}, &http.Client{})
	res, err := tr.request(context.Background(), "ping", "", nil)
	if err != nil || string(res) != `{"ok":true}` {
		t.Fatalf("带令牌的请求: %s %v", res, err)
	}
}

// 授权请求里的参数：S256、state、resource、scope 取自 401 的 challenge。
func TestLoginAuthorizationParams(t *testing.T) {
	f := newFakeOAuth(t)
	var got url.Values
	open := func(u string) error {
		p, _ := url.Parse(u)
		got = p.Query()
		return f.browser(nil)(u)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Login(ctx, testStore(t), "fs", f.mcpURL(), OAuthSpec{}, nil, openWith(open)); err != nil {
		t.Fatal(err)
	}
	if got.Get("code_challenge_method") != "S256" || got.Get("response_type") != "code" || got.Get("resource") != f.mcpURL() {
		t.Errorf("授权参数 = %v", got)
	}
	if got.Get("scope") != "read" || len(got.Get("state")) < 32 || len(got.Get("code_challenge")) != 43 {
		t.Errorf("scope/state/challenge = %v", got)
	}

	// 配置的 scopes 优先于 challenge。
	if err := Login(ctx, testStore(t), "fs", f.mcpURL(), OAuthSpec{Scopes: []string{"a", "b"}}, nil, openWith(open)); err != nil {
		t.Fatal(err)
	}
	if got.Get("scope") != "a b" {
		t.Errorf("scope = %q", got.Get("scope"))
	}
}

// 第二次登录沿用之前注册的 client_id；配置里写了 clientId 时不注册。
func TestLoginReusesClient(t *testing.T) {
	f := newFakeOAuth(t)
	store := testStore(t)
	for range 2 {
		if err := login(t, f, store, OAuthSpec{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := login(t, f, testStore(t), OAuthSpec{ClientID: "preset"}, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.registered) != 1 {
		t.Errorf("应只注册一次，实际 %d 次", len(f.registered))
	}
	if last := f.tokenForms[len(f.tokenForms)-1]; last.Get("client_id") != "preset" {
		t.Errorf("应使用配置的 clientId: %v", last)
	}
}

func TestLoginRejectsUnsafeServers(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeOAuth)
		want  string
	}{
		{"不支持 S256", func(f *fakeOAuth) { f.noS256 = true }, "S256"},
		{"issuer 不一致", func(f *fakeOAuth) { f.badIssuer = true }, "issuer"},
		{"resource 不一致", func(f *fakeOAuth) { f.badResource = true }, "resource"},
		{"不能动态注册又没配 clientId", func(f *fakeOAuth) { f.noRegistration = true }, "clientId"},
		{"服务端不要求认证", func(f *fakeOAuth) { f.open = true }, "不需要登录"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeOAuth(t)
			c.setup(f)
			store := testStore(t)
			err := login(t, f, store, OAuthSpec{}, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("想要包含 %q 的错误，得到 %v", c.want, err)
			}
			if store.LoggedIn("fs", f.mcpURL()) {
				t.Error("失败的登录不该留下令牌")
			}
		})
	}
}

// 回调参数的校验：iss 错了或缺了、授权服务器报错、没有授权码。
func TestLoginRejectsBadCallbacks(t *testing.T) {
	cases := []struct {
		name     string
		issParam bool
		mutate   func(url.Values)
		want     string
	}{
		{"iss 是别家", true, func(q url.Values) { q.Set("iss", "https://evil.example") }, "iss"},
		{"声明了 iss 却没带", true, func(q url.Values) { q.Del("iss") }, "iss"},
		{"没声明 iss 但带了别家的", false, func(q url.Values) { q.Set("iss", "https://evil.example") }, "iss"},
		{"授权服务器报错", true, func(q url.Values) { q.Del("code"); q.Set("error", "access_denied") }, "access_denied"},
		{"没有授权码", true, func(q url.Values) { q.Del("code") }, "授权码"},
		{"授权码不对", true, func(q url.Values) { q.Set("code", "forged") }, "invalid_grant"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeOAuth(t)
			f.issParam = c.issParam
			err := login(t, f, testStore(t), OAuthSpec{}, c.mutate)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("想要包含 %q 的错误，得到 %v", c.want, err)
			}
		})
	}
	// 没声明 iss、也没带 iss：允许。
	f := newFakeOAuth(t)
	f.issParam = false
	if err := login(t, f, testStore(t), OAuthSpec{}, nil); err != nil {
		t.Errorf("没声明 iss 的授权服务器应能登录: %v", err)
	}
}

// state 不对或 Host 不是回环地址的回调被拒绝，且不结束登录：之后正确的回调仍然有效。
func TestCallbackIgnoresForgedRequests(t *testing.T) {
	f := newFakeOAuth(t)
	open := func(authURL string) error {
		u, _ := url.Parse(authURL)
		redirect := u.Query().Get("redirect_uri")
		resp, err := http.Get(redirect + "?code=x&state=wrong")
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("state 不对应返回 400，得到 %d", resp.StatusCode)
		}
		req, _ := http.NewRequest(http.MethodGet, redirect+"?code=x&state="+u.Query().Get("state"), nil)
		req.Host = "evil.example"
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Host 不对应返回 400，得到 %d", resp.StatusCode)
		}
		return f.browser(nil)(authURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Login(ctx, testStore(t), "fs", f.mcpURL(), OAuthSpec{}, nil, openWith(open)); err != nil {
		t.Fatal(err)
	}
}

// 用户一直不回来：取消 ctx 后 Login 返回，不会挂住。
func TestLoginCancelled(t *testing.T) {
	f := newFakeOAuth(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Login(ctx, testStore(t), "fs", f.mcpURL(), OAuthSpec{}, nil, openWith(func(string) error { cancel(); return nil }))
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("得到 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Login 没有返回")
	}
}

// 临近过期时静默刷新；服务端轮换 refresh_token 时存下新的，不轮换时沿用旧的。
func TestTokenSourceRefresh(t *testing.T) {
	for _, rotate := range []bool{true, false} {
		f := newFakeOAuth(t)
		f.expiresIn = 30 // 比 refreshSkew 短，下一次取令牌就要刷新
		f.noRotate = !rotate
		store := testStore(t)
		if err := login(t, f, store, OAuthSpec{}, nil); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.expiresIn = 3600
		f.mu.Unlock()

		ts := NewTokenSource(store, "fs", f.mcpURL(), nil)
		// 并发取令牌只刷新一次。
		var wg sync.WaitGroup
		tokens := make([]string, 5)
		for i := range tokens {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tokens[i], _ = ts.Token(context.Background())
			}()
		}
		wg.Wait()
		for _, tok := range tokens {
			if tok != "at-2" {
				t.Fatalf("rotate=%v 令牌 = %v", rotate, tokens)
			}
		}
		f.mu.Lock()
		var refreshes int
		for _, form := range f.tokenForms {
			if form.Get("grant_type") == "refresh_token" {
				refreshes++
				if form.Get("refresh_token") != "rt-1" || form.Get("client_id") != "cid" || form.Get("resource") != f.mcpURL() {
					t.Errorf("刷新请求 = %v", form)
				}
			}
		}
		f.mu.Unlock()
		if refreshes != 1 {
			t.Errorf("rotate=%v 应只刷新一次，实际 %d 次", rotate, refreshes)
		}
		e, _, _ := store.get("fs", f.mcpURL())
		want := "rt-1"
		if rotate {
			want = "rt-2"
		}
		if e.RefreshToken != want || e.AccessToken != "at-2" {
			t.Errorf("rotate=%v 存下的 = %+v", rotate, e)
		}
	}
}

// 刷新令牌失效：清掉令牌、保留 client_id，提示重新登录。
func TestTokenSourceInvalidGrant(t *testing.T) {
	f := newFakeOAuth(t)
	f.expiresIn = 30
	store := testStore(t)
	if err := login(t, f, store, OAuthSpec{}, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.refreshToken = "revoked"
	f.mu.Unlock()
	_, err := NewTokenSource(store, "fs", f.mcpURL(), nil).Token(context.Background())
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "sprout mcp login fs") {
		t.Fatalf("得到 %v", err)
	}
	e, ok, _ := store.get("fs", f.mcpURL())
	if !ok || e.AccessToken != "" || e.RefreshToken != "" || e.ClientID != "cid" {
		t.Errorf("应清掉令牌、保留 client_id: %+v", e)
	}
	// 再取一次不再请求令牌端点。
	f.mu.Lock()
	n := len(f.tokenForms)
	f.mu.Unlock()
	if _, err := NewTokenSource(store, "fs", f.mcpURL(), nil).Token(context.Background()); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("得到 %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tokenForms) != n {
		t.Error("没有刷新令牌时不该再请求令牌端点")
	}
}

func TestTokenSourceNotLoggedIn(t *testing.T) {
	store := testStore(t)
	_, err := NewTokenSource(store, "fs", "https://a.example/mcp", nil).Token(context.Background())
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "sprout mcp login fs") {
		t.Errorf("得到 %v", err)
	}
	// 登录过但配置里的 URL 换了：旧令牌不能发给新地址。
	if err := store.put("fs", authEntry{URL: "https://a.example/mcp", AccessToken: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTokenSource(store, "fs", "https://b.example/mcp", nil).Token(context.Background()); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("URL 变了应要求重新登录: %v", err)
	}
	if tok, err := NewTokenSource(store, "fs", "https://a.example/mcp", nil).Token(context.Background()); err != nil || tok != "t" {
		t.Errorf("没有过期时间的令牌直接用: %q %v", tok, err)
	}
}

func TestAuthStoreLogoutAndCorruption(t *testing.T) {
	store := testStore(t)
	if had, err := store.Logout("fs"); err != nil || had {
		t.Errorf("没登录时 Logout: %v %v", had, err)
	}
	_ = store.put("fs", authEntry{URL: "u", AccessToken: "t"})
	_ = store.put("gh", authEntry{URL: "v", AccessToken: "s"})
	if had, err := store.Logout("fs"); err != nil || !had {
		t.Errorf("Logout: %v %v", had, err)
	}
	if store.LoggedIn("fs", "u") || !store.LoggedIn("gh", "v") {
		t.Error("Logout 只删指定的服务端")
	}
	if err := os.WriteFile(store.path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.get("gh", "v"); err == nil || !strings.Contains(err.Error(), "重新登录") {
		t.Errorf("文件损坏应报错: %v", err)
	}
	if err := store.put("gh", authEntry{}); err == nil {
		t.Error("文件损坏时不该覆盖写入")
	}
}

// 认证失败时的提示要告诉用户下一步怎么做。
func TestHTTPAuthFailures(t *testing.T) {
	f := newFakeOAuth(t)
	store := testStore(t)

	// 没配 oauth、服务端要 Bearer：提示加 oauth 段。
	tr := newHTTPTransport(Spec{Name: "fs", URL: f.mcpURL()}, &http.Client{})
	if _, err := tr.request(context.Background(), "ping", "", nil); err == nil || !strings.Contains(err.Error(), `"oauth"`) {
		t.Errorf("未配置 oauth: %v", err)
	}
	// 配了 oauth 但还没登录：不发请求，直接要求登录。
	tr = newHTTPTransport(Spec{Name: "fs", URL: f.mcpURL(), Auth: NewTokenSource(store, "fs", f.mcpURL(), nil)}, &http.Client{})
	if _, err := tr.request(context.Background(), "ping", "", nil); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("未登录: %v", err)
	}

	if err := login(t, f, store, OAuthSpec{}, nil); err != nil {
		t.Fatal(err)
	}
	// 服务端撤销了令牌：401 提示重新登录。
	f.mu.Lock()
	f.accessToken = "rotated-server-side"
	f.mu.Unlock()
	if _, err := tr.request(context.Background(), "ping", "", nil); err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "sprout mcp login fs") {
		t.Errorf("令牌被拒: %v", err)
	}
	// 权限不足：403 insufficient_scope。
	f.mu.Lock()
	f.accessToken, f.insufficient = "at-1", true
	f.mu.Unlock()
	if _, err := tr.request(context.Background(), "ping", "", nil); err == nil || !strings.Contains(err.Error(), "insufficient_scope") {
		t.Errorf("权限不足: %v", err)
	}
}

// headers 里手写的 Authorization 优先于 OAuth 令牌。
func TestHeadersOverrideOAuth(t *testing.T) {
	f := newFakeOAuth(t)
	store := testStore(t)
	if err := login(t, f, store, OAuthSpec{}, nil); err != nil {
		t.Fatal(err)
	}
	tr := newHTTPTransport(Spec{Name: "fs", URL: f.mcpURL(), Headers: map[string]string{"Authorization": "Bearer manual"},
		Auth: NewTokenSource(store, "fs", f.mcpURL(), nil)}, &http.Client{})
	if _, err := tr.request(context.Background(), "ping", "", nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("应使用 headers 里的令牌（服务端不认，所以 401）: %v", err)
	}
}

func TestCheckEndpoint(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://auth.example/token": true,
		"http://127.0.0.1:8080/t":    true,
		"http://localhost/t":         true,
		"http://[::1]:9/t":           true,
		"http://auth.example/token":  false,
		"ftp://auth.example/":        false,
		"/relative":                  false,
	} {
		if err := checkEndpoint(raw); (err == nil) != ok {
			t.Errorf("checkEndpoint(%q) = %v", raw, err)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	h := http.Header{}
	h.Add("WWW-Authenticate", `Bearer realm="x", resource_metadata="https://a.example/.well-known/oauth-protected-resource", scope="read write"`)
	c := parseChallenge(h)
	if c.ResourceMetadata != "https://a.example/.well-known/oauth-protected-resource" || c.Scope != "read write" {
		t.Errorf("%+v", c)
	}
	if authParam(`Bearer error="insufficient_scope"`, "error") != "insufficient_scope" {
		t.Error("error 参数")
	}
	// 不能把 xscope 当成 scope。
	if authParam(`Bearer xscope="a"`, "scope") != "" {
		t.Error("参数名必须完整匹配")
	}
}

// 没有 resource_metadata 时按 well-known 路径找资源元数据。
func TestDiscoverResourceWellKnownFallback(t *testing.T) {
	f := newFakeOAuth(t)
	m, err := discoverResource(context.Background(), &http.Client{}, f.mcpURL(), challenge{})
	if err != nil || len(m.AuthorizationServers) != 1 || m.AuthorizationServers[0] != f.url {
		t.Errorf("%+v %v", m, err)
	}
}
