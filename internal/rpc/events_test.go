package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shumengya/sprout/internal/agent/event"
)

func TestToolFinishedWireCarriesImageMetaWithoutData(t *testing.T) {
	m := wireEvent(1, event.ToolFinished{ID: "c1", Name: "shot", Images: []event.ImageInfo{
		{MediaType: "image/png", Bytes: 12, Data: "QUJD"},
	}})
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"images":[{"name":"","mediaType":"image/png","bytes":12}]`) || strings.Contains(string(b), "QUJD") {
		t.Errorf("tool.finished 的 images 应只有元数据: %s", b)
	}

	b, _ = json.Marshal(wireEvent(1, event.ToolFinished{ID: "c2"}))
	if !strings.Contains(string(b), `"images":[]`) {
		t.Errorf("没有图片时应是空数组: %s", b)
	}
}
