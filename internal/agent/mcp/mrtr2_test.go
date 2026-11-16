package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 没接界面（run 模式等）时不声明能力；服务端仍要输入则明确失败，而不是卡住或乱答。
func TestMRTRWithoutHandlerFails(t *testing.T) {
	srv := newMRTRServer(t, map[string]any{"tools": map[string]any{}}, func(mrtrRequest) any {
		return elicitInput("q")
	})
	c, err := Dial(context.Background(), Spec{Name: "mrtr", URL: srv.URL}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(srv.clientCaps()) != 0 {
		t.Errorf("没有处理函数时不该声明能力：%v", srv.clientCaps())
	}
	_, err = c.Call(context.Background(), "ask", nil)
	if err == nil || !strings.Contains(err.Error(), "elicitation") {
		t.Fatalf("应报告无法处理 elicitation，得到 %v", err)
	}
	if n := len(srv.calls()); n != 1 {
		t.Errorf("无法回答时不该重发，服务端收到 %d 次", n)
	}
}

// 服务端一直要输入时有轮数上限。
func TestMRTRRoundCap(t *testing.T) {
	srv := newMRTRServer(t, map[string]any{"tools": map[string]any{}}, func(mrtrRequest) any {
		return elicitInput("q")
	})
	asked := 0
	c, err := Dial(context.Background(), Spec{Name: "mrtr", URL: srv.URL,
		Elicit: func(context.Context, string, ElicitRequest) (ElicitResult, error) {
			asked++
			return ElicitResult{Action: ElicitAccept}, nil
		}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Call(context.Background(), "ask", nil)
	if err == nil || !strings.Contains(err.Error(), "放弃") {
		t.Fatalf("应因轮数过多放弃，得到 %v", err)
	}
	if asked != maxInputRounds {
		t.Errorf("问了用户 %d 次，期望 %d", asked, maxInputRounds)
	}
}

// 既没有 inputRequests 也没有 requestState 的 input_required 不合法。
func TestMRTREmptyInputRequiredIsError(t *testing.T) {
	srv := newMRTRServer(t, map[string]any{"tools": map[string]any{}}, func(mrtrRequest) any {
		return map[string]any{"resultType": "input_required"}
	})
	c, err := Dial(context.Background(), Spec{Name: "mrtr", URL: srv.URL}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Call(context.Background(), "ask", nil); err == nil {
		t.Fatal("空的 input_required 应报错")
	}
}

// 采样走当前模型：请求转成 SampleRequest，结果按 CreateMessageResult 格式回给服务端。
// 同一次 input_required 里既有 sampling 又有 roots/list 时都要答。
func TestMRTRSamplingAndRoots(t *testing.T) {
	srv := newMRTRServer(t, map[string]any{"tools": map[string]any{}}, func(req mrtrRequest) any {
		if _, ok := req.Params["inputResponses"]; !ok {
			return map[string]any{
				"resultType": "input_required",
				"inputRequests": map[string]any{
					"s": map[string]any{"method": "sampling/createMessage", "params": map[string]any{
						"messages":     []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "总结一下"}}},
						"systemPrompt": "简短",
						"maxTokens":    50,
						"modelPreferences": map[string]any{
							"hints": []any{map[string]any{"name": "claude-3"}},
						},
					}},
					"r": map[string]any{"method": "roots/list"},
				},
			}
		}
		return textResult(string(req.Params["inputResponses"]))
	})
	var got SampleRequest
	c, err := Dial(context.Background(), Spec{Name: "mrtr", URL: srv.URL,
		Sample: func(_ context.Context, _ string, req SampleRequest) (SampleResult, error) {
			got = req
			return SampleResult{Text: "摘要", Model: "m1"}, nil
		}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, ok := srv.clientCaps()["sampling"]; !ok {
		t.Errorf("应声明 sampling：%v", srv.clientCaps())
	}
	res, err := c.Call(context.Background(), "ask", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SystemPrompt != "简短" || got.MaxTokens != 50 || len(got.Messages) != 1 ||
		got.Messages[0].Text != "总结一下" || len(got.ModelHints) != 1 || got.ModelHints[0] != "claude-3" {
		t.Errorf("采样请求 = %+v", got)
	}
	var responses map[string]json.RawMessage
	if err := json.Unmarshal([]byte(res.Content[0].Text), &responses); err != nil {
		t.Fatal(err)
	}
	var sample struct {
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stopReason"`
		Content    struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(responses["s"], &sample); err != nil {
		t.Fatal(err)
	}
	if sample.Role != "assistant" || sample.Content.Text != "摘要" || sample.Content.Type != "text" ||
		sample.Model != "m1" || sample.StopReason != "endTurn" {
		t.Errorf("CreateMessageResult = %s", responses["s"])
	}
	if string(responses["r"]) != `{"roots":[]}` {
		t.Errorf("roots/list 应回空列表，得到 %s", responses["r"])
	}
}

// resources/read 与 prompts/get 同样支持 MRTR。
func TestMRTROnResourcesAndPrompts(t *testing.T) {
	srv := newMRTRServer(t, map[string]any{"resources": map[string]any{}, "prompts": map[string]any{}}, func(req mrtrRequest) any {
		if _, ok := req.Params["inputResponses"]; !ok {
			return elicitInput("q")
		}
		if req.Method == "resources/read" {
			return map[string]any{"contents": []map[string]any{{"uri": "file:///a", "text": "资源正文"}}}
		}
		return map[string]any{"messages": []map[string]any{{"role": "user", "content": map[string]any{"type": "text", "text": "提示词正文"}}}}
	})
	c, err := Dial(context.Background(), Spec{Name: "mrtr", URL: srv.URL,
		Elicit: func(context.Context, string, ElicitRequest) (ElicitResult, error) {
			return ElicitResult{Action: ElicitAccept}, nil
		}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	contents, err := c.ReadResource(context.Background(), "file:///a")
	if err != nil || len(contents) != 1 || contents[0].Text != "资源正文" {
		t.Fatalf("读资源 = %+v, %v", contents, err)
	}
	msgs, err := c.GetPrompt(context.Background(), "p", map[string]string{"k": "v"})
	if err != nil || len(msgs) != 1 || msgs[0].Content.Text != "提示词正文" {
		t.Fatalf("取提示词 = %+v, %v", msgs, err)
	}
	if !c.HasPrompts() || !c.HasResources() {
		t.Errorf("应识别出 prompts 与 resources 能力")
	}
}
