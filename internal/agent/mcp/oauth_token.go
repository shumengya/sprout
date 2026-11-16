package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// refreshSkew 是令牌离过期还剩多少时就提前刷新，避免请求发出去的路上过期。
const refreshSkew = time.Minute

// OAuthSpec 是 mcp.jsonc 里 oauth 段的内容。
type OAuthSpec struct {
	// ClientID 非空时直接使用；空则向授权服务器动态注册。
	ClientID string
	// Scopes 非空时作为授权请求的 scope；空则用服务端在 WWW-Authenticate 里给的 scope。
	Scopes []string
	// CallbackPort 是本机回调端口；0 表示随机。预先注册了 redirect_uri 的客户端需要固定端口。
	CallbackPort int
}

// Authorizer 给 HTTP 请求提供访问令牌。
type Authorizer interface {
	Token(ctx context.Context) (string, error)
}

// ErrLoginRequired 表示还没登录，或登录已失效且无法静默刷新。
var ErrLoginRequired = errors.New("需要登录")

var errInvalidGrant = errors.New("invalid_grant")

// TokenSource 从 AuthStore 取令牌，临近过期时用 refresh_token 静默刷新。
type TokenSource struct {
	store *AuthStore
	name  string
	url   string
	hc    *http.Client
	mu    sync.Mutex // 同一服务端的并发请求只刷新一次
}

// NewTokenSource 为服务端 name（端点 url）创建令牌来源。hc 为 nil 时用带超时的默认客户端。
func NewTokenSource(store *AuthStore, name, url string, hc *http.Client) *TokenSource {
	return &TokenSource{store: store, name: name, url: url, hc: oauthClient(hc)}
}

func oauthClient(hc *http.Client) *http.Client {
	if hc != nil {
		return hc
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (t *TokenSource) loginErr(why string) error {
	return fmt.Errorf("%w（%s）：运行 sprout mcp login %s", ErrLoginRequired, why, t.name)
}

// Token 实现 Authorizer。
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok, err := t.store.get(t.name, t.url)
	if err != nil {
		return "", err
	}
	if !ok || (e.AccessToken == "" && e.RefreshToken == "") {
		return "", t.loginErr("还没有登录")
	}
	if e.AccessToken != "" && (e.Expiry.IsZero() || time.Until(e.Expiry) > refreshSkew) {
		return e.AccessToken, nil
	}
	if e.RefreshToken == "" {
		return "", t.loginErr("登录已过期")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {e.RefreshToken}, "client_id": {e.ClientID}, "resource": {t.url}}
	tr, err := postToken(ctx, t.hc, e.TokenEndpoint, form)
	if errors.Is(err, errInvalidGrant) {
		// 刷新令牌已被撤销或过期：清掉没用的令牌，之后只能重新登录。
		e.AccessToken, e.RefreshToken, e.Expiry = "", "", time.Time{}
		_ = t.store.put(t.name, e)
		return "", t.loginErr("刷新令牌已失效")
	}
	if err != nil {
		return "", fmt.Errorf("刷新登录令牌失败: %w", err)
	}
	e.applyToken(tr)
	if err := t.store.put(t.name, e); err != nil {
		return "", err
	}
	return e.AccessToken, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// applyToken 把令牌响应写进登录状态。刷新时服务端可以不返回新的 refresh_token，此时沿用旧的。
func (e *authEntry) applyToken(tr tokenResponse) {
	e.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		e.RefreshToken = tr.RefreshToken
	}
	e.Expiry = time.Time{}
	if tr.ExpiresIn > 0 {
		e.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	if tr.Scope != "" {
		e.Scope = tr.Scope
	}
}

// postToken 向令牌端点提交表单。
func postToken(ctx context.Context, hc *http.Client, endpoint string, form url.Values) (tokenResponse, error) {
	var tr tokenResponse
	if err := checkEndpoint(endpoint); err != nil {
		return tr, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tr, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return tr, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode < 200 || resp.StatusCode > 299 || tr.Error != "" {
		if tr.Error == "invalid_grant" {
			return tr, fmt.Errorf("%w: %s", errInvalidGrant, tr.ErrorDesc)
		}
		msg := tr.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if tr.ErrorDesc != "" {
			msg += "：" + tr.ErrorDesc
		}
		return tr, fmt.Errorf("令牌端点返回错误 %s", msg)
	}
	if tr.AccessToken == "" {
		return tr, errors.New("令牌端点的响应里没有 access_token")
	}
	if tr.TokenType != "" && !strings.EqualFold(tr.TokenType, "bearer") {
		return tr, fmt.Errorf("不支持的令牌类型 %q，只支持 Bearer", tr.TokenType)
	}
	return tr, nil
}

// getJSON 取一份元数据文档。
func getJSON(ctx context.Context, hc *http.Client, target string, v any) error {
	if err := checkEndpoint(target); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s 返回 HTTP %d", target, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// checkEndpoint 要求授权相关的地址是 https，或本机回环上的 http（本地开发与测试）。
// 否则授权码与令牌会在明文链路上传输。
func checkEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("地址 %q 无效", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("地址 %s 必须使用 https（本机回环地址除外）", raw)
}
