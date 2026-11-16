package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shumengya/sprout/internal/agent/tool"
)

// 每个声明了 resources 能力的服务端注册两个工具：列出资源、按 URI 读取。
// 不声明能力的服务端不注册，免得模型白调一次才知道没有。
const (
	listResourcesTool = "list_resources"
	readResourceTool  = "read_resource"
)

// ResourceTools 返回该服务端的资源工具；服务端没声明 resources 能力时返回 nil。
func ResourceTools(c *Client) []tool.Tool {
	if !c.HasResources() {
		return nil
	}
	return []tool.Tool{&listResources{client: c}, &readResource{client: c}}
}

type listResources struct{ client *Client }

func (l *listResources) Name() string { return ToolName(l.client.spec.Name, listResourcesTool) }

func (l *listResources) Description() string {
	return fmt.Sprintf("列出 MCP 服务端 %s 提供的资源与资源模板。模板的 uriTemplate 里有 {参数}，要先填好参数再读取。", l.client.spec.Name)
}

func (l *listResources) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (l *listResources) Run(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	resources, templates, err := l.client.ListResources(ctx)
	if err != nil {
		return tool.Result{Command: "mcp " + l.client.spec.Name + "/resources"}, err
	}
	var out strings.Builder
	writeResources(&out, "资源", resources, false)
	writeResources(&out, "资源模板", templates, true)
	if out.Len() == 0 {
		out.WriteString("（没有资源）\n")
	}
	return tool.Result{Command: "mcp " + l.client.spec.Name + "/resources", Stdout: out.String()}, nil
}

func writeResources(out *strings.Builder, title string, list []Resource, template bool) {
	if len(list) == 0 {
		return
	}
	fmt.Fprintf(out, "%s（%d 个）：\n", title, len(list))
	for _, r := range list {
		uri := r.URI
		if template {
			uri = r.URITemplate
		}
		name := r.Name
		if r.Title != "" {
			name = r.Title
		}
		fmt.Fprintf(out, "- %s", uri)
		if name != "" && name != uri {
			fmt.Fprintf(out, "（%s）", name)
		}
		if r.MimeType != "" {
			fmt.Fprintf(out, " [%s]", r.MimeType)
		}
		if r.Description != "" {
			fmt.Fprintf(out, "：%s", oneLine(r.Description))
		}
		out.WriteByte('\n')
	}
}

// oneLine 把描述压成一行，列表里不该出现多行文本。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

type readResource struct{ client *Client }

func (r *readResource) Name() string { return ToolName(r.client.spec.Name, readResourceTool) }

func (r *readResource) Description() string {
	return fmt.Sprintf("读取 MCP 服务端 %s 的一个资源。uri 用 list_resources 给出的地址；模板资源要先把 {参数} 换成实际值。", r.client.spec.Name)
}

func (r *readResource) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"uri":{"type":"string","description":"资源 URI"}},"required":["uri"]}`)
}

func (r *readResource) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	cmd := "mcp " + r.client.spec.Name + "/resources/read"
	var args struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.URI) == "" {
		return tool.Result{Command: cmd}, fmt.Errorf("参数需要 {\"uri\": \"资源地址\"}")
	}
	contents, err := r.client.ReadResource(ctx, args.URI)
	if err != nil {
		return tool.Result{Command: cmd}, err
	}
	text, images := renderResource(contents)
	return tool.Result{Command: cmd, Stdout: text, Images: images}, nil
}

// renderResource 把资源内容转成文本和图片。图片按与工具结果图片相同的规则校验，
// 不合格或不是图片的二进制内容只在文本里说明，不把 base64 原文塞给模型。
func renderResource(contents []ResourceContent) (string, []tool.Image) {
	var out strings.Builder
	var images []tool.Image
	for _, c := range contents {
		if len(contents) > 1 {
			fmt.Fprintf(&out, "--- %s ---\n", orUnknown(c.URI))
		}
		switch {
		case c.Text != "":
			out.WriteString(c.Text)
			if !strings.HasSuffix(c.Text, "\n") {
				out.WriteByte('\n')
			}
		case c.Blob != "":
			img, err := toolImage(Content{Data: c.Blob})
			if err == nil {
				images = append(images, img)
				fmt.Fprintf(&out, "[图片 %d：%s，已附在结果后]\n", len(images), img.MediaType)
				continue
			}
			fmt.Fprintf(&out, "[二进制内容：%s，%d 字节，未附上：%v]\n", orUnknown(c.MimeType), blobSize(c.Blob), err)
		default:
			fmt.Fprintf(&out, "[空内容：%s]\n", orUnknown(c.URI))
		}
	}
	if out.Len() == 0 {
		out.WriteString("（资源没有内容）\n")
	}
	return out.String(), images
}

// blobSize 报告 base64 解码后的大致字节数；解不开时返回 0。
func blobSize(b64 string) int {
	n := base64.StdEncoding.DecodedLen(len(b64))
	if raw, err := base64.StdEncoding.DecodeString(b64); err == nil {
		return len(raw)
	}
	return n
}
