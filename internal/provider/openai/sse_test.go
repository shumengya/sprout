package openai

import (
	"io"
	"strings"
	"testing"
)

func readAll(t *testing.T, input string) []sseEvent {
	t.Helper()
	r := newSSEReader(strings.NewReader(input))
	var out []sseEvent
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
}

func TestSSEBasicAndCRLF(t *testing.T) {
	for _, nl := range []string{"\n", "\r\n"} {
		in := "data: a" + nl + nl + "data: b" + nl + nl
		evs := readAll(t, in)
		if len(evs) != 2 || evs[0].Data != "a" || evs[1].Data != "b" {
			t.Fatalf("换行 %q 解析得到 %+v", nl, evs)
		}
	}
}

func TestSSEEventNameCommentsAndMultilineData(t *testing.T) {
	in := ": 心跳\n" +
		"event: response.output_text.delta\n" +
		"data: {\"a\":\n" +
		"data: 1}\n\n" +
		": 又一个心跳\n\n" +
		"data:无空格\n\n"
	evs := readAll(t, in)
	if len(evs) != 2 {
		t.Fatalf("得到 %+v", evs)
	}
	if evs[0].Name != "response.output_text.delta" || evs[0].Data != "{\"a\":\n1}" {
		t.Errorf("多行 data 应以换行连接: %+v", evs[0])
	}
	if evs[1].Data != "无空格" || evs[1].Name != "" {
		t.Errorf("event 名不应泄漏到下一个事件，冒号后没有空格也应可解析: %+v", evs[1])
	}
}

func TestSSEFinalEventWithoutTrailingBlankLine(t *testing.T) {
	evs := readAll(t, "data: a\n\ndata: last")
	if len(evs) != 2 || evs[1].Data != "last" {
		t.Fatalf("流结束时已累积的 data 应作为最后一个事件返回: %+v", evs)
	}
}

func TestSSEIgnoresUnknownFieldsAndStrayBlankLines(t *testing.T) {
	evs := readAll(t, "\n\nid: 7\nretry: 100\ndata: x\n\n\n\n")
	if len(evs) != 1 || evs[0].Data != "x" {
		t.Fatalf("得到 %+v", evs)
	}
}

func TestSSEHugeLine(t *testing.T) {
	// 超过 bufio.Scanner 默认 64KB 单行上限；函数参数可能很长。
	big := strings.Repeat("x", 1<<20)
	evs := readAll(t, "data: "+big+"\n\n")
	if len(evs) != 1 || len(evs[0].Data) != 1<<20 {
		t.Fatalf("1MB 的单行 data 解析失败，得到 %d 个事件", len(evs))
	}
}

func TestSSEEmptyStream(t *testing.T) {
	if evs := readAll(t, ""); len(evs) != 0 {
		t.Fatalf("得到 %+v", evs)
	}
}
