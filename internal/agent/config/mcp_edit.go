package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// 这两个函数供 /mcp 面板写回配置：就地改 mcp.jsonc 里的一个键，注释、缩进和其余字段原样保留。
// 不用 encoding/json 重新序列化整个文件——那会把用户手写的注释全部抹掉。

// SetMCPServerEnabled 把服务端 name 的 enable 改成 enable。
//
// 写完重新解析并校验：改动让配置不合法时（比如给一个没写 url/command 的服务端打上 true）回滚并报错。
func SetMCPServerEnabled(path, name string, enable bool) error {
	return editMCP(path, name, func(text []byte, obj *hujson.Value) ([]byte, error) {
		return setMember(text, obj, "enable", boolLiteral(enable)), nil
	})
}

// SetMCPToolExcluded 把工具 tool 加入（excluded 为 true）或移出服务端 name 的 excludeTools。
// 列表被清空时写回空数组而不是删掉键：键还在，用户能看出这里曾经屏蔽过东西。
func SetMCPToolExcluded(path, name, tool string, excluded bool) error {
	f, err := LoadMCP(path)
	if err != nil {
		return err
	}
	srv, ok := f.Servers[name]
	if !ok {
		return fmt.Errorf("mcp.jsonc 里没有服务端 %q", name)
	}
	list := make([]string, 0, len(srv.ExcludeTools)+1)
	for _, t := range srv.ExcludeTools {
		if !strings.EqualFold(strings.TrimSpace(t), tool) {
			list = append(list, t)
		}
	}
	if excluded {
		list = append(list, tool)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return editMCP(path, name, func(text []byte, obj *hujson.Value) ([]byte, error) {
		return setMember(text, obj, "excludeTools", string(raw)), nil
	})
}

// editMCP 读取 path，定位服务端 name 的对象交给 edit 改文本，再写回并校验。
// 任何一步失败都不会留下改坏的配置：写回后 LoadMCP 报错时把原文恢复。
func editMCP(path, name string, edit func(text []byte, obj *hujson.Value) ([]byte, error)) error {
	return editJSONC(path, "mcp.jsonc", nil, func(text []byte, v *hujson.Value) ([]byte, error) {
		obj := v.Find("/servers/" + jsonPointerEscape(name))
		if obj == nil || !isJSONObject(obj) {
			return nil, fmt.Errorf("mcp.jsonc 里没有服务端 %q", name)
		}
		return edit(text, obj)
	}, func(path string) error {
		_, err := LoadMCP(path)
		return err
	})
}

// editJSONC 就地改写一个 JSONC 文件：edit 拿到去掉 BOM 的全文与解析结果，
// 返回改好的全文。注释、缩进和未改动的字段因此原样保留。
//
// 文件不存在时用 seed 建一份再改（seed 为空则报错）。写完先确认仍是合法 JSONC，
// 再用 check 做语义校验；check 失败时把原文写回去。本地失败路径都有对应测试覆盖。
func editJSONC(path, what string, seed []byte, edit func(text []byte, v *hujson.Value) ([]byte, error), check func(path string) error) error {
	raw, err := os.ReadFile(path)
	missing := errors.Is(err, fs.ErrNotExist)
	switch {
	case missing && len(seed) == 0:
		return fmt.Errorf("读取 %s 失败: %w", what, err)
	case missing:
		raw = seed
	case err != nil:
		return fmt.Errorf("读取 %s 失败: %w", what, err)
	}
	// 记事本等编辑器可能写入 BOM；偏移量与编辑器里的位置对齐前先分开，写回时原样带上。
	bom, body := splitBOM(raw)
	v, err := hujson.Parse(body)
	if err != nil {
		return fmt.Errorf("解析 %s 失败: %w", what, err)
	}
	out, err := edit(body, &v)
	if err != nil {
		return err
	}
	if _, err := hujson.Parse(out); err != nil {
		// 理论上不该发生；真发生了就放弃写入，别把用户的配置搞坏。
		return fmt.Errorf("改写后的 %s 不是合法 JSONC，已放弃写入: %w", what, err)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(path, append(bom, out...), mode); err != nil {
		return fmt.Errorf("写回 %s 失败: %w", what, err)
	}
	if check == nil {
		return nil
	}
	if err := check(path); err != nil {
		if werr := os.WriteFile(path, raw, mode); werr != nil {
			return fmt.Errorf("改动后的配置不合法（%v），且回滚失败: %w", err, werr)
		}
		return err
	}
	return nil
}

// setMember 把对象里的键 key 设为 value 字面量：已有就替换它的值，没有就插一行。
func setMember(text []byte, obj *hujson.Value, key, value string) []byte {
	if m := findMember(obj, key); m != nil {
		return splice(text, m.Value.StartOffset, m.Value.EndOffset, value)
	}
	return insertMember(text, obj, key, value)
}

// findMember 按键名找对象成员。键名比较用的是带引号的 JSON 字面量，不区分转义写法。
func findMember(obj *hujson.Value, key string) *hujson.ObjectMember {
	o, ok := obj.Value.(*hujson.Object)
	if !ok {
		return nil
	}
	want := `"` + key + `"`
	for i := range o.Members {
		if lit, ok := o.Members[i].Name.Value.(hujson.Literal); ok && string(lit) == want {
			return &o.Members[i]
		}
	}
	return nil
}

// insertMember 插入一个新成员，尽量跟上同一层的缩进：
// 多行对象插在最后一个成员之后（该有逗号就补一个），单行对象就地追加。
func insertMember(text []byte, obj *hujson.Value, key, value string) []byte {
	o, ok := obj.Value.(*hujson.Object)
	if !ok {
		return text
	}
	closeAt := obj.EndOffset - 1 // 指向对象自己的 '}'
	if closeAt <= obj.StartOffset {
		return text
	}
	inline := !bytes.Contains(text[obj.StartOffset:closeAt], []byte("\n"))
	member := strconv.Quote(key) + ": " + value

	if len(o.Members) == 0 {
		interior := text[obj.StartOffset+1 : closeAt]
		switch {
		case inline && len(bytes.TrimSpace(interior)) == 0:
			return splice(text, obj.StartOffset+1, closeAt, member)
		case !inline:
			// 对象里可能已经有注释（比如模板里的注册示例）：插在闭合括号那行之前，
			// 而不是替换整个内部，否则用户的注释会被抹掉。
			indent := lineIndent(text, closeAt) + "  "
			lineAt := lineFrom(text, closeAt)
			return splice(text, lineAt, lineAt, indent+member+"\n")
		default:
			// 单行且对象里有注释：追加在 '{' 之后，原注释留在原处。
			return splice(text, obj.StartOffset+1, obj.StartOffset+1, " "+member+",")
		}
	}

	last := o.Members[len(o.Members)-1]
	region := text[last.Value.EndOffset:closeAt]
	if inline {
		if hasComma(region) {
			return splice(text, closeAt, closeAt, " "+member)
		}
		return splice(text, last.Value.EndOffset, last.Value.EndOffset, ", "+member)
	}

	lineAt := lineFrom(text, closeAt)
	out := splice(text, lineAt, lineAt, lineIndent(text, last.Name.StartOffset)+member+"\n")
	if !hasComma(region) {
		// 最后一个成员后面没有逗号：紧跟它的值补一个，注释留在新的一行之前。
		out = splice(out, last.Value.EndOffset, last.Value.EndOffset, ",")
	}
	return out
}

// hasComma 报告两个位置之间（不含注释）是否有逗号。
func hasComma(b []byte) bool {
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == ',':
			return true
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			j := bytes.IndexByte(b[i:], '\n')
			if j < 0 {
				return false
			}
			i += j
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			j := bytes.Index(b[i+2:], []byte("*/"))
			if j < 0 {
				return false
			}
			i += j + 3
		}
	}
	return false
}

// lineFrom 返回 pos 所在行的行首下标。
func lineFrom(text []byte, pos int) int {
	if i := bytes.LastIndexByte(text[:min(pos, len(text))], '\n'); i >= 0 {
		return i + 1
	}
	return 0
}

// lineIndent 返回 pos 所在行的前导空白。
func lineIndent(text []byte, pos int) string {
	line := text[lineFrom(text, pos):min(pos, len(text))]
	return string(line[:len(line)-len(bytes.TrimLeft(line, " \t"))])
}

// splice 把 text[from:to] 换成 repl。
func splice(text []byte, from, to int, repl string) []byte {
	from, to = min(from, len(text)), min(to, len(text))
	out := make([]byte, 0, len(text)-(to-from)+len(repl))
	out = append(out, text[:from]...)
	out = append(out, repl...)
	return append(out, text[to:]...)
}

func boolLiteral(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func isJSONObject(v *hujson.Value) bool {
	_, ok := v.Value.(*hujson.Object)
	return ok
}

// jsonPointerEscape 按 RFC 6901 转义 JSON Pointer 里的特殊字符。
func jsonPointerEscape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// splitBOM 把 UTF-8 BOM 与正文分开。
func splitBOM(b []byte) (bom, body []byte) {
	const prefix = "\xEF\xBB\xBF"
	if bytes.HasPrefix(b, []byte(prefix)) {
		return b[:len(prefix)], b[len(prefix):]
	}
	return nil, b
}
