package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/event"
)

// 写一份 AGENTS.md，返回它的绝对路径。
func writeAgents(t *testing.T, dir, text string) string {
	t.Helper()
	path := filepath.Join(dir, config.AgentsFileName)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// 数据目录一份全局，工作目录到根各级一份项目说明，按"全局 → 最上层 → … → 工作目录"拼接。
func TestInstructionsOrderedFromGeneralToSpecific(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	nested := filepath.Join(root, "proj", "sub")
	for _, d := range []string{dataDir, filepath.Join(root, "proj"), nested} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	globalPath := writeAgents(t, dataDir, "全局说明：回复用中文")
	projPath := writeAgents(t, filepath.Join(root, "proj"), "项目说明：Go 1.27")
	subPath := writeAgents(t, nested, "子目录说明：这里都是测试")

	docs, problems := loadInstructions(dataDir, nested)
	if len(problems) != 0 {
		t.Fatalf("不该有问题: %v", problems)
	}
	var got []string
	for _, d := range docs {
		got = append(got, d.Scope+"|"+d.Path)
	}
	// root 的上级可能也有 AGENTS.md（比如用户 home），只断言这三份的相对顺序。
	want := []string{"全局|" + globalPath, "项目|" + projPath, "项目|" + subPath}
	var filtered []string
	set := map[string]bool{globalPath: true, projPath: true, subPath: true}
	for _, item := range got {
		if set[strings.SplitN(item, "|", 2)[1]] {
			filtered = append(filtered, item)
		}
	}
	if strings.Join(filtered, "\n") != strings.Join(want, "\n") {
		t.Fatalf("顺序不对:\n%s\n期望:\n%s\n全部: %v", strings.Join(filtered, "\n"), strings.Join(want, "\n"), got)
	}
}

func TestInstructionsSkipMissingAndEmpty(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	work := filepath.Join(root, "work")
	for _, d := range []string{dataDir, work} {
		os.MkdirAll(d, 0o755)
	}
	writeAgents(t, dataDir, "   \n\n") // 只有空白，等同空文件
	writeAgents(t, work, "真正的说明")
	docs, problems := loadInstructions(dataDir, work)
	if len(problems) != 0 {
		t.Fatalf("缺失或空白不算问题: %v", problems)
	}
	if len(docs) != 1 || docs[0].Text != "真正的说明" {
		t.Fatalf("只应保留有内容的文件: %+v", docs)
	}
}

func TestInstructionsTruncatedAtLimit(t *testing.T) {
	dir := t.TempDir()
	writeAgents(t, dir, strings.Repeat("说", maxInstructionBytes)) // 三字节一个字，远超上限
	docs, _ := loadInstructions(dir, dir)
	if len(docs) != 1 {
		t.Fatalf("docs = %+v", docs)
	}
	if len(docs[0].Text) > maxInstructionBytes+200 {
		t.Fatalf("没有截断: %d 字节", len(docs[0].Text))
	}
	if !strings.Contains(docs[0].Text, "已省略") {
		t.Error("截断处应注明")
	}
	// 截断不能切坏 UTF-8：否则发给服务端的请求会变成非法编码。
	if !utf8.ValidString(docs[0].Text) {
		t.Error("截断后不是合法的 UTF-8")
	}
}

// AGENTS.md 这类说明文件内容真的出现在发给模型的系统提示词里，并且带着来源路径。
func TestInstructionsReachModelRequest(t *testing.T) {
	srv := newModelServer(t, "bash")
	dir := t.TempDir()
	writeServerConfig(t, dir, srv.URL)
	writeAgents(t, dir, "AGENTS_SECRET_RULE：回答前先确认工作目录")
	a, err := buildApp(dir, testEnv(nil, shellSet(true, false)), nil)
	if err != nil || a.ag == nil {
		t.Fatalf("装配失败: %v / %s", err, a.disabled)
	}
	defer a.close()
	if err := a.ag.Run(t.Context(), agent.Input{Text: "你好"}, event.SinkFunc(func(event.Event) {})); err != nil {
		t.Fatal(err)
	}
	req := srv.bodies[0]
	// 请求体是 JSON，Windows 路径里的反斜杠会被转义，按同样方式编码后再比较。
	quoted, _ := json.Marshal(config.AgentsPath(dir))
	path := strings.Trim(string(quoted), `"`)
	for _, want := range []string{"## 用户与项目说明", "AGENTS_SECRET_RULE", path} {
		if !strings.Contains(req, want) {
			t.Errorf("请求里缺少 %q", want)
		}
	}
}
