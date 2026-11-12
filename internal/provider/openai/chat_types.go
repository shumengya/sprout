package openai

import (
	"encoding/json"

	"github.com/shumengya/sprout/internal/agent"
)

// Chat Completions 的线格式。只含本项目用到的字段。

type chatRequest struct {
	Model         string          `json:"model"`
	Messages      []chatMessage   `json:"messages"`
	Tools         []chatTool      `json:"tools,omitempty"`
	Stream        bool            `json:"stream"`
	StreamOptions *chatStreamOpts `json:"stream_options,omitempty"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	// ReasoningEffort 是思考强度（OpenAI o 系列 / GPT-5、DeepSeek 等）。只在 models.jsonc 为当前档位
	// 声明了取值时才发；Chat 协议没有预算字段，按 token 的预算在配置校验时就被拒绝。
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type chatStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // 纯文本发字符串，带图片时发 parts 数组；始终输出，部分兼容服务要求该字段存在
	// ReasoningContent 是 DeepSeek 等思考模式模型的思维链。带 tools 的请求必须原样回传历史里的这个字段，
	// 否则服务端可能返回 400。没有思维链时为空，omitempty 保证对其他服务商不多发字段。
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string          `json:"type"`
	Function chatToolDefFunc `json:"function"`
}

type chatToolDefFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// buildChatRequest 把内部请求转换成 Chat Completions 请求：system 放 messages[0]，
// 工具结果作为 role:"tool" 消息，assistant 的 tool call 放在 tool_calls。
func buildChatRequest(req agent.Request) chatRequest {
	out := chatRequest{
		Model:         req.Model,
		Stream:        true,
		StreamOptions: &chatStreamOpts{IncludeUsage: true},
		MaxTokens:     req.MaxOutputTokens,
		// Chat 协议没有“返回思考摘要”的开关：支持的服务（DeepSeek 等）直接在 reasoning_content 里返回。
		ReasoningEffort: req.Thinking.Effort,
	}
	if req.System != "" {
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: req.System})
	}
	// tool 消息只能放文本：工具结果里的图片攒起来，等这一组 tool 消息结束后用一条 user 消息补发，
	// 不能插在 tool 消息之间，否则 tool_calls 与结果的配对会被打断。
	var toolImages []agent.Block
	flushImages := func() {
		if len(toolImages) > 0 {
			blocks := append([]agent.Block{agent.TextBlock(toolImagesNote)}, toolImages...)
			out.Messages = append(out.Messages, chatMessage{Role: "user", Content: chatContent(agent.Message{Content: blocks})})
			toolImages = nil
		}
	}
	for _, m := range req.Messages {
		if m.Role != agent.RoleTool {
			flushImages()
		}
		cm := chatMessage{Role: string(m.Role), Content: chatContent(m)}
		if m.Role == agent.RoleAssistant {
			cm.ReasoningContent = m.Reasoning
		}
		if m.Role == agent.RoleTool {
			cm.ToolCallID = m.ToolID
			cm.Content = m.Text()
			toolImages = append(toolImages, imageBlocks(m)...)
		}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
				ID: tc.ID, Type: "function", Function: chatFunction{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		out.Messages = append(out.Messages, cm)
	}
	flushImages()
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, chatTool{
			Type:     "function",
			Function: chatToolDefFunc{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
		})
	}
	return out
}

type chatPart struct {
	Type     string        `json:"type"` // text | image_url
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"` // data:image/png;base64,...
}

// chatContent 把内部内容块转成 Chat 的 content。纯文本直接发字符串（兼容性最好，不含图片时
// 不引入 parts 数组，避免部分兼容服务不认）；带图片时按原顺序发 parts 数组。
func chatContent(m agent.Message) any {
	if !m.HasImages() {
		return m.Text()
	}
	parts := make([]chatPart, 0, len(m.Content))
	for _, blk := range m.Content {
		if blk.Type == agent.BlockImage {
			parts = append(parts, chatPart{Type: "image_url", ImageURL: &chatImageURL{URL: dataURL(blk)}})
			continue
		}
		parts = append(parts, chatPart{Type: "text", Text: blk.Text})
	}
	return parts
}

// toolImagesNote 是补发工具结果图片时的说明文字，让模型知道图片来自上面的工具结果。
const toolImagesNote = "（以上工具结果附带的图片）"

// imageBlocks 取出消息里的图片块。
func imageBlocks(m agent.Message) []agent.Block {
	var out []agent.Block
	for _, blk := range m.Content {
		if blk.Type == agent.BlockImage {
			out = append(out, blk)
		}
	}
	return out
}

// dataURL 拼出各协议通用的 data URL。
func dataURL(blk agent.Block) string {
	return "data:" + blk.MediaType + ";base64," + blk.Data
}

// chatChunk 是流式响应里的一个 data 事件。
type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		// OpenAI 把命中缓存的 token 放在 prompt_tokens_details.cached_tokens（已含在 prompt_tokens 里）；
		// DeepSeek 用 prompt_cache_hit_tokens。不报告的服务没有这些字段。
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
		// OpenAI 与 DeepSeek 都把思维链 token 放在这里；不报告的服务没有这个字段。
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	// 部分兼容服务在流内用 error 字段报告失败。
	Error json.RawMessage `json:"error"`
}
