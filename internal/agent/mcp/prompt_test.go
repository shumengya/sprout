package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func promptServer(t *testing.T, caps map[string]any) *mrtrServer {
	t.Helper()
	png := pngBase64(t)
	return newMRTRServer(t, caps, func(req mrtrRequest) any {
		switch req.Method {
		case "prompts/list":
			// 两页：验证翻页。
			if string(req.Params["cursor"]) == `"p2"` {
				return map[string]any{"prompts": []map[string]any{{"name": "review", "title": "代码审查"}}}
			}
			return map[string]any{
				"prompts": []map[string]any{{
					"name": "greet", "description": "打招呼\n多行描述",
					"arguments": []map[string]any{
						{"name": "who", "description": "对象", "required": true},
						{"name": "tone"},
					},
				}},
				"nextCursor": "p2",
			}
		case "prompts/get":
			var args map[string]string
			_ = json.Unmarshal(req.Params["arguments"], &args)
			return map[string]any{
				"description": "d",
				"messages": []map[string]any{
					{"role": "user", "content": map[string]any{"type": "text", "text": "你好，" + args["who"]}},
					{"role": "assistant", "content": map[string]any{"type": "image", "data": png, "mimeType": "image/png"}},
				},
			}
		}
		return textResult("?")
	})
}

func TestListAndGetPrompts(t *testing.T) {
	srv := promptServer(t, map[string]any{"prompts": map[string]any{}})
	c, err := Dial(context.Background(), Spec{Name: "p", URL: srv.URL}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.HasPrompts() {
		t.Fatal("应识别出 prompts 能力")
	}
	prompts, err := c.ListPrompts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || prompts[0].Name != "greet" || prompts[1].Title != "代码审查" {
		t.Fatalf("提示词 = %+v", prompts)
	}
	if len(prompts[0].Arguments) != 2 || !prompts[0].Arguments[0].Required || prompts[0].Arguments[1].Required {
		t.Errorf("参数 = %+v", prompts[0].Arguments)
	}

	msgs, err := c.GetPrompt(context.Background(), "greet", map[string]string{"who": "小明"})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Content.Text != "你好，小明" || msgs[1].Role != "assistant" {
		t.Errorf("消息 = %+v", msgs)
	}
}

func TestPromptToolsRender(t *testing.T) {
	srv := promptServer(t, map[string]any{"prompts": map[string]any{}})
	c, err := Dial(context.Background(), Spec{Name: "p", URL: srv.URL}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tools := PromptTools(c)
	if len(tools) != 2 || tools[0].Name() != "mcp__p__list_prompts" || tools[1].Name() != "mcp__p__get_prompt" {
		t.Fatalf("工具 = %v", tools)
	}

	res, err := tools[0].Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"greet：打招呼 多行描述", "who（必填）：对象", "tone（可选）", "review（代码审查）"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("列表缺少 %q：\n%s", want, res.Stdout)
		}
	}

	res, err = tools[1].Run(context.Background(), json.RawMessage(`{"name":"greet","arguments":{"who":"小红"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "--- user ---\n你好，小红") || !strings.Contains(res.Stdout, "--- assistant ---") {
		t.Errorf("渲染 = %q", res.Stdout)
	}
	if len(res.Images) != 1 || res.Images[0].MediaType != "image/png" {
		t.Errorf("图片 = %+v", res.Images)
	}

	if _, err := tools[1].Run(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("缺少 name 应报错")
	}
}

func TestPromptToolsNeedCapability(t *testing.T) {
	srv := promptServer(t, map[string]any{"tools": map[string]any{}})
	c, err := Dial(context.Background(), Spec{Name: "p", URL: srv.URL}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.HasPrompts() || PromptTools(c) != nil {
		t.Error("没声明 prompts 能力时不该注册提示词工具")
	}
}
