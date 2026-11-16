package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shumengya/sprout/internal/agent/mcp"
	"github.com/shumengya/sprout/internal/tui"
)

func TestParseElicitSchema(t *testing.T) {
	fields, err := parseElicitSchema(json.RawMessage(`{
		"type":"object",
		"properties":{
			"env":{"type":"string","title":"环境","description":"部署到哪","default":"dev"},
			"count":{"type":"integer","default":3},
			"rate":{"type":"number"},
			"ok":{"type":"boolean","default":true},
			"level":{"type":"string","enum":["a","b"],"enumNames":["甲","乙"],"default":"b"}
		},
		"required":["env","level"]}`))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]tui.AskField{}
	var order []string
	for _, f := range fields {
		by[f.Name] = f
		order = append(order, f.Name)
	}
	// 顺序是服务端安排的，不能被打乱成字母序。
	if strings.Join(order, ",") != "env,count,rate,ok,level" {
		t.Errorf("字段顺序 = %v", order)
	}
	if len(fields) != 5 {
		t.Fatalf("字段数 = %d", len(fields))
	}
	if f := by["env"]; f.Kind != "string" || !f.Required || f.Title != "环境" || f.Desc != "部署到哪" || f.Default != "dev" {
		t.Errorf("env = %+v", f)
	}
	if f := by["count"]; f.Kind != "integer" || f.Default != int64(3) || f.Required {
		t.Errorf("count = %+v", f)
	}
	if f := by["rate"]; f.Kind != "number" || f.Default != nil {
		t.Errorf("rate = %+v", f)
	}
	if f := by["ok"]; f.Kind != "boolean" || f.Default != true {
		t.Errorf("ok = %+v", f)
	}
	if f := by["level"]; f.Kind != "enum" || len(f.Choices) != 2 || f.Labels[0] != "甲" || f.Default != "b" || !f.Required {
		t.Errorf("level = %+v", f)
	}
}

func TestParseElicitSchemaRejects(t *testing.T) {
	for name, in := range map[string]string{
		"嵌套对象":    `{"type":"object","properties":{"a":{"type":"object"}}}`,
		"数组":      `{"type":"object","properties":{"a":{"type":"array"}}}`,
		"顶层不是对象":  `{"type":"string"}`,
		"不是 JSON": `[`,
	} {
		if _, err := parseElicitSchema(json.RawMessage(in)); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
	if fields, err := parseElicitSchema(nil); err != nil || fields != nil {
		t.Errorf("没有 schema 是纯确认：%v, %v", fields, err)
	}
}

// 回答、界面退出、上下文取消、队列满，四种结局都不能让调用方卡住。
func TestMCPInputAsk(t *testing.T) {
	in := newMCPInput(false)
	go func() {
		a := <-in.asks
		a.Reply <- tui.AskAnswer{Action: tui.AskAccept}
	}()
	ans, err := in.ask(context.Background(), &tui.Ask{})
	if err != nil || ans.Action != tui.AskAccept {
		t.Fatalf("正常回答 = %+v, %v", ans, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := in.ask(ctx, &tui.Ask{}); done <- err }()
	<-in.asks // 界面收到了但一直不回答
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("取消上下文应返回错误")
		}
	case <-time.After(time.Second):
		t.Fatal("取消后仍卡住")
	}

	full := newMCPInput(false)
	for i := 0; i < askCap; i++ {
		full.asks <- &tui.Ask{}
	}
	if _, err := full.ask(context.Background(), &tui.Ask{}); err == nil || !strings.Contains(err.Error(), "太多") {
		t.Errorf("队列满应拒绝：%v", err)
	}

	in.close()
	if _, err := in.ask(context.Background(), &tui.Ask{}); err == nil || !strings.Contains(err.Error(), "退出") {
		t.Errorf("关闭后应拒绝：%v", err)
	}
}

// answerAsks 在后台充当界面：把收到的请求交给 fn 决定回答，并记录请求。
func answerAsks(in *mcpInput, fn func(*tui.Ask) tui.AskAnswer) (seen func() []*tui.Ask) {
	var mu sync.Mutex
	var got []*tui.Ask
	go func() {
		for a := range in.asks {
			mu.Lock()
			got = append(got, a)
			mu.Unlock()
			a.Reply <- fn(a)
		}
	}()
	return func() []*tui.Ask {
		mu.Lock()
		defer mu.Unlock()
		return append([]*tui.Ask(nil), got...)
	}
}

// 返回一个只回一句话的 Chat Completions 假服务，并记录请求体。
func newSampleModelServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"这是摘要\"}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func interactiveApp(t *testing.T, baseURL string) *app {
	t.Helper()
	dir := t.TempDir()
	writeServerConfig(t, dir, baseURL)
	e := testEnv(nil, shellSet(true, true))
	e.interactive = true
	a, err := buildApp(dir, e, nil)
	if err != nil || a.ag == nil || a.input == nil {
		t.Fatalf("装配失败: %v disabled=%q", err, a.disabled)
	}
	t.Cleanup(a.close)
	return a
}

func TestElicitRoundTrip(t *testing.T) {
	a := interactiveApp(t, "http://127.0.0.1:1")
	seen := answerAsks(a.input, func(*tui.Ask) tui.AskAnswer {
		return tui.AskAnswer{Action: tui.AskAccept, Values: map[string]any{"env": "prod"}}
	})
	res, err := a.elicit(context.Background(), "deploy", mcp.ElicitRequest{
		Message: "填一下", Schema: json.RawMessage(`{"type":"object","properties":{"env":{"type":"string"}},"required":["env"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != mcp.ElicitAccept || res.Content["env"] != "prod" {
		t.Errorf("结果 = %+v", res)
	}
	got := seen()
	if len(got) != 1 || got[0].Server != "deploy" || got[0].Message != "填一下" || len(got[0].Fields) != 1 || !got[0].Fields[0].Required {
		t.Errorf("界面收到 %+v", got)
	}

	// 服务端给的 schema 不受支持：直接报错，不打扰用户。
	if _, err := a.elicit(context.Background(), "deploy", mcp.ElicitRequest{Schema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"array"}}}`)}); err == nil {
		t.Error("不支持的字段类型应报错")
	}
	if n := len(seen()); n != 1 {
		t.Errorf("无效 schema 不该弹出表单，界面共收到 %d 次", n)
	}
}

// 用户拒绝：错误里说明被拒绝，并且不得调用模型。确认框要列出服务端想让模型处理的内容。
func TestSampleDeclined(t *testing.T) {
	srv, bodies := newSampleModelServer(t)
	a := interactiveApp(t, srv.URL)
	seen := answerAsks(a.input, func(*tui.Ask) tui.AskAnswer { return tui.AskAnswer{Action: tui.AskDecline} })
	_, err := a.sample(context.Background(), "s", mcp.SampleRequest{
		SystemPrompt: "简短回答", MaxTokens: 77, ModelHints: []string{"claude-3"},
		Messages: []mcp.SampleMessage{{Role: "user", Text: "总结一下这段话"}},
	})
	if err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("拒绝应返回错误，得到 %v", err)
	}
	if n := len(bodies()); n != 0 {
		t.Fatalf("用户拒绝后模型仍被调用了 %d 次", n)
	}
	got := seen()
	if len(got) != 1 || len(got[0].Fields) != 0 {
		t.Fatalf("界面收到 %+v", got)
	}
	for _, want := range []string{"总结一下这段话", "claude-3", "简短回答", "77"} {
		if !strings.Contains(got[0].Message, want) {
			t.Errorf("确认框缺少 %q：%s", want, got[0].Message)
		}
	}
}

func TestSampleApproved(t *testing.T) {
	srv, bodies := newSampleModelServer(t)
	a := interactiveApp(t, srv.URL)
	answerAsks(a.input, func(*tui.Ask) tui.AskAnswer { return tui.AskAnswer{Action: tui.AskAccept} })
	res, err := a.sample(context.Background(), "s", mcp.SampleRequest{
		SystemPrompt: "简短回答", MaxTokens: 77,
		Messages: []mcp.SampleMessage{{Role: "user", Text: "总结一下这段话"}, {Role: "assistant", Text: "好"}, {Role: "user", Text: "继续"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "这是摘要" || res.Model != a.sel.Ref() || res.StopReason != "endTurn" {
		t.Errorf("结果 = %+v", res)
	}
	got := bodies()
	if len(got) != 1 {
		t.Fatalf("模型请求数 = %d", len(got))
	}
	for _, want := range []string{"简短回答", "总结一下这段话", `"assistant"`, "继续", `"max_tokens":77`, a.sel.ModelName} {
		if !strings.Contains(got[0], want) {
			t.Errorf("请求体缺少 %q：%s", want, got[0])
		}
	}
	if strings.Contains(got[0], `"tools"`) {
		t.Errorf("采样请求不该带工具：%s", got[0])
	}
}

// 取消（Esc）和拒绝一样不调用模型，但错误说明不同。
func TestSampleCancelled(t *testing.T) {
	srv, bodies := newSampleModelServer(t)
	a := interactiveApp(t, srv.URL)
	answerAsks(a.input, func(*tui.Ask) tui.AskAnswer { return tui.AskAnswer{Action: tui.AskCancel} })
	_, err := a.sample(context.Background(), "s", mcp.SampleRequest{Messages: []mcp.SampleMessage{{Role: "user", Text: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "取消") || len(bodies()) != 0 {
		t.Fatalf("err = %v，模型请求 %d 次", err, len(bodies()))
	}
}

// 只有接了界面才声明能力；sprout run 与 serve 里服务端发来的请求不会卡在没人回答的通道上。
func TestSpecTweakGatesCapabilities(t *testing.T) {
	dir := t.TempDir()
	writeServerConfig(t, dir, "http://127.0.0.1:1")
	for _, interactive := range []bool{false, true} {
		e := testEnv(nil, shellSet(true, true))
		e.interactive = interactive
		a, err := buildApp(dir, e, nil)
		if err != nil {
			t.Fatal(err)
		}
		var spec mcp.Spec
		a.specTweak()(&spec)
		if got := spec.Elicit != nil && spec.Sample != nil; got != interactive {
			t.Errorf("interactive=%v 时 Elicit/Sample 接入 = %v", interactive, got)
		}
		a.close()
	}
}
