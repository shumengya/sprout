package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

func visionAgent(t *testing.T, p Provider, vision bool) *Agent {
	t.Helper()
	a, err := New(Config{Provider: p, Model: "m", Vision: vision})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestVisionModelReceivesImagesAsIs(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("看到了")}}
	a := visionAgent(t, p, true)
	b64 := pngB64(t, 4, 4)
	if err := a.Run(context.Background(), Input{Text: "看图", Images: []Image{{MediaType: "image/png", Data: b64}}}, &collector{}); err != nil {
		t.Fatal(err)
	}
	sent := p.reqs[0].Messages[0]
	if !sent.HasImages() || sent.Content[1].Data != b64 {
		t.Fatalf("支持视觉的模型应收到原图: %+v", sent.Content)
	}
}

func TestNonVisionModelGetsPlaceholderButHistoryKeepsImage(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("收到")}}
	a := visionAgent(t, p, false)
	if err := a.Run(context.Background(), Input{Text: "看图", Images: []Image{{MediaType: "image/png", Data: pngB64(t, 4, 4)}}}, &collector{}); err != nil {
		t.Fatal(err)
	}
	sent := p.reqs[0].Messages[0]
	if sent.HasImages() {
		t.Fatal("不支持视觉的模型不应收到图片，否则整个请求会被服务端拒绝")
	}
	if !strings.Contains(sent.Text(), "图片已省略") {
		t.Errorf("应带占位说明，让模型知道用户发过图片: %q", sent.Text())
	}
	// 降级只作用于发出去的请求：历史保留原图，之后换到视觉模型仍能看到。
	if !a.History()[0].HasImages() {
		t.Error("历史里的图片不应被降级改掉")
	}
}

func TestTurnStartedCarriesImageInfo(t *testing.T) {
	p := &scripted{steps: []scriptStep{say("好")}}
	a := visionAgent(t, p, true)
	b64 := pngB64(t, 4, 4)
	var c collector
	if err := a.Run(context.Background(), Input{Text: "看", Images: []Image{{Name: "a.png", MediaType: "image/png", Data: b64}}}, &c); err != nil {
		t.Fatal(err)
	}
	ts, ok := c.events[0].(event.TurnStarted)
	if !ok || len(ts.Images) != 1 {
		t.Fatalf("TurnStarted 应带图片: %+v", c.events[0])
	}
	if ts.Images[0].MediaType != "image/png" || ts.Images[0].Data != b64 || ts.Images[0].Bytes <= 0 {
		t.Errorf("图片信息不对: %+v", ts.Images[0])
	}
}
