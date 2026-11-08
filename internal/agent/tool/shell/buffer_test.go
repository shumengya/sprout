package shell

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapBufferUnderLimit(t *testing.T) {
	b := newCapBuffer(100)
	b.Write([]byte("hello\r\nworld"))
	if b.Truncated() {
		t.Fatal("未超限不应标记截断")
	}
	if got := b.String(); got != "hello\nworld" {
		t.Fatalf("got %q，CRLF 应被规整为 LF", got)
	}
}

func TestCapBufferKeepsHeadAndTail(t *testing.T) {
	b := newCapBuffer(20)
	// 分多次写入，覆盖“头部写满后进入尾部”的路径。
	for i := 0; i < 100; i++ {
		b.Write([]byte("0123456789"))
	}
	if !b.Truncated() {
		t.Fatal("应标记截断")
	}
	s := b.String()
	if !strings.HasPrefix(s, "0123456789") || !strings.HasSuffix(s, "0123456789") {
		t.Fatalf("应保留头尾，got %q", s)
	}
	if !strings.Contains(s, "中间已截断 980 字节") {
		t.Fatalf("丢弃字节数不对: %q", s)
	}
	if len(b.head)+len(b.tail) > 20 {
		t.Fatalf("内存中保留量 %d 超过上限", len(b.head)+len(b.tail))
	}
}

func TestCapBufferSingleHugeWrite(t *testing.T) {
	b := newCapBuffer(10)
	n, err := b.Write([]byte(strings.Repeat("a", 1000)))
	if n != 1000 || err != nil {
		t.Fatalf("Write 应返回完整长度，got %d, %v", n, err)
	}
	if !strings.Contains(b.String(), "已截断 990 字节") {
		t.Fatalf("got %q", b.String())
	}
}

func TestCapBufferDoesNotSplitRunes(t *testing.T) {
	// 每个汉字 3 字节；上限 10 字节会切在汉字中间。
	b := newCapBuffer(10)
	b.Write([]byte(strings.Repeat("中", 50)))
	s := b.String()
	if !utf8.ValidString(s) || strings.ContainsRune(s, '�') {
		t.Fatalf("截断处产生了非法 UTF-8: %q", s)
	}
}

func TestCapBufferInvalidUTF8(t *testing.T) {
	b := newCapBuffer(100)
	b.Write([]byte{'a', 0xff, 'b'})
	if got := b.String(); got != "a�b" {
		t.Fatalf("got %q", got)
	}
}
