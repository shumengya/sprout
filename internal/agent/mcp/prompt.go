package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shumengya/sprout/internal/agent/tool"
)

// Prompt 是服务端提供的一个提示词模板。
type Prompt struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Arguments   []PromptArgument `json:"arguments"`
}

// PromptArgument 是提示词模板的一个参数。
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// PromptMessage 是 prompts/get 返回的一条消息。
type PromptMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// HasPrompts 报告服务端在握手时是否声明了 prompts 能力。
func (c *Client) HasPrompts() bool { return c.prompts }

// ListPrompts 取全部提示词模板，跟着 nextCursor 翻页。
func (c *Client) ListPrompts(ctx context.Context) ([]Prompt, error) {
	out, err := listPaged[Prompt](ctx, c, "prompts/list", "prompts")
	if err != nil {
		return nil, fmt.Errorf("获取提示词列表失败: %w", err)
	}
	return out, nil
}

// GetPrompt 取一个提示词模板展开后的消息。arguments 是模板参数，没有时传 nil。
func (c *Client) GetPrompt(ctx context.Context, name string, arguments map[string]string) ([]PromptMessage, error) {
	params := map[string]any{"name": name}
	if len(arguments) > 0 {
		params["arguments"] = arguments
	}
	raw, err := c.rpc(ctx, "prompts/get", "", params)
	if err != nil {
		return nil, err
	}
	var res struct {
		Messages []PromptMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("解析提示词内容失败: %w", err)
	}
	return res.Messages, nil
}

const (
	listPromptsTool = "list_prompts"
	getPromptTool   = "get_prompt"
)

// PromptTools 返回该服务端的提示词工具；服务端没声明 prompts 能力时返回 nil。
func PromptTools(c *Client) []tool.Tool {
	if !c.HasPrompts() {
		return nil
	}
	return []tool.Tool{&listPrompts{client: c}, &getPrompt{client: c}}
}

type listPrompts struct{ client *Client }

func (l *listPrompts) Name() string { return ToolName(l.client.spec.Name, listPromptsTool) }

func (l *listPrompts) Description() string {
	return fmt.Sprintf("列出 MCP 服务端 %s 提供的提示词模板及其参数。用 get_prompt 按名字取展开后的内容。", l.client.spec.Name)
}

func (l *listPrompts) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (l *listPrompts) Run(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	cmd := "mcp " + l.client.spec.Name + "/prompts"
	prompts, err := l.client.ListPrompts(ctx)
	if err != nil {
		return tool.Result{Command: cmd}, err
	}
	if len(prompts) == 0 {
		return tool.Result{Command: cmd, Stdout: "（没有提示词模板）\n"}, nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "提示词模板（%d 个）：\n", len(prompts))
	for _, p := range prompts {
		name := p.Name
		if p.Title != "" {
			name = fmt.Sprintf("%s（%s）", p.Name, p.Title)
		}
		fmt.Fprintf(&out, "- %s", name)
		if p.Description != "" {
			fmt.Fprintf(&out, "：%s", oneLine(p.Description))
		}
		out.WriteByte('\n')
		for _, a := range p.Arguments {
			mark := "可选"
			if a.Required {
				mark = "必填"
			}
			fmt.Fprintf(&out, "    %s（%s）", a.Name, mark)
			if a.Description != "" {
				fmt.Fprintf(&out, "：%s", oneLine(a.Description))
			}
			out.WriteByte('\n')
		}
	}
	return tool.Result{Command: cmd, Stdout: out.String()}, nil
}

type getPrompt struct{ client *Client }

func (g *getPrompt) Name() string { return ToolName(g.client.spec.Name, getPromptTool) }

func (g *getPrompt) Description() string {
	return fmt.Sprintf("取 MCP 服务端 %s 的一个提示词模板展开后的消息。name 用 list_prompts 给出的名字，arguments 是模板参数。", g.client.spec.Name)
}

func (g *getPrompt) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"提示词模板名"},"arguments":{"type":"object","description":"模板参数，键值都是字符串","additionalProperties":{"type":"string"}}},"required":["name"]}`)
}

func (g *getPrompt) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	cmd := "mcp " + g.client.spec.Name + "/prompts/get"
	var args struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.Name) == "" {
		return tool.Result{Command: cmd}, fmt.Errorf(`参数需要 {"name": "模板名"}`)
	}
	msgs, err := g.client.GetPrompt(ctx, args.Name, args.Arguments)
	if err != nil {
		return tool.Result{Command: cmd}, err
	}
	text, images := renderPrompt(msgs)
	return tool.Result{Command: cmd, Stdout: text, Images: images}, nil
}

// renderPrompt 把提示词消息转成文本和图片。每条消息标出角色，图片按工具结果图片的规则校验。
func renderPrompt(msgs []PromptMessage) (string, []tool.Image) {
	var out strings.Builder
	var images []tool.Image
	for _, m := range msgs {
		role := m.Role
		if role == "" {
			role = "user"
		}
		fmt.Fprintf(&out, "--- %s ---\n", role)
		c := m.Content
		switch c.Type {
		case "text", "":
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
			fmt.Fprintf(&out, "[图片 %d：%s，已附在结果后]\n", len(images), img.MediaType)
		default:
			fmt.Fprintf(&out, "[%s 内容]\n", orUnknown(c.Type))
		}
	}
	if out.Len() == 0 {
		out.WriteString("（提示词没有内容）\n")
	}
	return out.String(), images
}
