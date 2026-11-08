package openai

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

func respDelta(idx int, s string) string {
	return `{"type":"response.output_text.delta","output_index":` + strconv.Itoa(idx) + `,"delta":"` + s + `"}`
}

const completedUsage = `{"type":"response.completed","response":{"status":"completed",` +
	`"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13},"output":[]}}`

func TestResponsesStreamsTextAndUsage(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"type":"response.created","response":{"status":"in_progress"}}`,
		respDelta(0, "你"), respDelta(0, "好"),
		completedUsage,
	)))
	resp, deltas, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "你|好" || resp.Message.Content != "你好" {
		t.Errorf("文本 = %q / %q", deltas, resp.Message.Content)
	}
	if resp.StopReason != agent.StopStop || resp.Usage != (agent.Usage{InputTokens: 9, OutputTokens: 4, TotalTokens: 13}) {
		t.Errorf("响应 = %+v", resp)
	}
}

func TestResponsesRequestShape(t *testing.T) {
	srv := newServer(t, okBody(sse(completedUsage)))
	req := userReq("找入口")
	req.Tools = []agent.ToolSpec{{Name: "bash", Description: "run", Parameters: []byte(`{"type":"object"}`)}}
	req.Messages = append(req.Messages,
		agent.Message{Role: agent.RoleAssistant, Content: "我来看看", ToolCalls: []agent.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"ls"}`}}},
		agent.Message{Role: agent.RoleTool, ToolID: "c1", Content: "exit_code: 0"},
	)
	if _, _, err := collect(newProvider(t, config.ProtocolResponses, srv), req); err != nil {
		t.Fatal(err)
	}

	got := srv.reqs[0]
	if got.Path != "/v1/responses" {
		t.Errorf("路径 = %s", got.Path)
	}
	b := got.json(t)
	if b["instructions"] != "SYS" || b["stream"] != true || b["max_output_tokens"] != float64(77) {
		t.Errorf("基础字段 = %v", b)
	}
	if v, ok := b["store"]; !ok || v != false {
		t.Errorf("必须显式发送 store:false，得到 %v (存在=%v)", v, ok)
	}
	if _, ok := b["messages"]; ok {
		t.Error("Responses 请求不应带 messages 字段")
	}
	tool := asMap(asList(b["tools"])[0])
	if tool["type"] != "function" || tool["name"] != "bash" || tool["strict"] != false {
		t.Errorf("工具应是扁平结构且 strict:false: %v", tool)
	}
	if _, nested := tool["function"]; nested {
		t.Error("Responses 的工具声明不应嵌套在 function 下")
	}

	in := asList(b["input"])
	if len(in) != 4 {
		t.Fatalf("input 应有 4 项，得到 %v", in)
	}
	if asMap(in[0])["role"] != "user" || asMap(in[1])["role"] != "assistant" || asMap(in[1])["content"] != "我来看看" {
		t.Errorf("消息项 = %v / %v", in[0], in[1])
	}
	fc := asMap(in[2])
	if fc["type"] != "function_call" || fc["call_id"] != "c1" || fc["name"] != "bash" || fc["arguments"] != `{"command":"ls"}` {
		t.Errorf("function_call = %v", fc)
	}
	if _, has := fc["id"]; has {
		t.Error("store:false 时 function_call 不应带 id")
	}
	out := asMap(in[3])
	if out["type"] != "function_call_output" || out["call_id"] != "c1" || out["output"] != "exit_code: 0" {
		t.Errorf("function_call_output = %v", out)
	}
}

func TestResponsesEmptyInputIsArrayNotNull(t *testing.T) {
	srv := newServer(t, okBody(sse(completedUsage)))
	if _, _, err := collect(newProvider(t, config.ProtocolResponses, srv), agent.Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if asList(srv.reqs[0].json(t)["input"]) == nil {
		t.Error("input 不能序列化成 null")
	}
}

const fcAdded = `{"type":"response.output_item.added","output_index":0,` +
	`"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":""}}`

func TestResponsesFunctionCallFromArgumentEvents(t *testing.T) {
	srv := newServer(t, okBody(sse(
		fcAdded,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"command\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"\"ls\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_1","arguments":"{\"command\":\"ls\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}"}}`,
		completedUsage,
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	if err != nil {
		t.Fatal(err)
	}
	want := agent.ToolCall{ID: "call_1", Name: "bash", Arguments: `{"command":"ls"}`}
	if resp.StopReason != agent.StopToolCalls || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0] != want {
		t.Fatalf("同一个调用经过 added/delta/done/item.done 多个事件只应得到一次: %+v", resp)
	}
}

func TestResponsesFunctionCallFromDeltasOnly(t *testing.T) {
	srv := newServer(t, okBody(sse(
		fcAdded,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"a\""}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":":1}"}`,
		completedUsage,
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	if err != nil || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Arguments != `{"a":1}` {
		t.Fatalf("仅有增量事件时应拼出完整参数: %+v, %v", resp, err)
	}
}

func TestResponsesFunctionCallFromItemDoneOnly(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_9","name":"bash","arguments":"{\"x\":1}"}}`,
		completedUsage,
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	if err != nil || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].ID != "call_9" {
		t.Fatalf("只有 output_item.done 也应拿到完整 item: %+v, %v", resp, err)
	}
}

func TestResponsesFunctionCallFromCompletedOutputOnly(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"type":"response.completed","response":{"status":"completed","output":[`+
			`{"type":"reasoning","id":"rs_1"},`+
			`{"type":"function_call","call_id":"call_2","name":"powershell","arguments":"{\"command\":\"dir\"}"}]}}`,
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	if err != nil || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "powershell" {
		t.Fatalf("流里漏发时应由 completed.output 兜底，reasoning 条目应被忽略: %+v, %v", resp, err)
	}
}

func TestResponsesParallelCallsKeepOutputOrder(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","call_id":"c","name":"bash","arguments":"{}"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"b","name":"bash","arguments":"{}"}}`,
		completedUsage,
	)))
	resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	c := resp.Message.ToolCalls
	if err != nil || len(c) != 2 || c[0].ID != "b" || c[1].ID != "c" {
		t.Fatalf("应按 output_index 排序: %+v, %v", c, err)
	}
}

func TestResponsesTextNotDuplicatedByItemDone(t *testing.T) {
	// 已经通过增量输出过的文本，不能在 output_item.done / completed.output 里再输出一遍。
	item := `{"type":"message","content":[{"type":"output_text","text":"你好"}]}`
	srv := newServer(t, okBody(sse(
		respDelta(0, "你"), respDelta(0, "好"),
		`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`{"type":"response.completed","response":{"status":"completed","output":[`+item+`]}}`,
	)))
	resp, deltas, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	if err != nil || resp.Message.Content != "你好" || len(deltas) != 2 {
		t.Fatalf("文本重复了: content=%q deltas=%q err=%v", resp.Message.Content, deltas, err)
	}
}

func TestResponsesTextFromItemDoneWhenNoDeltas(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"整段"}]}}`,
		completedUsage,
	)))
	resp, deltas, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	if err != nil || resp.Message.Content != "整段" || len(deltas) != 1 {
		t.Fatalf("没有增量时应用完整 item 补发一次: content=%q deltas=%q err=%v", resp.Message.Content, deltas, err)
	}
}

func TestResponsesIncompleteMaxOutputIsLength(t *testing.T) {
	srv := newServer(t, okBody(sse(respDelta(0, "半"),
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`)))
	resp, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	if err != nil || resp.StopReason != agent.StopLength {
		t.Fatalf("得到 %+v, %v", resp, err)
	}
}

func TestResponsesFailedEvent(t *testing.T) {
	srv := newServer(t, okBody(sse(
		`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"出错了"}}}`)))
	_, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.Code != "server_error" || pe.Message != "出错了" {
		t.Fatalf("得到 %v", err)
	}
}

func TestResponsesErrorEvent(t *testing.T) {
	srv := newServer(t, okBody(sse(`{"type":"error","code":"rate_limit_exceeded","message":"慢点"}`)))
	_, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.Code != "rate_limit_exceeded" || pe.Message != "慢点" {
		t.Fatalf("得到 %v", err)
	}
}

func TestResponsesTruncatedStreamIsRetryable(t *testing.T) {
	srv := newServer(t, okBody(sse(respDelta(0, "半"))))
	_, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || !pe.Retryable {
		t.Fatalf("没有收到 completed 的流应是可重试错误，得到 %v", err)
	}
}

func TestResponsesHTTPError(t *testing.T) {
	srv := newServer(t, reply{status: 401, body: `{"error":{"message":"Incorrect API key","code":"invalid_api_key"}}`})
	_, _, err := collect(newProvider(t, config.ProtocolResponses, srv), userReq("q"))
	var pe *agent.ProviderError
	if !errors.As(err, &pe) || pe.Status != 401 || pe.Code != "invalid_api_key" || pe.Retryable {
		t.Fatalf("得到 %v", err)
	}
	if !strings.Contains(err.Error(), "API Key") {
		t.Errorf("提示应是中文说明: %s", err)
	}
}
