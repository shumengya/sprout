package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// challenge 是服务端 401 响应的 WWW-Authenticate 里我们关心的参数。
type challenge struct {
	ResourceMetadata string
	Scope            string
}

// authParam 取 WWW-Authenticate 里 key="value" 形式的参数。
func authParam(header, key string) string {
	re := regexp.MustCompile(`(?i)(?:^|[\s,])` + regexp.QuoteMeta(key) + `="([^"]*)"`)
	if m := re.FindStringSubmatch(header); m != nil {
		return m[1]
	}
	return ""
}

func parseChallenge(h http.Header) challenge {
	v := strings.Join(h.Values("Www-Authenticate"), ", ")
	return challenge{ResourceMetadata: authParam(v, "resource_metadata"), Scope: authParam(v, "scope")}
}

// probe 向服务端发一个不带令牌的请求，确认它要求认证并取出 WWW-Authenticate。
func probe(ctx context.Context, hc *http.Client, serverURL string) (challenge, error) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, bytes.NewReader(body))
	if err != nil {
		return challenge{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		return challenge{}, fmt.Errorf("连接 %s 失败: %w", serverURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusUnauthorized {
		return challenge{}, fmt.Errorf("服务端没有要求认证（HTTP %d），不需要登录", resp.StatusCode)
	}
	return parseChallenge(resp.Header), nil
}

// wellKnownURL 按 RFC 8615 把 /.well-known/<suffix> 插在路径之前。
func wellKnownURL(base *url.URL, suffix string, withPath bool) string {
	u := *base
	p := ""
	if withPath {
		p = strings.TrimSuffix(base.Path, "/")
	}
	u.Path, u.RawQuery, u.Fragment = "/.well-known/"+suffix+p, "", ""
	return u.String()
}

type resourceMeta struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// discoverResource 取受保护资源元数据，并要求其中的 resource 就是我们要连的地址：
// 否则恶意服务端可以借别的资源的元数据，骗我们把令牌发给它。
func discoverResource(ctx context.Context, hc *http.Client, serverURL string, ch challenge) (resourceMeta, error) {
	su, err := url.Parse(serverURL)
	if err != nil {
		return resourceMeta{}, err
	}
	candidates := []string{ch.ResourceMetadata}
	if ch.ResourceMetadata == "" {
		candidates = []string{wellKnownURL(su, "oauth-protected-resource", true), wellKnownURL(su, "oauth-protected-resource", false)}
	}
	var errs []error
	for _, c := range candidates {
		var m resourceMeta
		if err := getJSON(ctx, hc, c, &m); err != nil {
			errs = append(errs, err)
			continue
		}
		if strings.TrimSuffix(m.Resource, "/") != strings.TrimSuffix(serverURL, "/") {
			return m, fmt.Errorf("受保护资源元数据里的 resource %q 与服务端地址 %q 不一致，已拒绝", m.Resource, serverURL)
		}
		if len(m.AuthorizationServers) == 0 {
			return m, errors.New("受保护资源元数据里没有 authorization_servers")
		}
		return m, nil
	}
	return resourceMeta{}, fmt.Errorf("找不到受保护资源元数据: %w", errors.Join(errs...))
}

type serverMeta struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	IssParamSupported     bool     `json:"authorization_response_iss_parameter_supported"`
}

// discoverServer 取授权服务器元数据。issuer 必须与取文档用的地址完全一致（RFC 8414），
// 并且必须声明支持 PKCE 的 S256；不声明时拒绝，因为无法确认它会校验 code_verifier。
func discoverServer(ctx context.Context, hc *http.Client, issuer string) (serverMeta, error) {
	iu, err := url.Parse(issuer)
	if err != nil {
		return serverMeta{}, err
	}
	candidates := []string{
		wellKnownURL(iu, "oauth-authorization-server", true),
		wellKnownURL(iu, "openid-configuration", true),
	}
	if iu.Path != "" && iu.Path != "/" {
		candidates = append(candidates, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration")
	}
	var errs []error
	for _, c := range candidates {
		var m serverMeta
		if err := getJSON(ctx, hc, c, &m); err != nil {
			errs = append(errs, err)
			continue
		}
		if m.Issuer != issuer {
			errs = append(errs, fmt.Errorf("%s 里的 issuer %q 与 %q 不一致", c, m.Issuer, issuer))
			continue
		}
		for _, e := range []string{m.AuthorizationEndpoint, m.TokenEndpoint} {
			if err := checkEndpoint(e); err != nil {
				return m, err
			}
		}
		if !slices.Contains(m.CodeChallengeMethods, "S256") {
			return m, errors.New("授权服务器没有声明支持 PKCE 的 S256，拒绝登录")
		}
		return m, nil
	}
	return serverMeta{}, fmt.Errorf("找不到授权服务器元数据: %w", errors.Join(errs...))
}

// register 按 RFC 7591 动态注册一个公开客户端（没有密钥，靠 PKCE 保护）。
func register(ctx context.Context, hc *http.Client, endpoint, redirect string) (string, error) {
	if err := checkEndpoint(endpoint); err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                "Sprout",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		ClientID string `json:"client_id"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("动态注册失败：HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 200)])))
	}
	if err := json.Unmarshal(data, &out); err != nil || out.ClientID == "" {
		return "", errors.New("动态注册的响应里没有 client_id")
	}
	return out.ClientID, nil
}
