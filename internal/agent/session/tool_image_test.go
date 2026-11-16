package session

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/tool"
)

// 工具结果里的图片：运行时进历史、落盘到 <会话名>.images/，重放后历史与运行时逐条一致。
func TestToolResultImagesAreRecordedAndReplayed(t *testing.T) {
	b64 := testPNG(t)
	shot := funcTool{name: "shot", run: func(context.Context) (tool.Result, error) {
		return tool.Result{Command: "shot", Stdout: "截图如下", Images: []tool.Image{{MediaType: "image/png", Data: b64}}}, nil
	}}
	var seen []agent.Message
	prov := funcProvider(func(req agent.Request) agent.Response {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == agent.RoleTool {
			seen = req.Messages
			return answer("看到了")
		}
		return calls("", agent.ToolCall{ID: "c1", Name: "shot", Arguments: "{}"})
	})

	dir := t.TempDir()
	rec := NewRecorder(dir, Info{Provider: "p", Model: "m", Cwd: `D:\proj`, Tools: []string{"shot"}}, nil)
	ag, err := agent.New(agent.Config{
		Provider: prov, Model: "m", System: "SYS", Recorder: rec, Vision: true,
		Tools: registryOf(t, shot), Retry: &agent.RetryPolicy{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.Run(context.Background(), agent.Input{Text: "截个图"}, nil); err != nil {
		t.Fatal(err)
	}
	rec.Close()

	tm := seen[len(seen)-1]
	if len(tm.Content) != 2 || tm.Content[0].Type != agent.BlockText || tm.Content[1].Type != agent.BlockImage || tm.Content[1].Data != b64 {
		t.Fatalf("模型应看到 文本 + 图片 的工具结果: %+v", tm.Content)
	}

	raw, err := os.ReadFile(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), b64) {
		t.Error("JSONL 里不应内联 base64")
	}
	var toolImgs []any
	for _, r := range readRecords(t, rec.Path()) {
		if r["type"] == "tool" {
			toolImgs, _ = r["images"].([]any)
		}
	}
	if len(toolImgs) != 1 {
		t.Fatalf("tool 记录应带 1 张图片引用: %s", raw)
	}

	st, err := Load(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.History, ag.History()) {
		t.Fatalf("重放历史与运行时不同：\n重放:   %+v\n运行时: %+v", st.History, ag.History())
	}
	for _, e := range st.Events {
		if f, ok := e.(event.ToolFinished); ok {
			if len(f.Images) != 1 || f.Images[0].MediaType != "image/png" || f.Images[0].Bytes == 0 || f.Images[0].Data != "" {
				t.Errorf("重放的 ToolFinished 应带图片说明且不含数据: %+v", f.Images)
			}
		}
	}
}

// 工具失败时不附图：结果里即使带了图片也不进历史。
func TestFailedToolDropsImages(t *testing.T) {
	bad := funcTool{name: "bad", run: func(context.Context) (tool.Result, error) {
		return tool.Result{Command: "bad", Images: []tool.Image{{MediaType: "image/png", Data: testPNG(t)}}}, os.ErrNotExist
	}}
	prov := funcProvider(func(req agent.Request) agent.Response {
		if req.Messages[len(req.Messages)-1].Role == agent.RoleTool {
			return answer("好")
		}
		return calls("", agent.ToolCall{ID: "c1", Name: "bad", Arguments: "{}"})
	})
	ag, err := agent.New(agent.Config{Provider: prov, Model: "m", System: "SYS", Vision: true,
		Tools: registryOf(t, bad), Retry: &agent.RetryPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.Run(context.Background(), agent.Input{Text: "跑"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range ag.History() {
		if m.Role == agent.RoleTool && m.HasImages() {
			t.Errorf("失败的工具结果不应带图片: %+v", m.Content)
		}
	}
}
