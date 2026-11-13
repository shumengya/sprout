package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ModelsDevURL 是 models.dev 的模型目录（providers → models）。
// 数据由 OpenCode 维护，含每个模型的上下文窗口、输出上限和单价。
const ModelsDevURL = "https://models.dev/api.json"

// modelsDevURL 是实际拉取用的地址；测试替换它指向 httptest，避免打真实网络。
var modelsDevURL = ModelsDevURL

// ModelsDevURLForTest 与 SetModelsDevURLForTest 只给测试用：把拉取地址换成 httptest。
func ModelsDevURLForTest() string { return modelsDevURL }

func SetModelsDevURLForTest(u string) { modelsDevURL = u }

// modelsDevCacheFile 是 models.dev 目录的本地缓存文件名，放在数据目录里。
const modelsDevCacheFile = "models.dev.json"

// 查不到 models.dev 数据时用的默认值。
const (
	DefaultContextWindow = 400000
	DefaultMaxOutput     = 16000
)

// ModelsDevCachePath 是 models.dev 缓存的路径。
func ModelsDevCachePath(dataDir string) string { return joinDataDir(dataDir, modelsDevCacheFile) }

// ModelSpec 是一个模型从 models.dev 查到的规格。价格单位是美元 / 百万 token；0 表示该项目录里没有。
type ModelSpec struct {
	ContextWindow int
	MaxOutput     int
	InputPrice    float64 // 没命中缓存的输入
	OutputPrice   float64
	CacheRead     float64
	CacheWrite    float64
}

// modelsDevFile 是 api.json 里用到的部分。其余字段（描述、模态、状态）不需要，丢弃。
type modelsDevFile struct {
	Providers map[string]modelsDevProvider `json:"-"`
}

type modelsDevProvider struct {
	API    string                    `json:"api"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
}

// UnmarshalJSON 让顶层直接是 providers 的 map（api.json 没有 providers 这一层）。
func (f *modelsDevFile) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &f.Providers)
}

func (m modelsDevModel) spec() ModelSpec {
	return ModelSpec{
		ContextWindow: m.Limit.Context,
		MaxOutput:     m.Limit.Output,
		InputPrice:    m.Cost.Input,
		OutputPrice:   m.Cost.Output,
		CacheRead:     m.Cost.CacheRead,
		CacheWrite:    m.Cost.CacheWrite,
	}
}

// ModelsDev 是 models.dev 目录的本地副本，用来补 models.jsonc 里没写的窗口、输出上限和价格。
// 零值可用：什么都查不到，调用方按默认值处理。
type ModelsDev struct {
	byID  map[string]ModelSpec // 模型 id → 规格；多个渠道都有时保留先见到的
	byAPI map[string]ModelSpec // "baseUrl|模型id" → 规格，用来区分同名模型的不同渠道
}

// Lookup 按模型 id 查规格。baseURL 非空时优先匹配同一渠道（api 字段）的条目，
// 匹配不上再退回按 id 查到的第一条。ok 为 false 表示目录里没有这个模型。
func (d ModelsDev) Lookup(modelID, baseURL string) (ModelSpec, bool) {
	if d.byAPI != nil && baseURL != "" {
		if s, ok := d.byAPI[normBaseURL(baseURL)+"|"+modelID]; ok {
			return s, true
		}
	}
	s, ok := d.byID[modelID]
	return s, ok
}

// normBaseURL 去掉末尾斜杠并小写，让 "https://api.deepseek.com" 与 "https://api.deepseek.com/" 对得上。
func normBaseURL(u string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(u)), "/")
}

func indexModelsDev(f modelsDevFile) ModelsDev {
	d := ModelsDev{byID: map[string]ModelSpec{}, byAPI: map[string]ModelSpec{}}
	for _, p := range f.Providers {
		for id, m := range p.Models {
			s := m.spec()
			if _, seen := d.byID[id]; !seen {
				d.byID[id] = s
			}
			if p.API != "" {
				d.byAPI[normBaseURL(p.API)+"|"+id] = s
			}
		}
	}
	return d
}

// LoadModelsDev 读取数据目录里缓存的 models.dev 目录。
// 没有缓存、缓存为空或损坏时返回零值，不报错：查不到就用默认值，不能因为缓存坏了启动失败。
func LoadModelsDev(dataDir string) ModelsDev {
	b, err := os.ReadFile(ModelsDevCachePath(dataDir))
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return ModelsDev{}
	}
	var f modelsDevFile
	if json.Unmarshal(b, &f) != nil || len(f.Providers) == 0 {
		return ModelsDev{}
	}
	return indexModelsDev(f)
}

// modelsDevMaxBytes 是接受的目录上限。api.json 约 6 MB，留出增长空间；超过就当异常响应拒绝。
const modelsDevMaxBytes = 64 << 20

// FetchModelsDev 从 models.dev 拉取目录并原子写入数据目录的缓存，返回解析后的目录。
// 写入用同目录临时文件再改名，中途失败不会留下半截缓存。
func FetchModelsDev(dataDir string, client *http.Client) (ModelsDev, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Get(modelsDevURL)
	if err != nil {
		return ModelsDev{}, fmt.Errorf("获取 models.dev 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ModelsDev{}, fmt.Errorf("获取 models.dev 失败: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, modelsDevMaxBytes+1))
	if err != nil {
		return ModelsDev{}, fmt.Errorf("读取 models.dev 响应失败: %w", err)
	}
	if len(b) > modelsDevMaxBytes {
		return ModelsDev{}, fmt.Errorf("models.dev 响应超过 %d MB，已拒绝", modelsDevMaxBytes>>20)
	}
	var f modelsDevFile
	if err := json.Unmarshal(b, &f); err != nil {
		return ModelsDev{}, fmt.Errorf("models.dev 响应不是预期的 JSON: %w", err)
	}
	if len(f.Providers) == 0 {
		return ModelsDev{}, fmt.Errorf("models.dev 响应里没有任何渠道")
	}

	path := ModelsDevCachePath(dataDir)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".models.dev-*.tmp")
	if err != nil {
		return ModelsDev{}, fmt.Errorf("无法写入缓存 %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return ModelsDev{}, fmt.Errorf("写入缓存失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return ModelsDev{}, fmt.Errorf("写入缓存失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return ModelsDev{}, fmt.Errorf("替换缓存 %s 失败: %w", path, err)
	}
	return indexModelsDev(f), nil
}

// ResolveModel 用 models.dev 补全一个模型没写的字段。
//
// 优先级：models.jsonc 里手写的值 > models.dev 查到的 > 默认值（上下文 400000、输出 16000）。
// 手写 0 或不写都算“没写”。价格没有默认值：目录里没有就保持 0，调用方据此显示占位符。
// from 非空表示窗口或输出上限里至少有一项来自 models.dev 或默认值，供启动时提示用户。
func ResolveModel(m Model, spec ModelSpec, found bool) (Model, string) {
	src := func(written, looked int, name string) (int, string) {
		switch {
		case written > 0:
			return written, ""
		case found && looked > 0:
			return looked, "models.dev"
		default:
			return map[string]int{"contextWindow": DefaultContextWindow, "maxOutput": DefaultMaxOutput}[name], "默认值"
		}
	}
	var notes []string
	var from string
	m.ContextWindow, from = src(m.ContextWindow, spec.ContextWindow, "contextWindow")
	if from != "" {
		notes = append(notes, "contextWindow "+from)
	}
	m.MaxOutput, from = src(m.MaxOutput, spec.MaxOutput, "maxOutput")
	if from != "" {
		notes = append(notes, "maxOutput "+from)
	}
	if found {
		m.Price = ModelPrice{
			Input: spec.InputPrice, Output: spec.OutputPrice,
			CacheRead: spec.CacheRead, CacheWrite: spec.CacheWrite,
		}
	}
	return m, strings.Join(notes, "、")
}
