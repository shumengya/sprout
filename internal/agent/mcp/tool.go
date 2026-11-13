package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shumengya/sprout/internal/agent/tool"
)

// ToolPrefix 是 MCP 工具在注册表里的名字前缀，格式为 mcp__<server>__<tool>。
const ToolPrefix = "mcp__"

// Tool 把一个 MCP 工具适配成 agent 的工具。
type Tool struct {
	client *Client
	// Name 已在注册时定好：mcp__<服务端>__<工具>，满足模型的工具命名约束。
	name   string
	remote string // 服务端上的原始工具名
	schema json.RawMessage
	desc   string
}

// NewTool 构造适配器。name 必须是已经过 sanitizeToolName 且有前缀的完整名字。
func NewTool(c *Client, name, remote string, info ToolInfo) *Tool {
	return &Tool{client: c, name: name, remote: remote, schema: info.InputSchema, desc: info.Description}
}

func (t *Tool) Name() string { return t.name }

func (t *Tool) Description() string {
	d := t.desc
	if strings.TrimSpace(d) == "" {
		d = "MCP 工具 " + t.remote + "（服务端没有提供说明）。"
	}
	return fmt.Sprintf("%s\n（来自 MCP 服务端 %s，工具名 %s）", d, t.client.spec.Name, t.remote)
}

func (t *Tool) Parameters() json.RawMessage {
	if len(t.schema) == 0 || string(t.schema) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.schema
}

func (t *Tool) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	start := time.Now()
	res, err := t.client.Call(ctx, t.remote, raw)
	dur := time.Since(start)
	if err != nil {
		return tool.Result{Command: "mcp " + t.client.spec.Name + "/" + t.remote, Duration: dur}, err
	}

	var out strings.Builder
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			out.WriteString(c.Text)
			if !strings.HasSuffix(c.Text, "\n") {
				out.WriteByte('\n')
			}
		case "image":
			// 图片暂时不转成图片块：tool.Result 只承载文本，这里如实告诉模型有图。
			fmt.Fprintf(&out, "[图片内容：%s]\n", orUnknown(c.MimeType))
		case "audio":
			fmt.Fprintf(&out, "[音频内容：%s]\n", orUnknown(c.MimeType))
		case "resource":
			fmt.Fprintf(&out, "[资源：%s]\n", orUnknown(c.URI))
		case "resource_link":
			fmt.Fprintf(&out, "[资源链接：%s]\n", orUnknown(c.URI))
		default:
			fmt.Fprintf(&out, "[%s 内容]\n", orUnknown(c.Type))
		}
	}
	if len(res.StructuredContent) > 0 && string(res.StructuredContent) != "null" {
		out.WriteString("\n--- structuredContent ---\n")
		out.Write(res.StructuredContent)
		out.WriteByte('\n')
	}

	exit := 0
	stderr := ""
	if res.IsError {
		exit = 1
		stderr = "工具报告失败（isError: true）"
	}
	return tool.Result{
		Command:  "mcp " + t.client.spec.Name + "/" + t.remote,
		Stdout:   out.String(),
		Stderr:   stderr,
		ExitCode: exit,
		Duration: dur,
	}, nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知"
	}
	return s
}
