package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
)

func TestRequestSendsBase64ImageBlock(t *testing.T) {
	req := agent.Request{Model: "m", Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Block{
		agent.TextBlock("看图"), agent.ImageBlock("image/png", "QUJD"),
	}}}}
	out := buildRequest(req)
	if len(out.Messages) != 1 || len(out.Messages[0].Content) != 2 {
		t.Fatalf("block 数 = %+v", out.Messages)
	}
	img, ok := out.Messages[0].Content[1].(imageBlock)
	if !ok {
		t.Fatalf("第二块应是 imageBlock，得到 %T", out.Messages[0].Content[1])
	}
	if img.Source.Type != "base64" || img.Source.MediaType != "image/png" || img.Source.Data != "QUJD" {
		t.Errorf("image source 不对: %+v", img.Source)
	}
	b, _ := json.Marshal(out.Messages[0])
	want := `{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}]}`
	if string(b) != want {
		t.Errorf("线格式:\n得到 %s\n期望 %s", b, want)
	}
}

func TestToolResultCarriesImages(t *testing.T) {
	req := agent.Request{Model: "m", Messages: []agent.Message{
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "t1", Name: "shot", Arguments: "{}"}}},
		{Role: agent.RoleTool, ToolID: "t1", Content: []agent.Block{
			agent.TextBlock("截好了"), agent.ImageBlock("image/jpeg", "QUJD"),
		}},
	}}
	out := buildRequest(req)
	tr, ok := out.Messages[len(out.Messages)-1].Content[0].(toolResultBlock)
	if !ok {
		t.Fatalf("应是 tool_result")
	}
	blocks, ok := tr.Content.([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("tool_result 带图片时 content 应是块数组: %+v", tr.Content)
	}
	if _, ok := blocks[1].(imageBlock); !ok {
		t.Errorf("第二块应是图片: %T", blocks[1])
	}
	b, _ := json.Marshal(tr)
	if !strings.Contains(string(b), `"type":"image"`) {
		t.Errorf("序列化后应含 image block: %s", b)
	}
}

func TestToolResultStaysStringWithoutImages(t *testing.T) {
	tr := toolResultBlock{Type: "tool_result", ToolUseID: "t", Content: toolResultContent(agent.NewTextMessage(agent.RoleTool, "ok"))}
	if s, ok := tr.Content.(string); !ok || s != "ok" {
		t.Errorf("没有图片时应保持字符串，兼容性最好: %+v", tr.Content)
	}
}
