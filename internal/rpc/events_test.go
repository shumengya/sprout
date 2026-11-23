package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

func TestToolFinishedWireCarriesImageMetaWithoutData(t *testing.T) {
	m := wireEvent(1, event.ToolFinished{ID: "c1", Name: "shot", Images: []event.ImageInfo{
		{MediaType: "image/png", Bytes: 12, Data: "QUJD"},
	}})
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"images":[{"name":"","mediaType":"image/png","bytes":12}]`) || strings.Contains(string(b), "QUJD") {
		t.Errorf("tool.finished 的 images 应只有元数据: %s", b)
	}

	b, _ = json.Marshal(wireEvent(1, event.ToolFinished{ID: "c2"}))
	if !strings.Contains(string(b), `"images":[]`) {
		t.Errorf("没有图片时应是空数组: %s", b)
	}
}

func TestUserQueuedWireLikeTurnStarted(t *testing.T) {
	b, err := json.Marshal(wireEvent(2, event.UserQueued{Prompt: "补充", Images: []event.ImageInfo{{Name: "a.png", MediaType: "image/png", Bytes: 3, Data: "QUJD"}}}))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"kind":"user.queued"`) || !strings.Contains(s, `"prompt":"补充"`) ||
		!strings.Contains(s, `"name":"a.png"`) || strings.Contains(s, "QUJD") {
		t.Errorf("user.queued 应带 prompt 和图片元数据、不带 base64: %s", s)
	}
}

func TestSubagentWireNestsInnerEvent(t *testing.T) {
	m := wireEvent(3, event.Subagent{CallID: "call_1", Agent: "explore", Event: event.ToolRequested{
		Call: event.ToolCall{ID: "x", Name: "read", Arguments: `{"path":"a"}`},
	}})
	b, _ := json.Marshal(m)
	want := `{"agent":"explore","event":{"arguments":"{\"path\":\"a\"}","id":"x","kind":"tool.requested","name":"read"},"id":"call_1","kind":"subagent","turn":3}`
	if string(b) != want {
		t.Errorf("subagent 事件\n得到 %s\n期望 %s", b, want)
	}
	if wireEvent(1, event.Subagent{CallID: "c", Event: unknownEvent{}}) != nil {
		t.Error("内层不认识的事件不推送")
	}
}

type unknownEvent struct{}

func (unknownEvent) Kind() event.Kind { return "test.unknown" }

// 后台调用：tool.finished 带 background 标记；结果以 background.finished 推送，字段与 tool.finished 相同，另带写进历史的 message。
func TestBackgroundWireEvents(t *testing.T) {
	b, _ := json.Marshal(wireEvent(1, event.ToolFinished{ID: "c1", Name: "agent", Content: "占位", Background: true}))
	if !strings.Contains(string(b), `"background":true`) || !strings.Contains(string(b), `"kind":"tool.finished"`) {
		t.Errorf("tool.finished 应带 background: %s", b)
	}
	b, _ = json.Marshal(wireEvent(1, event.BackgroundFinished{
		Result:  event.ToolFinished{ID: "c1", Name: "agent", Stdout: "报告", ExitCode: 0},
		Message: "[后台任务已结束：agent，任务编号 c1]",
	}))
	s := string(b)
	for _, want := range []string{`"kind":"background.finished"`, `"id":"c1"`, `"stdout":"报告"`, `"exitCode":0`, `"message":"[后台任务已结束`, `"images":[]`} {
		if !strings.Contains(s, want) {
			t.Errorf("background.finished 缺少 %s: %s", want, s)
		}
	}
}
