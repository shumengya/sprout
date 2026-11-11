package google

import (
	"encoding/json"

	"github.com/shumengya/sprout/internal/agent"
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
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
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

	// id -> 函数名，供 tool 结果反查。
	callName := map[string]string{}
	for _, m := range req.Messages {
		switch m.Role {
		case agent.RoleUser:
			out.Contents = append(out.Contents, content{Role: "user", Parts: []part{{Text: m.Content}}})
		case agent.RoleAssistant:
			var parts []part
			// Gemini 不接受空的 text part；有 tool call 时内容为空很正常。
			if m.Content != "" {
				parts = append(parts, part{Text: m.Content})
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
				parts = append(parts, part{Text: m.Content})
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
			raw, _ := json.Marshal(map[string]string{"result": m.Content})
			fp := part{FunctionResponse: &functionResponse{Name: name, Response: raw}}
			if n := len(out.Contents); n > 0 && out.Contents[n-1].Role == "user" && onlyFunctionResponses(out.Contents[n-1]) {
				out.Contents[n-1].Parts = append(out.Contents[n-1].Parts, fp)
				continue
			}
			out.Contents = append(out.Contents, content{Role: "user", Parts: []part{fp}})
		}
	}
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
