// Package config 负责 models.jsonc 的解析、校验和数据目录解析。
package config

import (
	"errors"
	"fmt"
	"strings"
)

// 支持的 LLM 协议。
const (
	ProtocolChat      = "openai-chat"        // POST /chat/completions
	ProtocolResponses = "openai-responses"   // POST /responses
	ProtocolAnthropic = "anthropic-messages" // POST /messages
	ProtocolGoogle    = "google-generate"    // POST /models/{model}:streamGenerateContent
)

// Protocols 列出全部受支持的协议，顺序稳定，用于报错提示。
var Protocols = []string{ProtocolChat, ProtocolResponses, ProtocolAnthropic, ProtocolGoogle}

// File 对应 models.jsonc 的顶层结构。
type File struct {
	Default   string              `json:"default"`
	Providers map[string]Provider `json:"providers"`
	Shell     Shell               `json:"shell"`
}

// Provider 是一个模型渠道商。
type Provider struct {
	Protocol  string           `json:"protocol"`
	BaseURL   string           `json:"baseUrl"`
	APIKeyEnv string           `json:"apiKeyEnv"`
	APIKey    string           `json:"apiKey"`
	Models    map[string]Model `json:"models"`
}

// Model 描述一个模型的能力。
type Model struct {
	ContextWindow int `json:"contextWindow"`
	MaxOutput     int `json:"maxOutput"`
	// Tools 用指针区分“没写”和“写了 false”：没写按支持工具处理。
	Tools *bool `json:"tools"`
}

// SupportsTools 报告模型是否支持工具调用，缺省为 true。
func (m Model) SupportsTools() bool { return m.Tools == nil || *m.Tools }

// Shell 是 shell 探测的显式覆盖，留空则自动探测。
type Shell struct {
	BashPath       string `json:"bashPath"`
	PowerShellPath string `json:"powershellPath"`
}

// Selection 是 default 指向的 provider + model。
type Selection struct {
	ProviderName string
	ModelName    string
	Provider     Provider
	Model        Model
}

// Ref 返回 "provider/model" 形式的引用。
func (s Selection) Ref() string { return s.ProviderName + "/" + s.ModelName }

// ErrNoAPIKey 表示环境变量和配置文件里都没有 API Key。
var ErrNoAPIKey = errors.New("未配置 API Key")

// APIKey 解析 API Key：环境变量优先，apiKey 字段兜底。
func (s Selection) APIKey(getenv func(string) string) (string, error) {
	if s.Provider.APIKeyEnv != "" {
		if v := strings.TrimSpace(getenv(s.Provider.APIKeyEnv)); v != "" {
			return v, nil
		}
	}
	if v := strings.TrimSpace(s.Provider.APIKey); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%w：%s", ErrNoAPIKey, s.KeyHint())
}

// KeyHint 告诉用户去哪里配置 API Key。
func (s Selection) KeyHint() string {
	if s.Provider.APIKeyEnv != "" {
		return fmt.Sprintf("设置环境变量 %s，或在 models.jsonc 填写 providers.%s.apiKey", s.Provider.APIKeyEnv, s.ProviderName)
	}
	return fmt.Sprintf("在 models.jsonc 填写 providers.%s.apiKey", s.ProviderName)
}

// splitRef 把 "provider/model" 拆开。模型名本身可以含 "/"（如 OpenRouter），所以只按第一个 "/" 切。
func splitRef(ref string) (provider, model string, ok bool) {
	i := strings.Index(ref, "/")
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

// Selected 返回 default 指向的 provider 和 model。Load 已校验过，这里仍返回 error 以防手工构造的 File。
func (f *File) Selected() (Selection, error) {
	pn, mn, ok := splitRef(f.Default)
	if !ok {
		return Selection{}, fmt.Errorf("default %q 必须是 provider/model 形式", f.Default)
	}
	p, ok := f.Providers[pn]
	if !ok {
		return Selection{}, fmt.Errorf("default 引用的 provider %q 不存在", pn)
	}
	m, ok := p.Models[mn]
	if !ok {
		return Selection{}, fmt.Errorf("default 引用的模型 %q 不在 provider %q 下", mn, pn)
	}
	return Selection{ProviderName: pn, ModelName: mn, Provider: p, Model: m}, nil
}
