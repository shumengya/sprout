package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/config"
)

// devFixture 是一个最小的 models.dev 响应：两个渠道各有一个同名模型，价格和窗口都不同。
const devFixture = `{
  "deepseek": {"api": "https://api.deepseek.com", "models": {
    "m1": {"limit": {"context": 1048576, "output": 393216}, "cost": {"input": 0.15, "output": 0.6, "cache_read": 0.003}}
  }},
  "other": {"api": "https://other.example", "models": {
    "m1": {"limit": {"context": 8192, "output": 1024}, "cost": {"input": 9, "output": 9}}
  }}
}`

func devServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func withDevURL(t *testing.T, u string) {
	t.Helper()
	old := config.ModelsDevURLForTest()
	config.SetModelsDevURLForTest(u)
	t.Cleanup(func() { config.SetModelsDevURLForTest(old) })
}

func TestApplyModelsDevFillsOnlyWhatWasNotWritten(t *testing.T) {
	dir := t.TempDir()
	withDevURL(t, devServer(t, devFixture, http.StatusOK).URL)
	if _, err := updateModelsDev(dir, &config.File{}, nil); err != nil {
		t.Fatal(err)
	}
	dev := config.LoadModelsDev(dir)

	cfg := &config.File{Providers: map[string]config.Provider{
		"ds": {BaseURL: "https://api.deepseek.com/v1", Models: map[string]config.Model{
			"m1": {},                     // 什么都没写：窗口和输出都从 models.dev 补
			"m2": {ContextWindow: 12345}, // 手写的窗口保留，输出用默认值
			"m3": {MaxOutput: 777},       // 目录里没有：窗口用默认值，手写的输出保留
		}},
	}}
	applyModelsDev(cfg, dev, nil)

	m1 := cfg.Providers["ds"].Models["m1"]
	if m1.ContextWindow != 1048576 || m1.MaxOutput != 393216 {
		t.Errorf("m1 应从匹配渠道补全，得到 %d / %d", m1.ContextWindow, m1.MaxOutput)
	}
	if !m1.Price.Known() || m1.Price.Input != 0.15 || m1.Price.CacheRead != 0.003 {
		t.Errorf("m1 价格 = %+v", m1.Price)
	}
	// 同名模型在另一个渠道里窗口只有 8192；按 baseUrl 匹配，不能取错。
	if m1.ContextWindow == 8192 {
		t.Error("取到了 other 渠道的 m1")
	}

	m2 := cfg.Providers["ds"].Models["m2"]
	if m2.ContextWindow != 12345 || m2.MaxOutput != config.DefaultMaxOutput {
		t.Errorf("m2 手写窗口应保留、输出走默认值，得到 %d / %d", m2.ContextWindow, m2.MaxOutput)
	}
	m3 := cfg.Providers["ds"].Models["m3"]
	if m3.ContextWindow != config.DefaultContextWindow || m3.MaxOutput != 777 || m3.Price.Known() {
		t.Errorf("m3 目录里没有，不应有价格，得到 %+v", m3)
	}
}

func TestUpdateModelsReportsMatchedAndMissed(t *testing.T) {
	dir := t.TempDir()
	withDevURL(t, devServer(t, devFixture, http.StatusOK).URL)
	cfg := &config.File{Providers: map[string]config.Provider{
		"ds": {BaseURL: "https://api.deepseek.com", Models: map[string]config.Model{"m1": {}, "nope": {}}},
	}}
	msg, err := updateModelsDev(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"已更新模型目录", "2 个模型", "1 个在 models.dev 找到", "ds/nope", "重启 sprout 后生效"} {
		if !strings.Contains(msg, want) {
			t.Errorf("报告缺少 %q:\n%s", want, msg)
		}
	}
	// 第二次不联网也能读到：缓存写进去了。
	if _, ok := config.LoadModelsDev(dir).Lookup("m1", "https://api.deepseek.com"); !ok {
		t.Error("缓存里应能查到 m1")
	}
}

func TestUpdateModelsSurfacesFetchFailures(t *testing.T) {
	dir := t.TempDir()
	withDevURL(t, devServer(t, "nope", http.StatusBadGateway).URL)
	if _, err := updateModelsDev(dir, &config.File{}, nil); err == nil {
		t.Fatal("服务端报错时应返回错误，不能当成更新成功")
	}
	if _, ok := config.LoadModelsDev(dir).Lookup("m1", ""); ok {
		t.Error("失败的响应不该写入缓存")
	}
}

func TestUpdateModelsCommandPrintsTheReport(t *testing.T) {
	dir := t.TempDir()
	withDevURL(t, devServer(t, devFixture, http.StatusOK).URL)
	if _, err := config.EnsureTemplate(config.ModelsPath(dir)); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	if code := updateModels(dir, &out, &errOut); code != 0 {
		t.Fatalf("退出码 %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "已更新模型目录") || errOut.Len() != 0 {
		t.Errorf("stdout=%q stderr=%q", out.String(), errOut.String())
	}

	// 配置读不出来是错误，写到 stderr 而不是当成功。
	bad := t.TempDir()
	writeConfig(t, bad, "{这不是 jsonc")
	out.Reset()
	errOut.Reset()
	if code := updateModels(bad, &out, &errOut); code == 0 || errOut.Len() == 0 {
		t.Errorf("坏配置应报错退出，code=%d stderr=%q", code, errOut.String())
	}
}
