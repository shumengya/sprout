package main

import (
	"os"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/event"
)

func TestMissingOrEmptySystemPromptIsCreatedFromDefault(t *testing.T) {
	for name, prepare := range map[string]func(path string){
		"缺失": func(string) {},
		"为空": func(p string) { os.WriteFile(p, nil, 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := config.SystemPromptPath(dir)
			prepare(path)
			if _, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(path)
			if err != nil || string(b) != agent.DefaultPromptTemplate {
				t.Fatalf("应写入内置模板: err=%v len=%d", err, len(b))
			}
		})
	}
}

func TestExistingSystemPromptIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := config.SystemPromptPath(dir)
	if err := os.WriteFile(path, []byte("我自己的提示词"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "我自己的提示词" {
		t.Fatalf("用户的文件被改动: %q", b)
	}
}

// 写错的模板必须让启动失败并指出文件，静默回退到内置模板会让用户以为改动生效了。
func TestBrokenSystemPromptIsFatal(t *testing.T) {
	dir := t.TempDir()
	path := config.SystemPromptPath(dir)
	if err := os.WriteFile(path, []byte("工作目录 {{.Workdir}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := buildApp(dir, testEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}, shellSet(true, true)), nil)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "Workdir") {
		t.Fatalf("应报出文件路径和错误字段，得到 %v", err)
	}
}

// 自定义模板真的成为发给模型的系统提示词：变量按当前环境填好，按工具裁剪的分支生效，
// 切换模型后用同一份模板重新渲染。
func TestCustomSystemPromptReachesModelRequest(t *testing.T) {
	srv := newModelServer(t, "bash")
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	custom := "自定义提示词 cwd={{.Cwd}} tools={{join .Tools \",\"}}{{if .Has \"bash\"}} HAS_BASH{{end}}{{if .Has \"nope\"}} HAS_NOPE{{end}}"
	if err := os.WriteFile(config.SystemPromptPath(dir), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := buildApp(dir, testEnv(nil, shellSet(true, false)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v / %s", err, a.disabled)
	}
	defer a.close()
	if err := a.ag.Run(t.Context(), agent.Input{Text: "你好"}, event.SinkFunc(func(event.Event) {})); err != nil {
		t.Fatal(err)
	}
	body := srv.bodies[0]
	for _, want := range []string{"自定义提示词", `cwd=work`, "tools=bash,read,write,edit,delete,agent,ls", "HAS_BASH"} {
		if !strings.Contains(body, want) {
			t.Errorf("请求里缺少 %q: %.400s", want, body)
		}
	}
	for _, bad := range []string{"HAS_NOPE", "你是 Sprout", "{{"} {
		if strings.Contains(body, bad) {
			t.Errorf("请求里不应出现 %q", bad)
		}
	}
}
