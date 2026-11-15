// Package config 负责 models.jsonc 的解析、校验和数据目录解析。
package config

import (
	"errors"
	"fmt"
	"slices"
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
	// MaxTokensParam 是 Chat 协议里输出上限的参数名：留空或 max_tokens，或 max_completion_tokens（OpenAI o 系列、GPT-5 在 Chat 协议下只认后者）。其他协议忽略。
	MaxTokensParam string `json:"maxTokensParam"`
	// Tools 用指针区分“没写”和“写了 false”：没写按支持工具处理。
	Tools *bool `json:"tools"`
	// Vision 表示模型是否支持图片输入。缺省为 false：绝大多数模型没有视觉能力，
	// 未标注时发图片会被服务端拒绝，宁可在本地降级成占位文本。
	Vision *bool `json:"vision"`
	// Effort 声明可调的思考档位；为 nil 表示不能调节（不发送任何思考参数）。
	Effort *Effort `json:"effort"`
	// Price 是单价（美元 / 百万 token），由 models.dev 补上，不从 models.jsonc 读。
	// 全为 0 表示没有价格数据，状态栏的“花费”显示占位符。
	Price ModelPrice `json:"-"`
}

// ModelPrice 是一个模型各项 token 的单价，单位美元 / 百万 token。
type ModelPrice struct {
	Input      float64 // 没命中缓存的输入
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Known 报告是否有可用的价格：输入和输出都为 0 时认为没有（缓存价单独为 0 是正常的，表示该模型不计缓存费）。
func (p ModelPrice) Known() bool { return p.Input > 0 || p.Output > 0 }

// SupportsTools 报告模型是否支持工具调用，缺省为 true。
func (m Model) SupportsTools() bool { return m.Tools == nil || *m.Tools }

// SupportsVision 报告模型是否支持图片输入，缺省为 false。
func (m Model) SupportsVision() bool { return m.Vision != nil && *m.Vision }

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
func (f *File) Selected() (Selection, error) { return f.Select(f.Default) }

// Select 解析 "provider/model" 引用。provider 名不能含 "/"，模型名可以（如 OpenRouter）。
func (f *File) Select(ref string) (Selection, error) {
	pn, mn, ok := splitRef(ref)
	if !ok {
		return Selection{}, fmt.Errorf("%q 必须是 provider/model 形式", ref)
	}
	p, ok := f.Providers[pn]
	if !ok {
		return Selection{}, fmt.Errorf("引用的 provider %q 不存在", pn)
	}
	m, ok := p.Models[mn]
	if !ok {
		return Selection{}, fmt.Errorf("引用的模型 %q 不在 provider %q 下", mn, pn)
	}
	return Selection{ProviderName: pn, ModelName: mn, Provider: p, Model: m}, nil
}

// All 返回配置里全部 provider/model 组合，按引用排序，供 TUI 的 /model 列表使用。
func (f *File) All() []Selection {
	var out []Selection
	for pn, p := range f.Providers {
		for mn, m := range p.Models {
			out = append(out, Selection{ProviderName: pn, ModelName: mn, Provider: p, Model: m})
		}
	}
	slices.SortFunc(out, func(a, b Selection) int { return strings.Compare(a.Ref(), b.Ref()) })
	return out
}
