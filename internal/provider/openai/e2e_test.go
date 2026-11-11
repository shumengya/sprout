package openai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
	"github.com/shumengya/sprout/internal/agent/event"
	"github.com/shumengya/sprout/internal/agent/platform"
	"github.com/shumengya/sprout/internal/agent/tool"
	"github.com/shumengya/sprout/internal/agent/tool/bash"
	"github.com/shumengya/sprout/internal/agent/tool/powershell"
)

// 端到端：真实 agent 循环 + 真实 OpenAI 适配器 + 真实 shell 进程，只有模型服务端是 httptest。
// 这里的服务端只是协议另一端的回放，不是默认运行路径里的替身。

const marker = "sprout-e2e-marker.txt"

// realShell 返回本机真实可用的 shell 工具及其名字、列目录命令；都没有则跳过。
func realShell(t *testing.T, dir string) (tl tool.Tool, name, listCmd string) {
	t.Helper()
	sh := platform.DetectShells("", "")
	switch {
	case sh.PowerShell != nil:
		return powershell.New(*sh.PowerShell, dir), "powershell", "Get-ChildItem -Name"
	case sh.Bash != nil:
		return bash.New(*sh.Bash, dir), "bash", "ls"
	}
	t.Skip("本机既没有 PowerShell 也没有 bash")
	return
}

func toolArgs(command string) string {
	b, _ := json.Marshal(map[string]string{"command": command})
	return string(b)
}

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// chatReplies 是 Chat Completions 的两轮回放：先要求调用工具，再给最终答案。
func chatReplies(toolName, command, final string) []reply {
	call := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
		map[string]any{"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": toolName, "arguments": toolArgs(command)}}}}}}}
	return []reply{
		okBody(sse(jsonStr(call), `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, "[DONE]")),
		okBody(sse(jsonStr(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": final}}}}),
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]")),
	}
}

// responsesReplies 是 Responses 的两轮回放。
func responsesReplies(toolName, command, final string) []reply {
	item := map[string]any{"type": "function_call", "call_id": "call_1", "name": toolName, "arguments": toolArgs(command)}
	done := `{"type":"response.completed","response":{"status":"completed","output":[]}}`
	return []reply{
		okBody(sse(jsonStr(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}), done)),
		okBody(sse(jsonStr(map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": final}), done)),
	}
}

type e2eCase struct {
	protocol string
	replies  func(toolName, command, final string) []reply
	// toolResult 从第二次请求体里取出回传给模型的工具结果文本。
	toolResult func(t *testing.T, body map[string]any) string
}

var e2eCases = []e2eCase{
	{config.ProtocolChat, chatReplies, func(t *testing.T, body map[string]any) string {
		msgs := asList(body["messages"])
		last := asMap(msgs[len(msgs)-1])
		if last["role"] != "tool" || last["tool_call_id"] != "call_1" {
			t.Fatalf("Chat 应以 role:tool 消息回传结果: %v", last)
		}
		return last["content"].(string)
	}},
	{config.ProtocolResponses, responsesReplies, func(t *testing.T, body map[string]any) string {
		in := asList(body["input"])
		last := asMap(in[len(in)-1])
		if last["type"] != "function_call_output" || last["call_id"] != "call_1" {
			t.Fatalf("Responses 应以 function_call_output 回传结果: %v", last)
		}
		return last["output"].(string)
	}},
}

// runE2E 跑完整一轮并返回事件和服务端收到的两次请求。
func runE2E(t *testing.T, c e2eCase, command, final string) (*collectSink, *fakeServer, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tl, name, listCmd := realShell(t, dir)
	if command == "" {
		command = listCmd
	}
	srv := newServer(t, c.replies(name, command, final)...)
	reg := tool.NewRegistry()
	if err := reg.Register(tl); err != nil {
		t.Fatal(err)
	}
	a, err := agent.New(agent.Config{
		Provider: newProvider(t, c.protocol, srv), Model: "m", System: "SYS", Tools: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sink collectSink
	if err := a.Run(context.Background(), agent.Input{Text: "看一下当前目录"}, &sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return &sink, srv, name
}

type collectSink struct{ events []event.Event }

func (c *collectSink) Emit(e event.Event) { c.events = append(c.events, e) }

func (c *collectSink) toolFinished(t *testing.T) event.ToolFinished {
	t.Helper()
	for _, e := range c.events {
		if f, ok := e.(event.ToolFinished); ok {
			return f
		}
	}
	t.Fatal("没有 ToolFinished 事件")
	return event.ToolFinished{}
}

func TestE2EFullReActLoopWithRealShell(t *testing.T) {
	for _, c := range e2eCases {
		t.Run(c.protocol, func(t *testing.T) {
			sink, srv, _ := runE2E(t, c, "", "目录里有 "+marker)

			fin := sink.toolFinished(t)
			if fin.Err != "" || fin.ExitCode != 0 || !strings.Contains(fin.Stdout, marker) {
				t.Fatalf("真实 shell 应列出标记文件: %+v", fin)
			}
			if len(srv.reqs) != 2 {
				t.Fatalf("应请求模型 2 次（调用工具、给最终答案），实际 %d", len(srv.reqs))
			}
			// 第二次请求必须带着真实的命令输出，模型才可能基于它推理。
			result := c.toolResult(t, srv.reqs[1].json(t))
			for _, want := range []string{marker, "exit_code: 0", "command:"} {
				if !strings.Contains(result, want) {
					t.Errorf("回传给模型的工具结果缺少 %q:\n%s", want, result)
				}
			}
			last := sink.events[len(sink.events)-1]
			if tf, ok := last.(event.TurnFinished); !ok || tf.Reason != event.ReasonStop {
				t.Errorf("最后应是 TurnFinished(stop)，得到 %#v", last)
			}
			var answer strings.Builder
			for _, e := range sink.events {
				if d, ok := e.(event.MessageDelta); ok {
					answer.WriteString(d.Text)
				}
			}
			if !strings.Contains(answer.String(), marker) {
				t.Errorf("最终答案 = %q", answer.String())
			}
		})
	}
}

func TestE2ENonZeroExitReachesModel(t *testing.T) {
	for _, c := range e2eCases {
		t.Run(c.protocol, func(t *testing.T) {
			sink, srv, _ := runE2E(t, c, "exit 3", "命令失败了")

			fin := sink.toolFinished(t)
			if fin.Err != "" || fin.ExitCode != 3 {
				t.Fatalf("非零退出码是正常结果而不是工具错误: %+v", fin)
			}
			if result := c.toolResult(t, srv.reqs[1].json(t)); !strings.Contains(result, "exit_code: 3") {
				t.Errorf("退出码应回给模型:\n%s", result)
			}
		})
	}
}

func TestE2EToolsAreAdvertisedInTheRightShape(t *testing.T) {
	for _, c := range e2eCases {
		t.Run(c.protocol, func(t *testing.T) {
			_, srv, name := runE2E(t, c, "", "ok")
			tools := asList(srv.reqs[0].json(t)["tools"])
			if len(tools) != 1 {
				t.Fatalf("应下发 1 个工具，得到 %v", tools)
			}
			spec := asMap(tools[0])
			if c.protocol == config.ProtocolChat {
				spec = asMap(spec["function"])
			}
			if spec["name"] != name || !strings.Contains(spec["description"].(string), "全新的进程") {
				t.Errorf("工具声明 = %v", spec)
			}
			params := asMap(spec["parameters"])
			if params["type"] != "object" || len(asList(params["required"])) != 1 {
				t.Errorf("参数 schema = %v", params)
			}
		})
	}
}
