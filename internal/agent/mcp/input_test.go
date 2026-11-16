package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseSampleRequest(t *testing.T) {
	// content 可以是数组；非文本内容换成占位文字，不丢整条消息。
	req, err := parseSampleRequest(json.RawMessage(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"看图："},{"type":"image","data":"xx","mimeType":"image/png"}]},
			{"role":"assistant","content":{"type":"text","text":"好的"}}
		],"maxTokens":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "user" || req.Messages[1].Role != "assistant" {
		t.Fatalf("消息 = %+v", req.Messages)
	}
	if !strings.HasPrefix(req.Messages[0].Text, "看图：") || !strings.Contains(req.Messages[0].Text, "image") {
		t.Errorf("图片应有占位文字：%q", req.Messages[0].Text)
	}

	bad := map[string]string{
		"空消息":        `{"messages":[]}`,
		"未知角色":       `{"messages":[{"role":"system","content":{"type":"text","text":"x"}}]}`,
		"带 tool_use": `{"messages":[{"role":"assistant","content":[{"type":"tool_use"}]}]}`,
		"不是对象":       `[]`,
	}
	for name, in := range bad {
		if _, err := parseSampleRequest(json.RawMessage(in)); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
}

func TestClientCapabilities(t *testing.T) {
	if got := (Spec{}).clientCapabilities(); len(got) != 0 {
		t.Errorf("默认不声明任何能力：%v", got)
	}
	got := Spec{
		Elicit: func(context.Context, string, ElicitRequest) (ElicitResult, error) { return ElicitResult{}, nil },
		Sample: func(context.Context, string, SampleRequest) (SampleResult, error) { return SampleResult{}, nil },
	}.clientCapabilities()
	if _, ok := got["elicitation"]; !ok {
		t.Error("缺 elicitation")
	}
	if _, ok := got["sampling"]; !ok {
		t.Error("缺 sampling")
	}
}

// input 的各个分支：未启用的能力回 -32601，url 模式回 -32602，用户拒绝采样回 -1。
func TestInputErrors(t *testing.T) {
	c := &Client{spec: Spec{Name: "s"}, life: context.Background()}
	if _, rerr := c.input(context.Background(), "elicitation/create", json.RawMessage(`{}`)); rerr == nil || rerr.Code != -32601 {
		t.Errorf("未启用 elicitation 应回 -32601：%+v", rerr)
	}
	if _, rerr := c.input(context.Background(), "sampling/createMessage", json.RawMessage(`{}`)); rerr == nil || rerr.Code != -32601 {
		t.Errorf("未启用 sampling 应回 -32601：%+v", rerr)
	}
	if _, rerr := c.input(context.Background(), "unknown/method", nil); rerr == nil || rerr.Code != -32601 {
		t.Errorf("未知方法应回 -32601：%+v", rerr)
	}
	if res, rerr := c.input(context.Background(), "ping", nil); rerr != nil || res == nil {
		t.Errorf("ping = %v, %+v", res, rerr)
	}

	c.spec.Elicit = func(context.Context, string, ElicitRequest) (ElicitResult, error) {
		return ElicitResult{Action: "bogus"}, nil
	}
	if _, rerr := c.input(context.Background(), "elicitation/create", json.RawMessage(`{"message":"m"}`)); rerr == nil {
		t.Error("未知动作应报错")
	}
	if _, rerr := c.input(context.Background(), "elicitation/create", json.RawMessage(`{"mode":"url","message":"m"}`)); rerr == nil || rerr.Code != -32602 {
		t.Errorf("url 模式应回 -32602：%+v", rerr)
	}

	c.spec.Sample = func(context.Context, string, SampleRequest) (SampleResult, error) {
		return SampleResult{}, context.Canceled
	}
	params := json.RawMessage(`{"messages":[{"role":"user","content":{"type":"text","text":"x"}}]}`)
	if _, rerr := c.input(context.Background(), "sampling/createMessage", params); rerr == nil || rerr.Code != codeUserRejected {
		t.Errorf("采样失败应回 %d：%+v", codeUserRejected, rerr)
	}
}

// 等用户回答期间进行中请求的计时要暂停，回答完恢复剩余时间。
func TestTimeoutPausesWhileWaitingForInput(t *testing.T) {
	c := &Client{spec: Spec{Timeout: 200_000_000}} // 200ms
	ctx, done := c.withTimeout(context.Background())
	defer done()

	c.beginInput()
	select {
	case <-ctx.Done():
		t.Fatal("等用户回答期间不该超时")
	case <-time.After(400 * time.Millisecond):
	}
	c.endInput()
	select {
	case <-ctx.Done():
		if cause := context.Cause(ctx); cause != context.DeadlineExceeded {
			t.Errorf("原因 = %v", cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("恢复计时后应在剩余时间内超时")
	}
}
