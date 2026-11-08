package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "models.jsonc")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = `{
  "default": "p/m",
  "providers": {
    "p": {"protocol": "openai-chat", "baseUrl": "https://x.test/v1", "models": {"m": {}}}
  }
}`

func TestLoadAcceptsCommentsAndTrailingCommas(t *testing.T) {
	p := write(t, `// 顶部注释
{
  /* 块注释 */
  "default": "p/m", // 行尾注释
  "providers": {
    "p": {
      "protocol": "openai-chat",
      "baseUrl": "https://x.test/v1",
      "models": { "m": { "contextWindow": 1000, }, },
    },
  },
}
`)
	f, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := f.Selected()
	if err != nil {
		t.Fatal(err)
	}
	if sel.Ref() != "p/m" || sel.Model.ContextWindow != 1000 {
		t.Fatalf("得到 %+v", sel)
	}
}

func TestLoadStripsBOM(t *testing.T) {
	p := write(t, "\xEF\xBB\xBF"+minimal)
	if _, err := Load(p); err != nil {
		t.Fatalf("带 BOM 的文件应能解析: %v", err)
	}
}

func TestLoadSyntaxErrorHasLineAndColumn(t *testing.T) {
	// 第 3 行缺少逗号。
	p := write(t, "{\n  \"default\": \"p/m\"\n  \"providers\": {}\n}\n")
	_, err := Load(p)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("应返回 *ParseError，得到 %T: %v", err, err)
	}
	if pe.Line != 3 || pe.Col < 1 || pe.Path != p {
		t.Fatalf("位置不对: %+v", pe)
	}
	if !strings.Contains(err.Error(), p+":3:") {
		t.Fatalf("错误信息应含 路径:行:列，得到 %q", err.Error())
	}
}

func TestLoadTypeErrorHasLocation(t *testing.T) {
	p := write(t, "{\n  \"default\": \"p/m\",\n  \"providers\": {\n    \"p\": {\"protocol\": \"openai-chat\", \"baseUrl\": 123}\n  }\n}\n")
	_, err := Load(p)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("应返回 *ParseError，得到 %T: %v", err, err)
	}
	if pe.Line != 4 || !strings.Contains(pe.Msg, "baseUrl") {
		t.Fatalf("应指向第 4 行的 baseUrl: %+v", pe)
	}
}

func TestLoadSyntaxErrorColumnCountsChineseAsOneChar(t *testing.T) {
	// 同一行里中文注释之后出错：hujson 按字节报列号（35），编辑器里看到的是第 25 个字符。
	src := "{ /* 一二三四五 */ \"default\" 1 }"
	_, err := Load(write(t, src))
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Line != 1 {
		t.Fatalf("得到 %v", err)
	}
	want := utf8.RuneCountInString(src[:strings.Index(src, " 1 }")+1]) + 1
	if want != 25 {
		t.Fatalf("测试数据有误，期望列号应为 25，算出 %d", want)
	}
	if pe.Col != want {
		t.Fatalf("列号 = %d，期望 %d（按字符计，不是按字节）", pe.Col, want)
	}
}

func TestLoadTypeErrorColumnIgnoresChineseComment(t *testing.T) {
	// Standardize 把注释按字节换成空格；若拿标准化内容数字符，每个汉字会被数成 3 列。
	src := `{"default":"p/m","providers":{"p":{ /* 中文注释说明 */ "protocol":"openai-chat","baseUrl":123}}}`
	_, err := Load(write(t, src))
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("应返回 *ParseError，得到 %T: %v", err, err)
	}
	// 类型错误的偏移指向值 123 之后。
	want := utf8.RuneCountInString(src[:strings.Index(src, "123")+3]) + 1
	if pe.Line != 1 || pe.Col != want {
		t.Fatalf("位置 = %d:%d，期望 1:%d", pe.Line, pe.Col, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.jsonc"))
	if err == nil || !strings.Contains(err.Error(), "读取配置文件失败") {
		t.Fatalf("得到 %v", err)
	}
}

func TestLoadValidationCollectsAllProblems(t *testing.T) {
	p := write(t, `{
  "default": "ghost/m",
  "providers": {
    "a": {"protocol": "anthropic", "baseUrl": "ftp://x", "models": {"m": {"contextWindow": -1}}},
    "b": {"protocol": "", "baseUrl": ""}
  }
}`)
	_, err := Load(p)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("应返回 *ValidationError，得到 %T: %v", err, err)
	}
	joined := strings.Join(ve.Problems, "\n")
	for _, want := range []string{
		`providers.a.protocol "anthropic" 不受支持`,
		`providers.a.baseUrl "ftp://x" 必须以 http:// 或 https:// 开头`,
		"providers.a.models.m.contextWindow 不能为负数",
		"providers.b.protocol 不能为空",
		"providers.b.baseUrl 不能为空",
		`default 引用的 provider "ghost" 不存在`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少问题 %q，全部问题:\n%s", want, joined)
		}
	}
}

func TestValidateDefault(t *testing.T) {
	prov := Provider{Protocol: ProtocolChat, BaseURL: "https://x.test", Models: map[string]Model{"m": {}, "org/model": {}}}
	cases := []struct {
		def  string
		want string // 空表示应通过
	}{
		{"p/m", ""},
		{"p/org/model", ""}, // 模型名含斜杠
		{"", "default 不能为空"},
		{"pm", "必须是 provider/model"},
		{"/m", "必须是 provider/model"},
		{"p/", "必须是 provider/model"},
		{"p/zzz", `模型 "zzz" 不在 provider "p" 下`},
	}
	for _, c := range cases {
		f := &File{Default: c.def, Providers: map[string]Provider{"p": prov}}
		joined := strings.Join(f.problems(), "\n")
		if c.want == "" && joined != "" {
			t.Errorf("default=%q 不应有问题: %s", c.def, joined)
		}
		if c.want != "" && !strings.Contains(joined, c.want) {
			t.Errorf("default=%q 应含 %q，得到 %q", c.def, c.want, joined)
		}
	}
}

func TestValidateEmptyProviders(t *testing.T) {
	f := &File{Default: "p/m"}
	if !strings.Contains(strings.Join(f.problems(), "\n"), "providers 不能为空") {
		t.Fatal("providers 为空应报错")
	}
}

func TestBaseURLChecks(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://api.x.com/v1":  true,
		"http://127.0.0.1:8080": true,
		"":                      false,
		"api.x.com/v1":          false,
		"https://":              false,
		"ftp://x.com":           false,
	} {
		if got := checkBaseURL(raw) == ""; got != ok {
			t.Errorf("checkBaseURL(%q) 通过=%v，期望 %v", raw, got, ok)
		}
	}
}

func TestSupportsToolsDefaultsTrue(t *testing.T) {
	f, err := Load(write(t, `{"default":"p/a","providers":{"p":{"protocol":"openai-chat","baseUrl":"https://x.test",
      "models":{"a":{},"b":{"tools":false},"c":{"tools":true}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	m := f.Providers["p"].Models
	if !m["a"].SupportsTools() || m["b"].SupportsTools() || !m["c"].SupportsTools() {
		t.Fatalf("tools 缺省应为 true，显式 false 才关闭: a=%v b=%v c=%v",
			m["a"].SupportsTools(), m["b"].SupportsTools(), m["c"].SupportsTools())
	}
}

func TestAPIKeyResolution(t *testing.T) {
	sel := Selection{ProviderName: "p", Provider: Provider{APIKeyEnv: "P_KEY", APIKey: "from-file"}}
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	if k, err := sel.APIKey(env(map[string]string{"P_KEY": " from-env "})); err != nil || k != "from-env" {
		t.Errorf("环境变量应优先且去空白，得到 %q, %v", k, err)
	}
	if k, err := sel.APIKey(env(nil)); err != nil || k != "from-file" {
		t.Errorf("环境变量缺失时应回退到 apiKey，得到 %q, %v", k, err)
	}
	if k, err := sel.APIKey(env(map[string]string{"P_KEY": "   "})); err != nil || k != "from-file" {
		t.Errorf("环境变量全是空白应视为未设置，得到 %q, %v", k, err)
	}

	none := Selection{ProviderName: "p", Provider: Provider{APIKeyEnv: "P_KEY"}}
	_, err := none.APIKey(env(nil))
	if !errors.Is(err, ErrNoAPIKey) || !strings.Contains(err.Error(), "P_KEY") {
		t.Errorf("无 Key 应返回 ErrNoAPIKey 并提示环境变量名，得到 %v", err)
	}
	_, err = Selection{ProviderName: "q"}.APIKey(env(nil))
	if !errors.Is(err, ErrNoAPIKey) || !strings.Contains(err.Error(), "providers.q.apiKey") {
		t.Errorf("得到 %v", err)
	}
}

func TestSelectedErrors(t *testing.T) {
	f := &File{Default: "p/m", Providers: map[string]Provider{}}
	if _, err := f.Selected(); err == nil {
		t.Fatal("provider 不存在应报错")
	}
}
