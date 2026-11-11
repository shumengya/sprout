package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
)

// imageReq 构造一条带图片的用户请求。
func imageReq(text, mediaType, b64 string) agent.Request {
	return agent.Request{
		Model: "m", Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Block{
			agent.TextBlock(text), agent.ImageBlock(mediaType, b64),
		}}},
	}
}

func TestChatRequestSendsImagesAsParts(t *testing.T) {
	req := imageReq("看图", "image/png", "QUJD")
	out := buildChatRequest(req)
	if len(out.Messages) != 1 {
		t.Fatalf("消息数 = %d", len(out.Messages))
	}
	parts, ok := out.Messages[0].Content.([]chatPart)
	if !ok {
		t.Fatalf("带图片时 content 应是 parts 数组，得到 %T", out.Messages[0].Content)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "看图" {
		t.Fatalf("第一段应是文本: %+v", parts)
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL.URL != "data:image/png;base64,QUJD" {
		t.Errorf("第二段应是 data URL 图片: %+v", parts[1])
	}

	// 纯文本仍发字符串，保证兼容性。
	if _, ok := buildChatRequest(userReq("hi")).Messages[0].Content.(string); !ok {
		t.Error("纯文本消息的 content 应保持字符串")
	}
}

func TestChatRequestHoistsToolImagesIntoUserMessage(t *testing.T) {
	req := agent.Request{Model: "m", Messages: []agent.Message{
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "c1", Name: "shot", Arguments: "{}"}}},
		{Role: agent.RoleTool, ToolID: "c1", Content: []agent.Block{
			agent.TextBlock("截好了"), agent.ImageBlock("image/png", "QUJD"),
		}},
	}}
	out := buildChatRequest(req)

	// tool 消息只能带文本；图片不能插在 tool 消息之间，要等这组结果之后再补一条 user 消息。
	var toolMsg *chatMessage
	var last *chatMessage
	for i := range out.Messages {
		if out.Messages[i].Role == "tool" {
			toolMsg = &out.Messages[i]
		}
	}
	last = &out.Messages[len(out.Messages)-1]
	if toolMsg == nil || toolMsg.Content != "截好了" {
		t.Fatalf("tool 消息应只带文本: %+v", toolMsg)
	}
	if last.Role != "user" {
		t.Fatalf("图片应用 user 消息补发，最后一条是 %q", last.Role)
	}
	parts, ok := last.Content.([]chatPart)
	if !ok || len(parts) != 2 || parts[1].Type != "image_url" {
		t.Fatalf("补发的消息应包含图片: %+v", last.Content)
	}

	// 序列化后字段名要符合 Chat 协议。
	b, err := json.Marshal(out.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"image_url":{"url":"data:image/png;base64,QUJD"}`) {
		t.Errorf("线格式不对: %s", b)
	}
}

func TestResponsesRequestSendsInputImage(t *testing.T) {
	out := buildResponsesRequest(imageReq("看图", "image/jpeg", "QUJD"))
	if len(out.Input) != 1 {
		t.Fatalf("input 条数 = %d", len(out.Input))
	}
	msg, ok := out.Input[0].(responsesMessage)
	if !ok {
		t.Fatalf("类型 = %T", out.Input[0])
	}
	parts, ok := msg.Content.([]responsesPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("content 应是 2 段: %+v", msg.Content)
	}
	if parts[0].Type != "input_text" || parts[1].Type != "input_image" || parts[1].ImageURL != "data:image/jpeg;base64,QUJD" {
		t.Errorf("线格式不对: %+v", parts)
	}
	b, _ := json.Marshal(msg)
	if !strings.Contains(string(b), `"type":"input_image"`) {
		t.Errorf("序列化后应出现 input_image: %s", b)
	}

	if s, ok := buildResponsesRequest(userReq("hi")).Input[0].(responsesMessage).Content.(string); !ok || s != "hi" {
		t.Error("纯文本消息的 content 应保持字符串")
	}
}
