package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeTemp 在临时目录里造一个文件，内容原样写入。
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadReturnsNumberedLines(t *testing.T) {
	p := writeTemp(t, "a.txt", "第一行\nsecond\n第三行\n")
	r := NewRead(filepath.Dir(p))
	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d", res.ExitCode)
	}
	want := "1\t第一行\n2\tsecond\n3\t第三行\n"
	if !strings.HasSuffix(res.Stdout, want) {
		t.Errorf("Stdout = %q，应以 %q 结尾", res.Stdout, want)
	}
	if !strings.Contains(res.Stdout, "共 3 行") {
		t.Errorf("缺少行数摘要: %q", res.Stdout)
	}
}

func TestReadOffsetAndLimit(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		b.WriteString(strings.Repeat("x", i))
		b.WriteByte('\n')
	}
	p := writeTemp(t, "lines.txt", b.String())
	r := NewRead(filepath.Dir(p))

	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"lines.txt","offset":3,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "第 3–4 行") {
		t.Errorf("摘要不对: %q", res.Stdout)
	}
	body := res.Stdout[strings.Index(res.Stdout, "\n")+1:]
	if !strings.HasPrefix(body, " 3\txxx\n 4\txxxx\n") {
		t.Errorf("正文 = %q", body)
	}
	if strings.Contains(body, " 5\t") {
		t.Error("limit=2 不应返回第 5 行")
	}
}

func TestReadOffsetOutOfRange(t *testing.T) {
	p := writeTemp(t, "short.txt", "one\n")
	r := NewRead(filepath.Dir(p))
	if _, err := r.Run(context.Background(), json.RawMessage(`{"path":"short.txt","offset":5}`)); err == nil {
		t.Fatal("offset 超出应报错")
	}
}

func TestReadRejectsBinaryAndDirectories(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "x.bin")
	if err := os.WriteFile(binPath, []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRead(dir)
	if _, err := r.Run(context.Background(), json.RawMessage(`{"path":"x.bin"}`)); err == nil || !strings.Contains(err.Error(), "二进制") {
		t.Errorf("二进制文件应被拒绝，得到 %v", err)
	}
	if _, err := r.Run(context.Background(), json.RawMessage(`{"path":"."}`)); err == nil || !strings.Contains(err.Error(), "目录") {
		t.Errorf("目录应被拒绝，得到 %v", err)
	}
}

func TestReadAbsolutePathAndHome(t *testing.T) {
	p := writeTemp(t, "abs.txt", "hello\n")
	r := NewRead(t.TempDir())
	raw, _ := json.Marshal(map[string]string{"path": p})
	if _, err := r.Run(context.Background(), raw); err != nil {
		t.Errorf("绝对路径应可读: %v", err)
	}
	if _, err := r.Run(context.Background(), json.RawMessage(`{"path":"~/x.txt"}`)); err == nil {
		t.Error("~ 开头应被拒绝并提示用 shell 工具")
	}
}

func TestReadTruncatesByBytes(t *testing.T) {
	line := strings.Repeat("y", 1000) + "\n"
	p := writeTemp(t, "big.txt", strings.Repeat(line, 100)) // 100KB
	r := NewRead(filepath.Dir(p))
	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) > maxReadOutput+len(line)+200 {
		t.Errorf("输出 %d 字节，超过上限", len(res.Stdout))
	}
	if !strings.Contains(res.Stdout, "被截断") || !strings.Contains(res.Stdout, "offset=") {
		t.Errorf("应提示截断与续读方法: %q", res.Stdout[:120])
	}
}

func TestWriteCreatesFileAndParents(t *testing.T) {
	dir := t.TempDir()
	w := NewWrite(dir)
	res, err := w.Run(context.Background(), json.RawMessage(`{"path":"a/b/c.txt","content":"你好\nworld\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "新建") {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a", "b", "c.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "你好\nworld\n" {
		t.Errorf("内容 = %q", got)
	}
}

func TestWriteOverwritesAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewWrite(dir)
	res, err := w.Run(context.Background(), json.RawMessage(`{"path":"keep.txt","content":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "覆盖") {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "new" {
		t.Errorf("内容 = %q", got)
	}
	info, _ := os.Stat(p)
	// Windows 只有只读位，chmod 0600 会读回 0666；权限沿用的语义只在 Unix 上可验证。
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("权限应沿用 0600，实际 %o", info.Mode().Perm())
	}
}

func TestWriteRejectsDirectoryAndEmptyPath(t *testing.T) {
	dir := t.TempDir()
	w := NewWrite(dir)
	if _, err := w.Run(context.Background(), json.RawMessage(`{"path":".","content":"x"}`)); err == nil {
		t.Error("目标是目录应失败")
	}
	if _, err := w.Run(context.Background(), json.RawMessage(`{"path":"","content":"x"}`)); err == nil {
		t.Error("空路径应失败")
	}
}

func TestWriteLeavesNoTempFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	w := NewWrite(dir)
	if _, err := w.Run(context.Background(), json.RawMessage(`{"path":"f.txt","content":"x"}`)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".sprout-write-") {
			t.Errorf("遗留临时文件 %s", e.Name())
		}
	}
}

func TestToolsImplementToolContract(t *testing.T) {
	for _, tl := range []interface {
		Name() string
		Description() string
		Parameters() json.RawMessage
	}{NewRead("."), NewWrite("."), NewEdit(".")} {
		if tl.Name() == "" || tl.Description() == "" {
			t.Errorf("工具 %T 的描述不完整", tl)
		}
		var schema map[string]any
		if err := json.Unmarshal(tl.Parameters(), &schema); err != nil {
			t.Errorf("%s 的参数不是合法 JSON Schema: %v", tl.Name(), err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s 的 schema type = %v", tl.Name(), schema["type"])
		}
	}
}

func TestEditReplacesSingleMatch(t *testing.T) {
	p := writeTemp(t, "a.go", "package a\n\nfunc Foo() {}\n")
	e := NewEdit(filepath.Dir(p))
	res, err := e.Run(context.Background(), json.RawMessage(`{"path":"a.go","old_string":"func Foo() {}","new_string":"func Foo() int { return 1 }"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "替换 1 处") || !strings.Contains(res.Stdout, "第 3 行") {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "package a\n\nfunc Foo() int { return 1 }\n" {
		t.Errorf("内容 = %q", got)
	}
}

func TestEditRejectsAmbiguousMatchUnlessReplaceAll(t *testing.T) {
	p := writeTemp(t, "dup.txt", "x = 1\ny = 2\nx = 1\n")
	e := NewEdit(filepath.Dir(p))
	raw := json.RawMessage(`{"path":"dup.txt","old_string":"x = 1","new_string":"x = 9"}`)
	_, err := e.Run(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "出现了 2 处") {
		t.Fatalf("重复匹配应报错并说明处数，得到 %v", err)
	}
	got, _ := os.ReadFile(p)
	if strings.Contains(string(got), "x = 9") {
		t.Error("报错时不应改动文件")
	}

	res, err := e.Run(context.Background(), json.RawMessage(`{"path":"dup.txt","old_string":"x = 1","new_string":"x = 9","replace_all":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "替换 2 处") {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	got, _ = os.ReadFile(p)
	if string(got) != "x = 9\ny = 2\nx = 9\n" {
		t.Errorf("内容 = %q", got)
	}
}

func TestEditReportsMissingMatch(t *testing.T) {
	p := writeTemp(t, "a.txt", "hello\n")
	e := NewEdit(filepath.Dir(p))
	_, err := e.Run(context.Background(), json.RawMessage(`{"path":"a.txt","old_string":"nope","new_string":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "找不到 old_string") {
		t.Fatalf("应提示找不到原文，得到 %v", err)
	}
}

func TestEditRejectsBadArguments(t *testing.T) {
	p := writeTemp(t, "a.txt", "hello\n")
	e := NewEdit(filepath.Dir(p))
	cases := map[string]string{
		`{"path":"a.txt","old_string":"","new_string":"x"}`:          "不能为空",
		`{"path":"a.txt","old_string":"hello","new_string":"hello"}`: "没有要改的内容",
		`{"path":"missing.txt","old_string":"a","new_string":"b"}`:   "无法访问",
		`{"path":".","old_string":"a","new_string":"b"}`:             "目录",
	}
	for raw, want := range cases {
		if _, err := e.Run(context.Background(), json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s 应报错含 %q，得到 %v", raw, want, err)
		}
	}
}

func TestEditKeepsCRLF(t *testing.T) {
	p := writeTemp(t, "win.txt", "first\r\nsecond\r\n")
	e := NewEdit(filepath.Dir(p))
	// 模型从 read 的输出里抄片段时拿不到 \r，这里按 LF 提交也要能匹配上。
	_, err := e.Run(context.Background(), json.RawMessage(`{"path":"win.txt","old_string":"first\nsecond","new_string":"first\nchanged\nsecond"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "first\r\nchanged\r\nsecond\r\n" {
		t.Errorf("内容 = %q，换行风格应保持 CRLF", got)
	}
}

func TestEditRejectsBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.bin"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEdit(dir)
	if _, err := e.Run(context.Background(), json.RawMessage(`{"path":"x.bin","old_string":"a","new_string":"c"}`)); err == nil || !strings.Contains(err.Error(), "二进制") {
		t.Errorf("二进制文件应被拒绝，得到 %v", err)
	}
}
