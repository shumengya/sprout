// Package agent 是 ReAct 内核：维护对话历史，反复调用 Provider 与工具直到模型给出最终答案。
//
// 内核只处理下面这套与协议无关的内部消息模型，不出现任何厂商字段；
// 协议转换由 internal/provider 下的适配器负责。内核不得 import 任何 surface 包。
package agent

import (
	"encoding/base64"
	"strings"

	"github.com/shumengya/sprout/internal/agent/event"
)

// Role 是消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是模型发起的一次工具调用。Arguments 是模型给出的原始 JSON 字符串，
// 不在内核里解析，由具体工具负责校验。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// 内容块的类型。消息的 Content 是一串块，纯文本消息只有一个 text 块；
// 带图片的消息按原来的顺序排列文本与图片，多图、图文交错的语义不会丢。
const (
	BlockText  = "text"
	BlockImage = "image"
)

// Block 是消息内容里的一个块：text 用 Text，image 用 MediaType + Data（base64）。
type Block struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"` // base64 编码的图片数据
}

// TextBlock 构造一个文本块。
func TextBlock(s string) Block { return Block{Type: BlockText, Text: s} }

// ImageBlock 构造一个图片块。
func ImageBlock(mediaType, b64 string) Block {
	return Block{Type: BlockImage, MediaType: mediaType, Data: b64}
}

// Message 是对话历史里的一条消息。
//
//   - assistant 消息可带 ToolCalls，以及思考模式模型产生的 Reasoning（思维链）。
//     Reasoning 必须原样保存并在后续请求里回传：DeepSeek 要求带 tools 的请求回传历史 reasoning_content。
//   - tool 消息通过 ToolID 对应某个 ToolCall.ID，Content 是回给模型的结果。
//   - user 消息和 tool 消息可以带图片块。
type Message struct {
	Role      Role
	Content   []Block
	Reasoning string
	ToolCalls []ToolCall
	ToolID    string
}

// Text 返回全部文本块的拼接，供展示、压缩、token 估算等只要文字的场合使用。
func (m Message) Text() string {
	if len(m.Content) == 1 && m.Content[0].Type == BlockText {
		return m.Content[0].Text
	}
	var b strings.Builder
	for _, blk := range m.Content {
		if blk.Type == BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// HasImages 报告消息是否带图片块。
func (m Message) HasImages() bool {
	for _, blk := range m.Content {
		if blk.Type == BlockImage {
			return true
		}
	}
	return false
}

// WithoutImages 返回去掉图片块的副本，图片处留下占位文本，用于模型不支持视觉时降级。
// 连续的图片只留一个占位，避免刷屏。没有图片时原样返回。
func (m Message) WithoutImages(placeholder string) Message {
	if !m.HasImages() {
		return m
	}
	out := make([]Block, 0, len(m.Content))
	lastPlaceholder := false
	for _, blk := range m.Content {
		if blk.Type == BlockImage {
			if !lastPlaceholder {
				out = append(out, TextBlock(placeholder))
			}
			lastPlaceholder = true
			continue
		}
		out = append(out, blk)
		lastPlaceholder = blk.Type == BlockText && blk.Text == placeholder
	}
	m.Content = out
	return m
}

// NewTextMessage 构造一条纯文本消息。
func NewTextMessage(role Role, text string) Message {
	return Message{Role: role, Content: []Block{TextBlock(text)}}
}

// assistantMessage 把模型返回的文本转成 assistant 消息：空文本不产生文本块，
// 免得给各协议发一个空 text block（Anthropic 等会拒绝）。历史与重放共用它，保证两边一致。
func assistantMessage(text string) Message {
	m := Message{Role: RoleAssistant}
	if text != "" {
		m.Content = []Block{TextBlock(text)}
	}
	return m
}

// toolMessage 构造一条回给模型的工具结果消息。
func toolMessage(callID, content string) Message {
	m := NewTextMessage(RoleTool, content)
	m.ToolID = callID
	return m
}

// eventImages 把输入图片转成事件里的图片描述；bytes 由 base64 长度推算，免得再解码一遍。
func eventImages(imgs []Image) []event.ImageInfo {
	if len(imgs) == 0 {
		return nil
	}
	out := make([]event.ImageInfo, len(imgs))
	for i, img := range imgs {
		out[i] = event.ImageInfo{MediaType: img.MediaType, Bytes: base64.StdEncoding.DecodedLen(len(img.Data)), Data: img.Data}
	}
	return out
}
