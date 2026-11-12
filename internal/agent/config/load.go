package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/tailscale/hujson"
)

// ParseError 是 models.jsonc 的语法或类型错误，尽量带行列。Line 为 0 表示位置未知。
type ParseError struct {
	Path string
	Line int
	Col  int
	Msg  string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d:%d: %s", e.Path, e.Line, e.Col, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Msg)
}

// hujson 的错误信息形如 "hujson: line 3, column 5: invalid character ..."。
var hujsonPos = regexp.MustCompile(`line (\d+), column (\d+): (.*)$`)

// Load 读取并解析 models.jsonc（JSONC：允许 // 和 /* */ 注释、尾逗号），解析后做校验。
// 语法错误返回 *ParseError，内容问题返回 *ValidationError。
func Load(path string) (*File, error) {
	var f File
	if err := decodeJSONC(path, &f); err != nil {
		return nil, err
	}
	if probs := f.problems(); len(probs) > 0 {
		return nil, &ValidationError{Path: path, Problems: probs}
	}
	return &f, nil
}

// decodeJSONC 读取 JSONC 文件并解到 v，错误统一转成带行列的 *ParseError。
// models.jsonc 与 tools.jsonc 都走这里，注释处理与报错位置保持一致。
func decodeJSONC(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %w", err)
	}
	// 记事本等编辑器可能写入 BOM；去掉后列号才与编辑器里看到的一致。
	raw = bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF"))

	// Standardize 把注释和尾逗号替换成空格，字节偏移不变，因此后面的报错位置对应原文件。
	// 它会就地改写传入的缓冲区，必须传副本：raw 要保持原样，之后才能按字符数列号。
	std, err := hujson.Standardize(bytes.Clone(raw))
	if err != nil {
		return newParseError(path, raw, err)
	}
	if err := json.Unmarshal(std, v); err != nil {
		return typeError(path, raw, err)
	}
	return nil
}

// newParseError 把 hujson 的语法错误转成带位置的 ParseError。
// hujson 的列号按字节计，同一行里中文注释之后的列号会比编辑器里看到的偏大，
// 所以这里用原始内容把它换算成字符列。
func newParseError(path string, raw []byte, err error) *ParseError {
	pe := &ParseError{Path: path, Msg: err.Error()}
	if m := hujsonPos.FindStringSubmatch(err.Error()); m != nil {
		line, _ := strconv.Atoi(m[1])
		byteCol, _ := strconv.Atoi(m[2])
		pe.Line, pe.Col = lineCol(raw, int64(lineStart(raw, line)+byteCol-1))
		pe.Msg = m[3]
	}
	return pe
}

// typeError 把 json.Unmarshal 的错误转成带位置的 ParseError。
// 偏移量必须对照原始内容换算：Standardize 把注释按字节替换成空格，
// 用标准化后的内容数“字符”会把一个汉字数成三个。
func typeError(path string, raw []byte, err error) *ParseError {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		line, col := lineCol(raw, ute.Offset)
		return &ParseError{
			Path: path, Line: line, Col: col,
			Msg: fmt.Sprintf("字段 %s 的类型不对：需要 %s，实际是 %s", ute.Field, ute.Type, ute.Value),
		}
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		line, col := lineCol(raw, se.Offset)
		return &ParseError{Path: path, Line: line, Col: col, Msg: se.Error()}
	}
	return &ParseError{Path: path, Msg: err.Error()}
}

// lineStart 返回第 line 行（1 起始）第一个字节的偏移；行数超出时返回末尾。
func lineStart(b []byte, line int) int {
	off := 0
	for i := 1; i < line; i++ {
		j := bytes.IndexByte(b[off:], '\n')
		if j < 0 {
			return len(b)
		}
		off += j + 1
	}
	return off
}

// lineCol 把字节偏移换算成 1 起始的行、列（列按字符计）。
func lineCol(b []byte, off int64) (line, col int) {
	off = min(max(off, 0), int64(len(b)))
	head := b[:off]
	line = 1 + bytes.Count(head, []byte("\n"))
	col = 1 + utf8.RuneCount(head[bytes.LastIndexByte(head, '\n')+1:])
	return line, col
}
