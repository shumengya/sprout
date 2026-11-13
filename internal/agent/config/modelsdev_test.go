package config

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 两个渠道都提供同名模型，但窗口和价格不同，用来验证按 baseUrl 区分。
const modelsDevSample = `{
  "deepseek": {
    "id": "deepseek",
    "api": "https://api.deepseek.com",
    "models": {
      "deepseek-flash": {
        "id": "deepseek-flash",
        "limit": {"context": 1000000, "output": 384000},
        "cost": {"input": 0.15, "output": 0.6, "cache_read": 0.003}
      }
    }
  },
  "other": {
    "id": "other",
    "api": "https://example.test/v1/",
    "models": {
      "deepseek-flash": {
        "id": "deepseek-flash",
        "limit": {"context": 128000, "output": 8192},
        "cost": {"input": 1, "output": 2}
      },
      "no-price": {
        "id": "no-price",
        "limit": {"context": 8192, "output": 2048}
      }
    }
  }
}`

func TestModelsDevLookupPrefersTheMatchingProvider(t *testing.T) {
	var f modelsDevFile
	if err := f.UnmarshalJSON([]byte(modelsDevSample)); err != nil {
		t.Fatal(err)
	}
	d := indexModelsDev(f)

	s, ok := d.Lookup("deepseek-flash", "https://api.deepseek.com/")
	if !ok || s.ContextWindow != 1000000 || s.CacheRead != 0.003 {
		t.Fatalf("同一渠道应命中 deepseek 的条目: %+v ok=%v", s, ok)
	}
	s, ok = d.Lookup("deepseek-flash", "https://example.test/v1")
	if !ok || s.ContextWindow != 128000 || s.InputPrice != 1 {
		t.Fatalf("末尾斜杠不同也应命中 other: %+v", s)
	}
	s, ok = d.Lookup("deepseek-flash", "")
	if !ok || s.ContextWindow == 0 {
		t.Fatalf("没有 baseUrl 时应退回按 id 查到的第一条: %+v", s)
	}
	if _, ok := d.Lookup("不存在", ""); ok {
		t.Fatal("目录里没有的模型不应查到")
	}
	s, ok = d.Lookup("no-price", "")
	if !ok || s.InputPrice != 0 || s.ContextWindow != 8192 {
		t.Fatalf("没有价格的模型窗口照常返回，价格为 0: %+v", s)
	}
}

func TestResolveModelPrefersHandWrittenValues(t *testing.T) {
	spec := ModelSpec{ContextWindow: 1000000, MaxOutput: 384000, InputPrice: 0.15, OutputPrice: 0.6}

	got, from := ResolveModel(Model{ContextWindow: 123, MaxOutput: 456}, spec, true)
	if got.ContextWindow != 123 || got.MaxOutput != 456 {
		t.Fatalf("手写值应优先: %+v", got)
	}
	if from != "" {
		t.Fatalf("两项都是手写的，不该有来源说明: %q", from)
	}
	if !got.Price.Known() || got.Price.Input != 0.15 {
		t.Fatalf("价格应来自 models.dev: %+v", got.Price)
	}

	got, from = ResolveModel(Model{}, spec, true)
	if got.ContextWindow != 1000000 || got.MaxOutput != 384000 {
		t.Fatalf("没写时应取 models.dev: %+v", got)
	}
	if !strings.Contains(from, "contextWindow models.dev") || !strings.Contains(from, "maxOutput models.dev") {
		t.Fatalf("应说明来自 models.dev: %q", from)
	}

	// 手写其中一项，另一项仍从目录补。
	got, from = ResolveModel(Model{MaxOutput: 999}, spec, true)
	if got.ContextWindow != 1000000 || got.MaxOutput != 999 || !strings.Contains(from, "contextWindow") || strings.Contains(from, "maxOutput") {
		t.Fatalf("只该补没写的那一项: %+v %q", got, from)
	}
}

func TestResolveModelFallsBackToDefaults(t *testing.T) {
	got, from := ResolveModel(Model{}, ModelSpec{}, false)
	if got.ContextWindow != DefaultContextWindow || got.MaxOutput != DefaultMaxOutput {
		t.Fatalf("查不到应用默认值: %+v", got)
	}
	if !strings.Contains(from, "默认值") {
		t.Fatalf("应说明用了默认值: %q", from)
	}
	if got.Price.Known() {
		t.Fatal("查不到时不应有价格")
	}

	// 目录里有这个模型，但某一项是 0：该项走默认值，另一项照用目录的。
	got, from = ResolveModel(Model{}, ModelSpec{ContextWindow: 8192}, true)
	if got.ContextWindow != 8192 || got.MaxOutput != DefaultMaxOutput {
		t.Fatalf("目录缺的项走默认值: %+v", got)
	}
	if !strings.Contains(from, "contextWindow models.dev") || !strings.Contains(from, "maxOutput 默认值") {
		t.Fatalf("来源说明不对: %q", from)
	}
}

func TestFetchModelsDevCachesAtomically(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(modelsDevSample))
	}))
	defer srv.Close()

	dir := t.TempDir()
	old := ModelsDevURL
	t.Cleanup(func() { /* URL 是常量，测试里改不了；改为注入见下 */ _ = old })

	client := srv.Client()
	// FetchModelsDev 用固定 URL，测试通过替换一个可注入的变量来指向 httptest。
	restore := swapModelsDevURL(srv.URL + "/api.json")
	defer restore()

	d, err := FetchModelsDev(dir, client)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := d.Lookup("deepseek-flash", "https://api.deepseek.com")
	if !ok || s.MaxOutput != 384000 {
		t.Fatalf("拉取结果应可查询: %+v ok=%v", s, ok)
	}

	cached := LoadModelsDev(dir)
	s, ok = cached.Lookup("deepseek-flash", "https://api.deepseek.com")
	if !ok || s.ContextWindow != 1000000 {
		t.Fatalf("缓存应能读回同样的结果: %+v", s)
	}
	if _, err := os.Stat(ModelsDevCachePath(dir)); err != nil {
		t.Fatal(err)
	}
	// 原子替换：目录里不应残留临时文件。
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("残留临时文件 %s", e.Name())
		}
	}
}

func TestFetchModelsDevRejectsBadResponses(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"非 200":   func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
		"不是 JSON": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) },
		"空目录":     func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			defer swapModelsDevURL(srv.URL)()
			if _, err := FetchModelsDev(t.TempDir(), srv.Client()); err == nil {
				t.Fatal("应拒绝")
			}
		})
	}
}

func TestLoadModelsDevToleratesAMissingOrBrokenCache(t *testing.T) {
	if d := LoadModelsDev(t.TempDir()); len(d.byID) != 0 {
		t.Fatal("没有缓存时应是零值")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, modelsDevCacheFile), []byte("{这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := LoadModelsDev(dir); len(d.byID) != 0 {
		t.Fatal("损坏的缓存应被忽略而不是报错")
	}
}

// swapModelsDevURL 临时替换拉取地址，返回还原函数。models.dev 的地址对测试来说必须可替换，
// 否则只能打真实网络。
func swapModelsDevURL(u string) func() {
	old := modelsDevURL
	modelsDevURL = u
	return func() { modelsDevURL = old }
}
