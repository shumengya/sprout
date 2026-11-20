package platform

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestParseURIList(t *testing.T) {
	got := parseURIList("# 注释\r\nfile:///home/u/a%20b.png\r\nhttps://example.com/x.png\r\n\r\nfile:///tmp/c.jpg\n")
	want := []string{filepath.FromSlash("/home/u/a b.png"), filepath.FromSlash("/tmp/c.jpg")}
	if !slices.Equal(got, want) {
		t.Errorf("得到 %q，期望 %q", got, want)
	}
}
