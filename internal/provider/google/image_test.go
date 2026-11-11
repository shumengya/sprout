package google

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
)

func TestRequestSendsInlineImage(t *testing.T) {
	req := agent.Request{Model: "m", Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Block{
		agent.TextBlock("看图"), agent.ImageBlock("image/png", "QUJD"),
	}}}}
	out := buildRequest(req)
	if len(out.Contents) != 1 || len(out.Contents[0].Parts) != 2 {
		t.Fatalf("parts = %+v", out.Contents)
	}
	p := out.Contents[0].Parts[1]
	if p.InlineData == nil || p.InlineData.MimeType != "image/png" || p.InlineData.Data != "QUJD" {
		t.Fatalf("第二段应是内联图片: %+v", p)
	}
	b, _ := json.Marshal(out.Contents[0])
	if !strings.Contains(string(b), `"inlineData":{"mimeType":"image/png","data":"QUJD"}`) {
		t.Errorf("线格式不对: %s", b)
	}
}

func TestToolResponseImagesComeAfterFunctionResponses(t *testing.T) {
	req := agent.Request{Model: "m", Messages: []agent.Message{
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "c1", Name: "shot", Arguments: "{}"}}},
		{Role: agent.RoleTool, ToolID: "c1", Content: []agent.Block{
			agent.TextBlock("截好了"), agent.ImageBlock("image/png", "QUJD"),
		}},
	}}
	out := buildRequest(req)

	// functionResponse.response 是 JSON 对象，图片塞不进去：结果文本里注明，图片用紧随的 user 消息补发。
	var fr, img *content
	for i := range out.Contents {
		c := &out.Contents[i]
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				fr = c
				var m map[string]string
				if err := json.Unmarshal(p.FunctionResponse.Response, &m); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(m["result"], "1 张图片") {
					t.Errorf("结果文本应注明附了图片: %q", m["result"])
				}
			}
			if p.InlineData != nil {
				img = c
			}
		}
	}
	if fr == nil || img == nil {
		t.Fatalf("应有 functionResponse 与图片: %+v", out.Contents)
	}
	if fr == img {
		t.Error("图片不能和 functionResponse 混在同一条消息里，否则响应不再是纯 functionResponse")
	}
}

func TestUserTextWithoutImagesHasNoInlineData(t *testing.T) {
	out := buildRequest(agent.Request{Model: "m", Messages: []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Block{agent.TextBlock("hi")}},
	}})
	if len(out.Contents[0].Parts) != 1 || out.Contents[0].Parts[0].InlineData != nil {
		t.Errorf("纯文本不应带 inlineData: %+v", out.Contents[0].Parts)
	}
}
