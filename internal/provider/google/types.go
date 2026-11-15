package google

import (
	"encoding/json"
	"strconv"

	"github.com/shumengya/sprout/internal/agent"
	"github.com/shumengya/sprout/internal/agent/config"
)

// Gemini 协议的线格式。只含本项目用到的字段。

type request struct {
	SystemInstruction *content  `json:"systemInstruction,omitempty"`
	Contents          []content `json:"contents"`
	Tools             []tool    `json:"tools,omitempty"`
	// MaxOutputTokens 不传时由模型默认；配置里为 0 就省略，避免传 0 被拒。
	GenerationConfig *generationConfig `json:"generationConfig,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens int             `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *thinkingConfig `json:"thinkingConfig,omitempty"`
}

// thinkingConfig 控制思考：Gemini 3 起用 thinkingLevel（low / high 等），2.5 用 thinkingBudget
// （0 关闭、-1 动态）。两者由 models.jsonc 的档位声明决定发哪个，这里不做模型判断。
type thinkingConfig struct {
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
	// 指针：0 表示关闭思考，必须能发出去。
	ThinkingBudget  *int `json:"thinkingBudget,omitempty"`
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"` // user | model；systemInstruction 不带 role
	Parts []part `json:"parts"`
}

// part 是 text、functionCall、functionResponse 之一，用 omitempty 区分。
type part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
	InlineData       *inlineData       `json:"inlineData,omitempty"`
	// ThoughtSignature 是 Gemini 3 给函数调用等 part 的服务端签名，回传对应 part 时必须原样带回，
	// 否则服务端会拒绝。没有的模型不返回这个字段。
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

// inlineData 是内联的 base64 图片。Gemini 也支持 fileData/URI，这里只用内联，避免依赖 File API。
type inlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// contentParts 把内部内容块转成 Gemini 的 parts，文本与图片按原顺序排列。
func contentParts(m agent.Message) []part {
	parts := make([]part, 0, len(m.Content))
	for _, blk := range m.Content {
		if blk.Type == agent.BlockImage {
			parts = append(parts, part{InlineData: &inlineData{MimeType: blk.MediaType, Data: blk.Data}})
			continue
		}
		parts = append(parts, part{Text: blk.Text})
	}
	return parts
}

// nativeParts 取出原始 part 序列。历史来自别的协议、别的模型或已被清掉时返回 false。
func nativeParts(m agent.Message, model string) ([]part, bool) {
	if !m.Native.Usable(config.ProtocolGoogle, model) {
		return nil, false
	}
	var parts []part
	if json.Unmarshal(m.Native.Data, &parts) != nil || len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

// appendNative 把流式收到的 part 记进回传序列：不带签名的纯思考摘要不回传（它只是展示用的摘要），
// 相邻的、都不带签名的文本 part 合并成一个，避免逐字增量被拆成几百个 part。
func appendNative(parts []part, p part) []part {
	if p.Thought && p.ThoughtSignature == "" {
		return parts
	}
	if p.Text != "" && p.FunctionCall == nil && p.InlineData == nil && p.ThoughtSignature == "" && len(parts) > 0 {
		if last := &parts[len(parts)-1]; last.Text != "" && !last.Thought &&
			last.FunctionCall == nil && last.InlineData == nil && last.ThoughtSignature == "" {
			last.Text += p.Text
			return parts
		}
	}
	return append(parts, p)
}

type functionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type tool struct {
	FunctionDeclarations []functionDecl `json:"functionDeclarations"`
}

type functionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// buildRequest 把内部请求转换成 Gemini 请求。
//
// 消息映射规则：
//   - system 放 systemInstruction。
//   - user → user，assistant → model。
//   - assistant 的 tool call 作为 model 消息里的 functionCall part（functionCall 无 id，回传也不需要 id）。
//   - tool 消息 → user 消息里的 functionResponse part；连续多条 tool 消息合并进同一条 user 消息。
//     functionResponse 只有 name，没有 id，所以按调用 id 反查函数名的映射必须由本次请求的历史自行建立。
func buildRequest(req agent.Request) request {
	out := request{Contents: []content{}}
	if req.System != "" {
		out.SystemInstruction = &content{Parts: []part{{Text: req.System}}}
	}
	if req.MaxOutputTokens > 0 {
		out.GenerationConfig = &generationConfig{MaxOutputTokens: req.MaxOutputTokens}
	}
	if t := req.Thinking; t.Active() {
		if out.GenerationConfig == nil {
			out.GenerationConfig = &generationConfig{}
		}
		tc := &thinkingConfig{ThinkingLevel: t.Effort, ThinkingBudget: t.Budget}
		// 预算为 0 是关闭思考，这时要求返回思考摘要没有意义，部分模型还会报参数冲突。
		tc.IncludeThoughts = t.Summary && (t.Budget == nil || *t.Budget != 0)
		out.GenerationConfig.ThinkingConfig = tc
	}

	// id -> 函数名，供 tool 结果反查。
	callName := map[string]string{}
	// 工具结果的图片：functionResponse 里塞不进图片，攒到这组工具结果之后用一条 user 消息补发。
	var toolImages []agent.Block
	flushImages := func() {
		if len(toolImages) > 0 {
			blocks := append([]agent.Block{agent.TextBlock(toolImagesNote)}, toolImages...)
			out.Contents = append(out.Contents, content{Role: "user", Parts: contentParts(agent.Message{Content: blocks})})
			toolImages = nil
		}
	}
	for _, m := range req.Messages {
		if m.Role != agent.RoleTool {
			flushImages()
		}
		switch m.Role {
		case agent.RoleUser:
			out.Contents = append(out.Contents, content{Role: "user", Parts: contentParts(m)})
		case agent.RoleAssistant:
			// 带 thoughtSignature 的历史原样回传：签名只对当时的 part 序列有效，重建会失效。
			if parts, ok := nativeParts(m, req.Model); ok {
				for _, tc := range m.ToolCalls {
					callName[tc.ID] = tc.Name
				}
				out.Contents = append(out.Contents, content{Role: "model", Parts: parts})
				continue
			}
			var parts []part
			// Gemini 不接受空的 text part；有 tool call 时内容为空很正常。
			if text := m.Text(); text != "" {
				parts = append(parts, part{Text: text})
			}
			for _, tc := range m.ToolCalls {
				callName[tc.ID] = tc.Name
				args := json.RawMessage(tc.Arguments)
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				parts = append(parts, part{FunctionCall: &functionCall{Name: tc.Name, Args: args}})
			}
			// Gemini 要求 parts 非空，纯 reasoning 的历史至少发一段文本，避免触发空消息错误。
			if len(parts) == 0 {
				parts = append(parts, part{Text: m.Text()})
			}
			out.Contents = append(out.Contents, content{Role: "model", Parts: parts})
		case agent.RoleTool:
			name := callName[m.ToolID]
			// 历史被截断后可能只剩工具结果、没有配对调用。functionResponse 按 name 配对，
			// 名字缺失时用占位名，服务端报“未知函数”比缺字段更容易定位。
			if name == "" {
				name = "sprout_tool_result"
			}
			// 工具结果是纯文本，包成 {"result": "..."} 让 functionResponse.response
			// 始终是合法的 JSON 对象（Gemini 要求 response 必须是对象）。
			// functionResponse.response 必须是对象，图片放不进去：在结果文本里注明，
			// 图片本身用一条紧随其后的 user 消息补发。
			raw, _ := json.Marshal(map[string]string{"result": m.Text() + imageNote(m)})
			fp := part{FunctionResponse: &functionResponse{Name: name, Response: raw}}
			toolImages = append(toolImages, imageBlocks(m)...)
			if n := len(out.Contents); n > 0 && out.Contents[n-1].Role == "user" && onlyFunctionResponses(out.Contents[n-1]) {
				out.Contents[n-1].Parts = append(out.Contents[n-1].Parts, fp)
				continue
			}
			out.Contents = append(out.Contents, content{Role: "user", Parts: []part{fp}})
		}
	}
	flushImages()
	if len(req.Tools) > 0 {
		var decls []functionDecl
		for _, t := range req.Tools {
			params := json.RawMessage(t.Parameters)
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object"}`)
			}
			decls = append(decls, functionDecl{Name: t.Name, Description: t.Description, Parameters: params})
		}
		out.Tools = []tool{{FunctionDeclarations: decls}}
	}
	return out
}

// toolImagesNote 是补发工具结果图片时的说明文字，让模型知道图片来自上面的工具结果。
const toolImagesNote = "（以上工具结果附带的图片）"

// imageNote 在结果文本里注明还附了图片，模型才知道要往后看。
func imageNote(m agent.Message) string {
	n := len(imageBlocks(m))
	if n == 0 {
		return ""
	}
	return "\n（另附 " + strconv.Itoa(n) + " 张图片，见下一条消息）"
}

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

func onlyFunctionResponses(c content) bool {
	for _, p := range c.Parts {
		if p.FunctionResponse == nil {
			return false
		}
	}
	return len(c.Parts) > 0
}

// streamChunk 是流式响应里的一个 data 事件（alt=sse 时每个事件是一个完整 JSON 对象）。
type streamChunk struct {
	Candidates []struct {
		Content      *content `json:"content"`
		FinishReason string   `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
		// 命中缓存的 token，已含在 promptTokenCount 里；不报告时没有这个字段。
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
	PromptFeedback *struct {
		BlockReason        string `json:"blockReason"`
		BlockReasonMessage string `json:"blockReasonMessage"`
	} `json:"promptFeedback"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}
