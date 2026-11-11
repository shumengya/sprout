package main

import (
	"errors"
	"testing"
)

func TestParseArgsCollectsImages(t *testing.T) {
	a, err := parseArgs([]string{"run", "--image", "a.png", "--image=b.jpg", "看图"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.images) != 2 || a.images[0] != "a.png" || a.images[1] != "b.jpg" {
		t.Fatalf("images = %v", a.images)
	}
	if a.runPrompt == nil || *a.runPrompt != "看图" {
		t.Fatalf("runPrompt = %v", a.runPrompt)
	}
}

func TestParseArgsRejectsEmptyImage(t *testing.T) {
	if _, err := parseArgs([]string{"run", "--image", "", "q"}); !errors.Is(err, errUsage) {
		t.Fatalf("空图片路径应报用法错误，得到 %v", err)
	}
}
