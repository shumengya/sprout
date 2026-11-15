package google

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

func TestThoughtSignatureIsSavedAndReplayed(t *testing.T) {
	// Gemini 3 要求带 thoughtSignature 的 functionCall part 原样回传，否则报错。
	srv := newServer(t, 0, sse(
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a"}},"thoughtSignature":"sig-1"}]},"finishReason":"STOP"}]}`,
	))
	resp, _, _, err := collect(t, newProvider(t, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	n := resp.Message.Native
	if n == nil || n.Protocol != config.ProtocolGoogle {
		t.Fatalf("应保存带签名的 part: %+v", n)
	}
	if !strings.Contains(string(n.Data), `"thoughtSignature":"sig-1"`) {
		t.Errorf("Native.Data 应保留签名: %s", n.Data)
	}

	// 下一轮请求里那个 part 要带着签名回去，位置在同一条 model 消息里。
	req := userReq("hi")
	req.Messages = append(req.Messages, agent.Message{
		Role: agent.RoleAssistant, Content: []agent.Block{agent.TextBlock("答")},
		ToolCalls: resp.Message.ToolCalls, Native: n,
	})
	b := buildRequest(req)
	last := b.Contents[len(b.Contents)-1]
	if last.Role != "model" || len(last.Parts) != 1 || last.Parts[0].ThoughtSignature != "sig-1" {
		data, _ := json.Marshal(last)
		t.Errorf("应原样回传带签名的 part: %s", data)
	}
}

func TestNoSignatureMeansNoNative(t *testing.T) {
	srv := newServer(t, 0, sse(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"答"}]},"finishReason":"STOP"}]}`,
	))
	resp, _, _, err := collect(t, newProvider(t, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Native != nil {
		t.Errorf("没有签名时不该生成 Native: %s", resp.Message.Native.Data)
	}
}

func TestNativeFromAnotherModelIsIgnored(t *testing.T) {
	native := &agent.Native{Protocol: config.ProtocolGoogle, Model: "other",
		Data: json.RawMessage(`[{"functionCall":{"name":"read"},"thoughtSignature":"sig"}]`)}
	req := userReq("hi")
	req.Messages = append(req.Messages, agent.Message{
		Role: agent.RoleAssistant, Content: []agent.Block{agent.TextBlock("答")},
		ToolCalls: []agent.ToolCall{{ID: "x", Name: "read", Arguments: `{}`}}, Native: native,
	})
	b := buildRequest(req)
	last := b.Contents[len(b.Contents)-1]
	if len(last.Parts) == 1 && last.Parts[0].ThoughtSignature != "" {
		t.Errorf("换模型后不该回传旧签名: %+v", last.Parts)
	}
}

func TestAppendNativeMergesTextAndDropsUnsignedThought(t *testing.T) {
	var parts []part
	parts = appendNative(parts, part{Text: "a", Thought: true}) // 思维摘要不回传
	parts = appendNative(parts, part{FunctionCall: &functionCall{Name: "read"}, ThoughtSignature: "s1"})
	parts = appendNative(parts, part{Text: "你"}) // 逐字增量合并成一个 part
	parts = appendNative(parts, part{Text: "好"})
	if len(parts) != 2 {
		t.Fatalf("parts = %+v，期望 2 个（functionCall + 合并后的文本）", parts)
	}
	if parts[1].Text != "你好" {
		t.Errorf("相邻纯文本应合并: %q", parts[1].Text)
	}
}
