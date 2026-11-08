package shell

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// capBuffer 收集进程输出，总量有上限：保留开头 headMax 字节和最近 tailMax 字节，
// 中间丢弃并记数。这样 `yes` 这类无限输出的命令也不会撑爆内存。
type capBuffer struct {
	headMax, tailMax int
	head, tail       []byte
	dropped          int
}

func newCapBuffer(limit int) *capBuffer {
	head := limit / 2
	return &capBuffer{headMax: head, tailMax: limit - head}
}

// Write 总是返回 len(p), nil：输出过多不应让子进程因为写管道失败而提前退出。
func (b *capBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := b.headMax - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	b.tail = append(b.tail, p...)
	if over := len(b.tail) - b.tailMax; over > 0 {
		b.dropped += over
		// 拷贝到新切片头部，避免底层数组随写入无限增长。
		b.tail = append(b.tail[:0], b.tail[over:]...)
	}
	return n, nil
}

// Truncated 报告是否丢弃过内容。
func (b *capBuffer) Truncated() bool { return b.dropped > 0 }

// String 返回收集到的文本；发生过丢弃时在头尾之间插入截断标记。
func (b *capBuffer) String() string {
	var s string
	if b.dropped == 0 {
		s = string(bytes.ToValidUTF8(append(append([]byte(nil), b.head...), b.tail...), []byte("�")))
	} else {
		head := trimIncompleteTail(b.head)
		tail := trimIncompleteHead(b.tail)
		s = fmt.Sprintf("%s\n…[中间已截断 %d 字节]…\n%s", head, b.dropped, tail)
	}
	return normalizeNewlines(s)
}

// trimIncompleteTail 去掉末尾被切断的多字节字符。
func trimIncompleteTail(p []byte) []byte {
	for i := 0; i < utf8.UTFMax && i < len(p); i++ {
		idx := len(p) - 1 - i
		if utf8.RuneStart(p[idx]) {
			if !utf8.FullRune(p[idx:]) {
				return p[:idx]
			}
			break
		}
	}
	return p
}

// trimIncompleteHead 去掉开头被切断的多字节字符残片（孤立的续字节）。
func trimIncompleteHead(p []byte) []byte {
	for len(p) > 0 && !utf8.RuneStart(p[0]) {
		p = p[1:]
	}
	return p
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}
