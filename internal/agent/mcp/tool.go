package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
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

	text, images := renderContent(res.Content)
	var out strings.Builder
	out.WriteString(text)
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
		Images:   images,
	}, nil
}

// renderContent 把结果内容转成给模型的文本和图片；不合格的图片只在文本里说明原因。
func renderContent(content []Content) (string, []tool.Image) {
	var out strings.Builder
	var images []tool.Image
	for _, c := range content {
		switch c.Type {
		case "text":
			out.WriteString(c.Text)
			if !strings.HasSuffix(c.Text, "\n") {
				out.WriteByte('\n')
			}
		case "image":
			img, err := toolImage(c)
			if err != nil {
				fmt.Fprintf(&out, "[图片内容：%s，未附上：%v]\n", orUnknown(c.MimeType), err)
				continue
			}
			images = append(images, img)
			// 文本里留一行标记：界面的工具输出里看得到有图，模型也知道第几张图对应哪里。
			fmt.Fprintf(&out, "[图片 %d：%s，已附在结果后]\n", len(images), img.MediaType)
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
	return out.String(), images
}

// maxImageBytes 与用户输入图片的上限（media.MaxImageBytes）一致：多数服务商限制 base64 后 5MB 左右。
// 不 import media，免得 mcp 包为一个常量多依赖一层。
const maxImageBytes = 3 << 20

// toolImage 校验服务端给的图片：base64 要能解开、不超过上限、内容是四种支持的格式之一。
// 类型以内容嗅探为准，mimeType 只是声明，服务端写错时照样能发对。
func toolImage(c Content) (tool.Image, error) {
	raw, err := base64.StdEncoding.DecodeString(c.Data)
	if err != nil {
		return tool.Image{}, fmt.Errorf("base64 无效")
	}
	if len(raw) == 0 {
		return tool.Image{}, fmt.Errorf("数据为空")
	}
	if len(raw) > maxImageBytes {
		return tool.Image{}, fmt.Errorf("有 %d 字节，超过上限 %d", len(raw), maxImageBytes)
	}
	switch mt := http.DetectContentType(raw); mt {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return tool.Image{MediaType: mt, Data: c.Data}, nil
	}
	return tool.Image{}, fmt.Errorf("不是支持的格式（png / jpeg / gif / webp）")
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知"
	}
	return s
}
