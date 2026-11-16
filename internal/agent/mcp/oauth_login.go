package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// callbackTimeout 是用户完成授权的最长时间。
const callbackTimeout = 5 * time.Minute

// Login 走一次 OAuth 授权码流程并把令牌存进 store。
//
// open 收到授权地址后负责展示给用户（终端里打印出来即可），可以阻塞，但要在 ctx 取消时返回。
// 本机回调收到授权码、换到令牌后才返回。
func Login(ctx context.Context, store *AuthStore, name, serverURL string, spec OAuthSpec, hc *http.Client, open func(context.Context, string) error) error {
	hc = oauthClient(hc)
	ch, err := probe(ctx, hc, serverURL)
	if err != nil {
		return err
	}
	res, err := discoverResource(ctx, hc, serverURL, ch)
	if err != nil {
		return err
	}
	sm, err := discoverServer(ctx, hc, res.AuthorizationServers[0])
	if err != nil {
		return err
	}

	state, err := randomToken()
	if err != nil {
		return err
	}
	cb, err := newCallback(spec.CallbackPort, state)
	if err != nil {
		return err
	}
	defer cb.close()

	clientID, err := chooseClient(ctx, hc, store, name, serverURL, spec, sm, cb.redirect())
	if err != nil {
		return err
	}

	verifier, pkce, err := newPKCE()
	if err != nil {
		return err
	}
	authURL := authorizationURL(sm, clientID, cb.redirect(), pkce, state, scopeOf(spec, ch, res), serverURL)
	if err := open(ctx, authURL); err != nil {
		return err
	}
	got, err := cb.wait(ctx)
	if err != nil {
		return err
	}
	if err := got.check(sm); err != nil {
		return err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {got.code},
		"redirect_uri":  {cb.redirect()},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"resource":      {serverURL},
	}
	tr, err := postToken(ctx, hc, sm.TokenEndpoint, form)
	if err != nil {
		return err
	}
	e := authEntry{URL: serverURL, Issuer: sm.Issuer, TokenEndpoint: sm.TokenEndpoint, ClientID: clientID}
	e.applyToken(tr)
	return store.put(name, e)
}

// chooseClient 决定用哪个 client_id：配置里写了就用配置的；
// 否则沿用之前为同一个端点动态注册得到的；都没有才注册一个新的。
func chooseClient(ctx context.Context, hc *http.Client, store *AuthStore, name, serverURL string, spec OAuthSpec, sm serverMeta, redirect string) (string, error) {
	if spec.ClientID != "" {
		return spec.ClientID, nil
	}
	if e, ok, _ := store.get(name, serverURL); ok && e.ClientID != "" && e.Issuer == sm.Issuer {
		return e.ClientID, nil
	}
	if sm.RegistrationEndpoint == "" {
		return "", fmt.Errorf("授权服务器不支持动态注册，请在 mcp.jsonc 的 %s 里写 oauth.clientId", name)
	}
	return register(ctx, hc, sm.RegistrationEndpoint, redirect)
}

// scopeOf 决定授权请求要的 scope：配置优先，其次是 401 里服务端点名的，最后是资源元数据声明支持的。
func scopeOf(spec OAuthSpec, ch challenge, res resourceMeta) string {
	switch {
	case len(spec.Scopes) > 0:
		return strings.Join(spec.Scopes, " ")
	case ch.Scope != "":
		return ch.Scope
	default:
		return strings.Join(res.ScopesSupported, " ")
	}
}

func authorizationURL(sm serverMeta, clientID, redirect, pkce, state, scope, resource string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"code_challenge":        {pkce},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"resource":              {resource},
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	return sm.AuthorizationEndpoint + "?" + q.Encode()
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newPKCE 生成 code_verifier 与对应的 S256 code_challenge。
func newPKCE() (verifier, pkce string, err error) {
	verifier, err = randomToken()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// callback 是授权服务器把用户送回来时访问的本机地址。
type callback struct {
	ln    net.Listener
	port  int
	state string
	got   chan callbackResult
}

type callbackResult struct {
	code, iss, errCode, errDesc string
}

// check 校验回调参数。state 已在接收时核对过；这里确认 iss：授权服务器声明了会带 iss 就必须带且一致，
// 没声明但带了也不能是别家（RFC 9207，防止混淆攻击把别家的授权码塞给我们）。
func (r callbackResult) check(sm serverMeta) error {
	if r.errCode != "" {
		msg := r.errCode
		if r.errDesc != "" {
			msg += "：" + r.errDesc
		}
		return fmt.Errorf("授权服务器拒绝了登录：%s", msg)
	}
	if r.code == "" {
		return errors.New("回调里没有授权码")
	}
	if (sm.IssParamSupported || r.iss != "") && r.iss != sm.Issuer {
		return fmt.Errorf("回调里的 iss %q 与授权服务器 %q 不一致，已拒绝这次登录", r.iss, sm.Issuer)
	}
	return nil
}

func newCallback(port int, state string) (*callback, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("监听本机回调端口失败: %w", err)
	}
	c := &callback{ln: ln, port: ln.Addr().(*net.TCPAddr).Port, state: state, got: make(chan callbackResult, 1)}
	go http.Serve(ln, c)
	return c, nil
}

func (c *callback) redirect() string {
	return fmt.Sprintf("http://127.0.0.1:%d/callback", c.port)
}

func (c *callback) close() { c.ln.Close() }

// wait 等到回调，或 ctx 取消、超时。
func (c *callback) wait(ctx context.Context) (callbackResult, error) {
	ctx, cancel := context.WithTimeout(ctx, callbackTimeout)
	defer cancel()
	select {
	case r := <-c.got:
		return r, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return callbackResult{}, fmt.Errorf("等待授权超时（%s 内没有收到回调）", callbackTimeout)
		}
		return callbackResult{}, ctx.Err()
	}
}

func (c *callback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/callback" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	// state 不对的请求直接丢弃，不结束登录：本机上别的程序随便发一个请求，不该能让登录失败或抢占结果。
	// Host 必须是我们监听的回环地址，挡住 DNS 重绑定。
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(c.state)) != 1 || r.Host != fmt.Sprintf("127.0.0.1:%d", c.port) {
		http.Error(w, "无效的回调", http.StatusBadRequest)
		return
	}
	select {
	case c.got <- callbackResult{q.Get("code"), q.Get("iss"), q.Get("error"), q.Get("error_description")}:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>Sprout</title><p>授权完成，可以关闭这个页面，回到终端。</p>")
	default:
		http.Error(w, "这次登录已经结束", http.StatusGone)
	}
}
