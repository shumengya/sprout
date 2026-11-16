package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Resource 是服务端提供的一个资源或资源模板。模板的 URITemplate 里有 {参数}，填好后才能读。
type Resource struct {
	URI         string `json:"uri"`
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
	Size        int64  `json:"size"`
}

// ResourceContent 是 resources/read 返回的一段内容：文本资源用 Text，二进制资源用 Blob（base64）。
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	Blob     string `json:"blob"`
}

// HasResources 报告服务端在握手时是否声明了 resources 能力。
func (c *Client) HasResources() bool { return c.resources }

// ListResources 取全部资源与资源模板，各自跟着 nextCursor 翻页。
// 服务端只实现了其中一个方法时（另一个回 -32601），另一半当作空。
func (c *Client) ListResources(ctx context.Context) (resources, templates []Resource, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.spec.timeout())
	defer cancel()
	resources, err = listPaged[Resource](ctx, c, "resources/list", "resources")
	if err != nil && !isMethodNotFound(err) {
		return nil, nil, fmt.Errorf("获取资源列表失败: %w", err)
	}
	templates, err = listPaged[Resource](ctx, c, "resources/templates/list", "resourceTemplates")
	if err != nil && !isMethodNotFound(err) {
		return nil, nil, fmt.Errorf("获取资源模板失败: %w", err)
	}
	return resources, templates, nil
}

// ReadResource 按 URI 读一个资源。服务端可能返回多段内容（如目录下的多个文件），原样带回。
func (c *Client) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	ctx, cancel := context.WithTimeout(ctx, c.spec.timeout())
	defer cancel()
	raw, err := c.tr.request(ctx, "resources/read", "", c.wrap(map[string]any{"uri": uri}))
	if err != nil {
		return nil, err
	}
	var res struct {
		Contents []ResourceContent `json:"contents"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("解析资源内容失败: %w", err)
	}
	return res.Contents, nil
}

// listPaged 调一个分页的列表方法，取结果里 field 字段的数组，跟着 nextCursor 翻页。
// 最多 20 页，防止服务端返回环状 cursor 时死循环（与 listTools 一致）。
func listPaged[T any](ctx context.Context, c *Client, method, field string) ([]T, error) {
	var out []T
	cursor := ""
	for range 20 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.tr.request(ctx, method, "", c.wrap(params))
		if err != nil {
			return nil, err
		}
		var page map[string]json.RawMessage
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("解析 %s 结果失败: %w", method, err)
		}
		var items []T
		if b, ok := page[field]; ok {
			if err := json.Unmarshal(b, &items); err != nil {
				return nil, fmt.Errorf("解析 %s 结果失败: %w", method, err)
			}
		}
		out = append(out, items...)
		var next string
		if b, ok := page["nextCursor"]; ok {
			_ = json.Unmarshal(b, &next)
		}
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return out, nil
}

// isMethodNotFound 报告错误是不是 JSON-RPC 的“方法不存在”（-32601）。
func isMethodNotFound(err error) bool {
	var re *rpcError
	return errors.As(err, &re) && re.Code == -32601
}
